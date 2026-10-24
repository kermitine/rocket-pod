package productivity

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"sort"
	"strings"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

const (
	NBAGamesTodayIntent = "intent_sports_nba_games_today"
	nbaGamesTodaySource = "nba_schedule"
)

func MatchNBAGamesTodayVoiceCommand(text string) bool {
	text = normalizeStandingsVoiceText(text)
	if !containsAnyWord(text, "nba", "basketball") || hasStandingsCue(text) {
		return false
	}
	// Leave requests for other dates to the normal intent/knowledge handlers.
	if containsAnyWord(text, "tomorrow", "yesterday", "week", "weekend") || containsAny(text, "next ", "last ") {
		return false
	}
	return (containsAnyWord(text, "today", "tonight") || strings.Contains(text, "this evening")) &&
		containsAnyWord(text, "game", "games", "schedule", "matchup", "matchups", "playing", "plays")
}

func QueueVoiceNBAGamesToday(robotESN string) error {
	if strings.TrimSpace(robotESN) == "" {
		return fmt.Errorf("voice command has no robot serial")
	}
	generation := currentConfigurationGeneration()
	favorites := NormalizeNBATeams(vars.APIConfig.Productivity.NBA.FavoriteTeams)
	now := timeInProductivityTimezone(time.Now(), vars.APIConfig.Productivity.Timezone)
	expiresAt := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	select {
	case taskQueue <- Task{
		ID: fmt.Sprintf("nba_schedule_ack_%d", time.Now().UnixNano()), RobotESN: robotESN,
		Source: nbaGamesTodaySource, AcknowledgementOnly: true, ExpiresAt: expiresAt,
		AcknowledgementAnimation: standingsAcknowledgementAnimation, configurationGeneration: generation,
	}:
	default:
	}
	go func() {
		endInteraction := BeginBedtimeInteraction(robotESN)
		defer endInteraction()
		ctx, cancel := context.WithTimeout(context.Background(), standingsRequestTimeout)
		defer cancel()
		pages, err := fetchNBAGamesTodayPages(ctx, now, favorites)
		if err != nil {
			logger.Println("Productivity: NBA schedule request failed: " + err.Error())
			pages = nbaScheduleMessagePages("UNAVAILABLE", "Sorry, I couldn't get today's NBA games. Please try again.")
		}
		select {
		case taskQueue <- Task{
			ID: fmt.Sprintf("nba_schedule_%d", time.Now().UnixNano()), RobotESN: robotESN,
			Pages: pages, Source: nbaGamesTodaySource, ExpiresAt: expiresAt, configurationGeneration: generation,
		}:
		default:
			logger.Println("Productivity: Queue full, skipping requested NBA schedule")
		}
	}()
	return nil
}

func fetchNBAGamesTodayPages(ctx context.Context, now time.Time, favoriteTeams []string) ([]TaskPage, error) {
	favorites := make(map[string]bool)
	for _, team := range NormalizeNBATeams(favoriteTeams) {
		favorites[team] = true
	}
	if len(favorites) == 0 {
		return nbaScheduleMessagePages("CHOOSE TEAMS", "Choose your favorite NBA teams in the sports settings first, and I'll tell you their games today."), nil
	}
	// ESPN's scoreboard dates use Eastern time. Fetch every Eastern date that
	// overlaps the user's calendar day, then filter by the user's timezone.
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := start.AddDate(0, 0, 1)
	logger.Println(fmt.Sprintf("NBA schedule: checking %s in %s for %s", start.Format("2006-01-02"), now.Location(), strings.Join(NormalizeNBATeams(favoriteTeams), ", ")))
	eastern, err := time.LoadLocation("America/New_York")
	if err != nil {
		return nil, err
	}
	first, last := start.In(eastern), end.Add(-time.Nanosecond).In(eastern)
	date := time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, eastern)
	var games []nbaEvent
	invalidDates := false
	seen := make(map[string]bool)
	for ; date.Format("20060102") <= last.Format("20060102"); date = date.AddDate(0, 0, 1) {
		scoreboard, err := fetchNBAScoreboardDate(ctx, date.Format("20060102"))
		if err != nil {
			return nil, err
		}
		for _, game := range scoreboard.Events {
			if !gameIncludesFavorite(game, favorites) || (game.ID != "" && seen[game.ID]) {
				continue
			}
			tipoff, err := parseNBAGameTime(game.Date)
			if err != nil {
				invalidDates = true
				continue
			}
			if tipoff.Before(start) || !tipoff.Before(end) {
				continue
			}
			seen[game.ID] = true
			games = append(games, game)
		}
	}
	if len(games) == 0 {
		if invalidDates {
			return nil, fmt.Errorf("NBA schedule returned unreadable start times for favorite teams")
		}
		return nbaScheduleMessagePages("NO GAMES TODAY", "None of your favorite NBA teams have a game today."), nil
	}
	sort.SliceStable(games, func(i, j int) bool {
		left, _ := parseNBAGameTime(games[i].Date)
		right, _ := parseNBAGameTime(games[j].Date)
		return left.Before(right)
	})
	pages := make([]TaskPage, 0, len(games))
	for index, game := range games {
		away, home, _ := nbaGameTeams(game)
		tipoff, _ := parseNBAGameTime(game.Date)
		clock := tipoff.In(now.Location()).Format("3:04 PM MST")
		label, verb, scores := "TODAY", "play", false
		switch strings.ToLower(game.Status.Type.State) {
		case "in":
			label, verb, scores = "LIVE", "are playing", true
		case "post":
			label, verb, scores = "FINAL", "played", true
		}
		speech := fmt.Sprintf("The %s %s the %s. Tip off is at %s.", spokenNBATeamName(away), verb, spokenNBATeamName(home), clock)
		if scores {
			speech = fmt.Sprintf("The %s %s the %s. Tip off was at %s. %s, %s. %s, %s.", spokenNBATeamName(away), verb, spokenNBATeamName(home), clock, spokenNBATeamName(away), scoreOrZero(away.Score), spokenNBATeamName(home), scoreOrZero(home.Score))
		}
		status := strings.ToLower(game.Status.Type.Name + " " + game.Status.Type.Detail)
		switch {
		case strings.Contains(status, "postponed"):
			label, scores = "POSTPONED", false
			speech = fmt.Sprintf("The %s against the %s, originally scheduled for %s, has been postponed.", spokenNBATeamName(away), spokenNBATeamName(home), clock)
		case containsAny(status, "canceled", "cancelled"):
			label, scores = "CANCELED", false
			speech = fmt.Sprintf("The %s against the %s, originally scheduled for %s, has been canceled.", spokenNBATeamName(away), spokenNBATeamName(home), clock)
		}
		if index == 0 {
			intro := fmt.Sprintf("Your favorite teams have %d NBA games today. ", len(games))
			if len(games) == 1 {
				intro = "Your favorite teams have one NBA game today. "
			}
			speech = intro + speech
		}
		face, err := renderNBAGameFace(ctx, game, fmt.Sprintf("NBA %s %d/%d", label, index+1, len(games)), clock, scores)
		if err != nil {
			return nil, err
		}
		pages = append(pages, TaskPage{FaceData: face, Speech: speech})
	}
	return pages, nil
}

func nbaScheduleMessagePages(message, speech string) []TaskPage {
	canvas := image.NewNRGBA(image.Rect(0, 0, 184, 96))
	drawCenteredText(canvas, "NBA TODAY", 92, 25, color.RGBA{100, 220, 255, 255})
	drawCenteredText(canvas, message, 92, 58, color.White)
	return []TaskPage{{FaceData: convertImageToVectorFaceData(canvas), Speech: speech}}
}
