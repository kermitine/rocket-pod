package productivity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

func setupSportsTaskQueue(t *testing.T, capacity int) {
	t.Helper()
	taskQueueMu.Lock()
	previousQueue, previousPending := taskQueue, pendingSportsNotifications
	taskQueue = make(chan Task, capacity)
	pendingSportsNotifications = make(map[sportsNotificationKey]*pendingSportsNotification)
	taskQueueMu.Unlock()
	t.Cleanup(func() {
		taskQueueMu.Lock()
		for _, notification := range pendingSportsNotifications {
			close(notification.superseded)
		}
		taskQueue, pendingSportsNotifications = previousQueue, previousPending
		taskQueueMu.Unlock()
	})
}

func sportsQueueTestTask(source, id, robot string) Task {
	return Task{ID: id, RobotESN: robot, Source: source, Phrases: []string{id}, configurationGeneration: currentConfigurationGeneration()}
}

func TestSportsQueueKeepsNewestUpdateWhenFull(t *testing.T) {
	for _, source := range []string{"nba", "f1"} {
		t.Run(source, func(t *testing.T) {
			setupSportsTaskQueue(t, 1)
			var older []Task
			for _, kind := range []string{"pregame", "live_1", "notable", "live_2", "final"} {
				if !enqueueSportsNotification(sportsQueueTestTask(source, kind, "robot"), "game") {
					t.Fatal("new update could not replace an older update in a full queue")
				}
				if len(taskQueue) != 1 {
					t.Fatalf("pending notifications = %d, want 1", len(taskQueue))
				}
				latest := <-taskQueue
				if latest.ID != kind || latest.Phrases[0] != kind || taskIsSuperseded(latest) {
					t.Fatalf("wrong notification retained: %+v", latest)
				}
				for _, old := range older {
					if !taskIsSuperseded(old) || taskCanRun(old) || enqueueTask(old) {
						t.Fatal("an obsolete notification can still run or rejoin the queue")
					}
				}
				older = append(older, latest)
				if !enqueueTask(latest) {
					t.Fatal("could not restore pending notification")
				}
			}
		})
	}
}

func TestSportsQueuePreservesOtherGamesSessionsRobotsAndReminders(t *testing.T) {
	setupSportsTaskQueue(t, 6)
	for _, test := range []struct{ source, event, robot, id string }{
		{"nba", "one", "robot-a", "old"},
		{"nba", "two", "robot-a", "other-game"},
		{"nba", "one", "robot-b", "other-robot"},
		{"f1", "one", "robot-a", "other-sport"},
		{"f1", "qualifying", "robot-a", "other-session"},
	} {
		if !enqueueSportsNotification(sportsQueueTestTask(test.source, test.id, test.robot), test.event) {
			t.Fatal("could not enqueue fixture")
		}
	}
	if !enqueueTask(sportsQueueTestTask("manual", "reminder", "robot-a")) ||
		!enqueueSportsNotification(sportsQueueTestTask("nba", "new", "ROBOT-A"), "one") {
		t.Fatal("replacement failed in a queue containing unrelated tasks")
	}
	var got []string
	for len(taskQueue) > 0 {
		got = append(got, (<-taskQueue).ID)
	}
	want := []string{"other-game", "other-robot", "other-sport", "other-session", "reminder", "new"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pending notifications = %v, want %v", got, want)
	}
}

func TestNewSportsUpdateCancelsOfflineRetry(t *testing.T) {
	setupSportsTaskQueue(t, 1)
	enqueueSportsNotification(sportsQueueTestTask("nba", "old", "robot"), "game")
	old := <-taskQueue
	beginSportsTaskAttempt(old)
	deferTask(old, "robot is offline", 20*time.Millisecond)
	finishSportsTaskAttempt(old)
	if !enqueueSportsNotification(sportsQueueTestTask("nba", "new", "robot"), "game") {
		t.Fatal("new update could not replace an offline update")
	}
	if latest := <-taskQueue; latest.ID != "new" || taskIsSuperseded(latest) {
		t.Fatalf("latest update = %s", latest.ID)
	}
	select {
	case <-old.sportsNotification.superseded:
	default:
		t.Fatal("offline retry was not canceled")
	}
	if processTask(old, false) {
		t.Fatal("superseded update was processed")
	}
	select {
	case task := <-taskQueue:
		t.Fatalf("old offline update reappeared: %s", task.ID)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRejectedSportsUpdateKeepsPendingRetry(t *testing.T) {
	setupSportsTaskQueue(t, 1)
	enqueueSportsNotification(sportsQueueTestTask("nba", "old", "robot"), "game")
	old := <-taskQueue
	enqueueTask(sportsQueueTestTask("manual", "reminder", "robot"))
	if enqueueSportsNotification(sportsQueueTestTask("nba", "new", "robot"), "other-game") || taskIsSuperseded(old) {
		t.Fatal("rejected update invalidated the last accepted update")
	}
	if got := <-taskQueue; got.ID != "reminder" {
		t.Fatal("unrelated reminder was dropped")
	}
	requeueTaskAfter(old, 0)
	select {
	case got := <-taskQueue:
		if got.ID != "old" {
			t.Fatalf("retried notification = %s", got.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("last accepted update did not retry")
	}
}

func TestSportsQueueReplacesDeferredUpdateWhenOtherWorkFillsQueue(t *testing.T) {
	setupSportsTaskQueue(t, 1)
	enqueueSportsNotification(sportsQueueTestTask("nba", "old", "robot"), "game")
	old := <-taskQueue
	deferTask(old, "low battery", time.Hour)
	enqueueTask(sportsQueueTestTask("manual", "reminder", "robot"))
	if !enqueueSportsNotification(sportsQueueTestTask("nba", "new", "robot"), "game") || !taskIsSuperseded(old) {
		t.Fatal("new update did not replace the deferred update while the queue was full")
	}
	if got := <-taskQueue; got.ID != "reminder" {
		t.Fatal("replacement displaced an unrelated reminder")
	}
	select {
	case got := <-taskQueue:
		if got.ID != "new" || taskIsSuperseded(got) {
			t.Fatalf("retried notification = %s", got.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("new update did not retry after queue space became available")
	}
}

func TestSportsQueueReleasesFinishedWorkAndConfiguration(t *testing.T) {
	setupSportsTaskQueue(t, 1)
	task := sportsQueueTestTask("nba", "expired", "robot")
	task.ExpiresAt = time.Now().Add(-time.Second)
	enqueueSportsNotification(task, "game")
	processTask(<-taskQueue, false)
	if len(pendingSportsNotifications) != 0 {
		t.Fatal("abandoned notification remained registered")
	}
	enqueueSportsNotification(sportsQueueTestTask("nba", "pending", "robot"), "game")
	old := <-taskQueue
	NotifyConfigUpdated()
	if len(pendingSportsNotifications) != 0 || !taskIsSuperseded(old) {
		t.Fatal("configuration update retained pending sports work")
	}
	if enqueueSportsNotification(old, "game") {
		t.Fatal("a notification from the old configuration was accepted")
	}
}

func TestSportsQueueHandlesConcurrentProducers(t *testing.T) {
	setupSportsTaskQueue(t, 3)
	enqueueTask(sportsQueueTestTask("manual", "reminder", "robot"))
	var workers sync.WaitGroup
	for index := 0; index < 100; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			event := fmt.Sprintf("game-%d", index%2)
			if !enqueueSportsNotification(sportsQueueTestTask("nba", fmt.Sprint(index), "robot"), event) {
				t.Error("sports update was rejected despite replaceable queue space")
			}
		}(index)
	}
	workers.Wait()
	for _, event := range []string{"game-0", "game-1"} {
		if !enqueueSportsNotification(sportsQueueTestTask("nba", event+"-latest", "robot"), event) {
			t.Fatal("latest update was rejected")
		}
	}
	var got []string
	for len(taskQueue) > 0 {
		got = append(got, (<-taskQueue).ID)
	}
	if want := []string{"reminder", "game-0-latest", "game-1-latest"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("concurrent queue = %v, want %v", got, want)
	}
}

func TestSportsPollingReplacesQueuedLiveUpdatesWithNewestAndFinal(t *testing.T) {
	for _, sport := range []string{"nba", "f1"} {
		t.Run(sport, func(t *testing.T) {
			setupSportsTaskQueue(t, 1)
			previousConfig, previousClient := vars.APIConfig.Productivity, externalApiClient
			t.Cleanup(func() {
				vars.APIConfig.Productivity, externalApiClient = previousConfig, previousClient
				resetNBANotificationState()
				resetF1NotificationState()
			})
			resetNBANotificationState()
			resetF1NotificationState()
			vars.APIConfig.Productivity = vars.ProductivityConfig{
				TargetRobot: "queue-test", Timezone: "UTC",
				NBA: vars.NBAConfig{Enable: true, FavoriteTeams: []string{"LAL"}, LiveUpdateMinutes: 5, NotifyFinal: true},
				F1:  vars.F1Config{Enable: true, LiveUpdateMinutes: 10, NotifyFinal: true},
			}
			game := testNBAGame("queue-game", "in", time.Now(), "100", "99")
			event, session := syntheticF1Race()
			externalApiClient = &http.Client{Transport: bedtimeScoreboardTransport(func(request *http.Request) (*http.Response, error) {
				var data []byte
				var err error
				switch {
				case strings.Contains(request.URL.Path, "/basketball/nba/scoreboard"):
					data, err = json.Marshal(nbaScoreboard{Events: []nbaEvent{game}})
				case strings.Contains(request.URL.Path, "/racing/f1/scoreboard"):
					event.Competitions = []f1Competition{session}
					data, err = json.Marshal(f1Scoreboard{Events: []f1Event{event}})
				case strings.HasSuffix(request.URL.Path, "/summary"):
					data = []byte(`{}`)
				default:
					t.Fatalf("unexpected request: %s", request.URL)
				}
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
			})}
			poll := checkNBAGames
			if sport == "f1" {
				poll = checkF1Races
			}
			poll()
			if len(taskQueue) != 1 {
				t.Fatal("initial live update was not queued")
			}
			old := <-taskQueue
			enqueueTask(old)
			nbaLastLiveUpdate[game.ID] = time.Now().Add(-time.Hour)
			f1LastLiveUpdate[session.ID] = time.Now().Add(-time.Hour)
			game.Competitions[0].Competitors[0].Score = "105"
			session.Competitors[0].Order, session.Competitors[1].Order = 2, 1
			poll()
			if len(taskQueue) != 1 || !taskIsSuperseded(old) {
				t.Fatal("new live update did not replace the old queued update")
			}
			latest := <-taskQueue
			if sport == "nba" && !strings.Contains(latest.Phrases[0], "one oh five") {
				t.Fatal("latest score was not retained")
			}
			if sport == "f1" && reflect.DeepEqual(old.Pages, latest.Pages) {
				t.Fatal("latest race order was not retained")
			}
			enqueueTask(latest)
			game.Status.Type.State, session.Status.Type.State = "post", "post"
			poll()
			if len(taskQueue) != 1 || !taskIsSuperseded(latest) {
				t.Fatal("final result did not replace the pending live update")
			}
			if final := <-taskQueue; !strings.HasSuffix(final.ID, "_final") {
				t.Fatalf("expected final result, got %s", final.ID)
			}
		})
	}
}
