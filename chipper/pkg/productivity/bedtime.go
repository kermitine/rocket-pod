package productivity

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	_ "time/tzdata"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

const (
	bedtimeInteractionGrace = time.Minute
	bedtimeCheckInterval    = 5 * time.Second
	bedtimeHomeRetry        = 2 * time.Minute
	bedtimeSleepRetry       = 15 * time.Second
	bedtimeRequestTimeout   = 3 * time.Second
	bedtimeSleepPulse       = 750 * time.Millisecond
)

type bedtimeConfiguration struct {
	Schedule vars.BedtimeConfig
	RobotESN string
}

var bedtimeSettings atomic.Value

func refreshBedtimeConfiguration() {
	bedtimeSettings.Store(bedtimeConfiguration{vars.APIConfig.Productivity.Bedtime, productivityTargetRobot()})
}

func currentBedtimeConfiguration() bedtimeConfiguration {
	if value := bedtimeSettings.Load(); value != nil {
		return value.(bedtimeConfiguration)
	}
	return bedtimeConfiguration{}
}

func ValidateBedtimeConfig(config vars.BedtimeConfig) error {
	if !config.Enable {
		return nil
	}
	for _, value := range []string{config.Start, config.End} {
		if _, err := time.Parse("15:04", value); err != nil || len(value) != 5 {
			return fmt.Errorf("bedtime and morning must use HH:MM times")
		}
	}
	if config.Start == config.End {
		return fmt.Errorf("bedtime and morning must be different times")
	}
	if config.Timezone == "" || config.Timezone == "Local" {
		return fmt.Errorf("choose a bedtime timezone, such as America/Los_Angeles")
	}
	if _, err := time.LoadLocation(config.Timezone); err != nil {
		return fmt.Errorf("unknown bedtime timezone %q", config.Timezone)
	}
	return nil
}

func bedtimeActiveAt(config vars.BedtimeConfig, now time.Time) bool {
	if !config.Enable || ValidateBedtimeConfig(config) != nil {
		return false
	}
	location, _ := time.LoadLocation(config.Timezone)
	clock := now.In(location).Format("15:04")
	if config.Start < config.End {
		return clock >= config.Start && clock < config.End
	}
	return clock >= config.Start || clock < config.End
}

func isBedtimeForRobot(esn string, now time.Time) bool {
	config := currentBedtimeConfiguration()
	return esn != "" && strings.EqualFold(esn, config.RobotESN) && bedtimeActiveAt(config.Schedule, now)
}

func taskSuppressedAtBedtime(task Task, now time.Time) bool {
	// Explicit requests remain available when the user interacts with Vector.
	return task.Source != "standings" && task.Source != "test" && isBedtimeForRobot(task.RobotESN, now)
}

func cancelTaskAtBedtime(ctx context.Context, cancel context.CancelFunc, task Task) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if taskSuppressedAtBedtime(task, time.Now()) {
			cancel()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type bedtimeInteraction struct {
	Last time.Time
	Busy int
}

var bedtimeActivity = struct {
	sync.Mutex
	Robots  map[string]bedtimeInteraction
	Changed chan struct{}
}{Robots: make(map[string]bedtimeInteraction), Changed: make(chan struct{}, 1)}

func recordBedtimeInteraction(esn string, busyChange int, now time.Time) {
	bedtimeActivity.Lock()
	key := strings.ToLower(esn)
	activity := bedtimeActivity.Robots[key]
	activity.Last = now
	activity.Busy += busyChange
	bedtimeActivity.Robots[key] = activity
	bedtimeActivity.Unlock()
	select {
	case bedtimeActivity.Changed <- struct{}{}:
	default:
	}
}

// BeginBedtimeInteraction keeps long voice responses and explicitly requested
// SDK work awake until they finish, followed by a full interaction grace period.
func BeginBedtimeInteraction(esn string) func() {
	recordBedtimeInteraction(esn, 1, time.Now())
	return func() { recordBedtimeInteraction(esn, -1, time.Now()) }
}

func bedtimeInteractionActive(esn string, now time.Time) bool {
	bedtimeActivity.Lock()
	activity := bedtimeActivity.Robots[strings.ToLower(esn)]
	bedtimeActivity.Unlock()
	return activity.Busy > 0 || (!activity.Last.IsZero() && now.Sub(activity.Last) < bedtimeInteractionGrace)
}

func bedtimeRobotIsInteracting(state *vectorpb.RobotState) bool {
	return state != nil && (state.GetTouchData().GetIsBeingTouched() ||
		reminderRobotStateHas(state, vectorpb.RobotStatus_ROBOT_STATUS_IS_PICKED_UP) ||
		reminderRobotStateHas(state, vectorpb.RobotStatus_ROBOT_STATUS_IS_BEING_HELD) ||
		reminderRobotStateHas(state, vectorpb.RobotStatus_ROBOT_STATUS_IS_BUTTON_PRESSED))
}

// A separate loop keeps sports API requests from delaying bedtime enforcement.
func bedtimeLoop(ctx context.Context) {
	ticker := time.NewTicker(bedtimeCheckInterval)
	defer ticker.Stop()
	var running bedtimeConfiguration
	var stop context.CancelFunc
	var finished chan error
	stopSession := func() {
		if stop != nil {
			stop()
			<-finished
			stop, finished = nil, nil
		}
	}
	defer stopSession()
	for {
		config := currentBedtimeConfiguration()
		active := config.RobotESN != "" && bedtimeActiveAt(config.Schedule, time.Now())
		if stop != nil && (!active || config != running) {
			stopSession()
		}
		if active && stop == nil {
			running = config
			var sessionCtx context.Context
			sessionCtx, stop = context.WithCancel(ctx)
			finished = make(chan error, 1)
			go func(result chan<- error) { result <- runBedtimeSession(sessionCtx, config) }(finished)
		}
		select {
		case <-ctx.Done():
			return
		case err := <-finished:
			stop()
			stop, finished = nil, nil
			if err != nil {
				logger.Println("Bedtime: " + err.Error() + "; will reconnect")
			}
			// Wait before reconnecting to an unavailable robot.
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		case <-ticker.C:
		}
	}
}

func runBedtimeSession(ctx context.Context, config bedtimeConfiguration) error {
	var ip, token string
	for _, bot := range vars.BotInfo.Robots {
		if strings.EqualFold(bot.Esn, config.RobotESN) {
			ip, token = bot.IPAddress, bot.GUID
			break
		}
	}
	if ip == "" || token == "" {
		return fmt.Errorf("target robot has no connection details")
	}
	connectCtx, connectCancel := context.WithTimeout(ctx, bedtimeRequestTimeout)
	defer connectCancel()
	// Vector uses a self-signed certificate, as in the existing SDK connection.
	conn, err := grpc.DialContext(connectCtx, net.JoinHostPort(ip, "443"), grpc.WithBlock(),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})))
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	robot := vectorpb.NewExternalInterfaceClient(conn)
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	stream, err := robot.EventStream(streamCtx, &vectorpb.EventRequest{
		ListType: &vectorpb.EventRequest_WhiteList{WhiteList: &vectorpb.FilterList{
			List: []string{"robot_state", "wake_word", "user_intent"},
		}},
	})
	if err != nil {
		return err
	}

	var stateMu sync.Mutex
	var state *vectorpb.RobotState
	var stateAt time.Time
	streamError := make(chan error, 1)
	go func() {
		for {
			response, err := stream.Recv()
			if err != nil {
				streamError <- err
				return
			}
			event := response.GetEvent()
			now := time.Now()
			if nextState := event.GetRobotState(); nextState != nil {
				stateMu.Lock()
				state, stateAt = nextState, now
				stateMu.Unlock()
				if bedtimeRobotIsInteracting(nextState) {
					recordBedtimeInteraction(config.RobotESN, 0, now)
				}
			}
			if event.GetWakeWord() != nil || event.GetUserIntent() != nil {
				recordBedtimeInteraction(config.RobotESN, 0, now)
			}
		}
	}()

	consoleURL := "http://" + net.JoinHostPort(ip, "8889") + "/consolevarset?key=kSleepCycleForceSleep&value="
	client := &http.Client{Timeout: bedtimeRequestTimeout}
	defer client.CloseIdleConnections()
	// Clear a pulse interrupted by a previous server restart. Never latch this
	// flag overnight: voice and touch must still work if Rocket Pod disconnects.
	setNativeBedtimeSleep(context.Background(), client, consoleURL, false)
	ticker := time.NewTicker(bedtimeCheckInterval)
	defer ticker.Stop()
	var lastHome, lastSleep, lastFallback time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-streamError:
			return err
		case <-ticker.C:
			now := time.Now()
			if bedtimeInteractionActive(config.RobotESN, now) {
				lastHome, lastSleep, lastFallback = time.Time{}, time.Time{}, time.Time{}
				continue
			}
			stateMu.Lock()
			latest, latestAt := state, stateAt
			stateMu.Unlock()
			// Without fresh sensors we cannot safely distinguish an idle robot
			// from one being touched or held. Reconnect instead of forcing sleep.
			if latest == nil || now.Sub(latestAt) > 2*bedtimeCheckInterval {
				return fmt.Errorf("robot state stream is stale")
			}
			if !reminderRobotStateHas(latest, vectorpb.RobotStatus_ROBOT_STATUS_IS_ON_CHARGER) {
				if now.Sub(lastHome) < bedtimeHomeRetry {
					continue
				}
				requestCtx, cancel := context.WithTimeout(ctx, bedtimeRequestTimeout)
				_, err := robot.AppIntent(requestCtx, &vectorpb.AppIntentRequest{Intent: "intent_greeting_goodnight"})
				cancel()
				if err != nil {
					return err
				}
				lastHome = now
				continue
			}
			if now.Sub(lastSleep) >= bedtimeSleepRetry {
				if err := pulseNativeBedtimeSleep(ctx, client, consoleURL, config.RobotESN); err != nil {
					// Standard sleep enters a comatose phase that ignores touch.
					// Goodnight preserves touch reactions on firmware without the console.
					if ctx.Err() != nil {
						return nil
					}
					if !bedtimeInteractionActive(config.RobotESN, time.Now()) && now.Sub(lastFallback) >= bedtimeHomeRetry {
						logger.Println("Bedtime: Native sleep control unavailable; using goodnight: " + err.Error())
						requestCtx, cancel := context.WithTimeout(ctx, bedtimeRequestTimeout)
						_, fallbackErr := robot.AppIntent(requestCtx, &vectorpb.AppIntentRequest{Intent: "intent_greeting_goodnight"})
						cancel()
						if fallbackErr != nil {
							return fallbackErr
						}
						lastFallback = now
					}
				}
				lastSleep = now
			}
		}
	}
}

func setNativeBedtimeSleep(ctx context.Context, client *http.Client, consoleURL string, enabled bool) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, consoleURL+fmt.Sprint(enabled), nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("WireOS sleep control returned HTTP %d", response.StatusCode)
	}
	return nil
}

func pulseNativeBedtimeSleep(ctx context.Context, client *http.Client, consoleURL, esn string) (err error) {
	if ctx.Err() != nil || bedtimeInteractionActive(esn, time.Now()) {
		return nil
	}
	// Reset even if enabling times out: the robot may have received the request.
	defer func() {
		var resetErr error
		for attempt := 0; attempt < 3; attempt++ {
			resetCtx, cancel := context.WithTimeout(context.Background(), bedtimeRequestTimeout)
			resetErr = setNativeBedtimeSleep(resetCtx, client, consoleURL, false)
			cancel()
			if resetErr == nil {
				return
			}
		}
		err = fmt.Errorf("could not release WireOS sleep control: %w", resetErr)
	}()
	if err := setNativeBedtimeSleep(ctx, client, consoleURL, false); err != nil {
		return err
	}
	if bedtimeInteractionActive(esn, time.Now()) || ctx.Err() != nil {
		return nil
	}
	if err := setNativeBedtimeSleep(ctx, client, consoleURL, true); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if bedtimeInteractionActive(esn, time.Now()) {
		return nil
	}
	timer := time.NewTimer(bedtimeSleepPulse)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			return nil
		case <-bedtimeActivity.Changed:
			if bedtimeInteractionActive(esn, time.Now()) {
				return nil
			}
		}
	}
}
