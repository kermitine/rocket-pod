package sdkapp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCustomEyeColorRedHueWrapsForAnimations(t *testing.T) {
	for _, test := range []struct {
		name, hue string
		wantHue   float64
	}{
		{"start_of_wheel", "0", 0},
		{"end_of_wheel", "1.000", 0},
		{"rounded_on_robot", "0.99999999", 0},
		{"near_red", "0.999", 0.999},
		{"other_color", "0.57", 0.57},
	} {
		t.Run(test.name, func(t *testing.T) {
			hue, saturation, err := customEyeColorValues(test.hue, "1")
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/update_settings" {
					t.Errorf("unexpected eye color request: %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer test-guid" {
					t.Error("missing robot authorization")
				}
				var payload struct {
					UpdateSettings bool `json:"update_settings"`
					Settings       struct {
						CustomEyeColor struct {
							Enabled    bool    `json:"enabled"`
							Hue        float64 `json:"hue"`
							Saturation float64 `json:"saturation"`
						} `json:"custom_eye_color"`
					} `json:"settings"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				color := payload.Settings.CustomEyeColor
				if !payload.UpdateSettings || !color.Enabled || color.Hue != test.wantHue || color.Saturation != 1 {
					t.Errorf("wrong settings sent to Vector: %+v", payload)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			robot := Robot{Target: strings.TrimPrefix(server.URL, "https://"), GUID: "test-guid"}
			if err := setCustomEyeColor(robot, hue, saturation); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCustomEyeColorRejectsInvalidComponents(t *testing.T) {
	for _, invalid := range []string{"", "red", "NaN", "+Inf", "-Inf", "-0.1", "1.1", `0},"eye_color":0`} {
		if _, _, err := customEyeColorValues(invalid, "1"); err == nil {
			t.Errorf("accepted invalid hue %q", invalid)
		}
		if _, _, err := customEyeColorValues("0", invalid); err == nil {
			t.Errorf("accepted invalid saturation %q", invalid)
		}
	}
}

func TestCustomEyeColorReportsRobotFailure(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	robot := Robot{Target: strings.TrimPrefix(server.URL, "https://")}
	if err := setCustomEyeColor(robot, 0, 1); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("did not report rejected color settings: %v", err)
	}
}
