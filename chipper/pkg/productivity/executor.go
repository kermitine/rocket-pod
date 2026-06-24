package productivity

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	pb "github.com/digital-dream-labs/api/go/chipperpb"
	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/scripting"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/kercre123/wire-pod/chipper/pkg/vtt"
	"golang.org/x/text/unicode/norm"
	"google.golang.org/protobuf/encoding/protojson"
)

type Task struct {
	ID                       string
	RobotESN                 string
	Phrases                  []string
	Image                    string
	FaceData                 []byte
	AdditionalFaceData       [][]byte
	Pages                    []TaskPage
	Source                   string
	RetryCount               int
	RequireConfirmation      bool
	SnoozeMinutes            int
	ExpiresAt                time.Time
	AcknowledgementAnimation string
	AcknowledgementOnly      bool
	configurationGeneration  uint64
}

type TaskPage struct {
	FaceData []byte
	Speech   string
}

type systemIntentResponseStruct struct {
	Status       string `json:"status"`
	ReturnIntent string `json:"returnIntent"`
}

type confirmationResult int

type behaviorControlStream interface {
	Send(*vectorpb.BehaviorControlRequest) error
	Recv() (*vectorpb.BehaviorControlResponse, error)
}

const (
	confirmationTimedOut confirmationResult = iota
	confirmationAccepted
	confirmationDeclined

	reminderImageDisplayDuration = 3 * time.Second
	reminderImageSettleDelay     = 250 * time.Millisecond
	reminderPageMinimumDuration  = 10 * time.Second
	reminderPageSpeechBase       = 5 * time.Second
	reminderPageSpeechPerWord    = 500 * time.Millisecond
	reminderPageMaximumDuration  = 60 * time.Second
	reminderDefaultTaskTimeout   = 3 * time.Minute
	reminderFaceSearchTimeout    = 35 * time.Second
	reminderWorkflowOverhead     = 90 * time.Second
	reminderConfirmationTimeout  = 30 * time.Second
	reminderInitialFaceCheck     = 2 * time.Second
	reminderFaceTurnTimeout      = 6 * time.Second
	reminderHeadMoveTimeout      = 5 * time.Second
	reminderLiftMoveTimeout      = 5 * time.Second
	reminderFaceTurnActionTag    = 2400001
	reminderFaceScanActionTag    = 2400002
	reminderHeadActionTag        = 2400003
	reminderLiftActionTag        = 2400005
	reminderFaceScanStepAngle    = math.Pi / 9
	reminderViewingHeadAngle     = 20 * math.Pi / 180
	reminderLowestLiftHeightMM   = 32
	reminderFaceScanSpeed        = 1.0
	reminderFaceScanPause        = 250 * time.Millisecond
	reminderFaceScanMaxSteps     = 23
	reminderApproachDistanceMM   = 100
	reminderApproachSpeedMMPS    = 35
	reminderApproachTimeout      = 6 * time.Second
	reminderDriveStateWarmup     = 750 * time.Millisecond
	reminderDriveStopTimeout     = 2 * time.Second
	reminderDriveStopSettle      = 200 * time.Millisecond
	reminderApproachActionTag    = 2400004
	reminderAvailabilityTimeout  = 10 * time.Second
	reminderOfflineRetryDelay    = 30 * time.Second
	reminderLowBatteryRetryDelay = 5 * time.Minute
	confirmationSpeechSettle     = time.Second
	confirmationSpeechRetryDelay = 750 * time.Millisecond
	confirmationSpeechAttempts   = 3
	confirmationListenRetryDelay = 1500 * time.Millisecond
	confirmationListenAttempts   = 3
	acknowledgementSettle        = 350 * time.Millisecond
	acknowledgementRetryDelay    = 500 * time.Millisecond
	reminderFaceSearchAnimation  = "anim_knowledgegraph_searching_01"
)

var (
	taskQueue                     = make(chan Task, 10)
	configurationGeneration       uint64
	confirmationListeningSessions = struct {
		sync.Mutex
		byESN map[string]*confirmationListeningSession
	}{byESN: make(map[string]*confirmationListeningSession)}
)

type confirmationListeningSession struct {
	mu           sync.Mutex
	ctx          context.Context
	ip           string
	attempts     int
	retryPending bool
}

func executorLoop() {
	logger.Println("Productivity: executorLoop started")
	for task := range taskQueue {
		logger.Println("Productivity: Processing task for " + task.RobotESN)
		processTask(task)
		time.Sleep(5 * time.Second)
	}
}

func InjectTestTask(task Task) {
	task.configurationGeneration = currentConfigurationGeneration()
	select {
	case taskQueue <- task:
		logger.Println("Productivity: Test task pushed")
	default:
		logger.Println("Productivity: Queue full")
	}
}

func retryTask(task Task, reason string) {
	if !taskCanRun(task) {
		return
	}
	if task.RetryCount >= 4 {
		logger.Println("Productivity: Task failed permanently: " + reason)
		return
	}
	task.RetryCount++
	backoff := math.Pow(2, float64(task.RetryCount))
	requeueTaskAfter(task, time.Duration(backoff)*time.Second)
}

// deferTask keeps a reminder pending for conditions that are expected to
// resolve without consuming its limited delivery retries, such as the robot
// being offline or its battery being low.
func deferTask(task Task, reason string, delay time.Duration) {
	if !taskCanRun(task) {
		return
	}
	logger.Println("Productivity: Deferring task " + task.ID + ": " + reason)
	requeueTaskAfter(task, delay)
}

func requeueTaskAfter(task Task, delay time.Duration) {
	go func() {
		var ok bool
		delay, ok = taskRequeueDelay(task, time.Now(), delay)
		if !ok {
			return
		}

		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C

		for taskCanRun(task) {
			select {
			case taskQueue <- task:
				return
			default:
				time.Sleep(time.Second)
			}
		}
	}()
}

func taskRequeueDelay(task Task, now time.Time, requested time.Duration) (time.Duration, bool) {
	if task.ExpiresAt.IsZero() {
		return requested, true
	}
	remaining := task.ExpiresAt.Sub(now)
	if remaining <= 0 {
		return 0, false
	}
	if requested > remaining {
		return remaining, true
	}
	return requested, true
}

func reminderBatteryAllowsDelivery(level vectorpb.BatteryLevel) bool {
	return level == vectorpb.BatteryLevel_BATTERY_LEVEL_NOMINAL ||
		level == vectorpb.BatteryLevel_BATTERY_LEVEL_FULL
}

func snoozeTask(task Task) {
	if task.Source == "test" {
		logger.Println("Productivity: Test reminders are one-shot and will not be snoozed")
		return
	}
	if !taskCanRun(task) {
		return
	}
	duration := 10 * time.Minute
	if task.SnoozeMinutes > 0 {
		duration = time.Duration(task.SnoozeMinutes) * time.Minute
	}
	logger.Println("Productivity: Snoozing task " + task.ID + " for " + duration.String())
	task.RetryCount = 0
	requeueTaskAfter(task, duration)
}

// NotifyConfigUpdated invalidates work created from an older productivity
// configuration. Sleeping work is discarded before it can reach the robot.
func NotifyConfigUpdated() {
	atomic.AddUint64(&configurationGeneration, 1)
	select {
	case schedulerRefresh <- struct{}{}:
	default:
	}
}

func currentConfigurationGeneration() uint64 {
	return atomic.LoadUint64(&configurationGeneration)
}

func taskIsCurrent(task Task) bool {
	return task.configurationGeneration == currentConfigurationGeneration()
}

func taskIsExpiredAt(task Task, now time.Time) bool {
	return !task.ExpiresAt.IsZero() && !now.Before(task.ExpiresAt)
}

func taskCanRun(task Task) bool {
	return taskIsCurrent(task) && !taskIsExpiredAt(task, time.Now())
}

func getReminderState(id string) (bool, bool) {
	configStr := vars.APIConfig.Productivity.ManualConfig
	if configStr == "" || configStr == "[]" {
		return false, false
	}
	var reminders []ManualReminder
	if err := json.Unmarshal([]byte(configStr), &reminders); err != nil {
		return false, false
	}
	for _, r := range reminders {
		if r.ID == id {
			return true, r.Enabled
		}
	}
	return false, false
}

func processTask(task Task) {
	if taskIsExpiredAt(task, time.Now()) {
		logger.Println("Productivity: Discarding reminder because its scheduled day has ended")
		return
	}
	if !taskIsCurrent(task) {
		logger.Println("Productivity: Discarding task from an outdated configuration")
		return
	}
	if task.ID != "" {
		exists, enabled := getReminderState(task.ID)
		if task.Source == "manual" {
			if !exists || !enabled {
				logger.Println("Productivity: Reminder " + task.ID + " is no longer enabled or exists. Stopping loop.")
				return
			}
		} else if task.Source == "test" {
			if exists && !enabled {
				logger.Println("Productivity: Test Reminder " + task.ID + " was explicitly disabled in config. Stopping loop.")
				return
			}
		}
	}

	robot, err := vars.GetRobot(task.RobotESN)
	if err != nil {
		if task.AcknowledgementOnly {
			return
		}
		deferTask(task, "robot is unavailable", reminderOfflineRetryDelay)
		return
	}

	if !taskCanRun(task) {
		return
	}

	availabilityCtx, availabilityCancel := context.WithTimeout(context.Background(), reminderAvailabilityTimeout)
	battResp, err := robot.Conn.BatteryState(availabilityCtx, &vectorpb.BatteryStateRequest{})
	availabilityCancel()
	if err != nil || battResp == nil {
		if task.AcknowledgementOnly {
			return
		}
		deferTask(task, "robot is offline", reminderOfflineRetryDelay)
		return
	}
	if !taskCanRun(task) {
		return
	}
	batteryLevel := battResp.GetBatteryLevel()
	if !task.AcknowledgementOnly && batteryLevel == vectorpb.BatteryLevel_BATTERY_LEVEL_UNKNOWN {
		deferTask(task, "battery level is unavailable", reminderOfflineRetryDelay)
		return
	}
	if !task.AcknowledgementOnly && !reminderBatteryAllowsDelivery(batteryLevel) {
		deferTask(task, "battery is not above the low level", reminderLowBatteryRetryDelay)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), reminderTaskTimeout(task))
	defer cancel()

	bcClient, err := robot.Conn.BehaviorControl(ctx)
	if err != nil {
		if task.AcknowledgementOnly {
			return
		}
		deferTask(task, "robot became unavailable", reminderOfflineRetryDelay)
		return
	}

	if err := acquireBehaviorControl(bcClient); err != nil {
		if task.AcknowledgementOnly {
			return
		}
		deferTask(task, "robot is not ready for behavior control: "+err.Error(), reminderOfflineRetryDelay)
		return
	}
	controlHeld := true

	defer func() {
		if controlHeld {
			if err := releaseBehaviorControl(bcClient); err != nil {
				logger.Println("Productivity: Failed to release behavior control: " + err.Error())
			}
		}
	}()
	if task.AcknowledgementAnimation != "" {
		if err := playAcknowledgementAnimation(ctx, robot, task.AcknowledgementAnimation); err != nil {
			logger.Println("Productivity: Command acknowledgement failed: " + err.Error())
		}
	}
	if task.AcknowledgementOnly {
		return
	}

	if battResp.IsOnChargerPlatform {
		_, err := robot.Conn.DriveOffCharger(ctx, &vectorpb.DriveOffChargerRequest{})
		if err != nil {
			retryTask(task, "Drive off failed")
			return
		}
		time.Sleep(5 * time.Second)
	}
	if !taskCanRun(task) {
		return
	}
	if !facePersonForReminder(ctx, robot, task.RobotESN) {
		logger.Println("Productivity: Reminder presentation canceled because wheel stop could not be confirmed")
		return
	}
	if !taskCanRun(task) {
		return
	}

	pagesHandled := len(task.Pages) > 0
	if pagesHandled {
		if !processTaskPages(ctx, robot, task.Pages) {
			return
		}
	} else if len(task.FaceData) > 0 {
		if !displayReminderFaceData(ctx, robot, task.FaceData, "Dynamic face image") {
			return
		}
	} else if task.Image != "" {
		fullPath := filepath.Join(ProductivityImgPath, task.Image)
		if _, err := os.Stat(fullPath); err == nil {
			imgData, err := convertImageToVectorFace(fullPath)
			if err != nil {
				logger.Println("Productivity: Face image conversion failed: " + err.Error())
			} else {
				if _, err := robot.Conn.DisplayFaceImageRGB(ctx, &vectorpb.DisplayFaceImageRGBRequest{
					FaceData:         imgData,
					DurationMs:       uint32(reminderImageDisplayDuration / time.Millisecond),
					InterruptRunning: true,
				}); err != nil {
					logger.Println("Productivity: Face image display failed: " + err.Error())
				} else if !waitForReminderImage(ctx) {
					return
				}
			}
		} else {
			logger.Println("Productivity: Face image is unavailable: " + err.Error())
		}
	}
	if !pagesHandled {
		for _, faceData := range task.AdditionalFaceData {
			if len(faceData) > 0 && !displayReminderFaceData(ctx, robot, faceData, "Additional face image") {
				return
			}
		}
	}
	if !taskCanRun(task) {
		return
	}

	if !pagesHandled && len(task.Phrases) > 0 {
		phrase := task.Phrases[rand.Intn(len(task.Phrases))]
		if phrase != "" {
			if _, err := robot.Conn.SayText(ctx, &vectorpb.SayTextRequest{
				Text:           speechWithoutDiacritics(phrase),
				UseVectorVoice: true,
				DurationScalar: 1.0,
			}); err != nil {
				logger.Println("Productivity: Reminder speech failed: " + err.Error())
			}
		}
	}

	if task.RequireConfirmation {
		if !taskCanRun(task) {
			return
		}
		logger.Println("Productivity: Waiting for confirmation response...")
		if err := releaseBehaviorControl(bcClient); err != nil {
			controlHeld = false
			logger.Println("Productivity: Failed to release behavior control for confirmation: " + err.Error())
			snoozeTask(task)
			return
		}
		controlHeld = false

		result, err := waitForConfirmation(ctx, robot, task.RobotESN)
		if err != nil {
			logger.Println("Productivity: Confirmation wait failed: " + err.Error())
		}
		if !taskCanRun(task) {
			return
		}

		// A release followed by another request is supported on the same stream,
		// but no SDK action is safe until VIC explicitly grants control again.
		if err := acquireBehaviorControl(bcClient); err != nil {
			logger.Println("Productivity: Failed to reacquire behavior control: " + err.Error())
			if result != confirmationAccepted {
				snoozeTask(task)
			}
			return
		}
		controlHeld = true

		switch result {
		case confirmationAccepted:
			if err := sayConfirmationResponse(ctx, robot, "Great!"); err != nil {
				logger.Println("Productivity: Confirmation response speech failed: " + err.Error())
			}
			logger.Println("Productivity: Confirmation successful.")
		case confirmationDeclined:
			if err := sayConfirmationResponse(ctx, robot, "Ok, I'll remind you again soon."); err != nil {
				logger.Println("Productivity: Confirmation response speech failed: " + err.Error())
			}
			snoozeTask(task)
		default:
			if err := sayConfirmationResponse(ctx, robot, "I didn't hear anything. I'll remind you later."); err != nil {
				logger.Println("Productivity: Confirmation response speech failed: " + err.Error())
			}
			snoozeTask(task)
		}
	}
}

func processTaskPages(ctx context.Context, robot *vector.Vector, pages []TaskPage) bool {
	for index, page := range pages {
		if len(page.FaceData) > 0 {
			duration := estimatedReminderPageDuration(page.Speech)
			if _, err := robot.Conn.DisplayFaceImageRGB(ctx, &vectorpb.DisplayFaceImageRGBRequest{
				FaceData:         page.FaceData,
				DurationMs:       uint32(duration / time.Millisecond),
				InterruptRunning: true,
			}); err != nil {
				logger.Println(fmt.Sprintf("Productivity: Page %d face image display failed: %v", index+1, err))
			} else if !waitForReminderPageSettle(ctx) {
				return false
			}
		}
		if page.Speech == "" {
			continue
		}
		response, err := robot.Conn.SayText(ctx, &vectorpb.SayTextRequest{
			Text:           speechWithoutDiacritics(page.Speech),
			UseVectorVoice: true,
			DurationScalar: 1.0,
		})
		if err != nil {
			logger.Println(fmt.Sprintf("Productivity: Page %d speech failed: %v", index+1, err))
			continue
		}
		if response == nil || response.GetState() != vectorpb.SayTextResponse_FINISHED {
			state := "missing"
			if response != nil {
				state = response.GetState().String()
			}
			logger.Println(fmt.Sprintf("Productivity: Page %d speech returned state %s; waiting before advancing", index+1, state))
			if !waitForReminderSpeechFallback(ctx, page.Speech) {
				return false
			}
		}
	}
	return true
}

func reminderTaskTimeout(task Task) time.Duration {
	timeout := reminderFaceSearchTimeout + reminderWorkflowOverhead
	if task.RequireConfirmation {
		timeout += reminderConfirmationTimeout
	}
	for _, page := range task.Pages {
		timeout += estimatedReminderPageDuration(page.Speech)
	}
	if timeout < reminderDefaultTaskTimeout {
		return reminderDefaultTaskTimeout
	}
	return timeout
}

func estimatedReminderPageDuration(speech string) time.Duration {
	duration := reminderPageSpeechBase + time.Duration(len(strings.Fields(speech)))*reminderPageSpeechPerWord
	if duration < reminderPageMinimumDuration {
		return reminderPageMinimumDuration
	}
	if duration > reminderPageMaximumDuration {
		return reminderPageMaximumDuration
	}
	return duration
}

func estimatedReminderSpeechDuration(text string) time.Duration {
	wordCount := len(strings.Fields(text))
	if wordCount == 0 {
		return 0
	}
	duration := time.Second + time.Duration(wordCount)*400*time.Millisecond
	if duration > 20*time.Second {
		return 20 * time.Second
	}
	return duration
}

func waitForReminderPageSettle(ctx context.Context) bool {
	timer := time.NewTimer(reminderImageSettleDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func waitForReminderSpeechFallback(ctx context.Context, speech string) bool {
	timer := time.NewTimer(estimatedReminderSpeechDuration(speech))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func displayReminderFaceData(ctx context.Context, robot *vector.Vector, faceData []byte, description string) bool {
	if _, err := robot.Conn.DisplayFaceImageRGB(ctx, &vectorpb.DisplayFaceImageRGBRequest{
		FaceData:         faceData,
		DurationMs:       uint32(reminderImageDisplayDuration / time.Millisecond),
		InterruptRunning: true,
	}); err != nil {
		logger.Println("Productivity: " + description + " display failed: " + err.Error())
		return true
	}
	return waitForReminderImage(ctx)
}

// Voice-intent handling can still be releasing its audio and face tracks when
// behavior control is granted back to us. Allow it to settle, then retry if
// SayText loses that race while VIC finishes cleaning up the voice session.
func sayConfirmationResponse(ctx context.Context, robot *vector.Vector, text string) error {
	settleTimer := time.NewTimer(confirmationSpeechSettle)
	select {
	case <-ctx.Done():
		settleTimer.Stop()
		return ctx.Err()
	case <-settleTimer.C:
	}

	var lastErr error
	for attempt := 0; attempt < confirmationSpeechAttempts; attempt++ {
		if _, err := robot.Conn.SayText(ctx, &vectorpb.SayTextRequest{Text: speechWithoutDiacritics(text), UseVectorVoice: true}); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < confirmationSpeechAttempts-1 {
			retryTimer := time.NewTimer(confirmationSpeechRetryDelay)
			select {
			case <-ctx.Done():
				retryTimer.Stop()
				return ctx.Err()
			case <-retryTimer.C:
			}
		}
	}
	return lastErr
}

func acknowledgementAnimationRequest(name string) *vectorpb.PlayAnimationRequest {
	return &vectorpb.PlayAnimationRequest{
		Animation:       &vectorpb.Animation{Name: name},
		Loops:           1,
		IgnoreBodyTrack: true,
		IgnoreHeadTrack: true,
		IgnoreLiftTrack: true,
	}
}

func reminderFaceSearchAnimationRequest() *vectorpb.PlayAnimationRequest {
	return &vectorpb.PlayAnimationRequest{
		Animation:       &vectorpb.Animation{Name: reminderFaceSearchAnimation},
		Loops:           1,
		IgnoreBodyTrack: true,
		IgnoreHeadTrack: true,
		IgnoreLiftTrack: true,
	}
}

func playReminderFaceSearchAnimation(ctx context.Context, robot *vector.Vector) error {
	response, err := robot.Conn.PlayAnimation(ctx, reminderFaceSearchAnimationRequest())
	if err != nil {
		return err
	}
	if response == nil {
		return fmt.Errorf("animation returned no response")
	}
	if response.GetResult() != vectorpb.BehaviorResults_BEHAVIOR_COMPLETE_STATE {
		return fmt.Errorf("animation returned %s", response.GetResult().String())
	}
	return nil
}

// Voice intent handling can hold its face track briefly after returning its
// acknowledgement. Wait for that track to settle and retry once if VIC accepts
// the RPC but reports that the animation could not activate.
func playAcknowledgementAnimation(ctx context.Context, robot *vector.Vector, name string) error {
	settleTimer := time.NewTimer(acknowledgementSettle)
	select {
	case <-ctx.Done():
		settleTimer.Stop()
		return ctx.Err()
	case <-settleTimer.C:
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		response, err := robot.Conn.PlayAnimation(ctx, acknowledgementAnimationRequest(name))
		if err == nil && response != nil && response.GetResult() == vectorpb.BehaviorResults_BEHAVIOR_COMPLETE_STATE {
			return nil
		}
		switch {
		case err != nil:
			lastErr = err
		case response == nil:
			lastErr = fmt.Errorf("animation returned no response")
		default:
			lastErr = fmt.Errorf("animation returned %s", response.GetResult().String())
		}
		if attempt == 0 {
			retryTimer := time.NewTimer(acknowledgementRetryDelay)
			select {
			case <-ctx.Done():
				retryTimer.Stop()
				return ctx.Err()
			case <-retryTimer.C:
			}
		}
	}
	return lastErr
}

// speechWithoutDiacritics keeps names readable in the UI while giving Vector's
// voice engine a plain spelling that it pronounces more consistently.
func speechWithoutDiacritics(text string) string {
	decomposed := norm.NFD.String(text)
	withoutMarks := strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return r
	}, decomposed)
	return norm.NFC.String(withoutMarks)
}

type faceSearchObservations struct {
	mu     sync.Mutex
	faceID int32
	found  bool
}

func (o *faceSearchObservations) observe(event *vectorpb.Event) {
	if event == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	if face := event.GetRobotObservedFace(); face != nil {
		o.faceID = face.GetFaceId()
		o.found = true
	}
}

func (o *faceSearchObservations) face() (int32, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.faceID, o.found
}

// Scan in small, completed turns until the event stream reports a tracked face,
// explicitly face it, and make one short, cliff-monitored approach. Presentation
// is gated on positive confirmation that both wheels have stopped.
func facePersonForReminder(ctx context.Context, robot *vector.Vector, esn string) bool {
	positionReminderLiftDown(ctx, robot)
	positionReminderHeadForViewing(ctx, robot, "before face scan")
	defer positionReminderHeadForViewing(ctx, robot, "before reminder display")

	searchCtx, cancel := context.WithTimeout(ctx, reminderFaceSearchTimeout)
	observations := &faceSearchObservations{}
	if _, err := robot.Conn.EnableFaceDetection(searchCtx, &vectorpb.EnableFaceDetectionRequest{Enable: true}); err != nil {
		logger.Println("Productivity: Could not explicitly enable face detection: " + err.Error())
	}
	eventStream, eventErr := robot.Conn.EventStream(searchCtx, &vectorpb.EventRequest{})
	faceSeen := make(chan int32, 1)
	if eventErr == nil {
		go func() {
			for {
				response, err := eventStream.Recv()
				if err != nil {
					return
				}
				event := response.GetEvent()
				observations.observe(event)
				if face := event.GetRobotObservedFace(); face != nil {
					select {
					case faceSeen <- face.GetFaceId():
					default:
					}
				}
			}
		}()
	} else {
		cancel()
		logger.Println("Productivity: Could not observe faces; skipping face search: " + eventErr.Error())
		return true
	}

	logger.Println("Productivity: Looking for a person before delivering reminder")
	if err := playReminderFaceSearchAnimation(searchCtx, robot); err != nil && searchCtx.Err() == nil {
		logger.Println("Productivity: Face-search animation failed; continuing scan: " + err.Error())
	}
	if ip := robotIPForESN(esn); ip != "" {
		if err := stopReminderAnimationAudio(searchCtx, ip); err != nil && searchCtx.Err() == nil {
			logger.Println("Productivity: Could not stop face-search audio: " + err.Error())
		}
	}

	// Give face detection a moment before beginning the scan. Each turn is a
	// small, completed action, so observing a face prevents any further turns.
	warmupTimer := time.NewTimer(reminderInitialFaceCheck)
	select {
	case <-faceSeen:
		warmupTimer.Stop()
	case <-warmupTimer.C:
	case <-searchCtx.Done():
	}

scanLoop:
	for step := 0; step < reminderFaceScanMaxSteps; step++ {
		if _, found := observations.face(); found {
			break
		}
		if searchCtx.Err() != nil {
			break
		}
		turnResponse, err := robot.Conn.TurnInPlace(searchCtx, &vectorpb.TurnInPlaceRequest{
			AngleRad:        float32(reminderFaceScanStepAngle),
			SpeedRadPerSec:  reminderFaceScanSpeed,
			AccelRadPerSec2: 1.5,
			IdTag:           reminderFaceScanActionTag,
			NumRetries:      0,
		})
		if err != nil {
			if searchCtx.Err() == nil {
				logger.Println("Productivity: Face scan turn failed; continuing: " + err.Error())
			}
			break
		}
		if turnResponse.GetResult() == nil || turnResponse.GetResult().GetCode() != vectorpb.ActionResult_ACTION_RESULT_SUCCESS {
			logger.Println("Productivity: Face scan turn did not complete; continuing")
			break
		}

		pauseTimer := time.NewTimer(reminderFaceScanPause)
		select {
		case <-faceSeen:
			pauseTimer.Stop()
			break scanLoop
		case <-pauseTimer.C:
		case <-searchCtx.Done():
			pauseTimer.Stop()
			break scanLoop
		}
	}
	cancel()

	faceID, faceObserved := observations.face()
	if !faceObserved {
		logger.Println("Productivity: No face observed before reminder; continuing")
		return true
	}

	logger.Println("Productivity: Turning toward observed face for reminder")
	turnCtx, turnCancel := context.WithTimeout(ctx, reminderFaceTurnTimeout)
	defer turnCancel()
	turnResponse, err := robot.Conn.TurnTowardsFace(turnCtx, &vectorpb.TurnTowardsFaceRequest{
		FaceId:          faceID,
		MaxTurnAngleRad: float32(math.Pi),
		IdTag:           reminderFaceTurnActionTag,
		NumRetries:      1,
	})
	if err != nil {
		logger.Println("Productivity: Could not turn toward face; continuing: " + err.Error())
		return true
	}
	if turnResponse.GetResult() == nil || turnResponse.GetResult().GetCode() != vectorpb.ActionResult_ACTION_RESULT_SUCCESS {
		logger.Println("Productivity: Face turn did not complete; continuing")
		return true
	}
	logger.Println("Productivity: Facing person for reminder")
	return driveStraightForReminder(ctx, robot)
}

type reminderDriveResult struct {
	response *vectorpb.DriveStraightResponse
	err      error
}

func driveStraightForReminder(ctx context.Context, robot *vector.Vector) bool {
	motionCtx, cancel := context.WithTimeout(ctx, reminderApproachTimeout)
	defer cancel()
	eventStream, err := robot.Conn.EventStream(motionCtx, &vectorpb.EventRequest{})
	if err != nil {
		logger.Println("Productivity: Could not monitor cliff sensors; skipping reminder approach: " + err.Error())
		return true
	}
	states := make(chan *vectorpb.RobotState, 4)
	cliffSeen := make(chan struct{}, 1)
	go func() {
		for {
			response, recvErr := eventStream.Recv()
			if recvErr != nil {
				return
			}
			if response == nil || response.GetEvent() == nil {
				continue
			}
			state := response.GetEvent().GetRobotState()
			if state == nil {
				continue
			}
			select {
			case states <- state:
			default:
			}
			if reminderRobotStateHas(state, vectorpb.RobotStatus_ROBOT_STATUS_CLIFF_DETECTED) {
				select {
				case cliffSeen <- struct{}{}:
				default:
				}
			}
		}
	}()

	warmup := time.NewTimer(reminderDriveStateWarmup)
	defer warmup.Stop()
	select {
	case state := <-states:
		if reminderRobotStateHas(state, vectorpb.RobotStatus_ROBOT_STATUS_CLIFF_DETECTED) {
			logger.Println("Productivity: Cliff already detected; skipping reminder approach")
			return true
		}
	case <-cliffSeen:
		logger.Println("Productivity: Cliff already detected; skipping reminder approach")
		return true
	case <-warmup.C:
		logger.Println("Productivity: No robot safety state received; skipping reminder approach")
		return true
	case <-motionCtx.Done():
		return true
	}

	logger.Println("Productivity: Driving straight toward observed face before reminder")
	driveDone := make(chan reminderDriveResult, 1)
	go func() {
		response, driveErr := robot.Conn.DriveStraight(motionCtx, reminderDriveRequest())
		driveDone <- reminderDriveResult{response: response, err: driveErr}
	}()

	var result reminderDriveResult
	select {
	case result = <-driveDone:
	case <-cliffSeen:
		logger.Println("Productivity: Cliff detected during reminder approach; canceling drive")
		cancelReminderDrive(robot)
		select {
		case result = <-driveDone:
		case <-motionCtx.Done():
			result.err = motionCtx.Err()
		}
	case <-motionCtx.Done():
		logger.Println("Productivity: Reminder approach timed out; canceling drive")
		cancelReminderDrive(robot)
		result.err = motionCtx.Err()
	}
	if result.err != nil {
		logger.Println("Productivity: Reminder approach ended without success: " + result.err.Error())
	} else if result.response == nil || result.response.GetResult() == nil || result.response.GetResult().GetCode() != vectorpb.ActionResult_ACTION_RESULT_SUCCESS {
		logger.Println("Productivity: Reminder approach was stopped by robot safety")
	}

	stopCtx, stopCancel := context.WithTimeout(ctx, reminderDriveStopTimeout)
	defer stopCancel()
	if _, err := robot.Conn.StopAllMotors(stopCtx, &vectorpb.StopAllMotorsRequest{}); err != nil {
		logger.Println("Productivity: Could not issue final motor stop: " + err.Error())
	}
	if !waitForReminderWheelsStopped(stopCtx, robot) {
		logger.Println("Productivity: Wheels did not report stopped after reminder approach")
		return false
	}
	logger.Println("Productivity: Reminder approach stopped; presentation may begin")
	return true
}

func reminderDriveRequest() *vectorpb.DriveStraightRequest {
	return &vectorpb.DriveStraightRequest{
		SpeedMmps:           reminderApproachSpeedMMPS,
		DistMm:              reminderApproachDistanceMM,
		ShouldPlayAnimation: false,
		IdTag:               reminderApproachActionTag,
		NumRetries:          0,
	}
}

func reminderRobotStateHas(state *vectorpb.RobotState, flag vectorpb.RobotStatus) bool {
	return state != nil && state.GetStatus()&uint32(flag) != 0
}

func reminderRobotStateStopped(state *vectorpb.RobotState) bool {
	if state == nil {
		return false
	}
	if reminderRobotStateHas(state, vectorpb.RobotStatus_ROBOT_STATUS_IS_MOVING) || reminderRobotStateHas(state, vectorpb.RobotStatus_ROBOT_STATUS_ARE_WHEELS_MOVING) {
		return false
	}
	return math.Abs(float64(state.GetLeftWheelSpeedMmps())) < 1 && math.Abs(float64(state.GetRightWheelSpeedMmps())) < 1
}

func cancelReminderDrive(robot *vector.Vector) {
	cancelCtx, cancel := context.WithTimeout(context.Background(), reminderDriveStopTimeout)
	defer cancel()
	if _, err := robot.Conn.CancelActionByIdTag(cancelCtx, &vectorpb.CancelActionByIdTagRequest{IdTag: reminderApproachActionTag}); err != nil {
		logger.Println("Productivity: Could not cancel reminder drive action: " + err.Error())
	}
	if _, err := robot.Conn.StopAllMotors(cancelCtx, &vectorpb.StopAllMotorsRequest{}); err != nil {
		logger.Println("Productivity: Could not stop reminder drive motors: " + err.Error())
	}
}

func waitForReminderWheelsStopped(ctx context.Context, robot *vector.Vector) bool {
	settle := time.NewTimer(reminderDriveStopSettle)
	select {
	case <-ctx.Done():
		settle.Stop()
		return false
	case <-settle.C:
	}
	stream, err := robot.Conn.EventStream(ctx, &vectorpb.EventRequest{})
	if err != nil {
		return false
	}
	for {
		response, recvErr := stream.Recv()
		if recvErr != nil {
			return false
		}
		if response == nil || response.GetEvent() == nil {
			continue
		}
		if reminderRobotStateStopped(response.GetEvent().GetRobotState()) {
			return true
		}
	}
}

func positionReminderHeadForViewing(ctx context.Context, robot *vector.Vector, reason string) {
	headCtx, cancel := context.WithTimeout(ctx, reminderHeadMoveTimeout)
	defer cancel()
	response, err := robot.Conn.SetHeadAngle(headCtx, reminderHeadAngleRequest())
	if err != nil {
		logger.Println("Productivity: Could not position head " + reason + ": " + err.Error())
		return
	}
	if response == nil || response.GetResult() == nil || response.GetResult().GetCode() != vectorpb.ActionResult_ACTION_RESULT_SUCCESS {
		logger.Println("Productivity: Head positioning did not complete " + reason)
	}
}

func positionReminderLiftDown(ctx context.Context, robot *vector.Vector) {
	liftCtx, cancel := context.WithTimeout(ctx, reminderLiftMoveTimeout)
	defer cancel()
	response, err := robot.Conn.SetLiftHeight(liftCtx, reminderLiftHeightRequest())
	if err != nil {
		logger.Println("Productivity: Could not lower lift before face scan: " + err.Error())
		return
	}
	if response == nil || response.GetResult() == nil || response.GetResult().GetCode() != vectorpb.ActionResult_ACTION_RESULT_SUCCESS {
		logger.Println("Productivity: Lift did not reach its lowest position before face scan")
	}
}

func reminderLiftHeightRequest() *vectorpb.SetLiftHeightRequest {
	return &vectorpb.SetLiftHeightRequest{
		HeightMm:          reminderLowestLiftHeightMM,
		MaxSpeedRadPerSec: 10,
		AccelRadPerSec2:   10,
		DurationSec:       0,
		IdTag:             reminderLiftActionTag,
		NumRetries:        1,
	}
}

func reminderHeadAngleRequest() *vectorpb.SetHeadAngleRequest {
	return &vectorpb.SetHeadAngleRequest{
		AngleRad:          float32(reminderViewingHeadAngle),
		MaxSpeedRadPerSec: 10,
		AccelRadPerSec2:   10,
		DurationSec:       0,
		IdTag:             reminderHeadActionTag,
		NumRetries:        1,
	}
}

// DisplayFaceImageRGB only confirms that the face-image chunks were submitted;
// vic-engine continues running the face action for DurationMs. Starting SayText
// during that window makes the two actions compete for the face.
func waitForReminderImage(ctx context.Context) bool {
	timer := time.NewTimer(reminderImageDisplayDuration + reminderImageSettleDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func acquireBehaviorControl(bcClient behaviorControlStream) error {
	req := &vectorpb.BehaviorControlRequest{
		RequestType: &vectorpb.BehaviorControlRequest_ControlRequest{
			ControlRequest: &vectorpb.ControlRequest{
				Priority: vectorpb.ControlRequest_OVERRIDE_BEHAVIORS,
			},
		},
	}
	if err := bcClient.Send(req); err != nil {
		return err
	}
	for {
		resp, err := bcClient.Recv()
		if err != nil {
			return err
		}
		if resp != nil && resp.GetControlGrantedResponse() != nil {
			return nil
		}
		if resp != nil && resp.GetControlLostEvent() != nil {
			return fmt.Errorf("control was lost before it was granted")
		}
	}
}

func releaseBehaviorControl(bcClient behaviorControlStream) error {
	return bcClient.Send(&vectorpb.BehaviorControlRequest{
		RequestType: &vectorpb.BehaviorControlRequest_ControlRelease{
			ControlRelease: &vectorpb.ControlRelease{},
		},
	})
}

func waitForConfirmation(ctx context.Context, robot *vector.Vector, esn string) (confirmationResult, error) {
	confirmationCtx, cancel := context.WithTimeout(ctx, reminderConfirmationTimeout)
	defer cancel()

	// Give VIC time to process the control release before simulating the
	// button press that enables normal voice-intent handling.
	releaseDelay := time.NewTimer(500 * time.Millisecond)
	defer releaseDelay.Stop()
	select {
	case <-confirmationCtx.Done():
		return confirmationTimedOut, nil
	case <-releaseDelay.C:
	}

	ip := robotIPForESN(esn)

	eventStream, err := robot.Conn.EventStream(confirmationCtx, &vectorpb.EventRequest{})
	if err != nil {
		return confirmationTimedOut, err
	}
	listeningSession := registerConfirmationListeningSession(confirmationCtx, esn, ip)
	defer unregisterConfirmationListeningSession(esn, listeningSession)
	if ip != "" {
		if err := triggerConfirmationListening(confirmationCtx, ip); err != nil {
			logger.Println("Productivity: Could not start confirmation listening: " + err.Error())
		} else {
			listeningSession.mu.Lock()
			listeningSession.attempts++
			listeningSession.mu.Unlock()
		}
	}

	for {
		msg, err := eventStream.Recv()
		if err != nil {
			if confirmationCtx.Err() != nil {
				return confirmationTimedOut, nil
			}
			return confirmationTimedOut, err
		}
		if msg == nil || msg.Event == nil {
			continue
		}
		intent := msg.Event.GetUserIntent()
		if intent == nil {
			continue
		}
		b, err := protojson.Marshal(intent)
		if err != nil {
			continue
		}
		switch classifyConfirmationIntent(string(b)) {
		case confirmationAccepted:
			return confirmationAccepted, nil
		case confirmationDeclined:
			return confirmationDeclined, nil
		case confirmationTimedOut:
			listeningSession.queueRetry("Confirmation was not yes or no")
		}
	}
}

func registerConfirmationListeningSession(ctx context.Context, esn, ip string) *confirmationListeningSession {
	session := &confirmationListeningSession{ctx: ctx, ip: ip}
	confirmationListeningSessions.Lock()
	confirmationListeningSessions.byESN[esn] = session
	confirmationListeningSessions.Unlock()
	return session
}

func unregisterConfirmationListeningSession(esn string, session *confirmationListeningSession) {
	confirmationListeningSessions.Lock()
	if confirmationListeningSessions.byESN[esn] == session {
		delete(confirmationListeningSessions.byESN, esn)
	}
	confirmationListeningSessions.Unlock()
}

// NotifyConfirmationNoAudio connects the cloud speech result directly to a
// pending productivity confirmation. The robot does not reliably publish its
// no-audio result back through the SDK event stream.
func NotifyConfirmationNoAudio(esn string) bool {
	confirmationListeningSessions.Lock()
	session := confirmationListeningSessions.byESN[esn]
	confirmationListeningSessions.Unlock()
	if session == nil {
		return false
	}
	return session.queueRetry("No confirmation audio")
}

// NotifyConfirmationUnmatched retries a pending confirmation when speech was
// heard but did not resolve to a yes/no intent.
func NotifyConfirmationUnmatched(esn string) bool {
	confirmationListeningSessions.Lock()
	session := confirmationListeningSessions.byESN[esn]
	confirmationListeningSessions.Unlock()
	if session == nil {
		return false
	}
	return session.queueRetry("Confirmation was not yes or no")
}

func (s *confirmationListeningSession) queueRetry(reason string) bool {
	s.mu.Lock()
	if s.ip == "" || s.retryPending || s.attempts >= confirmationListenAttempts || s.ctx.Err() != nil {
		s.mu.Unlock()
		return false
	}
	s.retryPending = true
	s.mu.Unlock()

	go func() {
		timer := time.NewTimer(confirmationListenRetryDelay)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			s.mu.Lock()
			s.retryPending = false
			s.mu.Unlock()
			return
		case <-timer.C:
		}

		err := triggerConfirmationListening(s.ctx, s.ip)
		s.mu.Lock()
		s.retryPending = false
		if err == nil {
			s.attempts++
		}
		attempt := s.attempts
		s.mu.Unlock()
		if err != nil {
			if s.ctx.Err() == nil {
				logger.Println("Productivity: Could not restart confirmation listening: " + err.Error())
			}
			return
		}
		logger.Println(fmt.Sprintf("Productivity: %s; listening again (%d/%d)", reason, attempt, confirmationListenAttempts))
	}()
	return true
}

func triggerConfirmationListening(ctx context.Context, ip string) error {
	url := fmt.Sprintf("http://%s:8889/consolevarset?key=FakeButtonPressType&value=singlePressDetected", ip)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("robot console returned %s", response.Status)
	}
	return nil
}

func robotIPForESN(esn string) string {
	for _, bot := range vars.BotInfo.Robots {
		if bot.Esn == esn {
			return bot.IPAddress
		}
	}
	return ""
}

func stopReminderAnimationAudio(ctx context.Context, ip string) error {
	url := fmt.Sprintf("http://%s:8889/consolefunccall?func=StopAllAudioEvents", ip)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("robot console returned %s", response.Status)
	}
	return nil
}

func classifyConfirmationIntent(intent string) confirmationResult {
	if strings.Contains(intent, "intent_imperative_affirmative") || strings.Contains(intent, "intent_global_yes") {
		return confirmationAccepted
	}
	if strings.Contains(intent, "intent_imperative_negative") {
		return confirmationDeclined
	}
	return confirmationTimedOut
}

func IntentPass(req interface{}, intentThing string, speechText string, intentParams map[string]string, isParam bool) (interface{}, error) {
	var esn string
	var req1 *vtt.IntentRequest
	var req2 *vtt.IntentGraphRequest
	var isIntentGraph bool

	if str, ok := req.(*vtt.IntentRequest); ok {
		req1 = str
		esn = req1.Device
		isIntentGraph = false
	} else if str, ok := req.(*vtt.IntentGraphRequest); ok {
		req2 = str
		esn = req2.Device
		isIntentGraph = true
	}

	if !isIntentGraph && vars.APIConfig.Knowledge.IntentGraph && intentThing == "intent_system_unmatched" {
		intentThing = "intent_greeting_hello"
	}

	var intentResult pb.IntentResult
	if isParam {
		intentResult = pb.IntentResult{
			QueryText:  speechText,
			Action:     intentThing,
			Parameters: intentParams,
		}
	} else {
		intentResult = pb.IntentResult{
			QueryText: speechText,
			Action:    intentThing,
		}
	}

	logger.LogUI("Intent matched: " + intentThing + ", transcribed text: '" + speechText + "', device: " + esn)

	intent := pb.IntentResponse{
		IsFinal:      true,
		IntentResult: &intentResult,
	}

	intentGraphSend := pb.IntentGraphResponse{
		ResponseType: pb.IntentGraphMode_INTENT,
		IsFinal:      true,
		IntentResult: &intentResult,
		CommandType:  pb.RobotMode_VOICE_COMMAND.String(),
	}

	if !isIntentGraph {
		if err := req1.Stream.Send(&intent); err != nil {
			return nil, err
		}
		return &vtt.IntentResponse{Intent: &intent}, nil
	} else {
		if err := req2.Stream.Send(&intentGraphSend); err != nil {
			return nil, err
		}
		return &vtt.IntentGraphResponse{Intent: &intentGraphSend}, nil
	}
}

func CustomIntentHandler(req interface{}, voiceText string, botSerial string) bool {
	if !vars.CustomIntentsExist {
		return false
	}

	voiceText = strings.ToLower(voiceText)
	for _, c := range vars.CustomIntents {
		for _, v := range c.Utterances {
			seekText := strings.ToLower(strings.TrimSpace(v))
			if (c.IsSystemIntent && strings.HasPrefix(seekText, "*")) || strings.Contains(voiceText, seekText) {
				logger.Println("Bot " + botSerial + " Custom Intent Matched: " + c.Name)

				var intentParams map[string]string
				var isParam bool = false
				if c.Params.ParamValue != "" {
					intentParams = map[string]string{c.Params.ParamName: c.Params.ParamValue}
					isParam = true
				}

				if c.LuaScript != "" {
					go func() {
						if err := scripting.RunLuaScript(botSerial, c.LuaScript); err != nil {
							logger.Println("Error running Lua script: " + err.Error())
						}
					}()
				}

				var args []string
				for _, arg := range c.ExecArgs {
					switch arg {
					case "!botSerial":
						arg = botSerial
					case "!speechText":
						arg = "\"" + voiceText + "\""
					case "!intentName":
						arg = c.Name
					case "!locale":
						arg = vars.APIConfig.STT.Language
					}
					args = append(args, arg)
				}

				var customIntentExec *exec.Cmd
				if len(args) == 0 {
					customIntentExec = exec.Command(c.Exec)
				} else {
					customIntentExec = exec.Command(c.Exec, args...)
				}

				var out bytes.Buffer
				var stderr bytes.Buffer
				customIntentExec.Stdout = &out
				customIntentExec.Stderr = &stderr

				if err := customIntentExec.Run(); err != nil {
					logger.Println("Exec error: " + err.Error() + ": " + stderr.String())
				}

				if c.IsSystemIntent {
					var resp systemIntentResponseStruct
					if err := json.Unmarshal(out.Bytes(), &resp); err == nil && resp.Status == "ok" {
						IntentPass(req, resp.ReturnIntent, voiceText, intentParams, isParam)
						return true
					}
				} else {
					IntentPass(req, c.Intent, voiceText, intentParams, isParam)
					return true
				}
			}
		}
	}
	return false
}

func ProcessTextAll(req interface{}, voiceText string, intents []vars.JsonIntent, isOpus bool) bool {
	var botSerial string
	if str, ok := req.(*vtt.IntentRequest); ok {
		botSerial = str.Device
	} else if str, ok := req.(*vtt.KnowledgeGraphRequest); ok {
		botSerial = str.Device
	} else if str, ok := req.(*vtt.IntentGraphRequest); ok {
		botSerial = str.Device
	}

	voiceText = strings.ToLower(voiceText)

	if CustomIntentHandler(req, voiceText, botSerial) {
		return true
	}

	for _, b := range intents {
		for _, c := range b.Keyphrases {
			if voiceText == strings.ToLower(c) {
				logger.Println("Bot " + botSerial + " Perfect match for intent " + b.Name)
				IntentPass(req, b.Name, voiceText, nil, false)
				return true
			}
		}
	}

	for _, b := range intents {
		if b.RequireExactMatch {
			continue
		}
		for _, c := range b.Keyphrases {
			if matchesPartialIntentKeyphrase(b.Name, voiceText, c) {
				logger.Println("Bot " + botSerial + " Partial match for intent " + b.Name)
				IntentPass(req, b.Name, voiceText, nil, false)
				return true
			}
		}
	}

	return false
}

func matchesPartialIntentKeyphrase(intentName string, voiceText string, keyphrase string) bool {
	voiceText = strings.ToLower(strings.TrimSpace(voiceText))
	keyphrase = strings.ToLower(strings.TrimSpace(keyphrase))
	if keyphrase == "" {
		return false
	}
	if intentName == "intent_imperative_affirmative" && containsNegativeCue(voiceText) {
		return false
	}
	if utf8.RuneCountInString(keyphrase) <= 2 {
		for _, token := range strings.FieldsFunc(voiceText, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsNumber(r)
		}) {
			if token == keyphrase {
				return true
			}
		}
		return false
	}
	return strings.Contains(voiceText, keyphrase)
}

func containsNegativeCue(voiceText string) bool {
	negativeCues := map[string]struct{}{
		"no": {}, "not": {}, "dont": {}, "don't": {}, "never": {}, "nope": {}, "nah": {},
	}
	for _, token := range strings.FieldsFunc(strings.ToLower(voiceText), func(r rune) bool {
		return !unicode.IsLetter(r) && r != '\''
	}) {
		if _, found := negativeCues[token]; found {
			return true
		}
	}
	return false
}

func convertImageToVectorFace(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	img, _, err := image.Decode(file)
	if err != nil {
		return nil, err
	}
	return convertImageToVectorFaceData(img), nil
}

// Vector's face display uses standard RGB565, transported high byte first as
// specified by Anki's official Vector SDK.
func convertImageToVectorFaceData(img image.Image) []byte {
	const width = 184
	const height = 96
	buf := make([]byte, width*height*2)
	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			srcX := x * srcW / width
			srcY := y * srcH / height
			c := img.At(bounds.Min.X+srcX, bounds.Min.Y+srcY)
			r, g, b, _ := c.RGBA()
			faceColor := (uint16(r>>11) << 11) | (uint16(g>>10) << 5) | uint16(b>>11)
			idx := (y*width + x) * 2
			binary.BigEndian.PutUint16(buf[idx:idx+2], faceColor)
		}
	}
	return buf
}
