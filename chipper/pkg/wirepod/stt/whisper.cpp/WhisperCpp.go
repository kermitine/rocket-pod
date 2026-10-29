package wirepod_whispercpp

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"

	whisper "github.com/kercre123/whisper.cpp/bindings/go"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	sr "github.com/kercre123/wire-pod/chipper/pkg/wirepod/speechrequest"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/stt/models"
)

var Name string = "whisper.cpp"

var context *whisper.Context
var params whisper.Params
var recognitionMu sync.Mutex

func padPCM(data []byte) []byte {
	const sampleRate = 16000
	const minDurationMs = 1020
	const minDurationSamples = sampleRate * minDurationMs / 1000
	const bytesPerSample = 2

	currentSamples := len(data) / bytesPerSample

	if currentSamples >= minDurationSamples {
		return data
	}

	logger.Println("Padding audio data to be 1000ms")

	paddingSamples := minDurationSamples - currentSamples
	paddingBytes := make([]byte, paddingSamples*bytesPerSample)

	return append(data, paddingBytes...)
}

func Init() error {
	if !vars.APIConfig.PastInitialSetup {
		return nil
	}
	selected, err := models.Resolve(models.Current(vars.APIConfig.STT))
	if err != nil {
		return err
	}
	if err := models.Ensure(selected, nil); err != nil {
		return err
	}
	var sttLanguage string
	if len(vars.APIConfig.STT.Language) == 0 {
		sttLanguage = "en"
	} else {
		sttLanguage = strings.Split(vars.APIConfig.STT.Language, "-")[0]
	}
	if sttLanguage == "nt" {
		sttLanguage = "nl"
	}

	modelPath := models.Path(selected)
	if _, err := os.Stat(modelPath); err != nil {
		logger.Println("Model does not exist: " + modelPath)
		return err
	}
	logger.Println("Opening Whisper model (" + modelPath + ")")
	//logger.Println(whisper.Whisper_print_system_info())
	next := whisper.Whisper_init(modelPath)
	if next == nil {
		return fmt.Errorf("Whisper could not load %s; check model files and available RAM", modelPath)
	}
	if context != nil {
		context.Whisper_free()
	}
	context = next
	params = context.Whisper_full_default_params(whisper.SamplingStrategy(whisper.SAMPLING_GREEDY))
	params.SetTranslate(false)
	params.SetPrintSpecial(false)
	params.SetPrintProgress(false)
	params.SetPrintRealtime(false)
	params.SetPrintTimestamps(false)
	params.SetThreads(runtime.NumCPU())
	params.SetNoContext(true)
	params.SetSingleSegment(true)
	params.SetLanguage(context.Whisper_lang_id(sttLanguage))
	return nil
}

func STT(req sr.SpeechRequest) (string, error) {
	recognitionMu.Lock()
	defer recognitionMu.Unlock()
	logger.Println("(Bot " + req.Device + ", Whisper) Processing...")
	speechIsDone := false
	var err error
	for {
		_, err = req.GetNextStreamChunk()
		if err != nil {
			return "", err
		}
		// has to be split into 320 []byte chunks for VAD
		speechIsDone, _ = req.DetectEndOfSpeech()
		if speechIsDone {
			break
		}
	}
	transcribedText, err := process(BytesToFloat32Buffer(padPCM(req.DecodedMicData)))
	if err != nil {
		return "", err
	}
	transcribedText = strings.ToLower(transcribedText)
	logger.Println("Bot " + req.Device + " Transcribed text: " + transcribedText)
	return transcribedText, nil
}

func process(data []float32) (string, error) {
	var transcribedText string
	context.Whisper_full(params, data, nil, func(_ int) {
		transcribedText = strings.TrimSpace(context.Whisper_full_get_segment_text(0))
	}, nil)
	return transcribedText, nil
}

func BytesToFloat32Buffer(buf []byte) []float32 {
	newB := make([]float32, len(buf)/2)
	factor := math.Pow(2, float64(16)-1)
	for i := 0; i < len(buf)/2; i++ {
		newB[i] = float32(float64(int16(binary.LittleEndian.Uint16(buf[i*2:]))) / factor)
	}
	return newB
}
