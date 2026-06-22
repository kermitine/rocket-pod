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
