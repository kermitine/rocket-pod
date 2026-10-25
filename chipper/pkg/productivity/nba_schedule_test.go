package productivity

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

func TestMatchNBAGamesTodayVoiceCommand(t *testing.T) {
	for _, text := range []string{
		"NBA games today", "what NBA games are on tonight?", "N. B. A. schedule today",
		"and b a games today", "basketball games this evening", "who is playing in the NBA today",
	} {
		if !MatchNBAGamesTodayVoiceCommand(text) {
			t.Errorf("did not recognize %q", text)
		}
	}
	for _, text := range []string{
		"NBA standings today", "NBA games tomorrow", "NBA games yesterday", "NBA schedule next week",
		"NBA games today and tomorrow", "F1 games today", "games today", "NBA news today",
	} {
		if MatchNBAGamesTodayVoiceCommand(text) {
			t.Errorf("unexpectedly recognized %q", text)
		}
	}
}

func mockNBASchedule(t *testing.T, events []nbaEvent) *[]string {
	t.Helper()
	previousClient := externalApiClient
	t.Cleanup(func() { externalApiClient = previousClient })
	var dates []string
	externalApiClient = &http.Client{Transport: bedtimeScoreboardTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/apis/site/v2/sports/basketball/nba/scoreboard" {
			t.Errorf("unexpected request: %s", request.URL)
		}
		dates = append(dates, request.URL.Query().Get("dates"))
		body, err := json.Marshal(nbaScoreboard{Events: events})
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, err
	})}
	return &dates
}

func TestNBAGamesTodayPagesFilterSortAndPairSpeechWithFaces(t *testing.T) {
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 20, 0, 0, 0, location)
	final := testNBAGame("final", "post", now.Add(-4*time.Hour), "104", "111")
	live := testNBAGame("live", "in", now.Add(-time.Hour), "133", "130")
	upcoming := testNBAGame("upcoming", "pre", now.Add(2*time.Hour), "0", "0")
	unrelated := testNBAGame("other", "pre", now, "0", "0")
	unrelated.Competitions[0].Competitors[0].Team.Abbreviation = "CLE"
	unrelated.Competitions[0].Competitors[1].Team.Abbreviation = "NY"
	invalid := testNBAGame("invalid", "pre", now, "0", "0")
	invalid.Date = "invalid"
	requested := mockNBASchedule(t, []nbaEvent{
		upcoming, unrelated, final, live, invalid, {ID: "no-teams", Date: now.Format(time.RFC3339)},
		testNBAGame("yesterday", "post", now.AddDate(0, 0, -1), "1", "2"),
		testNBAGame("tomorrow", "pre", now.AddDate(0, 0, 1), "0", "0"),
	})
	pages, err := fetchNBAGamesTodayPages(context.Background(), now, []string{" lal ", "BOS", "LAL"})
	if err != nil || len(pages) != 3 {
		t.Fatalf("got %d pages, %v; want three distinct games", len(pages), err)
	}
	if !reflect.DeepEqual(*requested, []string{"20260101", "20260102"}) {
		t.Fatalf("requested dates = %v", *requested)
	}
	for index, want := range []string{
		"The Lakers played the Celtics at 4:00 PM.",
		"The Lakers are playing the Celtics at 7:00 PM.",
		"The Lakers play the Celtics at 10:00 PM.",
	} {
		if !strings.Contains(pages[index].Speech, want) {
			t.Errorf("page %d speech = %q, want matchup and local time %q", index, pages[index].Speech, want)
		}
		if len(pages[index].FaceData) != 184*96*2 {
			t.Errorf("page %d face data length = %d", index, len(pages[index].FaceData))
		}
	}
	if !strings.Contains(pages[0].Speech, "3 NBA games today") || !strings.Contains(pages[0].Speech, "played") || !strings.Contains(pages[0].Speech, "one eleven") {
		t.Errorf("final game speech = %q", pages[0].Speech)
	}
	if !strings.Contains(pages[1].Speech, "are playing") || !strings.Contains(pages[1].Speech, "one thirty three") || !strings.Contains(pages[1].Speech, "one thirty") {
		t.Errorf("live game speech = %q", pages[1].Speech)
	}
	wantFace, err := renderNBAGameFace(context.Background(), upcoming, "NBA TODAY 3/3", "10:00 PM", false)
	if err != nil || !bytes.Equal(pages[2].FaceData, wantFace) {
		t.Fatalf("upcoming speech is not paired with its matchup and start time face: %v", err)
	}
}

func TestNBAGamesTodayCalendarBoundaries(t *testing.T) {
	for _, test := range []struct {
		zone  string
		month time.Month
		day   int
		dates []string
	}{
		{"America/Los_Angeles", time.January, 1, []string{"20260101", "20260102"}},
		{"Asia/Tokyo", time.January, 2, []string{"20260101", "20260102"}},
		{"America/Los_Angeles", time.March, 8, []string{"20260308", "20260309"}},
		{"America/Los_Angeles", time.November, 1, []string{"20261101", "20261102"}},
		{"America/New_York", time.January, 1, []string{"20260101"}},
	} {
		t.Run(test.zone+"/"+test.month.String(), func(t *testing.T) {
			location, err := time.LoadLocation(test.zone)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Date(2026, test.month, test.day, 0, 0, 0, 0, location)
			end := start.AddDate(0, 0, 1)
			requested := mockNBASchedule(t, []nbaEvent{
				testNBAGame("before", "pre", start.Add(-time.Second), "0", "0"),
				testNBAGame("start", "pre", start, "0", "0"),
				testNBAGame("last", "pre", end.Add(-time.Second), "0", "0"),
				testNBAGame("after", "pre", end, "0", "0"),
			})
			pages, err := fetchNBAGamesTodayPages(context.Background(), start.Add(time.Hour), []string{"BOS"})
			if err != nil || len(pages) != 2 {
				t.Fatalf("got %d pages, %v; want only today's two games", len(pages), err)
			}
			if !reflect.DeepEqual(*requested, test.dates) {
				t.Errorf("requested dates = %v, want %v", *requested, test.dates)
			}
		})
	}
}

func TestNBAGamesTodayEmptyResponses(t *testing.T) {
	requested := mockNBASchedule(t, nil)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	pages, err := fetchNBAGamesTodayPages(context.Background(), now, []string{"invalid"})
	if err != nil || len(pages) != 1 || !strings.Contains(pages[0].Speech, "Choose your favorite") || len(*requested) != 0 {
		t.Fatalf("missing favorites: pages=%v, error=%v, requests=%v", len(pages), err, *requested)
	}
	pages, err = fetchNBAGamesTodayPages(context.Background(), now, []string{"BOS"})
	if err != nil || len(pages) != 1 || !strings.Contains(pages[0].Speech, "None of your favorite") {
		t.Fatalf("no games: pages=%v, error=%v", len(pages), err)
	}
}

func TestNBAGamesTodayHandlesESPNMinutePrecisionDenverAndPhiladelphia(t *testing.T) {
	// Preserve ESPN's timestamp format and home-first competitor order, rather
	// than formatting dates through testNBAGame (which adds seconds).
	var scoreboard nbaScoreboard
	err := json.Unmarshal([]byte(`{"events":[
		{"id":"denver","date":"2026-10-25T01:00Z","status":{"type":{"state":"pre"}},"competitions":[{"competitors":[
			{"homeAway":"home","team":{"abbreviation":"DEN","displayName":"Denver Nuggets"}},
			{"homeAway":"away","team":{"abbreviation":"NO","displayName":"New Orleans Pelicans"}}
		]}]},
		{"id":"philadelphia","date":"2026-10-24T23:30Z","status":{"type":{"state":"pre"}},"competitions":[{"competitors":[
			{"homeAway":"home","team":{"abbreviation":"PHI","displayName":"Philadelphia 76ers"}},
			{"homeAway":"away","team":{"abbreviation":"MIL","displayName":"Milwaukee Bucks"}}
		]}]}
	]}`), &scoreboard)
	if err != nil {
		t.Fatal(err)
	}
	mockNBASchedule(t, scoreboard.Events)
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, location)
	pages, err := fetchNBAGamesTodayPages(context.Background(), now, []string{"DEN", "LAC", "LAL", "NY", "PHI", "TOR"})
	if err != nil || len(pages) != 2 {
		t.Fatalf("got %d pages, %v; want Philadelphia and Denver", len(pages), err)
	}
	if !strings.Contains(pages[0].Speech, "76ers") || strings.Contains(pages[0].Speech, "Philadelphia") || !strings.Contains(pages[0].Speech, "4:30 PM.") {
		t.Fatalf("Philadelphia schedule: %s", pages[0].Speech)
	}
	if !strings.Contains(pages[1].Speech, "Nuggets") || strings.Contains(pages[1].Speech, "Denver") || !strings.Contains(pages[1].Speech, "6:00 PM.") {
		t.Fatalf("Denver schedule: %s", pages[1].Speech)
	}
}

func TestNBAGamesTodayDoesNotReportNoGamesForUnreadableFavoriteDates(t *testing.T) {
	game := testNBAGame("bad-date", "pre", time.Now(), "0", "0")
	game.Date = "unreadable"
	mockNBASchedule(t, []nbaEvent{game})
	if _, err := fetchNBAGamesTodayPages(context.Background(), time.Now(), []string{"LAL"}); err == nil {
		t.Fatal("an unreadable date was silently reported as no games")
	}
}

func TestNBAGamesTodayPostponedAndCanceled(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, status := range []string{"postponed", "canceled"} {
		t.Run(status, func(t *testing.T) {
			game := testNBAGame(status, "pre", now, "0", "0")
			game.Status.Type.Name = "STATUS_" + strings.ToUpper(status)
			mockNBASchedule(t, []nbaEvent{game})
			pages, err := fetchNBAGamesTodayPages(context.Background(), now, []string{"LAL"})
			if err != nil || len(pages) != 1 || !strings.Contains(pages[0].Speech, "has been "+status) {
				t.Fatalf("status %s: pages=%d, error=%v", status, len(pages), err)
			}
		})
	}
}

func TestQueueVoiceNBAGamesTodayReportsFetchFailure(t *testing.T) {
	previousConfig, previousClient := vars.APIConfig.Productivity, externalApiClient
	t.Cleanup(func() { vars.APIConfig.Productivity, externalApiClient = previousConfig, previousClient })
	// Explicit requests work even when automatic NBA updates are disabled.
	vars.APIConfig.Productivity.NBA = vars.NBAConfig{FavoriteTeams: []string{"LAL"}}
	externalApiClient = &http.Client{Transport: bedtimeScoreboardTransport(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	if err := QueueVoiceNBAGamesToday(""); err == nil {
		t.Fatal("accepted a voice request without a robot")
	}
	if err := QueueVoiceNBAGamesToday("schedule-robot"); err != nil {
		t.Fatal(err)
	}
	select {
	case task := <-taskQueue:
		if task.RobotESN != "schedule-robot" || task.Source != nbaGamesTodaySource || !taskIsCurrent(task) {
			t.Fatalf("unexpected queued task: %s, robot=%s, source=%s", task.ID, task.RobotESN, task.Source)
		}
		if !task.ExpiresAt.After(time.Now()) || task.ExpiresAt.Format("15:04:05") != "00:00:00" {
			t.Fatalf("today's schedule must expire at the next local midnight, got %v", task.ExpiresAt)
		}
		if len(task.Pages) != 1 || !strings.Contains(task.Pages[0].Speech, "couldn't get today's NBA games") {
			t.Fatal("failed request did not queue an explanation; acknowledgement must not use the reminder queue")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("NBA voice request did not queue a response")
	}
}
