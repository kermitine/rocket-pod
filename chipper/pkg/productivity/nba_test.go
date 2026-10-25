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

func TestSpokenNBAScoreUsesShorthand(t *testing.T) {
	for _, test := range []struct{ score, want string }{
		{"133", "one thirty three"}, {"130", "one thirty"},
		{"104", "one oh four"}, {"109", "one oh nine"},
		{"110", "one ten"}, {"111", "one eleven"}, {"119", "one nineteen"},
		{"100", "one hundred"}, {"200", "two hundred"}, {"205", "two oh five"},
		{"99", "99"}, {"0", "0"}, {"", "0"}, {" 133 ", "one thirty three"},
		{"unavailable", "unavailable"}, {"1000", "1000"},
	} {
		if got := spokenNBAScore(test.score); got != test.want {
			t.Errorf("spokenNBAScore(%q) = %q, want %q", test.score, got, test.want)
		}
	}
}

func TestNBAAnnouncementsShareScoreShorthand(t *testing.T) {
	for _, test := range []struct {
		name, beforeAway, beforeHome, away, home, detail, state, wantKind string
		want                                                              []string
	}{
		{"live", "133", "132", "133", "132", "7:20 - 1st", "in", "live", []string{"one thirty three", "one thirty two"}},
		{"final", "133", "132", "133", "132", "Final", "post", "final", []string{"one thirty three", "one thirty two"}},
		{"away lead", "131", "132", "133", "132", "3:00 - 4th", "in", "notable", []string{"one thirty three to one thirty two"}},
		{"home lead", "133", "132", "133", "134", "3:00 - 4th", "in", "notable", []string{"one thirty four to one thirty three"}},
		{"tie", "133", "132", "133", "133", "3:00 - 4th", "in", "notable", []string{"tied at one thirty three"}},
		{"close game", "133", "128", "133", "131", "1:20 - 4th", "in", "notable", []string{"Close game", "one thirty three", "one thirty one"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetNBANotificationState()
			t.Cleanup(resetNBANotificationState)
			now := time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC)
			game := testNBAGame("score-shorthand", "in", now, test.beforeAway, test.beforeHome)
			game.Status.Type.ShortDetail = "3:00 - 4th"
			config := vars.NBAConfig{NotifyNotable: true, NotifyFinal: true, LiveUpdateMinutes: 30}
			if test.name != "live" {
				kind, _, _ := nbaNotificationForGame(game, config, now)
				markNBANotified(game.ID, kind, now)
			}
			game.Status.Type.State, game.Status.Type.ShortDetail = test.state, test.detail
			game.Competitions[0].Competitors[0].Score = test.away
			game.Competitions[0].Competitors[1].Score = test.home
			kind, phrase, notify := nbaNotificationForGame(game, config, now.Add(time.Minute))
			if !notify || kind != test.wantKind {
				t.Fatalf("notification = %q, %q, %v", kind, phrase, notify)
			}
			for _, want := range test.want {
				if !strings.Contains(phrase, want) {
					t.Errorf("speech %q does not contain %q", phrase, want)
				}
			}
		})
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

func TestParseNBAGameTimeSupportsESPNFormats(t *testing.T) {
	want := time.Date(2026, 10, 24, 23, 30, 0, 0, time.UTC)
	for _, value := range []string{"2026-10-24T23:30Z", "2026-10-24T23:30:00Z", "2026-10-24T16:30-07:00", "2026-10-24T16:30:00-07:00"} {
		got, err := parseNBAGameTime(value)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseNBAGameTime(%q)=%v, %v; want %v", value, got, err, want)
		}
	}
	if _, err := parseNBAGameTime("not a date"); err == nil {
		t.Fatal("accepted an invalid NBA time")
	}
}

func TestNBAPregameNotificationAndFaceAcceptMinutePrecision(t *testing.T) {
	resetNBANotificationState()
	t.Cleanup(resetNBANotificationState)
	now := time.Date(2026, 10, 24, 23, 20, 0, 0, time.UTC)
	game := testNBAGame("minute-time", "pre", now.Add(10*time.Minute), "0", "0")
	game.Date = "2026-10-24T23:30Z"
	kind, _, notify := nbaNotificationForGame(game, vars.NBAConfig{PregameMinutes: 15}, now)
	if !notify || kind != "pregame" {
		t.Fatal("ESPN's minute-precision time suppressed a pregame reminder")
	}
	if got := nbaFaceStatus(game, time.UTC); got != "11:30 PM" {
		t.Fatalf("pregame start time on face=%q", got)
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

func TestNBANotableLeadChangesWaitUntilFourthQuarter(t *testing.T) {
	for _, detail := range []string{"5:00 - 1st", "5:00 - 2nd", "5:00 - 3rd", "Halftime", "Unknown"} {
		t.Run(detail, func(t *testing.T) {
			resetNBANotificationState()
			t.Cleanup(resetNBANotificationState)
			now := time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC)
			game := testNBAGame("early-lead", "in", now, "100", "99")
			game.Status.Type.ShortDetail = detail
			config := vars.NBAConfig{LiveUpdateMinutes: 30, NotifyNotable: true}
			kind, _, notify := nbaNotificationForGame(game, config, now)
			if !notify || kind != "live" {
				t.Fatal("regular live update was suppressed")
			}
			markNBANotified(game.ID, kind, now)
			game.Competitions[0].Competitors[1].Score = "101"
			if kind, _, notify := nbaNotificationForGame(game, config, now.Add(time.Minute)); notify {
				t.Fatalf("early lead change announced as %s", kind)
			}
			game.Status.Type.ShortDetail = "12:00 - 4th"
			if _, _, notify := nbaNotificationForGame(game, config, now.Add(2*time.Minute)); notify {
				t.Fatal("starting the fourth quarter replayed an earlier lead change")
			}
			game.Competitions[0].Competitors[0].Score = "102"
			game.Status.Type.ShortDetail = "11:00 - 4th"
			kind, phrase, notify := nbaNotificationForGame(game, config, now.Add(3*time.Minute))
			if !notify || kind != "notable" || !strings.Contains(phrase, "taken the lead") {
				t.Fatalf("fourth-quarter lead change = (%s, %s, %v)", kind, phrase, notify)
			}
		})
	}
}

func TestNBANotableOvertimeLeadChanges(t *testing.T) {
	for _, detail := range []string{"3:00 - OT", "3:00 - 2OT", "3:00 - 3OT"} {
		t.Run(detail, func(t *testing.T) {
			resetNBANotificationState()
			t.Cleanup(resetNBANotificationState)
			now := time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC)
			game := testNBAGame("overtime-lead", "in", now, "100", "99")
			game.Status.Type.ShortDetail = detail
			config := vars.NBAConfig{LiveUpdateMinutes: 30, NotifyNotable: true}
			kind, _, _ := nbaNotificationForGame(game, config, now)
			markNBANotified(game.ID, kind, now)
			game.Competitions[0].Competitors[1].Score = "101"
			kind, phrase, notify := nbaNotificationForGame(game, config, now.Add(time.Minute))
			if !notify || kind != "notable" || !strings.Contains(phrase, "taken the lead") || !strings.Contains(phrase, "overtime") {
				t.Fatalf("overtime lead change = (%s, %s, %v)", kind, phrase, notify)
			}
		})
	}
}

func TestSpokenNBAGameDetail(t *testing.T) {
	tests := []struct {
		detail string
		want   string
	}{
		{detail: "7:20 - 1st", want: "7 minutes left in the 1st"},
		{detail: "7:29 - 1st", want: "7 minutes left in the 1st"},
		{detail: "7:30 - 1st", want: "8 minutes left in the 1st"},
		{detail: "7:59 - 1st", want: "8 minutes left in the 1st"},
		{detail: "4:39 - 3rd", want: "5 minutes left in the 3rd"},
		{detail: "2:00 - 2nd", want: "2 minutes left in the 2nd"},
		{detail: "1:01 - 1st", want: "1 minute left in the 1st"},
		{detail: "0:08 - 4th", want: "less than a minute left in the 4th"},
		{detail: "0:29 - 4th", want: "less than a minute left in the 4th"},
		{detail: "0:30 - 4th", want: "1 minute left in the 4th"},
		{detail: "0:00 - 4th", want: "no time left in the 4th"},
		{detail: "2:00 - OT", want: "2 minutes left in overtime"},
		{detail: "1:05 - 2OT", want: "1 minute left in overtime"},
		{detail: "Halftime", want: "Halftime"},
	}
	for _, tt := range tests {
		if got := spokenNBAGameDetail(tt.detail); got != tt.want {
			t.Errorf("spokenNBAGameDetail(%q) = %q, want %q", tt.detail, got, tt.want)
		}
	}
}

func TestNBANotificationsShareShortClockSpeech(t *testing.T) {
	for _, test := range []struct {
		name, clock, homeScore, want string
	}{
		{"live", "7:20 - 1st", "99", "7 minutes left in the 1st"},
		{"lead change", "7:20 - 4th", "101", "7 minutes left in the 4th"},
		{"tie", "7:20 - 4th", "100", "7 minutes left in the 4th"},
		{"close game", "1:20 - 4th", "98", "1 minute left in the 4th"},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetNBANotificationState()
			t.Cleanup(resetNBANotificationState)
			now := time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC)
			game := testNBAGame("short-clock", "in", now, "100", "95")
			game.Status.Type.ShortDetail = "3:00 - 4th"
			config := vars.NBAConfig{LiveUpdateMinutes: 30, NotifyNotable: true}
			if test.name != "live" {
				kind, _, _ := nbaNotificationForGame(game, config, now)
				markNBANotified(game.ID, kind, now)
			}
			game.Status.Type.ShortDetail = test.clock
			game.Competitions[0].Competitors[1].Score = test.homeScore
			kind, phrase, notify := nbaNotificationForGame(game, config, now.Add(time.Minute))
			wantKind := "notable"
			if test.name == "live" {
				wantKind = "live"
			}
			if !notify || kind != wantKind || !strings.Contains(phrase, test.want) {
				t.Fatalf("notification = (%s, %q, %v), want %s with %q", kind, phrase, notify, wantKind, test.want)
			}
		})
	}
}

func TestSpokenNBATeamNamesUseShorthand(t *testing.T) {
	for _, test := range []struct{ full, code, want string }{
		{"Philadelphia 76ers", "PHI", "76ers"},
		{"Denver Nuggets", "DEN", "Nuggets"},
		{"Los Angeles Lakers", "LAL", "Lakers"},
		{"LA Clippers", "LAC", "Clippers"},
		{"Golden State Warriors", "GS", "Warriors"},
		{"Portland Trail Blazers", "POR", "Trail Blazers"},
		{"Philadelphia 76ers", "", "76ers"},
		{"Los Angeles Clippers", "", "Clippers"},
		{" Denver Nuggets ", " den ", "Nuggets"},
		{"Unknown Team", "NEW", "Unknown Team"},
		{"", "NEW", "NEW"},
	} {
		competitor := nbaCompetitor{}
		competitor.Team.DisplayName, competitor.Team.Abbreviation = test.full, test.code
		if got := spokenNBATeamName(competitor); got != test.want {
			t.Errorf("spokenNBATeamName(%q, %q) = %q, want %q", test.full, test.code, got, test.want)
		}
	}
}

func TestNBANotificationsUseShortTeamNames(t *testing.T) {
	for _, state := range []string{"pre", "in", "post"} {
		t.Run(state, func(t *testing.T) {
			resetNBANotificationState()
			t.Cleanup(resetNBANotificationState)
			now := time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC)
			game := testNBAGame("short-teams", "in", now.Add(10*time.Minute), "104", "111")
			game.Competitions[0].Competitors[0].Team.Abbreviation = "PHI"
			game.Competitions[0].Competitors[0].Team.DisplayName = "Philadelphia 76ers"
			game.Competitions[0].Competitors[1].Team.Abbreviation = "DEN"
			game.Competitions[0].Competitors[1].Team.DisplayName = "Denver Nuggets"
			config := vars.NBAConfig{PregameMinutes: 15, NotifyFinal: true}
			// Observe the unfinished game so its final score remains eligible.
			nbaNotificationForGame(game, config, now)
			game.Status.Type.State = state
			_, phrase, notify := nbaNotificationForGame(game, config, now)
			if !notify || !strings.Contains(phrase, "76ers") || !strings.Contains(phrase, "Nuggets") || strings.Contains(phrase, "Philadelphia") || strings.Contains(phrase, "Denver") {
				t.Fatalf("%s notification did not use short names: %q", state, phrase)
			}
		})
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

func TestNBACompletedGamesAreNotReplayed(t *testing.T) {
	now := time.Date(2026, time.January, 2, 23, 0, 0, 0, time.UTC)
	config := vars.NBAConfig{NotifyFinal: true}
	for restart := 0; restart < 2; restart++ {
		resetNBANotificationState()
		for poll := 0; poll < 2; poll++ {
			for _, id := range []string{"old-game-1", "old-game-2"} {
				game := testNBAGame(id, "post", now.Add(-6*time.Hour), "104", "111")
				if kind, _, notify := nbaNotificationForGame(game, config, now.Add(time.Duration(poll)*time.Minute)); notify {
					t.Fatalf("restart %d, poll %d: completed game %s produced %q notification", restart, poll, id, kind)
				}
			}
		}
	}
}

func TestNBAFinalNotificationAfterPregameWithoutReminder(t *testing.T) {
	resetNBANotificationState()
	now := time.Date(2026, time.January, 2, 1, 0, 0, 0, time.UTC)
	game := testNBAGame("upcoming", "pre", now.Add(time.Hour), "0", "0")
	config := vars.NBAConfig{PregameMinutes: 15, NotifyFinal: true}
	if _, _, notify := nbaNotificationForGame(game, config, now); notify {
		t.Fatal("pregame reminder was announced too early")
	}
	game.Status.Type.State = "post"
	if kind, _, notify := nbaNotificationForGame(game, config, now.Add(4*time.Hour)); !notify || kind != "final" {
		t.Fatalf("final notification = (%q, %v), want final for a game observed before completion", kind, notify)
	}
}

func TestNBAFinalNotificationAndFaceRender(t *testing.T) {
	resetNBANotificationState()
	now := time.Date(2026, time.January, 2, 1, 0, 0, 0, time.UTC)
	game := testNBAGame("final", "in", now, "104", "111")
	config := vars.NBAConfig{NotifyFinal: true}
	if kind, _, notify := nbaNotificationForGame(game, config, now); !notify || kind != "live" {
		t.Fatalf("live notification = (%q, %v), want live for a game in progress at startup", kind, notify)
	}
	game.Status.Type.State = "post"

	kind, phrase, notify := nbaNotificationForGame(game, config, now)
	if !notify || kind != "final" || !strings.Contains(phrase, "one oh four") || !strings.Contains(phrase, "one eleven") {
		t.Fatalf("final notification = (%q, %q, %v)", kind, phrase, notify)
	}
	markNBANotified(game.ID, kind, now)
	if _, _, notify := nbaNotificationForGame(game, config, now.Add(time.Minute)); notify {
		t.Fatal("final notification repeated")
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
