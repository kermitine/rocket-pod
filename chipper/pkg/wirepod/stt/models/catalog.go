// Package models manages trusted, local speech recognition model downloads.
package models

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

type Model struct {
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	Language    string `json:"language"` // empty means multilingual
	Name        string `json:"name"`
	Size        string `json:"size"`
	Description string `json:"description"`
	Installed   bool   `json:"installed"`
	SHA1        string `json:"-"`
}

type Selection struct {
	Provider string `json:"provider"`
	Language string `json:"language"`
	Model    string `json:"model"`
}

var defaults = map[string]string{
	"en-US": "vosk-model-small-en-us-0.15", "it-IT": "vosk-model-small-it-0.22",
	"es-ES": "vosk-model-small-es-0.42", "fr-FR": "vosk-model-small-fr-0.22",
	"de-DE": "vosk-model-small-de-0.15", "pt-BR": "vosk-model-small-pt-0.3",
	"pl-PL": "vosk-model-small-pl-0.22", "zh-CN": "vosk-model-small-cn-0.22",
	"tr-TR": "vosk-model-small-tr-0.3", "ru-RU": "vosk-model-small-ru-0.22",
	"nt-NL": "vosk-model-small-nl-0.22", "uk-UA": "vosk-model-small-uk-v3-small",
	"vi-VN": "vosk-model-small-vn-0.4", "ko-KR": "vosk-model-small-ko-0.22",
}

// Sources: alphacephei.com/vosk/models and huggingface.co/ggerganov/whisper.cpp.
func Catalog() []Model {
	var result []Model
	for _, language := range []string{"en-US", "it-IT", "es-ES", "fr-FR", "de-DE", "pt-BR", "pl-PL", "tr-TR", "ko-KR", "zh-CN", "ru-RU", "nt-NL", "uk-UA", "vi-VN"} {
		result = append(result, Model{ID: defaults[language], Provider: "vosk", Language: language,
			Name: "Vosk small (" + language + ")", Size: "~30–100 MB", Description: "Fast, low memory use. A good starting point for small hosts."})
	}
	result = append(result,
		Model{ID: "vosk-model-en-us-0.22-lgraph", Provider: "vosk", Language: "en-US", Name: "Vosk English balanced", Size: "128 MB", Description: "Try this first for better English accuracy with modest resource use."},
		Model{ID: "vosk-model-en-us-0.22", Provider: "vosk", Language: "en-US", Name: "Vosk English large", Size: "1.8 GB", Description: "Higher accuracy; needs several GB of RAM. Use on a capable desktop/server."},
		Model{ID: "vosk-model-en-us-0.42-gigaspeech", Provider: "vosk", Language: "en-US", Name: "Vosk English GigaSpeech", Size: "2.3 GB", Description: "Large general English model. High memory use; try balanced first."},
	)
	for _, item := range []struct{ id, size, sha string }{
		{"tiny", "75 MiB", "bd577a113a864445d4c299885e0cb97d4ba92b5f"},
		{"tiny.en", "75 MiB", "c78c86eb1a8faa21b369bcd33207cc90d64ae9df"},
		{"base", "142 MiB", "465707469ff3a37a2b9b8d8f89f2f99de7299dac"},
		{"base.en", "142 MiB", "137c40403d78fd54d454da0f9bd998f78703390c"},
		{"small", "466 MiB", "55356645c2b361a969dfd0ef2c5a50d530afd8d5"},
		{"small.en", "466 MiB", "db8a495a91d927739e50b3fc1cc4c6b8f6c2d022"},
		{"medium", "1.5 GiB", "fd9727b6e1217c2f614f9b698455c4ffd82463b4"},
		{"medium.en", "1.5 GiB", "8c30f0e44ce9560643ebd10bbe50cd20eafd3723"},
	} {
		language, description := "", "Multilingual. "
		if strings.HasSuffix(item.id, ".en") {
			language, description = "en-US", "English only. "
		}
		switch strings.Split(item.id, ".")[0] {
		case "tiny":
			description += "Fastest Whisper option; lowest memory use."
		case "base":
			description += "Start here to compare Whisper with Vosk."
		case "small":
			description += "Try for better accuracy on a desktop; slower than base."
		case "medium":
			description += "High CPU and memory use; slower responses on small hosts."
		}
		result = append(result, Model{ID: item.id, Provider: "whisper.cpp", Language: language, Name: "Whisper " + item.id, Size: item.size, Description: description, SHA1: item.sha})
	}
	for i := range result {
		result[i].Installed = Installed(result[i])
	}
	return result
}

func Resolve(s Selection) (Model, error) {
	if _, ok := defaults[s.Language]; !ok {
		return Model{}, fmt.Errorf("unsupported speech language %q", s.Language)
	}
	if s.Model == "" {
		if s.Provider == "vosk" {
			s.Model = defaults[s.Language]
		} else {
			s.Model = "base"
		}
	}
	for _, model := range Catalog() {
		if model.Provider == s.Provider && model.ID == s.Model {
			if model.Language != "" && model.Language != s.Language {
				return Model{}, fmt.Errorf("%s does not support %s", model.Name, s.Language)
			}
			return model, nil
		}
	}
	return Model{}, fmt.Errorf("unknown speech engine or model")
}

func Current(config vars.STTConfig) Selection {
	s := Selection{Provider: config.Service, Language: config.Language, Model: config.VoskModel}
	if s.Language == "" {
		s.Language = "en-US"
	}
	if s.Provider == "whisper.cpp" {
		s.Model = config.WhisperModel
		if s.Model == "" {
			s.Model = strings.TrimSpace(os.Getenv("WHISPER_MODEL"))
		}
		if s.Model == "" {
			s.Model = "tiny"
		} // retain the previous startup default
	} else if s.Model == "" {
		s.Model = defaults[s.Language]
	}
	return s
}

func Path(model Model) string {
	if model.Provider == "whisper.cpp" {
		return filepath.Join(vars.WhisperModelPath, "ggml-"+model.ID+".bin")
	}
	if defaults[model.Language] == model.ID {
		legacy := filepath.Join(vars.VoskModelPath, model.Language, "model")
		if VoskFilesExist(legacy) {
			return legacy
		}
	}
	return filepath.Join(vars.VoskModelPath, model.ID, "model")
}

func VoskFilesExist(path string) bool {
	file := func(name string) bool {
		i, e := os.Stat(filepath.Join(path, name))
		return e == nil && i.Mode().IsRegular() && i.Size() > 0
	}
	return (file("am/final.mdl") && file("conf/model.conf")) || (file("final.mdl") && file("mfcc.conf"))
}

func Installed(model Model) bool {
	if model.Provider == "vosk" {
		return VoskFilesExist(Path(model))
	}
	file, err := os.Open(Path(model))
	if err != nil {
		return false
	}
	defer file.Close()
	var magic [4]byte
	_, err = file.Read(magic[:])
	info, statErr := file.Stat()
	return err == nil && string(magic[:]) == "lmgg" && statErr == nil && info.Size() > 1024*1024
}
