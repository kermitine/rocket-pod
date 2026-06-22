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
	"sort"
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

const nbaScoreboardEndpoint = "https://site.api.espn.com/apis/site/v2/sports/basketball/nba/scoreboard"

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

type nbaScoreboard struct {
	Events []nbaEvent `json:"events"`
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
	phrase := fmt.Sprintf("NBA score update. %s, %s. %s, %s. %s.", away.Team.DisplayName, away.Score, home.Team.DisplayName, home.Score, game.Status.Type.ShortDetail)
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

func setNBATestCompetitor(competitor *nbaCompetitor, homeAway, abbreviation string, score int) {
	competitor.HomeAway = homeAway
	competitor.Score = fmt.Sprint(score)
	competitor.Team.Abbreviation = abbreviation
	competitor.Team.DisplayName = nbaTeamNames[abbreviation]
	competitor.Team.Logo = fmt.Sprintf("https://a.espncdn.com/i/teamlogos/nba/500/scoreboard/%s.png", strings.ToLower(abbreviation))
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
		task := Task{
			ID:                      "nba_" + game.ID + "_" + kind,
			RobotESN:                targetRobot,
			Phrases:                 []string{phrase},
			FaceData:                faceData,
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
			phrase := fmt.Sprintf("NBA game reminder. The %s play the %s in about %d minutes.", away.Team.DisplayName, home.Team.DisplayName, maxInt(1, int(until.Round(time.Minute)/time.Minute)))
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
			phrase := fmt.Sprintf("NBA score update. %s, %s. %s, %s. %s.", away.Team.DisplayName, scoreOrZero(away.Score), home.Team.DisplayName, scoreOrZero(home.Score), detail)
			return "live", phrase, true
		}
	case "post":
		if config.NotifyFinal && !nbaFinalNotified[game.ID] {
			phrase := fmt.Sprintf("Final NBA score. %s, %s. %s, %s.", away.Team.DisplayName, scoreOrZero(away.Score), home.Team.DisplayName, scoreOrZero(home.Score))
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
	return convertImageToVectorRGB565(canvas), nil
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

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
