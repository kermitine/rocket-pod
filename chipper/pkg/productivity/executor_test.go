package productivity

import (
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestReminderBatteryAllowsDelivery(t *testing.T) {
	tests := []struct {
		name  string
		level vectorpb.BatteryLevel
		want  bool
	}{
		{name: "unknown", level: vectorpb.BatteryLevel_BATTERY_LEVEL_UNKNOWN, want: false},
		{name: "low", level: vectorpb.BatteryLevel_BATTERY_LEVEL_LOW, want: false},
		{name: "nominal", level: vectorpb.BatteryLevel_BATTERY_LEVEL_NOMINAL, want: true},
		{name: "full", level: vectorpb.BatteryLevel_BATTERY_LEVEL_FULL, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reminderBatteryAllowsDelivery(tt.level); got != tt.want {
				t.Fatalf("reminderBatteryAllowsDelivery(%v) = %v, want %v", tt.level, got, tt.want)
			}
		})
	}
}

func TestDeferredReminderDoesNotExpireAtRetryLimit(t *testing.T) {
	task := Task{
		ID:                      "offline-reminder",
		RetryCount:              4,
		configurationGeneration: currentConfigurationGeneration(),
	}
	deferTask(task, "robot is offline", time.Millisecond)

	select {
	case got := <-taskQueue:
		if got.ID != task.ID || got.RetryCount != task.RetryCount {
			t.Fatalf("deferred task = %#v, want %#v", got, task)
		}
	case <-time.After(time.Second):
		t.Fatal("deferred reminder was not requeued")
	}
}

func TestTaskExpiresAtEndOfScheduledDay(t *testing.T) {
	expiresAt := time.Date(2026, time.June, 23, 0, 0, 0, 0, time.UTC)
	task := Task{ExpiresAt: expiresAt}

	if taskIsExpiredAt(task, expiresAt.Add(-time.Nanosecond)) {
		t.Fatal("task expired before the end of its scheduled day")
	}
	if !taskIsExpiredAt(task, expiresAt) {
		t.Fatal("task did not expire at the end of its scheduled day")
	}
	if taskIsExpiredAt(Task{}, expiresAt.Add(24*time.Hour)) {
		t.Fatal("task without an expiration unexpectedly expired")
	}
}

func TestSnoozeWaitIsCappedAtEndOfDay(t *testing.T) {
	now := time.Date(2026, time.June, 22, 23, 55, 0, 0, time.UTC)
	task := Task{ExpiresAt: now.Add(5 * time.Minute)}

	delay, ok := taskRequeueDelay(task, now, 10*time.Minute)
	if !ok || delay != 5*time.Minute {
		t.Fatalf("taskRequeueDelay() = (%v, %v), want (5m, true)", delay, ok)
	}
	if _, ok := taskRequeueDelay(task, task.ExpiresAt, time.Minute); ok {
		t.Fatal("expired task was allowed to requeue")
	}
}

func TestWaitForReminderImageHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if waitForReminderImage(ctx) {
		t.Fatal("waitForReminderImage() = true for canceled context")
	}
}

func TestEstimatedReminderSpeechDuration(t *testing.T) {
	short := estimatedReminderSpeechDuration("Final score")
	long := estimatedReminderSpeechDuration("Top performer with twenty points ten rebounds and twelve assists")
	if short <= 0 || long <= short {
		t.Fatalf("speech duration estimates short=%v long=%v", short, long)
	}
}

func TestSpeechWithoutDiacritics(t *testing.T) {
	input := "Nico Hülkenberg, Sergio Pérez, São Paulo, and Jose\u0301"
	want := "Nico Hulkenberg, Sergio Perez, Sao Paulo, and Jose"
	if got := speechWithoutDiacritics(input); got != want {
		t.Fatalf("speechWithoutDiacritics() = %q, want %q", got, want)
	}
}

func TestAcknowledgementAnimationRequestUsesOnlyFaceTrack(t *testing.T) {
	request := acknowledgementAnimationRequest("anim_knowledgegraph_success_01")
	if request.GetAnimation().GetName() != "anim_knowledgegraph_success_01" || request.GetLoops() != 1 {
		t.Fatalf("acknowledgement request has wrong animation: %#v", request)
	}
	if !request.GetIgnoreBodyTrack() || !request.GetIgnoreHeadTrack() || !request.GetIgnoreLiftTrack() {
		t.Fatalf("acknowledgement request can be blocked by physical tracks: %#v", request)
	}
}

func TestReminderFaceSearchAnimationRequestUsesOnlyFaceTrack(t *testing.T) {
	request := reminderFaceSearchAnimationRequest()
	if request.GetAnimation().GetName() != reminderFaceSearchAnimation || request.GetLoops() != 1 {
		t.Fatalf("face-search request has wrong animation: %#v", request)
	}
	if !request.GetIgnoreBodyTrack() || !request.GetIgnoreHeadTrack() || !request.GetIgnoreLiftTrack() {
		t.Fatalf("face-search request can block physical scan tracks: %#v", request)
	}
}

func TestStandingsPageDurationCoversLongSpeech(t *testing.T) {
	longSpeech := strings.TrimSpace(strings.Repeat("standing ", 60))
	if got := estimatedReminderPageDuration(longSpeech); got != 35*time.Second {
		t.Fatalf("estimatedReminderPageDuration() = %v, want 35s", got)
	}
	task := Task{Pages: []TaskPage{{Speech: longSpeech}, {Speech: longSpeech}, {Speech: longSpeech}}}
	if got := reminderTaskTimeout(task); got <= reminderDefaultTaskTimeout {
		t.Fatalf("reminderTaskTimeout() = %v, want more than %v", got, reminderDefaultTaskTimeout)
	}
}

func TestReminderTaskTimeoutIncludesWorkflowAndConfirmationMargin(t *testing.T) {
	task := Task{RequireConfirmation: true}
	minimum := reminderFaceSearchTimeout + reminderWorkflowOverhead + reminderConfirmationTimeout
	if got := reminderTaskTimeout(task); got < minimum {
		t.Fatalf("reminderTaskTimeout() = %v, want at least %v", got, minimum)
	}
	if reminderTaskTimeout(Task{}) < reminderDefaultTaskTimeout {
		t.Fatal("ordinary reminder timeout is shorter than the default safety budget")
	}
}

func TestFaceObservationDoesNotRequireMappedPose(t *testing.T) {
	observations := &faceSearchObservations{}
	observations.observe(&vectorpb.Event{
		EventType: &vectorpb.Event_RobotObservedFace{
			RobotObservedFace: &vectorpb.RobotObservedFace{FaceId: -7},
		},
	})

	faceID, found := observations.face()
	if !found || faceID != -7 {
		t.Fatalf("face() = (%d, %v), want (-7, true)", faceID, found)
	}
}

func TestFaceScanCoversFourHundredSixtyDegrees(t *testing.T) {
	totalAngle := reminderFaceScanStepAngle * reminderFaceScanMaxSteps
	wantAngle := 460 * math.Pi / 180
	if math.Abs(totalAngle-wantAngle) > 0.000001 {
		t.Fatalf("face scan angle = %v, want 460 degrees", totalAngle)
	}
}

func TestReminderHeadAngleIsRaisedForScanningAndViewing(t *testing.T) {
	request := reminderHeadAngleRequest()
	wantAngle := float32(20 * math.Pi / 180)
	if math.Abs(float64(request.AngleRad-wantAngle)) > 0.000001 {
		t.Fatalf("head angle = %v, want %v", request.AngleRad, wantAngle)
	}
	if request.MaxSpeedRadPerSec <= 0 || request.AccelRadPerSec2 <= 0 || request.IdTag != reminderHeadActionTag {
		t.Fatalf("head angle request is incomplete: %#v", request)
	}
}

func TestReminderLiftIsLoweredForScanning(t *testing.T) {
	request := reminderLiftHeightRequest()
	if request.GetHeightMm() != reminderLowestLiftHeightMM {
		t.Fatalf("lift height = %v, want %v", request.GetHeightMm(), reminderLowestLiftHeightMM)
	}
	if request.GetMaxSpeedRadPerSec() <= 0 || request.GetAccelRadPerSec2() <= 0 || request.GetIdTag() != reminderLiftActionTag {
		t.Fatalf("lift request is incomplete: %#v", request)
	}
}

func TestReminderDriveRequestIsShortAndBounded(t *testing.T) {
	request := reminderDriveRequest()
	if request.DistMm <= 0 || request.DistMm > 100 {
		t.Fatalf("approach distance = %v, want 1..100 mm", request.DistMm)
	}
	if request.SpeedMmps <= 0 || request.SpeedMmps > 40 {
		t.Fatalf("approach speed = %v, want 1..40 mm/s", request.SpeedMmps)
	}
	if request.IdTag != reminderApproachActionTag || request.NumRetries != 0 || request.ShouldPlayAnimation {
		t.Fatalf("approach request is not safely bounded: %#v", request)
	}
}

func TestReminderDriveStopHasIndependentRetryAndVerificationBudgets(t *testing.T) {
	if reminderDriveStopAttempts < 2 {
		t.Fatalf("motor stop attempts = %d, want at least 2", reminderDriveStopAttempts)
	}
	if reminderDriveStopTimeout <= 2*time.Second {
		t.Fatalf("motor stop timeout = %v, want more than 2s", reminderDriveStopTimeout)
	}
	if reminderDriveVerifyTimeout <= reminderDriveStopTimeout {
		t.Fatalf("wheel verification timeout = %v, want more than stop timeout %v", reminderDriveVerifyTimeout, reminderDriveStopTimeout)
	}
}

func TestReminderRobotStateCliffAndStoppedChecks(t *testing.T) {
	stopped := &vectorpb.RobotState{}
	if !reminderRobotStateStopped(stopped) {
		t.Fatal("stationary robot state was not recognized as stopped")
	}
	moving := &vectorpb.RobotState{Status: uint32(vectorpb.RobotStatus_ROBOT_STATUS_ARE_WHEELS_MOVING), LeftWheelSpeedMmps: 20, RightWheelSpeedMmps: 20}
	if reminderRobotStateStopped(moving) {
		t.Fatal("moving robot state was recognized as stopped")
	}
	cliff := &vectorpb.RobotState{Status: uint32(vectorpb.RobotStatus_ROBOT_STATUS_CLIFF_DETECTED)}
	if !reminderRobotStateHas(cliff, vectorpb.RobotStatus_ROBOT_STATUS_CLIFF_DETECTED) {
		t.Fatal("cliff status was not recognized")
	}
}

func TestConvertImageToVectorFaceDataPacking(t *testing.T) {
	tests := []struct {
		name      string
		color     color.Color
		wantPixel [2]byte
	}{
		{name: "red", color: color.RGBA{R: 255, A: 255}, wantPixel: [2]byte{0xF8, 0x00}},
		{name: "green", color: color.RGBA{G: 255, A: 255}, wantPixel: [2]byte{0x07, 0xE0}},
		{name: "blue", color: color.RGBA{B: 255, A: 255}, wantPixel: [2]byte{0x00, 0x1F}},
		{name: "white", color: color.White, wantPixel: [2]byte{0xFF, 0xFF}},
		{name: "transparent", color: color.NRGBA{R: 255, G: 255, B: 255, A: 0}, wantPixel: [2]byte{0x00, 0x00}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img := image.NewRGBA(image.Rect(0, 0, 1, 1))
			img.Set(0, 0, tt.color)
			data := convertImageToVectorFaceData(img)
			if data[0] != tt.wantPixel[0] || data[1] != tt.wantPixel[1] {
				t.Fatalf("packed pixel = [%02x %02x], want [%02x %02x]", data[0], data[1], tt.wantPixel[0], tt.wantPixel[1])
			}
		})
	}
}
