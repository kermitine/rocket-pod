package productivity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"google.golang.org/grpc"
)

func TestBedtimeScheduleTimezoneAndBoundaries(t *testing.T) {
	config := vars.BedtimeConfig{Enable: true, Start: "21:00", End: "07:00", Timezone: "America/Los_Angeles"}
	for _, test := range []struct {
		time string
		want bool
	}{
		{"2026-06-22T03:59:59Z", false}, // 20:59 PDT
		{"2026-06-22T04:00:00Z", true},  // 21:00 PDT
		{"2026-06-22T13:59:59Z", true},
		{"2026-06-22T14:00:00Z", false},
		{"2026-12-22T04:59:59Z", false}, // 20:59 PST
		{"2026-12-22T05:00:00Z", true},
		{"2026-12-22T15:00:00Z", false},
		{"2026-11-01T08:30:00Z", true}, // both occurrences of 01:30 during DST change
		{"2026-11-01T09:30:00Z", true},
	} {
		t.Run(test.time, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, test.time)
			if err != nil {
				t.Fatal(err)
			}
			if got := bedtimeActiveAt(config, now); got != test.want {
				t.Fatalf("bedtime active = %v, want %v", got, test.want)
			}
		})
	}
	config.Start, config.End, config.Timezone = "13:00", "15:00", "UTC"
	for hour, want := range map[int]bool{12: false, 13: true, 14: true, 15: false} {
		if got := bedtimeActiveAt(config, time.Date(2026, 6, 22, hour, 0, 0, 0, time.UTC)); got != want {
			t.Errorf("daytime sleep at %d:00 = %v, want %v", hour, got, want)
		}
	}
}

func TestBedtimeRejectsInvalidEnabledSchedules(t *testing.T) {
	for _, config := range []vars.BedtimeConfig{
		{Enable: true, Start: "25:00", End: "07:00", Timezone: "UTC"},
		{Enable: true, Start: "21:00", End: "7:00", Timezone: "UTC"},
		{Enable: true, Start: "21:00", End: "21:00", Timezone: "UTC"},
		{Enable: true, Start: "21:00", End: "07:00", Timezone: "Not/AZone"},
		{Enable: true, Start: "21:00", End: "07:00", Timezone: "Local"},
		{Enable: true, Start: "21:00", End: "07:00"},
	} {
		if ValidateBedtimeConfig(config) == nil || bedtimeActiveAt(config, time.Now()) {
			t.Errorf("invalid schedule was accepted: %+v", config)
		}
	}
	if err := ValidateBedtimeConfig(vars.BedtimeConfig{}); err != nil {
		t.Fatalf("disabled empty schedule rejected: %v", err)
	}
}

func TestBedtimeSuppressesOnlyAutomaticTasksForSelectedRobot(t *testing.T) {
	previous := currentBedtimeConfiguration()
	t.Cleanup(func() { bedtimeSettings.Store(previous) })
	bedtimeSettings.Store(bedtimeConfiguration{
		RobotESN: "bedtime-robot",
		Schedule: vars.BedtimeConfig{Enable: true, Start: "21:00", End: "07:00", Timezone: "UTC"},
	})
	night := time.Date(2026, 6, 22, 23, 0, 0, 0, time.UTC)
	for _, source := range []string{"manual", "nba", "f1", "todoist", "standings", nbaGamesTodaySource, "test"} {
		task := Task{RobotESN: "bedtime-robot", Source: source}
		want := source != "standings" && source != nbaGamesTodaySource && source != "test"
		if got := taskSuppressedAtBedtime(task, night); got != want {
			t.Errorf("source %s suppressed = %v, want %v", source, got, want)
		}
		if taskSuppressedAtBedtime(task, night.Add(8*time.Hour)) {
			t.Errorf("source %s suppressed after morning", source)
		}
		task.RobotESN = "other-robot"
		if taskSuppressedAtBedtime(task, night) {
			t.Errorf("bedtime affected another robot's %s task", source)
		}
	}
}

func TestBedtimeVoiceInteractionRemainsActiveUntilResponseFinishes(t *testing.T) {
	const esn = "long-bedtime-conversation"
	endInteraction := BeginBedtimeInteraction(esn)
	if !bedtimeInteractionActive(esn, time.Now().Add(5*time.Minute)) {
		t.Fatal("long voice response lost its bedtime exemption")
	}
	endInteraction()
	if !bedtimeInteractionActive(esn, time.Now().Add(30*time.Second)) {
		t.Fatal("interaction did not receive its full rest delay")
	}
	if bedtimeInteractionActive(esn, time.Now().Add(time.Minute)) {
		t.Fatal("completed interaction prevented returning to sleep")
	}
}

func TestBedtimeRecognizesTouchPickupAndButton(t *testing.T) {
	states := []*vectorpb.RobotState{
		{TouchData: &vectorpb.TouchData{IsBeingTouched: true}},
		{Status: uint32(vectorpb.RobotStatus_ROBOT_STATUS_IS_PICKED_UP)},
		{Status: uint32(vectorpb.RobotStatus_ROBOT_STATUS_IS_BEING_HELD)},
		{Status: uint32(vectorpb.RobotStatus_ROBOT_STATUS_IS_BUTTON_PRESSED)},
	}
	for _, state := range states {
		if !bedtimeRobotIsInteracting(state) {
			t.Errorf("missed user interaction: %v", state)
		}
	}
	if bedtimeRobotIsInteracting(nil) || bedtimeRobotIsInteracting(&vectorpb.RobotState{Status: uint32(vectorpb.RobotStatus_ROBOT_STATUS_IS_ANIMATING)}) {
		t.Fatal("autonomous animation was treated as user interaction")
	}
}

type bedtimeIntentRobot struct {
	vectorpb.ExternalInterfaceClient
	intents  []string
	response *vectorpb.AppIntentResponse
	err      error
}

func (robot *bedtimeIntentRobot) AppIntent(_ context.Context, request *vectorpb.AppIntentRequest, _ ...grpc.CallOption) (*vectorpb.AppIntentResponse, error) {
	robot.intents = append(robot.intents, request.Intent)
	return robot.response, robot.err
}

func TestBedtimeReturnsHomeAfterTestNotificationFinishes(t *testing.T) {
	const esn = "bedtime-after-nba-test"
	t.Cleanup(func() {
		bedtimeActivity.Lock()
		delete(bedtimeActivity.Robots, esn)
		bedtimeActivity.Unlock()
	})
	robot := &bedtimeIntentRobot{response: &vectorpb.AppIntentResponse{
		Status: &vectorpb.ResponseStatus{Code: vectorpb.ResponseStatus_REQUEST_PROCESSING},
	}}
	enforcer := bedtimeEnforcer{robot: robot, esn: esn, lastHome: time.Now()}
	// An NBA test is allowed during bedtime and keeps the robot awake until
	// delivery finishes, even if delivery lasts longer than the grace period.
	endInteraction := BeginBedtimeInteraction(esn)
	state := &vectorpb.RobotState{Status: uint32(vectorpb.RobotStatus_ROBOT_STATUS_IS_MOVING | vectorpb.RobotStatus_ROBOT_STATUS_IS_ANIMATING)}
	now := time.Now().Add(5 * time.Minute)
	if err := enforcer.check(context.Background(), state, now, now); err != nil {
		t.Fatal(err)
	}
	endInteraction()
	bedtimeActivity.Lock()
	finished := bedtimeActivity.Robots[esn].Last
	bedtimeActivity.Unlock()
	now = finished.Add(bedtimeInteractionGrace - time.Second)
	if err := enforcer.check(context.Background(), state, now, now); err != nil {
		t.Fatal(err)
	}
	if len(robot.intents) != 0 {
		t.Fatal("bedtime interrupted the notification or its full interaction grace period")
	}
	now = finished.Add(bedtimeInteractionGrace)
	if err := enforcer.check(context.Background(), state, now, now); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(robot.intents, []string{"intent_system_charger"}) {
		t.Fatalf("idle robot was not sent home with a supported app intent: %v", robot.intents)
	}
	if err := enforcer.check(context.Background(), state, now.Add(time.Second), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(robot.intents) != 1 {
		t.Fatal("go-home retries were not rate limited")
	}
	now = now.Add(bedtimeHomeRetry)
	if err := enforcer.check(context.Background(), state, now, now); err != nil {
		t.Fatal(err)
	}
	if len(robot.intents) != 2 {
		t.Fatal("robot still off the charger was not sent home again")
	}
}

func TestBedtimeGoHomeChecksRobotResponse(t *testing.T) {
	for _, test := range []struct {
		name     string
		response *vectorpb.AppIntentResponse
		err      error
		wantErr  bool
	}{
		{"received", &vectorpb.AppIntentResponse{Status: &vectorpb.ResponseStatus{Code: vectorpb.ResponseStatus_RESPONSE_RECEIVED}}, nil, false},
		{"processing", &vectorpb.AppIntentResponse{Status: &vectorpb.ResponseStatus{Code: vectorpb.ResponseStatus_REQUEST_PROCESSING}}, nil, false},
		{"ok", &vectorpb.AppIntentResponse{Status: &vectorpb.ResponseStatus{Code: vectorpb.ResponseStatus_OK}}, nil, false},
		{"forbidden", &vectorpb.AppIntentResponse{Status: &vectorpb.ResponseStatus{Code: vectorpb.ResponseStatus_FORBIDDEN}}, nil, true},
		{"not found", &vectorpb.AppIntentResponse{Status: &vectorpb.ResponseStatus{Code: vectorpb.ResponseStatus_NOT_FOUND}}, nil, true},
		{"busy", &vectorpb.AppIntentResponse{Status: &vectorpb.ResponseStatus{Code: vectorpb.ResponseStatus_ERROR_UPDATE_IN_PROGRESS}}, nil, true},
		{"unknown", &vectorpb.AppIntentResponse{Status: &vectorpb.ResponseStatus{Code: vectorpb.ResponseStatus_UNKNOWN}}, nil, true},
		{"missing status", &vectorpb.AppIntentResponse{}, nil, true},
		{"missing response", nil, nil, true},
		{"offline", nil, errors.New("offline"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			robot := &bedtimeIntentRobot{response: test.response, err: test.err}
			if err := requestBedtimeCharger(context.Background(), robot); (err != nil) != test.wantErr {
				t.Fatalf("go-home error=%v, want error=%v", err, test.wantErr)
			}
		})
	}
}

func TestBedtimeDoesNotSendHomeWithoutFreshSensors(t *testing.T) {
	now := time.Now()
	for _, state := range []*vectorpb.RobotState{nil, {}} {
		robot := &bedtimeIntentRobot{}
		enforcer := bedtimeEnforcer{robot: robot, esn: "stale-bedtime-robot"}
		if err := enforcer.check(context.Background(), state, now.Add(-3*bedtimeCheckInterval), now); err == nil {
			t.Fatal("missing or stale sensors did not trigger reconnection")
		}
		if len(robot.intents) != 0 {
			t.Fatal("sent the robot home without fresh interaction sensors")
		}
	}
}

func TestBedtimeWithoutNativeSleepKeepsRobotDocked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()
	robot := &bedtimeIntentRobot{response: &vectorpb.AppIntentResponse{
		Status: &vectorpb.ResponseStatus{Code: vectorpb.ResponseStatus_REQUEST_PROCESSING},
	}}
	enforcer := bedtimeEnforcer{robot: robot, client: server.Client(), consoleURL: server.URL + "?value=", esn: "bedtime-without-console"}
	state := &vectorpb.RobotState{Status: uint32(vectorpb.RobotStatus_ROBOT_STATUS_IS_ON_CHARGER)}
	now := time.Now()
	enforcer.lastHome = now.Add(-time.Second)
	if err := enforcer.check(context.Background(), state, now, now); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(robot.intents, []string{"intent_system_charger"}) {
		t.Fatalf("native sleep fallback used an unsupported intent or touch-blocking sleep: %v", robot.intents)
	}
	now = now.Add(bedtimeSleepRetry)
	if err := enforcer.check(context.Background(), state, now, now); err != nil {
		t.Fatal(err)
	}
	if len(robot.intents) != 1 {
		t.Fatal("native sleep fallback was not rate limited")
	}
	// Docking clears the old go-home retry delay: a later departure should
	// trigger a new request on the next check, rather than wait two minutes.
	state.Status = 0
	if err := enforcer.check(context.Background(), state, now, now); err != nil {
		t.Fatal(err)
	}
	if len(robot.intents) != 2 {
		t.Fatal("robot leaving the charger was not sent home again")
	}
}

func TestNativeBedtimePulseReleasesSleepForInteractionAndCancellation(t *testing.T) {
	for _, action := range []string{"voice-or-touch", "shutdown", "enable-error"} {
		t.Run(action, func(t *testing.T) {
			esn := "pulse-" + action
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var mu sync.Mutex
			var values []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				value := r.URL.Query().Get("value")
				mu.Lock()
				values = append(values, value)
				mu.Unlock()
				if value == "true" {
					switch action {
					case "voice-or-touch":
						recordBedtimeInteraction(esn, 0, time.Now())
					case "shutdown":
						cancel()
					case "enable-error":
						http.Error(w, "failed", http.StatusServiceUnavailable)
					}
				}
			}))
			defer server.Close()
			start := time.Now()
			err := pulseNativeBedtimeSleep(ctx, server.Client(), server.URL+"?value=", esn)
			if action == "enable-error" && err == nil {
				t.Fatal("native sleep failure was ignored")
			}
			if action != "enable-error" && err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(values, []string{"false", "true", "false"}) {
				t.Fatalf("native sleep sequence = %v, want reset, enable, release", values)
			}
			if time.Since(start) >= bedtimeSleepPulse {
				t.Fatal("sleep enforcement did not yield immediately")
			}
		})
	}
}

func TestNativeBedtimePulseRetriesRelease(t *testing.T) {
	esn := "pulse-release-retry"
	var mu sync.Mutex
	var enabled bool
	releases := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Query().Get("value") == "true" {
			enabled = true
			recordBedtimeInteraction(esn, 0, time.Now())
		} else if enabled {
			releases++
			if releases < 3 {
				http.Error(w, "retry release", http.StatusServiceUnavailable)
				return
			}
			enabled = false
		}
		fmt.Fprint(w, "OK")
	}))
	defer server.Close()
	if err := pulseNativeBedtimeSleep(context.Background(), server.Client(), server.URL+"?value=", esn); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if enabled || releases != 3 {
		t.Fatalf("sleep flag was not released after retries: enabled=%v, attempts=%d", enabled, releases)
	}
}

type bedtimeScoreboardTransport func(*http.Request) (*http.Response, error)

func (transport bedtimeScoreboardTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestBedtimeSkipsSportsFinalsWithoutMorningReplay(t *testing.T) {
	previousSettings, previousConfig, previousClient := currentBedtimeConfiguration(), vars.APIConfig.Productivity, externalApiClient
	t.Cleanup(func() {
		bedtimeSettings.Store(previousSettings)
		vars.APIConfig.Productivity, externalApiClient = previousConfig, previousClient
		resetNBANotificationState()
		resetF1NotificationState()
	})
	now := time.Now().UTC()
	schedule := vars.BedtimeConfig{
		Enable: true, Timezone: "UTC", Start: now.Add(-time.Hour).Format("15:04"), End: now.Add(time.Hour).Format("15:04"),
	}
	bedtimeSettings.Store(bedtimeConfiguration{Schedule: schedule, RobotESN: "night-robot"})
	vars.APIConfig.Productivity.TargetRobot = "night-robot"
	vars.APIConfig.Productivity.NBA = vars.NBAConfig{Enable: true, FavoriteTeams: []string{"LAL"}, NotifyFinal: true}
	vars.APIConfig.Productivity.F1 = vars.F1Config{Enable: true, NotifyFinal: true, NotifyQualifying: true, NotifyPractice: true}
	resetNBANotificationState()
	resetF1NotificationState()
	game := testNBAGame("night-game", "in", now, "100", "99")
	nbaNotificationForGame(game, vars.APIConfig.Productivity.NBA, now)
	game.Status.Type.State = "post"
	_, session := syntheticF1Race()
	f1NotificationForSession(session, vars.APIConfig.Productivity.F1, now, true)
	session.Status.Type.State = "post"
	externalApiClient = &http.Client{Transport: bedtimeScoreboardTransport(func(request *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(request.URL.Path, "/scoreboard") {
			t.Errorf("suppressed notification fetched extra data: %s", request.URL.Path)
		}
		var scoreboard interface{} = f1Scoreboard{Events: []f1Event{{Competitions: []f1Competition{session}}}}
		if strings.Contains(request.URL.Path, "/basketball/") {
			scoreboard = nbaScoreboard{Events: []nbaEvent{game}}
		}
		body, err := json.Marshal(scoreboard)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, err
	})}
	for _, night := range []bool{true, false} {
		schedule.Enable = night
		bedtimeSettings.Store(bedtimeConfiguration{Schedule: schedule, RobotESN: "night-robot"})
		checkNBAGames()
		checkF1Races()
		select {
		case task := <-taskQueue:
			t.Fatalf("night=%v: unwanted sports notification queued: %s", night, task.ID)
		default:
		}
	}
}

func TestBedtimeCancelsRunningAutomaticNotification(t *testing.T) {
	previous := currentBedtimeConfiguration()
	t.Cleanup(func() { bedtimeSettings.Store(previous) })
	now := time.Now().UTC()
	bedtimeSettings.Store(bedtimeConfiguration{
		RobotESN: "cancel-at-bedtime",
		Schedule: vars.BedtimeConfig{Enable: true, Start: now.Add(-time.Hour).Format("15:04"), End: now.Add(time.Hour).Format("15:04"), Timezone: "UTC"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cancelTaskAtBedtime(ctx, cancel, Task{RobotESN: "cancel-at-bedtime", Source: "manual"})
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("running automatic notification was not cancelled at bedtime")
	}
}
