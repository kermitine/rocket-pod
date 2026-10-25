package productivity

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

const nbaInjuriesFixture = `{"injuries":[
	{"team":{"abbreviation":"LAL"},"injuries":[
		{"status":"Out","athlete":{"lastName":"Grimes","displayName":"Quentin Grimes"}},
		{"status":"Out","athlete":{"lastName":"Reaves","displayName":"Austin Reaves"}},
		{"status":"Day-To-Day","athlete":{"lastName":"Doncic","displayName":"Luka Doncic"}}
	]},
	{"team":{"abbreviation":"BOS"},"injuries":[
		{"status":"Out","athlete":{"lastName":"Thybulle","displayName":"Matisse Thybulle"}},
		{"status":"Day-To-Day","athlete":{"lastName":"Kessler","displayName":"Walker Kessler"}}
	]}
]}`

func TestNBAInjuryReportCombinesBothTeams(t *testing.T) {
	var summary nbaSummary
	if err := json.Unmarshal([]byte(nbaInjuriesFixture), &summary); err != nil {
		t.Fatal(err)
	}
	want := "OUT: Grimes, Reaves, Thybulle. Gametime: Doncic, Kessler."
	if got := nbaInjuryReport(summary); got != want {
		t.Fatalf("injury report = %q, want %q", got, want)
	}
}

func TestNBAInjuryReportOmitsEmptyCategoriesAndUnavailablePlayers(t *testing.T) {
	for _, test := range []struct{ name, injuries, want string }{
		{"missing", `{}`, ""},
		{"empty", `{"injuries":[]}`, ""},
		{"out only", `{"injuries":[{"status":" OUT ","athlete":{"lastName":" Reaves "}}]}`, "OUT: Reaves."},
		{"gametime only", `{"injuries":[{"status":"Questionable","athlete":{"lastName":"Doncic"}},{"status":"Doubtful","athlete":{"lastName":"Kessler"}}]}`, "Gametime: Doncic, Kessler."},
		{"other gametime labels", `{"injuries":[{"status":"Game Time Decision","athlete":{"lastName":"Doncic"}},{"status":"Game-Time Decision","athlete":{"lastName":"Kessler"}},{"status":"GTD","athlete":{"lastName":"Reaves"}}]}`, "Gametime: Doncic, Kessler, Reaves."},
		{"missing last name", `{"injuries":[{"status":"Out","athlete":{"displayName":"Gary Trent Jr."}}]}`, "OUT: Trent Jr."},
		{"unnamed and healthy", `{"injuries":[{"status":"Out","athlete":{}},{"status":"Active","athlete":{"lastName":"Healthy"}},{"status":"Unknown","athlete":{"lastName":"Unknown"}}]}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var team nbaTeamInjuries
			if err := json.Unmarshal([]byte(test.injuries), &team); err != nil {
				t.Fatal(err)
			}
			if got := nbaInjuryReport(nbaSummary{Injuries: []nbaTeamInjuries{team}}); got != test.want {
				t.Fatalf("injury report = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNBAPregameQueuesInjuryReportAndSurvivesLookupFailure(t *testing.T) {
	for _, test := range []struct {
		name, state, body, wantReport string
		status                        int
		timeout                       bool
	}{
		{name: "both teams", state: "pre", body: nbaInjuriesFixture, status: http.StatusOK, wantReport: "OUT: Grimes, Reaves, Thybulle. Gametime: Doncic, Kessler."},
		{name: "no injuries", state: "pre", body: `{}`, status: http.StatusOK},
		{name: "HTTP failure", state: "pre", body: `unavailable`, status: http.StatusServiceUnavailable},
		{name: "invalid JSON", state: "pre", body: `{`, status: http.StatusOK},
		{name: "timeout", state: "pre", timeout: true},
		{name: "live skips injuries", state: "in"},
	} {
		t.Run(test.name, func(t *testing.T) {
			previousConfig, previousClient, previousQueue := vars.APIConfig.Productivity, externalApiClient, taskQueue
			t.Cleanup(func() {
				vars.APIConfig.Productivity, externalApiClient, taskQueue = previousConfig, previousClient, previousQueue
				resetNBANotificationState()
			})
			resetNBANotificationState()
			taskQueue = make(chan Task, 2)
			vars.APIConfig.Productivity.TargetRobot = "nba-injury-test"
			vars.APIConfig.Productivity.Timezone = "UTC"
			vars.APIConfig.Productivity.NBA = vars.NBAConfig{
				Enable: true, FavoriteTeams: []string{"LAL", "BOS"}, PregameMinutes: 15,
			}
			game := testNBAGame("injury-game", test.state, time.Now().Add(10*time.Minute), "0", "0")
			summaryRequests := 0
			externalApiClient = &http.Client{Transport: bedtimeScoreboardTransport(func(request *http.Request) (*http.Response, error) {
				var body []byte
				status := http.StatusOK
				switch {
				case strings.HasSuffix(request.URL.Path, "/scoreboard"):
					var err error
					body, err = json.Marshal(nbaScoreboard{Events: []nbaEvent{game}})
					if err != nil {
						t.Fatal(err)
					}
				case strings.HasSuffix(request.URL.Path, "/summary"):
					summaryRequests++
					if request.URL.Query().Get("event") != game.ID {
						t.Fatalf("injuries requested for wrong game: %s", request.URL)
					}
					deadline, ok := request.Context().Deadline()
					if !ok || time.Until(deadline) > 5*time.Second {
						t.Fatal("injury lookup has no short timeout")
					}
					if test.timeout {
						return nil, context.DeadlineExceeded
					}
					body, status = []byte(test.body), test.status
				default:
					t.Fatalf("unexpected request: %s", request.URL)
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
			})}

			checkNBAGames()
			select {
			case task := <-taskQueue:
				wantSpeech := "NBA game reminder. The Lakers play the Celtics in about 10 minutes."
				if test.state == "in" {
					wantSpeech = "NBA score update. Lakers, 0. Celtics, 0. IN."
				}
				if test.wantReport != "" {
					wantSpeech += " " + test.wantReport
				}
				if len(task.Phrases) != 1 || task.Phrases[0] != wantSpeech {
					t.Fatalf("queued speech = %q, want %q", task.Phrases, wantSpeech)
				}
			default:
				t.Fatal("NBA reminder was not queued")
			}
			checkNBAGames()
			wantRequests := 1
			if test.state == "in" {
				wantRequests = 0
			}
			if summaryRequests != wantRequests || len(taskQueue) != 0 {
				t.Fatalf("summary requests = %d, repeated reminders = %d", summaryRequests, len(taskQueue))
			}
		})
	}
}
