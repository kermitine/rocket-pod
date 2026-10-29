package productivity

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"google.golang.org/grpc"
)

func TestReminderBatchReusesOnlySuccessfulFaceSearches(t *testing.T) {
	for _, test := range []struct {
		name    string
		robots  []string
		results []bool
		want    []bool
	}{
		{"found immediately", []string{"a", "a", "a"}, []bool{true, true, true}, []bool{false, true, true}},
		{"finds face later", []string{"a", "a", "a", "a"}, []bool{false, false, true, true}, []bool{false, false, false, true}},
		{"never finds face", []string{"a", "a", "a"}, []bool{false, false, false}, []bool{false, false, false}},
		{"delivery interrupted", []string{"a", "a", "a", "a"}, []bool{true, false, true, true}, []bool{false, true, false, true}},
		{"different robots", []string{"a", "a", "b", "b", "a"}, []bool{true, true, true, true, true}, []bool{false, true, false, true, false}},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := make(chan Task, len(test.robots))
			for index, robot := range test.robots {
				queue <- Task{ID: strconv.Itoa(index), RobotESN: robot}
			}
			close(queue)
			var got []bool
			processReminderBatch(<-queue, queue, func(task Task, reuse bool, _ *reminderBatchControl) bool {
				index := len(got)
				if task.ID != strconv.Itoa(index) {
					t.Fatalf("task %s was played out of order", task.ID)
				}
				got = append(got, reuse)
				return test.results[index]
			})
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("reused face = %v, want %v", got, test.want)
			}
		})
	}
}

func TestReminderBatchResetsAfterEmptyQueueOrConfigurationChange(t *testing.T) {
	queue := make(chan Task, 2)
	first := Task{RobotESN: "robot", configurationGeneration: 1}
	queue <- first
	queue <- Task{RobotESN: "robot", configurationGeneration: 2}
	var got []bool
	process := func(task Task, reuse bool, _ *reminderBatchControl) bool {
		got = append(got, reuse)
		return true
	}
	processReminderBatch(first, queue, process)
	// This notification arrives after the previous batch has drained.
	processReminderBatch(first, queue, process)
	if want := []bool{false, true, false, false}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reused face = %v, want %v", got, want)
	}
}

type batchRobotClient struct {
	vectorpb.ExternalInterfaceClient
	faceResults  []bool
	searches     int
	faceStream   bool
	turns        int
	batteryCalls int
	speech       []string
	failSpeechAt int
	headAngles   []float32
}

func (client *batchRobotClient) BatteryState(context.Context, *vectorpb.BatteryStateRequest, ...grpc.CallOption) (*vectorpb.BatteryStateResponse, error) {
	client.batteryCalls++
	return &vectorpb.BatteryStateResponse{BatteryLevel: vectorpb.BatteryLevel_BATTERY_LEVEL_NOMINAL, BatteryVolts: 4}, nil
}

func (*batchRobotClient) BehaviorControl(context.Context, ...grpc.CallOption) (vectorpb.ExternalInterface_BehaviorControlClient, error) {
	return &readinessControlStream{}, nil
}

func (client *batchRobotClient) EnableFaceDetection(context.Context, *vectorpb.EnableFaceDetectionRequest, ...grpc.CallOption) (*vectorpb.EnableFaceDetectionResponse, error) {
	client.searches++
	client.faceStream = true
	return &vectorpb.EnableFaceDetectionResponse{}, nil
}

type batchFaceStream struct {
	vectorpb.ExternalInterface_EventStreamClient
	face bool
}

func (stream *batchFaceStream) Recv() (*vectorpb.EventResponse, error) {
	if !stream.face {
		return nil, io.EOF
	}
	stream.face = false
	return &vectorpb.EventResponse{Event: &vectorpb.Event{EventType: &vectorpb.Event_RobotObservedFace{
		RobotObservedFace: &vectorpb.RobotObservedFace{FaceId: 7},
	}}}, nil
}

func (client *batchRobotClient) EventStream(context.Context, *vectorpb.EventRequest, ...grpc.CallOption) (vectorpb.ExternalInterface_EventStreamClient, error) {
	if !client.faceStream {
		// Keep this test focused on scanning and presentation, without driving.
		return nil, errors.New("approach monitoring unavailable")
	}
	client.faceStream = false
	found := client.searches <= len(client.faceResults) && client.faceResults[client.searches-1]
	return &batchFaceStream{face: found}, nil
}

func (client *batchRobotClient) SetHeadAngle(_ context.Context, request *vectorpb.SetHeadAngleRequest, _ ...grpc.CallOption) (*vectorpb.SetHeadAngleResponse, error) {
	client.headAngles = append(client.headAngles, request.AngleRad)
	return &vectorpb.SetHeadAngleResponse{Result: &vectorpb.ActionResult{Code: vectorpb.ActionResult_ACTION_RESULT_SUCCESS}}, nil
}

func (*batchRobotClient) SetLiftHeight(context.Context, *vectorpb.SetLiftHeightRequest, ...grpc.CallOption) (*vectorpb.SetLiftHeightResponse, error) {
	return &vectorpb.SetLiftHeightResponse{Result: &vectorpb.ActionResult{Code: vectorpb.ActionResult_ACTION_RESULT_SUCCESS}}, nil
}

func (*batchRobotClient) PlayAnimation(context.Context, *vectorpb.PlayAnimationRequest, ...grpc.CallOption) (*vectorpb.PlayAnimationResponse, error) {
	return &vectorpb.PlayAnimationResponse{Result: vectorpb.BehaviorResults_BEHAVIOR_COMPLETE_STATE}, nil
}

func (*batchRobotClient) TurnInPlace(context.Context, *vectorpb.TurnInPlaceRequest, ...grpc.CallOption) (*vectorpb.TurnInPlaceResponse, error) {
	return nil, errors.New("scan ended without a face")
}

func (client *batchRobotClient) TurnTowardsFace(context.Context, *vectorpb.TurnTowardsFaceRequest, ...grpc.CallOption) (*vectorpb.TurnTowardsFaceResponse, error) {
	client.turns++
	return &vectorpb.TurnTowardsFaceResponse{Result: &vectorpb.ActionResult{Code: vectorpb.ActionResult_ACTION_RESULT_SUCCESS}}, nil
}

func (client *batchRobotClient) SayText(_ context.Context, request *vectorpb.SayTextRequest, _ ...grpc.CallOption) (*vectorpb.SayTextResponse, error) {
	client.speech = append(client.speech, request.Text)
	if len(client.speech) == client.failSpeechAt {
		return nil, errors.New("speech interrupted")
	}
	return &vectorpb.SayTextResponse{State: vectorpb.SayTextResponse_FINISHED}, nil
}

func TestReminderBatchDeliverySearchesUntilFaceFound(t *testing.T) {
	for _, test := range []struct {
		name         string
		faces        []bool
		failSpeechAt int
		wantSearches int
		wantTurns    int
	}{
		{"finds first face", []bool{true}, 0, 1, 1},
		{"finds later face", []bool{false, true}, 0, 2, 1},
		{"never finds face", []bool{false, false, false}, 0, 3, 0},
		{"interrupted delivery searches again", []bool{true, true}, 2, 2, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			esn := "batch-" + test.name
			clearReminderReadinessAfterTest(t, esn)
			now := time.Now()
			observeHealthyReminder(&reminderConnections, esn, now.Add(-reminderReconnectGrace), now)
			client := &batchRobotClient{faceResults: test.faces, failSpeechAt: test.failSpeechAt}
			robot := &vector.Vector{Conn: client}
			makeTask := func(id string) Task {
				return Task{ID: id, RobotESN: esn, Phrases: []string{id}, Source: "test", RetryCount: 4, configurationGeneration: currentConfigurationGeneration()}
			}
			queue := make(chan Task, 2)
			queue <- makeTask("second")
			queue <- makeTask("third")
			processReminderBatch(makeTask("first"), queue, func(task Task, reuse bool, control *reminderBatchControl) bool {
				return deliverReminderTaskWithControl(task, robot, reuse, control)
			})
			if want := []string{"first", "second", "third"}; !reflect.DeepEqual(client.speech, want) {
				t.Fatalf("spoken notifications = %v, want %v", client.speech, want)
			}
			if client.searches != test.wantSearches || client.turns != test.wantTurns {
				t.Fatalf("searches=%d, turns=%d; want searches=%d, turns=%d", client.searches, client.turns, test.wantSearches, test.wantTurns)
			}
			if client.batteryCalls < 6 {
				t.Fatalf("batch bypassed per-notification battery checks: %d calls", client.batteryCalls)
			}
		})
	}
}
