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
	APIConfig.Productivity.NBA = NBAConfig{
		Enable:            true,
		FavoriteTeams:     []string{"LAL", "BOS"},
		PregameMinutes:    15,
		LiveUpdateMinutes: 5,
		NotifyFinal:       true,
	}

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
	if !persisted.Productivity.NBA.Enable || len(persisted.Productivity.NBA.FavoriteTeams) != 2 || !persisted.Productivity.NBA.NotifyFinal {
		t.Fatalf("NBA config was not persisted: %#v", persisted.Productivity.NBA)
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
