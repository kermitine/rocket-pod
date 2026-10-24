package localization

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

func isolateVoskDownloads(t *testing.T) {
	t.Helper()
	previousPath, previousURL, previousConfig := vars.VoskModelPath, URLPrefix, vars.APIConfig
	previousModels, previousInit, previousIntents := vars.DownloadedVoskModels, vars.SttInitFunc, vars.IntentList
	previousConfigPath := vars.ApiConfigPath
	root := t.TempDir()
	vars.VoskModelPath = filepath.Join(root, "vosk", "models")
	vars.ApiConfigPath = filepath.Join(root, "apiConfig.json")
	vars.DownloadedVoskModels = nil
	setModelDownloadStatus("not downloading")
	t.Cleanup(func() {
		vars.VoskModelPath, URLPrefix, vars.APIConfig = previousPath, previousURL, previousConfig
		vars.DownloadedVoskModels, vars.SttInitFunc, vars.IntentList = previousModels, previousInit, previousIntents
		vars.ApiConfigPath = previousConfigPath
		setModelDownloadStatus("not downloading")
	})
}

func modelZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range entries {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func smallModelZip(t *testing.T, language string) []byte {
	t.Helper()
	root := strings.TrimSuffix(voskModelArchives[language], ".zip")
	return modelZip(t, map[string]string{root + "/am/final.mdl": "acoustic data", root + "/conf/model.conf": "model config"})
}

func TestEnsureVoskModelRecoversDeletedFolderWithoutChangingConfiguration(t *testing.T) {
	isolateVoskDownloads(t)
	vars.APIConfig.STT.Language = "fr-FR"
	vars.APIConfig.PastInitialSetup = true
	vars.SttInitFunc = func() error { t.Fatal("startup model recovery recursively reloaded Vosk"); return nil }
	archive := smallModelZip(t, "fr-FR")
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		if r.URL.Path != "/"+voskModelArchives["fr-FR"] {
			t.Errorf("download selected the wrong language: %s", r.URL.Path)
		}
		w.Write(archive)
	}))
	defer server.Close()
	URLPrefix = server.URL + "/"
	for redeploy := 0; redeploy < 2; redeploy++ {
		if err := EnsureVoskModel("fr-FR"); err != nil {
			t.Fatal(err)
		}
		if !VoskModelExists("fr-FR") || len(vars.DownloadedVoskModels) != 1 || vars.DownloadedVoskModels[0] != "fr-FR" {
			t.Fatalf("model was not restored: %v", vars.DownloadedVoskModels)
		}
		if err := EnsureVoskModel("fr-FR"); err != nil {
			t.Fatal(err)
		}
		if atomic.LoadInt32(&requests) != int32(redeploy+1) {
			t.Fatal("an installed model was downloaded again")
		}
		if redeploy == 0 {
			// This path is the isolated test model root created above.
			if err := os.RemoveAll(vars.VoskModelPath); err != nil {
				t.Fatal(err)
			}
		}
	}
	if vars.APIConfig.STT.Language != "fr-FR" || !vars.APIConfig.PastInitialSetup {
		t.Fatal("recovery changed the language or initial setup flag")
	}
	if _, err := os.Stat(vars.ApiConfigPath); !os.IsNotExist(err) {
		t.Fatal("startup model recovery rewrote the configuration")
	}
}

func TestEnsureVoskModelPreservesLegacyInstalledModel(t *testing.T) {
	isolateVoskDownloads(t)
	path := filepath.Join(vars.VoskModelPath, "en-US", "model")
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"final.mdl", "mfcc.conf"} {
		if err := os.WriteFile(filepath.Join(path, name), []byte("custom model"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	URLPrefix = "http://127.0.0.1:1/should-not-download/"
	if err := EnsureVoskModel("en-US"); err != nil {
		t.Fatal(err)
	}
}

func TestVoskDownloadFailuresPreserveFilesAndCanRetry(t *testing.T) {
	for _, failure := range []string{"http-404", "invalid-zip", "missing-model", "unsafe-path"} {
		t.Run(failure, func(t *testing.T) {
			isolateVoskDownloads(t)
			oldPath := filepath.Join(vars.VoskModelPath, "en-US", "model", "keep.txt")
			if err := os.MkdirAll(filepath.Dir(oldPath), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(oldPath, []byte("preserve"), 0644); err != nil {
				t.Fatal(err)
			}
			var retry int32
			valid := smallModelZip(t, "en-US")
			missing := modelZip(t, map[string]string{"not-a-model/readme": "incomplete"})
			unsafe := modelZip(t, map[string]string{"../escaped": "outside staging"})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if atomic.LoadInt32(&retry) != 0 {
					w.Write(valid)
					return
				}
				switch failure {
				case "http-404":
					http.Error(w, "not found", http.StatusNotFound)
				case "invalid-zip":
					fmt.Fprint(w, "not a zip file")
				case "missing-model":
					w.Write(missing)
				case "unsafe-path":
					w.Write(unsafe)
				}
			}))
			defer server.Close()
			URLPrefix = server.URL + "/"
			if err := EnsureVoskModel("en-US"); err == nil {
				t.Fatal("broken download was accepted")
			}
			if status := PollDownloadStatus(); !strings.HasPrefix(status, "error:") {
				t.Fatalf("download failure reported as %q", status)
			}
			if _, err := os.Stat(oldPath); err != nil {
				t.Fatal("failed download removed existing files")
			}
			if _, err := os.Stat(filepath.Join(vars.VoskModelPath, "en-US", "escaped")); !os.IsNotExist(err) {
				t.Fatal("archive escaped staging directory")
			}
			staging, err := filepath.Glob(filepath.Join(vars.VoskModelPath, "en-US", ".download-*"))
			if err != nil || len(staging) != 0 {
				t.Fatalf("failed download left staging files: %v, %v", staging, err)
			}
			atomic.StoreInt32(&retry, 1)
			if err := EnsureVoskModel("en-US"); err != nil {
				t.Fatal(err)
			}
			if !VoskModelExists("en-US") {
				t.Fatal("retry did not install the model")
			}
		})
	}
}

func TestLanguageDownloadDoesNotClaimSuccessAfterDownloadOrReloadFailure(t *testing.T) {
	isolateVoskDownloads(t)
	vars.APIConfig.STT.Language = "en-US"
	var reloads int
	vars.SttInitFunc = func() error { reloads++; return fmt.Errorf("reload failed") }
	valid := smallModelZip(t, "fr-FR")
	var fail int32 = 1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&fail) != 0 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write(valid)
	}))
	defer server.Close()
	URLPrefix = server.URL + "/"
	DownloadVoskModel("fr-FR")
	if vars.APIConfig.STT.Language != "en-US" || reloads != 0 {
		t.Fatal("failed download changed language or reloaded Vosk")
	}
	if !strings.HasPrefix(PollDownloadStatus(), "error:") {
		t.Fatal("failed download reported success")
	}
	atomic.StoreInt32(&fail, 0)
	DownloadVoskModel("fr-FR")
	if reloads != 1 || !strings.Contains(PollDownloadStatus(), "reload failed") {
		t.Fatal("reload failure reported success")
	}
	vars.SttInitFunc = func() error { reloads++; return EnsureVoskModel("fr-FR") }
	DownloadVoskModel("fr-FR")
	if reloads != 2 || PollDownloadStatus() != "success" {
		t.Fatal("valid download could not complete its reload")
	}
	if PollDownloadStatus() != "not downloading" {
		t.Fatal("terminal download status was not consumed")
	}
}
