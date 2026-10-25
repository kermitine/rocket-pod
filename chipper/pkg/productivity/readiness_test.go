package productivity

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"google.golang.org/grpc"
)

func healthyReminderBattery() *vectorpb.BatteryStateResponse {
	return &vectorpb.BatteryStateResponse{BatteryLevel: vectorpb.BatteryLevel_BATTERY_LEVEL_NOMINAL, BatteryVolts: 4.0, IsOnChargerPlatform: true}
}

func observeHealthyReminder(tracker *reminderConnectionTracker, esn string, start, end time.Time) {
	for now := start; !now.After(end); now = now.Add(reminderReadinessPoll) {
		tracker.observe(esn, healthyReminderBattery(), nil, now)
	}
}

func TestReminderReadinessRequiresMinuteOfFreshHealthyReadings(t *testing.T) {
	tracker := reminderConnectionTracker{states: make(map[string]reminderConnectionState)}
	start := time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC)
	if delay := tracker.remaining("robot", start); delay != time.Minute {
		t.Fatalf("unobserved robot delay=%v", delay)
	}
	observeHealthyReminder(&tracker, "robot", start, start.Add(55*time.Second))
	if delay := tracker.remaining("robot", start.Add(59*time.Second)); delay != time.Second {
		t.Fatalf("ready before grace period ended: %v", delay)
	}
	tracker.observe("robot", healthyReminderBattery(), nil, start.Add(time.Minute))
	if delay := tracker.remaining("robot", start.Add(time.Minute)); delay != 0 {
		t.Fatalf("healthy robot still delayed: %v", delay)
	}
	if delay := tracker.remaining("other-robot", start.Add(time.Minute)); delay != time.Minute {
		t.Fatal("one robot's connection warmed up another robot")
	}
	staleAt := start.Add(time.Minute + reminderBatteryFreshness + time.Second)
	if delay := tracker.remaining("robot", staleAt); delay != time.Minute {
		t.Fatal("stale observations allowed a reminder")
	}
	tracker.observe("robot", healthyReminderBattery(), nil, staleAt)
	if delay := tracker.remaining("robot", staleAt); delay != time.Minute {
		t.Fatal("a single observation after a monitoring gap skipped the grace period")
	}
}

func TestReminderReadinessRestartsAfterDisconnectOrUnsafeBattery(t *testing.T) {
	for _, test := range []struct {
		name    string
		battery *vectorpb.BatteryStateResponse
		err     error
	}{
		{"offline", nil, errors.New("offline")},
		{"missing", nil, nil},
		{"low", &vectorpb.BatteryStateResponse{BatteryLevel: vectorpb.BatteryLevel_BATTERY_LEVEL_LOW, BatteryVolts: 3.4}, nil},
		{"unknown", &vectorpb.BatteryStateResponse{BatteryLevel: vectorpb.BatteryLevel_BATTERY_LEVEL_UNKNOWN, BatteryVolts: 4}, nil},
		{"zero voltage", &vectorpb.BatteryStateResponse{BatteryLevel: vectorpb.BatteryLevel_BATTERY_LEVEL_NOMINAL}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			tracker := reminderConnectionTracker{states: make(map[string]reminderConnectionState)}
			start := time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC)
			observeHealthyReminder(&tracker, "robot", start, start.Add(time.Minute))
			tracker.observe("robot", test.battery, test.err, start.Add(61*time.Second))
			if tracker.remaining("robot", start.Add(61*time.Second)) == 0 {
				t.Fatal("unsafe reading did not reset readiness")
			}
			reconnected := start.Add(62 * time.Second)
			tracker.observe("robot", healthyReminderBattery(), nil, reconnected)
			if delay := tracker.remaining("robot", reconnected); delay != time.Minute {
				t.Fatalf("reconnect skipped grace period: %v", delay)
			}
			observeHealthyReminder(&tracker, "robot", reconnected.Add(reminderReadinessPoll), reconnected.Add(time.Minute))
			if delay := tracker.remaining("robot", reconnected.Add(time.Minute)); delay != 0 {
				t.Fatalf("healthy reconnection never became ready: %v", delay)
			}
		})
	}
}

func TestReminderReadinessRejectsUninitializedAndInvalidBatteryData(t *testing.T) {
	for _, volts := range []float32{0, -1, float32(math.NaN()), float32(math.Inf(1))} {
		battery := healthyReminderBattery()
		battery.BatteryVolts = volts
		if reminderBatteryReadingReady(battery) {
			t.Errorf("accepted invalid voltage %v", volts)
		}
	}
	tracker := reminderConnectionTracker{states: make(map[string]reminderConnectionState)}
	now := time.Now()
	battery := healthyReminderBattery()
	if delay, _ := reminderReadinessDeferral(&tracker, "robot", battery, nil, now); delay != time.Minute {
		t.Fatal("first nominal reading bypassed reconnect delay")
	}
	battery.BatteryLevel = vectorpb.BatteryLevel_BATTERY_LEVEL_LOW
	if delay, _ := reminderReadinessDeferral(&tracker, "robot", battery, nil, now.Add(time.Second)); delay != reminderLowBatteryRetryDelay {
		t.Fatal("low battery was not kept pending for charging")
	}
}

type readinessControlStream struct {
	vectorpb.ExternalInterface_BehaviorControlClient
}

func (*readinessControlStream) Send(*vectorpb.BehaviorControlRequest) error { return nil }
func (*readinessControlStream) Recv() (*vectorpb.BehaviorControlResponse, error) {
	return &vectorpb.BehaviorControlResponse{ResponseType: &vectorpb.BehaviorControlResponse_ControlGrantedResponse{ControlGrantedResponse: &vectorpb.ControlGrantedResponse{}}}, nil
}

type readinessRobotClient struct {
	vectorpb.ExternalInterfaceClient
	batteries                              []*vectorpb.BatteryStateResponse
	batteryCalls, controlCalls, driveCalls int
}

func (client *readinessRobotClient) BatteryState(context.Context, *vectorpb.BatteryStateRequest, ...grpc.CallOption) (*vectorpb.BatteryStateResponse, error) {
	index := client.batteryCalls
	client.batteryCalls++
	if index >= len(client.batteries) {
		return nil, errors.New("unexpected battery request")
	}
	return client.batteries[index], nil
}
func (client *readinessRobotClient) BehaviorControl(context.Context, ...grpc.CallOption) (vectorpb.ExternalInterface_BehaviorControlClient, error) {
	client.controlCalls++
	return &readinessControlStream{}, nil
}
func (client *readinessRobotClient) DriveOffCharger(context.Context, *vectorpb.DriveOffChargerRequest, ...grpc.CallOption) (*vectorpb.DriveOffChargerResponse, error) {
	client.driveCalls++
	return nil, errors.New("unexpected charger departure")
}

func clearReminderReadinessAfterTest(t *testing.T, esn string) {
	t.Helper()
	t.Cleanup(func() {
		reminderConnections.Lock()
		delete(reminderConnections.states, esn)
		reminderConnections.Unlock()
	})
}

func TestReconnectedReminderCannotTakeControlOnAnEarlyNominalReading(t *testing.T) {
	const esn = "warming-up-robot"
	clearReminderReadinessAfterTest(t, esn)
	now := time.Now()
	observeHealthyReminder(&reminderConnections, esn, now.Add(-2*time.Minute), now.Add(-time.Minute))
	reminderConnections.observe(esn, nil, errors.New("robot was offline"), now.Add(-time.Second))
	client := &readinessRobotClient{batteries: []*vectorpb.BatteryStateResponse{healthyReminderBattery()}}
	deliverReminderTask(Task{RobotESN: esn, Source: "test", configurationGeneration: currentConfigurationGeneration()}, &vector.Vector{Conn: client})
	if client.batteryCalls != 1 || client.controlCalls != 0 || client.driveCalls != 0 {
		t.Fatalf("reminder moved or took control during reconnect grace: %+v", client)
	}
}

func TestReminderRechecksBatteryAfterControlBeforeLeavingCharger(t *testing.T) {
	const esn = "battery-changed-after-control"
	clearReminderReadinessAfterTest(t, esn)
	now := time.Now()
	observeHealthyReminder(&reminderConnections, esn, now.Add(-reminderReconnectGrace), now)
	low := healthyReminderBattery()
	low.BatteryLevel = vectorpb.BatteryLevel_BATTERY_LEVEL_LOW
	low.BatteryVolts = 3.4
	client := &readinessRobotClient{batteries: []*vectorpb.BatteryStateResponse{healthyReminderBattery(), low}}
	deliverReminderTask(Task{RobotESN: esn, Source: "test", configurationGeneration: currentConfigurationGeneration()}, &vector.Vector{Conn: client})
	if client.batteryCalls != 2 || client.controlCalls != 1 || client.driveCalls != 0 {
		t.Fatalf("charger departure used the earlier battery sample: %+v", client)
	}
}
