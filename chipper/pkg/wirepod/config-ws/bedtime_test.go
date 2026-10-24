package webserver

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kercre123/wire-pod/chipper/pkg/productivity"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

func bedtimeSettingsRequest(t *testing.T, config string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{"provider": "none", "manual_config": "[]", "bedtime_config": config} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/set_productivity_api", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func TestBedtimeSettingsSaveValidationAndCompatibility(t *testing.T) {
	previous, previousPath := vars.APIConfig.Productivity, vars.ApiConfigPath
	t.Cleanup(func() {
		vars.APIConfig.Productivity, vars.ApiConfigPath = previous, previousPath
		productivity.NotifyConfigUpdated()
	})
	vars.ApiConfigPath = filepath.Join(t.TempDir(), "apiConfig.json")
	valid := `{"enable":true,"start":"22:30","end":"07:15","timezone":"America/Los_Angeles"}`
	response := httptest.NewRecorder()
	handleSetProductivityAPI(response, bedtimeSettingsRequest(t, valid))
	if response.Code != http.StatusOK {
		t.Fatalf("save bedtime: %d %s", response.Code, response.Body.String())
	}
	want := vars.BedtimeConfig{Enable: true, Start: "22:30", End: "07:15", Timezone: "America/Los_Angeles"}
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
	if persisted.Productivity.Bedtime != want {
		t.Fatalf("persisted bedtime = %+v, want %+v", persisted.Productivity.Bedtime, want)
	}
	for _, invalid := range []string{
		`{"enable":true,"start":"25:00","end":"07:00","timezone":"UTC"}`,
		`{"enable":true,"start":"21:00","end":"21:00","timezone":"UTC"}`,
		`{"enable":true,"start":"21:00","end":"07:00","timezone":"Not/AZone"}`,
		`{"enable":true,"start":"21:00","end":"07:00"}`,
		`{broken`,
	} {
		response = httptest.NewRecorder()
		handleSetProductivityAPI(response, bedtimeSettingsRequest(t, invalid))
		if response.Code != http.StatusBadRequest || vars.APIConfig.Productivity.Bedtime != want {
			t.Fatalf("invalid schedule changed saved settings: %d %s", response.Code, response.Body.String())
		}
	}
	response = httptest.NewRecorder()
	handleSetProductivityAPI(response, bedtimeSettingsRequest(t, ""))
	if response.Code != http.StatusOK || vars.APIConfig.Productivity.Bedtime != want {
		t.Fatal("an older client erased the bedtime schedule")
	}
	vars.ApiConfigPath = filepath.Join(t.TempDir(), "missing", "apiConfig.json")
	response = httptest.NewRecorder()
	handleSetProductivityAPI(response, bedtimeSettingsRequest(t, `{"enable":false}`))
	if response.Code != http.StatusInternalServerError || vars.APIConfig.Productivity.Bedtime != want {
		t.Fatal("failed save changed the active bedtime schedule")
	}
}
