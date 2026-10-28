package sdkapp

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEyeOverlayBootContentsPreservesOtherSettings(t *testing.T) {
	original := []byte("; keep this comment\r\n[Other]\r\nUnrelatedSetting = 9\r\n[Face]\r\n ProcFace_CustomEyes = false\r\nPROCFACE_CUSTOMEYES = false\nProcFace_FlavorOfGay = 2\nProcFace_CustomEyeOpacity = 0.8\nAnotherSetting = 3")
	enabled := eyeOverlayBootContents(original, true)
	for _, expected := range []string{"; keep this comment\r\n[Other]\r\nUnrelatedSetting = 9\r\n[Face]\r\n", "AnotherSetting = 3\n", "ProcFace_CustomEyes = true\n", "ProcFace_FlavorOfGay = 8\n", "ProcFace_CustomEyeOpacity = 1\n"} {
		if !bytes.Contains(enabled, []byte(expected)) {
			t.Errorf("missing preserved or updated setting %q in %s", expected, enabled)
		}
	}
	if strings.Count(strings.ToLower(string(enabled)), "procface_customeyes") != 1 {
		t.Fatal("duplicate eye settings could override the saved choice")
	}
	if !bytes.Equal(enabled, eyeOverlayBootContents(enabled, true)) {
		t.Fatal("reapplying eye colors should not grow or change the settings file")
	}
	disabled := eyeOverlayBootContents(enabled, false)
	if !bytes.Contains(disabled, []byte("ProcFace_CustomEyes = false\n")) || !bytes.Contains(disabled, []byte("ProcFace_FlavorOfGay = 8\nProcFace_CustomEyeOpacity = 1\n")) {
		t.Fatalf("matching eyes must stay disabled at boot without disturbing the other eye settings: %s", disabled)
	}
	if !bytes.Equal(disabled, eyeOverlayBootContents(disabled, false)) {
		t.Fatal("saving matching eyes should not grow or change the settings file")
	}
}

func TestReadEyeOverlayBootConfig(t *testing.T) {
	for _, original := range [][]byte{nil, {}, []byte("OtherSetting = 7\n")} {
		remote := &fakeEyeRemote{boot: original, bootExists: original != nil}
		config, err := readEyeOverlayBootConfig(remote)
		if err != nil || config.path != "/data/data/com.anki.victor/cache/consoleVarsAnim.ini" || config.existed != (original != nil) || !bytes.Equal(config.original, original) {
			t.Fatalf("unexpected snapshot: %+v error=%v", config, err)
		}
		if err := config.save(remote, true); err != nil {
			t.Fatal(err)
		}
		// Read as a new request would after a restart; no browser or saved SSH key is involved.
		reloaded, err := readEyeOverlayBootConfig(remote)
		if err != nil || !bytes.Equal(reloaded.original, eyeOverlayBootContents(original, true)) {
			t.Fatalf("saved eye settings did not survive rereading: %+v error=%v", reloaded, err)
		}
		if err := config.restore(remote); err != nil || remote.bootExists != (original != nil) || !bytes.Equal(remote.boot, original) {
			t.Fatalf("could not restore the exact previous file: %q error=%v", remote.boot, err)
		}
	}
	for _, platform := range []string{"invalid", `{}`, `{"DataPlatformCachePath":"/tmp/cache"}`, `{"DataPlatformCachePath":"/data/../../etc"}`, `{"DataPlatformCachePath":"/data/cache\ninvalid"}`} {
		remote := &fakeEyeRemote{platform: platform}
		if _, err := readEyeOverlayBootConfig(remote); err == nil {
			t.Errorf("accepted invalid boot settings directory: %s", platform)
		}
	}
	remote := &fakeEyeRemote{bootExists: true, boot: bytes.Repeat([]byte("a"), 1024*1024+1)}
	if _, err := readEyeOverlayBootConfig(remote); err == nil {
		t.Fatal("accepted an oversized boot settings file")
	}
}

func TestDisablePersistentEyeOverlayAndRollback(t *testing.T) {
	for _, scenario := range []string{"success", "no_original", "save_failure", "console_failure"} {
		t.Run(scenario, func(t *testing.T) {
			enabled, failed := true, false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "console_failure" && !failed {
					failed = true
					fmt.Fprint(w, "Error setting variable<br>")
					return
				}
				enabled = r.URL.Query().Get("value") == "1"
				fmt.Fprint(w, r.URL.Query().Get("value"))
			}))
			defer server.Close()
			original := []byte("OtherSetting = 7\nProcFace_CustomEyes = true\n")
			remote := &fakeEyeRemote{boot: original, bootExists: scenario != "no_original", failBoot: scenario == "save_failure"}
			console := eyeConsole{baseURL: server.URL, client: server.Client()}
			err := disablePersistentEyeOverlay(context.Background(), console, remote, eyeOverlayState{Enabled: true, Flavor: 8, Opacity: 1})
			if scenario == "success" || scenario == "no_original" {
				if err != nil || enabled || !bytes.Contains(remote.boot, []byte("ProcFace_CustomEyes = false\n")) {
					t.Fatalf("matching eyes were not saved for reboot: enabled=%v file=%q error=%v", enabled, remote.boot, err)
				}
			} else if err == nil || !enabled || !bytes.Equal(remote.boot, original) {
				t.Fatalf("failed disable did not restore runtime and boot settings: enabled=%v file=%q error=%v", enabled, remote.boot, err)
			}
		})
	}
}

type localEyeBootShell struct{ executable string }

func (remote localEyeBootShell) Run(command string, data []byte) (string, error) {
	process := exec.Command(remote.executable, "-c", command)
	process.Stdin = bytes.NewReader(data)
	output, err := process.CombinedOutput()
	return string(output), err
}

func TestEyeOverlayBootFileTransactionWithShell(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		// Git for Windows includes a POSIX shell even when it is not on PATH.
		if git, gitErr := exec.LookPath("git"); gitErr == nil {
			candidate := filepath.Join(filepath.Dir(git), "..", "bin", "bash.exe")
			if _, statErr := os.Stat(candidate); statErr == nil {
				shell, err = candidate, nil
			}
		}
	}
	if err != nil {
		t.Skip("no POSIX shell available for the remote-file integration test")
	}
	remote := localEyeBootShell{executable: shell}
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint("existing_", existing), func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "Vector's consoleVarsAnim.ini")
			original := []byte("; unrelated preference\nOtherSetting = 7\n")
			config := eyeOverlayBootConfig{path: filepath.ToSlash(filename), existed: existing}
			if existing {
				config.original = original
				if err := os.WriteFile(filename, original, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := config.save(remote, true); err != nil {
				t.Fatal(err)
			}
			written, err := os.ReadFile(filename)
			if err != nil || !bytes.Equal(written, eyeOverlayBootContents(config.original, true)) {
				t.Fatalf("actual shell did not install the boot settings: %q error=%v", written, err)
			}
			if existing {
				backup, err := os.ReadFile(filename + ".rocket-pod-backup")
				if err != nil || !bytes.Equal(backup, original) {
					t.Fatalf("actual shell did not back up the original: %q error=%v", backup, err)
				}
			}
			if err := config.restore(remote); err != nil {
				t.Fatal(err)
			}
			restored, err := os.ReadFile(filename)
			if existing && (err != nil || !bytes.Equal(restored, original)) {
				t.Fatalf("actual shell did not restore the original: %q error=%v", restored, err)
			}
			if !existing && !os.IsNotExist(err) {
				t.Fatalf("actual shell left the new settings file behind: %v", err)
			}
		})
	}
}
