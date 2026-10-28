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
	nbaDoubleClickWindow       = 500 * time.Millisecond
	nbaButtonDebounce          = 40 * time.Millisecond
	nbaMuteConfirmationTimeout = 12 * time.Second
)

type nbaGameMuteKey struct {
	robotESN, gameID string
}

// Protected by taskQueueMu. Game IDs keep mutes specific to a single matchup,
// including when the game crosses midnight or settings are saved again.
var mutedNBAGames = make(map[nbaGameMuteKey]bool)

func nbaMuteKey(robotESN, gameID string) nbaGameMuteKey {
	return nbaGameMuteKey{strings.ToLower(robotESN), gameID}
}

func nbaGameIsMuted(robotESN, gameID string) bool {
	taskQueueMu.Lock()
	defer taskQueueMu.Unlock()
	return mutedNBAGames[nbaMuteKey(robotESN, gameID)]
}

func nbaTaskIsMuted(task Task) bool {
	if task.nbaGameID == "" || task.nbaFinal {
		return false
	}
	taskQueueMu.Lock()
	defer taskQueueMu.Unlock()
	return nbaTaskIsMutedLocked(task)
}

func nbaTaskIsMutedLocked(task Task) bool {
	return task.nbaGameID != "" && !task.nbaFinal && mutedNBAGames[nbaMuteKey(task.RobotESN, task.nbaGameID)]
}

func muteNBAGame(task Task) {
	if task.nbaGameID == "" || task.nbaFinal {
		return
	}
	taskQueueMu.Lock()
	defer taskQueueMu.Unlock()
	key := nbaMuteKey(task.RobotESN, task.nbaGameID)
	mutedNBAGames[key] = true
	// Wake deferred retries immediately, but preserve a final result that may
	// have arrived while the older live update was still being spoken.
	for pendingKey, notification := range pendingSportsNotifications {
		if pendingKey.source == "nba" && pendingKey.eventID == key.gameID && pendingKey.robotESN == key.robotESN && !notification.nbaFinal {
			close(notification.superseded)
			delete(pendingSportsNotifications, pendingKey)
		}
	}
	pending := make([]Task, 0, cap(taskQueue))
drain:
	for {
		select {
		case queued := <-taskQueue:
			if !taskIsSuperseded(queued) && !nbaTaskIsMutedLocked(queued) {
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

type nbaDoubleClickDetector struct {
	initialized bool
	pressed     bool
	pressedAt   time.Time
	firstClick  time.Time
}

func (detector *nbaDoubleClickDetector) observe(state *vectorpb.RobotState, now time.Time) bool {
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
	// Count completed clicks only. A held button, repeated sensor samples,
	// switch bounce, or a button already held at startup is not a double-click.
	duration := now.Sub(detector.pressedAt)
	if detector.pressedAt.IsZero() || duration < nbaButtonDebounce || duration > nbaDoubleClickWindow {
		detector.firstClick = time.Time{}
		return false
	}
	if !detector.firstClick.IsZero() && now.Sub(detector.firstClick) <= nbaDoubleClickWindow {
		detector.firstClick = time.Time{}
		return true
	}
	detector.firstClick = now
	return false
}

// Listen only during presentation, so clicks while idle, driving, or delivering
// unrelated reminders cannot mute a game. Join cleanup before the next task so
// its speech can never be silenced by a late audio-stop request.
func startNBAMuteListener(ctx context.Context, robot vectorpb.ExternalInterfaceClient, task Task, stopPresentation context.CancelFunc) func() bool {
	if task.nbaGameID == "" || task.nbaFinal {
		return func() bool { return false }
	}
	streamCtx, stopStream := context.WithCancel(ctx)
	stream, err := robot.EventStream(streamCtx, &vectorpb.EventRequest{
		ListType: &vectorpb.EventRequest_WhiteList{WhiteList: &vectorpb.FilterList{List: []string{"robot_state"}}},
	})
	if err != nil {
		stopStream()
		logger.Println("Productivity: NBA mute button listener unavailable: " + err.Error())
		return func() bool { return false }
	}
	done := make(chan struct{})
	muted := false // Read only after done closes.
	go func() {
		defer close(done)
		var detector nbaDoubleClickDetector
		for {
			response, err := stream.Recv()
			if streamCtx.Err() != nil {
				return
			}
			if err != nil {
				logger.Println("Productivity: NBA mute button listener stopped: " + err.Error())
				return
			}
			if !detector.observe(response.GetEvent().GetRobotState(), time.Now()) {
				continue
			}
			muteNBAGame(task)
			muted = true
			stopPresentation()
			logger.Println("Productivity: Muted NBA game " + task.nbaGameID + " until the final score for " + task.RobotESN)
			if ip := robotIPForESN(task.RobotESN); ip != "" {
				stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				err := stopReminderAnimationAudio(stopCtx, ip)
				cancel()
				if err != nil {
					logger.Println("Productivity: Could not stop NBA audio before releasing control: " + err.Error())
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

// The interrupted presentation must finish and release control before this
// runs, so its audio cleanup cannot silence the chime or confirmation speech.
func confirmNBAGameMuted(parent context.Context, robot *vector.Vector) {
	ctx, cancel := context.WithTimeout(parent, nbaMuteConfirmationTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return
	}
	chimeCtx, stopChime := context.WithTimeout(ctx, commandAcknowledgementTimeout)
	err := playVoiceCommandAcknowledgement(chimeCtx, robot.Conn)
	stopChime()
	if err != nil {
		logger.Println("Productivity: NBA mute confirmation chime failed: " + err.Error())
	}
	if ctx.Err() != nil {
		return
	}
	// Use a fresh, bounded control session: the presentation context was
	// canceled to interrupt SayText, while this speech needs a live context.
	control, err := robot.Conn.BehaviorControl(ctx)
	if err != nil {
		logger.Println("Productivity: NBA mute confirmation control unavailable: " + err.Error())
		return
	}
	if err := acquireBehaviorControl(control); err != nil {
		logger.Println("Productivity: NBA mute confirmation control failed: " + err.Error())
		return
	}
	defer func() {
		if err := releaseBehaviorControl(control); err != nil {
			logger.Println("Productivity: Failed to release NBA mute confirmation control: " + err.Error())
		}
	}()
	if err := sayConfirmationResponse(ctx, robot, "Game muted."); err != nil {
		logger.Println("Productivity: NBA mute confirmation speech failed: " + err.Error())
	}
}
