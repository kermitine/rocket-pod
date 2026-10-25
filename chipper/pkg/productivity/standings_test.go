package productivity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestQueueVoiceStandingsRespondsWhenAcknowledgementUnavailable(t *testing.T) {
	previousClient := externalApiClient
	t.Cleanup(func() { externalApiClient = previousClient })
	externalApiClient = &http.Client{Transport: bedtimeScoreboardTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	for _, kind := range []string{standingsNBAEast, standingsF1Drivers, standingsF1Constructors} {
		t.Run(kind, func(t *testing.T) {
			if err := QueueVoiceStandings("standings-test-robot", kind); err != nil {
				t.Fatal(err)
			}
			select {
			case task := <-taskQueue:
				if task.RobotESN != "standings-test-robot" || task.Source != "standings" || !taskIsCurrent(task) || len(task.Pages) == 0 {
					t.Fatalf("expected standings response without a queued acknowledgement: %v", task)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("acknowledgement failure blocked the standings response")
			}
		})
	}
}

func TestMatchStandingsVoiceCommand(t *testing.T) {
	tests := []struct {
		text string
		kind string
		ok   bool
	}{
		{"show me the eastern conference standings", standingsNBAEast, true},
		{"show the eastern standings", standingsNBAEast, true},
		{"what are the NBA western conference rankings", standingsNBAWest, true},
		{"and b a western conference standings", standingsNBAWest, true},
		{"N. B. A. standings", standingsNBAAll, true},
		{"show the NBA standings", standingsNBAAll, true},
		{"who's leading the eastern conference", standingsNBAEast, true},
		{"who is first in the NBA west", standingsNBAWest, true},
		{"show me the NBA playoff picture", standingsNBAAll, true},
		{"show Formula 1 driver standings", standingsF1Drivers, true},
		{"if one driver standings", standingsF1Drivers, true},
		{"driver championship rankings", standingsF1Drivers, true},
		{"F1 drivers championship", standingsF1Drivers, true},
		{"who leads the Formula One driver championship", standingsF1Drivers, true},
		{"f one constructor table", standingsF1Constructors, true},
		{"F1 constructor leaderboard", standingsF1Constructors, true},
		{"constructor standings", standingsF1Constructors, true},
		{"constructors championship order", standingsF1Constructors, true},
		{"F1 team standings", standingsF1Constructors, true},
		{"NBA team standings", standingsNBAAll, true},
		{"help me understand NBA teams", "", false},
		{"team standings", "", false},
		{"how is Ferrari doing", "", false},
	}
	for _, tt := range tests {
		kind, ok := MatchStandingsVoiceCommand(tt.text)
		if kind != tt.kind || ok != tt.ok {
			t.Errorf("MatchStandingsVoiceCommand(%q) = %q, %v; want %q, %v", tt.text, kind, ok, tt.kind, tt.ok)
		}
	}
}

func TestNormalizeStandingsVoiceTextHandlesASRConfusions(t *testing.T) {
	tests := map[string]string{
		"and be a standings":    "nba standings",
		"n. b. a. standings":    "nba standings",
		"N B.A. standings":      "nba standings",
		"if won standings":      "f1 standings",
		"formula won rankings":  "formula one rankings",
		"driver's championship": "drivers championship",
		"contractor points":     "constructor points",
	}
	for input, want := range tests {
		if got := normalizeStandingsVoiceText(input); got != want {
			t.Errorf("normalizeStandingsVoiceText(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNBAStandingsPagesIncludeFullConference(t *testing.T) {
	response := espnStandingsResponse{Children: []espnStandingsGroup{standingsNBAGroup("Eastern Conference", "E", 15), standingsNBAGroup("Western Conference", "W", 15)}}
	pages, err := nbaStandingsPages(context.Background(), response, standingsNBAEast)
	if err != nil {
		t.Fatalf("nbaStandingsPages() error = %v", err)
	}
	if len(pages) != 3 {
		t.Fatalf("NBA East pages = %d, want 3", len(pages))
	}
	speech := standingsPageSpeech(pages)
	for position := 1; position <= 15; position++ {
		if !strings.Contains(speech, fmt.Sprintf("Eastern Team %d", position)) {
			t.Errorf("speech omitted Eastern Team %d", position)
		}
	}
	if len(pages[0].FaceData) == 0 {
		t.Fatal("NBA standings face data is empty")
	}
	if strings.Contains(pages[1].Speech, "Continuing") || !strings.HasPrefix(pages[1].Speech, "sixth,") {
		t.Fatalf("second standings page has an unnecessary introduction: %s", pages[1].Speech)
	}

	allPages, err := nbaStandingsPages(context.Background(), response, standingsNBAAll)
	if err != nil || len(allPages) != 6 {
		t.Fatalf("all NBA pages = %d, %v; want 6", len(allPages), err)
	}
}

func TestStandingsIntentNamesDescribeMatchedCommand(t *testing.T) {
	tests := map[string]string{
		standingsNBAEast:        "intent_sports_nba_east_standings",
		standingsNBAWest:        "intent_sports_nba_west_standings",
		standingsNBAAll:         "intent_sports_nba_standings",
		standingsF1Drivers:      "intent_sports_f1_driver_standings",
		standingsF1Constructors: "intent_sports_f1_constructor_standings",
	}
	for kind, want := range tests {
		if got := StandingsIntentName(kind); got != want {
			t.Errorf("StandingsIntentName(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestF1StandingsPagesIncludeEveryDriverAndConstructor(t *testing.T) {
	response := espnStandingsResponse{Children: []espnStandingsGroup{standingsF1Group("Driver Standings", false, 22), standingsF1Group("Constructor Standings", true, 11)}}
	driverPages, err := f1StandingsPages(response, standingsF1Drivers)
	if err != nil || len(driverPages) != 5 {
		t.Fatalf("driver pages = %d, %v; want 5", len(driverPages), err)
	}
	constructorPages, err := f1StandingsPages(response, standingsF1Constructors)
	if err != nil || len(constructorPages) != 3 {
		t.Fatalf("constructor pages = %d, %v; want 3", len(constructorPages), err)
	}
	if !strings.Contains(standingsPageSpeech(driverPages), "Driver 22") {
		t.Fatal("driver speech omitted the final driver")
	}
	if !strings.Contains(standingsPageSpeech(constructorPages), "Constructor 11") {
		t.Fatal("constructor speech omitted the final constructor")
	}
	if strings.Contains(standingsPageSpeech(constructorPages), ", 0 points") || !strings.Contains(standingsPageSpeech(constructorPages), "two ninety nine points") {
		t.Fatalf("constructor speech has incorrect points: %s", standingsPageSpeech(constructorPages))
	}
}

func TestF1StandingsDecodeESPNStringStatValues(t *testing.T) {
	payload := []byte(`{
		"children": [{
			"name": "Driver Standings",
			"standings": {
				"entries": [{
					"athlete": {"id": "5829", "displayName": "Kimi Antonelli"},
					"stats": [
						{"name": "rank", "value": 1.0, "displayValue": "1"},
						{"name": "championshipPts", "type": "points", "value": 156.0, "displayValue": "156"},
						{"name": "BRN", "value": " ", "displayValue": " "}
					]
				}]
			}
		}]
	}`)
	var response espnStandingsResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("F1 standings decode failed: %v", err)
	}
	pages, err := f1StandingsPages(response, standingsF1Drivers)
	if err != nil {
		t.Fatal(err)
	}
	if speech := standingsPageSpeech(pages); !strings.Contains(speech, "one fifty six points") {
		t.Fatalf("driver points were not parsed: %s", speech)
	}
}

func TestStandingStatParsesNumericStringValue(t *testing.T) {
	var stat espnStandingStat
	if err := json.Unmarshal([]byte(`{"name":"rank","value":"12","displayValue":""}`), &stat); err != nil {
		t.Fatal(err)
	}
	if got := standingStat([]espnStandingStat{stat}, "rank"); got != "12" {
		t.Fatalf("standingStat() = %q, want 12", got)
	}
}

func TestF1StandingsUseSportsNumberShorthand(t *testing.T) {
	for _, kind := range []string{standingsF1Drivers, standingsF1Constructors} {
		for _, source := range []string{"display", "numeric"} {
			for _, test := range []struct {
				points float64
				want   string
			}{
				{0, "0"}, {99, "99"}, {100, "one hundred"}, {104, "one oh four"},
				{110, "one ten"}, {133, "one thirty three"}, {156.5, "one fifty six point five"},
				{205, "two oh five"}, {999, "nine ninety nine"}, {1000, "1000"},
			} {
				t.Run(fmt.Sprintf("%s/%s/%g", kind, source, test.points), func(t *testing.T) {
					constructor := kind == standingsF1Constructors
					name := "Driver Standings"
					if constructor {
						name = "Constructor Standings"
					}
					group := standingsF1Group(name, constructor, 1)
					stat := &group.Standings.Entries[0].Stats[1]
					stat.Value = test.points
					stat.DisplayValue = fmt.Sprint(test.points)
					if source == "numeric" {
						stat.DisplayValue = ""
					}
					pages, err := f1StandingsPages(espnStandingsResponse{Children: []espnStandingsGroup{group}}, kind)
					if err != nil {
						t.Fatal(err)
					}
					if speech := standingsPageSpeech(pages); !strings.Contains(speech, test.want+" points.") {
						t.Fatalf("standings speech = %q, want %q", speech, test.want+" points.")
					}
				})
			}
		}
	}
}

func TestStandingsPagesSortRowsByPosition(t *testing.T) {
	rows := []standingsRow{
		{Position: 3, Name: "Third", Speech: "three points"},
		{Position: 1, Name: "First", Speech: "one point"},
		{Position: 2, Name: "Second", Speech: "two points"},
	}
	pages := standingsTaskPages(context.Background(), "Standings", "standings", rows, false)
	if len(pages) != 1 {
		t.Fatalf("pages = %d, want 1", len(pages))
	}
	speech := pages[0].Speech
	first := strings.Index(speech, "First")
	second := strings.Index(speech, "Second")
	third := strings.Index(speech, "Third")
	if first < 0 || second <= first || third <= second {
		t.Fatalf("standings speech is not ordered by position: %s", speech)
	}
}

func TestNBAStandingsUseShortTeamNamesForSpeech(t *testing.T) {
	group := standingsNBAGroup("Western Conference", "W", 1)
	group.Standings.Entries[0].Team.DisplayName = "LA Clippers"
	group.Standings.Entries[0].Team.Abbreviation = "LAC"
	response := espnStandingsResponse{Children: []espnStandingsGroup{group}}

	pages, err := nbaStandingsPages(context.Background(), response, standingsNBAWest)
	if err != nil {
		t.Fatal(err)
	}
	if speech := standingsPageSpeech(pages); !strings.Contains(speech, "Clippers") || strings.Contains(speech, "Los Angeles") || strings.Contains(speech, "LA Clippers") {
		t.Fatalf("standings did not use the short team name: %s", speech)
	}
}

func TestF1OrdinalLong(t *testing.T) {
	for value, want := range map[int]string{1: "first", 10: "tenth", 11: "11th", 12: "12th", 13: "13th", 21: "21st", 22: "22nd"} {
		if got := f1OrdinalLong(value); got != want {
			t.Errorf("f1OrdinalLong(%d) = %q, want %q", value, got, want)
		}
	}
}

func standingsNBAGroup(name, prefix string, count int) espnStandingsGroup {
	group := espnStandingsGroup{Name: name}
	for position := 1; position <= count; position++ {
		entry := espnStandingsEntry{}
		entry.Team.DisplayName = fmt.Sprintf("%s Team %d", name[:7], position)
		entry.Team.Abbreviation = fmt.Sprintf("%s%02d", prefix, position)
		entry.Stats = []espnStandingStat{
			{Name: "playoffSeed", DisplayValue: fmt.Sprint(position)},
			{Name: "wins", DisplayValue: fmt.Sprint(60 - position)},
			{Name: "losses", DisplayValue: fmt.Sprint(20 + position)},
		}
		group.Standings.Entries = append(group.Standings.Entries, entry)
	}
	return group
}

func standingsF1Group(name string, constructors bool, count int) espnStandingsGroup {
	group := espnStandingsGroup{Name: name}
	for position := 1; position <= count; position++ {
		entry := espnStandingsEntry{}
		if constructors {
			entry.Team.DisplayName = fmt.Sprintf("Constructor %d", position)
			entry.Team.ShortDisplayName = fmt.Sprintf("C%d", position)
			entry.Team.Color = "FF0000"
		} else {
			entry.Athlete.ID = fmt.Sprint(1000 + position)
			entry.Athlete.DisplayName = fmt.Sprintf("Driver %d", position)
		}
		pointsName := "championshipPts"
		if constructors {
			pointsName = "points"
		}
		entry.Stats = []espnStandingStat{{Name: "rank", DisplayValue: fmt.Sprint(position)}, {Name: pointsName, DisplayValue: fmt.Sprint(300 - position)}}
		group.Standings.Entries = append(group.Standings.Entries, entry)
	}
	return group
}

func standingsPageSpeech(pages []TaskPage) string {
	parts := make([]string, len(pages))
	for index, page := range pages {
		parts[index] = page.Speech
	}
	return strings.Join(parts, " ")
}
