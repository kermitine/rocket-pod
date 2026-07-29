package productivity

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

const f1ScoreboardEndpoint = "https://site.api.espn.com/apis/site/v2/sports/racing/f1/scoreboard?limit=100"

type f1Scoreboard struct {
	Events []f1Event `json:"events"`
}

type f1Event struct {
	ID        string `json:"id"`
	Date      string `json:"date"`
	Name      string `json:"name"`
	ShortName string `json:"shortName"`
	Circuit   struct {
		FullName string `json:"fullName"`
		Address  struct {
			City    string `json:"city"`
			Country string `json:"country"`
		} `json:"address"`
	} `json:"circuit"`
	Status       f1Status        `json:"status"`
	Competitions []f1Competition `json:"competitions"`
}

type f1Status struct {
	Period int `json:"period"`
	Type   struct {
		Name        string `json:"name"`
		State       string `json:"state"`
		Detail      string `json:"detail"`
		ShortDetail string `json:"shortDetail"`
		Completed   bool   `json:"completed"`
	} `json:"type"`
}

type f1Competition struct {
	ID   string `json:"id"`
	Date string `json:"date"`
	Type struct {
		Abbreviation string `json:"abbreviation"`
	} `json:"type"`
	Status      f1Status       `json:"status"`
	Competitors []f1Competitor `json:"competitors"`
}

type f1Competitor struct {
	ID      string `json:"id"`
	Order   int    `json:"order"`
	Winner  bool   `json:"winner"`
	Athlete struct {
		FullName    string `json:"fullName"`
		DisplayName string `json:"displayName"`
		ShortName   string `json:"shortName"`
	} `json:"athlete"`
}

type f1Team struct {
	Name  string
	Code  string
	Color color.RGBA
}

type f1NotableSnapshot struct {
	Leader string
	Phase  string
}

var (
	f1PregameNotified  = make(map[string]bool)
	f1FinalNotified    = make(map[string]bool)
	f1LastLiveUpdate   = make(map[string]time.Time)
	f1NotableSnapshots = make(map[string]f1NotableSnapshot)
)

var f1Teams = map[string]f1Team{
	"mercedes":     {Name: "Mercedes", Code: "MER", Color: color.RGBA{0, 210, 190, 255}},
	"ferrari":      {Name: "Ferrari", Code: "FER", Color: color.RGBA{220, 0, 0, 255}},
	"mclaren":      {Name: "McLaren", Code: "MCL", Color: color.RGBA{255, 135, 0, 255}},
	"red-bull":     {Name: "Red Bull", Code: "RBR", Color: color.RGBA{50, 90, 200, 255}},
	"alpine":       {Name: "Alpine", Code: "ALP", Color: color.RGBA{255, 80, 180, 255}},
	"racing-bulls": {Name: "Racing Bulls", Code: "RB", Color: color.RGBA{102, 146, 255, 255}},
	"haas":         {Name: "Haas", Code: "HAS", Color: color.RGBA{180, 180, 180, 255}},
	"williams":     {Name: "Williams", Code: "WIL", Color: color.RGBA{30, 115, 255, 255}},
	"audi":         {Name: "Audi", Code: "AUD", Color: color.RGBA{255, 45, 0, 255}},
	"aston-martin": {Name: "Aston Martin", Code: "AMR", Color: color.RGBA{0, 111, 98, 255}},
	"cadillac":     {Name: "Cadillac", Code: "CAD", Color: color.RGBA{162, 170, 173, 255}},
}

// ESPN supplies race order but not constructor membership, so IDs from its
// current driver catalog provide the small constructor marks on Vector's face.
var f1DriverTeams = map[string]string{
	"5829": "mercedes", "5503": "mercedes",
	"868": "ferrari", "5498": "ferrari",
	"5579": "mclaren", "5752": "mclaren",
	"4665": "red-bull", "5790": "red-bull",
	"5501": "alpine", "5823": "alpine",
	"5741": "racing-bulls", "5855": "racing-bulls",
	"5789": "haas", "4678": "haas",
	"4686": "williams", "5592": "williams",
	"5835": "audi", "4396": "audi",
	"348": "aston-martin", "4775": "aston-martin",
	"4520": "cadillac", "4472": "cadillac",
}

var f1NameTeams = map[string]string{
	"antonelli": "mercedes", "russell": "mercedes", "hamilton": "ferrari", "leclerc": "ferrari",
	"norris": "mclaren", "piastri": "mclaren", "verstappen": "red-bull", "hadjar": "red-bull",
	"gasly": "alpine", "colapinto": "alpine", "lawson": "racing-bulls", "lindblad": "racing-bulls",
	"bearman": "haas", "ocon": "haas", "sainz": "williams", "albon": "williams",
	"bortoleto": "audi", "hulkenberg": "audi", "alonso": "aston-martin", "stroll": "aston-martin",
	"bottas": "cadillac", "perez": "cadillac",
}

func resetF1NotificationState() {
	f1PregameNotified = make(map[string]bool)
	f1FinalNotified = make(map[string]bool)
	f1LastLiveUpdate = make(map[string]time.Time)
	f1NotableSnapshots = make(map[string]f1NotableSnapshot)
}

func InjectTestF1Update(robotESN string) error {
	return injectTestF1Leaderboard(robotESN, "race", "live")
}

func InjectTestF1QualifyingUpdate(robotESN string) error {
	return injectTestF1Leaderboard(robotESN, "qualifying", "final")
}

func InjectTestF1LiveQualifyingUpdate(robotESN string) error {
	return injectTestF1Leaderboard(robotESN, "qualifying", "live")
}

func InjectTestF1LivePracticeUpdate(robotESN string) error {
	return injectTestF1Leaderboard(robotESN, "practice", "live")
}

func injectTestF1Leaderboard(robotESN string, sessionType, kind string) error {
	if robotESN == "" || robotESN == "None" {
		robotESN = productivityTargetRobot()
	}
	if robotESN == "" {
		return fmt.Errorf("no target robot is configured")
	}
	event, race := syntheticF1Race()
	logName := "live-race"
	switch sessionType {
	case "qualifying":
		if kind == "final" {
			event, race = syntheticF1Qualifying()
			logName = "qualifying-final"
		} else {
			event, race = syntheticF1LiveQualifying()
			logName = "live-qualifying"
		}
	case "practice":
		event, race = syntheticF1LivePractice()
		logName = "live-practice"
	}
	pages, err := f1LeaderboardTaskPages(event, race, kind)
	if err != nil {
		return err
	}
	select {
	case taskQueue <- Task{ID: fmt.Sprintf("f1_test_%s_%d", logName, time.Now().UnixNano()), RobotESN: robotESN, Pages: pages, Source: "f1", configurationGeneration: currentConfigurationGeneration()}:
		logger.Println("Productivity: F1 " + logName + " test update queued")
		return nil
	default:
		return fmt.Errorf("reminder queue is full")
	}
}

func syntheticF1Race() (f1Event, f1Competition) {
	event := f1Event{ID: "f1-test", Name: "Rocket Pod Grand Prix", ShortName: "Rocket Pod GP"}
	event.Circuit.FullName = "Vector Raceway"
	event.Circuit.Address.City = "San Francisco"
	event.Circuit.Address.Country = "United States"
	race := f1Competition{ID: "f1-test-race", Date: time.Now().Format(time.RFC3339)}
	race.Type.Abbreviation = "Race"
	race.Status.Type.State = "in"
	race.Status.Type.ShortDetail = "Lap 42 of 57"
	fixtures := []struct{ id, name string }{
		{"5579", "Lando Norris"}, {"4665", "Max Verstappen"}, {"5498", "Charles Leclerc"},
		{"5503", "George Russell"}, {"5752", "Oscar Piastri"}, {"868", "Lewis Hamilton"},
		{"4686", "Carlos Sainz"}, {"348", "Fernando Alonso"}, {"5592", "Alexander Albon"}, {"5789", "Oliver Bearman"},
	}
	for index, fixture := range fixtures {
		competitor := f1Competitor{ID: fixture.id, Order: index + 1}
		competitor.Athlete.DisplayName = fixture.name
		competitor.Athlete.FullName = fixture.name
		race.Competitors = append(race.Competitors, competitor)
	}
	return event, race
}

func syntheticF1Qualifying() (f1Event, f1Competition) {
	event, qualifying := syntheticF1LiveQualifying()
	qualifying.Status.Type.State = "post"
	qualifying.Status.Type.ShortDetail = "Final"
	return event, qualifying
}

func syntheticF1LiveQualifying() (f1Event, f1Competition) {
	event, qualifying := syntheticF1Race()
	qualifying.ID = "f1-test-qualifying"
	qualifying.Type.Abbreviation = "Qual"
	qualifying.Status.Type.ShortDetail = "Q3 - 4:12"
	return event, qualifying
}

func syntheticF1LivePractice() (f1Event, f1Competition) {
	event, practice := syntheticF1Race()
	practice.ID = "f1-test-practice"
	practice.Type.Abbreviation = "FP1"
	practice.Status.Type.ShortDetail = "Practice 1 - 12:34"
	return event, practice
}

func checkF1Races() {
	config := vars.APIConfig.Productivity.F1
	if !config.Enable {
		return
	}
	target := productivityTargetRobot()
	if target == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	scoreboard, err := fetchF1Scoreboard(ctx)
	if err != nil {
		logger.Println("Productivity: F1 scoreboard request failed: " + err.Error())
		return
	}
	now := timeInProductivityTimezone(time.Now(), vars.APIConfig.Productivity.Timezone)
	for _, event := range scoreboard.Events {
		for _, session := range f1NotificationSessions(event, config.NotifyQualifying, config.NotifyPractice) {
			notifyFinal := config.NotifyFinal
			if f1IsQualifying(session) || f1IsPractice(session) {
				notifyFinal = true
			}
			kind, notify := "", false
			if config.NotifyNotable {
				kind, notify = f1NotableMoment(session)
			}
			if !notify {
				kind, notify = f1NotificationForSession(session, config, now, notifyFinal)
			}
			if !notify {
				continue
			}
			if !f1WithinAllowedHours(now, config.AllowedStart, config.AllowedEnd) {
				if kind == "pregame" || kind == "final" {
					markF1Notified(session.ID, kind, now)
					logger.Println("Productivity: Suppressed F1 " + f1SessionName(session) + " " + kind + " update during quiet hours")
				}
				continue
			}
			var pages []TaskPage
			if kind == "pregame" {
				face := renderF1RaceFace(event, session, now.Location())
				pages = []TaskPage{{FaceData: face, Speech: f1PregameSpeech(event, session, now)}}
			} else {
				pages, err = f1LeaderboardTaskPages(event, session, kind)
				if err != nil {
					logger.Println("Productivity: F1 leaderboard unavailable: " + err.Error())
					continue
				}
			}
			select {
			case taskQueue <- Task{ID: "f1_" + session.ID + "_" + kind, RobotESN: target, Pages: pages, Source: "f1", configurationGeneration: currentConfigurationGeneration()}:
				markF1Notified(session.ID, kind, now)
				logger.Println("Productivity: Queued F1 " + f1SessionName(session) + " " + kind + " update for session " + session.ID)
			default:
				logger.Println("Productivity: Queue full, skipping F1 update for session " + session.ID)
			}
		}
	}
}

func fetchF1Scoreboard(ctx context.Context) (*f1Scoreboard, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f1ScoreboardEndpoint, nil)
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
	var scoreboard f1Scoreboard
	if err := json.NewDecoder(resp.Body).Decode(&scoreboard); err != nil {
		return nil, err
	}
	return &scoreboard, nil
}

func f1RaceCompetition(event f1Event) (f1Competition, bool) {
	for _, competition := range event.Competitions {
		if strings.EqualFold(competition.Type.Abbreviation, "Race") {
			return competition, true
		}
	}
	return f1Competition{}, false
}

func f1NotificationSessions(event f1Event, includeQualifying, includePractice bool) []f1Competition {
	sessions := make([]f1Competition, 0, 3)
	for _, competition := range event.Competitions {
		if strings.EqualFold(competition.Type.Abbreviation, "Race") ||
			(includeQualifying && f1IsQualifying(competition)) ||
			(includePractice && f1IsPractice(competition)) {
			sessions = append(sessions, competition)
		}
	}
	return sessions
}

func f1IsQualifying(session f1Competition) bool {
	return strings.Contains(strings.ToLower(session.Type.Abbreviation), "qual")
}

func f1IsPractice(session f1Competition) bool {
	abbreviation := strings.ToLower(strings.TrimSpace(session.Type.Abbreviation))
	return strings.Contains(abbreviation, "practice") ||
		strings.Contains(abbreviation, "prac") ||
		strings.HasPrefix(abbreviation, "fp") ||
		strings.HasPrefix(abbreviation, "p")
}

func f1SessionName(session f1Competition) string {
	if f1IsQualifying(session) {
		return "qualifying"
	}
	if f1IsPractice(session) {
		return "practice"
	}
	return "race"
}

func f1NotificationForRace(race f1Competition, config vars.F1Config, now time.Time) (string, bool) {
	return f1NotificationForSession(race, config, now, config.NotifyFinal)
}

func f1NotificationForSession(session f1Competition, config vars.F1Config, now time.Time, notifyFinal bool) (string, bool) {
	state := strings.ToLower(session.Status.Type.State)
	switch state {
	case "pre":
		start, err := time.Parse(time.RFC3339, session.Date)
		if err != nil {
			return "", false
		}
		until := start.Sub(now)
		lead := time.Duration(config.PregameMinutes) * time.Minute
		if until > 0 && until <= lead && !f1PregameNotified[session.ID] {
			return "pregame", true
		}
	case "in":
		interval := time.Duration(f1LiveUpdateMinutes(session, config)) * time.Minute
		if len(session.Competitors) > 0 && (f1LastLiveUpdate[session.ID].IsZero() || now.Sub(f1LastLiveUpdate[session.ID]) >= interval) {
			return "live", true
		}
	case "post":
		if notifyFinal && len(session.Competitors) > 0 && !f1FinalNotified[session.ID] {
			return "final", true
		}
	}
	return "", false
}

func f1LiveUpdateMinutes(session f1Competition, config vars.F1Config) int {
	minutes := config.LiveUpdateMinutes
	if f1IsQualifying(session) && config.QualifyingLiveUpdateMinutes > 0 {
		minutes = config.QualifyingLiveUpdateMinutes
	} else if f1IsPractice(session) && config.PracticeLiveUpdateMinutes > 0 {
		minutes = config.PracticeLiveUpdateMinutes
	}
	if minutes <= 0 {
		return 10
	}
	return minutes
}

func f1NotableMoment(session f1Competition) (string, bool) {
	if strings.ToLower(session.Status.Type.State) != "in" || len(session.Competitors) == 0 {
		return "", false
	}
	leader := ""
	if top := f1TopDrivers(session, 1); len(top) > 0 {
		leader = f1DriverSnapshotKey(top[0])
	}
	phase := f1QualifyingPhase(session)
	previous, seen := f1NotableSnapshots[session.ID]
	f1NotableSnapshots[session.ID] = f1NotableSnapshot{Leader: leader, Phase: phase}
	if !seen {
		return "", false
	}
	if previous.Leader != "" && leader != "" && previous.Leader != leader {
		return "leader", true
	}
	if previous.Phase != "" && phase != "" && previous.Phase != phase {
		return "phase", true
	}
	return "", false
}

func f1DriverSnapshotKey(driver f1Competitor) string {
	if driver.ID != "" {
		return driver.ID
	}
	name := strings.TrimSpace(driver.Athlete.DisplayName)
	if name == "" {
		name = strings.TrimSpace(driver.Athlete.FullName)
	}
	return strings.ToLower(name)
}

func f1WithinAllowedHours(now time.Time, startValue, endValue string) bool {
	startMinutes, startOK := f1ClockMinutes(startValue)
	endMinutes, endOK := f1ClockMinutes(endValue)
	if !startOK {
		startMinutes = 8 * 60
	}
	if !endOK {
		endMinutes = 22 * 60
	}
	if startMinutes == endMinutes {
		return true
	}
	currentMinutes := now.Hour()*60 + now.Minute()
	if startMinutes < endMinutes {
		return currentMinutes >= startMinutes && currentMinutes < endMinutes
	}
	return currentMinutes >= startMinutes || currentMinutes < endMinutes
}

func f1ClockMinutes(value string) (int, bool) {
	if len(value) != 5 || value[2] != ':' {
		return 0, false
	}
	hour, hourErr := strconv.Atoi(value[:2])
	minute, minuteErr := strconv.Atoi(value[3:])
	if hourErr != nil || minuteErr != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, false
	}
	return hour*60 + minute, true
}

func markF1Notified(raceID, kind string, now time.Time) {
	switch kind {
	case "pregame":
		f1PregameNotified[raceID] = true
	case "live":
		f1LastLiveUpdate[raceID] = now
	case "final":
		f1FinalNotified[raceID] = true
	}
}

func f1TopTen(race f1Competition) []f1Competitor {
	return f1TopDrivers(race, 10)
}

func f1TopDrivers(race f1Competition, limit int) []f1Competitor {
	competitors := append([]f1Competitor(nil), race.Competitors...)
	sort.SliceStable(competitors, func(i, j int) bool {
		left, right := competitors[i].Order, competitors[j].Order
		if left <= 0 {
			left = 999
		}
		if right <= 0 {
			right = 999
		}
		return left < right
	})
	if limit > 0 && len(competitors) > limit {
		competitors = competitors[:limit]
	}
	return competitors
}

func f1LeaderboardDriverLimit(race f1Competition) int {
	if f1QualifyingPhase(race) == "Q1" {
		return 15
	}
	return 10
}

func f1QualifyingPhase(race f1Competition) string {
	if !f1IsQualifying(race) {
		return ""
	}
	detail := strings.ToUpper(strings.TrimSpace(race.Status.Type.ShortDetail))
	if detail == "" {
		detail = strings.ToUpper(strings.TrimSpace(race.Status.Type.Detail))
	}
	tokens := strings.FieldsFunc(detail, func(r rune) bool {
		return (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	})
	for index, token := range tokens {
		switch token {
		case "Q1", "Q2", "Q3":
			return token
		case "Q":
			if index+1 < len(tokens) {
				switch tokens[index+1] {
				case "1":
					return "Q1"
				case "2":
					return "Q2"
				case "3":
					return "Q3"
				}
			}
		}
	}
	return ""
}

func f1LeaderboardTaskPages(event f1Event, race f1Competition, kind string) ([]TaskPage, error) {
	top := f1TopDrivers(race, f1LeaderboardDriverLimit(race))
	if len(top) == 0 {
		return nil, fmt.Errorf("%s has no classified drivers", f1SessionName(race))
	}
	pages := make([]TaskPage, 0, 2)
	for start := 0; start < len(top); start += 5 {
		end := start + 5
		if end > len(top) {
			end = len(top)
		}
		pages = append(pages, TaskPage{
			FaceData: renderF1LeaderboardFace(event, race, top[start:end], start, kind),
			Speech:   f1LeaderboardSpeech(event, race, top[start:end], start, kind),
		})
	}
	return pages, nil
}

func renderF1LeaderboardFace(event f1Event, race f1Competition, drivers []f1Competitor, offset int, kind string) []byte {
	canvas := image.NewNRGBA(image.Rect(0, 0, 184, 96))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
	drawCenteredText(canvas, f1LeaderboardHeaderText(event, race, kind), 92, 11, color.RGBA{100, 220, 255, 255})
	for index, driver := range drivers {
		position := offset + index + 1
		y := 19 + index*15
		team := f1TeamForDriver(driver)
		f1DrawText(canvas, strconv.Itoa(position), 3, y+10, color.White)
		draw.Draw(canvas, image.Rect(20, y, 27, y+11), image.NewUniform(team.Color), image.Point{}, draw.Src)
		f1DrawText(canvas, truncateF1Text(f1LastName(driver.Athlete.DisplayName), 12), 32, y+10, color.White)
		f1DrawText(canvas, team.Code, 157, y+10, team.Color)
	}
	return convertImageToVectorFaceData(canvas)
}

func f1LeaderboardHeaderText(event f1Event, race f1Competition, kind string) string {
	header := f1TrackName(event)
	if kind == "final" {
		header = strings.ToUpper(f1SessionName(race)) + " FINAL - " + header
	} else if phase := f1QualifyingPhase(race); phase != "" {
		header = phase + " - " + header
	} else if detail := f1StatusText(race); detail != "" {
		header += " - " + detail
	}
	return truncateF1Text(strings.ToUpper(header), 25)
}

func renderF1RaceFace(event f1Event, race f1Competition, location *time.Location) []byte {
	canvas := image.NewNRGBA(image.Rect(0, 0, 184, 96))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
	drawCenteredText(canvas, "FORMULA 1", 92, 18, color.RGBA{255, 50, 50, 255})
	drawCenteredText(canvas, truncateF1Text(strings.ToUpper(f1TrackName(event)), 24), 92, 42, color.White)
	drawCenteredText(canvas, strings.ToUpper(f1SessionName(race)), 92, 61, color.RGBA{100, 220, 255, 255})
	startText := "SOON"
	if start, err := time.Parse(time.RFC3339, race.Date); err == nil {
		startText = start.In(location).Format("MON 3:04 PM")
	}
	drawCenteredText(canvas, startText, 92, 83, color.White)
	return convertImageToVectorFaceData(canvas)
}

func f1LeaderboardSpeech(event f1Event, race f1Competition, drivers []f1Competitor, offset int, kind string) string {
	parts := make([]string, 0, len(drivers))
	for index, driver := range drivers {
		parts = append(parts, fmt.Sprintf("%s, %s", f1Ordinal(offset+index+1), f1LastName(driver.Athlete.DisplayName)))
	}
	sessionName := f1SessionName(race)
	prefix := "F1 " + sessionName + " update"
	if kind == "final" {
		prefix = "Final F1 " + sessionName + " result"
	} else if kind == "leader" {
		prefix = "F1 Alert. " + sessionName + " leader change"
	} else if kind == "phase" {
		prefix = "F1 Alert. " + sessionName
	}
	if phase := f1QualifyingPhase(race); phase != "" {
		prefix += ", " + phase
	}
	if offset == 0 {
		prefix += " from " + f1TrackName(event) + ". The top " + f1SpokenDriverLimit(f1LeaderboardDriverLimit(race)) + " are"
		return prefix + ". " + strings.Join(parts, ". ") + "."
	}
	return strings.Join(parts, ". ") + "."
}

func f1SpokenDriverLimit(limit int) string {
	switch limit {
	case 15:
		return "fifteen"
	default:
		return "ten"
	}
}

func f1PregameSpeech(event f1Event, race f1Competition, now time.Time) string {
	minutes := 1
	if start, err := time.Parse(time.RFC3339, race.Date); err == nil {
		minutes = maxInt(1, int(start.Sub(now).Round(time.Minute)/time.Minute))
	}
	sessionName := f1SessionName(race)
	subject := "The race"
	if f1IsQualifying(race) {
		subject = "Qualifying"
	} else if f1IsPractice(race) {
		subject = "Practice"
	}
	return fmt.Sprintf("Formula 1 %s reminder. %s at %s starts in about %d minutes.", sessionName, subject, f1TrackName(event), minutes)
}

func f1TrackName(event f1Event) string {
	if strings.TrimSpace(event.Circuit.FullName) != "" {
		return event.Circuit.FullName
	}
	name := event.ShortName
	if name == "" {
		name = event.Name
	}
	return name
}

func f1StatusText(race f1Competition) string {
	detail := race.Status.Type.ShortDetail
	if detail == "" {
		detail = race.Status.Type.Detail
	}
	if strings.EqualFold(detail, "Final") {
		return "FINAL"
	}
	return strings.ToUpper(detail)
}

func f1TeamForDriver(driver f1Competitor) f1Team {
	key := f1DriverTeams[driver.ID]
	if key == "" {
		key = f1NameTeams[strings.ToLower(f1LastName(driver.Athlete.DisplayName))]
	}
	if team, ok := f1Teams[key]; ok {
		return team
	}
	return f1Team{Name: "Formula 1", Code: "F1", Color: color.RGBA{220, 50, 50, 255}}
}

func f1LastName(name string) string {
	fields := strings.Fields(strings.TrimSpace(name))
	if len(fields) == 0 {
		return "Unknown"
	}
	return fields[len(fields)-1]
}

func f1Ordinal(position int) string {
	words := []string{"", "first", "second", "third", "fourth", "fifth", "sixth", "seventh", "eighth", "ninth", "tenth"}
	if position > 0 && position < len(words) {
		return words[position]
	}
	return strconv.Itoa(position)
}

func truncateF1Text(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func f1DrawText(dst draw.Image, text string, x, baselineY int, textColor color.Color) {
	drawer := font.Drawer{Dst: dst, Src: image.NewUniform(textColor), Face: basicfont.Face7x13, Dot: fixed.P(x, baselineY)}
	drawer.DrawString(text)
}
