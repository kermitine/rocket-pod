package botsetup

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	scp "github.com/bramvdbogaerde/go-scp"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/robotssh"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"golang.org/x/crypto/ssh"
)

// this file will be copied to the bot
var SetupScriptPath = "../vector-cloud/pod-bot-install.sh"

// path to copy to
const BotSetupPath = "/data/pod-bot-install.sh"

var SetupSSHStatus string = "not running"
var SSHSettingUp bool = false
var setupSSHMu sync.Mutex

func doErr(err error, msg string) error {
	SSHSettingUp = false
	SetupSSHStatus = "not running (last error: " + err.Error() + ", last step: " + msg + ")"
	return err
}

func runCmd(client *ssh.Client, cmd string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	output, err := session.Output(cmd)
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func setCPURAMfreq(client *ssh.Client, cpufreq string, ramfreq string, gov string) {
	runCmd(client, "echo "+cpufreq+" > /sys/devices/system/cpu/cpu0/cpufreq/scaling_max_freq && echo disabled > /sys/kernel/debug/msm_otg/bus_voting && echo 0 > /sys/kernel/debug/msm-bus-dbg/shell-client/update_request && echo 1 > /sys/kernel/debug/msm-bus-dbg/shell-client/mas && echo 512 > /sys/kernel/debug/msm-bus-dbg/shell-client/slv && echo 0 > /sys/kernel/debug/msm-bus-dbg/shell-client/ab && echo active clk2 0 1 max "+ramfreq+" > /sys/kernel/debug/rpm_send_msg/message && echo "+gov+" > /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor && echo 1 > /sys/kernel/debug/msm-bus-dbg/shell-client/update_request")
}

func SetupBotViaSSH(ip string, key []byte) error {
	return setupBotViaSSH(ip, "", key, true)
}

func setupBotViaSSH(ip, serial string, key []byte, uploadedKey bool) error {
	if runtime.GOOS == "android" || runtime.GOOS == "ios" {
		SetupScriptPath = vars.AndroidPath + "/static/pod-bot-install.sh"
	}
	if vars.IsPackagedLinux {
		SetupScriptPath = "./pod-bot-install.sh"
	}
	if setupSSHMu.TryLock() {
		defer setupSSHMu.Unlock()
		SSHSettingUp = true
		defer func() { SSHSettingUp = false }()
		logger.Println("Setting up " + ip + " via SSH")
		SetupSSHStatus = "Setting up SSH connection..."
		CreateServerConfig()
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return doErr(err, "parsing priv key")
		}
		config := &ssh.ClientConfig{
			User: "root",
			Auth: []ssh.AuthMethod{
				ssh.PublicKeys(signer),
			},
			HostKeyCallback:   ssh.InsecureIgnoreHostKey(),
			HostKeyAlgorithms: []string{"ssh-rsa", "ecdsa-sha2-nistp256"},
			Timeout:           time.Second * 5,
		}
		client, err := ssh.Dial("tcp", net.JoinHostPort(ip, "22"), config)
		if err != nil {
			return doErr(err, "ssh dial")
		}
		defer client.Close()
		SetupSSHStatus = "Checking if device is a Vector..."
		if _, err := robotssh.VerifyAndRememberKey(serial, key, uploadedKey, func(command string) (string, error) { return runCmd(client, command) }); err != nil {
			return doErr(err, "checking Vector and saving SSH key")
		}
		SetupSSHStatus = "Checking if Vector is running CFW..."
		// outputWired, _ := runCmd(client, "cat /etc/wired/webroot/index.html")
		var doCloud bool = true
		var initCommand string = "mount -o rw,remount / && mount -o rw,remount,exec /data && systemctl stop anki-robot.target mm-anki-camera mm-qcamera-daemon"
		_, err = runCmd(client, "head -n1 /anki/bin/vic-gateway")
		if err != nil {
			if err.Error() == "Process exited with status 1" {
				logger.Println("SSH setup: modern CFW detected, not copying vic-cloud")
				//|| strings.Contains(outputWired, "revertDefaultWakeWord") {
				initCommand = "mount -o rw,remount,exec /data && systemctl stop anki-robot.target mm-anki-camera mm-qcamera-daemon"
				// my cfw already has a wire-pod compatible vic-cloud
				doCloud = false
			} else {
				return doErr(err, "checking if cfw")
			}
		}
		SetupSSHStatus = "Running initial commands before transfers (screen will go blank, this is normal)..."
		_, err = runCmd(client, initCommand)
		if err != nil {
			if !strings.Contains(err.Error(), "Process exited with status 1") {
				return doErr(err, "initial commands")
			}
		}
		setCPURAMfreq(client, "1267200", "800000", "performance")
		SetupSSHStatus = "Waiting a few seconds for filesystem syncing"
		time.Sleep(time.Second * 3)
		SetupSSHStatus = "Transferring bot setup script and certs..."
		scpClient, err := scp.NewClientBySSH(client)
		if err != nil {
			return doErr(err, "new scp client")
		}
		script, err := os.Open(SetupScriptPath)
		if err != nil {
			return doErr(err, "opening setup script")
		}
		err = scpClient.CopyFile(context.Background(), script, "/data/pod-bot-install.sh", "0755")
		if err != nil {
			return doErr(err, "copying pod-bot-install")
		}
		scpClient.Close()
		serverConfig, err := os.Open(vars.ServerConfigPath)
		if err != nil {
			return doErr(err, "opening server config on disk")
		}
		scpClient, err = scp.NewClientBySSH(client)
		if err != nil {
			return doErr(err, "new scp client 2")
		}
		err = scpClient.CopyFile(context.Background(), serverConfig, "/data/data/server_config.json", "0755")
		if err != nil {
			return doErr(err, "copying server-config.json")
		}
		scpClient.Close()
		if doCloud {
			if runtime.GOOS != "android" && !vars.Packaged {
				cloud, err := os.Open("../vector-cloud/build/vic-cloud")
				if err != nil {
					return doErr(err, "transferring new vic-cloud")
				}
				SetupSSHStatus = "Transferring new vic-cloud..."
				scpClient, err = scp.NewClientBySSH(client)
				if err != nil {
					return doErr(err, "new scp client 3")
				}
				err = scpClient.CopyFile(context.Background(), cloud, "/anki/bin/vic-cloud", "0755")
				if err != nil {
					time.Sleep(time.Second * 1)
					scpClient, err = scp.NewClientBySSH(client)
					if err != nil {
						return doErr(err, "copying vic-cloud")
					}
					err = scpClient.CopyFile(context.Background(), cloud, "/anki/bin/vic-cloud", "0755")
					if err != nil {
						return doErr(err, "copying vic-cloud")
					}
				}
			} else {
				resp, err := http.Get("https://github.com/kercre123/wire-pod/raw/main/vector-cloud/build/vic-cloud")
				if err != nil {
					return doErr(err, "transferring new vic-cloud (download)")
				}
				SetupSSHStatus = "Transferring new vic-cloud..."
				scpClient, err = scp.NewClientBySSH(client)
				if err != nil {
					return doErr(err, "new scp client 3")
				}
				err = scpClient.CopyFile(context.Background(), resp.Body, "/anki/bin/vic-cloud", "0755")
				if err != nil {
					time.Sleep(time.Second * 1)
					scpClient, err = scp.NewClientBySSH(client)
					if err != nil {
						return doErr(err, "copying vic-cloud")
					}
					err = scpClient.CopyFile(context.Background(), resp.Body, "/anki/bin/vic-cloud", "0755")
					if err != nil {
						return doErr(err, "copying vic-cloud")
					}
				}
			}
		}
		scpClient.Close()
		certPath := vars.CertPath
		if vars.APIConfig.Server.EPConfig {
			if runtime.GOOS == "android" || runtime.GOOS == "ios" {
				certPath = vars.AndroidPath + "/static/epod/ep.crt"
			} else {
				certPath = "./epod/ep.crt"
			}
		}
		cert, err := os.Open(certPath)
		if err != nil {
			return doErr(err, "opening cert")
		}
		scpClient, err = scp.NewClientBySSH(client)
		if err != nil {
			return doErr(err, "new scp client 4")
		}
		err = scpClient.CopyFile(context.Background(), cert, "/data/data/wirepod-cert.crt", "0755")
		if err != nil {
			return doErr(err, "copying wire-pod cert")
		}
		scpClient.Close()
		SetupSSHStatus = "Generating new robot certificate (this may take a while)..."
		_, err = runCmd(client, "chmod +rwx /data/data/server_config.json /data/data/wirepod-cert.crt /data/pod-bot-install.sh && /data/pod-bot-install.sh")
		if err != nil {
			return doErr(err, "generating new robot cert")
		}
		setCPURAMfreq(client, "733333", "500000", "interactive")
		client.Close()
		SetupSSHStatus = "done"
	} else {
		return fmt.Errorf("a bot is already being setup")
	}
	return nil
}

func SSHSetup(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api-ssh/robots":
		sshRobotsAPI(w, r)
		return
	case r.URL.Path == "/api-ssh/repair_voice" || r.URL.Path == "/api-ssh/voice_repair_status" || r.URL.Path == "/api-ssh/check_voice":
		voiceRepairAPI(w, r)
		return
	case r.URL.Path == "/api-ssh/setup":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
		if err := r.ParseMultipartForm(128 * 1024); err != nil {
			http.Error(w, "upload an SSH key smaller than 64 KB", http.StatusBadRequest)
			return
		}
		defer r.MultipartForm.RemoveAll()
		serial, ip, err := sshSetupDestination(r.FormValue("serial"), r.FormValue("ip"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		key, uploadedKey, err := robotssh.ReadKey(r, serial)
		if err == nil {
			_, err = ssh.ParsePrivateKey(key)
		}
		if err != nil {
			for index := range key {
				key[index] = 0
			}
			http.Error(w, "select Vector's saved key or upload an unencrypted SSH private-key file", http.StatusBadRequest)
			return
		}
		go func() {
			defer func() {
				for index := range key {
					key[index] = 0
				}
			}()
			setupBotViaSSH(ip, serial, key, uploadedKey)
		}()
		fmt.Fprint(w, "running")
		return
	case r.URL.Path == "/api-ssh/get_setup_status":
		fmt.Fprint(w, SetupSSHStatus)
		if SetupSSHStatus == "done" || strings.Contains(SetupSSHStatus, "error") {
			SetupSSHStatus = "not running"
		}
		return
	}
}

func RegisterSSHAPI() {
	http.HandleFunc("/api-ssh/", SSHSetup)
}
