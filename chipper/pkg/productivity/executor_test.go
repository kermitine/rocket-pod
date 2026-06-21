package productivity

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

type fakeBehaviorControlStream struct {
	sent      []*vectorpb.BehaviorControlRequest
	responses []*vectorpb.BehaviorControlResponse
}

func (f *fakeBehaviorControlStream) Send(req *vectorpb.BehaviorControlRequest) error {
	f.sent = append(f.sent, req)
	return nil
}

func (f *fakeBehaviorControlStream) Recv() (*vectorpb.BehaviorControlResponse, error) {
	if len(f.responses) == 0 {
		return nil, errors.New("no response")
	}
	resp := f.responses[0]
	f.responses = f.responses[1:]
	return resp, nil
}

func TestAcquireBehaviorControlWaitsForGrant(t *testing.T) {
	stream := &fakeBehaviorControlStream{responses: []*vectorpb.BehaviorControlResponse{
		{ResponseType: &vectorpb.BehaviorControlResponse_KeepAlive{KeepAlive: &vectorpb.KeepAlivePing{}}},
		{ResponseType: &vectorpb.BehaviorControlResponse_ControlGrantedResponse{ControlGrantedResponse: &vectorpb.ControlGrantedResponse{}}},
	}}

	if err := acquireBehaviorControl(stream); err != nil {
		t.Fatalf("acquireBehaviorControl() error = %v", err)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("sent %d requests, want 1", len(stream.sent))
	}
	request := stream.sent[0].GetControlRequest()
	if request == nil || request.Priority != vectorpb.ControlRequest_OVERRIDE_BEHAVIORS {
		t.Fatalf("sent control request = %#v", request)
	}
}

func TestAcquireBehaviorControlRejectsLostControl(t *testing.T) {
	stream := &fakeBehaviorControlStream{responses: []*vectorpb.BehaviorControlResponse{
		{ResponseType: &vectorpb.BehaviorControlResponse_ControlLostEvent{ControlLostEvent: &vectorpb.ControlLostResponse{}}},
	}}

	if err := acquireBehaviorControl(stream); err == nil {
		t.Fatal("acquireBehaviorControl() error = nil, want control-lost error")
	}
}

func TestReleaseBehaviorControl(t *testing.T) {
	stream := &fakeBehaviorControlStream{}
	if err := releaseBehaviorControl(stream); err != nil {
		t.Fatalf("releaseBehaviorControl() error = %v", err)
	}
	if len(stream.sent) != 1 || stream.sent[0].GetControlRelease() == nil {
		t.Fatalf("sent requests = %#v, want one control release", stream.sent)
	}
}

func TestClassifyConfirmationIntent(t *testing.T) {
	tests := []struct {
		name   string
		intent string
		want   confirmationResult
	}{
		{name: "affirmative", intent: `{"intent":"intent_imperative_affirmative"}`, want: confirmationAccepted},
		{name: "global yes", intent: `{"intent":"intent_global_yes"}`, want: confirmationAccepted},
		{name: "negative", intent: `{"intent":"intent_imperative_negative"}`, want: confirmationDeclined},
		{name: "unrelated", intent: `{"intent":"intent_greeting_hello"}`, want: confirmationTimedOut},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyConfirmationIntent(tt.intent); got != tt.want {
				t.Fatalf("classifyConfirmationIntent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchesPartialAffirmativeKeyphrase(t *testing.T) {
	tests := []struct {
		name      string
		voiceText string
		keyphrase string
		wantMatch bool
	}{
		{name: "ok", voiceText: "ok", keyphrase: "ok", wantMatch: true},
		{name: "okay", voiceText: "okay that works", keyphrase: "okay", wantMatch: true},
		{name: "sounds good", voiceText: "that sounds good", keyphrase: "sounds good", wantMatch: true},
		{name: "short word boundary", voiceText: "look over there", keyphrase: "ok", wantMatch: false},
		{name: "negated okay", voiceText: "not okay", keyphrase: "okay", wantMatch: false},
		{name: "negative prefix", voiceText: "no, that sounds good", keyphrase: "sounds good", wantMatch: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesPartialIntentKeyphrase("intent_imperative_affirmative", tt.voiceText, tt.keyphrase)
			if got != tt.wantMatch {
				t.Fatalf("matchesPartialIntentKeyphrase() = %v, want %v", got, tt.wantMatch)
			}
		})
	}
}

func TestNotifyConfigUpdatedInvalidatesExistingTasks(t *testing.T) {
	previousGeneration := currentConfigurationGeneration()
	defer func() {
		atomic.StoreUint64(&configurationGeneration, previousGeneration)
		select {
		case <-schedulerRefresh:
		default:
		}
	}()

	task := Task{configurationGeneration: previousGeneration}
	if !taskIsCurrent(task) {
		t.Fatal("new task should match the current configuration")
	}

	NotifyConfigUpdated()
	if taskIsCurrent(task) {
		t.Fatal("task from the previous configuration was not invalidated")
	}
}

func TestTestReminderDoesNotSnooze(t *testing.T) {
	snoozeTask(Task{
		Source:                  "test",
		configurationGeneration: currentConfigurationGeneration(),
	})

	select {
	case task := <-taskQueue:
		t.Fatalf("test reminder was requeued: %#v", task)
	default:
	}
}

func TestWaitForReminderImageHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if waitForReminderImage(ctx) {
		t.Fatal("waitForReminderImage() = true for canceled context")
	}
}

func TestConvertImageToVectorFaceUsesVector2Dimensions(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})

	path := filepath.Join(t.TempDir(), "red.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := png.Encode(file, img); err != nil {
		file.Close()
		t.Fatalf("png.Encode() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	got, err := convertImageToVectorFace(path)
	if err != nil {
		t.Fatalf("convertImageToVectorFace() error = %v", err)
	}

	wantLen := reminderFaceImageWidth * reminderFaceImageHeight * 2
	if len(got) != wantLen {
		t.Fatalf("convertImageToVectorFace() returned %d bytes, want %d", len(got), wantLen)
	}
	if got[0] != 0xF8 || got[1] != 0x00 {
		t.Fatalf("first RGB565 pixel = %02x%02x, want f800", got[0], got[1])
	}
}
