package botsetup

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kercre123/wire-pod/chipper/pkg/robotssh"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

func sharedSSHTestState(t *testing.T) []byte {
	t.Helper()
	previous, directory, server := vars.BotInfo, vars.JdocsDir, vars.APIConfig.Server
	t.Cleanup(func() { vars.BotInfo, vars.JdocsDir, vars.APIConfig.Server = previous, directory, server })
	vars.JdocsDir = t.TempDir()
	vars.APIConfig.Server.EPConfig = false
	if err := json.Unmarshal([]byte(`{"robots":[{"esn":"00aa0001","ip_address":"192.0.2.1","guid":"keep-private-token"},{"esn":"00aa0002","ip_address":"192.0.2.2"}]}`), &vars.BotInfo); err != nil {
		t.Fatal(err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	key := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})
	if err := robotssh.SaveKey("00aa0001", key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestSSHRobotListSharesExistingKeysWithoutExposingCredentials(t *testing.T) {
	key := sharedSSHTestState(t)
	response := httptest.NewRecorder()
	SSHSetup(response, httptest.NewRequest(http.MethodGet, "/api-ssh/robots", nil))
	var info struct {
		Robots []struct {
			Serial   string `json:"esn"`
			KeySaved bool   `json:"key_saved"`
		} `json:"robots"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil || len(info.Robots) != 2 || !info.Robots[0].KeySaved || info.Robots[1].KeySaved {
		t.Fatalf("incorrect shared-key status: %v", err)
	}
	if bytes.Contains(response.Body.Bytes(), key) || strings.Contains(response.Body.String(), "PRIVATE KEY") || strings.Contains(response.Body.String(), "keep-private-token") {
		t.Fatal("robot list exposed credentials")
	}
	for _, scenario := range []struct{ serial, ip, wantSerial, wantIP string }{
		{"00AA0001", "", "00aa0001", "192.0.2.1"},
		{"", "192.0.2.1", "00aa0001", "192.0.2.1"},
		{"00aa0001", "192.0.2.42", "00aa0001", "192.0.2.42"},
		{"", "192.0.2.99", "", "192.0.2.99"},
	} {
		serial, ip, err := sshSetupDestination(scenario.serial, scenario.ip)
		if err != nil || serial != scenario.wantSerial || ip != scenario.wantIP {
			t.Fatalf("incorrect setup destination: %q %q %v", serial, ip, err)
		}
	}
}

func TestVoiceRepairUsesTheSharedKeyWithoutAnUpload(t *testing.T) {
	sharedSSHTestState(t)
	voiceRepair.Lock()
	previous := voiceRepair.status
	voiceRepair.status = voiceRepairStatus{Running: true, Message: "Already repairing"}
	voiceRepair.Unlock()
	t.Cleanup(func() { voiceRepair.Lock(); voiceRepair.status = previous; voiceRepair.Unlock() })
	response := httptest.NewRecorder()
	SSHSetup(response, voiceRepairRequest(t, map[string]string{"serial": "00aa0001", "server_ip": "192.0.2.100"}, nil))
	// Reaching the overlap guard proves that the stored key loaded and parsed;
	// this test never starts SSH or changes any robot files.
	if response.Code != http.StatusConflict {
		t.Fatalf("saved key was not reused: HTTP %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	SSHSetup(response, voiceRepairRequest(t, map[string]string{"serial": "00aa0002", "server_ip": "192.0.2.100"}, nil))
	if response.Code != http.StatusBadRequest {
		t.Fatal("used another robot's key")
	}
}

func TestSSHSetupRejectsInvalidRequestsBeforeStarting(t *testing.T) {
	sharedSSHTestState(t)
	for _, scenario := range []struct {
		ip  string
		key []byte
	}{
		{"127.0.0.1", nil}, {"192.0.2.42; reboot", nil}, {"192.0.2.42", nil}, {"192.0.2.42", []byte("invalid")}, {"192.0.2.42", bytes.Repeat([]byte("x"), 130*1024)},
	} {
		request := voiceRepairRequest(t, map[string]string{"ip": scenario.ip}, scenario.key)
		request.URL.Path = "/api-ssh/setup"
		response := httptest.NewRecorder()
		SSHSetup(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("accepted invalid setup: HTTP %d", response.Code)
		}
	}
}
