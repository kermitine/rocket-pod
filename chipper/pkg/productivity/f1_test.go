package productivity

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

func TestF1RaceCompetitionSelectsRaceSession(t *testing.T) {
	event := f1Event{}
	practice := f1Competition{ID: "practice"}
	practice.Type.Abbreviation = "FP1"
	race := f1Competition{ID: "race"}
	race.Type.Abbreviation = "Race"
	event.Competitions = []f1Competition{practice, race}
	got, ok := f1RaceCompetition(event)
	if !ok || got.ID != "race" {
		t.Fatalf("f1RaceCompetition() = %#v, %v", got, ok)
	}
}

func TestF1NotificationSessionsIncludeOptionalSessionsWhenEnabled(t *testing.T) {
	event := f1Event{}
	practice := f1Competition{ID: "practice"}
	practice.Type.Abbreviation = "FP1"
	qualifying := f1Competition{ID: "qualifying"}
	qualifying.Type.Abbreviation = "Qual"
	race := f1Competition{ID: "race"}
	race.Type.Abbreviation = "Race"
	event.Competitions = []f1Competition{practice, qualifying, race}

	if got := f1NotificationSessions(event, false, false); len(got) != 1 || got[0].ID != "race" {
		t.Fatalf("race-only sessions = %#v", got)
	}
	if got := f1NotificationSessions(event, true, false); len(got) != 2 || got[0].ID != "qualifying" || got[1].ID != "race" {
		t.Fatalf("race and qualifying sessions = %#v", got)
	}
	if got := f1NotificationSessions(event, false, true); len(got) != 2 || got[0].ID != "practice" || got[1].ID != "race" {
		t.Fatalf("race and practice sessions = %#v", got)
	}
	if got := f1NotificationSessions(event, true, true); len(got) != 3 || got[0].ID != "practice" || got[1].ID != "qualifying" || got[2].ID != "race" {
		t.Fatalf("all F1 sessions = %#v", got)
	}
}

func TestF1TopTenSortsAndLimitsClassification(t *testing.T) {
	race := f1Competition{}
	for position := 12; position >= 1; position-- {
		driver := f1Competitor{Order: position}
		driver.Athlete.DisplayName = "Driver " + f1Ordinal(position)
		race.Competitors = append(race.Competitors, driver)
	}
	top := f1TopTen(race)
	if len(top) != 10 || top[0].Order != 1 || top[9].Order != 10 {
		t.Fatalf("f1TopTen() returned orders %v", f1Orders(top))
	}
}

func TestF1NotificationLifecycle(t *testing.T) {
	resetF1NotificationState()
	now := time.Date(2026, time.June, 28, 12, 0, 0, 0, time.UTC)
	config := vars.F1Config{PregameMinutes: 60, LiveUpdateMinutes: 10, NotifyFinal: true}
	race := f1Competition{ID: "austria", Date: now.Add(45 * time.Minute).Format(time.RFC3339)}
	race.Status.Type.State = "pre"
	if kind, ok := f1NotificationForRace(race, config, now); !ok || kind != "pregame" {
		t.Fatalf("pregame notification = %q, %v", kind, ok)
	}
	markF1Notified(race.ID, "pregame", now)
	if _, ok := f1NotificationForRace(race, config, now); ok {
		t.Fatal("duplicate pregame notification was allowed")
	}

	race.Status.Type.State = "in"
	race.Competitors = []f1Competitor{{Order: 1}}
	if kind, ok := f1NotificationForRace(race, config, now); !ok || kind != "live" {
		t.Fatalf("live notification = %q, %v", kind, ok)
	}
	markF1Notified(race.ID, "live", now)
	if _, ok := f1NotificationForRace(race, config, now.Add(9*time.Minute)); ok {
		t.Fatal("live notification ignored its interval")
	}
	if _, ok := f1NotificationForRace(race, config, now.Add(10*time.Minute)); !ok {
		t.Fatal("live notification did not resume at its interval")
	}

	race.Status.Type.State = "post"
	if kind, ok := f1NotificationForRace(race, config, now); !ok || kind != "final" {
		t.Fatalf("final notification = %q, %v", kind, ok)
	}
}

func TestF1LiveNotificationUsesSessionIntervals(t *testing.T) {
	resetF1NotificationState()
	now := time.Date(2026, time.June, 28, 12, 0, 0, 0, time.UTC)
	config := vars.F1Config{
		LiveUpdateMinutes:           20,
		QualifyingLiveUpdateMinutes: 6,
		PracticeLiveUpdateMinutes:   3,
	}
	tests := []struct {
		name       string
		session    f1Competition
		before     time.Duration
		atInterval time.Duration
	}{
		{name: "race", session: f1LiveIntervalSession("race", "Race"), before: 19 * time.Minute, atInterval: 20 * time.Minute},
		{name: "qualifying", session: f1LiveIntervalSession("qualifying", "Qual"), before: 5 * time.Minute, atInterval: 6 * time.Minute},
		{name: "practice", session: f1LiveIntervalSession("practice", "FP1"), before: 2 * time.Minute, atInterval: 3 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetF1NotificationState()
			if kind, ok := f1NotificationForSession(tt.session, config, now, true); !ok || kind != "live" {
				t.Fatalf("initial live notification = %q, %v", kind, ok)
			}
			markF1Notified(tt.session.ID, "live", now)
			if _, ok := f1NotificationForSession(tt.session, config, now.Add(tt.before), true); ok {
				t.Fatal("live notification ignored its session interval")
			}
			if kind, ok := f1NotificationForSession(tt.session, config, now.Add(tt.atInterval), true); !ok || kind != "live" {
				t.Fatalf("interval live notification = %q, %v", kind, ok)
			}
		})
	}
}

func TestF1NotableLeaderChange(t *testing.T) {
	resetF1NotificationState()
	_, session := syntheticF1Race()
	if kind, notify := f1NotableMoment(session); notify || kind != "" {
		t.Fatalf("initial notable moment = %q, %v", kind, notify)
	}
	session.Competitors[0].Order = 2
	session.Competitors[1].Order = 1
	if kind, notify := f1NotableMoment(session); !notify || kind != "leader" {
		t.Fatalf("leader notable moment = %q, %v", kind, notify)
	}
}

func TestF1NotableQualifyingPhaseChange(t *testing.T) {
	resetF1NotificationState()
	_, session := syntheticF1LiveQualifying()
	session.Status.Type.ShortDetail = "Q1 - 5:00"
	if kind, notify := f1NotableMoment(session); notify || kind != "" {
		t.Fatalf("initial notable moment = %q, %v", kind, notify)
	}
	session.Status.Type.ShortDetail = "Q2 - 12:00"
	if kind, notify := f1NotableMoment(session); !notify || kind != "phase" {
		t.Fatalf("phase notable moment = %q, %v", kind, notify)
	}
}

func TestF1NotableSpeechUsesAlertPrefix(t *testing.T) {
	event, session := syntheticF1LiveQualifying()
	leaderSpeech := f1LeaderboardSpeech(event, session, session.Competitors[:5], 0, "leader")
	if !strings.HasPrefix(leaderSpeech, "F1 Alert. qualifying leader change") {
		t.Fatalf("leader alert speech = %q", leaderSpeech)
	}
	phaseSpeech := f1LeaderboardSpeech(event, session, session.Competitors[:5], 0, "phase")
	if !strings.HasPrefix(phaseSpeech, "F1 Alert. qualifying") {
		t.Fatalf("phase alert speech = %q", phaseSpeech)
	}
}

func TestF1LeaderboardPagesShowAndSpeakTopTen(t *testing.T) {
	event, race := syntheticF1Race()
	pages, err := f1LeaderboardTaskPages(event, race, "final")
	if err != nil {
		t.Fatalf("f1LeaderboardTaskPages() error = %v", err)
	}
	if len(pages) != 2 || len(pages[0].FaceData) == 0 || len(pages[1].FaceData) == 0 {
		t.Fatalf("leaderboard pages = %#v", pages)
	}
	if strings.Contains(pages[1].Speech, "Continuing") || !strings.HasPrefix(pages[1].Speech, "sixth,") {
		t.Fatalf("second-page speech = %q", pages[1].Speech)
	}
	speech := pages[0].Speech + " " + pages[1].Speech
	for _, lastName := range []string{"Norris", "Verstappen", "Leclerc", "Russell", "Piastri", "Hamilton", "Sainz", "Alonso", "Albon", "Bearman"} {
		if !strings.Contains(speech, lastName) {
			t.Errorf("leaderboard speech does not include %s: %q", lastName, speech)
		}
	}
}

func TestF1DriverTeamBadge(t *testing.T) {
	driver := f1Competitor{ID: "5579"}
	driver.Athlete.DisplayName = "Lando Norris"
	if got := f1TeamForDriver(driver); got.Code != "MCL" || got.Name != "McLaren" {
		t.Fatalf("f1TeamForDriver() = %#v", got)
	}
}

func TestF1QualifyingLeaderboardSpeech(t *testing.T) {
	event, session := syntheticF1Race()
	session.Type.Abbreviation = "Qual"
	pages, err := f1LeaderboardTaskPages(event, session, "final")
	if err != nil {
		t.Fatalf("f1LeaderboardTaskPages() error = %v", err)
	}
	if !strings.Contains(pages[0].Speech, "Final F1 qualifying result") {
		t.Fatalf("qualifying speech = %q", pages[0].Speech)
	}
}

func TestSyntheticF1QualifyingLeaderboard(t *testing.T) {
	event, session := syntheticF1Qualifying()
	if session.ID != "f1-test-qualifying" || session.Type.Abbreviation != "Qual" || session.Status.Type.State != "post" {
		t.Fatalf("syntheticF1Qualifying() session = %#v", session)
	}
	pages, err := f1LeaderboardTaskPages(event, session, "final")
	if err != nil {
		t.Fatalf("f1LeaderboardTaskPages() error = %v", err)
	}
	if len(pages) != 2 || !strings.Contains(pages[0].Speech, "Final F1 qualifying result") {
		t.Fatalf("qualifying test pages = %#v", pages)
	}
}

func TestSyntheticF1LiveQualifyingLeaderboard(t *testing.T) {
	event, session := syntheticF1LiveQualifying()
	if !f1IsQualifying(session) || session.Status.Type.State != "in" || session.Status.Type.ShortDetail != "Q3 - 4:12" {
		t.Fatalf("syntheticF1LiveQualifying() session = %#v", session)
	}
	pages, err := f1LeaderboardTaskPages(event, session, "live")
	if err != nil {
		t.Fatalf("f1LeaderboardTaskPages() error = %v", err)
	}
	if len(pages) != 2 || !strings.Contains(pages[0].Speech, "F1 qualifying update, Q3") {
		t.Fatalf("live qualifying test pages = %#v", pages)
	}
	if header := f1LeaderboardHeaderText(event, session, "live"); header != "Q3 - VECTOR RACEWAY" {
		t.Fatalf("live qualifying header = %q", header)
	}
}

func TestSyntheticF1LivePracticeLeaderboard(t *testing.T) {
	event, session := syntheticF1LivePractice()
	if !f1IsPractice(session) || session.Status.Type.State != "in" || session.Status.Type.ShortDetail != "Practice 1 - 12:34" {
		t.Fatalf("syntheticF1LivePractice() session = %#v", session)
	}
	pages, err := f1LeaderboardTaskPages(event, session, "live")
	if err != nil {
		t.Fatalf("f1LeaderboardTaskPages() error = %v", err)
	}
	if len(pages) != 2 || !strings.Contains(pages[0].Speech, "F1 practice update") {
		t.Fatalf("live practice test pages = %#v", pages)
	}
	now := time.Now()
	session.Date = now.Add(30 * time.Minute).Format(time.RFC3339)
	if speech := f1PregameSpeech(event, session, now); !strings.Contains(speech, "Formula 1 practice reminder") || !strings.Contains(speech, "Practice at Vector Raceway") {
		t.Fatalf("practice pregame speech = %q", speech)
	}
}

func TestF1Q1QualifyingLeaderboardIncludesTopFifteen(t *testing.T) {
	event, session := syntheticF1LiveQualifying()
	session.Status.Type.ShortDetail = "Q1 - 8:12"
	addF1TestCompetitors(&session, 11, 15)
	pages, err := f1LeaderboardTaskPages(event, session, "live")
	if err != nil {
		t.Fatalf("f1LeaderboardTaskPages() error = %v", err)
	}
	if len(pages) != 3 {
		t.Fatalf("Q1 pages = %d, want 3", len(pages))
	}
	speech := pages[0].Speech + " " + pages[1].Speech + " " + pages[2].Speech
	if !strings.Contains(pages[0].Speech, "F1 qualifying update, Q1") || !strings.Contains(pages[0].Speech, "The top fifteen are") {
		t.Fatalf("Q1 opening speech = %q", pages[0].Speech)
	}
	if !strings.Contains(speech, "Driver15") {
		t.Fatalf("Q1 speech omitted fifteenth driver: %q", speech)
	}
}

func TestF1Q2QualifyingLeaderboardIncludesTopTen(t *testing.T) {
	event, session := syntheticF1LiveQualifying()
	session.Status.Type.ShortDetail = "Q2 - 2:47"
	addF1TestCompetitors(&session, 11, 15)
	pages, err := f1LeaderboardTaskPages(event, session, "live")
	if err != nil {
		t.Fatalf("f1LeaderboardTaskPages() error = %v", err)
	}
	if len(pages) != 2 || !strings.Contains(pages[0].Speech, "F1 qualifying update, Q2") {
		t.Fatalf("Q2 pages = %#v", pages)
	}
	speech := pages[0].Speech + " " + pages[1].Speech
	if strings.Contains(speech, "Driver11") || strings.Contains(speech, "Driver15") {
		t.Fatalf("Q2 speech included drivers outside the top ten: %q", speech)
	}
}

func TestF1AllowedHours(t *testing.T) {
	tests := []struct {
		name       string
		hour       int
		minute     int
		start, end string
		want       bool
	}{
		{name: "daytime", hour: 12, start: "08:00", end: "22:00", want: true},
		{name: "three AM quiet", hour: 3, start: "08:00", end: "22:00", want: false},
		{name: "end exclusive", hour: 22, start: "08:00", end: "22:00", want: false},
		{name: "overnight evening", hour: 23, start: "20:00", end: "08:00", want: true},
		{name: "overnight morning", hour: 7, minute: 59, start: "20:00", end: "08:00", want: true},
		{name: "same means unrestricted", hour: 3, start: "00:00", end: "00:00", want: true},
		{name: "invalid uses defaults", hour: 3, start: "bad", end: "bad", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, time.June, 28, tt.hour, tt.minute, 0, 0, time.FixedZone("configured", -7*60*60))
			if got := f1WithinAllowedHours(now, tt.start, tt.end); got != tt.want {
				t.Fatalf("f1WithinAllowedHours(%s, %q, %q) = %v, want %v", now.Format("15:04"), tt.start, tt.end, got, tt.want)
			}
		})
	}
}

func addF1TestCompetitors(session *f1Competition, first, last int) {
	for position := first; position <= last; position++ {
		driver := f1Competitor{Order: position}
		driver.Athlete.DisplayName = fmt.Sprintf("Driver%d", position)
		driver.Athlete.FullName = driver.Athlete.DisplayName
		session.Competitors = append(session.Competitors, driver)
	}
}

func f1LiveIntervalSession(id, abbreviation string) f1Competition {
	session := f1Competition{ID: id}
	session.Type.Abbreviation = abbreviation
	session.Status.Type.State = "in"
	session.Competitors = []f1Competitor{{Order: 1}}
	return session
}

func f1Orders(drivers []f1Competitor) []int {
	orders := make([]int, len(drivers))
	for index, driver := range drivers {
		orders[index] = driver.Order
	}
	return orders
}
