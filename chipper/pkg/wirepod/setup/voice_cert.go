package botsetup

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

var voiceCertificateMu sync.RWMutex

func voiceCertificatePaths() (string, string) {
	if vars.APIConfig.Server.EPConfig {
		if runtime.GOOS == "android" || runtime.GOOS == "ios" {
			return filepath.Join(vars.AndroidPath, "static/epod/ep.crt"), filepath.Join(vars.AndroidPath, "static/epod/ep.key")
		}
		return "./epod/ep.crt", "./epod/ep.key"
	}
	if runtime.GOOS == "android" || runtime.GOOS == "ios" {
		return filepath.Join(vars.AndroidPath, "wire-pod/certs/cert.crt"), filepath.Join(vars.AndroidPath, "wire-pod/certs/cert.key")
	}
	return vars.CertPath, vars.KeyPath
}

// Read on each new TLS handshake so a repaired certificate takes effect without
// restarting the container or interrupting existing connections.
func LoadVoiceTLSCertificate() (*tls.Certificate, error) {
	voiceCertificateMu.RLock()
	defer voiceCertificateMu.RUnlock()
	certPath, keyPath := voiceCertificatePaths()
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	return &certificate, err
}

func prepareVoiceCertificate(serverIP net.IP) ([]byte, error) {
	voiceCertificateMu.Lock()
	defer voiceCertificateMu.Unlock()
	certPath, keyPath := voiceCertificatePaths()
	certificate, loadErr := tls.LoadX509KeyPair(certPath, keyPath)
	if loadErr == nil {
		leaf, err := x509.ParseCertificate(certificate.Certificate[0])
		host := serverIP.String()
		if vars.APIConfig.Server.EPConfig {
			host = "escapepod.local"
		}
		if err == nil && leaf.VerifyHostname(host) == nil && time.Now().After(leaf.NotBefore) && time.Now().Before(leaf.NotAfter) {
			return os.ReadFile(certPath)
		}
	}
	if vars.APIConfig.Server.EPConfig {
		return nil, fmt.Errorf("the Escape Pod certificate is unavailable or invalid; restore epod/ep.crt and epod/ep.key")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	leaf := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "Rocket Pod"},
		IPAddresses: []net.IP{serverIP}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := saveVoiceCertificatePair(certPath, keyPath, certPEM, keyPEM); err != nil {
		return nil, err
	}
	vars.ChipperCert, vars.ChipperKey, vars.ChipperKeysLoaded = certPEM, keyPEM, true
	return certPEM, nil
}

func saveVoiceCertificatePair(certPath, keyPath string, cert, key []byte) error {
	if err := os.MkdirAll(filepath.Dir(certPath), 0700); err != nil {
		return err
	}
	backup, err := os.MkdirTemp(filepath.Dir(certPath), "voice-repair-backup-")
	if err != nil {
		return err
	}
	previous := make([][]byte, 2)
	existed := make([]bool, 2)
	for index, path := range []string{certPath, keyPath} {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		previous[index], existed[index] = data, true
		name := []string{"cert.crt", "cert.key"}[index]
		if err := os.WriteFile(filepath.Join(backup, name), data, 0600); err != nil {
			return err
		}
	}
	if err := writeVoiceFile(certPath, cert, 0644); err != nil {
		return err
	}
	if err := writeVoiceFile(keyPath, key, 0600); err != nil {
		if existed[0] {
			if restoreErr := writeVoiceFile(certPath, previous[0], 0644); restoreErr != nil {
				return fmt.Errorf("save key: %v; restore certificate: %v; backups: %s", err, restoreErr, backup)
			}
		} else {
			os.Remove(certPath)
		}
		return err
	}
	return nil
}

func writeVoiceFile(path string, data []byte, mode os.FileMode) error {
	staged, err := os.CreateTemp(filepath.Dir(path), ".voice-repair-*")
	if err != nil {
		return err
	}
	defer os.Remove(staged.Name())
	if err := staged.Chmod(mode); err != nil {
		staged.Close()
		return err
	}
	_, writeErr := staged.Write(data)
	closeErr := staged.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(staged.Name(), path)
}
