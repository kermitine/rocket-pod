package productivity

import (
	"bytes"
	"context"
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
	"google.golang.org/protobuf/encoding/protojson"
)

type Task struct {
	ID                      string
	RobotESN                string
	Phrases                 []string
	Image                   string
	Source                  string
	RetryCount              int
	RequireConfirmation     bool
	SnoozeMinutes           int
	configurationGeneration uint64
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
	reminderFaceSearchTimeout    = 12 * time.Second
	reminderFaceTurnTimeout      = 6 * time.Second
	reminderFaceTurnActionTag    = 2400001
	reminderFaceScanActionTag    = 2400002
	reminderFaceScanStepAngle    = math.Pi / 6
	reminderFaceScanSpeed        = 0.75
)

var (
	taskQueue               = make(chan Task, 10)
	configurationGeneration uint64
)

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
	if !taskIsCurrent(task) {
		return
	}
	if task.RetryCount >= 4 {
		logger.Println("Productivity: Task failed permanently: " + reason)
		return
	}
	task.RetryCount++
	backoff := math.Pow(2, float64(task.RetryCount))
	go func() {
		time.Sleep(time.Duration(backoff) * time.Second)
		if taskIsCurrent(task) {
			taskQueue <- task
		}
	}()
}

func snoozeTask(task Task) {
	if task.Source == "test" {
		logger.Println("Productivity: Test reminders are one-shot and will not be snoozed")
		return
	}
	if !taskIsCurrent(task) {
		return
	}
	duration := 10 * time.Minute
	if task.SnoozeMinutes > 0 {
		duration = time.Duration(task.SnoozeMinutes) * time.Minute
	}
	logger.Println("Productivity: Snoozing task " + task.ID + " for " + duration.String())
	go func() {
		time.Sleep(duration)
		task.RetryCount = 0
		if taskIsCurrent(task) {
			taskQueue <- task
		}
	}()
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
		retryTask(task, "Robot lookup failed")
		return
	}

	if !taskIsCurrent(task) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	defer cancel()

	bcClient, err := robot.Conn.BehaviorControl(ctx)
	if err != nil {
		retryTask(task, "BC stream failed")
		return
	}

	if err := acquireBehaviorControl(bcClient); err != nil {
		retryTask(task, "Behavior control request failed: "+err.Error())
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

	battResp, err := robot.Conn.BatteryState(ctx, &vectorpb.BatteryStateRequest{})
	if err == nil && battResp.IsOnChargerPlatform {
		_, err := robot.Conn.DriveOffCharger(ctx, &vectorpb.DriveOffChargerRequest{})
		if err != nil {
			retryTask(task, "Drive off failed")
			return
		}
		time.Sleep(5 * time.Second)
	}
	if !taskIsCurrent(task) {
		return
	}

	facePersonForReminder(ctx, robot)
	if !taskIsCurrent(task) {
		return
	}

	if task.Image != "" {
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
	if !taskIsCurrent(task) {
		return
	}

	if len(task.Phrases) > 0 {
		phrase := task.Phrases[rand.Intn(len(task.Phrases))]
		if phrase != "" {
			if _, err := robot.Conn.SayText(ctx, &vectorpb.SayTextRequest{
				Text:           phrase,
				UseVectorVoice: true,
				DurationScalar: 1.0,
			}); err != nil {
				logger.Println("Productivity: Reminder speech failed: " + err.Error())
			}
		}
	}

	if task.RequireConfirmation {
		if !taskIsCurrent(task) {
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
		if !taskIsCurrent(task) {
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
			if _, err := robot.Conn.SayText(ctx, &vectorpb.SayTextRequest{Text: "Great!", UseVectorVoice: true}); err != nil {
				logger.Println("Productivity: Confirmation response speech failed: " + err.Error())
			}
			logger.Println("Productivity: Confirmation successful.")
		case confirmationDeclined:
			if _, err := robot.Conn.SayText(ctx, &vectorpb.SayTextRequest{Text: "Ok, I'll remind you again soon.", UseVectorVoice: true}); err != nil {
				logger.Println("Productivity: Confirmation response speech failed: " + err.Error())
			}
			snoozeTask(task)
		default:
			if _, err := robot.Conn.SayText(ctx, &vectorpb.SayTextRequest{Text: "I didn't hear anything. I'll remind you later.", UseVectorVoice: true}); err != nil {
				logger.Println("Productivity: Confirmation response speech failed: " + err.Error())
			}
			snoozeTask(task)
		}
	}
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
// then explicitly face it. Reminder face seeking is deliberately rotation-only:
// Vector must never drive toward a person here.
func facePersonForReminder(ctx context.Context, robot *vector.Vector) {
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
		return
	}

	logger.Println("Productivity: Looking for a person before delivering reminder")

	// Give face detection a moment before beginning the scan. Each turn is a
	// small, completed action, so observing a face prevents any further turns.
	warmupTimer := time.NewTimer(500 * time.Millisecond)
	select {
	case <-faceSeen:
		warmupTimer.Stop()
	case <-warmupTimer.C:
	case <-searchCtx.Done():
	}

	for {
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
	}
	cancel()

	faceID, faceObserved := observations.face()
	if !faceObserved {
		logger.Println("Productivity: No face observed before reminder; continuing")
		return
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
		return
	}
	if turnResponse.GetResult() == nil || turnResponse.GetResult().GetCode() != vectorpb.ActionResult_ACTION_RESULT_SUCCESS {
		logger.Println("Productivity: Face turn did not complete; continuing")
		return
	}
	logger.Println("Productivity: Facing person for reminder")
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
	confirmationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
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

	var ip string
	for _, bot := range vars.BotInfo.Robots {
		if bot.Esn == esn {
			ip = bot.IPAddress
			break
		}
	}

	if ip != "" {
		go func() {
			url := fmt.Sprintf("http://%s:8889/consolevarset?key=FakeButtonPressType&value=singlePressDetected", ip)
			client := &http.Client{Timeout: 2 * time.Second}
			resp, err := client.Get(url)
			if err == nil {
				resp.Body.Close()
			}
		}()
	}

	eventStream, err := robot.Conn.EventStream(confirmationCtx, &vectorpb.EventRequest{})
	if err != nil {
		return confirmationTimedOut, err
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
		}
	}
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
			r5 := uint16((r >> 11) & 0x1F)
			g6 := uint16((g >> 10) & 0x3F)
			b5 := uint16((b >> 11) & 0x1F)
			rgb565 := (r5 << 11) | (g6 << 5) | b5
			idx := (y*width + x) * 2
			buf[idx] = byte(rgb565 >> 8)
			buf[idx+1] = byte(rgb565 & 0xFF)
		}
	}
	return buf, nil
}
