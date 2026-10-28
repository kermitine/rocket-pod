package sdkapp

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

var transCfg = &http.Transport{
	TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // ignore SSL warnings
}

func customEyeColorValues(hueText, saturationText string) (float64, float64, error) {
	hue, hueErr := strconv.ParseFloat(hueText, 64)
	saturation, saturationErr := strconv.ParseFloat(saturationText, 64)
	if hueErr != nil || saturationErr != nil || !(hue >= 0 && hue <= 1) || !(saturation >= 0 && saturation <= 1) {
		return 0, 0, fmt.Errorf("eye hue and saturation must be numbers from 0 to 1")
	}
	// Hue 1 and hue 0 are the same red. WireOS's sprite HSV conversion can
	// select sector 6 for hue 1 and render white, while procedural eyes stay red.
	// Settings use float32 on Vector, so include values that round up to 1 there.
	if float32(hue) == 1 {
		hue = 0
	}
	return hue, saturation, nil
}

func setCustomEyeColor(robot Robot, hue float64, sat float64) error {
	url := "https://" + robot.Target + "/v1/update_settings"
	updateJSON, err := json.Marshal(map[string]interface{}{
		"update_settings": true,
		"settings": map[string]interface{}{
			"custom_eye_color": map[string]interface{}{"enabled": true, "hue": hue, "saturation": sat},
		},
	})
	if err != nil {
		return fmt.Errorf("could not prepare eye color settings: %w", err)
	}
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(updateJSON))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+robot.GUID)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: transCfg, Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("could not apply eye color: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Vector rejected eye color settings (HTTP %d)", resp.StatusCode)
	}
	return nil
}

func setPresetEyeColor(robot Robot, value string) {
	url := "https://" + robot.Target + "/v1/update_settings"
	var updateJSON = []byte(`{"update_settings": true, "settings": {"custom_eye_color": {"enabled": false}, "eye_color": ` + value + `} }`)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(updateJSON))
	req.Header.Set("Authorization", "Bearer "+robot.GUID)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: transCfg}
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
}

func setSettingSDKstring(robot Robot, setting string, value string) {
	url := "https://" + robot.Target + "/v1/update_settings"
	var updateJSON = []byte(`{"update_settings": true, "settings": {"` + setting + `": "` + value + `" } }`)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(updateJSON))
	req.Header.Set("Authorization", "Bearer "+robot.GUID)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: transCfg}
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
}

func setSettingSDKintbool(robot Robot, setting string, value string) {
	url := "https://" + robot.Target + "/v1/update_settings"
	var updateJSON = []byte(`{"update_settings": true, "settings": {"` + setting + `": ` + value + ` } }`)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(updateJSON))
	req.Header.Set("Authorization", "Bearer "+robot.GUID)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: transCfg}
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
}
