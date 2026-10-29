package productivity

import (
	"context"
	"strings"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
)

const (
	sportsButtonClickWindow       = 500 * time.Millisecond
	sportsButtonDebounce          = 40 * time.Millisecond
	sportsMuteConfirmationTimeout = 12 * time.Second
)

type sportsMuteKey struct {
	source, robotESN, eventID string
}

// Protected by taskQueueMu. Event IDs keep mutes specific to a single game or
// F1 session, including across midnight and configuration changes.
var mutedSportsEvents = make(map[sportsMuteKey]bool)

func sportsEventMuteKey(source, robotESN, eventID string) sportsMuteKey {
	return sportsMuteKey{source, strings.ToLower(robotESN), eventID}
}

func sportsEventIsMuted(source, robotESN, eventID string) bool {
	taskQueueMu.Lock()
	defer taskQueueMu.Unlock()
	return mutedSportsEvents[sportsEventMuteKey(source, robotESN, eventID)]
}

func sportsTaskSupportsMute(task Task) bool {
	return (task.sportsEventSource == "nba" || task.sportsEventSource == "f1") && task.sportsEventID != "" && !task.sportsFinal
}

func sportsTaskIsMuted(task Task) bool {
	if !sportsTaskSupportsMute(task) {
		return false
	}
	taskQueueMu.Lock()
	defer taskQueueMu.Unlock()
	return sportsTaskIsMutedLocked(task)
}

func sportsTaskIsMutedLocked(task Task) bool {
	return sportsTaskSupportsMute(task) && mutedSportsEvents[sportsEventMuteKey(task.sportsEventSource, task.RobotESN, task.sportsEventID)]
}

func muteSportsEvent(task Task) {
	if !sportsTaskSupportsMute(task) {
		return
	}
	taskQueueMu.Lock()
	defer taskQueueMu.Unlock()
	key := sportsEventMuteKey(task.sportsEventSource, task.RobotESN, task.sportsEventID)
	mutedSportsEvents[key] = true
	// Wake deferred retries immediately, but preserve a final result that may
	// have arrived while the older live update was still being spoken.
	for pendingKey, notification := range pendingSportsNotifications {
		if pendingKey.source == key.source && pendingKey.eventID == key.eventID && pendingKey.robotESN == key.robotESN && !notification.final {
			close(notification.superseded)
			delete(pendingSportsNotifications, pendingKey)
		}
	}
	pending := make([]Task, 0, cap(taskQueue))
drain:
	for {
		select {
		case queued := <-taskQueue:
			if !taskIsSuperseded(queued) && !sportsTaskIsMutedLocked(queued) {
				pending = append(pending, queued)
			}
		default:
			break drain
		}
	}
	for _, queued := range pending {
		taskQueue <- queued
	}
}

type sportsButtonClickDetector struct {
	initialized bool
	pressed     bool
	pressedAt   time.Time
}

func (detector *sportsButtonClickDetector) observe(state *vectorpb.RobotState, now time.Time) bool {
	if state == nil {
		return false
	}
	pressed := reminderRobotStateHas(state, vectorpb.RobotStatus_ROBOT_STATUS_IS_BUTTON_PRESSED)
	if !detector.initialized {
		detector.initialized, detector.pressed = true, pressed
		return false
	}
	if pressed == detector.pressed {
		return false
	}
	detector.pressed = pressed
	if pressed {
		detector.pressedAt = now
		return false
	}
	// Count one completed click. Ignore holds, switch bounce, repeated samples,
	// and a button already held when notification delivery begins.
	duration := now.Sub(detector.pressedAt)
	return !detector.pressedAt.IsZero() && duration >= sportsButtonDebounce && duration <= sportsButtonClickWindow
}

// Listen only during presentation, so clicks while idle, driving, or delivering
// unrelated reminders cannot mute a game. Join cleanup before the next task so
// its speech can never be silenced by a late audio-stop request.
func startSportsMuteListener(ctx context.Context, robot vectorpb.ExternalInterfaceClient, task Task, stopPresentation context.CancelFunc) func() bool {
	if !sportsTaskSupportsMute(task) || ctx.Err() != nil {
		return func() bool { return false }
	}
	streamCtx, stopStream := context.WithCancel(ctx)
	stream, err := robot.EventStream(streamCtx, &vectorpb.EventRequest{
		ListType: &vectorpb.EventRequest_WhiteList{WhiteList: &vectorpb.FilterList{List: []string{"robot_state"}}},
	})
	if err != nil {
		stopStream()
		logger.Println("Productivity: Sports mute button listener unavailable: " + err.Error())
		return func() bool { return false }
	}
	done := make(chan struct{})
	muted := false // Read only after done closes.
	go func() {
		defer close(done)
		var detector sportsButtonClickDetector
		for {
			response, err := stream.Recv()
			if streamCtx.Err() != nil {
				return
			}
			if err != nil {
				logger.Println("Productivity: Sports mute button listener stopped: " + err.Error())
				return
			}
			if !detector.observe(response.GetEvent().GetRobotState(), time.Now()) {
				continue
			}
			muteSportsEvent(task)
			muted = true
			stopPresentation()
			logger.Println("Productivity: Muted " + task.sportsEventSource + " event " + task.sportsEventID + " until the final result for " + task.RobotESN)
			if ip := robotIPForESN(task.RobotESN); ip != "" {
				stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				err := stopReminderAnimationAudio(stopCtx, ip)
				cancel()
				if err != nil {
					logger.Println("Productivity: Could not stop sports audio before mute confirmation: " + err.Error())
				}
			}
			return
		}
	}()
	return func() bool {
		stopStream()
		<-done
		return muted
	}
}

// Join the interrupted presentation's audio cleanup before this runs. Keep its
// existing behavior control through feedback so the mute click cannot also
// activate normal button listening or interrupt a native cliff reaction.
func confirmSportsEventMuted(parent context.Context, robot *vector.Vector, task Task) {
	ctx, cancel := context.WithTimeout(parent, sportsMuteConfirmationTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return
	}
	chimeCtx, stopChime := context.WithTimeout(ctx, commandAcknowledgementTimeout)
	err := playVoiceCommandAcknowledgement(chimeCtx, robot.Conn)
	stopChime()
	if err != nil {
		logger.Println("Productivity: Sports mute confirmation chime failed: " + err.Error())
	}
	if ctx.Err() != nil {
		return
	}
	message := "Game muted."
	if task.sportsEventSource == "f1" {
		message = "Session muted."
	}
	if err := sayConfirmationResponse(ctx, robot, message); err != nil {
		logger.Println("Productivity: Sports mute confirmation speech failed: " + err.Error())
	}
}
