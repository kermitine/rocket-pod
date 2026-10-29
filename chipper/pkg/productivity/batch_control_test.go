package productivity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"google.golang.org/grpc"
)

func TestReminderBatchControlSurvivesCompletedTaskAndResetsForNewTarget(t *testing.T) {
	client := &sportsMuteRobotClient{}
	robot := &vector.Vector{Conn: client}
	control := &reminderBatchControl{}
	defer control.close()
	for index, task := range []Task{
		{RobotESN: "robot-a", configurationGeneration: 1},
		{RobotESN: "ROBOT-A", configurationGeneration: 1},
		{RobotESN: "robot-b", configurationGeneration: 1},
		{RobotESN: "robot-b", configurationGeneration: 2},
	} {
		taskCtx, cancel := context.WithCancel(context.Background())
		stopWatching, err := control.acquire(taskCtx, robot, task)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		stopWatching()
		cancel()
		if control.ctx.Err() != nil || !control.held {
			t.Fatal("completing a task canceled the control session for the remaining batch")
		}
		wantSessions := index
		if index == 0 {
			wantSessions = 1
		}
		if len(client.controls) != wantSessions {
			t.Fatalf("task %d opened %d sessions, want %d", index, len(client.controls), wantSessions)
		}
		for _, previous := range client.controls[:len(client.controls)-1] {
			if !previous.released || previous.failed || previous.ctx.Err() == nil {
				t.Fatal("changing the robot or configuration did not cleanly release the previous session")
			}
		}
	}
	control.close()
	if last := client.controls[len(client.controls)-1]; !last.released || last.failed || last.ctx.Err() == nil {
		t.Fatal("ending the batch did not release and close the final session")
	}
}

type pendingBatchControlStream struct {
	vectorpb.ExternalInterface_BehaviorControlClient
	ctx       context.Context
	requested chan struct{}
}

func (stream *pendingBatchControlStream) Send(request *vectorpb.BehaviorControlRequest) error {
	if request.GetControlRequest() != nil {
		close(stream.requested)
	}
	return nil
}

func (stream *pendingBatchControlStream) Recv() (*vectorpb.BehaviorControlResponse, error) {
	<-stream.ctx.Done()
	return nil, stream.ctx.Err()
}

type pendingBatchControlClient struct {
	vectorpb.ExternalInterfaceClient
	requested chan struct{}
}

func (client *pendingBatchControlClient) BehaviorControl(ctx context.Context, _ ...grpc.CallOption) (vectorpb.ExternalInterface_BehaviorControlClient, error) {
	return &pendingBatchControlStream{ctx: ctx, requested: client.requested}, nil
}

func TestReminderBatchControlAcquisitionHonorsTaskCancellation(t *testing.T) {
	client := &pendingBatchControlClient{requested: make(chan struct{})}
	control := &reminderBatchControl{}
	defer control.close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		stopWatching, err := control.acquire(ctx, &vector.Vector{Conn: client}, Task{RobotESN: "robot"})
		if stopWatching != nil {
			stopWatching()
		}
		done <- err
	}()
	select {
	case <-client.requested:
	case <-time.After(time.Second):
		t.Fatal("control was not requested")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || control.stream != nil {
			t.Fatalf("canceled acquisition returned %v and left stream %v", err, control.stream)
		}
	case <-time.After(time.Second):
		t.Fatal("task cancellation left control acquisition blocked")
	}
}
