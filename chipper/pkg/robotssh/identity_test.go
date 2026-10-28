package robotssh

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

func TestSharedKeyIsRememberedOnlyForTheVerifiedVector(t *testing.T) {
	previous := vars.JdocsDir
	t.Cleanup(func() { vars.JdocsDir = previous })
	vars.JdocsDir = t.TempDir()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	key := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})
	for _, scenario := range []struct {
		name, expected, device, uname string
		failure                       bool
	}{
		{"initial_setup", "", "00aa0001\n", "Linux Vector", false},
		{"existing_robot", "00AA0001", "00aa0001", "Linux Vector", false},
		{"wrong_robot", "00aa0002", "00aa0001", "Linux Vector", true},
		{"invalid_serial", "", "not a serial", "Linux Vector", true},
		{"wrong_device", "", "00aa0001", "Linux desktop", true},
		{"unreadable_serial", "", "", "Linux Vector", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			run := func(command string) (string, error) {
				switch command {
				case "uname -a":
					return scenario.uname, nil
				case "/bin/emr-cat e":
					return scenario.device, nil
				default:
					return "", fmt.Errorf("unexpected remote command")
				}
			}
			serial, err := VerifyAndRememberKey(scenario.expected, key, true, run)
			if scenario.failure {
				if err == nil {
					t.Fatal("remembered an unverified device")
				}
				if HasSavedKey("00aa0002") {
					t.Fatal("stored another robot's key under the selected serial")
				}
				return
			}
			if err != nil || serial != "00aa0001" {
				t.Fatalf("could not identify Vector: %q %v", serial, err)
			}
			// All three consumers load this same on-disk key by serial.
			for _, endpoint := range []string{"/api-sdk/eye_overlay", "/api-ssh/repair_voice", "/api-ssh/setup"} {
				loaded, uploaded, err := ReadKey(httptest.NewRequest(http.MethodPost, endpoint, nil), serial)
				if err != nil || uploaded || !bytes.Equal(loaded, key) {
					t.Fatalf("shared key unavailable at %s: %v", endpoint, err)
				}
			}
		})
	}
	run := func(command string) (string, error) {
		if command == "uname -a" {
			return "Linux Vector", nil
		}
		return "00aa0001", nil
	}
	if _, err := VerifyAndRememberKey("00aa0001", []byte("bad replacement"), true, run); err == nil {
		t.Fatal("accepted an invalid replacement")
	}
	loaded, _, err := ReadKey(httptest.NewRequest(http.MethodPost, "/", nil), "00aa0001")
	if err != nil || !bytes.Equal(loaded, key) {
		t.Fatal("invalid replacement changed the shared key")
	}
}
