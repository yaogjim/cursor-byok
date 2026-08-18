package certs

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const legacySharedCASHA256 = "836E6BB84F6C3E63316DBB4EC257223AF09F7490E7AAE09030B8515ED61EE9FF"

// LoadOrCreateManager loads the installation-specific CA, generating it on
// first run. The private key is persisted only in the supplied local path.
func LoadOrCreateManager(certPath, keyPath string) (*Manager, []byte, error) {
	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)

	if certErr == nil && isLegacySharedCA(certPEM) {
		return generateAndPersistManager(certPath, keyPath)
	}
	if certErr == nil && keyErr == nil {
		manager, err := NewManagerFromPEM(certPEM, keyPEM)
		if err != nil {
			return nil, nil, fmt.Errorf("load installation CA: %w", err)
		}
		if err := os.Chmod(keyPath, 0o600); err != nil {
			return nil, nil, fmt.Errorf("restrict installation CA private key permissions: %w", err)
		}
		return manager, manager.CACertPEM(), nil
	}
	if errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist) {
		return generateAndPersistManager(certPath, keyPath)
	}
	// A key without a certificate cannot have been installed as a trusted root.
	// This is safe to recover if the first-run write was interrupted.
	if errors.Is(certErr, os.ErrNotExist) && keyErr == nil {
		return generateAndPersistManager(certPath, keyPath)
	}
	if certErr != nil && !errors.Is(certErr, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("read installation CA certificate: %w", certErr)
	}
	if keyErr != nil && !errors.Is(keyErr, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("read installation CA private key: %w", keyErr)
	}
	return nil, nil, errors.New("installation CA is incomplete; both certificate and private key are required")
}

// NewGeneratedManager creates an in-memory CA suitable for short-lived tools.
func NewGeneratedManager() (*Manager, []byte, error) {
	certPEM, keyPEM, err := generateCA()
	if err != nil {
		return nil, nil, err
	}
	manager, err := NewManagerFromPEM(certPEM, keyPEM)
	if err != nil {
		return nil, nil, err
	}
	return manager, manager.CACertPEM(), nil
}

func generateAndPersistManager(certPath, keyPath string) (*Manager, []byte, error) {
	if filepath.Dir(certPath) != filepath.Dir(keyPath) {
		return nil, nil, errors.New("installation CA certificate and key must share a directory")
	}
	certPEM, keyPEM, err := generateCA()
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return nil, nil, fmt.Errorf("create installation CA directory: %w", err)
	}
	// Write the private key first so a crash cannot leave a new certificate
	// without the signing key needed by the proxy.
	if err := writeLocalCAFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, nil, fmt.Errorf("persist installation CA private key: %w", err)
	}
	if err := writeLocalCAFile(certPath, certPEM, 0o644); err != nil {
		return nil, nil, fmt.Errorf("persist installation CA certificate: %w", err)
	}
	manager, err := NewManagerFromPEM(certPEM, keyPEM)
	if err != nil {
		return nil, nil, err
	}
	return manager, manager.CACertPEM(), nil
}

func generateCA() ([]byte, []byte, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return nil, nil, fmt.Errorf("generate installation CA private key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate installation CA serial: %w", err)
	}
	publicKeyDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal installation CA public key: %w", err)
	}
	subjectKeyID := sha256.Sum256(publicKeyDER)
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Cursor BYOK Local CA",
			Organization: []string{"Cursor BYOK"},
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		SubjectKeyId:          append([]byte(nil), subjectKeyID[:20]...),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create installation CA certificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	return certPEM, keyPEM, nil
}

func writeLocalCAFile(path string, data []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".ca-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func isLegacySharedCA(certPEM []byte) bool {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(cert.Raw)
	return strings.EqualFold(hex.EncodeToString(sum[:]), legacySharedCASHA256)
}

// loadCAPEMFromFiles reads an explicitly supplied CA pair.
func loadCAPEMFromFiles(certPath, keyPath string) ([]byte, []byte, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	return certPEM, keyPEM, nil
}
