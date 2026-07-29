package productivity

import (
	"context"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

func TestNormalizeNBATeams(t *testing.T) {
	got := NormalizeNBATeams([]string{"lal", "BOS", "lal", "invalid", " NY "})
	want := []string{"BOS", "LAL", "NY"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("NormalizeNBATeams() = %v, want %v", got, want)
	}
}

func TestNBAPregameNotification(t *testing.T) {
	resetNBANotificationState()
	now := time.Date(2026, time.January, 2, 1, 0, 0, 0, time.UTC)
	game := testNBAGame("pregame", "pre", now.Add(10*time.Minute), "0", "0")
	config := vars.NBAConfig{PregameMinutes: 15}

	kind, phrase, notify := nbaNotificationForGame(game, config, now)
	if !notify || kind != "pregame" || !strings.Contains(phrase, "about 10 minutes") {
		t.Fatalf("notification = (%q, %q, %v), want pregame notification", kind, phrase, notify)
	}
	markNBANotified(game.ID, kind, now)
	if _, _, notify := nbaNotificationForGame(game, config, now); notify {
		t.Fatal("pregame notification repeated")
	}
}

func TestNBALiveNotificationInterval(t *testing.T) {
	resetNBANotificationState()
	now := time.Date(2026, time.January, 2, 1, 0, 0, 0, time.UTC)
	game := testNBAGame("live", "in", now, "88", "91")
	game.Status.Type.ShortDetail = "2:14 - 3rd"
	config := vars.NBAConfig{LiveUpdateMinutes: 5}

	kind, _, notify := nbaNotificationForGame(game, config, now)
	if !notify || kind != "live" {
		t.Fatal("first live score was not announced")
	}
	markNBANotified(game.ID, kind, now)
	if _, _, notify := nbaNotificationForGame(game, config, now.Add(4*time.Minute)); notify {
		t.Fatal("live score repeated before configured interval")
	}
	if _, _, notify := nbaNotificationForGame(game, config, now.Add(5*time.Minute)); !notify {
		t.Fatal("live score was not repeated at configured interval")
	}
}

func TestNBANotableLeadChangeBypassesLiveInterval(t *testing.T) {
	resetNBANotificationState()
	now := time.Date(2026, time.January, 2, 1, 0, 0, 0, time.UTC)
	game := testNBAGame("lead-change", "in", now, "100", "99")
	game.Status.Type.ShortDetail = "3:00 - 4th"
	config := vars.NBAConfig{LiveUpdateMinutes: 30, NotifyNotable: true}

	kind, _, notify := nbaNotificationForGame(game, config, now)
	if !notify || kind != "live" {
		t.Fatalf("initial notification = (%q, %v)", kind, notify)
	}
	markNBANotified(game.ID, kind, now)
	game.Competitions[0].Competitors[1].Score = "101"
	kind, phrase, notify := nbaNotificationForGame(game, config, now.Add(time.Minute))
	if !notify || kind != "notable" || !strings.HasPrefix(phrase, "NBA Alert.") || !strings.Contains(phrase, "taken the lead") {
		t.Fatalf("lead-change notification = (%q, %q, %v)", kind, phrase, notify)
	}
}

func TestNBANotableLateCloseGame(t *testing.T) {
	resetNBANotificationState()
	now := time.Date(2026, time.January, 2, 1, 0, 0, 0, time.UTC)
	game := testNBAGame("clutch", "in", now, "100", "95")
	game.Status.Type.ShortDetail = "3:00 - 4th"
	config := vars.NBAConfig{LiveUpdateMinutes: 30, NotifyNotable: true}

	kind, _, notify := nbaNotificationForGame(game, config, now)
	if !notify || kind != "live" {
		t.Fatalf("initial notification = (%q, %v)", kind, notify)
	}
	markNBANotified(game.ID, kind, now)
	game.Competitions[0].Competitors[1].Score = "98"
	game.Status.Type.ShortDetail = "1:30 - 4th"
	kind, phrase, notify := nbaNotificationForGame(game, config, now.Add(time.Minute))
	if !notify || kind != "notable" || !strings.HasPrefix(phrase, "NBA Alert.") || !strings.Contains(phrase, "Close game") {
		t.Fatalf("clutch notification = (%q, %q, %v)", kind, phrase, notify)
	}
}

func TestSpokenNBAGameDetail(t *testing.T) {
	tests := []struct {
		detail string
		want   string
	}{
		{detail: "4:39 - 3rd", want: "4 minutes 39 seconds left in the third quarter"},
		{detail: "1:01 - 1st", want: "1 minute 1 second left in the first quarter"},
		{detail: "0:08 - 4th", want: "8 seconds left in the fourth quarter"},
		{detail: "2:00 - OT", want: "2 minutes left in overtime"},
		{detail: "Halftime", want: "Halftime"},
	}
	for _, tt := range tests {
		if got := spokenNBAGameDetail(tt.detail); got != tt.want {
			t.Errorf("spokenNBAGameDetail(%q) = %q, want %q", tt.detail, got, tt.want)
		}
	}
}

func TestSpokenNBATeamNameExpandsLAClippers(t *testing.T) {
	competitor := nbaCompetitor{}
	competitor.Team.Abbreviation = "LAC"
	competitor.Team.DisplayName = "LA Clippers"
	if got := spokenNBATeamName(competitor); got != "Los Angeles Clippers" {
		t.Fatalf("spokenNBATeamName() = %q, want %q", got, "Los Angeles Clippers")
	}
}

func TestSelectNBATopPerformer(t *testing.T) {
	summary := nbaSummary{}
	team := nbaBoxscoreTeam{}
	team.Team.Logo = "team-logo"
	table := nbaPlayerStatistics{Labels: []string{"MIN", "PTS", "REB", "AST"}}
	first := nbaBoxscoreAthlete{Stats: []string{"35", "28", "6", "5"}}
	first.Athlete.DisplayName = "First Player"
	second := nbaBoxscoreAthlete{Stats: []string{"38", "24", "12", "9"}}
	second.Athlete.DisplayName = "Second Player"
	second.Athlete.Headshot.Href = "portrait"
	table.Athletes = []nbaBoxscoreAthlete{first, second}
	team.Statistics = []nbaPlayerStatistics{table}
	summary.Boxscore.Players = []nbaBoxscoreTeam{team}

	got, err := selectNBATopPerformer(summary)
	if err != nil {
		t.Fatalf("selectNBATopPerformer() error = %v", err)
	}
	if got.Name != "Second Player" || got.Points != 24 || got.Rebounds != 12 || got.Assists != 9 || got.Headshot != "portrait" || got.TeamLogo != "team-logo" {
		t.Fatalf("selectNBATopPerformer() = %#v", got)
	}
}

func TestSelectRandomNBAStarter(t *testing.T) {
	summary := nbaSummary{}
	team := nbaBoxscoreTeam{}
	team.Team.Abbreviation = "BOS"
	team.Team.Logo = "boston-logo"
	table := nbaPlayerStatistics{Labels: []string{"PTS", "REB", "AST"}}
	bench := nbaBoxscoreAthlete{Stats: []string{"30", "8", "7"}}
	bench.Athlete.DisplayName = "Bench Player"
	starter := nbaBoxscoreAthlete{Starter: true, Stats: []string{"18", "10", "6"}}
	starter.Athlete.DisplayName = "Starting Player"
	starter.Athlete.Headshot.Href = "starter-portrait"
	table.Athletes = []nbaBoxscoreAthlete{bench, starter}
	team.Statistics = []nbaPlayerStatistics{table}
	summary.Boxscore.Players = []nbaBoxscoreTeam{team}

	got, err := selectRandomNBAStarter(summary, "BOS", rand.New(rand.NewSource(42)))
	if err != nil {
		t.Fatalf("selectRandomNBAStarter() error = %v", err)
	}
	if got.Name != "Starting Player" || got.Points != 18 || got.Rebounds != 10 || got.Assists != 6 || got.TeamLogo != "boston-logo" {
		t.Fatalf("selectRandomNBAStarter() = %#v", got)
	}
}

func TestRenderNBAPerformerFace(t *testing.T) {
	faceData, err := renderNBAPerformerFace(context.Background(), nbaTopPerformer{
		Name: "Pascal Siakam", Points: 26, Rebounds: 10, Assists: 6,
	})
	if err != nil {
		t.Fatalf("renderNBAPerformerFace() error = %v", err)
	}
	if len(faceData) != 184*96*2 {
		t.Fatalf("face data length = %d, want %d", len(faceData), 184*96*2)
	}
}

func TestNBAFinalTaskPagesIncludeOrderedSpeech(t *testing.T) {
	performer := nbaTopPerformer{Name: "Test Player", Points: 31, Rebounds: 1, Assists: 1}
	pages := nbaFinalTaskPages([]byte{1}, []byte{2}, "Final score speech.", performer)
	if len(pages) != 2 {
		t.Fatalf("nbaFinalTaskPages() returned %d pages, want 2", len(pages))
	}
	if pages[0].Speech != "Final score speech." || pages[0].FaceData[0] != 1 {
		t.Fatalf("scoreboard page = %#v", pages[0])
	}
	wantPerformerSpeech := "Top performer, Test Player, with 31 points, 1 rebound, and 1 assist."
	if pages[1].Speech != wantPerformerSpeech || pages[1].FaceData[0] != 2 {
		t.Fatalf("performer page = %#v, want speech %q", pages[1], wantPerformerSpeech)
	}
}

func TestNBAFinalNotificationAndFaceRender(t *testing.T) {
	resetNBANotificationState()
	now := time.Date(2026, time.January, 2, 1, 0, 0, 0, time.UTC)
	game := testNBAGame("final", "post", now, "104", "111")
	config := vars.NBAConfig{NotifyFinal: true}

	kind, phrase, notify := nbaNotificationForGame(game, config, now)
	if !notify || kind != "final" || !strings.Contains(phrase, "104") || !strings.Contains(phrase, "111") {
		t.Fatalf("final notification = (%q, %q, %v)", kind, phrase, notify)
	}
	faceData, err := renderNBAScoreFace(context.Background(), game, time.UTC)
	if err != nil {
		t.Fatalf("renderNBAScoreFace() error = %v", err)
	}
	if len(faceData) != 184*96*2 {
		t.Fatalf("face data length = %d, want %d", len(faceData), 184*96*2)
	}
}

func TestRandomNBATestGameUsesDistinctTeams(t *testing.T) {
	game := randomNBATestGame(rand.New(rand.NewSource(42)))
	away, home, ok := nbaGameTeams(game)
	if !ok {
		t.Fatal("random test game has no home/away teams")
	}
	if away.Team.Abbreviation == home.Team.Abbreviation {
		t.Fatalf("random test game selected %s twice", away.Team.Abbreviation)
	}
	if away.Score == "" || home.Score == "" || away.Team.Logo == "" || home.Team.Logo == "" {
		t.Fatalf("random test game is incomplete: %#v", game)
	}
	if game.Status.Type.State != "in" {
		t.Fatalf("random test game state = %q, want in", game.Status.Type.State)
	}
}

func TestRandomNBAFinalTestGame(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	team, performer := fallbackNBAFinalTestStarter(rng)
	game, performer := randomNBAFinalTestGame(rng, team, performer)
	away, home, ok := nbaGameTeams(game)
	if !ok {
		t.Fatal("random final test game has no home/away teams")
	}
	if game.Status.Type.State != "post" || game.Status.Type.ShortDetail != "Final" {
		t.Fatalf("random final test status = (%q, %q), want post/final", game.Status.Type.State, game.Status.Type.ShortDetail)
	}
	if away.Team.Abbreviation == home.Team.Abbreviation || away.Score == home.Score {
		t.Fatalf("random final test matchup is invalid: %#v", game)
	}
	if performer.Name == "" || performer.Headshot == "" || performer.TeamLogo == "" {
		t.Fatalf("random final test performer is incomplete: %#v", performer)
	}
	if performer.TeamLogo != away.Team.Logo && performer.TeamLogo != home.Team.Logo {
		t.Fatalf("performer logo %q does not belong to either test team", performer.TeamLogo)
	}
}

func TestNBASeasonYear(t *testing.T) {
	if got := nbaSeasonYear(time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)); got != 2026 {
		t.Fatalf("nbaSeasonYear(June 2026) = %d, want 2026", got)
	}
	if got := nbaSeasonYear(time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)); got != 2027 {
		t.Fatalf("nbaSeasonYear(October 2026) = %d, want 2027", got)
	}
}

func testNBAGame(id, state string, start time.Time, awayScore, homeScore string) nbaEvent {
	game := nbaEvent{ID: id, Date: start.Format(time.RFC3339)}
	game.Status.Type.State = state
	game.Status.Type.Detail = strings.ToUpper(state)
	competition := nbaCompetition{Competitors: make([]nbaCompetitor, 2)}
	competition.Competitors[0].HomeAway = "away"
	competition.Competitors[0].Score = awayScore
	competition.Competitors[0].Team.Abbreviation = "LAL"
	competition.Competitors[0].Team.DisplayName = "Los Angeles Lakers"
	competition.Competitors[1].HomeAway = "home"
	competition.Competitors[1].Score = homeScore
	competition.Competitors[1].Team.Abbreviation = "BOS"
	competition.Competitors[1].Team.DisplayName = "Boston Celtics"
	game.Competitions = []nbaCompetition{competition}
	return game
}
