package certs

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadOrCreateManagerPersistsAndReusesInstallationCA(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")

	manager, certPEM, err := LoadOrCreateManager(certPath, keyPath)
	if err != nil {
		t.Fatalf("LoadOrCreateManager() error = %v", err)
	}
	if isLegacySharedCA(certPEM) {
		t.Fatal("generated CA reused the legacy shared certificate")
	}
	keyInfo, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat private key: %v", err)
	}
	if runtime.GOOS != "windows" && keyInfo.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode = %o, want 600", keyInfo.Mode().Perm())
	}

	leaf, err := manager.CertificateForServerName("api2.cursor.sh")
	if err != nil {
		t.Fatalf("CertificateForServerName() error = %v", err)
	}
	ca := parseCertificatePEM(t, certPEM)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{DNSName: "api2.cursor.sh", Roots: roots}); err != nil {
		t.Fatalf("verify generated leaf: %v", err)
	}

	_, reusedCertPEM, err := LoadOrCreateManager(certPath, keyPath)
	if err != nil {
		t.Fatalf("second LoadOrCreateManager() error = %v", err)
	}
	if !bytes.Equal(certPEM, reusedCertPEM) {
		t.Fatal("installation CA changed between loads")
	}
}

func TestLoadOrCreateManagerCreatesUniqueCAsPerInstallation(t *testing.T) {
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	_, firstCert, err := LoadOrCreateManager(filepath.Join(firstDir, "ca.crt"), filepath.Join(firstDir, "ca.key"))
	if err != nil {
		t.Fatalf("create first CA: %v", err)
	}
	_, secondCert, err := LoadOrCreateManager(filepath.Join(secondDir, "ca.crt"), filepath.Join(secondDir, "ca.key"))
	if err != nil {
		t.Fatalf("create second CA: %v", err)
	}
	if bytes.Equal(firstCert, secondCert) {
		t.Fatal("separate installations received the same CA certificate")
	}
}

func TestLoadOrCreateManagerReplacesLegacySharedCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")
	legacyCert, err := os.ReadFile(filepath.Join("testdata", "legacy_shared_ca.crt"))
	if err != nil {
		t.Fatalf("read legacy certificate fixture: %v", err)
	}
	if !isLegacySharedCA(legacyCert) {
		t.Fatal("legacy certificate fixture fingerprint changed")
	}
	if err := os.WriteFile(certPath, legacyCert, 0o644); err != nil {
		t.Fatalf("write legacy certificate: %v", err)
	}

	_, generatedCert, err := LoadOrCreateManager(certPath, keyPath)
	if err != nil {
		t.Fatalf("migrate legacy certificate: %v", err)
	}
	if bytes.Equal(legacyCert, generatedCert) || isLegacySharedCA(generatedCert) {
		t.Fatal("legacy shared certificate was not replaced")
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("generated private key missing: %v", err)
	}
}

func TestNewManagerFromPEMRejectsMismatchedKey(t *testing.T) {
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	_, _, err := LoadOrCreateManager(filepath.Join(firstDir, "ca.crt"), filepath.Join(firstDir, "ca.key"))
	if err != nil {
		t.Fatalf("create first CA: %v", err)
	}
	_, _, err = LoadOrCreateManager(filepath.Join(secondDir, "ca.crt"), filepath.Join(secondDir, "ca.key"))
	if err != nil {
		t.Fatalf("create second CA: %v", err)
	}
	certPEM, _ := os.ReadFile(filepath.Join(firstDir, "ca.crt"))
	keyPEM, _ := os.ReadFile(filepath.Join(secondDir, "ca.key"))
	if _, err := NewManagerFromPEM(certPEM, keyPEM); err == nil {
		t.Fatal("NewManagerFromPEM() accepted a mismatched private key")
	}
}

func parseCertificatePEM(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("certificate PEM is invalid")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return cert
}
