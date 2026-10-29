package whisperlocal

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestWhisperAudioRequest(t *testing.T) {
	pcm := []byte{1, 2, 3, 4}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference" {
			t.Errorf("wrong inference URL: %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1024 * 1024); err != nil {
			t.Error(err)
			return
		}
		if r.FormValue("language") != "de" || r.FormValue("translate") != "false" {
			t.Error("language/translation settings missing")
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		if header.Filename != "command.wav" || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
			t.Error("invalid WAV upload")
		}
		if binary.LittleEndian.Uint32(data[24:28]) != 16000 || binary.LittleEndian.Uint16(data[22:24]) != 1 {
			t.Error("incorrect sample rate or channels")
		}
		if !bytes.Equal(data[44:48], pcm) || len(data) < 32044 {
			t.Error("lost PCM data or short-command padding")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"text":"  Hallo Vector  "}`)
	}))
	defer server.Close()
	previous := active
	active = &worker{url: server.URL, language: "de"}
	defer func() { active = previous }()
	text, err := Transcribe(pcm)
	if err != nil || text != "hallo vector" {
		t.Fatalf("unexpected transcription: %q, %v", text, err)
	}
}

func TestWhisperFailureIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	previous := active
	active = &worker{url: server.URL, language: "en"}
	defer func() { active = previous }()
	text, err := Transcribe(nil)
	if text != "" || err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("inference failure became recognized speech: %q, %v", text, err)
	}
}

func TestLocalWhisperIntegration(t *testing.T) {
	model, pcmPath := os.Getenv("ROCKET_POD_TEST_WHISPER_MODEL"), os.Getenv("ROCKET_POD_TEST_PCM")
	if model == "" || pcmPath == "" {
		t.Skip("set ROCKET_POD_TEST_WHISPER_MODEL and ROCKET_POD_TEST_PCM for native inference")
	}
	pcm, err := os.ReadFile(pcmPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := Init(model, "en-US"); err != nil {
		t.Fatal(err)
	}
	defer Close()
	for i := 0; i < 2; i++ {
		text, err := Transcribe(pcm)
		if err != nil || text == "" {
			t.Fatalf("inference %d: text=%q error=%v", i, text, err)
		}
		t.Logf("inference %d: %s", i, text)
	}
}
