package robotssh

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// Call after SSH authentication, before changing the robot. Initial setup has
// no authenticated BotInfo entry yet, so obtain the serial from Vector's EMR.
func VerifyAndRememberKey(expectedSerial string, key []byte, uploaded bool, run func(string) (string, error)) (string, error) {
	identity, err := run("uname -a")
	if err != nil || !strings.Contains(identity, "Vector") {
		return "", fmt.Errorf("the SSH device did not identify itself as Vector")
	}
	raw, err := run("/bin/emr-cat e")
	serial := strings.ToLower(strings.TrimSpace(raw))
	if _, decodeErr := hex.DecodeString(serial); err != nil || decodeErr != nil || len(serial) != 8 {
		return "", fmt.Errorf("could not read Vector's serial number over SSH")
	}
	if expectedSerial != "" && !strings.EqualFold(strings.TrimSpace(expectedSerial), serial) {
		return "", fmt.Errorf("the SSH device's serial number does not match the selected Vector")
	}
	if uploaded {
		if err := SaveKey(serial, key); err != nil {
			return "", fmt.Errorf("SSH connected, but the key could not be saved: %w", err)
		}
	}
	return serial, nil
}
