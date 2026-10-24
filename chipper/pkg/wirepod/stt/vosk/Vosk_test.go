package wirepod_vosk

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/localization"
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
