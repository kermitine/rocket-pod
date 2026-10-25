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
