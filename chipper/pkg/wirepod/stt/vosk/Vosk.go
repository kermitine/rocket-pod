package wirepod_vosk

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/localization"
	sr "github.com/kercre123/wire-pod/chipper/pkg/wirepod/speechrequest"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/stt/engine"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/stt/models"
	vosk "github.com/kercre123/wire-pod/chipper/pkg/wirepod/stt/vosk/native"
)

var GrammerEnable bool = false

var Name string = "vosk"

func init() { engine.RegisterCleanup(Name, Close) }

var model *vosk.VoskModel
var recsmu sync.Mutex

var grmRecs []ARec
var gpRecs []ARec

var modelLoaded bool

type ARec struct {
	InUse bool
	Rec   *vosk.VoskRecognizer
}

var Grammer string

func Init() error {
	if vars.APIConfig.PastInitialSetup {
		sttLanguage := vars.APIConfig.STT.Language
		if len(sttLanguage) == 0 {
			sttLanguage = "en-US"
		}
		modelPath := filepath.Join(vars.VoskModelPath, sttLanguage, "model")
		if vars.APIConfig.STT.VoskModel == "" {
			if err := localization.EnsureVoskModel(sttLanguage); err != nil {
				return fmt.Errorf("prepare Vosk model for %s: %w", sttLanguage, err)
			}
		} else {
			selected, err := models.Resolve(models.Current(vars.APIConfig.STT))
			if err != nil {
				return err
			}
			if err := models.Ensure(selected, nil); err != nil {
				return err
			}
			modelPath = models.Path(selected)
		}
		vosk.SetLogLevel(-1)
		logger.Println("Opening VOSK model (" + modelPath + ")")
		aModel, err := vosk.NewModel(modelPath)
		if err != nil {
			return err
		}
		gpRecognizer, err := vosk.NewRecognizer(aModel, 16000.0)
		if err != nil {
			aModel.Free()
			return err
		}
		grammarEnabled := os.Getenv("VOSK_WITH_GRAMMER") == "true"
		// Static-graph large models cannot accept a dynamic grammar.
		if _, err := os.Stat(filepath.Join(modelPath, "graph", "HCLr.fst")); err != nil {
			grammarEnabled = false
		}
		var nextGrammar []ARec
		grammar := ""
		if grammarEnabled {
			grammar = GetGrammerList(sttLanguage, aModel)
			grmRecognizer, err := vosk.NewRecognizerGrm(aModel, 16000.0, grammar)
			if err != nil {
				gpRecognizer.Free()
				aModel.Free()
				return err
			}
			nextGrammar = []ARec{{Rec: grmRecognizer}}
		}
		// Retain the old model until the replacement and its recognizers load.
		Close()
		model, gpRecs, grmRecs = aModel, []ARec{{Rec: gpRecognizer}}, nextGrammar
		GrammerEnable, Grammer = grammarEnabled, grammar
		modelLoaded = true
		logger.Println("VOSK initiated successfully")
		runTest()
	}
	return nil
}

// Close is called only after the engine coordinator drains active requests.
func Close() {
	for _, rec := range grmRecs {
		rec.Rec.Free()
	}
	for _, rec := range gpRecs {
		rec.Rec.Free()
	}
	if modelLoaded {
		model.Free()
	}
	grmRecs, gpRecs, model, modelLoaded = nil, nil, nil, false
}

func runTest() {
	// make sure recognizer is all loaded into RAM
	logger.Println("Running recognizer test")
	var withGrm bool
	if GrammerEnable {
		logger.Println("Using grammer-optimized recognizer")
		withGrm = true
	} else {
		logger.Println("Using general recognizer")
		withGrm = false
	}
	rec, recind, err := getRec(withGrm)
	if err != nil {
		logger.Println("Vosk warmup: " + err.Error())
		return
	}
	defer releaseRec(withGrm, recind)
	sttTestPath := "./stttest.pcm"
	if runtime.GOOS == "android" {
		sttTestPath = vars.AndroidPath + "/static/stttest.pcm"
	}
	pcmBytes, _ := os.ReadFile(sttTestPath)
	var micData [][]byte
	cTime := time.Now()
	micData = sr.SplitVAD(pcmBytes)
	for _, sample := range micData {
		rec.AcceptWaveform(sample)
	}
	var jres map[string]interface{}
	json.Unmarshal([]byte(rec.FinalResult()), &jres)
	transcribedText, _ := jres["text"].(string)
	tTime := time.Now().Sub(cTime)
	logger.Println("Text (from test):", transcribedText)
	if tTime.Seconds() > 3 {
		logger.Println("Vosk test took a while, performance may be degraded. (" + fmt.Sprint(tTime) + ")")
	}
	logger.Println("Vosk test successful! (Took " + fmt.Sprint(tTime) + ")")

}

func getRec(withGrm bool) (*vosk.VoskRecognizer, int, error) {
	recsmu.Lock()
	defer recsmu.Unlock()
	if withGrm && GrammerEnable {
		for ind, rec := range grmRecs {
			if !rec.InUse {
				grmRecs[ind].InUse = true
				return grmRecs[ind].Rec, ind, nil
			}
		}
	} else {
		for ind, rec := range gpRecs {
			if !rec.InUse {
				gpRecs[ind].InUse = true
				return gpRecs[ind].Rec, ind, nil
			}
		}
	}
	var newrec ARec
	var newRec *vosk.VoskRecognizer
	var err error
	newrec.InUse = true
	if withGrm {
		newRec, err = vosk.NewRecognizerGrm(model, 16000.0, Grammer)
	} else {
		newRec, err = vosk.NewRecognizer(model, 16000.0)
	}
	if err != nil {
		return nil, -1, err
	}
	newrec.Rec = newRec
	if withGrm {
		grmRecs = append(grmRecs, newrec)
		return grmRecs[len(grmRecs)-1].Rec, len(grmRecs) - 1, nil
	} else {
		gpRecs = append(gpRecs, newrec)
		return gpRecs[len(gpRecs)-1].Rec, len(gpRecs) - 1, nil
	}
}

func releaseRec(grammar bool, index int) {
	recsmu.Lock()
	defer recsmu.Unlock()
	if grammar {
		grmRecs[index].InUse = false
	} else {
		gpRecs[index].InUse = false
	}
}

func STT(req sr.SpeechRequest) (string, error) {
	logger.Println("(Bot " + req.Device + ", Vosk) Processing...")
	var withGrm bool
	if (vars.APIConfig.Knowledge.IntentGraph || req.IsKG) || !GrammerEnable {
		logger.Println("Using general recognizer")
		withGrm = false
	} else {
		logger.Println("Using grammer-optimized recognizer")
		withGrm = true
	}
	rec, recind, err := getRec(withGrm)
	if err != nil {
		return "", err
	}
	defer releaseRec(withGrm, recind)
	rec.SetWords(1)
	rec.AcceptWaveform(req.FirstReq)
	req.DetectEndOfSpeech()
	for {
		chunk, err := req.GetNextStreamChunk()
		if err != nil {
			rec.FinalResult() // clear partial audio before reusing this recognizer
			return "", err
		}
		speechIsDone, doProcess := req.DetectEndOfSpeech()
		if doProcess {
			rec.AcceptWaveform(chunk)
		}
		if speechIsDone {
			break
		}
	}
	var jres map[string]interface{}
	json.Unmarshal([]byte(rec.FinalResult()), &jres)
	transcribedText, _ := jres["text"].(string)
	logger.Println("Bot " + req.Device + " Transcribed text: " + transcribedText)
	return transcribedText, nil
}
