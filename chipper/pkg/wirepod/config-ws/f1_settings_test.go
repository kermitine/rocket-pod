package webserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kercre123/wire-pod/chipper/pkg/productivity"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

func TestF1SkipUnchangedSettingsSaveAndValidation(t *testing.T) {
	previous, previousPath := vars.APIConfig.Productivity, vars.ApiConfigPath
	t.Cleanup(func() {
		vars.APIConfig.Productivity, vars.ApiConfigPath = previous, previousPath
		productivity.NotifyConfigUpdated()
	})
	vars.ApiConfigPath = filepath.Join(t.TempDir(), "apiConfig.json")
	for _, test := range []struct{ value, want int }{{3, 3}, {1, 1}, {10, 10}, {0, 0}, {-1, 0}, {11, 10}} {
		request := bedtimeSettingsRequest(t, "")
		if err := request.ParseMultipartForm(10 << 20); err != nil {
			t.Fatal(err)
		}
		request.Form.Set("f1_config", fmt.Sprintf(`{"enable":true,"skip_unchanged_top":%d}`, test.value))
		response := httptest.NewRecorder()
		handleSetProductivityAPI(response, request)
		if response.Code != http.StatusOK || vars.APIConfig.Productivity.F1.SkipUnchangedTop != test.want {
			t.Fatalf("save %d: status=%d, setting=%d, body=%s", test.value, response.Code, vars.APIConfig.Productivity.F1.SkipUnchangedTop, response.Body.String())
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
		if persisted.Productivity.F1.SkipUnchangedTop != test.want {
			t.Fatalf("persisted setting=%d, want %d", persisted.Productivity.F1.SkipUnchangedTop, test.want)
		}
	}
}
