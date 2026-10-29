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

func sportsButtonState(pressed bool) *vectorpb.RobotState {
	state := &vectorpb.RobotState{}
	if pressed {
		state.Status = uint32(vectorpb.RobotStatus_ROBOT_STATUS_IS_BUTTON_PRESSED)
	}
	return state
}

func TestSportsMuteRequiresOneCompletedClickDuringDelivery(t *testing.T) {
	for _, test := range []struct {
		name    string
		pressed []bool
		times   []int // Milliseconds since the first sample.
		clicks  int
	}{
		{"single click", []bool{false, true, false}, []int{0, 100, 200}, 1},
		{"press without release", []bool{false, true, true}, []int{0, 100, 200}, 0},
		{"held button", []bool{false, true, true, true, false}, []int{0, 100, 200, 300, 1200}, 0},
		{"switch bounce", []bool{false, true, false, true, false}, []int{0, 100, 110, 120, 130}, 0},
		{"held before delivery", []bool{true, true, false}, []int{0, 100, 200}, 0},
		{"new click after held at startup", []bool{true, true, false, true, false}, []int{0, 100, 200, 300, 400}, 1},
		{"repeated sensor samples", []bool{false, true, true, false, false}, []int{0, 100, 150, 200, 250}, 1},
		{"minimum click duration", []bool{false, true, false}, []int{0, 100, 140}, 1},
		{"maximum click duration", []bool{false, true, false}, []int{0, 100, 600}, 1},
		{"release without press", []bool{false, false}, []int{0, 100}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			var detector sportsButtonClickDetector
			now := time.Now()
			if detector.observe(nil, now) {
				t.Fatal("missing robot state triggered a mute")
			}
			got := 0
			for index, pressed := range test.pressed {
				if detector.observe(sportsButtonState(pressed), now.Add(time.Duration(test.times[index])*time.Millisecond)) {
					if pressed {
						t.Fatal("mute triggered before the button was released")
					}
					got++
				}
			}
			if got != test.clicks {
				t.Fatalf("completed clicks=%d, want %d", got, test.clicks)
			}
		})
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
	requests := 0
	externalApiClient = &http.Client{Transport: bedtimeScoreboardTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		data := []byte(`{}`)
		if strings.HasSuffix(request.URL.Path, "/scoreboard") {
			var err error
			data, err = json.Marshal(nbaScoreboard{Events: []nbaEvent{game}})
			if err != nil {
				t.Fatal(err)
			}
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
	if requests != 2 {
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
	ctx      context.Context
	released bool
	failed   bool
	calls    *[]string
}

func (stream *sportsMuteControlStream) Send(request *vectorpb.BehaviorControlRequest) error {
	if request.GetControlRequest() != nil {
		*stream.calls = append(*stream.calls, "acquire")
	}
	if request.GetControlRelease() != nil {
		stream.released, stream.failed = true, stream.ctx.Err() != nil
		*stream.calls = append(*stream.calls, "release")
	}
	return nil
}

type sportsMuteRobotClient struct {
	batchRobotClient
	events        chan *vectorpb.EventResponse
	speaking      chan struct{}
	control       *sportsMuteControlStream
	controls      []*sportsMuteControlStream
	calls         []string
	chime         *fakeAcknowledgementStream
	chimeError    error
	muteVoice     bool
	eventRequests int
	eventError    error
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
	}
	if client.speaking != nil && len(client.speech) == 1 {
		close(client.speaking)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &vectorpb.SayTextResponse{State: vectorpb.SayTextResponse_FINISHED}, nil
}

func (client *sportsMuteRobotClient) ExternalAudioStreamPlayback(ctx context.Context, _ ...grpc.CallOption) (vectorpb.ExternalInterface_ExternalAudioStreamPlaybackClient, error) {
	client.calls = append(client.calls, "chime")
	client.chime = successfulAcknowledgementStream()
	client.chime.ctx = ctx
	return client.chime, client.chimeError
}

func TestSportsSingleClickInterruptsSpeechAndReleasesControlWithoutRetry(t *testing.T) {
	for _, test := range []struct{ source, taskSource string }{{"nba", "nba"}, {"f1", "f1"}, {"nba", "test"}} {
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
			for index, pressed := range []bool{false, true, false} {
				if index > 0 {
					time.Sleep(70 * time.Millisecond)
				}
				event := &vectorpb.EventResponse{Event: &vectorpb.Event{EventType: &vectorpb.Event_RobotState{RobotState: sportsButtonState(pressed)}}}
				select {
				case client.events <- event:
				case <-time.After(time.Second):
					t.Fatal("button event was not consumed")
				}
			}
			select {
			case faceFound := <-done:
				if faceFound {
					t.Fatal("interrupted presentation retained the face for batching")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("single-click did not interrupt blocked speech")
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
			// stop joins the listener. A complete click after delivery must remain
			// unconsumed and cannot affect this event or the next notification.
			for _, pressed := range []bool{false, true, false} {
				client.events <- &vectorpb.EventResponse{Event: &vectorpb.Event{EventType: &vectorpb.Event_RobotState{RobotState: sportsButtonState(pressed)}}}
			}
			if len(client.events) != 3 || sportsTaskIsMuted(task) {
				t.Fatal("a click after notification delivery was consumed or muted the event")
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
				t.Fatal("a canceled presentation subscribed to button events")
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
