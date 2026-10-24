package botsetup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

func voiceCertificateTestPaths(t *testing.T) {
	t.Helper()
	previousConfig, previousCert, previousKey := vars.APIConfig.Server, vars.CertPath, vars.KeyPath
	previousCertBytes, previousKeyBytes, previousLoaded := vars.ChipperCert, vars.ChipperKey, vars.ChipperKeysLoaded
	t.Cleanup(func() {
		vars.APIConfig.Server, vars.CertPath, vars.KeyPath = previousConfig, previousCert, previousKey
		vars.ChipperCert, vars.ChipperKey, vars.ChipperKeysLoaded = previousCertBytes, previousKeyBytes, previousLoaded
	})
	directory := t.TempDir()
	vars.APIConfig.Server.EPConfig = false
	vars.CertPath, vars.KeyPath = filepath.Join(directory, "cert.crt"), filepath.Join(directory, "cert.key")
}

func TestVoiceCertificateRenewalPreservesBackupsAndReusesValidCertificate(t *testing.T) {
	voiceCertificateTestPaths(t)
	old, err := prepareVoiceCertificate(net.ParseIP("192.168.1.225"))
	if err != nil {
		t.Fatal(err)
	}
	oldKey, _ := os.ReadFile(vars.KeyPath)
	updated, err := prepareVoiceCertificate(net.ParseIP("192.168.1.226"))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(updated)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || cert.VerifyHostname("192.168.1.226") != nil {
		t.Fatalf("renewed certificate has the wrong host: %v", err)
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(vars.CertPath), "voice-repair-backup-*"))
	found := false
	for _, directory := range backups {
		certificate, _ := os.ReadFile(filepath.Join(directory, "cert.crt"))
		key, _ := os.ReadFile(filepath.Join(directory, "cert.key"))
		if bytes.Equal(certificate, old) && bytes.Equal(key, oldKey) {
			found = true
		}
	}
	if !found {
		t.Fatal("previous certificate and key were not retained together")
	}
	again, err := prepareVoiceCertificate(net.ParseIP("192.168.1.226"))
	if err != nil || !bytes.Equal(updated, again) {
		t.Fatal("repair unnecessarily replaced an already valid certificate")
	}
}

func TestVoiceCertificateUpdatesNewTLSConnectionsWithoutRestart(t *testing.T) {
	voiceCertificateTestPaths(t)
	first, err := prepareVoiceCertificate(net.ParseIP("192.168.1.225"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	server.TLS = &tls.Config{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return LoadVoiceTLSCertificate() }}
	// StartTLS otherwise installs a static test certificate, which can mask a
	// GetCertificate callback for IP clients that send no SNI.
	server.StartTLS()
	server.TLS.Certificates = nil
	defer server.Close()
	check := func(certPEM []byte, host string) {
		t.Helper()
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(certPEM)
		transport := &http.Transport{DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: host}}
		defer transport.CloseIdleConnections()
		response, err := (&http.Client{Transport: transport}).Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatal(response.Status)
		}
	}
	check(first, "192.168.1.225")
	second, err := prepareVoiceCertificate(net.ParseIP("192.168.1.226"))
	if err != nil {
		t.Fatal(err)
	}
	check(second, "192.168.1.226")
}

func TestVoiceCertificateWriteFailureRestoresPreviousCertificate(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "cert.crt")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	err := saveVoiceCertificatePair(path, filepath.Join(directory, "missing", "key"), []byte("replacement"), []byte("key"))
	data, _ := os.ReadFile(path)
	if err == nil || string(data) != "original" {
		t.Fatalf("failed key write did not preserve old certificate: %q, %v", data, err)
	}
}

func TestRepairedVoiceConfigPreservesOtherSettings(t *testing.T) {
	input := []byte(`{"chipper":"192.168.1.225:443","check":"192.168.1.225/ok","jdocs":"old:443","tms":"old:443","appkey":"preserve","logfiles":"preserve-logs","offboard_vision":"preserve-vision","extra":{"value":7}}`)
	data, err := repairedVoiceConfig(input, "192.168.1.226", "443")
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"chipper", "jdocs", "tms"} {
		if result[field] != "192.168.1.226:443" {
			t.Errorf("%s=%v", field, result[field])
		}
	}
	if result["check"] != "192.168.1.226/ok" || result["appkey"] != "preserve" || result["logfiles"] != "preserve-logs" || result["offboard_vision"] != "preserve-vision" || result["extra"].(map[string]interface{})["value"] != float64(7) {
		t.Fatalf("unrelated settings changed: %s", data)
	}
	for _, invalid := range []string{"null", "[]", "broken"} {
		if _, err := repairedVoiceConfig([]byte(invalid), "192.168.1.226", "443"); err == nil {
			t.Errorf("accepted invalid configuration %q", invalid)
		}
	}
	if _, err := repairedVoiceConfig(input, "192.168.1.226", "443; reboot"); err == nil {
		t.Fatal("accepted an invalid voice port")
	}
}

type fakeVoiceSSH struct {
	directory   string
	uploadError bool
	uploads     map[string][]byte
	commands    []string
}

func (remote *fakeVoiceSSH) Run(command string) (string, error) {
	remote.commands = append(remote.commands, command)
	if strings.HasPrefix(command, "mktemp ") {
		return remote.directory, nil
	}
	return "", nil
}
func (remote *fakeVoiceSSH) Upload(_ context.Context, path string, data []byte) error {
	if remote.uploadError {
		return fmt.Errorf("transfer interrupted")
	}
	remote.uploads[path] = data
	return nil
}

func TestVoiceRepairStagesBothFilesBeforeInstalling(t *testing.T) {
	remote := &fakeVoiceSSH{directory: "/data/data/rocket-pod-voice-repair.abc123", uploads: make(map[string][]byte)}
	backup, err := installVoiceRepair(context.Background(), remote, []byte("configuration"), []byte("certificate"))
	if err != nil || backup != remote.directory || len(remote.uploads) != 2 || len(remote.commands) != 2 {
		t.Fatalf("incomplete repair: %s, %v, %+v", backup, err, remote)
	}
	if string(remote.uploads[backup+"/server_config.json"]) != "configuration" || string(remote.uploads[backup+"/wirepod-cert.crt"]) != "certificate" {
		t.Fatal("connection files were not staged intact")
	}
	for _, test := range []struct {
		directory string
		fail      bool
	}{{"/data/data/rocket-pod-voice-repair.abc123", true}, {"/data/data/rocket-pod-voice-repair.abc;reboot", false}} {
		remote = &fakeVoiceSSH{directory: test.directory, uploadError: test.fail, uploads: make(map[string][]byte)}
		if _, err := installVoiceRepair(context.Background(), remote, nil, nil); err == nil || len(remote.commands) != 1 {
			t.Fatalf("failed staging was installed: %+v, %v", remote, err)
		}
	}
}

func TestVoiceConnectionCheckPinsCertificateAndRequiresEveryPacket(t *testing.T) {
	code, received, expected := "1", 50, 50
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing authenticated connection check")
		}
		fmt.Fprintf(w, `{"code":%s,"num_packets":%d,"expected_packets":%d}`, code, received, expected)
	}))
	defer server.Close()
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	status, err := checkRobotVoice(context.Background(), server.URL, "test-token", cert)
	if err != nil || status.Received != 50 || !strings.Contains(status.Message, "working") {
		t.Fatalf("successful check: %+v, %v", status, err)
	}
	for _, test := range []struct {
		code               string
		received, expected int
	}{{"2", 0, 50}, {"1", 49, 50}, {"1", 0, 0}} {
		code, received, expected = test.code, test.received, test.expected
		if _, err := checkRobotVoice(context.Background(), server.URL, "test-token", cert); err == nil {
			t.Fatal("reported a failed or incomplete connection as working")
		}
	}
	before := requests
	wrong := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("wrong certificate")})
	if _, err := checkRobotVoice(context.Background(), server.URL, "test-token", wrong); err == nil || requests != before {
		t.Fatal("sent authentication to a robot with a different certificate")
	}
}

func voiceRepairRequest(t *testing.T, fields map[string]string, key []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if key != nil {
		file, err := writer.CreateFormFile("key", "ssh_root_key")
		if err != nil {
			t.Fatal(err)
		}
		file.Write(key)
	}
	writer.Close()
	request := httptest.NewRequest(http.MethodPost, "/api-ssh/repair_voice", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func TestVoiceRepairRejectsInvalidRequestsBeforeStarting(t *testing.T) {
	previous := vars.BotInfo
	previousServer := vars.APIConfig.Server
	t.Cleanup(func() { vars.BotInfo = previous; vars.APIConfig.Server = previousServer })
	if err := json.Unmarshal([]byte(`{"robots":[{"esn":"test","ip_address":"192.168.1.241","guid":"test-token"}]}`), &vars.BotInfo); err != nil {
		t.Fatal(err)
	}
	vars.APIConfig.Server.EPConfig = false
	for _, test := range []struct {
		serial, ip string
		key        []byte
	}{{"unknown", "192.168.1.226", []byte("key")}, {"test", "127.0.0.1", []byte("key")}, {"test", "192.168.1.226;reboot", []byte("key")}, {"test", "192.168.1.226", nil}, {"test", "192.168.1.226", []byte("invalid")}, {"test", "192.168.1.226", bytes.Repeat([]byte("x"), 65*1024)}} {
		response := httptest.NewRecorder()
		SSHSetup(response, voiceRepairRequest(t, map[string]string{"serial": test.serial, "server_ip": test.ip}, test.key))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("accepted invalid repair: %d %s", response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	SSHSetup(response, httptest.NewRequest(http.MethodGet, "/api-ssh/repair_voice", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatal("repair endpoint accepted GET")
	}
	response = httptest.NewRecorder()
	SSHSetup(response, httptest.NewRequest(http.MethodGet, "/api-ssh/voice_repair_status", nil))
	if _, err := io.ReadAll(response.Result().Body); err != nil || response.Code != http.StatusOK {
		t.Fatal("status cannot be read")
	}
}

func TestVoiceRepairPreventsOverlappingJobs(t *testing.T) {
	previous, previousServer := vars.BotInfo, vars.APIConfig.Server
	voiceRepair.Lock()
	previousStatus := voiceRepair.status
	voiceRepair.status = voiceRepairStatus{Running: true, Message: "Already repairing"}
	voiceRepair.Unlock()
	t.Cleanup(func() {
		vars.BotInfo, vars.APIConfig.Server = previous, previousServer
		voiceRepair.Lock()
		voiceRepair.status = previousStatus
		voiceRepair.Unlock()
	})
	if err := json.Unmarshal([]byte(`{"robots":[{"esn":"test","ip_address":"192.168.1.241"}]}`), &vars.BotInfo); err != nil {
		t.Fatal(err)
	}
	vars.APIConfig.Server.EPConfig = false
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	private := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	response := httptest.NewRecorder()
	SSHSetup(response, voiceRepairRequest(t, map[string]string{"serial": "test", "server_ip": "192.168.1.226"}, private))
	if response.Code != http.StatusConflict {
		t.Fatalf("overlapping repair accepted: %d %s", response.Code, response.Body.String())
	}
	for attempt := 0; attempt < 2; attempt++ {
		response = httptest.NewRecorder()
		SSHSetup(response, httptest.NewRequest(http.MethodGet, "/api-ssh/voice_repair_status", nil))
		var status voiceRepairStatus
		if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || !status.Running || status.Message != "Already repairing" {
			t.Fatal("polling changed or consumed the active job")
		}
	}
}

func TestVoiceRepairRobotDestinationIsConfigurableAndValidated(t *testing.T) {
	previous, previousPath, previousRecurring := vars.BotInfo, vars.BotInfoPath, vars.RecurringInfo
	t.Cleanup(func() { vars.BotInfo, vars.BotInfoPath, vars.RecurringInfo = previous, previousPath, previousRecurring })
	if err := json.Unmarshal([]byte(`{"global_guid":"preserve","robots":[{"esn":"test","ip_address":"192.168.1.241","guid":"keep-token","activated":true},{"esn":"other","ip_address":"192.168.1.250","guid":"other-token"}]}`), &vars.BotInfo); err != nil {
		t.Fatal(err)
	}
	vars.BotInfoPath = filepath.Join(t.TempDir(), "bot-info.json")
	vars.RecurringInfo = nil
	for _, test := range []struct{ requested, want string }{{"", "192.168.1.241"}, {" 192.168.1.242 ", "192.168.1.242"}} {
		ip, err := voiceDestinationIP("test", test.requested)
		if err != nil || ip != test.want {
			t.Fatalf("destination %q=%q, %v", test.requested, ip, err)
		}
	}
	for _, invalid := range []string{"127.0.0.1", "0.0.0.0", "192.168.1.241; reboot", "not an ip"} {
		if _, err := voiceDestinationIP("test", invalid); err == nil {
			t.Errorf("accepted invalid robot IP %q", invalid)
		}
	}
	if _, err := voiceDestinationIP("unknown", "192.168.1.242"); err == nil {
		t.Fatal("accepted an unauthenticated serial")
	}
	if err := rememberVoiceRobotIP("test", "192.168.1.242"); err != nil {
		t.Fatal(err)
	}
	if vars.BotInfo.Robots[0].IPAddress != "192.168.1.242" || vars.BotInfo.Robots[0].GUID != "keep-token" || vars.BotInfo.Robots[1].IPAddress != "192.168.1.250" {
		t.Fatal("saving the robot IP changed authentication or another robot")
	}
	var saved vars.RobotInfoStore
	data, err := os.ReadFile(vars.BotInfoPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &saved); err != nil || saved.Robots[0].IPAddress != "192.168.1.242" {
		t.Fatal("new robot IP was not persisted")
	}
}
