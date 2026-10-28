package sdkapp

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"golang.org/x/crypto/ssh"
)

// WireOS 3.0.1.26+ masks this image with the normal animated eyes.
const eyeOverlayPath = "/data/data/customFaceOverlay.jpg"

var eyeOverlayLocks sync.Map

type eyeConsole struct {
	baseURL string
	client  *http.Client
}

type eyeOverlayState struct {
	Enabled bool    `json:"enabled"`
	Flavor  float64 `json:"flavor"`
	Opacity float64 `json:"opacity"`
}

func (console eyeConsole) request(ctx context.Context, path string, query url.Values) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, console.baseURL+path+"?"+query.Encode(), nil)
	if err != nil {
		return "", err
	}
	response, err := console.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("cannot reach WireOS eye controls: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("WireOS eye controls returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(body) > 4096 {
		return "", fmt.Errorf("could not read WireOS eye control response")
	}
	return strings.TrimSpace(strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<br />", "\n").Replace(string(body))), nil
}

func (console eyeConsole) value(ctx context.Context, key string, value *float64) (float64, error) {
	path := "/consolevarget"
	query := url.Values{"key": {key}}
	if value != nil {
		path = "/consolevarset"
		query.Set("value", strconv.FormatFloat(*value, 'f', -1, 64))
	}
	feedback, err := console.request(ctx, path, query)
	if err != nil {
		return 0, err
	}
	if feedback == "true" {
		feedback = "1"
	} else if feedback == "false" {
		feedback = "0"
	}
	actual, err := strconv.ParseFloat(feedback, 64)
	if err != nil || math.IsNaN(actual) || math.IsInf(actual, 0) || (value != nil && math.Abs(actual-*value) > 0.00001) {
		return 0, fmt.Errorf("WireOS did not confirm %s; eye overlays require WireOS 3.0.1.26 or newer", key)
	}
	return actual, nil
}

func (console eyeConsole) set(ctx context.Context, key string, value float64) error {
	_, err := console.value(ctx, key, &value)
	return err
}

func (console eyeConsole) state(ctx context.Context) (eyeOverlayState, error) {
	var state eyeOverlayState
	enabled, err := console.value(ctx, "ProcFace_CustomEyes", nil)
	if err != nil {
		return state, err
	}
	state.Enabled = enabled == 1
	state.Flavor, err = console.value(ctx, "ProcFace_FlavorOfGay", nil)
	if err != nil {
		return state, err
	}
	state.Opacity, err = console.value(ctx, "ProcFace_CustomEyeOpacity", nil)
	if err != nil {
		return state, err
	}
	functions, err := console.request(ctx, "/consolefunclist", url.Values{"key": {"LOOK_LoadFaceOverlay"}})
	if err != nil {
		return state, err
	}
	if functions != "LOOK_LoadFaceOverlay" || (enabled != 0 && enabled != 1) || state.Flavor < 0 || state.Flavor > 8 || state.Opacity < 0 || state.Opacity > 1 {
		return state, fmt.Errorf("this firmware does not expose the expected WireOS eye overlay controls")
	}
	return state, nil
}

func (console eyeConsole) reload(ctx context.Context) error {
	// The function returns no text, including when an image cannot be loaded.
	// Check its existence in state() and upload a valid JPEG before calling it.
	feedback, err := console.request(ctx, "/consolefunccall", url.Values{"func": {"LOOK_LoadFaceOverlay"}})
	if err == nil && feedback != "" {
		err = fmt.Errorf("WireOS could not reload the eye overlay")
	}
	return err
}

func twoColorEyeOverlay(left, right string) ([]byte, error) {
	parseColor := func(value string) (color.RGBA, error) {
		if len(value) != 7 || value[0] != '#' {
			return color.RGBA{}, fmt.Errorf("choose both eye colors as #RRGGBB")
		}
		rgb, err := hex.DecodeString(value[1:])
		if err != nil {
			return color.RGBA{}, fmt.Errorf("choose both eye colors as #RRGGBB")
		}
		return color.RGBA{R: rgb[0], G: rgb[1], B: rgb[2], A: 255}, nil
	}
	leftColor, err := parseColor(left)
	if err != nil {
		return nil, err
	}
	rightColor, err := parseColor(right)
	if err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, 184, 96))
	for y := 0; y < 96; y++ {
		for x := 0; x < 184; x++ {
			pixel := leftColor
			if x >= 92 {
				pixel = rightColor
			}
			img.SetRGBA(x, y, pixel)
		}
	}
	var output bytes.Buffer
	err = jpeg.Encode(&output, img, &jpeg.Options{Quality: 100})
	return output.Bytes(), err
}

type eyeOverlayRemote interface {
	Run(string, []byte) (string, error)
}

type eyeSSH struct{ client *ssh.Client }

func (remote eyeSSH) Run(command string, input []byte) (string, error) {
	session, err := remote.client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	session.Stdin = bytes.NewReader(input)
	output, err := session.CombinedOutput(command)
	return string(output), err
}

func applyEyeOverlay(ctx context.Context, console eyeConsole, remote eyeOverlayRemote, state eyeOverlayState, data []byte) (err error) {
	// Keep the original custom image, and a separate copy for rollback of this request.
	present, err := remote.Run("if [ -f "+eyeOverlayPath+" ]; then printf present; fi", nil)
	if err != nil {
		return fmt.Errorf("could not check the existing eye overlay: %w", err)
	}
	if present == "present" {
		_, err = remote.Run("cp -p "+eyeOverlayPath+" "+eyeOverlayPath+".rocket-pod-previous && if [ ! -f "+eyeOverlayPath+".rocket-pod-backup ]; then cp -p "+eyeOverlayPath+" "+eyeOverlayPath+".rocket-pod-backup; fi", nil)
		if err != nil {
			return fmt.Errorf("could not back up the existing eye overlay: %w", err)
		}
	}
	if _, err = remote.Run("umask 077; cat > "+eyeOverlayPath+".rocket-pod-tmp", data); err != nil {
		return fmt.Errorf("could not upload eye colors: %w", err)
	}
	// A failed connection can leave an uncertain result, so restore even if mv fails.
	defer func() {
		if err == nil {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		restore := "rm -f " + eyeOverlayPath
		if present == "present" {
			restore = "cp -p " + eyeOverlayPath + ".rocket-pod-previous " + eyeOverlayPath
		}
		_, fileErr := remote.Run(restore, nil)
		flavorErr := console.set(rollbackCtx, "ProcFace_FlavorOfGay", state.Flavor)
		opacityErr := console.set(rollbackCtx, "ProcFace_CustomEyeOpacity", state.Opacity)
		reloadErr := console.reload(rollbackCtx)
		enabled := 0.0
		if state.Enabled {
			enabled = 1
		}
		enabledErr := console.set(rollbackCtx, "ProcFace_CustomEyes", enabled)
		if fileErr != nil || flavorErr != nil || opacityErr != nil || reloadErr != nil || enabledErr != nil {
			err = fmt.Errorf("%w; could not fully restore the previous overlay, check Vector's eye controls", err)
		}
	}()
	if _, err = remote.Run("mv "+eyeOverlayPath+".rocket-pod-tmp "+eyeOverlayPath, nil); err != nil {
		return fmt.Errorf("could not install eye colors: %w", err)
	}
	if err = console.set(ctx, "ProcFace_CustomEyeOpacity", 1); err != nil {
		return err
	}
	if err = console.set(ctx, "ProcFace_FlavorOfGay", 8); err != nil {
		return err
	}
	if err = console.reload(ctx); err != nil {
		return err
	}
	return console.set(ctx, "ProcFace_CustomEyes", 1)
}

func eyeOverlayAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	statusRequest := r.URL.Path == "/api-sdk/eye_overlay_status"
	if (!statusRequest && r.Method != http.MethodPost) || (statusRequest && r.Method != http.MethodGet) {
		w.Header().Set("Allow", map[bool]string{true: "GET", false: "POST"}[statusRequest])
		http.Error(w, "unsupported method", http.StatusMethodNotAllowed)
		return
	}
	// Bound multipart uploads before FormValue can parse or spool the SSH key.
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(128 * 1024); err != nil {
			http.Error(w, "invalid upload; maximum request size is 128 KB", http.StatusBadRequest)
			return
		}
		defer r.MultipartForm.RemoveAll()
	} else if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	serial := r.FormValue("serial")
	var ip string
	for _, robot := range vars.BotInfo.Robots {
		if serial != "" && strings.EqualFold(robot.Esn, serial) && net.ParseIP(robot.IPAddress) != nil {
			ip = robot.IPAddress
			serial = strings.ToLower(robot.Esn)
			break
		}
	}
	if ip == "" {
		http.Error(w, "choose an authenticated Vector", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	console := eyeConsole{baseURL: "http://" + net.JoinHostPort(ip, "8889"), client: &http.Client{Timeout: 5 * time.Second}}
	if statusRequest {
		state, err := console.state(ctx)
		if err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{"supported": false, "message": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"supported": true, "enabled": state.Enabled, "custom": state.Flavor == 8})
		return
	}
	lock, _ := eyeOverlayLocks.LoadOrStore(serial, &sync.Mutex{})
	if !lock.(*sync.Mutex).TryLock() {
		http.Error(w, "an eye color change is already running for this Vector", http.StatusConflict)
		return
	}
	defer lock.(*sync.Mutex).Unlock()
	if r.FormValue("enabled") == "false" {
		if err := console.set(ctx, "ProcFace_CustomEyes", 0); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"message": "Matching eyes restored. Preset and custom eye colors now apply to both eyes."})
		return
	}
	if r.FormValue("enabled") != "true" {
		http.Error(w, "enabled must be true or false", http.StatusBadRequest)
		return
	}
	data, err := twoColorEyeOverlay(r.FormValue("left"), r.FormValue("right"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	keyFile, _, err := r.FormFile("key")
	if err != nil {
		http.Error(w, "select Vector's SSH private-key file", http.StatusBadRequest)
		return
	}
	defer keyFile.Close()
	key, err := io.ReadAll(io.LimitReader(keyFile, 64*1024+1))
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()
	if err != nil || len(key) > 64*1024 {
		http.Error(w, "unable to read SSH key (maximum 64 KB)", http.StatusBadRequest)
		return
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		http.Error(w, "use Vector's unencrypted SSH private-key file", http.StatusBadRequest)
		return
	}
	state, err := console.state(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	address := net.JoinHostPort(ip, "22")
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		http.Error(w, "could not connect to Vector over SSH", http.StatusBadGateway)
		return
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-finished:
		}
	}()
	sshConn, channels, requests, err := ssh.NewClientConn(conn, address, &ssh.ClientConfig{
		User: "root", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		http.Error(w, "SSH authentication failed; check Vector's SSH key", http.StatusBadGateway)
		return
	}
	client := ssh.NewClient(sshConn, channels, requests)
	defer client.Close()
	remote := eyeSSH{client: client}
	identity, err := remote.Run("uname -a", nil)
	if err != nil || !strings.Contains(identity, "Vector") {
		http.Error(w, "the SSH device did not identify itself as Vector", http.StatusBadGateway)
		return
	}
	if err := applyEyeOverlay(ctx, console, remote, state, data); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"message": "Separate eye colors applied. WireOS may reset the overlay when Vector restarts; reapply here if needed."})
}
