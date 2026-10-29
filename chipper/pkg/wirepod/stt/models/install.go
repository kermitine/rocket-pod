package models

import (
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/localization"
)

var installMu sync.Mutex
var downloadClient = &http.Client{Timeout: 60 * time.Minute}
var voskURL = "https://alphacephei.com/vosk/models/"
var whisperURL = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/"

type progressWriter struct {
	done, total int64
	last        time.Time
	report      func(string)
}

func (p *progressWriter) Write(data []byte) (int, error) {
	p.done += int64(len(data))
	if time.Since(p.last) > time.Second {
		message := fmt.Sprintf("Downloading: %d MiB", p.done/(1024*1024))
		if p.total > 0 {
			message = fmt.Sprintf("Downloading: %d%% (%d MiB)", p.done*100/p.total, p.done/(1024*1024))
		}
		p.report(message)
		p.last = time.Now()
	}
	return len(data), nil
}

func download(url, destination, checksum string, report func(string)) error {
	response, err := downloadClient.Get(url)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: HTTP %d", response.StatusCode)
	}
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	hash := sha1.New()
	// Bound downloads even if a server omits Content-Length.
	const maxDownload = int64(5 * 1024 * 1024 * 1024)
	n, copyErr := io.Copy(io.MultiWriter(file, hash, &progressWriter{total: response.ContentLength, report: report}), io.LimitReader(response.Body, maxDownload+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n > maxDownload {
		return fmt.Errorf("download exceeded 5 GiB")
	}
	if response.ContentLength >= 0 && n != response.ContentLength {
		return fmt.Errorf("incomplete model download")
	}
	if checksum != "" && fmt.Sprintf("%x", hash.Sum(nil)) != checksum {
		return fmt.Errorf("model checksum mismatch; retry the download")
	}
	return nil
}

// Ensure stages and validates files before installing. Interrupted downloads
// never appear as installed and never replace a working model.
func Ensure(model Model, report func(string)) error {
	installMu.Lock()
	defer installMu.Unlock()
	if report == nil {
		report = func(string) {}
	}
	if Installed(model) {
		return nil
	}
	destination := Path(model)
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".install-")
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			os.RemoveAll(stage)
		}
	}()
	source := filepath.Join(stage, "download")
	report("Downloading " + model.Name + " (" + model.Size + ")")
	if model.Provider == "vosk" {
		if err := download(voskURL+model.ID+".zip", source, "", report); err != nil {
			return err
		}
		report("Unpacking " + model.Name)
		if err := localization.ExtractModelArchive(source, stage); err != nil {
			return err
		}
		source = filepath.Join(stage, model.ID)
		if !VoskFilesExist(source) {
			return fmt.Errorf("archive does not contain a complete Vosk model")
		}
	} else {
		if err := download(whisperURL+"ggml-"+model.ID+".bin", source, model.SHA1, report); err != nil {
			return err
		}
	}
	// An incomplete destination can remain from an older/manual install.
	backup := filepath.Join(stage, "previous")
	hadPrevious := false
	if _, err := os.Lstat(destination); err == nil {
		if err := os.Rename(destination, backup); err != nil {
			return err
		}
		hadPrevious = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(source, destination); err != nil {
		if hadPrevious {
			if restoreErr := os.Rename(backup, destination); restoreErr != nil {
				cleanup = false
				return fmt.Errorf("install: %v; restoring previous files failed: %v; backup retained at %s", err, restoreErr, backup)
			}
		}
		return err
	}
	return nil
}

func WhisperBinary() (string, error) {
	if path := os.Getenv("WHISPER_SERVER"); path != "" {
		resolved, err := exec.LookPath(path)
		if err != nil {
			return "", fmt.Errorf("WHISPER_SERVER: %w", err)
		}
		return filepath.Abs(resolved)
	}
	for _, path := range []string{"whisper-server", filepath.Join(filepath.Dir(vars.WhisperModelPath), "rocket-runtime", "build", "bin", "whisper-server"), filepath.Join(filepath.Dir(vars.WhisperModelPath), "build", "bin", "whisper-server")} {
		if runtime.GOOS == "windows" && path != "whisper-server" {
			path += ".exe"
		}
		if resolved, err := exec.LookPath(path); err == nil {
			return filepath.Abs(resolved)
		}
	}
	return "", fmt.Errorf("local Whisper runtime is not installed")
}

func WhisperInstallSupport() (bool, string) {
	if _, err := WhisperBinary(); err == nil {
		return true, "Local Whisper runtime is ready."
	}
	if os.Getenv("WHISPER_SERVER") != "" {
		return false, "WHISPER_SERVER must point to a working whisper-server executable."
	}
	for _, command := range []string{"cmake", "c++"} {
		if _, err := exec.LookPath(command); err != nil {
			return false, "Automatic Whisper setup needs CMake and a C++ compiler. Docker images include the runtime; on Linux install cmake and build-essential, or set WHISPER_SERVER to an existing whisper-server."
		}
	}
	return true, "The first selection also builds the local Whisper runtime. This may take several minutes."
}

// EnsureWhisperRuntime builds a pinned release without changing system packages.
// Docker ships this executable, so containers only need to download models.
func EnsureWhisperRuntime(report func(string)) error {
	if _, err := WhisperBinary(); err == nil {
		return nil
	}
	if ok, reason := WhisperInstallSupport(); !ok {
		return fmt.Errorf("%s", reason)
	}
	if report == nil {
		report = func(string) {}
	}
	parent := filepath.Dir(vars.WhisperModelPath)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".runtime-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	report("Downloading local Whisper runtime source (v1.7.6)")
	archive := filepath.Join(stage, "source.zip")
	if err := download("https://codeload.github.com/ggml-org/whisper.cpp/zip/refs/tags/v1.7.6", archive, "", report); err != nil {
		return err
	}
	if err := localization.ExtractModelArchive(archive, stage); err != nil {
		return err
	}
	source := filepath.Join(stage, "whisper.cpp-1.7.6")
	build := filepath.Join(source, "build")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	for _, args := range [][]string{
		{"-S", source, "-B", build, "-DCMAKE_BUILD_TYPE=Release", "-DBUILD_SHARED_LIBS=OFF", "-DGGML_NATIVE=OFF", "-DGGML_OPENMP=OFF", "-DGGML_METAL=OFF", "-DWHISPER_BUILD_TESTS=OFF", "-DWHISPER_BUILD_SERVER=ON"},
		{"--build", build, "--config", "Release", "--target", "whisper-server", "-j", "2"},
	} {
		report("Building local Whisper runtime; this may take several minutes")
		output, err := exec.CommandContext(ctx, "cmake", args...).CombinedOutput()
		if err != nil {
			message := string(output)
			if len(message) > 2000 {
				message = message[len(message)-2000:]
			}
			return fmt.Errorf("Whisper build failed: %w: %s", err, strings.TrimSpace(message))
		}
	}
	destination := filepath.Join(parent, "rocket-runtime")
	// Reuse an interrupted build directory only after replacing it with a complete build.
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("incomplete runtime exists at %s; remove it and retry", destination)
	}
	return os.Rename(source, destination)
}
