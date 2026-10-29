// Package engine coordinates model installation and live recognition changes.
package engine

import (
	"fmt"
	"sync"

	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/stt/models"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/stt/whisperlocal"
)

var mu sync.RWMutex // switching waits for active utterances to finish
var nativeName string
var nativeInit func() error
var ready, usingLocalWhisper bool
var cleanup = map[string]func(){}

func RegisterCleanup(name string, close func()) { cleanup[name] = close }

type Provider struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Available   bool   `json:"available"`
	Description string `json:"description"`
}
type Job struct {
	State     string           `json:"state"`
	Message   string           `json:"message"`
	Selection models.Selection `json:"selection"`
}

var jobMu sync.Mutex
var job = Job{State: "idle"}

// Configure is called once, including when startup model loading fails, so the
// settings page can still manage the saved selection after a startup failure.
func Configure(name string, init func() error) {
	nativeName, nativeInit = name, init
}

func Providers() []Provider {
	ok, description := models.WhisperInstallSupport()
	if nativeName == "whisper.cpp" {
		ok, description = true, "Local Whisper is included in this build."
	}
	if nativeName != "vosk" && nativeName != "whisper.cpp" {
		ok, description = false, "Model switching requires a Vosk or Whisper build of Rocket Pod."
	}
	voskDescription := "Fast offline recognition. Try English balanced before the large models."
	if nativeName != "vosk" {
		voskDescription = "Vosk requires a Vosk build of Rocket Pod. The Docker image includes Vosk and local Whisper."
	}
	return []Provider{{"vosk", "Vosk (local)", nativeName == "vosk", voskDescription}, {"whisper.cpp", "Whisper (local)", ok, description}}
}

func Status() Job            { jobMu.Lock(); defer jobMu.Unlock(); return job }
func report(message string)  { jobMu.Lock(); job.Message = message; jobMu.Unlock() }
func Config() vars.STTConfig { mu.RLock(); defer mu.RUnlock(); return vars.APIConfig.STT }

func Start(selection models.Selection) error {
	model, err := models.Resolve(selection)
	if err != nil {
		return err
	}
	available := false
	for _, provider := range Providers() {
		if provider.ID == selection.Provider {
			if !provider.Available {
				return fmt.Errorf("%s", provider.Description)
			}
			available = true
		}
	}
	if !available {
		return fmt.Errorf("unsupported speech engine")
	}
	selection.Model = model.ID
	jobMu.Lock()
	if job.State == "working" {
		jobMu.Unlock()
		return fmt.Errorf("a speech model change is already in progress")
	}
	job = Job{State: "working", Message: "Preparing " + model.Name, Selection: selection}
	jobMu.Unlock()
	go func() {
		err := prepare(selection, model)
		jobMu.Lock()
		defer jobMu.Unlock()
		if err != nil {
			job.State, job.Message = "error", err.Error()
			logger.Println("Speech model change failed: " + err.Error())
		} else {
			job.State, job.Message = "success", "Now using "+model.Name+". Ready for voice commands."
		}
	}()
	return nil
}

func prepare(selection models.Selection, model models.Model) error {
	if selection.Provider == "whisper.cpp" && nativeName != "whisper.cpp" {
		if err := models.EnsureWhisperRuntime(report); err != nil {
			return err
		}
	}
	if err := models.Ensure(model, report); err != nil {
		return err
	}
	report("Loading model; waiting for any active voice command to finish")
	mu.Lock()
	defer mu.Unlock()
	previous, previousSetup, previousIntents := vars.APIConfig.STT, vars.APIConfig.PastInitialSetup, vars.IntentList
	previousReady, previousLocal := ready, usingLocalWhisper
	vars.APIConfig.STT.Service, vars.APIConfig.STT.Language = selection.Provider, selection.Language
	if selection.Provider == "vosk" {
		vars.APIConfig.STT.VoskModel = model.ID
	} else {
		vars.APIConfig.STT.WhisperModel = model.ID
	}
	vars.APIConfig.PastInitialSetup = true
	vars.IntentList, _ = vars.LoadIntents()
	err := load()
	if err == nil {
		err = vars.WriteConfigToDiskWithError()
	}
	if err != nil {
		vars.APIConfig.STT, vars.APIConfig.PastInitialSetup, vars.IntentList = previous, previousSetup, previousIntents
		ready, usingLocalWhisper = previousReady, previousLocal
		if previousSetup {
			if restoreErr := load(); restoreErr != nil {
				return fmt.Errorf("%v; restoring the previous engine also failed: %w", err, restoreErr)
			}
		}
		if !previousLocal {
			whisperlocal.Close()
		}
		return err
	}
	if selection.Provider != "whisper.cpp" {
		whisperlocal.Close()
	}
	if selection.Provider != nativeName && cleanup[nativeName] != nil {
		cleanup[nativeName]()
	}
	logger.Println("Speech recognition switched to " + selection.Provider + ": " + model.ID)
	return nil
}

func load() error {
	config := vars.APIConfig.STT
	if config.Service == nativeName {
		if err := nativeInit(); err != nil {
			return err
		}
		ready, usingLocalWhisper = vars.APIConfig.PastInitialSetup, false
		return nil
	}
	if config.Service != "whisper.cpp" {
		return fmt.Errorf("this build does not include %s", config.Service)
	}
	if !vars.APIConfig.PastInitialSetup {
		return nil
	}
	model, err := models.Resolve(models.Current(config))
	if err != nil {
		return err
	}
	if err := models.EnsureWhisperRuntime(nil); err != nil {
		return err
	}
	if err := models.Ensure(model, nil); err != nil {
		return err
	}
	if err := whisperlocal.Init(models.Path(model), config.Language); err != nil {
		return err
	}
	ready, usingLocalWhisper = true, true
	return nil
}

func Reload() error { mu.Lock(); defer mu.Unlock(); return load() }

func Transcribe(native, localWhisper func() (string, error)) (string, error) {
	mu.RLock()
	defer mu.RUnlock()
	if !ready {
		return "", fmt.Errorf("choose a speech recognition model in Server Settings")
	}
	if usingLocalWhisper {
		return localWhisper()
	}
	return native()
}
