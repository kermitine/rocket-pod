package sdkapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

func TestTwoColorEyeOverlay(t *testing.T) {
	data, err := twoColorEyeOverlay("#00aaff", "#ff9900")
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 184 || img.Bounds().Dy() != 96 {
		t.Fatalf("wrong overlay size: %v", img.Bounds())
	}
	for _, sample := range []struct {
		x    int
		want [3]int
	}{{40, [3]int{0, 170, 255}}, {140, [3]int{255, 153, 0}}} {
		r, g, b, _ := img.At(sample.x, 48).RGBA()
		for index, actual := range []uint32{r >> 8, g >> 8, b >> 8} {
			delta := int(actual) - sample.want[index]
			if delta < -3 || delta > 3 {
				t.Fatalf("wrong eye color at x=%d: %d, %d, %d", sample.x, r>>8, g>>8, b>>8)
			}
		}
	}
	for _, invalid := range []string{"", "blue", "#fff", "#zz0000", "#0000000", "#000000; reboot"} {
		if _, err := twoColorEyeOverlay(invalid, "#ffffff"); err == nil {
			t.Errorf("accepted invalid left color %q", invalid)
		}
		if _, err := twoColorEyeOverlay("#ffffff", invalid); err == nil {
			t.Errorf("accepted invalid right color %q", invalid)
		}
	}
}

func TestEyeConsoleRejectsUnconfirmedResponses(t *testing.T) {
	for _, feedback := range []string{"Variable not found<br>", "Error setting variable<br>", "0<br>", "NaN", "+Inf", "", strings.Repeat("1", 4097)} {
		t.Run(fmt.Sprintf("response_%.25s", feedback), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, feedback)
			}))
			defer server.Close()
			console := eyeConsole{baseURL: server.URL, client: server.Client()}
			if err := console.set(context.Background(), "ProcFace_CustomEyes", 1); err == nil {
				t.Fatal("treated an unconfirmed HTTP 200 as success")
			}
		})
	}
	for _, feedback := range []string{"true<br>", "1<br />", "1.000000<br/>\n"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, feedback)
		}))
		console := eyeConsole{baseURL: server.URL, client: server.Client()}
		err := console.set(context.Background(), "ProcFace_CustomEyes", 1)
		server.Close()
		if err != nil {
			t.Fatalf("rejected valid confirmation %q: %v", feedback, err)
		}
	}
}

type fakeEyeRemote struct {
	commands   []string
	upload     []byte
	present    bool
	fail       string
	boot       []byte
	bootExists bool
	failBoot   bool
	platform   string
}

func (remote *fakeEyeRemote) Run(command string, input []byte) (string, error) {
	remote.commands = append(remote.commands, command)
	if strings.Contains(command, "platform_config.json") {
		if remote.platform != "" {
			return remote.platform, nil
		}
		return `{"DataPlatformCachePath":"/data/data/com.anki.victor/cache"}`, nil
	}
	if strings.HasPrefix(command, "if [ -e ") && strings.Contains(command, "consoleVarsAnim.ini") {
		if remote.bootExists {
			return "present\n" + string(remote.boot), nil
		}
		return "missing\n", nil
	}
	if strings.Contains(command, "consoleVarsAnim.ini.rocket-pod-tmp") && strings.HasPrefix(command, "set -e\n") {
		remote.boot, remote.bootExists = append([]byte{}, input...), true
		if remote.failBoot {
			remote.failBoot = false
			return "", fmt.Errorf("connection lost after saving boot settings")
		}
		return "", nil
	}
	if strings.HasPrefix(command, "rm -f ") && strings.Contains(command, "consoleVarsAnim.ini") {
		remote.boot, remote.bootExists = nil, false
		return "", nil
	}
	if len(input) != 0 && strings.Contains(command, eyeOverlayPath+".rocket-pod-tmp") {
		remote.upload = append([]byte(nil), input...)
	}
	if remote.fail != "" && strings.HasPrefix(command, remote.fail) {
		return "", fmt.Errorf("transfer failed")
	}
	if strings.Contains(command, "printf present") && remote.present {
		return "present", nil
	}
	return "", nil
}

func TestApplyEyeOverlayAndRollback(t *testing.T) {
	for _, scenario := range []string{"success", "upload_failure", "enable_failure", "enable_failure_no_original", "boot_failure", "boot_failure_no_original"} {
		t.Run(scenario, func(t *testing.T) {
			values := map[string]float64{"ProcFace_CustomEyes": 0, "ProcFace_FlavorOfGay": 2, "ProcFace_CustomEyeOpacity": 0.8}
			reloads := 0
			failed := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/consolevarget":
					fmt.Fprintf(w, "%f<br>", values[r.URL.Query().Get("key")])
				case "/consolefunclist":
					fmt.Fprint(w, "LOOK_LoadFaceOverlay<br>\n")
				case "/consolevarset":
					key := r.URL.Query().Get("key")
					value, _ := strconv.ParseFloat(r.URL.Query().Get("value"), 64)
					if strings.HasPrefix(scenario, "enable_failure") && key == "ProcFace_CustomEyes" && value == 1 && !failed {
						failed = true
						fmt.Fprint(w, "Error setting variable<br>")
						return
					}
					values[key] = value
					fmt.Fprintf(w, "%f<br>", value)
				case "/consolefunccall":
					reloads++
				default:
					t.Errorf("unexpected console path %s", r.URL.Path)
				}
			}))
			defer server.Close()
			console := eyeConsole{baseURL: server.URL, client: server.Client()}
			state, err := console.state(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			originalBoot := []byte("; existing preferences\nOtherSetting = 7\nProcFace_CustomEyes = false\n")
			remote := &fakeEyeRemote{present: !strings.HasSuffix(scenario, "no_original"), boot: originalBoot, bootExists: !strings.HasSuffix(scenario, "no_original"), failBoot: strings.HasPrefix(scenario, "boot_failure")}
			if scenario == "upload_failure" {
				remote.fail = "umask"
			}
			data, _ := twoColorEyeOverlay("#00aaff", "#ff9900")
			err = applyEyeOverlay(context.Background(), console, remote, state, data)
			if !bytes.Equal(remote.upload, data) {
				t.Fatal("did not upload the generated JPEG")
			}
			if scenario == "success" {
				if err != nil || values["ProcFace_CustomEyes"] != 1 || values["ProcFace_FlavorOfGay"] != 8 || values["ProcFace_CustomEyeOpacity"] != 1 || reloads != 1 {
					t.Fatalf("overlay was not applied: values=%v reloads=%d error=%v", values, reloads, err)
				}
				if !remote.bootExists || !bytes.Equal(remote.boot, eyeOverlayBootContents(originalBoot, true)) {
					t.Fatalf("did not save eye colors for reboot: %s", remote.boot)
				}
			} else {
				if err == nil {
					t.Fatal("reported success after a failure")
				}
				if values["ProcFace_CustomEyes"] != 0 || values["ProcFace_FlavorOfGay"] != 2 || values["ProcFace_CustomEyeOpacity"] != 0.8 {
					t.Fatalf("did not restore previous console values: %v", values)
				}
				if strings.HasPrefix(scenario, "enable_failure") || strings.HasPrefix(scenario, "boot_failure") {
					last := remote.commands[len(remote.commands)-1]
					if remote.present && last != "cp -p "+eyeOverlayPath+".rocket-pod-previous "+eyeOverlayPath {
						t.Fatalf("did not restore the previous image: %s", last)
					}
					if !remote.present && last != "rm -f "+eyeOverlayPath {
						t.Fatalf("did not remove the failed new image: %s", last)
					}
				}
				if remote.bootExists != !strings.HasSuffix(scenario, "no_original") || (remote.bootExists && !bytes.Equal(remote.boot, originalBoot)) {
					t.Fatalf("did not restore the original boot settings: %s", remote.boot)
				}
			}
		})
	}
}

func TestEyeOverlayAPIValidatesBeforeConnecting(t *testing.T) {
	previous := vars.BotInfo
	t.Cleanup(func() { vars.BotInfo = previous })
	json.Unmarshal([]byte(`{"robots":[{"esn":"00test01","ip_address":"192.0.2.1"}]}`), &vars.BotInfo)
	for _, scenario := range []string{"wrong_method", "missing_robot", "invalid_ip", "invalid_color", "invalid_key", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			writer.WriteField("enabled", "true")
			if scenario == "invalid_ip" {
				writer.WriteField("robot_ip", "not-an-ip")
			} else {
				writer.WriteField("robot_ip", "192.0.2.42")
			}
			left := "#00aaff"
			if scenario == "invalid_color" {
				left = "invalid"
			}
			writer.WriteField("left", left)
			writer.WriteField("right", "#ff9900")
			file, _ := writer.CreateFormFile("key", "robot.key")
			if scenario == "oversized" {
				file.Write(bytes.Repeat([]byte("a"), 130*1024))
			} else {
				file.Write([]byte("invalid private key"))
			}
			writer.Close()
			serial := "00test01"
			if scenario == "missing_robot" {
				serial = "unknown"
			}
			method := http.MethodPost
			wantStatus := http.StatusBadRequest
			if scenario == "wrong_method" {
				method, wantStatus = http.MethodGet, http.StatusMethodNotAllowed
			}
			request := httptest.NewRequest(method, "/api-sdk/eye_overlay?serial="+url.QueryEscape(serial), &body)
			request.Header.Set("Content-Type", writer.FormDataContentType())
			response := httptest.NewRecorder()
			SdkapiHandler(response, request)
			if response.Code != wantStatus {
				t.Fatalf("got HTTP %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestEyeOverlayDestination(t *testing.T) {
	previous := vars.BotInfo
	t.Cleanup(func() { vars.BotInfo = previous })
	json.Unmarshal([]byte(`{"robots":[{"esn":"00Test01","ip_address":"192.0.2.1"},{"esn":"00test02","ip_address":"stale"}]}`), &vars.BotInfo)
	for _, scenario := range []struct {
		name, serial, requested, wantIP string
	}{
		{"saved", "00test01", "", "192.0.2.1"},
		{"whitespace", "00test01", "  ", "192.0.2.1"},
		{"override", "00test01", " 192.0.2.42 ", "192.0.2.42"},
		{"ipv6", "00test01", "2001:db8::42", "2001:db8::42"},
		{"invalid_saved_with_override", "00test02", "192.0.2.42", "192.0.2.42"},
		{"invalid_saved", "00test02", "", ""},
		{"unknown_robot", "unknown", "192.0.2.42", ""},
		{"missing_robot", "", "192.0.2.42", ""},
		{"hostname", "00test01", "vector.local", ""},
		{"port", "00test01", "192.0.2.42:22", ""},
		{"loopback", "00test01", "127.0.0.1", ""},
		{"ipv6_loopback", "00test01", "::1", ""},
		{"unspecified", "00test01", "0.0.0.0", ""},
		{"ipv6_unspecified", "00test01", "::", ""},
		{"multicast", "00test01", "224.0.0.1", ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			serial, ip, err := eyeOverlayDestination(scenario.serial, scenario.requested)
			if scenario.wantIP == "" {
				if err == nil {
					t.Fatalf("accepted invalid destination %q for %q", scenario.requested, scenario.serial)
				}
				return
			}
			if err != nil || ip != scenario.wantIP || serial != strings.ToLower(scenario.serial) {
				t.Fatalf("got serial=%q ip=%q error=%v", serial, ip, err)
			}
		})
	}
}

func TestEyeOverlayAPIUsesDestinationAndRequiresKey(t *testing.T) {
	previous, previousTransport := vars.BotInfo, http.DefaultTransport
	t.Cleanup(func() { vars.BotInfo, http.DefaultTransport = previous, previousTransport })
	json.Unmarshal([]byte(`{"robots":[{"esn":"00test01","ip_address":"192.0.2.1"}]}`), &vars.BotInfo)
	for _, requested := range []string{"", "192.0.2.42"} {
		t.Run("ip_"+requested, func(t *testing.T) {
			wantIP := requested
			if wantIP == "" {
				wantIP = "192.0.2.1"
			}
			disabled := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != net.JoinHostPort(wantIP, "8889") {
					t.Errorf("eye controls used unexpected host %q", r.Host)
				}
				switch r.URL.Path {
				case "/consolevarget":
					fmt.Fprint(w, "1<br>")
				case "/consolefunclist":
					fmt.Fprint(w, "LOOK_LoadFaceOverlay<br>")
				case "/consolevarset":
					if r.URL.Query().Get("key") != "ProcFace_CustomEyes" || r.URL.Query().Get("value") != "0" {
						t.Errorf("unexpected disable request: %s", r.URL)
					}
					disabled = true
					fmt.Fprint(w, "0<br>")
				default:
					t.Errorf("unexpected console path: %s", r.URL.Path)
				}
			}))
			defer server.Close()
			transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				if address != net.JoinHostPort(wantIP, "8889") {
					return nil, fmt.Errorf("unexpected destination %s", address)
				}
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}}
			defer transport.CloseIdleConnections()
			http.DefaultTransport = transport
			query := url.Values{"serial": {"00TEST01"}, "robot_ip": {requested}}
			status := httptest.NewRecorder()
			SdkapiHandler(status, httptest.NewRequest(http.MethodGet, "/api-sdk/eye_overlay_status?"+query.Encode(), nil))
			var state struct {
				Supported bool   `json:"supported"`
				RobotIP   string `json:"robot_ip"`
			}
			if err := json.Unmarshal(status.Body.Bytes(), &state); err != nil || !state.Supported || state.RobotIP != wantIP {
				t.Fatalf("unexpected status: HTTP %d %s", status.Code, status.Body.String())
			}
			query.Set("enabled", "false")
			request := httptest.NewRequest(http.MethodPost, "/api-sdk/eye_overlay", strings.NewReader(query.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			SdkapiHandler(response, request)
			if response.Code != http.StatusBadRequest || disabled || !strings.Contains(response.Body.String(), "SSH private-key") {
				t.Fatalf("disable without a key must leave the eyes unchanged: HTTP %d %s", response.Code, response.Body.String())
			}
			if vars.BotInfo.Robots[0].IPAddress != "192.0.2.1" {
				t.Fatal("eye overlay override changed the saved robot address")
			}
		})
	}
}
