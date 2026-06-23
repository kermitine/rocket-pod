package productivity

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"math/rand"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

const (
	nbaScoreboardEndpoint = "https://site.api.espn.com/apis/site/v2/sports/basketball/nba/scoreboard"
	nbaSummaryEndpoint    = "https://site.api.espn.com/apis/site/v2/sports/basketball/nba/summary"
	nbaTeamScheduleURL    = "https://site.api.espn.com/apis/site/v2/sports/basketball/nba/teams/%s/schedule?season=%d&seasontype=2"
)

var nbaTeamNames = map[string]string{
	"ATL": "Atlanta Hawks", "BOS": "Boston Celtics", "BKN": "Brooklyn Nets",
	"CHA": "Charlotte Hornets", "CHI": "Chicago Bulls", "CLE": "Cleveland Cavaliers",
	"DAL": "Dallas Mavericks", "DEN": "Denver Nuggets", "DET": "Detroit Pistons",
	"GS": "Golden State Warriors", "HOU": "Houston Rockets", "IND": "Indiana Pacers",
	"LAC": "LA Clippers", "LAL": "Los Angeles Lakers", "MEM": "Memphis Grizzlies",
	"MIA": "Miami Heat", "MIL": "Milwaukee Bucks", "MIN": "Minnesota Timberwolves",
	"NO": "New Orleans Pelicans", "NY": "New York Knicks", "OKC": "Oklahoma City Thunder",
	"ORL": "Orlando Magic", "PHI": "Philadelphia 76ers", "PHX": "Phoenix Suns",
	"POR": "Portland Trail Blazers", "SA": "San Antonio Spurs", "SAC": "Sacramento Kings",
	"TOR": "Toronto Raptors", "UTAH": "Utah Jazz", "WSH": "Washington Wizards",
}

var nbaSpokenClockPattern = regexp.MustCompile(`(?i)^\s*(\d+):(\d{2})\s*-\s*(1st|2nd|3rd|4th|OT)\b`)

type nbaScoreboard struct {
	Events []nbaEvent `json:"events"`
}

type nbaSummary struct {
	Boxscore struct {
		Players []nbaBoxscoreTeam `json:"players"`
	} `json:"boxscore"`
}

type nbaBoxscoreTeam struct {
	Team struct {
		Abbreviation string `json:"abbreviation"`
		DisplayName  string `json:"displayName"`
		Logo         string `json:"logo"`
	} `json:"team"`
	Statistics []nbaPlayerStatistics `json:"statistics"`
}

type nbaPlayerStatistics struct {
	Labels   []string             `json:"labels"`
	Athletes []nbaBoxscoreAthlete `json:"athletes"`
}

type nbaBoxscoreAthlete struct {
	DidNotPlay bool `json:"didNotPlay"`
	Starter    bool `json:"starter"`
	Athlete    struct {
		DisplayName string `json:"displayName"`
		Headshot    struct {
			Href string `json:"href"`
		} `json:"headshot"`
	} `json:"athlete"`
	Stats []string `json:"stats"`
}

type nbaTopPerformer struct {
	Name     string
	Headshot string
	TeamLogo string
	Points   int
	Rebounds int
	Assists  int
}

type nbaTeamSchedule struct {
	Events []struct {
		ID           string `json:"id"`
		Date         string `json:"date"`
		Competitions []struct {
			Status nbaStatus `json:"status"`
		} `json:"competitions"`
	} `json:"events"`
}

type nbaFinalTestPerformer struct {
	TeamAbbreviation string
	Name             string
	Headshot         string
}

var nbaFinalTestFallbackPerformers = []nbaFinalTestPerformer{
	{TeamAbbreviation: "LAL", Name: "LeBron James", Headshot: "https://a.espncdn.com/i/headshots/nba/players/full/1966.png"},
	{TeamAbbreviation: "GS", Name: "Stephen Curry", Headshot: "https://a.espncdn.com/i/headshots/nba/players/full/3975.png"},
	{TeamAbbreviation: "DEN", Name: "Nikola Jokic", Headshot: "https://a.espncdn.com/i/headshots/nba/players/full/3112335.png"},
	{TeamAbbreviation: "MIL", Name: "Giannis Antetokounmpo", Headshot: "https://a.espncdn.com/i/headshots/nba/players/full/3032977.png"},
}

type nbaEvent struct {
	ID           string           `json:"id"`
	Date         string           `json:"date"`
	Status       nbaStatus        `json:"status"`
	Competitions []nbaCompetition `json:"competitions"`
}

type nbaStatus struct {
	Type struct {
		Name        string `json:"name"`
		State       string `json:"state"`
		Detail      string `json:"detail"`
		ShortDetail string `json:"shortDetail"`
	} `json:"type"`
}

type nbaCompetition struct {
	Competitors []nbaCompetitor `json:"competitors"`
}

type nbaCompetitor struct {
	HomeAway string `json:"homeAway"`
	Score    string `json:"score"`
	Team     struct {
		ID           string `json:"id"`
		Abbreviation string `json:"abbreviation"`
		DisplayName  string `json:"displayName"`
		Logo         string `json:"logo"`
	} `json:"team"`
}

type nbaLogoCache struct {
	sync.Mutex
	images map[string]image.Image
}

var (
	nbaPregameNotified = make(map[string]bool)
	nbaFinalNotified   = make(map[string]bool)
	nbaLastLiveUpdate  = make(map[string]time.Time)
	nbaLogos           = nbaLogoCache{images: make(map[string]image.Image)}
)

func NormalizeNBATeams(teams []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(teams))
	for _, team := range teams {
		team = strings.ToUpper(strings.TrimSpace(team))
		if _, valid := nbaTeamNames[team]; !valid || seen[team] {
			continue
		}
		seen[team] = true
		result = append(result, team)
	}
	sort.Strings(result)
	return result
}

// InjectTestNBAUpdate exercises the same queue, face-seeking, display, and
// speech path as a real live update without depending on an active game.
func InjectTestNBAUpdate(robotESN string) error {
	if robotESN == "" || robotESN == "None" {
		robotESN = productivityTargetRobot()
	}
	if robotESN == "" {
		return fmt.Errorf("no target robot is available")
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	game := randomNBATestGame(rng)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	faceData, err := renderNBAScoreFace(ctx, game, time.Local)
	if err != nil {
		return fmt.Errorf("render score display: %w", err)
	}
	away, home, _ := nbaGameTeams(game)
	phrase := fmt.Sprintf("NBA score update. %s, %s. %s, %s. %s.", spokenNBATeamName(away), away.Score, spokenNBATeamName(home), home.Score, spokenNBAGameDetail(game.Status.Type.ShortDetail))
	task := Task{
		ID:                      fmt.Sprintf("nba_test_%d", time.Now().UnixNano()),
		RobotESN:                robotESN,
		Phrases:                 []string{phrase},
		FaceData:                faceData,
		Source:                  "test",
		configurationGeneration: currentConfigurationGeneration(),
	}
	select {
	case taskQueue <- task:
		logger.Println("Productivity: Random NBA test update queued")
		return nil
	default:
		return fmt.Errorf("reminder queue is full")
	}
}

// InjectTestNBAFinalUpdate queues a synthetic completed game so the final
// scoreboard and top-performer card can be checked without waiting for a game.
func InjectTestNBAFinalUpdate(robotESN string) error {
	if robotESN == "" || robotESN == "None" {
		robotESN = productivityTargetRobot()
	}
	if robotESN == "" {
		return fmt.Errorf("no target robot is available")
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	lookupCtx, lookupCancel := context.WithTimeout(context.Background(), 35*time.Second)
	performerTeam, performer, err := fetchRandomNBAStarter(lookupCtx, rng, time.Now())
	lookupCancel()
	if err != nil {
		logger.Println("Productivity: Live NBA starter lookup failed; using fallback: " + err.Error())
		performerTeam, performer = fallbackNBAFinalTestStarter(rng)
	} else {
		logger.Println("Productivity: NBA final test selected starter " + performer.Name + " from " + performerTeam)
	}
	game, performer := randomNBAFinalTestGame(rng, performerTeam, performer)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	scoreFace, err := renderNBAScoreFace(ctx, game, time.Local)
	if err != nil {
		return fmt.Errorf("render final score display: %w", err)
	}
	performerFace, err := renderNBAPerformerFace(ctx, performer)
	if err != nil {
		return fmt.Errorf("render top performer display: %w", err)
	}
	away, home, _ := nbaGameTeams(game)
	phrase := fmt.Sprintf("Final NBA score. %s, %s. %s, %s.", spokenNBATeamName(away), away.Score, spokenNBATeamName(home), home.Score)
	task := Task{
		ID:                      fmt.Sprintf("nba_final_test_%d", time.Now().UnixNano()),
		RobotESN:                robotESN,
		Pages:                   nbaFinalTaskPages(scoreFace, performerFace, phrase, performer),
		Source:                  "test",
		configurationGeneration: currentConfigurationGeneration(),
	}
	select {
	case taskQueue <- task:
		logger.Println("Productivity: Random NBA final score test queued")
		return nil
	default:
		return fmt.Errorf("reminder queue is full")
	}
}

func randomNBATestGame(rng *rand.Rand) nbaEvent {
	teams := make([]string, 0, len(nbaTeamNames))
	for abbreviation := range nbaTeamNames {
		teams = append(teams, abbreviation)
	}
	sort.Strings(teams)
	awayIndex := rng.Intn(len(teams))
	homeIndex := rng.Intn(len(teams) - 1)
	if homeIndex >= awayIndex {
		homeIndex++
	}
	awayAbbreviation := teams[awayIndex]
	homeAbbreviation := teams[homeIndex]
	quarter := 1 + rng.Intn(4)
	minutes := rng.Intn(12)
	seconds := rng.Intn(60)
	awayScore := 18*quarter + rng.Intn(18*quarter+1)
	homeScore := 18*quarter + rng.Intn(18*quarter+1)

	game := nbaEvent{ID: fmt.Sprintf("test-%d", time.Now().UnixNano()), Date: time.Now().Format(time.RFC3339)}
	game.Status.Type.State = "in"
	game.Status.Type.Name = "STATUS_IN_PROGRESS"
	game.Status.Type.Detail = fmt.Sprintf("%d:%02d - %s", minutes, seconds, ordinalQuarter(quarter))
	game.Status.Type.ShortDetail = game.Status.Type.Detail
	competition := nbaCompetition{Competitors: make([]nbaCompetitor, 2)}
	setNBATestCompetitor(&competition.Competitors[0], "away", awayAbbreviation, awayScore)
	setNBATestCompetitor(&competition.Competitors[1], "home", homeAbbreviation, homeScore)
	game.Competitions = []nbaCompetition{competition}
	return game
}

func randomNBAFinalTestGame(rng *rand.Rand, performerTeam string, performer nbaTopPerformer) (nbaEvent, nbaTopPerformer) {
	opponents := make([]string, 0, len(nbaTeamNames)-1)
	for abbreviation := range nbaTeamNames {
		if abbreviation != performerTeam {
			opponents = append(opponents, abbreviation)
		}
	}
	sort.Strings(opponents)
	opponent := opponents[rng.Intn(len(opponents))]
	performerIsAway := rng.Intn(2) == 0
	awayAbbreviation, homeAbbreviation := opponent, performerTeam
	if performerIsAway {
		awayAbbreviation, homeAbbreviation = performerTeam, opponent
	}
	awayScore := 95 + rng.Intn(36)
	homeScore := 95 + rng.Intn(36)
	if awayScore == homeScore {
		homeScore++
	}

	game := nbaEvent{ID: fmt.Sprintf("final-test-%d", time.Now().UnixNano()), Date: time.Now().Format(time.RFC3339)}
	game.Status.Type.State = "post"
	game.Status.Type.Name = "STATUS_FINAL"
	game.Status.Type.Detail = "Final"
	game.Status.Type.ShortDetail = "Final"
	competition := nbaCompetition{Competitors: make([]nbaCompetitor, 2)}
	setNBATestCompetitor(&competition.Competitors[0], "away", awayAbbreviation, awayScore)
	setNBATestCompetitor(&competition.Competitors[1], "home", homeAbbreviation, homeScore)
	game.Competitions = []nbaCompetition{competition}

	teamLogo := competition.Competitors[1].Team.Logo
	if performerIsAway {
		teamLogo = competition.Competitors[0].Team.Logo
	}
	performer.TeamLogo = teamLogo
	return game, performer
}

func fallbackNBAFinalTestStarter(rng *rand.Rand) (string, nbaTopPerformer) {
	fixture := nbaFinalTestFallbackPerformers[rng.Intn(len(nbaFinalTestFallbackPerformers))]
	return fixture.TeamAbbreviation, nbaTopPerformer{
		Name:     fixture.Name,
		Headshot: fixture.Headshot,
		Points:   24 + rng.Intn(18),
		Rebounds: 5 + rng.Intn(11),
		Assists:  4 + rng.Intn(10),
	}
}

func setNBATestCompetitor(competitor *nbaCompetitor, homeAway, abbreviation string, score int) {
	competitor.HomeAway = homeAway
	competitor.Score = fmt.Sprint(score)
	competitor.Team.Abbreviation = abbreviation
	competitor.Team.DisplayName = nbaTeamNames[abbreviation]
	competitor.Team.Logo = fmt.Sprintf("https://a.espncdn.com/i/teamlogos/nba/500/scoreboard/%s.png", strings.ToLower(abbreviation))
}

func spokenNBATeamName(competitor nbaCompetitor) string {
	return spokenNBATeamDisplayName(competitor.Team.DisplayName, competitor.Team.Abbreviation)
}

func spokenNBATeamDisplayName(displayName, abbreviation string) string {
	// Vector's TTS expands "LA" as Louisiana, so use the unabbreviated city
	// for the Clippers regardless of the display name returned by ESPN.
	if strings.EqualFold(abbreviation, "LAC") || strings.EqualFold(displayName, "LA Clippers") {
		return "Los Angeles Clippers"
	}
	return displayName
}

func ordinalQuarter(quarter int) string {
	switch quarter {
	case 1:
		return "1st"
	case 2:
		return "2nd"
	case 3:
		return "3rd"
	default:
		return "4th"
	}
}

func resetNBANotificationState() {
	nbaPregameNotified = make(map[string]bool)
	nbaFinalNotified = make(map[string]bool)
	nbaLastLiveUpdate = make(map[string]time.Time)
}

func checkNBAGames() {
	config := vars.APIConfig.Productivity.NBA
	if !config.Enable || len(config.FavoriteTeams) == 0 {
		return
	}
	generation := currentConfigurationGeneration()
	now := timeInProductivityTimezone(time.Now(), vars.APIConfig.Productivity.Timezone)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	scoreboard, err := fetchNBAScoreboard(ctx, now)
	if err != nil {
		logger.Println("Productivity: NBA scoreboard request failed: " + err.Error())
		return
	}
	targetRobot := productivityTargetRobot()
	if targetRobot == "" {
		return
	}
	favorites := make(map[string]bool)
	for _, team := range NormalizeNBATeams(config.FavoriteTeams) {
		favorites[team] = true
	}

	for _, game := range scoreboard.Events {
		if generation != currentConfigurationGeneration() || !gameIncludesFavorite(game, favorites) {
			continue
		}
		kind, phrase, shouldNotify := nbaNotificationForGame(game, config, now)
		if !shouldNotify {
			continue
		}
		faceData, renderErr := renderNBAScoreFace(ctx, game, now.Location())
		if renderErr != nil {
			logger.Println("Productivity: NBA score image failed: " + renderErr.Error())
		}
		var additionalFaceData [][]byte
		var performer nbaTopPerformer
		if kind == "final" {
			var performerErr error
			performer, performerErr = fetchNBATopPerformer(ctx, game.ID)
			if performerErr != nil {
				logger.Println("Productivity: NBA top performer unavailable for game " + game.ID + ": " + performerErr.Error())
			} else if performerFace, performerRenderErr := renderNBAPerformerFace(ctx, performer); performerRenderErr != nil {
				logger.Println("Productivity: NBA top performer image failed: " + performerRenderErr.Error())
			} else {
				additionalFaceData = append(additionalFaceData, performerFace)
			}
		}
		var pages []TaskPage
		if kind == "final" && len(additionalFaceData) > 0 {
			pages = nbaFinalTaskPages(faceData, additionalFaceData[0], phrase, performer)
		}
		task := Task{
			ID:                      "nba_" + game.ID + "_" + kind,
			RobotESN:                targetRobot,
			Phrases:                 []string{phrase},
			FaceData:                faceData,
			AdditionalFaceData:      additionalFaceData,
			Pages:                   pages,
			Source:                  "nba",
			configurationGeneration: generation,
		}
		select {
		case taskQueue <- task:
			markNBANotified(game.ID, kind, now)
			logger.Println("Productivity: Queued NBA " + kind + " update for game " + game.ID)
		default:
			logger.Println("Productivity: Queue full, skipping NBA update for game " + game.ID)
		}
	}
}

func nbaFinalTaskPages(scoreFace, performerFace []byte, finalSpeech string, performer nbaTopPerformer) []TaskPage {
	return []TaskPage{
		{FaceData: scoreFace, Speech: finalSpeech},
		{FaceData: performerFace, Speech: spokenNBAPerformer(performer)},
	}
}

func spokenNBAPerformer(performer nbaTopPerformer) string {
	return fmt.Sprintf(
		"Top performer, %s, with %d %s, %d %s, and %d %s.",
		performer.Name,
		performer.Points, pluralize(performer.Points, "point", "points"),
		performer.Rebounds, pluralize(performer.Rebounds, "rebound", "rebounds"),
		performer.Assists, pluralize(performer.Assists, "assist", "assists"),
	)
}

func fetchNBATopPerformer(ctx context.Context, gameID string) (nbaTopPerformer, error) {
	summary, err := fetchNBASummary(ctx, gameID)
	if err != nil {
		return nbaTopPerformer{}, err
	}
	return selectNBATopPerformer(summary)
}

func fetchNBASummary(ctx context.Context, gameID string) (nbaSummary, error) {
	url := fmt.Sprintf("%s?event=%s", nbaSummaryEndpoint, gameID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nbaSummary{}, err
	}
	resp, err := externalApiClient.Do(req)
	if err != nil {
		return nbaSummary{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nbaSummary{}, fmt.Errorf("game summary returned HTTP %d", resp.StatusCode)
	}
	var summary nbaSummary
	if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
		return nbaSummary{}, err
	}
	return summary, nil
}

func fetchRandomNBAStarter(ctx context.Context, rng *rand.Rand, now time.Time) (string, nbaTopPerformer, error) {
	teams := make([]string, 0, len(nbaTeamNames))
	for abbreviation := range nbaTeamNames {
		teams = append(teams, abbreviation)
	}
	rng.Shuffle(len(teams), func(i, j int) { teams[i], teams[j] = teams[j], teams[i] })
	var lastErr error
	for _, team := range teams {
		gameID, err := fetchLatestCompletedNBAGame(ctx, team, now)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				break
			}
			continue
		}
		summary, err := fetchNBASummary(ctx, gameID)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				break
			}
			continue
		}
		performer, err := selectRandomNBAStarter(summary, team, rng)
		if err == nil {
			return team, performer, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no NBA teams are available")
	}
	return "", nbaTopPerformer{}, lastErr
}

func fetchLatestCompletedNBAGame(ctx context.Context, team string, now time.Time) (string, error) {
	season := nbaSeasonYear(now)
	for _, year := range []int{season, season - 1} {
		url := fmt.Sprintf(nbaTeamScheduleURL, strings.ToLower(team), year)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		resp, err := externalApiClient.Do(req)
		if err != nil {
			return "", err
		}
		var schedule nbaTeamSchedule
		decodeErr := json.NewDecoder(resp.Body).Decode(&schedule)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("team schedule returned HTTP %d", resp.StatusCode)
		}
		if decodeErr != nil {
			return "", decodeErr
		}
		latestID, latestDate := "", time.Time{}
		for _, event := range schedule.Events {
			if len(event.Competitions) == 0 || !strings.EqualFold(event.Competitions[0].Status.Type.State, "post") {
				continue
			}
			date, err := time.Parse(time.RFC3339, event.Date)
			if err == nil && !date.After(now) && (latestID == "" || date.After(latestDate)) {
				latestID, latestDate = event.ID, date
			}
		}
		if latestID != "" {
			return latestID, nil
		}
	}
	return "", fmt.Errorf("no completed game found for %s", team)
}

func nbaSeasonYear(now time.Time) int {
	year := now.Year()
	if now.Month() >= time.October {
		year++
	}
	return year
}

func selectRandomNBAStarter(summary nbaSummary, teamAbbreviation string, rng *rand.Rand) (nbaTopPerformer, error) {
	starters := make([]nbaTopPerformer, 0, 5)
	for _, team := range summary.Boxscore.Players {
		if !strings.EqualFold(team.Team.Abbreviation, teamAbbreviation) {
			continue
		}
		for _, table := range team.Statistics {
			pointsIndex := nbaStatIndex(table.Labels, "PTS")
			reboundsIndex := nbaStatIndex(table.Labels, "REB")
			assistsIndex := nbaStatIndex(table.Labels, "AST")
			if pointsIndex < 0 || reboundsIndex < 0 || assistsIndex < 0 {
				continue
			}
			for _, player := range table.Athletes {
				if !player.Starter || player.DidNotPlay || player.Athlete.DisplayName == "" || pointsIndex >= len(player.Stats) || reboundsIndex >= len(player.Stats) || assistsIndex >= len(player.Stats) {
					continue
				}
				starters = append(starters, nbaTopPerformer{
					Name:     player.Athlete.DisplayName,
					Headshot: player.Athlete.Headshot.Href,
					TeamLogo: team.Team.Logo,
					Points:   parseNBAStat(player.Stats[pointsIndex]),
					Rebounds: parseNBAStat(player.Stats[reboundsIndex]),
					Assists:  parseNBAStat(player.Stats[assistsIndex]),
				})
			}
		}
	}
	if len(starters) == 0 {
		return nbaTopPerformer{}, fmt.Errorf("box score has no starters for %s", teamAbbreviation)
	}
	return starters[rng.Intn(len(starters))], nil
}

func selectNBATopPerformer(summary nbaSummary) (nbaTopPerformer, error) {
	var best nbaTopPerformer
	bestProduction := -1
	for _, team := range summary.Boxscore.Players {
		for _, table := range team.Statistics {
			pointsIndex := nbaStatIndex(table.Labels, "PTS")
			reboundsIndex := nbaStatIndex(table.Labels, "REB")
			assistsIndex := nbaStatIndex(table.Labels, "AST")
			if pointsIndex < 0 || reboundsIndex < 0 || assistsIndex < 0 {
				continue
			}
			for _, player := range table.Athletes {
				if player.DidNotPlay || player.Athlete.DisplayName == "" || pointsIndex >= len(player.Stats) || reboundsIndex >= len(player.Stats) || assistsIndex >= len(player.Stats) {
					continue
				}
				candidate := nbaTopPerformer{
					Name:     player.Athlete.DisplayName,
					Headshot: player.Athlete.Headshot.Href,
					TeamLogo: team.Team.Logo,
					Points:   parseNBAStat(player.Stats[pointsIndex]),
					Rebounds: parseNBAStat(player.Stats[reboundsIndex]),
					Assists:  parseNBAStat(player.Stats[assistsIndex]),
				}
				production := candidate.Points + candidate.Rebounds + candidate.Assists
				if production > bestProduction || (production == bestProduction && candidate.Points > best.Points) {
					best, bestProduction = candidate, production
				}
			}
		}
	}
	if bestProduction < 0 {
		return nbaTopPerformer{}, fmt.Errorf("game summary has no player box score")
	}
	return best, nil
}

func nbaStatIndex(labels []string, wanted string) int {
	for index, label := range labels {
		if strings.EqualFold(strings.TrimSpace(label), wanted) {
			return index
		}
	}
	return -1
}

func parseNBAStat(value string) int {
	parsed, _ := strconv.Atoi(strings.TrimSpace(value))
	return parsed
}

func fetchNBAScoreboard(ctx context.Context, now time.Time) (*nbaScoreboard, error) {
	eastern, err := time.LoadLocation("America/New_York")
	if err != nil {
		eastern = now.Location()
	}
	easternNow := now.In(eastern)
	dates := []string{easternNow.Format("20060102")}
	if easternNow.Hour() < 4 {
		dates = append([]string{easternNow.AddDate(0, 0, -1).Format("20060102")}, dates...)
	}
	combined := &nbaScoreboard{}
	seen := make(map[string]bool)
	for _, date := range dates {
		scoreboard, err := fetchNBAScoreboardDate(ctx, date)
		if err != nil {
			return nil, err
		}
		for _, event := range scoreboard.Events {
			if !seen[event.ID] {
				seen[event.ID] = true
				combined.Events = append(combined.Events, event)
			}
		}
	}
	return combined, nil
}

func fetchNBAScoreboardDate(ctx context.Context, date string) (*nbaScoreboard, error) {
	url := fmt.Sprintf("%s?dates=%s&limit=100", nbaScoreboardEndpoint, date)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := externalApiClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("scoreboard returned HTTP %d", resp.StatusCode)
	}
	var scoreboard nbaScoreboard
	if err := json.NewDecoder(resp.Body).Decode(&scoreboard); err != nil {
		return nil, err
	}
	return &scoreboard, nil
}

func productivityTargetRobot() string {
	target := vars.APIConfig.Productivity.TargetRobot
	if target != "" && target != "None" {
		return target
	}
	if len(vars.BotInfo.Robots) > 0 {
		return vars.BotInfo.Robots[0].Esn
	}
	return ""
}

func gameIncludesFavorite(game nbaEvent, favorites map[string]bool) bool {
	away, home, ok := nbaGameTeams(game)
	return ok && (favorites[strings.ToUpper(away.Team.Abbreviation)] || favorites[strings.ToUpper(home.Team.Abbreviation)])
}

func nbaNotificationForGame(game nbaEvent, config vars.NBAConfig, now time.Time) (string, string, bool) {
	away, home, ok := nbaGameTeams(game)
	if !ok {
		return "", "", false
	}
	state := strings.ToLower(game.Status.Type.State)
	switch state {
	case "pre":
		start, err := time.Parse(time.RFC3339, game.Date)
		if err != nil {
			return "", "", false
		}
		until := start.Sub(now)
		lead := time.Duration(config.PregameMinutes) * time.Minute
		if until > 0 && until <= lead && !nbaPregameNotified[game.ID] {
			phrase := fmt.Sprintf("NBA game reminder. The %s play the %s in about %d minutes.", spokenNBATeamName(away), spokenNBATeamName(home), maxInt(1, int(until.Round(time.Minute)/time.Minute)))
			return "pregame", phrase, true
		}
	case "in":
		interval := time.Duration(config.LiveUpdateMinutes) * time.Minute
		if interval <= 0 {
			interval = 5 * time.Minute
		}
		if last := nbaLastLiveUpdate[game.ID]; last.IsZero() || now.Sub(last) >= interval {
			detail := game.Status.Type.ShortDetail
			if detail == "" {
				detail = game.Status.Type.Detail
			}
			phrase := fmt.Sprintf("NBA score update. %s, %s. %s, %s. %s.", spokenNBATeamName(away), scoreOrZero(away.Score), spokenNBATeamName(home), scoreOrZero(home.Score), spokenNBAGameDetail(detail))
			return "live", phrase, true
		}
	case "post":
		if config.NotifyFinal && !nbaFinalNotified[game.ID] {
			phrase := fmt.Sprintf("Final NBA score. %s, %s. %s, %s.", spokenNBATeamName(away), scoreOrZero(away.Score), spokenNBATeamName(home), scoreOrZero(home.Score))
			return "final", phrase, true
		}
	}
	return "", "", false
}

func markNBANotified(gameID, kind string, now time.Time) {
	switch kind {
	case "pregame":
		nbaPregameNotified[gameID] = true
	case "live":
		nbaLastLiveUpdate[gameID] = now
	case "final":
		nbaFinalNotified[gameID] = true
	}
}

func nbaGameTeams(game nbaEvent) (nbaCompetitor, nbaCompetitor, bool) {
	if len(game.Competitions) == 0 {
		return nbaCompetitor{}, nbaCompetitor{}, false
	}
	var away, home nbaCompetitor
	var foundAway, foundHome bool
	for _, competitor := range game.Competitions[0].Competitors {
		switch strings.ToLower(competitor.HomeAway) {
		case "away":
			away, foundAway = competitor, true
		case "home":
			home, foundHome = competitor, true
		}
	}
	return away, home, foundAway && foundHome
}

func renderNBAScoreFace(ctx context.Context, game nbaEvent, location *time.Location) ([]byte, error) {
	away, home, ok := nbaGameTeams(game)
	if !ok {
		return nil, fmt.Errorf("game has no home/away competitors")
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, 184, 96))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)

	awayLogo, _ := loadNBALogo(ctx, away.Team.Logo)
	homeLogo, _ := loadNBALogo(ctx, home.Team.Logo)
	if awayLogo != nil {
		xdraw.CatmullRom.Scale(canvas, image.Rect(5, 15, 49, 59), awayLogo, awayLogo.Bounds(), draw.Over, nil)
	}
	if homeLogo != nil {
		xdraw.CatmullRom.Scale(canvas, image.Rect(135, 15, 179, 59), homeLogo, homeLogo.Bounds(), draw.Over, nil)
	}

	drawCenteredText(canvas, strings.ToUpper(away.Team.Abbreviation), 27, 75, color.White)
	drawCenteredText(canvas, strings.ToUpper(home.Team.Abbreviation), 157, 75, color.White)
	drawCenteredText(canvas, scoreOrZero(away.Score), 72, 50, color.White)
	drawCenteredText(canvas, "-", 92, 50, color.RGBA{180, 180, 180, 255})
	drawCenteredText(canvas, scoreOrZero(home.Score), 112, 50, color.White)
	drawCenteredText(canvas, nbaFaceStatus(game, location), 92, 88, color.RGBA{100, 220, 255, 255})
	return convertImageToVectorFaceData(canvas), nil
}

func renderNBAPerformerFace(ctx context.Context, performer nbaTopPerformer) ([]byte, error) {
	if strings.TrimSpace(performer.Name) == "" {
		return nil, fmt.Errorf("top performer name is empty")
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, 184, 96))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)

	portrait, _ := loadNBALogo(ctx, performer.Headshot)
	if portrait != nil {
		xdraw.CatmullRom.Scale(canvas, image.Rect(0, 7, 74, 96), portrait, portrait.Bounds(), draw.Over, nil)
	}
	teamLogo, _ := loadNBALogo(ctx, performer.TeamLogo)
	if teamLogo != nil {
		xdraw.CatmullRom.Scale(canvas, image.Rect(78, 39, 112, 73), teamLogo, teamLogo.Bounds(), draw.Over, nil)
	}

	drawPlayerName(canvas, performer.Name)
	statColor := color.RGBA{100, 220, 255, 255}
	drawCenteredText(canvas, fmt.Sprintf("%d PTS", performer.Points), 148, 48, statColor)
	drawCenteredText(canvas, fmt.Sprintf("%d REB", performer.Rebounds), 148, 66, color.White)
	drawCenteredText(canvas, fmt.Sprintf("%d AST", performer.Assists), 148, 84, color.White)
	return convertImageToVectorFaceData(canvas), nil
}

func drawPlayerName(dst draw.Image, name string) {
	parts := strings.Fields(name)
	if len(parts) <= 1 {
		drawCenteredText(dst, truncateNBAFaceText(name, 15), 128, 23, color.White)
		return
	}
	drawCenteredText(dst, truncateNBAFaceText(parts[0], 15), 128, 14, color.White)
	drawCenteredText(dst, truncateNBAFaceText(strings.Join(parts[1:], " "), 15), 128, 29, color.White)
}

func truncateNBAFaceText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func loadNBALogo(ctx context.Context, url string) (image.Image, error) {
	if url == "" {
		return nil, fmt.Errorf("team logo URL is empty")
	}
	nbaLogos.Lock()
	if cached := nbaLogos.images[url]; cached != nil {
		nbaLogos.Unlock()
		return cached, nil
	}
	nbaLogos.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := externalApiClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("logo returned HTTP %d", resp.StatusCode)
	}
	logo, _, err := image.Decode(resp.Body)
	if err != nil {
		return nil, err
	}
	nbaLogos.Lock()
	nbaLogos.images[url] = logo
	nbaLogos.Unlock()
	return logo, nil
}

func drawCenteredText(dst draw.Image, text string, centerX, baselineY int, textColor color.Color) {
	if len(text) > 18 {
		text = text[:18]
	}
	width := font.MeasureString(basicfont.Face7x13, text).Ceil()
	drawer := font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(textColor),
		Face: basicfont.Face7x13,
		Dot:  fixed.P(centerX-width/2, baselineY),
	}
	drawer.DrawString(text)
}

func nbaFaceStatus(game nbaEvent, location *time.Location) string {
	switch strings.ToLower(game.Status.Type.State) {
	case "pre":
		if start, err := time.Parse(time.RFC3339, game.Date); err == nil {
			return start.In(location).Format("3:04 PM")
		}
	case "post":
		return "FINAL"
	}
	if detail := game.Status.Type.ShortDetail; detail != "" {
		return strings.ToUpper(detail)
	}
	return strings.ToUpper(game.Status.Type.Detail)
}

func scoreOrZero(score string) string {
	if strings.TrimSpace(score) == "" {
		return "0"
	}
	return score
}

func spokenNBAGameDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	match := nbaSpokenClockPattern.FindStringSubmatch(detail)
	if len(match) != 4 {
		return strings.ReplaceAll(detail, " - ", ", ")
	}
	minutes, _ := strconv.Atoi(match[1])
	seconds, _ := strconv.Atoi(match[2])
	timeParts := make([]string, 0, 2)
	if minutes > 0 {
		timeParts = append(timeParts, fmt.Sprintf("%d %s", minutes, pluralize(minutes, "minute", "minutes")))
	}
	if seconds > 0 {
		timeParts = append(timeParts, fmt.Sprintf("%d %s", seconds, pluralize(seconds, "second", "seconds")))
	}
	if len(timeParts) == 0 {
		timeParts = append(timeParts, "no time")
	}
	period := map[string]string{
		"1st": "the first quarter",
		"2nd": "the second quarter",
		"3rd": "the third quarter",
		"4th": "the fourth quarter",
		"ot":  "overtime",
	}[strings.ToLower(match[3])]
	return strings.Join(timeParts, " ") + " left in " + period
}

func pluralize(value int, singular, plural string) string {
	if value == 1 {
		return singular
	}
	return plural
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
