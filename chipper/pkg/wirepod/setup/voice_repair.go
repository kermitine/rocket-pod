package botsetup

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	scp "github.com/bramvdbogaerde/go-scp"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/robotssh"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"golang.org/x/crypto/ssh"
)

type voiceRepairStatus struct {
	Running  bool   `json:"running"`
	Message  string `json:"message"`
	Error    string `json:"error,omitempty"`
	Received int    `json:"received,omitempty"`
	Expected int    `json:"expected,omitempty"`
}

var voiceRepair = struct {
	sync.Mutex
	status voiceRepairStatus
}{}

func setVoiceRepairStatus(status voiceRepairStatus) {
	voiceRepair.Lock()
	voiceRepair.status = status
	voiceRepair.Unlock()
	logger.Println("Voice repair: " + status.Message)
	if status.Error != "" {
		logger.Println("Voice repair: " + status.Error)
	}
}

func voiceRobot(serial string) (string, string, error) {
	for _, robot := range vars.BotInfo.Robots {
		if robot.Esn == serial && net.ParseIP(robot.IPAddress) != nil {
			return robot.IPAddress, robot.GUID, nil
		}
	}
	return "", "", fmt.Errorf("choose an authenticated Vector in Bot Setup")
}

func voiceDestinationIP(serial, requested string) (string, error) {
	stored, _, err := voiceRobot(serial)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(requested) == "" {
		return stored, nil
	}
	ip := net.ParseIP(strings.TrimSpace(requested))
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() {
		return "", fmt.Errorf("enter Vector's LAN IP address for the SSH destination")
	}
	return ip.String(), nil
}

func voiceRepairAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/api-ssh/voice_repair_status" {
		voiceRepair.Lock()
		status := voiceRepair.status
		voiceRepair.Unlock()
		json.NewEncoder(w).Encode(status)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/api-ssh/check_voice" {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		serial := r.FormValue("serial")
		robotIP, err := voiceDestinationIP(serial, r.FormValue("robot_ip"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		status, err := testVoiceConnectionAtIP(ctx, serial, robotIP)
		if err != nil {
			status.Message, status.Error = "Voice connection failed.", err.Error()
		}
		json.NewEncoder(w).Encode(status)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	if err := r.ParseMultipartForm(128 * 1024); err != nil {
		http.Error(w, "upload an SSH key smaller than 64 KB", http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	serial := r.FormValue("serial")
	ip, err := voiceDestinationIP(serial, r.FormValue("robot_ip"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	serverIP := net.ParseIP(strings.TrimSpace(r.FormValue("server_ip")))
	if !vars.APIConfig.Server.EPConfig && (serverIP == nil || serverIP.IsUnspecified() || serverIP.IsLoopback()) {
		http.Error(w, "enter this Rocket Pod server's LAN IP address", http.StatusBadRequest)
		return
	}
	key, uploadedKey, err := robotssh.ReadKey(r, serial)
	started := false
	defer func() {
		if !started {
			for index := range key {
				key[index] = 0
			}
		}
	}()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		http.Error(w, "invalid SSH key; use Vector's unencrypted SSH private-key file", http.StatusBadRequest)
		return
	}
	voiceRepair.Lock()
	if voiceRepair.status.Running {
		voiceRepair.Unlock()
		http.Error(w, "a voice repair is already running", http.StatusConflict)
		return
	}
	voiceRepair.status = voiceRepairStatus{Running: true, Message: "Connecting to Vector..."}
	status := voiceRepair.status
	voiceRepair.Unlock()
	started = true
	go func() {
		defer func() {
			for index := range key {
				key[index] = 0
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if err := repairVoiceConnection(ctx, serial, ip, serverIP, signer, key, uploadedKey); err != nil {
			setVoiceRepairStatus(voiceRepairStatus{Message: "Repair could not finish.", Error: err.Error()})
		}
	}()
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(status)
}

type voiceSSH interface {
	Run(string) (string, error)
	Upload(context.Context, string, []byte) error
}

type voiceSSHClient struct{ client *ssh.Client }

func (client voiceSSHClient) Run(command string) (string, error) {
	session, err := client.client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	output, err := session.CombinedOutput(command)
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func (client voiceSSHClient) Upload(ctx context.Context, path string, data []byte) error {
	transfer, err := scp.NewClientBySSH(client.client)
	if err != nil {
		return err
	}
	defer transfer.Close()
	return transfer.CopyFile(ctx, bytes.NewReader(data), path, "0644")
}

func repairVoiceConnection(ctx context.Context, serial, robotIP string, serverIP net.IP, signer ssh.Signer, key []byte, uploadedKey bool) error {
	address := net.JoinHostPort(robotIP, "22")
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("could not reach Vector over SSH: %w", err)
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	sshConn, channels, requests, err := ssh.NewClientConn(conn, address, &ssh.ClientConfig{
		User: "root", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		return fmt.Errorf("SSH authentication failed; check the selected key: %w", err)
	}
	client := ssh.NewClient(sshConn, channels, requests)
	defer client.Close()
	remote := voiceSSHClient{client: client}
	if _, err := robotssh.VerifyAndRememberKey(serial, key, uploadedKey, remote.Run); err != nil {
		return err
	}
	setVoiceRepairStatus(voiceRepairStatus{Running: true, Message: "Checking the server address and certificate..."})
	host := serverIP.String()
	if vars.APIConfig.Server.EPConfig {
		host = "escapepod.local"
	}
	// Read and validate the robot's configuration before changing certificates.
	raw, err := remote.Run("if [ -f /data/data/server_config.json ]; then cat /data/data/server_config.json; else cat /anki/data/assets/cozmo_resources/config/server_config.json; fi")
	if err != nil {
		return fmt.Errorf("could not read Vector's server settings: %w", err)
	}
	config, err := repairedVoiceConfig([]byte(raw), host, vars.APIConfig.Server.Port)
	if err != nil {
		return err
	}
	certificate, err := prepareVoiceCertificate(serverIP)
	if err != nil {
		return fmt.Errorf("could not prepare the server certificate: %w", err)
	}
	setVoiceRepairStatus(voiceRepairStatus{Running: true, Message: "Backing up and updating Vector's connection settings..."})
	backup, err := installVoiceRepair(ctx, remote, config, certificate)
	if err != nil {
		return err
	}
	logger.Println("Voice repair: original robot files retained in " + backup)
	setVoiceRepairStatus(voiceRepairStatus{Running: true, Message: "Reconnecting Vector..."})
	if _, err := remote.Run("set -e\nsystemctl restart vic-switchboard\nsystemctl restart vic-cloud\n"); err != nil {
		return fmt.Errorf("settings were updated, but Vector's connection services could not restart: %w", err)
	}
	setVoiceRepairStatus(voiceRepairStatus{Running: true, Message: "Testing the repaired voice connection..."})
	var status voiceRepairStatus
	for attempt := 0; attempt < 5; attempt++ {
		status, err = testVoiceConnectionAtIP(ctx, serial, robotIP)
		if err == nil {
			if err := rememberVoiceRobotIP(serial, robotIP); err != nil {
				return fmt.Errorf("voice connection repaired, but saving Vector's new IP failed: %w", err)
			}
			setVoiceRepairStatus(status)
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("settings were updated, but the connection test timed out; try Check voice connection")
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("settings were updated, but the connection test failed: %w", err)
}

func repairedVoiceConfig(data []byte, host, port string) ([]byte, error) {
	var config map[string]json.RawMessage
	if err := json.Unmarshal(data, &config); err != nil || config == nil {
		return nil, fmt.Errorf("Vector's server configuration is not valid JSON; no robot files were changed")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, fmt.Errorf("Rocket Pod's voice port is invalid")
	}
	for _, field := range []string{"chipper", "jdocs", "tms"} {
		config[field], _ = json.Marshal(net.JoinHostPort(host, port))
	}
	checkHost := host
	if strings.Contains(host, ":") {
		checkHost = "[" + host + "]"
	}
	config["check"], _ = json.Marshal(checkHost + "/ok")
	return json.MarshalIndent(config, "", "  ")
}

var voiceRepairDirectory = regexp.MustCompile(`^/data/data/rocket-pod-voice-repair\.[a-zA-Z0-9]+$`)

func installVoiceRepair(ctx context.Context, remote voiceSSH, config, certificate []byte) (string, error) {
	directory, err := remote.Run("mktemp -d /data/data/rocket-pod-voice-repair.XXXXXX")
	if err != nil {
		return "", fmt.Errorf("could not create a repair backup directory: %w", err)
	}
	directory = strings.TrimSpace(directory)
	if !voiceRepairDirectory.MatchString(directory) {
		return "", fmt.Errorf("Vector returned an unexpected repair directory")
	}
	for name, data := range map[string][]byte{"server_config.json": config, "wirepod-cert.crt": certificate} {
		if err := remote.Upload(ctx, directory+"/"+name, data); err != nil {
			return "", fmt.Errorf("could not send connection files to Vector: %w", err)
		}
	}
	_, err = remote.Run("set -eu\nrepair=" + directory + "\n" + voiceRepairInstallScript)
	if err != nil {
		return "", fmt.Errorf("could not install connection files; backups are in %s: %w", directory, err)
	}
	return directory, nil
}

const voiceRepairInstallScript = `
config=/data/data/server_config.json
cert=/data/data/wirepod-cert.crt
if [ -f /anki/etc/wirepod-cert.crt ] && ! [ /anki/etc/wirepod-cert.crt -ef "$cert" ]; then
    echo 'The firmware manages a separate /anki/etc/wirepod-cert.crt; it must be updated before repair can proceed'
    exit 1
fi
had_config=0
had_cert=0
if [ -e "$config" ]; then cp -p "$config" "$repair/previous-server_config.json"; had_config=1; fi
if [ -e "$cert" ]; then cp -p "$cert" "$repair/previous-wirepod-cert.crt"; had_cert=1; fi
restore() {
    result=$?
    if [ "$result" -ne 0 ]; then
        if [ "$had_config" -eq 1 ]; then cp -p "$repair/previous-server_config.json" "$config"; else rm -f "$config"; fi
        if [ "$had_cert" -eq 1 ]; then cp -p "$repair/previous-wirepod-cert.crt" "$cert"; else rm -f "$cert"; fi
    fi
    exit "$result"
}
trap restore EXIT
chmod 0644 "$repair/server_config.json" "$repair/wirepod-cert.crt"
mv "$repair/server_config.json" "$config"
mv "$repair/wirepod-cert.crt" "$cert"
sync
trap - EXIT
`

func rememberVoiceRobotIP(serial, ip string) error {
	info := vars.BotInfo
	info.Robots = append(info.Robots[:0:0], info.Robots...)
	for index, robot := range info.Robots {
		if robot.Esn != serial || robot.IPAddress == ip {
			continue
		}
		info.Robots[index].IPAddress = ip
		data, err := json.Marshal(info)
		if err != nil {
			return err
		}
		if err := writeVoiceFile(vars.BotInfoPath, data, 0600); err != nil {
			return err
		}
		vars.BotInfo = info
		for index, robot := range vars.RecurringInfo {
			if robot.ESN == serial {
				vars.RecurringInfo[index].IP = ip
			}
		}
		return nil
	}
	return nil
}

func testVoiceConnectionAtIP(ctx context.Context, serial, ip string) (voiceRepairStatus, error) {
	_, token, err := voiceRobot(serial)
	if err != nil {
		return voiceRepairStatus{}, err
	}
	certPEM, err := os.ReadFile(filepath.Join(vars.SessionCertPath, serial))
	if err != nil {
		return voiceRepairStatus{}, fmt.Errorf("Vector's session certificate is missing; authenticate him in Bot Setup first")
	}
	return checkRobotVoice(ctx, "https://"+net.JoinHostPort(ip, "443")+"/v1/check_cloud_connection", token, certPEM)
}

func checkRobotVoice(ctx context.Context, endpoint, token string, certPEM []byte) (voiceRepairStatus, error) {
	pinned, _ := pem.Decode(certPEM)
	if pinned == nil {
		return voiceRepairStatus{}, fmt.Errorf("Vector's session certificate is invalid")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{
		// Vector uses a self-signed certificate; pin the certificate saved at pairing.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 || !bytes.Equal(raw[0], pinned.Bytes) {
				return fmt.Errorf("Vector's certificate changed; authenticate him in Bot Setup again")
			}
			return nil
		},
	}}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("{}"))
	if err != nil {
		return voiceRepairStatus{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Transport: transport, Timeout: 25 * time.Second}).Do(req)
	if err != nil {
		return voiceRepairStatus{}, fmt.Errorf("could not reach Vector's connection test: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return voiceRepairStatus{}, fmt.Errorf("Vector's connection test returned HTTP %d; check his authentication in Bot Setup", resp.StatusCode)
	}
	var result struct {
		Code     json.RawMessage `json:"code"`
		Received int             `json:"num_packets"`
		Expected int             `json:"expected_packets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&result); err != nil {
		return voiceRepairStatus{}, fmt.Errorf("could not read Vector's connection-test result: %w", err)
	}
	status := voiceRepairStatus{Received: result.Received, Expected: result.Expected}
	if (string(result.Code) != "1" && string(result.Code) != `"AVAILABLE"`) || result.Expected <= 0 || result.Received != result.Expected {
		return status, fmt.Errorf("Vector's voice connection failed (%d/%d packets). Repair can update his saved server address and certificate", result.Received, result.Expected)
	}
	status.Message = fmt.Sprintf("Voice connection is working (%d/%d packets). Try \"Hey Vector, what time is it?\"", result.Received, result.Expected)
	return status, nil
}
