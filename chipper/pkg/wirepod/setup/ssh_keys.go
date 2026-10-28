package botsetup

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/kercre123/wire-pod/chipper/pkg/robotssh"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

func sshRobotsAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "use GET", http.StatusMethodNotAllowed)
		return
	}
	type robot struct {
		Serial   string `json:"esn"`
		IP       string `json:"ip_address"`
		KeySaved bool   `json:"key_saved"`
	}
	robots := make([]robot, 0, len(vars.BotInfo.Robots))
	for _, info := range vars.BotInfo.Robots {
		robots = append(robots, robot{info.Esn, info.IPAddress, robotssh.HasSavedKey(info.Esn)})
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"robots": robots})
}

func sshSetupDestination(serial, requestedIP string) (string, string, error) {
	serial = strings.ToLower(strings.TrimSpace(serial))
	address := strings.TrimSpace(requestedIP)
	for _, robot := range vars.BotInfo.Robots {
		if serial != "" && strings.EqualFold(robot.Esn, serial) {
			if address == "" {
				address = robot.IPAddress
			}
			break
		}
		if serial == "" && address != "" && address == robot.IPAddress {
			serial = strings.ToLower(robot.Esn)
			break
		}
	}
	ip := net.ParseIP(address)
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() {
		return "", "", fmt.Errorf("enter Vector's LAN IP address for SSH setup")
	}
	return serial, ip.String(), nil
}
