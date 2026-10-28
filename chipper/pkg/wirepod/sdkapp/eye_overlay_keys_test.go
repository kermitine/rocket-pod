package sdkapp

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kercre123/wire-pod/chipper/pkg/robotssh"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

func eyeOverlayTestKeyDirectory(t *testing.T) {
	t.Helper()
	previous := vars.JdocsDir
	t.Cleanup(func() { vars.JdocsDir = previous })
	vars.JdocsDir = t.TempDir()
}

func newEyeOverlayTestKey(t *testing.T) []byte {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})
}

func TestEyeOverlaySavedKeysPersistPerRobotAndCanBeReplaced(t *testing.T) {
	eyeOverlayTestKeyDirectory(t)
	first, second := newEyeOverlayTestKey(t), newEyeOverlayTestKey(t)
	if robotssh.HasSavedKey("Vector-A") {
		t.Fatal("unexpected saved key before the first upload")
	}
	if err := robotssh.SaveKey("Vector-A", first); err != nil {
		t.Fatal(err)
	}
	for _, serial := range []string{"Vector-A", "vector-a", "VECTOR-A"} {
		// Each fresh request reads the persisted file without using an in-memory cache.
		request := httptest.NewRequest(http.MethodPost, "/api-sdk/eye_overlay", nil)
		key, uploaded, err := robotssh.ReadKey(request, serial)
		if err != nil || uploaded || !bytes.Equal(key, first) || !robotssh.HasSavedKey(serial) {
			t.Fatalf("fresh request did not reuse the saved key: uploaded=%v error=%v", uploaded, err)
		}
	}
	if _, _, err := robotssh.ReadKey(httptest.NewRequest(http.MethodPost, "/", nil), "Vector-B"); err == nil {
		t.Fatal("reused another robot's private key")
	}
	if err := robotssh.SaveKey("Vector-B", second); err != nil {
		t.Fatal(err)
	}
	if err := robotssh.SaveKey("VECTOR-A", second); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]byte{nil, []byte("invalid private key"), bytes.Repeat([]byte("x"), robotssh.KeyLimit+1)} {
		if err := robotssh.SaveKey("Vector-A", invalid); err == nil {
			t.Fatal("accepted an invalid replacement key")
		}
	}
	stored, err := os.ReadFile(robotssh.KeyPath("Vector-A"))
	if err != nil || !bytes.Equal(stored, second) {
		t.Fatalf("replacement did not persist or an invalid key overwrote it: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(vars.JdocsDir, "ssh-keys"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("key replacement left extra files: count=%d error=%v", len(entries), err)
	}
	if runtime.GOOS != "windows" {
		for filename, want := range map[string]os.FileMode{robotssh.KeyPath("Vector-A"): 0600, filepath.Join(vars.JdocsDir, "ssh-keys"): 0700} {
			info, err := os.Stat(filename)
			if err != nil || info.Mode().Perm() != want {
				t.Fatalf("incorrect SSH key permissions for %s: %v", filepath.Base(filename), err)
			}
		}
	}
	root, _ := filepath.Abs(filepath.Join(vars.JdocsDir, "ssh-keys"))
	filename, _ := filepath.Abs(robotssh.KeyPath("../../escape"))
	if filepath.Dir(filename) != root {
		t.Fatal("a robot identifier escaped the private key directory")
	}
}

func TestEyeOverlayRejectedUploadKeepsSavedKey(t *testing.T) {
	eyeOverlayTestKeyDirectory(t)
	previous := vars.BotInfo
	t.Cleanup(func() { vars.BotInfo = previous })
	json.Unmarshal([]byte(`{"robots":[{"esn":"00test01","ip_address":"192.0.2.1"}]}`), &vars.BotInfo)
	original := newEyeOverlayTestKey(t)
	if err := robotssh.SaveKey("00test01", original); err != nil {
		t.Fatal(err)
	}
	for _, replacement := range [][]byte{{}, []byte("invalid private key"), bytes.Repeat([]byte("x"), robotssh.KeyLimit+1)} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		writer.WriteField("enabled", "false")
		file, _ := writer.CreateFormFile("key", "replacement.key")
		file.Write(replacement)
		writer.Close()
		request := httptest.NewRequest(http.MethodPost, "/api-sdk/eye_overlay?serial=00test01", &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		response := httptest.NewRecorder()
		SdkapiHandler(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid upload reached the robot: HTTP %d", response.Code)
		}
		stored, err := os.ReadFile(robotssh.KeyPath("00test01"))
		if err != nil || !bytes.Equal(stored, original) {
			t.Fatalf("rejected upload changed the saved key: %v", err)
		}
		if strings.Contains(response.Body.String(), "BEGIN PRIVATE KEY") {
			t.Fatal("private key appeared in an API response")
		}
	}
}
