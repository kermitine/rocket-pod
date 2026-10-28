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

func setupNBAMuteTest(t *testing.T, capacity int) {
	t.Helper()
	setupSportsTaskQueue(t, capacity)
	taskQueueMu.Lock()
	previous := mutedNBAGames
	mutedNBAGames = make(map[nbaGameMuteKey]bool)
	taskQueueMu.Unlock()
	t.Cleanup(func() {
		taskQueueMu.Lock()
		mutedNBAGames = previous
		taskQueueMu.Unlock()
	})
}

func nbaMuteTestTask(gameID, robot, kind string) Task {
	task := sportsQueueTestTask("nba", "nba_"+gameID+"_"+kind, robot)
	task.nbaGameID, task.nbaFinal = gameID, kind == "final"
	return task
}

func nbaButtonState(pressed bool) *vectorpb.RobotState {
	state := &vectorpb.RobotState{}
	if pressed {
		state.Status = uint32(vectorpb.RobotStatus_ROBOT_STATUS_IS_BUTTON_PRESSED)
	}
	return state
}

func TestNBADoubleClickRequiresTwoQuickCompletedClicks(t *testing.T) {
	for _, test := range []struct {
		name    string
		pressed []bool
		times   []int // Milliseconds since the first sample.
		want    bool
	}{
		{"double click", []bool{false, true, false, true, false}, []int{0, 100, 200, 300, 400}, true},
		{"single click", []bool{false, true, false}, []int{0, 100, 200}, false},
		{"held button", []bool{false, true, true, true, false}, []int{0, 100, 200, 300, 1200}, false},
		{"click then hold", []bool{false, true, false, true, true}, []int{0, 100, 200, 300, 400}, false},
		{"slow clicks", []bool{false, true, false, true, false}, []int{0, 100, 200, 800, 900}, false},
		{"switch bounce", []bool{false, true, false, true, false}, []int{0, 100, 110, 120, 130}, false},
		{"held at startup", []bool{true, true, false, true, false}, []int{0, 100, 200, 300, 400}, false},
		{"repeated sensor samples", []bool{false, true, true, false, false, true, true, false}, []int{0, 100, 150, 200, 250, 300, 350, 400}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var detector nbaDoubleClickDetector
			now := time.Now()
			got := false
			for index, pressed := range test.pressed {
				if detector.observe(nbaButtonState(pressed), now.Add(time.Duration(test.times[index])*time.Millisecond)) {
					if index != len(test.pressed)-1 {
						t.Fatal("mute triggered before the second completed click")
					}
					got = true
				}
			}
			if got != test.want {
				t.Fatalf("double click=%v, want %v", got, test.want)
			}
		})
	}
}

func TestNBAMuteCancelsPendingAndDeferredUpdatesForOnlyThatGameAndRobot(t *testing.T) {
	setupNBAMuteTest(t, 4)
	current := nbaMuteTestTask("game", "ROBOT-A", "live")
	enqueueSportsNotification(current, "game")
	current = <-taskQueue
	deferTask(current, "offline", time.Hour)
	// A newer update is already queued while the old one is being spoken.
	enqueueSportsNotification(nbaMuteTestTask("game", "robot-a", "notable"), "game")
	newer := <-taskQueue
	deferTask(newer, "offline", time.Hour)
	enqueueTask(newer)
	enqueueSportsNotification(nbaMuteTestTask("other-game", "robot-a", "live"), "other-game")
	enqueueSportsNotification(nbaMuteTestTask("game", "robot-b", "live"), "game")
	enqueueTask(sportsQueueTestTask("manual", "reminder", "robot-a"))
	muteNBAGame(current)
	if !taskIsSuperseded(newer) || taskCanRun(current) || taskCanRun(newer) || enqueueTask(newer) || enqueueSportsNotification(nbaMuteTestTask("game", "robot-a", "live"), "game") {
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
	if want := []string{"nba_other-game_live", "nba_game_live", "reminder"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("remaining queue=%v, want %v", got, want)
	}
	NotifyConfigUpdated()
	if !nbaGameIsMuted("robot-a", "game") {
		t.Fatal("saving settings removed the game mute")
	}
}

func TestNBAMutePreservesFinalQueuedWhileLiveUpdateIsSpeaking(t *testing.T) {
	setupNBAMuteTest(t, 1)
	live := nbaMuteTestTask("game", "robot", "live")
	enqueueSportsNotification(live, "game")
	live = <-taskQueue
	final := nbaMuteTestTask("game", "robot", "final")
	enqueueSportsNotification(final, "game")
	muteNBAGame(live)
	if len(taskQueue) != 1 {
		t.Fatal("muting the live announcement dropped the pending final score")
	}
	if queued := <-taskQueue; !queued.nbaFinal || !taskCanRun(queued) {
		t.Fatal("the final score was muted or superseded")
	}
	if !enqueueTask(final) {
		t.Fatal("a final score could not retry while the game was muted")
	}
}

func TestNBAMutedGamePollingSkipsUpdatesAndStillQueuesFinal(t *testing.T) {
	setupNBAMuteTest(t, 1)
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
	muteNBAGame(nbaMuteTestTask(game.ID, "muted-polling", "pregame"))
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
	if !final.nbaFinal || final.nbaGameID != game.ID || !taskCanRun(final) {
		t.Fatal("final score lost game metadata or was suppressed")
	}
}

type nbaMuteEventStream struct {
	vectorpb.ExternalInterface_EventStreamClient
	ctx    context.Context
	events <-chan *vectorpb.EventResponse
}

func (stream *nbaMuteEventStream) Recv() (*vectorpb.EventResponse, error) {
	select {
	case <-stream.ctx.Done():
		return nil, stream.ctx.Err()
	case event := <-stream.events:
		return event, nil
	}
}

type nbaMuteControlStream struct {
	readinessControlStream
	ctx      context.Context
	released bool
	failed   bool
	calls    *[]string
}

func (stream *nbaMuteControlStream) Send(request *vectorpb.BehaviorControlRequest) error {
	if request.GetControlRequest() != nil {
		*stream.calls = append(*stream.calls, "acquire")
	}
	if request.GetControlRelease() != nil {
		stream.released, stream.failed = true, stream.ctx.Err() != nil
		*stream.calls = append(*stream.calls, "release")
	}
	return nil
}

type nbaMuteRobotClient struct {
	batchRobotClient
	events        chan *vectorpb.EventResponse
	speaking      chan struct{}
	control       *nbaMuteControlStream
	controls      []*nbaMuteControlStream
	calls         []string
	chime         *fakeAcknowledgementStream
	chimeError    error
	muteVoice     bool
	eventRequests int
	eventError    error
}

func (client *nbaMuteRobotClient) BehaviorControl(ctx context.Context, _ ...grpc.CallOption) (vectorpb.ExternalInterface_BehaviorControlClient, error) {
	client.control = &nbaMuteControlStream{ctx: ctx, calls: &client.calls}
	client.controls = append(client.controls, client.control)
	return client.control, nil
}

func (client *nbaMuteRobotClient) EventStream(ctx context.Context, _ *vectorpb.EventRequest, _ ...grpc.CallOption) (vectorpb.ExternalInterface_EventStreamClient, error) {
	client.eventRequests++
	return &nbaMuteEventStream{ctx: ctx, events: client.events}, client.eventError
}

func (client *nbaMuteRobotClient) SayText(ctx context.Context, request *vectorpb.SayTextRequest, _ ...grpc.CallOption) (*vectorpb.SayTextResponse, error) {
	client.speech = append(client.speech, request.Text)
	client.calls = append(client.calls, "say:"+request.Text)
	if request.Text == "Game muted." {
		client.muteVoice = request.UseVectorVoice
	}
	if client.speaking != nil && len(client.speech) == 1 {
		close(client.speaking)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &vectorpb.SayTextResponse{State: vectorpb.SayTextResponse_FINISHED}, nil
}

func (client *nbaMuteRobotClient) ExternalAudioStreamPlayback(ctx context.Context, _ ...grpc.CallOption) (vectorpb.ExternalInterface_ExternalAudioStreamPlaybackClient, error) {
	client.calls = append(client.calls, "chime")
	client.chime = successfulAcknowledgementStream()
	client.chime.ctx = ctx
	return client.chime, client.chimeError
}

func TestNBADoubleClickInterruptsSpeechAndReleasesControlWithoutRetry(t *testing.T) {
	setupNBAMuteTest(t, 1)
	const esn = "mute-speaking-robot"
	clearReminderReadinessAfterTest(t, esn)
	now := time.Now()
	observeHealthyReminder(&reminderConnections, esn, now.Add(-reminderReconnectGrace), now)
	client := &nbaMuteRobotClient{events: make(chan *vectorpb.EventResponse), speaking: make(chan struct{})}
	task := nbaMuteTestTask("speaking-game", esn, "live")
	task.Pages = []TaskPage{{Speech: "Current score"}, {Speech: "Must not be spoken"}}
	done := make(chan bool, 1)
	go func() { done <- deliverReminderTask(task, &vector.Vector{Conn: client}, true) }()
	select {
	case <-client.speaking:
	case <-time.After(time.Second):
		t.Fatal("announcement did not start")
	}
	for index, pressed := range []bool{false, true, false, true, false} {
		if index > 0 {
			time.Sleep(70 * time.Millisecond)
		}
		event := &vectorpb.EventResponse{Event: &vectorpb.Event{EventType: &vectorpb.Event_RobotState{RobotState: nbaButtonState(pressed)}}}
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
		t.Fatal("double-click did not interrupt blocked speech")
	}
	if len(client.controls) != 2 || len(taskQueue) != 0 || !nbaTaskIsMuted(task) {
		t.Fatal("mute did not stop remaining pages, release live control, and suppress retries")
	}
	for _, control := range client.controls {
		if !control.released || control.failed {
			t.Fatal("presentation or confirmation control was not released with a live context")
		}
	}
	wantCalls := []string{"acquire", "say:Current score", "release", "chime", "acquire", "say:Game muted.", "release"}
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
	final := nbaMuteTestTask(task.nbaGameID, esn, "final")
	deliverReminderTask(final, &vector.Vector{Conn: client}, true)
	if len(client.speech) != 3 || client.eventRequests != 1 {
		t.Fatal("final score was suppressed or acquired a mute listener")
	}
}

func TestNBAMuteListenerScopeAndCleanup(t *testing.T) {
	setupNBAMuteTest(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &nbaMuteRobotClient{events: make(chan *vectorpb.EventResponse)}
	for _, task := range []Task{{Source: "manual"}, nbaMuteTestTask("game", "robot", "final")} {
		if startNBAMuteListener(ctx, client, task, cancel)() {
			t.Fatal("an unrelated reminder reported a successful mute")
		}
	}
	if client.eventRequests != 0 || ctx.Err() != nil {
		t.Fatal("a final score or unrelated reminder listened for muting")
	}
	task := nbaMuteTestTask("game", "robot", "live")
	stop := startNBAMuteListener(ctx, client, task, cancel)
	if stop() {
		t.Fatal("normal listener cleanup reported a successful mute")
	}
	if ctx.Err() != nil || nbaTaskIsMuted(task) {
		t.Fatal("normal listener cleanup muted a game or canceled presentation")
	}
	client.eventError = errors.New("robot unavailable")
	if startNBAMuteListener(ctx, client, task, cancel)() {
		t.Fatal("event stream failure reported a successful mute")
	}
	if ctx.Err() != nil || nbaTaskIsMuted(task) {
		t.Fatal("event stream failure muted a game or canceled presentation")
	}
}

func TestNBAMuteConfirmationStillSpeaksIfChimeFails(t *testing.T) {
	setupNBAMuteTest(t, 1)
	task := nbaMuteTestTask("chime-failed", "robot", "live")
	muteNBAGame(task)
	client := &nbaMuteRobotClient{chimeError: errors.New("audio stream unavailable")}
	confirmNBAGameMuted(context.Background(), &vector.Vector{Conn: client})
	if !reflect.DeepEqual(client.speech, []string{"Game muted."}) || !client.muteVoice || !client.control.released || !nbaTaskIsMuted(task) || len(taskQueue) != 0 {
		t.Fatal("chime failure prevented spoken confirmation or undid the mute")
	}
}

func TestNBAMuteConfirmationHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &nbaMuteRobotClient{}
	confirmNBAGameMuted(ctx, &vector.Vector{Conn: client})
	if len(client.calls) != 0 {
		t.Fatal("canceled confirmation attempted audio or behavior control")
	}
}
