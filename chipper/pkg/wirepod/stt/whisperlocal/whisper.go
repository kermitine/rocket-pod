// Package whisperlocal runs whisper.cpp on loopback and keeps its model loaded.
package whisperlocal

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/stt/models"
)

type worker struct {
	command                       *exec.Cmd
	done                          chan error
	url, model, language, tempDir string
}

var mu sync.Mutex
var active *worker
var client = &http.Client{Timeout: 2 * time.Minute}

func stop(w *worker) {
	if w == nil {
		return
	}
	_ = w.command.Process.Kill()
	<-w.done
	_ = os.RemoveAll(w.tempDir)
}

func Close() { mu.Lock(); defer mu.Unlock(); stop(active); active = nil }

func Init(modelPath, language string) error {
	mu.Lock()
	defer mu.Unlock()
	language = strings.Split(language, "-")[0]
	if language == "nt" {
		language = "nl"
	} // preserve the app's historical Dutch locale
	modelPath, err := filepath.Abs(modelPath)
	if err != nil {
		return err
	}
	if active != nil && active.model == modelPath && active.language == language {
		select {
		case <-active.done:
			_ = os.RemoveAll(active.tempDir)
			active = nil
		default:
			return nil
		}
	}
	binaryPath, err := models.WhisperBinary()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	dir, err := os.MkdirTemp("", "rocket-pod-whisper-")
	if err != nil {
		return err
	}
	threads := runtime.NumCPU()
	if threads > 4 {
		threads = 4
	}
	command := exec.Command(binaryPath, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "-m", modelPath,
		"-l", language, "-t", strconv.Itoa(threads), "-nt", "-nc", "-sns", "--public", dir)
	command.Dir = dir // whisper-server writes temporary audio files here
	configureProcess(command)
	log, err := os.Create(filepath.Join(dir, "runtime.log"))
	if err != nil {
		os.RemoveAll(dir)
		return err
	}
	command.Stdout, command.Stderr = log, log
	w := &worker{command: command, done: make(chan error, 1), url: fmt.Sprintf("http://127.0.0.1:%d", port), model: modelPath, language: language, tempDir: dir}
	started := make(chan error)
	go func() {
		// Keep the launching thread alive: Linux ties the parent-death signal to
		// this thread, ensuring the worker exits when Rocket Pod stops.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		err := command.Start()
		started <- err
		if err != nil {
			return
		}
		err = command.Wait()
		log.Close()
		w.done <- err
	}()
	if err := <-started; err != nil {
		log.Close()
		os.RemoveAll(dir)
		return err
	}
	deadline := time.NewTimer(3 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	probe := &http.Client{Timeout: time.Second}
	for {
		select {
		case err := <-w.done:
			message, _ := os.ReadFile(filepath.Join(dir, "runtime.log"))
			os.RemoveAll(dir)
			if len(message) > 1500 {
				message = message[len(message)-1500:]
			}
			return fmt.Errorf("Whisper could not load the model: %v: %s", err, strings.TrimSpace(string(message)))
		case <-deadline.C:
			stop(w)
			return fmt.Errorf("Whisper model loading timed out")
		case <-tick.C:
			response, err := probe.Get(w.url + "/health")
			if err != nil {
				continue
			}
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				stop(active)
				active = w
				return nil
			}
		}
	}
}

func wavBytes(pcm []byte) []byte {
	// Short Vector commands still need a full second of input for Whisper.
	if len(pcm) < 32640 {
		padded := make([]byte, 32640)
		copy(padded, pcm)
		pcm = padded
	}
	var out bytes.Buffer
	out.WriteString("RIFF")
	binary.Write(&out, binary.LittleEndian, uint32(36+len(pcm)))
	out.WriteString("WAVEfmt ")
	for _, value := range []any{uint32(16), uint16(1), uint16(1), uint32(16000), uint32(32000), uint16(2), uint16(16)} {
		binary.Write(&out, binary.LittleEndian, value)
	}
	out.WriteString("data")
	binary.Write(&out, binary.LittleEndian, uint32(len(pcm)))
	out.Write(pcm)
	return out.Bytes()
}

func Transcribe(pcm []byte) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if active == nil {
		return "", fmt.Errorf("local Whisper is not initialized")
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", "command.wav")
	if err != nil {
		return "", err
	}
	file.Write(wavBytes(pcm))
	form.WriteField("response_format", "json")
	form.WriteField("language", active.language)
	form.WriteField("temperature", "0.0")
	form.WriteField("translate", "false")
	form.Close()
	response, err := client.Post(active.url+"/inference", form.FormDataContentType(), &body)
	if err != nil {
		return "", fmt.Errorf("local Whisper: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 2000))
		return "", fmt.Errorf("local Whisper HTTP %d: %s", response.StatusCode, message)
	}
	var result struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&result); err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.ToLower(result.Text)), nil
}
