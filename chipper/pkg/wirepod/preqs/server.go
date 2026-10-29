package processreqs

import (
	"fmt"

	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	sr "github.com/kercre123/wire-pod/chipper/pkg/wirepod/speechrequest"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/stt/engine"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/stt/whisperlocal"
	ttr "github.com/kercre123/wire-pod/chipper/pkg/wirepod/ttr"
)

// Server stores the config
type Server struct{}

var VoiceProcessor = ""

type JsonIntent struct {
	Name              string   `json:"name"`
	Keyphrases        []string `json:"keyphrases"`
	RequireExactMatch bool     `json:"requiresexact"`
}

var sttLanguage string = "en-US"

// speech-to-text
var sttHandler func(sr.SpeechRequest) (string, error)

// speech-to-intent (rhino)
var stiHandler func(sr.SpeechRequest) (string, map[string]string, error)

var isSti bool = false

func ReloadVosk() {
	if vars.APIConfig.STT.Service == "vosk" || vars.APIConfig.STT.Service == "whisper.cpp" {
		vars.SttInitFunc()
		vars.IntentList, _ = vars.LoadIntents()
	}
}

// New returns a new server
func New(InitFunc func() error, SttHandler interface{}, voiceProcessor string) (*Server, error) {

	// Decide the TTS language
	if voiceProcessor != "vosk" && voiceProcessor != "whisper.cpp" && voiceProcessor != "whisper" {
		vars.APIConfig.STT.Language = "en-US"
	}
	sttLanguage = vars.APIConfig.STT.Language
	vars.IntentList, _ = vars.LoadIntents()
	logger.Println("Initiating " + voiceProcessor + " voice processor with language " + sttLanguage)
	vars.SttInitFunc = InitFunc

	// SttHandler can either be `func(sr.SpeechRequest) (string, error)` or `func (sr.SpeechRequest) (string, map[string]string, error)`
	// second one exists to accomodate Rhino

	// check function type
	if str, is := SttHandler.(func(sr.SpeechRequest) (string, error)); is {
		sttHandler = str
		if voiceProcessor == "vosk" || voiceProcessor == "whisper.cpp" {
			engine.Configure(voiceProcessor, InitFunc)
			vars.SttInitFunc = engine.Reload
			sttHandler = func(req sr.SpeechRequest) (string, error) {
				return engine.Transcribe(func() (string, error) { return str(req) }, func() (string, error) { return localWhisperSTT(req) })
			}
		}
	} else if str, is := SttHandler.(func(sr.SpeechRequest) (string, map[string]string, error)); is {
		stiHandler = str
		isSti = true
	} else {
		return nil, fmt.Errorf("stthandler not of correct type")
	}

	// Initiating the chosen voice processor and load intents from json
	VoiceProcessor = voiceProcessor
	err := vars.SttInitFunc()
	if err != nil {
		return nil, err
	}

	// Load plugins
	ttr.LoadPlugins()

	return &Server{}, err
}

func localWhisperSTT(req sr.SpeechRequest) (string, error) {
	logger.Println("(Bot " + req.Device + ", local Whisper) Processing...")
	for {
		if _, err := req.GetNextStreamChunk(); err != nil {
			return "", err
		}
		if done, _ := req.DetectEndOfSpeech(); done {
			break
		}
	}
	text, err := whisperlocal.Transcribe(req.DecodedMicData)
	if err == nil {
		logger.Println("Bot " + req.Device + " Transcribed text: " + text)
	}
	return text, err
}
