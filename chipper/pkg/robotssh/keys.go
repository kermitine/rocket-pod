package robotssh

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"golang.org/x/crypto/ssh"
)

const KeyLimit = 64 * 1024

func KeyPath(serial string) string {
	// The serial identifies the robot even after its IP changes. Hashing also
	// keeps robot identifiers from becoming filesystem path components.
	id := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(serial))))
	return filepath.Join(vars.JdocsDir, "ssh-keys", fmt.Sprintf("%x.key", id))
}

func HasSavedKey(serial string) bool {
	info, err := os.Stat(KeyPath(serial))
	return err == nil && info.Mode().IsRegular() && info.Size() > 0 && info.Size() <= KeyLimit
}

func ReadKey(r *http.Request, serial string) ([]byte, bool, error) {
	file, _, err := r.FormFile("key")
	uploaded := err == nil
	if errors.Is(err, http.ErrMissingFile) || errors.Is(err, http.ErrNotMultipart) {
		file, err = os.Open(KeyPath(serial))
		if os.IsNotExist(err) {
			return nil, false, fmt.Errorf("select Vector's SSH private-key file once to save it in Rocket Pod")
		}
		if err != nil {
			return nil, false, fmt.Errorf("could not read Vector's saved SSH key; upload a new key")
		}
	} else if err != nil {
		return nil, false, fmt.Errorf("could not read the uploaded SSH key")
	}
	defer file.Close()
	key, err := io.ReadAll(io.LimitReader(file, KeyLimit+1))
	if err != nil || len(key) == 0 || len(key) > KeyLimit {
		return key, uploaded, fmt.Errorf("unable to read SSH key (maximum 64 KB)")
	}
	return key, uploaded, nil
}

// Call only after the uploaded key has authenticated to Vector. A failed
// replacement must leave the last working key available.
func SaveKey(serial string, key []byte) error {
	if strings.TrimSpace(serial) == "" {
		return fmt.Errorf("cannot save an SSH key without Vector's serial number")
	}
	if len(key) > KeyLimit {
		return fmt.Errorf("SSH key exceeds 64 KB")
	}
	if _, err := ssh.ParsePrivateKey(key); err != nil {
		return fmt.Errorf("use Vector's unencrypted SSH private-key file")
	}
	filename := KeyPath(serial)
	directory := filepath.Dir(filename)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("could not create the SSH key directory: %w", err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return fmt.Errorf("could not set SSH key directory permissions: %w", err)
	}
	staged, err := os.CreateTemp(directory, ".ssh-key-*")
	if err != nil {
		return fmt.Errorf("could not stage the SSH key: %w", err)
	}
	defer func() {
		staged.Close()
		os.Remove(staged.Name())
	}()
	if err := staged.Chmod(0600); err != nil {
		return fmt.Errorf("could not set SSH key file permissions: %w", err)
	}
	if _, err := staged.Write(key); err != nil {
		return fmt.Errorf("could not write the SSH key: %w", err)
	}
	if err := staged.Sync(); err != nil {
		return fmt.Errorf("could not flush the SSH key: %w", err)
	}
	if err := staged.Close(); err != nil {
		return fmt.Errorf("could not close the SSH key file: %w", err)
	}
	if err := os.Rename(staged.Name(), filename); err != nil {
		return fmt.Errorf("could not replace the saved SSH key: %w", err)
	}
	return nil
}
