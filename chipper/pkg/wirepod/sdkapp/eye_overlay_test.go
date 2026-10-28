package sdkapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"mime/multipart"
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
	commands []string
	upload   []byte
	present  bool
	fail     string
}

func (remote *fakeEyeRemote) Run(command string, input []byte) (string, error) {
	remote.commands = append(remote.commands, command)
	if len(input) != 0 {
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
	for _, scenario := range []string{"success", "upload_failure", "enable_failure", "enable_failure_no_original"} {
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
			remote := &fakeEyeRemote{present: scenario != "enable_failure_no_original"}
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
			} else {
				if err == nil {
					t.Fatal("reported success after a failure")
				}
				if values["ProcFace_CustomEyes"] != 0 || values["ProcFace_FlavorOfGay"] != 2 || values["ProcFace_CustomEyeOpacity"] != 0.8 {
					t.Fatalf("did not restore previous console values: %v", values)
				}
				if strings.HasPrefix(scenario, "enable_failure") {
					last := remote.commands[len(remote.commands)-1]
					if remote.present && last != "cp -p "+eyeOverlayPath+".rocket-pod-previous "+eyeOverlayPath {
						t.Fatalf("did not restore the previous image: %s", last)
					}
					if !remote.present && last != "rm -f "+eyeOverlayPath {
						t.Fatalf("did not remove the failed new image: %s", last)
					}
				}
			}
		})
	}
}

func TestEyeOverlayAPIValidatesBeforeConnecting(t *testing.T) {
	previous := vars.BotInfo
	t.Cleanup(func() { vars.BotInfo = previous })
	json.Unmarshal([]byte(`{"robots":[{"esn":"00test01","ip_address":"192.0.2.1"}]}`), &vars.BotInfo)
	for _, scenario := range []string{"wrong_method", "missing_robot", "invalid_color", "invalid_key", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			writer.WriteField("enabled", "true")
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
