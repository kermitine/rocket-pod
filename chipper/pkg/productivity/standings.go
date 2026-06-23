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

	xdraw "golang.org/x/image/draw"
)

const (
	standingsNBAEast        = "nba_east"
	standingsNBAWest        = "nba_west"
	standingsNBAAll         = "nba_all"
	standingsF1Drivers      = "f1_drivers"
	standingsF1Constructors = "f1_constructors"
	nbaStandingsEndpoint    = "https://site.api.espn.com/apis/v2/sports/basketball/nba/standings?region=us&lang=en&contentorigin=espn&type=0&level=2"
	f1StandingsEndpoint     = "https://site.web.api.espn.com/apis/v2/sports/racing/f1/standings?region=us&lang=en"
	standingsEntriesPerPage = 5
	standingsRequestTimeout = 45 * time.Second
)

type espnStandingsResponse struct {
	Children []espnStandingsGroup `json:"children"`
}

type espnStandingsGroup struct {
	Name         string `json:"name"`
	Abbreviation string `json:"abbreviation"`
	Standings    struct {
		Entries []espnStandingsEntry `json:"entries"`
	} `json:"standings"`
}

type espnStandingsEntry struct {
	Team struct {
		ID               string `json:"id"`
		DisplayName      string `json:"displayName"`
		ShortDisplayName string `json:"shortDisplayName"`
		Abbreviation     string `json:"abbreviation"`
		Color            string `json:"color"`
		Logos            []struct {
			Href string `json:"href"`
		} `json:"logos"`
	} `json:"team"`
	Athlete struct {
		ID           string `json:"id"`
		DisplayName  string `json:"displayName"`
		Abbreviation string `json:"abbreviation"`
		ShortName    string `json:"shortName"`
	} `json:"athlete"`
	Stats []espnStandingStat `json:"stats"`
}

type espnStandingStat struct {
	Name         string  `json:"name"`
	Type         string  `json:"type"`
	Value        float64 `json:"value"`
	DisplayValue string  `json:"displayValue"`
}

type standingsRow struct {
	Position  int
	Name      string
	FaceName  string
	Detail    string
	Speech    string
	LogoURL   string
	BadgeCode string
	Badge     color.RGBA
}

func MatchStandingsVoiceCommand(text string) (string, bool) {
	text = normalizeStandingsVoiceText(text)
	if !containsAnyWord(text, "standing", "standings", "leaderboard", "rankings", "table") {
		return "", false
	}
	if containsAny(text, "formula one", "formula 1", "f1", "grand prix") || containsAnyWord(text, "driver", "drivers", "constructor", "constructors") {
		if containsAny(text, "constructor", "constructors", "team", "teams") {
			return standingsF1Constructors, true
		}
		return standingsF1Drivers, true
	}
	if containsAny(text, "nba", "basketball", "eastern conference", "western conference") || containsAnyWord(text, "eastern", "western") {
		if containsAny(text, "eastern", "east") {
			return standingsNBAEast, true
		}
		if containsAny(text, "western", "west") {
			return standingsNBAWest, true
		}
		return standingsNBAAll, true
	}
	return "", false
}

func normalizeStandingsVoiceText(text string) string {
	text = strings.ToLower(text)
	text = strings.Join(strings.FieldsFunc(text, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}), " ")
	replacer := strings.NewReplacer(
		"and be a", "nba",
		"and b a", "nba",
		"n be a", "nba",
		"n b a", "nba",
		"in b a", "nba",
		"m b a", "nba",
		"formula won", "formula one",
		"if won", "f1",
		"if one", "f1",
		"f won", "f1",
		"f one", "f1",
		"f 1", "f1",
	)
	return strings.Join(strings.Fields(replacer.Replace(text)), " ")
}

func containsAnyWord(text string, values ...string) bool {
	wanted := make(map[string]bool, len(values))
	for _, value := range values {
		wanted[value] = true
	}
	for _, token := range strings.FieldsFunc(text, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	}) {
		if wanted[token] {
			return true
		}
	}
	return false
}

func containsAny(text string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}

func QueueVoiceStandings(robotESN, kind string) error {
	if strings.TrimSpace(robotESN) == "" {
		return fmt.Errorf("voice command has no robot serial")
	}
	switch kind {
	case standingsNBAEast, standingsNBAWest, standingsNBAAll, standingsF1Drivers, standingsF1Constructors:
	default:
		return fmt.Errorf("unknown standings kind %q", kind)
	}
	generation := currentConfigurationGeneration()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), standingsRequestTimeout)
		defer cancel()
		pages, err := fetchVoiceStandingsPages(ctx, kind)
		if err != nil {
			pages = standingsErrorPages(kind, err)
		}
		task := Task{
			ID:                      fmt.Sprintf("standings_%s_%d", kind, time.Now().UnixNano()),
			RobotESN:                robotESN,
			Pages:                   pages,
			Source:                  "standings",
			configurationGeneration: generation,
		}
		select {
		case taskQueue <- task:
		default:
		}
	}()
	return nil
}

func fetchVoiceStandingsPages(ctx context.Context, kind string) ([]TaskPage, error) {
	switch kind {
	case standingsNBAEast, standingsNBAWest, standingsNBAAll:
		response, err := fetchStandings(ctx, nbaStandingsEndpoint)
		if err != nil {
			return nil, err
		}
		return nbaStandingsPages(ctx, response, kind)
	case standingsF1Drivers, standingsF1Constructors:
		response, err := fetchStandings(ctx, f1StandingsEndpoint)
		if err != nil {
			return nil, err
		}
		return f1StandingsPages(response, kind)
	default:
		return nil, fmt.Errorf("unsupported standings kind %q", kind)
	}
}

func fetchStandings(ctx context.Context, endpoint string) (espnStandingsResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return espnStandingsResponse{}, err
	}
	resp, err := externalApiClient.Do(req)
	if err != nil {
		return espnStandingsResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return espnStandingsResponse{}, fmt.Errorf("standings returned HTTP %d", resp.StatusCode)
	}
	var result espnStandingsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return espnStandingsResponse{}, err
	}
	return result, nil
}

func nbaStandingsPages(ctx context.Context, response espnStandingsResponse, kind string) ([]TaskPage, error) {
	pages := make([]TaskPage, 0, 6)
	for _, group := range response.Children {
		isEast := strings.Contains(strings.ToLower(group.Name), "east")
		if (kind == standingsNBAEast && !isEast) || (kind == standingsNBAWest && isEast) {
			continue
		}
		rows := make([]standingsRow, 0, len(group.Standings.Entries))
		for index, entry := range group.Standings.Entries {
			wins := standingStat(entry.Stats, "wins")
			losses := standingStat(entry.Stats, "losses")
			position := standingPosition(entry.Stats, index+1, "playoffSeed")
			logoURL := ""
			if len(entry.Team.Logos) > 0 {
				logoURL = entry.Team.Logos[0].Href
			}
			rows = append(rows, standingsRow{
				Position:  position,
				Name:      spokenNBATeamDisplayName(entry.Team.DisplayName, entry.Team.Abbreviation),
				FaceName:  entry.Team.Abbreviation,
				Detail:    wins + "-" + losses,
				Speech:    fmt.Sprintf("%s wins and %s losses", wins, losses),
				LogoURL:   logoURL,
				BadgeCode: entry.Team.Abbreviation,
				Badge:     color.RGBA{80, 130, 220, 255},
			})
		}
		conference := "Western Conference"
		if isEast {
			conference = "Eastern Conference"
		}
		pages = append(pages, standingsTaskPages(ctx, "NBA "+conference, conference+" standings", rows, true)...)
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("NBA standings response has no requested conference")
	}
	return pages, nil
}

func f1StandingsPages(response espnStandingsResponse, kind string) ([]TaskPage, error) {
	wantedConstructor := kind == standingsF1Constructors
	for _, group := range response.Children {
		isConstructor := strings.Contains(strings.ToLower(group.Name), "constructor")
		if isConstructor != wantedConstructor {
			continue
		}
		rows := make([]standingsRow, 0, len(group.Standings.Entries))
		for index, entry := range group.Standings.Entries {
			points := standingStat(entry.Stats, "championshipPts", "points")
			position := standingPosition(entry.Stats, index+1, "rank")
			row := standingsRow{Position: position, Detail: points + " PTS", Speech: points + " points"}
			if isConstructor {
				row.Name = entry.Team.DisplayName
				row.FaceName = entry.Team.ShortDisplayName
				if row.FaceName == "" {
					row.FaceName = entry.Team.DisplayName
				}
				row.BadgeCode = f1ConstructorCode(entry.Team.DisplayName)
				row.Badge = parseStandingsColor(entry.Team.Color, color.RGBA{220, 50, 50, 255})
			} else {
				row.Name = entry.Athlete.DisplayName
				row.FaceName = f1LastName(entry.Athlete.DisplayName)
				driver := f1Competitor{ID: entry.Athlete.ID}
				driver.Athlete.DisplayName = entry.Athlete.DisplayName
				team := f1TeamForDriver(driver)
				row.BadgeCode = team.Code
				row.Badge = team.Color
			}
			rows = append(rows, row)
		}
		title := "F1 Driver Standings"
		speechTitle := "Formula 1 driver standings"
		if isConstructor {
			title = "F1 Constructors"
			speechTitle = "Formula 1 constructor standings"
		}
		return standingsTaskPages(context.Background(), title, speechTitle, rows, false), nil
	}
	return nil, fmt.Errorf("F1 standings response has no requested table")
}

func standingsTaskPages(ctx context.Context, title, speechTitle string, rows []standingsRow, showLogos bool) []TaskPage {
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].Position < rows[j].Position
	})
	pages := make([]TaskPage, 0, (len(rows)+standingsEntriesPerPage-1)/standingsEntriesPerPage)
	for start := 0; start < len(rows); start += standingsEntriesPerPage {
		end := start + standingsEntriesPerPage
		if end > len(rows) {
			end = len(rows)
		}
		pageRows := rows[start:end]
		pages = append(pages, TaskPage{
			FaceData: renderStandingsFace(ctx, title, pageRows, showLogos),
			Speech:   standingsSpeech(speechTitle, pageRows, start == 0),
		})
	}
	return pages
}

func renderStandingsFace(ctx context.Context, title string, rows []standingsRow, showLogos bool) []byte {
	canvas := image.NewNRGBA(image.Rect(0, 0, 184, 96))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
	drawCenteredText(canvas, strings.ToUpper(truncateF1Text(title, 22)), 92, 11, color.RGBA{100, 220, 255, 255})
	for index, row := range rows {
		y := 19 + index*15
		f1DrawText(canvas, strconv.Itoa(row.Position), 2, y+10, color.White)
		logoDrawn := false
		if showLogos && row.LogoURL != "" {
			if logo, err := loadNBALogo(ctx, row.LogoURL); err == nil {
				xdraw.CatmullRom.Scale(canvas, image.Rect(18, y, 30, y+12), logo, logo.Bounds(), draw.Over, nil)
				logoDrawn = true
			}
		}
		if !logoDrawn {
			draw.Draw(canvas, image.Rect(19, y, 27, y+11), image.NewUniform(row.Badge), image.Point{}, draw.Src)
		}
		f1DrawText(canvas, truncateF1Text(row.FaceName, 12), 34, y+10, color.White)
		detailX := 184 - len(row.Detail)*7 - 2
		if detailX < 125 {
			detailX = 125
		}
		f1DrawText(canvas, row.Detail, detailX, y+10, row.Badge)
	}
	return convertImageToVectorFaceData(canvas)
}

func standingsSpeech(title string, rows []standingsRow, firstPage bool) string {
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, fmt.Sprintf("%s, %s, %s", f1OrdinalLong(row.Position), row.Name, row.Speech))
	}
	prefix := "Continuing " + title
	if firstPage {
		prefix = title
	}
	return prefix + ". " + strings.Join(parts, ". ") + "."
}

func standingsErrorPages(kind string, err error) []TaskPage {
	canvas := image.NewNRGBA(image.Rect(0, 0, 184, 96))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
	drawCenteredText(canvas, "STANDINGS", 92, 36, color.RGBA{220, 50, 50, 255})
	drawCenteredText(canvas, "UNAVAILABLE", 92, 62, color.White)
	return []TaskPage{{FaceData: convertImageToVectorFaceData(canvas), Speech: "Sorry, I couldn't get the current standings."}}
}

func standingStat(stats []espnStandingStat, wanted ...string) string {
	for _, stat := range stats {
		for _, candidate := range wanted {
			if strings.EqualFold(stat.Name, candidate) || strings.EqualFold(stat.Type, candidate) {
				if strings.TrimSpace(stat.DisplayValue) != "" {
					return stat.DisplayValue
				}
				return strconv.Itoa(int(stat.Value))
			}
		}
	}
	return "0"
}

func standingPosition(stats []espnStandingStat, fallback int, wanted string) int {
	value := standingStat(stats, wanted)
	position, err := strconv.Atoi(strings.TrimSpace(value))
	if err == nil && position > 0 {
		return position
	}
	return fallback
}

func parseStandingsColor(value string, fallback color.RGBA) color.RGBA {
	value = strings.TrimPrefix(strings.TrimSpace(value), "#")
	if len(value) != 6 {
		return fallback
	}
	parsed, err := strconv.ParseUint(value, 16, 32)
	if err != nil {
		return fallback
	}
	return color.RGBA{uint8(parsed >> 16), uint8(parsed >> 8), uint8(parsed), 255}
}

func f1ConstructorCode(name string) string {
	for _, team := range f1Teams {
		if strings.EqualFold(team.Name, name) {
			return team.Code
		}
	}
	words := strings.Fields(name)
	code := ""
	for _, word := range words {
		code += strings.ToUpper(string([]rune(word)[0]))
	}
	return truncateF1Text(code, 3)
}

func f1OrdinalLong(position int) string {
	if position <= 10 {
		return f1Ordinal(position)
	}
	lastTwo := position % 100
	suffix := "th"
	if lastTwo < 11 || lastTwo > 13 {
		switch position % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(position) + suffix
}
