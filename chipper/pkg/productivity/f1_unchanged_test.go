package productivity

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

func setupF1UnchangedPolling(t *testing.T, session *f1Competition, limit int) {
	t.Helper()
	setupSportsTaskQueue(t, 1)
	previousConfig, previousClient := vars.APIConfig.Productivity, externalApiClient
	t.Cleanup(func() {
		vars.APIConfig.Productivity, externalApiClient = previousConfig, previousClient
		resetF1NotificationState()
	})
	resetF1NotificationState()
	vars.APIConfig.Productivity = vars.ProductivityConfig{
		TargetRobot: "f1-unchanged-test", Timezone: "UTC",
		F1: vars.F1Config{
			Enable: true, PregameMinutes: 60, LiveUpdateMinutes: 10, SkipUnchangedTop: limit,
			NotifyFinal: true, NotifyQualifying: true, NotifyPractice: true,
		},
	}
	externalApiClient = &http.Client{Transport: bedtimeScoreboardTransport(func(request *http.Request) (*http.Response, error) {
		if !strings.Contains(request.URL.Path, "/racing/f1/scoreboard") {
			t.Fatalf("unexpected request: %s", request.URL)
		}
		event := f1Event{ID: "unchanged-event", ShortName: "Test GP", Competitions: []f1Competition{*session}}
		body, err := json.Marshal(f1Scoreboard{Events: []f1Event{event}})
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, err
	})}
}

func TestF1SkipUnchangedRaceUpdates(t *testing.T) {
	for _, test := range []struct {
		name, abbreviation string
		limit              int
		change             func(*f1Competition)
		wantUpdate         bool
	}{
		{name: "disabled", limit: 0, wantUpdate: true},
		{name: "unchanged top three", limit: 3},
		{name: "leader only ignores podium swap", limit: 1, change: func(s *f1Competition) { s.Competitors[1].Order, s.Competitors[2].Order = 3, 2 }},
		{name: "podium swap", limit: 3, wantUpdate: true, change: func(s *f1Competition) { s.Competitors[1].Order, s.Competitors[2].Order = 3, 2 }},
		{name: "new driver in top three", limit: 3, wantUpdate: true, change: func(s *f1Competition) { s.Competitors[2].Order, s.Competitors[3].Order = 4, 3 }},
		{name: "changes below top three", limit: 3, change: func(s *f1Competition) { s.Competitors[3].Order, s.Competitors[4].Order = 5, 4 }},
		{name: "tenth changes", limit: 10, wantUpdate: true, change: func(s *f1Competition) {
			addF1TestCompetitors(s, 11, 11)
			s.Competitors[9].Order, s.Competitors[10].Order = 11, 10
		}},
		{name: "lap progress alone", limit: 3, change: func(s *f1Competition) { s.Status.Type.ShortDetail = "Lap 43 of 57" }},
		{name: "feed array order ignored", limit: 3, change: func(s *f1Competition) { s.Competitors[0], s.Competitors[9] = s.Competitors[9], s.Competitors[0] }},
		{name: "incomplete top three", limit: 3, wantUpdate: true, change: func(s *f1Competition) { s.Competitors = s.Competitors[:2] }},
		{name: "unknown position", limit: 3, wantUpdate: true, change: func(s *f1Competition) { s.Competitors[0].Order = 0 }},
		{name: "unknown driver", limit: 3, wantUpdate: true, change: func(s *f1Competition) {
			s.Competitors[0].ID = ""
			s.Competitors[0].Athlete.DisplayName, s.Competitors[0].Athlete.FullName = "", ""
		}},
		{name: "separate race", limit: 3, wantUpdate: true, change: func(s *f1Competition) { s.ID = "another-race" }},
		{name: "final always sent", limit: 3, wantUpdate: true, change: func(s *f1Competition) { s.Status.Type.State = "post" }},
		{name: "session reminder always sent", limit: 3, wantUpdate: true, change: func(s *f1Competition) {
			s.Status.Type.State = "pre"
			s.Date = time.Now().Add(30 * time.Minute).Format(time.RFC3339)
		}},
		{name: "qualifying unchanged", abbreviation: "Qual", limit: 3, wantUpdate: true},
		{name: "practice unchanged", abbreviation: "FP1", limit: 3, wantUpdate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, session := syntheticF1Race()
			if test.abbreviation != "" {
				session.Type.Abbreviation = test.abbreviation
			}
			setupF1UnchangedPolling(t, &session, test.limit)
			checkF1Races()
			if len(taskQueue) != 1 {
				t.Fatal("first race update was suppressed")
			}
			<-taskQueue
			lastUpdate := time.Now().Add(-10 * time.Minute)
			f1LastLiveUpdate[session.ID] = lastUpdate
			if test.change != nil {
				test.change(&session)
			}
			checkF1Races()
			if got := len(taskQueue) == 1; got != test.wantUpdate {
				t.Fatalf("queued update = %v, want %v", got, test.wantUpdate)
			}
			if !test.wantUpdate && !f1LastLiveUpdate[session.ID].Equal(lastUpdate) {
				t.Fatal("skipping unchanged drivers delayed the next changed update")
			}
		})
	}
}

func TestF1UnchangedRaceTracksLastQueuedUpdate(t *testing.T) {
	_, session := syntheticF1Race()
	setupF1UnchangedPolling(t, &session, 3)
	// A full queue must not establish a comparison baseline.
	taskQueue <- Task{ID: "busy"}
	checkF1Races()
	if len(f1LastRaceOrder[session.ID]) > 0 {
		t.Fatal("an unqueued update became the baseline")
	}
	<-taskQueue
	checkF1Races()
	if len(taskQueue) != 1 {
		t.Fatal("first update was lost after the queue became available")
	}
	<-taskQueue
	f1LastLiveUpdate[session.ID] = time.Now().Add(-10 * time.Minute)
	checkF1Races()
	if len(taskQueue) != 0 {
		t.Fatal("unchanged update was not skipped")
	}
	// Changing positions after a skipped poll should notify without a new wait.
	session.Competitors[1].Order, session.Competitors[2].Order = 3, 2
	checkF1Races()
	if len(taskQueue) != 1 {
		t.Fatal("changed podium was not announced after a skipped poll")
	}
	<-taskQueue
	f1LastLiveUpdate[session.ID] = time.Now().Add(-10 * time.Minute)
	checkF1Races()
	if len(taskQueue) != 0 {
		t.Fatal("comparison baseline was not updated after the changed update")
	}
	resetF1NotificationState()
	checkF1Races()
	if len(taskQueue) != 1 {
		t.Fatal("notification reset retained an old comparison baseline")
	}
}

func TestF1LeaderAlertRefreshesUnchangedRaceComparison(t *testing.T) {
	_, session := syntheticF1Race()
	setupF1UnchangedPolling(t, &session, 3)
	vars.APIConfig.Productivity.F1.NotifyNotable = true
	checkF1Races()
	if len(taskQueue) != 1 {
		t.Fatal("initial race update was not queued")
	}
	<-taskQueue
	session.Competitors[0].Order, session.Competitors[1].Order = 2, 1
	checkF1Races()
	if len(taskQueue) != 1 {
		t.Fatal("top-driver filter suppressed an immediate leader alert")
	}
	if task := <-taskQueue; !strings.HasSuffix(task.ID, "_leader") {
		t.Fatalf("queued %q, want leader alert", task.ID)
	}
	f1LastLiveUpdate[session.ID] = time.Now().Add(-10 * time.Minute)
	checkF1Races()
	if len(taskQueue) != 0 {
		t.Fatal("regular update repeated the unchanged order from the leader alert")
	}
}
