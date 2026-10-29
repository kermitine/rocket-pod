package productivity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"google.golang.org/grpc"
)

func setupSportsMuteTest(t *testing.T, capacity int) {
	t.Helper()
	setupSportsTaskQueue(t, capacity)
	taskQueueMu.Lock()
	previous := mutedSportsEvents
	mutedSportsEvents = make(map[sportsMuteKey]bool)
	taskQueueMu.Unlock()
	t.Cleanup(func() {
		taskQueueMu.Lock()
		mutedSportsEvents = previous
		taskQueueMu.Unlock()
	})
}

func nbaMuteTestTask(gameID, robot, kind string) Task {
	return sportsMuteTestTask("nba", gameID, robot, kind)
}

func sportsMuteTestTask(source, eventID, robot, kind string) Task {
	task := sportsQueueTestTask(source, source+"_"+eventID+"_"+kind, robot)
	task.sportsEventSource = source
	task.sportsEventID, task.sportsFinal = eventID, kind == "final"
	return task
}

func sportsTouchState(touched bool) *vectorpb.RobotState {
	return &vectorpb.RobotState{TouchData: &vectorpb.TouchData{IsBeingTouched: touched}}
}

func TestSportsMuteRequiresOneSecondContinuousTouchDuringDelivery(t *testing.T) {
	for _, test := range []struct {
		name    string
		touched []bool
		times   []int // Milliseconds since the first sample.
		mutesAt []int
	}{
		{"one second without release", []bool{false, true, true, true}, []int{0, 100, 1099, 1100}, []int{1100}},
		{"short tap", []bool{false, true, false}, []int{0, 100, 200}, nil},
		{"less than one second", []bool{false, true, true}, []int{0, 100, 1099}, nil},
		{"release before threshold", []bool{false, true, false, false}, []int{0, 100, 1099, 1500}, nil},
		{"release at threshold", []bool{false, true, false}, []int{0, 100, 1100}, nil},
		{"interrupted touches do not add up", []bool{false, true, true, false, true, true, false}, []int{0, 100, 700, 800, 900, 1500, 1600}, nil},
		{"touch held before delivery", []bool{true, true, true, false}, []int{0, 1000, 2000, 2100}, nil},
		{"fresh touch after held at startup", []bool{true, true, false, true, true}, []int{0, 1000, 1100, 1200, 2200}, []int{2200}},
		{"long hold triggers once", []bool{false, true, true, true, true, false}, []int{0, 100, 1100, 1200, 3000, 3100}, []int{1100}},
		{"separate holds", []bool{false, true, true, false, true, true}, []int{0, 100, 1100, 1200, 1300, 2300}, []int{1100, 2300}},
		{"untouched", []bool{false, false}, []int{0, 2000}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			var detector sportsTouchHoldDetector
			now := time.Now()
			if detector.observe(nil, now) {
				t.Fatal("missing robot state triggered a mute")
			}
			var got []int
			for index, touched := range test.touched {
				if detector.observe(sportsTouchState(touched), now.Add(time.Duration(test.times[index])*time.Millisecond)) {
					got = append(got, test.times[index])
				}
			}
			if !reflect.DeepEqual(got, test.mutesAt) {
				t.Fatalf("muted at %v ms, want %v ms", got, test.mutesAt)
			}
		})
	}
}

func TestSportsMuteRawTouchHoldWhenReportedTouchIsFalse(t *testing.T) {
	for _, test := range []struct {
		name    string
		raw     []uint32
		times   []int
		mutesAt []int
	}{
		{"one second raw touch", []uint32{1000, 1100, 1100, 1100}, []int{0, 100, 1099, 1100}, []int{1100}},
		{"sensor noise below threshold", []uint32{1000, 1049, 1050, 1000}, []int{0, 100, 1100, 1200}, nil},
		{"held touch with noisy readings", []uint32{1000, 1100, 1040, 1045}, []int{0, 100, 500, 1100}, []int{1100}},
		{"short raw tap", []uint32{1000, 1100, 1000, 1000}, []int{0, 100, 1099, 1100}, nil},
		{"interrupted raw touches", []uint32{1000, 1100, 1100, 1000, 1100, 1100, 1000}, []int{0, 100, 700, 800, 900, 1500, 1600}, nil},
		{"initially held raw sensor", []uint32{1100, 1100, 1100}, []int{0, 1000, 2000}, nil},
		{"release and fresh raw touch", []uint32{1100, 1100, 1000, 1100, 1100}, []int{0, 1000, 1100, 1200, 2200}, []int{2200}},
		{"zero reading is not a baseline", []uint32{0, 1000, 1000, 1000}, []int{0, 100, 1100, 1200}, nil},
		{"long raw hold triggers once", []uint32{1000, 1100, 1100, 1100, 1100}, []int{0, 100, 1100, 1200, 3000}, []int{1100}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var detector sportsTouchHoldDetector
			now := time.Now()
			var got []int
			for index, raw := range test.raw {
				state := sportsTouchState(false)
				state.TouchData.RawTouchValue = raw
				if detector.observe(state, now.Add(time.Duration(test.times[index])*time.Millisecond)) {
					got = append(got, test.times[index])
				}
			}
			if !reflect.DeepEqual(got, test.mutesAt) {
				t.Fatalf("muted at %v ms, want %v ms with reported touch always false", got, test.mutesAt)
			}
		})
	}
}

func TestSportsMuteRawTouchSurvivesReportedTouchFlicker(t *testing.T) {
	var detector sportsTouchHoldDetector
	now := time.Now()
	for index, sample := range []struct {
		raw     uint32
		touched bool
		at      time.Duration
	}{
		{1000, false, 0},
		{1100, true, 100 * time.Millisecond},
		{1040, false, 500 * time.Millisecond},
		{1045, false, 1100 * time.Millisecond},
	} {
		state := sportsTouchState(sample.touched)
		state.TouchData.RawTouchValue = sample.raw
		if muted, want := detector.observe(state, now.Add(sample.at)), index == 3; muted != want {
			t.Fatalf("sample %d: muted=%t, want %t despite the interpreted touch flag dropping", index, muted, want)
		}
	}
}

func TestSportsMuteMissingTouchDataResetsHold(t *testing.T) {
	for _, test := range []struct {
		name  string
		state *vectorpb.RobotState
	}{
		{"missing robot state", nil},
		{"missing touch data", &vectorpb.RobotState{}},
		{"button press without touch data", &vectorpb.RobotState{Status: uint32(vectorpb.RobotStatus_ROBOT_STATUS_IS_BUTTON_PRESSED)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var detector sportsTouchHoldDetector
			now := time.Now()
			for index, state := range []*vectorpb.RobotState{
				sportsTouchState(false), sportsTouchState(true), test.state,
				sportsTouchState(true), sportsTouchState(true),
				sportsTouchState(false), sportsTouchState(true),
			} {
				if detector.observe(state, now.Add(time.Duration(index)*time.Second)) {
					t.Fatal("missing sensor data allowed an interrupted or existing touch to mute")
				}
			}
			if !detector.observe(sportsTouchState(true), now.Add(7*time.Second)) {
				t.Fatal("fresh one-second touch after missing sensor data did not mute")
			}
		})
	}
}

func TestSportsMuteIgnoresButtonPresses(t *testing.T) {
	var detector sportsTouchHoldDetector
	now := time.Now()
	for index, pressed := range []bool{false, true, true, false} {
		state := sportsTouchState(false)
		if pressed {
			state.Status = uint32(vectorpb.RobotStatus_ROBOT_STATUS_IS_BUTTON_PRESSED)
		}
		if detector.observe(state, now.Add(time.Duration(index)*time.Second)) {
			t.Fatal("a button press without touching the sensor triggered a mute")
		}
	}
}

func TestSportsMuteCancelsOnlyMatchingEventAndRobot(t *testing.T) {
	for _, source := range []string{"nba", "f1"} {
		t.Run(source, func(t *testing.T) {
			setupSportsMuteTest(t, 5)
			current := sportsMuteTestTask(source, "game", "ROBOT-A", "live")
			enqueueSportsNotification(current, "game")
			current = <-taskQueue
			deferTask(current, "offline", time.Hour)
			// A newer update is already queued while the old one is being spoken.
			enqueueSportsNotification(sportsMuteTestTask(source, "game", "robot-a", "notable"), "game")
			newer := <-taskQueue
			deferTask(newer, "offline", time.Hour)
			enqueueTask(newer)
			enqueueSportsNotification(sportsMuteTestTask(source, "other-game", "robot-a", "live"), "other-game")
			enqueueSportsNotification(sportsMuteTestTask(source, "game", "robot-b", "live"), "game")
			otherSource := "f1"
			if source == "f1" {
				otherSource = "nba"
			}
			enqueueSportsNotification(sportsMuteTestTask(otherSource, "game", "robot-a", "live"), "game")
			enqueueTask(sportsQueueTestTask("manual", "reminder", "robot-a"))
			muteSportsEvent(current)
			if !taskIsSuperseded(newer) || taskCanRun(current) || taskCanRun(newer) || enqueueTask(newer) || enqueueSportsNotification(sportsMuteTestTask(source, "game", "robot-a", "live"), "game") {
				t.Fatal("a muted game can still run, retry, or enqueue a newer update")
			}
			var got []string
			for len(taskQueue) > 0 {
				task := <-taskQueue
				if !taskCanRun(task) {
					t.Fatal("mute affected an unrelated notification")
				}
				got = append(got, task.ID)
			}
			if want := []string{source + "_other-game_live", source + "_game_live", otherSource + "_game_live", "reminder"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("remaining queue=%v, want %v", got, want)
			}
			NotifyConfigUpdated()
			if !sportsEventIsMuted(source, "robot-a", "game") {
				t.Fatal("saving settings removed the game mute")
			}
		})
	}
}

func TestSportsMutePreservesFinalQueuedWhileLiveUpdateIsSpeaking(t *testing.T) {
	for _, source := range []string{"nba", "f1"} {
		t.Run(source, func(t *testing.T) {
			setupSportsMuteTest(t, 1)
			live := sportsMuteTestTask(source, "game", "robot", "live")
			enqueueSportsNotification(live, "game")
			live = <-taskQueue
			final := sportsMuteTestTask(source, "game", "robot", "final")
			enqueueSportsNotification(final, "game")
			muteSportsEvent(live)
			if len(taskQueue) != 1 {
				t.Fatal("muting the live announcement dropped the pending final score")
			}
			if queued := <-taskQueue; !queued.sportsFinal || !taskCanRun(queued) {
				t.Fatal("the final score was muted or superseded")
			}
			if !enqueueTask(final) {
				t.Fatal("a final score could not retry while the game was muted")
			}
		})
	}
}

func TestNBAMutedGamePollingSkipsUpdatesAndStillQueuesFinal(t *testing.T) {
	setupSportsMuteTest(t, 1)
	previousConfig, previousClient := vars.APIConfig.Productivity, externalApiClient
	t.Cleanup(func() {
		vars.APIConfig.Productivity, externalApiClient = previousConfig, previousClient
		resetNBANotificationState()
	})
	resetNBANotificationState()
	vars.APIConfig.Productivity = vars.ProductivityConfig{
		TargetRobot: "muted-polling", Timezone: "UTC",
		NBA: vars.NBAConfig{Enable: true, FavoriteTeams: []string{"LAL"}, PregameMinutes: 15, LiveUpdateMinutes: 1, NotifyNotable: true, NotifyFinal: true},
	}
	game := testNBAGame("muted-game", "pre", time.Now().Add(5*time.Minute), "0", "0")
	nonScoreboardRequests := 0
	externalApiClient = &http.Client{Transport: bedtimeScoreboardTransport(func(request *http.Request) (*http.Response, error) {
		data := []byte(`{}`)
		if strings.HasSuffix(request.URL.Path, "/scoreboard") {
			var err error
			data, err = json.Marshal(nbaScoreboard{Events: []nbaEvent{game}})
			if err != nil {
				t.Fatal(err)
			}
		} else {
			nonScoreboardRequests++
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})}
	muteSportsEvent(nbaMuteTestTask(game.ID, "muted-polling", "pregame"))
	for _, state := range []string{"pre", "in"} {
		game.Status.Type.State = state
		checkNBAGames()
		if len(taskQueue) != 0 {
			t.Fatal("muted pregame/live update was queued")
		}
	}
	// Overnight polling can fetch both yesterday's and today's scoreboards.
	if nonScoreboardRequests != 0 {
		t.Fatal("muted game unnecessarily fetched injuries, logos, or a summary")
	}
	game.Status.Type.State = "post"
	checkNBAGames()
	if len(taskQueue) != 1 {
		t.Fatal("muted game's final score was not queued")
	}
	final := <-taskQueue
	if !final.sportsFinal || final.sportsEventID != game.ID || !taskCanRun(final) {
		t.Fatal("final score lost game metadata or was suppressed")
	}
}

func TestF1MutedSessionPollingSkipsUpdatesAndStillQueuesFinal(t *testing.T) {
	for _, abbreviation := range []string{"Race", "Qual", "FP1"} {
		t.Run(abbreviation, func(t *testing.T) {
			_, session := syntheticF1Race()
			session.Type.Abbreviation = abbreviation
			session.Status.Type.State = "pre"
			session.Date = time.Now().Add(30 * time.Minute).Format(time.RFC3339)
			setupF1UnchangedPolling(t, &session, 0)
			setupSportsMuteTest(t, 1)
			vars.APIConfig.Productivity.F1.NotifyNotable = true
			const robot = "f1-unchanged-test"
			muteSportsEvent(sportsMuteTestTask("f1", session.ID, robot, "pregame"))
			checkF1Races()
			session.Status.Type.State = "in"
			checkF1Races()
			session.Competitors[0].Order, session.Competitors[1].Order = 2, 1
			session.Status.Type.ShortDetail = "Q2 - 4:12"
			checkF1Races()
			if len(taskQueue) != 0 {
				t.Fatal("muted session queued a reminder, leaderboard, or notable moment")
			}
			session.Status.Type.State = "post"
			checkF1Races()
			if len(taskQueue) != 1 {
				t.Fatal("muted session's final result was not queued")
			}
			final := <-taskQueue
			if !final.sportsFinal || final.sportsEventID != session.ID || !taskCanRun(final) {
				t.Fatal("final result lost session metadata or was suppressed")
			}
		})
	}
}

func TestF1TestUpdatesIncludeMuteMetadata(t *testing.T) {
	for _, test := range []struct{ session, kind, eventID string }{
		{"race", "live", "f1-test-race"},
		{"qualifying", "live", "f1-test-qualifying"},
		{"practice", "live", "f1-test-practice"},
		{"qualifying", "final", "f1-test-qualifying"},
	} {
		t.Run(test.session+"_"+test.kind, func(t *testing.T) {
			setupSportsMuteTest(t, 1)
			if err := injectTestF1Leaderboard("robot", test.session, test.kind); err != nil {
				t.Fatal(err)
			}
			task := <-taskQueue
			if task.sportsEventSource != "f1" || task.sportsEventID != test.eventID || task.sportsFinal != (test.kind == "final") {
				t.Fatal("test update did not include its session ID and final-result flag")
			}
		})
	}
}

type sportsMuteEventStream struct {
	vectorpb.ExternalInterface_EventStreamClient
	ctx    context.Context
	events <-chan *vectorpb.EventResponse
}

func (stream *sportsMuteEventStream) Recv() (*vectorpb.EventResponse, error) {
	select {
	case <-stream.ctx.Done():
		return nil, stream.ctx.Err()
	case event := <-stream.events:
		return event, nil
	}
}

type sportsMuteControlStream struct {
	readinessControlStream
	ctx        context.Context
	released   bool
	releasedAt time.Time
	failed     bool
	calls      *[]string
}

func (stream *sportsMuteControlStream) Send(request *vectorpb.BehaviorControlRequest) error {
	if request.GetControlRequest() != nil {
		*stream.calls = append(*stream.calls, "acquire")
	}
	if request.GetControlRelease() != nil {
		stream.released, stream.failed = true, stream.ctx.Err() != nil
		stream.releasedAt = time.Now()
		*stream.calls = append(*stream.calls, "release")
	}
	return nil
}

type sportsMuteRobotClient struct {
	batchRobotClient
	events                   chan *vectorpb.EventResponse
	speaking                 chan struct{}
	control                  *sportsMuteControlStream
	controls                 []*sportsMuteControlStream
	calls                    []string
	chime                    *fakeAcknowledgementStream
	chimeError               error
	muteVoice                bool
	muteSpeechDurationScalar float32
	onSpeech                 func(context.Context, *vectorpb.SayTextRequest) (*vectorpb.SayTextResponse, error)
	eventRequests            int
	eventError               error
}

func (client *sportsMuteRobotClient) BehaviorControl(ctx context.Context, _ ...grpc.CallOption) (vectorpb.ExternalInterface_BehaviorControlClient, error) {
	client.control = &sportsMuteControlStream{ctx: ctx, calls: &client.calls}
	client.controls = append(client.controls, client.control)
	return client.control, nil
}

func (client *sportsMuteRobotClient) EventStream(ctx context.Context, _ *vectorpb.EventRequest, _ ...grpc.CallOption) (vectorpb.ExternalInterface_EventStreamClient, error) {
	client.eventRequests++
	return &sportsMuteEventStream{ctx: ctx, events: client.events}, client.eventError
}

func (client *sportsMuteRobotClient) SayText(ctx context.Context, request *vectorpb.SayTextRequest, _ ...grpc.CallOption) (*vectorpb.SayTextResponse, error) {
	client.speech = append(client.speech, request.Text)
	client.calls = append(client.calls, "say:"+request.Text)
	if request.Text == "Game muted." || request.Text == "Session muted." {
		client.muteVoice = request.UseVectorVoice
		client.muteSpeechDurationScalar = request.DurationScalar
	}
	if client.speaking != nil && len(client.speech) == 1 {
		close(client.speaking)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if client.onSpeech != nil {
		return client.onSpeech(ctx, request)
	}
	return &vectorpb.SayTextResponse{State: vectorpb.SayTextResponse_FINISHED}, nil
}

func (client *sportsMuteRobotClient) ExternalAudioStreamPlayback(ctx context.Context, _ ...grpc.CallOption) (vectorpb.ExternalInterface_ExternalAudioStreamPlaybackClient, error) {
	client.calls = append(client.calls, "chime")
	client.chime = successfulAcknowledgementStream()
	client.chime.ctx = ctx
	return client.chime, client.chimeError
}

func TestSportsTouchHoldInterruptsSpeechAndReleasesControlWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		source, taskSource string
		rawTouch           bool
	}{{"nba", "nba", true}, {"f1", "f1", true}, {"nba", "test", false}} {
		t.Run(test.source+"_"+test.taskSource, func(t *testing.T) {
			source := test.source
			setupSportsMuteTest(t, 1)
			const esn = "mute-speaking-robot"
			clearReminderReadinessAfterTest(t, esn)
			now := time.Now()
			observeHealthyReminder(&reminderConnections, esn, now.Add(-reminderReconnectGrace), now)
			client := &sportsMuteRobotClient{events: make(chan *vectorpb.EventResponse), speaking: make(chan struct{})}
			task := sportsMuteTestTask(source, "speaking-game", esn, "live")
			task.Source = test.taskSource
			task.Pages = []TaskPage{{Speech: "Current score"}, {Speech: "Must not be spoken"}}
			done := make(chan bool, 1)
			go func() { done <- deliverReminderTask(task, &vector.Vector{Conn: client}, true) }()
			select {
			case <-client.speaking:
			case <-time.After(time.Second):
				t.Fatal("announcement did not start")
			}
			for index, touched := range []bool{false, true, true} {
				if index == 2 {
					time.Sleep(sportsTouchHoldDuration + 50*time.Millisecond)
				}
				state := sportsTouchState(touched)
				if test.rawTouch {
					// Real raw sensor contact must interrupt speech even when the
					// firmware's interpreted touch flag stays false throughout.
					state.TouchData.IsBeingTouched = false
					state.TouchData.RawTouchValue = 1000
					if touched {
						state.TouchData.RawTouchValue = 1100
					}
				}
				event := &vectorpb.EventResponse{Event: &vectorpb.Event{EventType: &vectorpb.Event_RobotState{RobotState: state}}}
				select {
				case client.events <- event:
				case <-time.After(time.Second):
					t.Fatal("touch event was not consumed")
				}
			}
			select {
			case faceFound := <-done:
				if faceFound {
					t.Fatal("interrupted presentation retained the face for batching")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("one-second touch did not interrupt blocked speech")
			}
			if len(client.controls) != 2 || len(taskQueue) != 0 || !sportsTaskIsMuted(task) {
				t.Fatal("mute did not stop remaining pages, release live control, and suppress retries")
			}
			for _, control := range client.controls {
				if !control.released || control.failed {
					t.Fatal("presentation or confirmation control was not released with a live context")
				}
			}
			confirmation := "Game muted."
			if source == "f1" {
				confirmation = "Session muted."
			}
			wantCalls := []string{"acquire", "say:Current score", "release", "chime", "acquire", "say:" + confirmation, "release"}
			if !reflect.DeepEqual(client.calls, wantCalls) || !client.muteVoice {
				t.Fatalf("mute feedback order=%v, want %v with Vector's voice", client.calls, wantCalls)
			}
			if client.muteSpeechDurationScalar != 1 {
				t.Fatalf("mute speech duration scalar=%v, want normal-speed speech", client.muteSpeechDurationScalar)
			}
			var pcm []byte
			for _, request := range client.chime.sent {
				if chunk := request.GetAudioStreamChunk(); chunk != nil {
					pcm = append(pcm, chunk.AudioChunkSamples...)
				}
			}
			if !bytes.Equal(pcm, commandAcknowledgementPCM()) || !client.chime.closed {
				t.Fatal("mute feedback did not reuse and finish the existing confirmation chime")
			}
			client.speaking = nil
			final := sportsMuteTestTask(source, task.sportsEventID, esn, "final")
			deliverReminderTask(final, &vector.Vector{Conn: client}, true)
			if len(client.speech) != 3 || client.eventRequests != 1 {
				t.Fatal("final score was suppressed or acquired a mute listener")
			}
		})
	}
}

func TestSportsMuteListenerScopeAndCleanup(t *testing.T) {
	for _, source := range []string{"nba", "f1"} {
		t.Run(source, func(t *testing.T) {
			setupSportsMuteTest(t, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &sportsMuteRobotClient{events: make(chan *vectorpb.EventResponse, 3)}
			for _, task := range []Task{
				{Source: "manual", sportsEventID: "game"},
				{Source: source},
				sportsMuteTestTask(source, "game", "robot", "final"),
			} {
				if startSportsMuteListener(ctx, client, task, cancel)() {
					t.Fatal("an unrelated reminder reported a successful mute")
				}
			}
			if client.eventRequests != 0 || ctx.Err() != nil {
				t.Fatal("a final score or unrelated reminder listened for muting")
			}
			task := sportsMuteTestTask(source, "game", "robot", "live")
			stop := startSportsMuteListener(ctx, client, task, cancel)
			if stop() {
				t.Fatal("normal listener cleanup reported a successful mute")
			}
			if ctx.Err() != nil || sportsTaskIsMuted(task) {
				t.Fatal("normal listener cleanup muted a game or canceled presentation")
			}
			// stop joins the listener. Touch samples after delivery must remain
			// unconsumed and cannot affect this event or the next notification.
			for _, touched := range []bool{false, true, true} {
				client.events <- &vectorpb.EventResponse{Event: &vectorpb.Event{EventType: &vectorpb.Event_RobotState{RobotState: sportsTouchState(touched)}}}
			}
			if len(client.events) != 3 || sportsTaskIsMuted(task) {
				t.Fatal("a touch after notification delivery was consumed or muted the event")
			}
			client.eventError = errors.New("robot unavailable")
			if startSportsMuteListener(ctx, client, task, cancel)() {
				t.Fatal("event stream failure reported a successful mute")
			}
			if ctx.Err() != nil || sportsTaskIsMuted(task) {
				t.Fatal("event stream failure muted a game or canceled presentation")
			}
			requests := client.eventRequests
			cancel()
			if startSportsMuteListener(ctx, client, task, cancel)() || client.eventRequests != requests {
				t.Fatal("a canceled presentation subscribed to touch events")
			}
		})
	}
}

func TestNBAMuteConfirmationStillSpeaksIfChimeFails(t *testing.T) {
	setupSportsMuteTest(t, 1)
	task := nbaMuteTestTask("chime-failed", "robot", "live")
	muteSportsEvent(task)
	client := &sportsMuteRobotClient{chimeError: errors.New("audio stream unavailable")}
	confirmSportsEventMuted(context.Background(), &vector.Vector{Conn: client}, task)
	if !reflect.DeepEqual(client.speech, []string{"Game muted."}) || !client.muteVoice || !client.control.released || !sportsTaskIsMuted(task) || len(taskQueue) != 0 {
		t.Fatal("chime failure prevented spoken confirmation or undid the mute")
	}
}

func TestSportsMuteConfirmationHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &sportsMuteRobotClient{}
	confirmSportsEventMuted(ctx, &vector.Vector{Conn: client}, Task{Source: "nba"})
	if len(client.calls) != 0 {
		t.Fatal("canceled confirmation attempted audio or behavior control")
	}
}

func TestSportsMuteConfirmationHoldsControlWhileSpeechIsStillPlaying(t *testing.T) {
	for _, test := range []struct {
		name, source string
		response     *vectorpb.SayTextResponse
	}{
		{"NBA playing", "nba", &vectorpb.SayTextResponse{State: vectorpb.SayTextResponse_PLAYING}},
		{"F1 generating", "f1", &vectorpb.SayTextResponse{State: vectorpb.SayTextResponse_GENERATING}},
		{"missing completion state", "nba", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			var speechReturnedAt time.Time
			client := &sportsMuteRobotClient{onSpeech: func(context.Context, *vectorpb.SayTextRequest) (*vectorpb.SayTextResponse, error) {
				speechReturnedAt = time.Now()
				return test.response, nil
			}}
			task := sportsMuteTestTask(test.source, "game", "robot", "live")
			confirmSportsEventMuted(context.Background(), &vector.Vector{Conn: client}, task)
			if len(client.speech) != 1 || !client.control.released || client.control.failed {
				t.Fatal("confirmation did not speak once and release control with a live context")
			}
			if elapsed, want := client.control.releasedAt.Sub(speechReturnedAt), estimatedReminderSpeechDuration(client.speech[0]); elapsed < want {
				t.Fatalf("released control %v after speech started, before the %v playback allowance", elapsed, want)
			}
		})
	}
}

func TestConfirmationSpeechPlaybackWaitHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &sportsMuteRobotClient{onSpeech: func(context.Context, *vectorpb.SayTextRequest) (*vectorpb.SayTextResponse, error) {
		cancel()
		return &vectorpb.SayTextResponse{State: vectorpb.SayTextResponse_PLAYING}, nil
	}}
	if err := sayConfirmationResponse(ctx, &vector.Vector{Conn: client}, "Game muted."); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled playback wait returned %v, want context.Canceled", err)
	}
	if len(client.speech) != 1 {
		t.Fatal("canceled playback wait repeated the confirmation")
	}
}
