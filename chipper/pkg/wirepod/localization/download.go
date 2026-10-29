package localization

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

var URLPrefix = "https://github.com/kercre123/vosk-models/raw/main/"

var voskModelArchives = map[string]string{
	"en-US": "vosk-model-small-en-us-0.15.zip",
	"it-IT": "vosk-model-small-it-0.22.zip",
	"es-ES": "vosk-model-small-es-0.42.zip",
	"fr-FR": "vosk-model-small-fr-0.22.zip",
	"de-DE": "vosk-model-small-de-0.15.zip",
	"pt-BR": "vosk-model-small-pt-0.3.zip",
	"pl-PL": "vosk-model-small-pl-0.22.zip",
	"zh-CN": "vosk-model-small-cn-0.22.zip",
	"tr-TR": "vosk-model-small-tr-0.3.zip",
	"ru-RU": "vosk-model-small-ru-0.22.zip",
	"nt-NL": "vosk-model-small-nl-0.22.zip",
	"uk-UA": "vosk-model-small-uk-v3-small.zip",
	"vi-VN": "vosk-model-small-vn-0.4.zip",
	"ko-KR": "vosk-model-small-ko-0.22.zip",
}

var modelDownloadClient = &http.Client{Timeout: 10 * time.Minute}
var modelInstallMu sync.Mutex
var modelSelectionMu sync.Mutex
var modelDownloadStatus = struct {
	sync.Mutex
	message string
}{message: "not downloading"}

func setModelDownloadStatus(message string) {
	modelDownloadStatus.Lock()
	modelDownloadStatus.message = message
	modelDownloadStatus.Unlock()
}

// PollDownloadStatus consumes terminal statuses, matching the web UI's polling protocol.
func PollDownloadStatus() string {
	modelDownloadStatus.Lock()
	defer modelDownloadStatus.Unlock()
	status := modelDownloadStatus.message
	if status == "success" || strings.HasPrefix(status, "error:") {
		modelDownloadStatus.message = "not downloading"
	}
	return status
}

func voskModelFilesExist(path string) bool {
	nonemptyFile := func(name string) bool {
		info, err := os.Stat(filepath.Join(path, name))
		return err == nil && info.Mode().IsRegular() && info.Size() > 0
	}
	// Vosk supports both current and legacy model layouts.
	return (nonemptyFile("am/final.mdl") && nonemptyFile("conf/model.conf")) ||
		(nonemptyFile("final.mdl") && nonemptyFile("mfcc.conf"))
}

func VoskModelExists(language string) bool {
	if _, supported := voskModelArchives[language]; !supported {
		return false
	}
	return voskModelFilesExist(filepath.Join(vars.VoskModelPath, language, "model"))
}

// EnsureVoskModel restores missing model files without changing the selected
// language or reloading Vosk. Startup can call it without a recursive Init.
func EnsureVoskModel(language string) error {
	modelInstallMu.Lock()
	defer modelInstallMu.Unlock()
	if err := ensureVoskModel(language); err != nil {
		setModelDownloadStatus("error: " + err.Error())
		logger.Println("VOSK model recovery failed: " + err.Error())
		return err
	}
	vars.GetDownloadedVoskModels()
	return nil
}

func ensureVoskModel(language string) error {
	filename, supported := voskModelArchives[language]
	if !supported {
		return fmt.Errorf("unsupported Vosk model language %q", language)
	}
	if VoskModelExists(language) {
		return nil
	}
	languagePath := filepath.Join(vars.VoskModelPath, language)
	if err := os.MkdirAll(languagePath, 0755); err != nil {
		return fmt.Errorf("create Vosk model directory: %w", err)
	}
	stagingPath, err := os.MkdirTemp(languagePath, ".download-")
	if err != nil {
		return fmt.Errorf("create Vosk model staging directory: %w", err)
	}
	cleanupStaging := true
	defer func() {
		if cleanupStaging {
			os.RemoveAll(stagingPath)
		}
	}()
	archivePath := filepath.Join(stagingPath, "model.zip")
	url := URLPrefix + filename
	logger.Println("VOSK model missing or incomplete for " + language + "; downloading " + url)
	setModelDownloadStatus("Downloading VOSK model for " + language + "...")
	if err := downloadModelArchive(url, archivePath); err != nil {
		return err
	}
	setModelDownloadStatus("Unpacking model...")
	if err := extractModelArchive(archivePath, stagingPath); err != nil {
		return err
	}
	modelPath := filepath.Join(stagingPath, strings.TrimSuffix(filename, ".zip"))
	if !voskModelFilesExist(modelPath) {
		return fmt.Errorf("downloaded Vosk archive for %s does not contain model files", language)
	}
	// Stage and validate the download before replacing an incomplete model.
	destination := filepath.Join(languagePath, "model")
	backup := filepath.Join(stagingPath, "previous-model")
	hadPrevious := false
	if _, err := os.Lstat(destination); err == nil {
		if err := os.Rename(destination, backup); err != nil {
			return fmt.Errorf("move incomplete Vosk model aside: %w", err)
		}
		hadPrevious = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(modelPath, destination); err != nil {
		if hadPrevious {
			if restoreErr := os.Rename(backup, destination); restoreErr != nil {
				// Preserve the staging directory containing the only backup.
				cleanupStaging = false
				return fmt.Errorf("install Vosk model: %v; previous files remain at %s: %w", err, backup, restoreErr)
			}
		}
		return fmt.Errorf("install Vosk model: %w", err)
	}
	logger.Println("VOSK model installed for " + language + " at " + destination)
	setModelDownloadStatus("Model ready")
	return nil
}

func DownloadVoskModel(language string) {
	// Serialize language changes as well as model installs and reloads.
	modelSelectionMu.Lock()
	defer modelSelectionMu.Unlock()
	if err := EnsureVoskModel(language); err != nil {
		return
	}
	previousLanguage, previousSetup := vars.APIConfig.STT.Language, vars.APIConfig.PastInitialSetup
	vars.APIConfig.STT.Language = language
	vars.APIConfig.PastInitialSetup = true
	if err := vars.WriteConfigToDiskWithError(); err != nil {
		vars.APIConfig.STT.Language, vars.APIConfig.PastInitialSetup = previousLanguage, previousSetup
		setModelDownloadStatus("error: " + err.Error())
		return
	}
	setModelDownloadStatus("Reloading voice processor")
	vars.IntentList, _ = vars.LoadIntents()
	if vars.SttInitFunc == nil {
		setModelDownloadStatus("error: voice processor is not initialized; restart Rocket Pod")
		return
	}
	if err := vars.SttInitFunc(); err != nil {
		setModelDownloadStatus("error: unable to reload voice processor: " + err.Error())
		return
	}
	logger.Println("Reloaded voice processor successfully")
	setModelDownloadStatus("success")
}

type modelDownloadProgress struct {
	bytes, total int64
	lastUpdate   time.Time
}

func (progress *modelDownloadProgress) Write(data []byte) (int, error) {
	progress.bytes += int64(len(data))
	if time.Since(progress.lastUpdate) >= time.Second {
		if progress.total > 0 {
			setModelDownloadStatus(fmt.Sprintf("Model download status: %d%%", progress.bytes*100/progress.total))
		} else {
			setModelDownloadStatus(fmt.Sprintf("Downloading model: %d MB", progress.bytes/(1024*1024)))
		}
		progress.lastUpdate = time.Now()
	}
	return len(data), nil
}

func downloadModelArchive(url, destination string) error {
	response, err := modelDownloadClient.Get(url)
	if err != nil {
		return fmt.Errorf("download Vosk model: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download Vosk model: HTTP %d from %s", response.StatusCode, url)
	}
	file, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("create Vosk download file: %w", err)
	}
	progress := &modelDownloadProgress{total: response.ContentLength}
	_, copyErr := io.Copy(io.MultiWriter(file, progress), response.Body)
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("download Vosk model: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("save Vosk download: %w", closeErr)
	}
	return nil
}

// ExtractModelArchive unpacks a trusted model/source ZIP with path traversal and
// symlink protection. Callers must use a fresh staging directory.
func ExtractModelArchive(archive, destination string) error {
	return extractModelArchive(archive, destination)
}

func extractModelArchive(archive, destination string) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("open downloaded Vosk archive: %w", err)
	}
	defer reader.Close()
	for _, entry := range reader.File {
		path := filepath.Join(destination, entry.Name)
		relative, err := filepath.Rel(destination, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) ||
			filepath.IsAbs(entry.Name) || strings.Contains(entry.Name, "\\") || entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("invalid path in Vosk archive: %q", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(path, 0755); err != nil {
				return err
			}
			continue
		}
		if err := extractModelFile(entry, path); err != nil {
			return fmt.Errorf("unpack Vosk model: %w", err)
		}
	}
	return nil
}

func extractModelFile(entry *zip.File, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	source, err := entry.Open()
	if err != nil {
		return err
	}
	defer source.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, source)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
