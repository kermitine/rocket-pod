package sdkapp

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// vic-anim loads consoleVarsAnim.ini before drawing the first procedural face.
// Edit only our eye settings; SaveConsoleVars would also persist unrelated,
// temporary debug controls such as DevDoNothing.
type eyeOverlayBootConfig struct {
	path     string
	original []byte
	existed  bool
}

func eyeOverlayShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func readEyeOverlayBootConfig(remote eyeOverlayRemote) (eyeOverlayBootConfig, error) {
	var config eyeOverlayBootConfig
	platform, err := remote.Run("head -c 65537 /anki/etc/config/platform_config.json", nil)
	if err != nil || len(platform) > 65536 {
		return config, fmt.Errorf("could not read Vector's boot configuration")
	}
	var settings struct {
		CachePath string `json:"DataPlatformCachePath"`
	}
	if err := json.Unmarshal([]byte(platform), &settings); err != nil {
		return config, fmt.Errorf("could not parse Vector's boot configuration")
	}
	cache := path.Clean(settings.CachePath)
	if !strings.HasPrefix(cache, "/data/") || strings.ContainsAny(cache, "\x00\r\n") {
		return config, fmt.Errorf("Vector does not expose a persistent animation-settings directory")
	}
	config.path = path.Join(cache, "consoleVarsAnim.ini")
	quoted := eyeOverlayShellQuote(config.path)
	const maxConfigSize = 1024 * 1024
	command := fmt.Sprintf("if [ -e %s ]; then [ -f %s ] || exit 1; printf 'present\\n'; head -c %d %s; else printf 'missing\\n'; fi", quoted, quoted, maxConfigSize+1, quoted)
	raw, err := remote.Run(command, nil)
	if err != nil {
		return config, fmt.Errorf("could not read Vector's saved animation settings: %w", err)
	}
	if raw == "missing\n" {
		return config, nil
	}
	if !strings.HasPrefix(raw, "present\n") || len(raw)-len("present\n") > maxConfigSize {
		return config, fmt.Errorf("could not read Vector's saved animation settings (maximum 1 MB)")
	}
	config.existed = true
	config.original = []byte(strings.TrimPrefix(raw, "present\n"))
	return config, nil
}

func eyeOverlayBootContents(original []byte, enabled bool) []byte {
	values := []struct{ key, value string }{{"ProcFace_CustomEyes", "false"}}
	if enabled {
		values[0].value = "true"
		values = append(values, struct{ key, value string }{"ProcFace_FlavorOfGay", "8"}, struct{ key, value string }{"ProcFace_CustomEyeOpacity", "1"})
	}
	var updated strings.Builder
	for _, line := range strings.SplitAfter(string(original), "\n") {
		key, _, _ := strings.Cut(line, "=")
		replace := false
		for _, setting := range values {
			if strings.EqualFold(strings.TrimSpace(key), setting.key) {
				replace = true
				break
			}
		}
		if !replace {
			updated.WriteString(line)
		}
	}
	if updated.Len() > 0 && !strings.HasSuffix(updated.String(), "\n") {
		updated.WriteByte('\n')
	}
	for _, setting := range values {
		fmt.Fprintf(&updated, "%s = %s\n", setting.key, setting.value)
	}
	return []byte(updated.String())
}

func (config eyeOverlayBootConfig) write(remote eyeOverlayRemote, data []byte) error {
	temporary := eyeOverlayShellQuote(config.path + ".rocket-pod-tmp")
	_, err := remote.Run("set -e\numask 077\ncat > "+temporary+"\nmv "+temporary+" "+eyeOverlayShellQuote(config.path), data)
	return err
}

func (config eyeOverlayBootConfig) save(remote eyeOverlayRemote, enabled bool) error {
	if config.existed {
		backup := eyeOverlayShellQuote(config.path + ".rocket-pod-backup")
		if _, err := remote.Run("if [ ! -e "+backup+" ]; then cp -p "+eyeOverlayShellQuote(config.path)+" "+backup+"; fi", nil); err != nil {
			return fmt.Errorf("could not back up Vector's animation settings: %w", err)
		}
	}
	if err := config.write(remote, eyeOverlayBootContents(config.original, enabled)); err != nil {
		return fmt.Errorf("could not save eye colors for reboot: %w", err)
	}
	return nil
}

func (config eyeOverlayBootConfig) restore(remote eyeOverlayRemote) error {
	if config.existed {
		return config.write(remote, config.original)
	}
	_, err := remote.Run("rm -f "+eyeOverlayShellQuote(config.path)+" "+eyeOverlayShellQuote(config.path+".rocket-pod-tmp"), nil)
	return err
}
