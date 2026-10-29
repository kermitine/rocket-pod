package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/stt/models"
)

func setup(t *testing.T) (models.Selection, models.Model) {
	t.Helper()
	previous, oldPath, oldModels := vars.APIConfig, vars.ApiConfigPath, vars.VoskModelPath
	oldName, oldInit, oldReady, oldLocal, oldJob := nativeName, nativeInit, ready, usingLocalWhisper, Status()
	t.Cleanup(func() {
		vars.APIConfig, vars.ApiConfigPath, vars.VoskModelPath = previous, oldPath, oldModels
		nativeName, nativeInit, ready, usingLocalWhisper = oldName, oldInit, oldReady, oldLocal
		jobMu.Lock()
		job = oldJob
		jobMu.Unlock()
	})
	root := t.TempDir()
	vars.ApiConfigPath, vars.VoskModelPath = filepath.Join(root, "config.json"), filepath.Join(root, "models")
	vars.APIConfig.STT = vars.STTConfig{Service: "vosk", Language: "en-US", WhisperModel: "small.en"}
	vars.APIConfig.PastInitialSetup = true
	ready, usingLocalWhisper = true, false
	jobMu.Lock()
	job = Job{State: "idle"}
	jobMu.Unlock()
	Configure("vosk", func() error { return nil })
	selection := models.Selection{Provider: "vosk", Language: "en-US", Model: "vosk-model-en-us-0.22-lgraph"}
	model, err := models.Resolve(selection)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"am/final.mdl", "conf/model.conf"} {
		path := filepath.Join(models.Path(model), file)
		os.MkdirAll(filepath.Dir(path), 0755)
		os.WriteFile(path, []byte("fixture"), 0644)
	}
	return selection, model
}

func waitJob(t *testing.T) Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := Status()
		if status.State != "working" {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("model job did not finish")
	return Job{}
}

func TestSwitchPersistsAndRetainsOtherEngineModel(t *testing.T) {
	selection, _ := setup(t)
	if err := Start(selection); err != nil {
		t.Fatal(err)
	}
	if job := waitJob(t); job.State != "success" {
		t.Fatalf("switch failed: %+v", job)
	}
	config := Config()
	if config.VoskModel != selection.Model || config.WhisperModel != "small.en" {
		t.Fatalf("lost model selection: %+v", config)
	}
	data, err := os.ReadFile(vars.ApiConfigPath)
	if err != nil || !strings.Contains(string(data), selection.Model) {
		t.Fatalf("selection not persisted: %s, %v", data, err)
	}
	text, err := Transcribe(func() (string, error) { return "native", nil }, func() (string, error) { return "wrong", nil })
	if text != "native" || err != nil {
		t.Fatalf("wrong backend: %s, %v", text, err)
	}
}

func TestFailedActivationRestoresSelection(t *testing.T) {
	selection, model := setup(t)
	previous := Config()
	if err := vars.WriteConfigToDiskWithError(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(vars.ApiConfigPath)
	Configure("vosk", func() error {
		if vars.APIConfig.STT.VoskModel == selection.Model {
			return errors.New("not enough RAM")
		}
		return nil
	})
	if err := prepare(selection, model); err == nil || !strings.Contains(err.Error(), "not enough RAM") {
		t.Fatalf("activation error lost: %v", err)
	}
	if Config() != previous {
		t.Fatalf("failed activation changed config: %+v", Config())
	}
	after, _ := os.ReadFile(vars.ApiConfigPath)
	if string(before) != string(after) {
		t.Fatal("failed activation changed saved config")
	}
}

func TestFailedSaveRestoresSelection(t *testing.T) {
	selection, model := setup(t)
	previous := Config()
	vars.ApiConfigPath = filepath.Join(t.TempDir(), "missing", "config.json")
	if err := prepare(selection, model); err == nil {
		t.Fatal("expected write failure")
	}
	if Config() != previous || !ready {
		t.Fatal("write failure did not restore the working engine")
	}
}

func TestSwitchWaitsForUtteranceAndRejectsDuplicateJob(t *testing.T) {
	selection, _ := setup(t)
	entered, release, utteranceDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(utteranceDone)
		Transcribe(func() (string, error) { close(entered); <-release; return "done", nil }, nil)
	}()
	<-entered
	activated := make(chan struct{}, 1)
	Configure("vosk", func() error { activated <- struct{}{}; return nil })
	if err := Start(selection); err != nil {
		close(release)
		t.Fatal(err)
	}
	if err := Start(selection); err == nil {
		close(release)
		t.Fatal("accepted a concurrent change")
	}
	select {
	case <-activated:
		close(release)
		t.Fatal("reloaded while transcribing")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-utteranceDone
	if job := waitJob(t); job.State != "success" {
		t.Fatalf("switch failed: %+v", job)
	}
}
