package models

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

func modelPaths(t *testing.T) {
	t.Helper()
	vosk, whisper, oldVoskURL, oldWhisperURL := vars.VoskModelPath, vars.WhisperModelPath, voskURL, whisperURL
	t.Cleanup(func() {
		vars.VoskModelPath, vars.WhisperModelPath, voskURL, whisperURL = vosk, whisper, oldVoskURL, oldWhisperURL
	})
	root := t.TempDir()
	vars.VoskModelPath, vars.WhisperModelPath = filepath.Join(root, "vosk"), filepath.Join(root, "whisper", "models")
}

func TestCatalogLanguageValidation(t *testing.T) {
	modelPaths(t)
	for language, id := range defaults {
		model, err := Resolve(Selection{Provider: "vosk", Language: language})
		if err != nil || model.ID != id {
			t.Fatalf("default for %s: %v, %v", language, model, err)
		}
		if _, err := Resolve(Selection{Provider: "whisper.cpp", Language: language, Model: "base"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, invalid := range []Selection{
		{"vosk", "en-US", "../../other"}, {"vosk", "de-DE", "vosk-model-en-us-0.22"},
		{"whisper.cpp", "de-DE", "base.en"}, {"whisper.cpp", "xx-XX", "base"}, {"http://example.com", "en-US", "base"},
	} {
		if _, err := Resolve(invalid); err == nil {
			t.Fatalf("accepted invalid selection: %+v", invalid)
		}
	}
}

func TestExistingLegacyModelIsReused(t *testing.T) {
	modelPaths(t)
	path := filepath.Join(vars.VoskModelPath, "en-US", "model")
	for _, file := range []string{"am/final.mdl", "conf/model.conf"} {
		name := filepath.Join(path, file)
		os.MkdirAll(filepath.Dir(name), 0755)
		os.WriteFile(name, []byte("existing"), 0644)
	}
	model, _ := Resolve(Selection{Provider: "vosk", Language: "en-US"})
	voskURL = "http://127.0.0.1:1/"
	if !model.Installed || Path(model) != path {
		t.Fatal("legacy model was not detected")
	}
	if err := Ensure(model, nil); err != nil {
		t.Fatalf("tried to download an installed model: %v", err)
	}
}

func archive(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for name, contents := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		entry.Write([]byte(contents))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestVoskInstallRetryAndArchiveValidation(t *testing.T) {
	modelPaths(t)
	model, _ := Resolve(Selection{"vosk", "en-US", "vosk-model-en-us-0.22-lgraph"})
	response := archive(t, map[string]string{"../escape": "bad"})
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/"+model.ID+".zip" {
			t.Errorf("unexpected URL: %s", r.URL.Path)
		}
		w.Write(response)
	}))
	defer server.Close()
	voskURL = server.URL + "/"
	if err := Ensure(model, nil); err == nil {
		t.Fatal("accepted path traversal")
	}
	if Installed(model) {
		t.Fatal("failed download appeared installed")
	}
	response = archive(t, map[string]string{model.ID + "/am/final.mdl": "model", model.ID + "/conf/model.conf": "config"})
	if err := Ensure(model, nil); err != nil {
		t.Fatal(err)
	}
	if !Installed(model) {
		t.Fatal("model was not installed")
	}
	if err := Ensure(model, nil); err != nil || requests != 2 {
		t.Fatalf("installed model was downloaded again: %d, %v", requests, err)
	}
	files, _ := os.ReadDir(filepath.Dir(Path(model)))
	for _, file := range files {
		if strings.HasPrefix(file.Name(), ".install-") {
			t.Fatal("staging files were not cleaned up")
		}
	}
}

func TestWhisperChecksumAndFailedDownload(t *testing.T) {
	modelPaths(t)
	model, _ := Resolve(Selection{"whisper.cpp", "en-US", "base.en"})
	data := append([]byte("lmgg"), bytes.Repeat([]byte{0}, 1024*1024)...)
	checksum := fmt.Sprintf("%x", sha1.Sum(data))
	fail := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write(data)
	}))
	defer server.Close()
	whisperURL = server.URL + "/"
	if err := Ensure(model, nil); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("HTTP failure not reported: %v", err)
	}
	fail = false
	if err := Ensure(model, nil); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("bad checksum not reported: %v", err)
	}
	if Installed(model) {
		t.Fatal("unverified file was installed")
	}
	model.SHA1 = checksum
	if err := Ensure(model, nil); err != nil {
		t.Fatal(err)
	}
	if !Installed(model) {
		t.Fatal("verified Whisper model was not installed")
	}
}

func TestWhisperRuntimeInstallationIntegration(t *testing.T) {
	if os.Getenv("ROCKET_POD_TEST_INSTALL_WHISPER_RUNTIME") != "1" {
		t.Skip("set ROCKET_POD_TEST_INSTALL_WHISPER_RUNTIME=1 to build the runtime")
	}
	modelPaths(t)
	t.Setenv("WHISPER_SERVER", "")
	if err := EnsureWhisperRuntime(func(message string) { t.Log(message) }); err != nil {
		t.Fatal(err)
	}
	path, err := WhisperBinary()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(path, "--help").CombinedOutput(); err != nil {
		t.Fatalf("installed runtime does not run after moving out of staging: %v: %s", err, output)
	}
}

func TestWhisperInstallSupportRequiresGitForSourceBuild(t *testing.T) {
	modelPaths(t)
	t.Setenv("WHISPER_SERVER", "")
	toolsDir := t.TempDir()
	t.Setenv("PATH", toolsDir)
	if runtime.GOOS == "windows" {
		t.Setenv("PATHEXT", ".EXE")
	}
	addTool := func(name string) {
		t.Helper()
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if err := os.WriteFile(filepath.Join(toolsDir, name), []byte("stub"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	addTool("cmake")
	addTool("c++")
	if ok, reason := WhisperInstallSupport(); ok || !strings.Contains(reason, "Git") {
		t.Fatalf("source build without Git was accepted: available=%v, reason=%q", ok, reason)
	}
	addTool("git")
	if ok, reason := WhisperInstallSupport(); !ok {
		t.Fatalf("source build dependencies were rejected: %s", reason)
	}
}

func TestInstalledWhisperRuntimeDoesNotRequireBuildTools(t *testing.T) {
	modelPaths(t)
	t.Setenv("WHISPER_SERVER", "")
	toolsDir := t.TempDir()
	t.Setenv("PATH", toolsDir)
	name := "whisper-server"
	if runtime.GOOS == "windows" {
		name += ".exe"
		t.Setenv("PATHEXT", ".EXE")
	}
	if err := os.WriteFile(filepath.Join(toolsDir, name), []byte("stub"), 0755); err != nil {
		t.Fatal(err)
	}
	if ok, reason := WhisperInstallSupport(); !ok {
		t.Fatalf("installed runtime incorrectly requires build dependencies: %s", reason)
	}
}
