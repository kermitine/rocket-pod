package wirepod_vosk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/localization"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/stt/engine"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/stt/models"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/stt/whisperlocal"
)

func TestInitAttemptsToRestoreMissingConfiguredModel(t *testing.T) {
	previousConfig, previousPath, previousURL := vars.APIConfig, vars.VoskModelPath, localization.URLPrefix
	t.Cleanup(func() {
		vars.APIConfig, vars.VoskModelPath, localization.URLPrefix = previousConfig, previousPath, previousURL
		localization.PollDownloadStatus()
	})
	vars.VoskModelPath = filepath.Join(t.TempDir(), "deleted-models-folder")
	vars.APIConfig.PastInitialSetup = true
	vars.APIConfig.STT.Language = "it-IT"
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		if r.URL.Path != "/vosk-model-small-it-0.22.zip" {
			t.Errorf("startup requested wrong model: %s", r.URL.Path)
		}
		http.Error(w, "model download unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	localization.URLPrefix = server.URL + "/"
	err := Init()
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") || atomic.LoadInt32(&requests) != 1 {
		t.Fatalf("startup did not attempt model recovery or report the download failure: requests=%d, error=%v", requests, err)
	}
	if vars.APIConfig.STT.Language != "it-IT" || !vars.APIConfig.PastInitialSetup {
		t.Fatal("failed recovery changed the saved language or setup flag")
	}
	vars.APIConfig.PastInitialSetup = false
	if err := Init(); err != nil || atomic.LoadInt32(&requests) != 1 {
		t.Fatalf("initial setup should allow the user to choose a model: %v", err)
	}
}

func TestLiveVoskWhisperSwitchIntegration(t *testing.T) {
	voskPath, whisperPath, pcmPath := os.Getenv("ROCKET_POD_TEST_VOSK_MODEL"), os.Getenv("ROCKET_POD_TEST_WHISPER_MODEL"), os.Getenv("ROCKET_POD_TEST_PCM")
	if voskPath == "" || whisperPath == "" || pcmPath == "" {
		t.Skip("set the three ROCKET_POD_TEST_* model/audio paths for live switching")
	}
	previous, oldVosk, oldWhisper, oldConfigPath := vars.APIConfig, vars.VoskModelPath, vars.WhisperModelPath, vars.ApiConfigPath
	t.Cleanup(func() {
		whisperlocal.Close()
		Close()
		vars.APIConfig, vars.VoskModelPath, vars.WhisperModelPath, vars.ApiConfigPath = previous, oldVosk, oldWhisper, oldConfigPath
	})
	root := t.TempDir()
	vars.VoskModelPath, vars.WhisperModelPath, vars.ApiConfigPath = filepath.Join(root, "vosk"), filepath.Join(root, "whisper", "models"), filepath.Join(root, "config.json")
	if err := os.MkdirAll(filepath.Join(vars.VoskModelPath, "en-US"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(vars.WhisperModelPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(voskPath, filepath.Join(vars.VoskModelPath, "en-US", "model")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(whisperPath, filepath.Join(vars.WhisperModelPath, "ggml-tiny.en.bin")); err != nil {
		t.Fatal(err)
	}
	vars.APIConfig.STT = vars.STTConfig{Service: "vosk", Language: "en-US", WhisperModel: "tiny.en"}
	vars.APIConfig.PastInitialSetup = true
	t.Setenv("VOSK_WITH_GRAMMER", "true")
	engine.Configure(Name, Init)
	if err := engine.Reload(); err != nil {
		t.Fatal(err)
	}
	pcm, err := os.ReadFile(pcmPath)
	if err != nil {
		t.Fatal(err)
	}
	native := func() (string, error) {
		rec, index, err := getRec(false)
		if err != nil {
			return "", err
		}
		defer releaseRec(false, index)
		rec.AcceptWaveform(pcm)
		var result struct {
			Text string `json:"text"`
		}
		err = json.Unmarshal([]byte(rec.FinalResult()), &result)
		return result.Text, err
	}
	transcribe := func() {
		text, err := engine.Transcribe(native, func() (string, error) { return whisperlocal.Transcribe(pcm) })
		if err != nil || !strings.Contains(text, "how are you") {
			t.Fatalf("%s transcription: %q, %v", engine.Config().Service, text, err)
		}
		t.Logf("%s: %s", engine.Config().Service, text)
	}
	transcribe()
	for _, selection := range []models.Selection{{Provider: "whisper.cpp", Language: "en-US", Model: "tiny.en"}, {Provider: "vosk", Language: "en-US"}} {
		if err := engine.Start(selection); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Minute)
		for engine.Status().State == "working" && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if status := engine.Status(); status.State != "success" {
			t.Fatalf("switch failed: %+v", status)
		}
		if selection.Provider == "whisper.cpp" && modelLoaded {
			t.Fatal("inactive Vosk model was not released")
		}
		transcribe()
	}
}
