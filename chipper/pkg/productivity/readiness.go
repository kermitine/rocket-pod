package productivity

import (
	"context"
	"crypto/tls"
	"math"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

const (
	reminderReconnectGrace   = time.Minute
	reminderReadinessPoll    = 5 * time.Second
	reminderReadinessTimeout = 3 * time.Second
	reminderBatteryFreshness = 3 * reminderReadinessPoll
)

type reminderConnectionState struct {
	healthySince time.Time
	lastObserved time.Time
}

type reminderConnectionTracker struct {
	sync.Mutex
	states map[string]reminderConnectionState
}

var reminderConnections = reminderConnectionTracker{states: make(map[string]reminderConnectionState)}

func reminderBatteryReadingReady(battery *vectorpb.BatteryStateResponse) bool {
	volts := float64(battery.GetBatteryVolts())
	return battery != nil && reminderBatteryAllowsDelivery(battery.GetBatteryLevel()) && volts > 0 && !math.IsNaN(volts) && !math.IsInf(volts, 0)
}

func (tracker *reminderConnectionTracker) observe(esn string, battery *vectorpb.BatteryStateResponse, err error, now time.Time) {
	esn = strings.ToLower(strings.TrimSpace(esn))
	tracker.Lock()
	defer tracker.Unlock()
	state := tracker.states[esn]
	if now.Before(state.lastObserved) {
		return
	}
	if err != nil || !reminderBatteryReadingReady(battery) {
		state.healthySince = time.Time{}
	} else if state.healthySince.IsZero() || now.Sub(state.lastObserved) > reminderBatteryFreshness {
		state.healthySince = now
	}
	state.lastObserved = now
	tracker.states[esn] = state
}

func (tracker *reminderConnectionTracker) remaining(esn string, now time.Time) time.Duration {
	tracker.Lock()
	state := tracker.states[strings.ToLower(strings.TrimSpace(esn))]
	tracker.Unlock()
	if state.healthySince.IsZero() || now.Before(state.lastObserved) || now.Sub(state.lastObserved) > reminderBatteryFreshness {
		return reminderReconnectGrace
	}
	if remaining := state.healthySince.Add(reminderReconnectGrace).Sub(now); remaining > 0 {
		return remaining
	}
	return 0
}

func reminderReadinessDeferral(tracker *reminderConnectionTracker, esn string, battery *vectorpb.BatteryStateResponse, err error, now time.Time) (time.Duration, string) {
	tracker.observe(esn, battery, err, now)
	if err != nil || battery == nil {
		return reminderOfflineRetryDelay, "robot is offline"
	}
	if battery.GetBatteryLevel() != vectorpb.BatteryLevel_BATTERY_LEVEL_UNKNOWN && !reminderBatteryAllowsDelivery(battery.GetBatteryLevel()) {
		return reminderLowBatteryRetryDelay, "battery is not above the low level"
	}
	if !reminderBatteryReadingReady(battery) {
		return reminderOfflineRetryDelay, "battery reading is not ready"
	}
	if remaining := tracker.remaining(esn, now); remaining > 0 {
		return remaining, "waiting for 60 seconds of stable battery readings after reconnect"
	}
	return 0, ""
}

type reminderBatteryClient interface {
	BatteryState(context.Context, *vectorpb.BatteryStateRequest, ...grpc.CallOption) (*vectorpb.BatteryStateResponse, error)
}

func checkReminderReadiness(ctx context.Context, esn string, client reminderBatteryClient) (*vectorpb.BatteryStateResponse, time.Duration, string) {
	requestCtx, cancel := context.WithTimeout(ctx, reminderAvailabilityTimeout)
	defer cancel()
	battery, err := client.BatteryState(requestCtx, &vectorpb.BatteryStateRequest{})
	delay, reason := reminderReadinessDeferral(&reminderConnections, esn, battery, err, time.Now())
	return battery, delay, reason
}

type reminderMonitorConfig struct{ esn, ip, token string }
type reminderMonitor struct {
	config reminderMonitorConfig
	cancel context.CancelFunc
}

// Keep observing between reminders so an idle but connected robot does not need
// another warm-up every time a reminder is queued. Each worker owns its gRPC connection.
func reminderConnectionLoop(ctx context.Context) {
	workers := make(map[string]reminderMonitor)
	defer func() {
		for _, worker := range workers {
			worker.cancel()
		}
	}()
	ticker := time.NewTicker(reminderReadinessPoll)
	defer ticker.Stop()
	for {
		wanted := make(map[string]reminderMonitorConfig)
		for _, robot := range vars.BotInfo.Robots {
			if robot.Esn == "" || robot.IPAddress == "" || robot.GUID == "" {
				continue
			}
			wanted[robot.Esn] = reminderMonitorConfig{robot.Esn, robot.IPAddress, robot.GUID}
		}
		for esn, worker := range workers {
			if config, ok := wanted[esn]; !ok || config != worker.config {
				worker.cancel()
				delete(workers, esn)
				reminderConnections.observe(esn, nil, nil, time.Now())
			}
		}
		for esn, config := range wanted {
			if _, running := workers[esn]; running {
				continue
			}
			workerCtx, cancel := context.WithCancel(ctx)
			workers[esn] = reminderMonitor{config: config, cancel: cancel}
			reminderConnections.observe(esn, nil, nil, time.Now())
			go monitorReminderConnection(workerCtx, config)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func monitorReminderConnection(ctx context.Context, config reminderMonitorConfig) {
	// Vector's self-signed SDK connection uses the same credentials as bedtime monitoring.
	conn, err := grpc.DialContext(ctx, net.JoinHostPort(config.ip, "443"), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})))
	if err != nil {
		reminderConnections.observe(config.esn, nil, err, time.Now())
		return
	}
	defer conn.Close()
	client := vectorpb.NewExternalInterfaceClient(conn)
	authCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+config.token)
	ticker := time.NewTicker(reminderReadinessPoll)
	defer ticker.Stop()
	for {
		requestCtx, cancel := context.WithTimeout(authCtx, reminderReadinessTimeout)
		battery, err := client.BatteryState(requestCtx, &vectorpb.BatteryStateRequest{})
		cancel()
		if ctx.Err() != nil {
			return
		}
		reminderConnections.observe(config.esn, battery, err, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
