package webserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kercre123/wire-pod/chipper/pkg/productivity"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

func TestFaceSearchDirectionSettingsPersistenceAndCompatibility(t *testing.T) {
	previous, previousPath := vars.APIConfig.Productivity, vars.ApiConfigPath
	t.Cleanup(func() {
		vars.APIConfig.Productivity, vars.ApiConfigPath = previous, previousPath
		productivity.NotifyConfigUpdated()
	})
	vars.APIConfig.Productivity = vars.ProductivityConfig{}
	vars.ApiConfigPath = filepath.Join(t.TempDir(), "apiConfig.json")

	for _, test := range []struct {
		name, value string
		want        bool
		status      int
	}{
		{"existing config defaults to current direction", "", false, http.StatusOK},
		{"reverse", "true", true, http.StatusOK},
		{"older client preserves selection", "", true, http.StatusOK},
		{"invalid setting preserves selection", "sideways", true, http.StatusBadRequest},
		{"restore current direction", "false", false, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := bedtimeSettingsRequest(t, "")
			if err := request.ParseMultipartForm(10 << 20); err != nil {
				t.Fatal(err)
			}
			if test.value != "" {
				request.Form.Set("reverse_face_search", test.value)
			}
			response := httptest.NewRecorder()
			handleSetProductivityAPI(response, request)
			if response.Code != test.status || vars.APIConfig.Productivity.ReverseFaceSearch != test.want {
				t.Fatalf("save direction: status=%d, reverse=%v, body=%s", response.Code, vars.APIConfig.Productivity.ReverseFaceSearch, response.Body.String())
			}
			data, err := os.ReadFile(vars.ApiConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			var persisted struct {
				Productivity vars.ProductivityConfig `json:"productivity"`
			}
			if err := json.Unmarshal(data, &persisted); err != nil {
				t.Fatal(err)
			}
			if persisted.Productivity.ReverseFaceSearch != test.want {
				t.Fatalf("persisted reverse=%v, want %v", persisted.Productivity.ReverseFaceSearch, test.want)
			}
			response = httptest.NewRecorder()
			handleGetProductivityAPI(response)
			var settings vars.ProductivityConfig
			if err := json.Unmarshal(response.Body.Bytes(), &settings); err != nil {
				t.Fatal(err)
			}
			if settings.ReverseFaceSearch != test.want {
				t.Fatal("saved face-search direction was not returned to the UI")
			}
		})
	}

	vars.ApiConfigPath = filepath.Join(t.TempDir(), "missing", "apiConfig.json")
	request := bedtimeSettingsRequest(t, "")
	if err := request.ParseMultipartForm(10 << 20); err != nil {
		t.Fatal(err)
	}
	request.Form.Set("reverse_face_search", "true")
	response := httptest.NewRecorder()
	handleSetProductivityAPI(response, request)
	if response.Code != http.StatusInternalServerError || vars.APIConfig.Productivity.ReverseFaceSearch {
		t.Fatal("failed save changed the active face-search direction")
	}
}

func TestFaceSearchSpeedSettingsPersistenceAndValidation(t *testing.T) {
	previous, previousPath := vars.APIConfig.Productivity, vars.ApiConfigPath
	t.Cleanup(func() {
		vars.APIConfig.Productivity, vars.ApiConfigPath = previous, previousPath
		productivity.NotifyConfigUpdated()
	})
	vars.APIConfig.Productivity = vars.ProductivityConfig{}
	vars.ApiConfigPath = filepath.Join(t.TempDir(), "apiConfig.json")
	requestFor := func(value string) *http.Request {
		request := bedtimeSettingsRequest(t, "")
		if err := request.ParseMultipartForm(10 << 20); err != nil {
			t.Fatal(err)
		}
		if value != "" {
			request.Form.Set("face_search_speed_multiplier", value)
		}
		return request
	}
	for _, test := range []struct {
		value  string
		want   float64
		status int
	}{
		{"", 0, http.StatusOK},
		{"0.5", 0.5, http.StatusOK},
		{"1.5", 1.5, http.StatusOK},
		{"2.0", 2, http.StatusOK},
		{"2.1", 2.1, http.StatusOK},
		{"3.0", 3, http.StatusOK},
		{"4.0", 4, http.StatusOK},
		{"4.1", 4.1, http.StatusOK},
		{"6.0", 6, http.StatusOK},
		{"8.0", 8, http.StatusOK},
		{"", 8, http.StatusOK},
		{"NaN", 8, http.StatusBadRequest},
		{"Inf", 8, http.StatusBadRequest},
		{"invalid", 8, http.StatusBadRequest},
		{"0", 8, http.StatusBadRequest},
		{"0.4", 8, http.StatusBadRequest},
		{"8.1", 8, http.StatusBadRequest},
		{"1.0", 1, http.StatusOK},
	} {
		response := httptest.NewRecorder()
		handleSetProductivityAPI(response, requestFor(test.value))
		if response.Code != test.status || vars.APIConfig.Productivity.FaceSearchSpeedMultiplier != test.want {
			t.Fatalf("save %q: status=%d, speed=%v, body=%s", test.value, response.Code, vars.APIConfig.Productivity.FaceSearchSpeedMultiplier, response.Body.String())
		}
		data, err := os.ReadFile(vars.ApiConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		var persisted struct {
			Productivity vars.ProductivityConfig `json:"productivity"`
		}
		if err := json.Unmarshal(data, &persisted); err != nil {
			t.Fatal(err)
		}
		if persisted.Productivity.FaceSearchSpeedMultiplier != test.want {
			t.Fatalf("persisted speed=%v, want %v", persisted.Productivity.FaceSearchSpeedMultiplier, test.want)
		}
		response = httptest.NewRecorder()
		handleGetProductivityAPI(response)
		var settings vars.ProductivityConfig
		if err := json.Unmarshal(response.Body.Bytes(), &settings); err != nil {
			t.Fatal(err)
		}
		if settings.FaceSearchSpeedMultiplier != test.want {
			t.Fatal("saved speed was not returned to the UI")
		}
	}
	vars.ApiConfigPath = filepath.Join(t.TempDir(), "missing", "apiConfig.json")
	response := httptest.NewRecorder()
	handleSetProductivityAPI(response, requestFor("1.5"))
	if response.Code != http.StatusInternalServerError || vars.APIConfig.Productivity.FaceSearchSpeedMultiplier != 1 {
		t.Fatal("failed save changed the active face-search speed")
	}
}
