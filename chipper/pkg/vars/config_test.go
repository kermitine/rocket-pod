package vars

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteConfigToDiskWithErrorPersistsEmptyReminders(t *testing.T) {
	previousPath := ApiConfigPath
	previousConfig := APIConfig
	defer func() {
		ApiConfigPath = previousPath
		APIConfig = previousConfig
	}()

	ApiConfigPath = filepath.Join(t.TempDir(), "apiConfig.json")
	APIConfig.Productivity.ManualConfig = "[]"
	APIConfig.Productivity.ReverseFaceSearch = true
	APIConfig.Productivity.NBA = NBAConfig{
		Enable:            true,
		FavoriteTeams:     []string{"LAL", "BOS"},
		PregameMinutes:    15,
		LiveUpdateMinutes: 5,
		NotifyFinal:       true,
		NotifyNotable:     true,
	}
	APIConfig.Productivity.F1 = F1Config{Enable: true, PregameMinutes: 60, LiveUpdateMinutes: 10, QualifyingLiveUpdateMinutes: 6, PracticeLiveUpdateMinutes: 3, NotifyFinal: true, NotifyQualifying: true, NotifyPractice: true, NotifyNotable: true}
	APIConfig.Productivity.F1.SkipUnchangedTop = 3
	APIConfig.Productivity.Bedtime = BedtimeConfig{Enable: true, Start: "21:30", End: "07:15", Timezone: "America/Los_Angeles"}

	if err := WriteConfigToDiskWithError(); err != nil {
		t.Fatalf("WriteConfigToDiskWithError() error = %v", err)
	}
	data, err := os.ReadFile(ApiConfigPath)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	var persisted apiConfig
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("decode persisted config: %v", err)
	}
	if persisted.Productivity.ManualConfig != "[]" {
		t.Fatalf("manual_config = %q, want []", persisted.Productivity.ManualConfig)
	}
	if !persisted.Productivity.ReverseFaceSearch {
		t.Fatal("reverse face-search direction was not persisted")
	}
	if !persisted.Productivity.NBA.Enable || len(persisted.Productivity.NBA.FavoriteTeams) != 2 || !persisted.Productivity.NBA.NotifyFinal || !persisted.Productivity.NBA.NotifyNotable {
		t.Fatalf("NBA config was not persisted: %#v", persisted.Productivity.NBA)
	}
	if !persisted.Productivity.F1.Enable || persisted.Productivity.F1.LiveUpdateMinutes != 10 || persisted.Productivity.F1.SkipUnchangedTop != 3 || persisted.Productivity.F1.QualifyingLiveUpdateMinutes != 6 || persisted.Productivity.F1.PracticeLiveUpdateMinutes != 3 || !persisted.Productivity.F1.NotifyFinal || !persisted.Productivity.F1.NotifyQualifying || !persisted.Productivity.F1.NotifyPractice || !persisted.Productivity.F1.NotifyNotable {
		t.Fatalf("F1 config was not persisted: %#v", persisted.Productivity.F1)
	}
	if persisted.Productivity.Bedtime != APIConfig.Productivity.Bedtime {
		t.Fatalf("bedtime config was not persisted: %#v", persisted.Productivity.Bedtime)
	}
}

func TestWriteConfigToDiskWithErrorReportsFailure(t *testing.T) {
	previousPath := ApiConfigPath
	defer func() { ApiConfigPath = previousPath }()

	ApiConfigPath = filepath.Join(t.TempDir(), "missing", "apiConfig.json")
	if err := WriteConfigToDiskWithError(); err == nil {
		t.Fatal("WriteConfigToDiskWithError() error = nil, want write error")
	}
}
