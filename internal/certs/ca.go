package certs

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"
)

// Manager 定义了当前模块中的 Manager 类型。
type Manager struct {
	// caCert 表示当前声明中的 caCert。
	caCert *x509.Certificate
	// caKey 表示当前声明中的 caKey。
	caKey crypto.PrivateKey
	// caCertPEM 保存可注入宿主信任存储的 CA 证书，不包含私钥。
	caCertPEM []byte

	// mu 表示当前声明中的 mu。
	mu sync.Mutex
	// cache 表示当前声明中的 cache。
	cache map[string]*tls.Certificate
}

// NewManager 用于处理与 NewManager 相关的逻辑。
func NewManager(caCertPath, caKeyPath string) (*Manager, error) {
	certPEM, keyPEM, err := loadCAPEMFromFiles(caCertPath, caKeyPath)
	if err != nil {
		return nil, err
	}
	return NewManagerFromPEM(certPEM, keyPEM)
}

// NewManagerFromPEM 用于处理与 NewManagerFromPEM 相关的逻辑。
func NewManagerFromPEM(caCertPEM, caKeyPEM []byte) (*Manager, error) {
	caCert, caKey, err := loadCAFromPEM(caCertPEM, caKeyPEM)
	if err != nil {
		return nil, err
	}
	return &Manager{
		caCert:    caCert,
		caKey:     caKey,
		caCertPEM: cloneBytes(caCertPEM),
		cache:     make(map[string]*tls.Certificate),
	}, nil
}

// CACertPEM 返回当前 Manager 使用的 CA 证书。返回值不包含私钥。
func (m *Manager) CACertPEM() []byte {
	if m == nil {
		return nil
	}
	return cloneBytes(m.caCertPEM)
}

// CATLSCertificate 用于处理与 CATLSCertificate 相关的逻辑。
func (m *Manager) CATLSCertificate() (*tls.Certificate, error) {
	if m == nil || m.caCert == nil || m.caKey == nil {
		return nil, errors.New("CA is not initialized")
	}
	return &tls.Certificate{
		Certificate: [][]byte{append([]byte(nil), m.caCert.Raw...)},
		PrivateKey:  m.caKey,
		Leaf:        m.caCert,
	}, nil
}

// CertificateForServerName 用于处理与 CertificateForServerName 相关的逻辑。
func (m *Manager) CertificateForServerName(serverName string) (*tls.Certificate, error) {
	host := normalizeHost(serverName)
	if host == "" {
		return nil, errors.New("empty server name")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if cert, ok := m.cache[host]; ok {
		return cert, nil
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	leaf := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   host,
			Organization: []string{"Cursor Local Proxy"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if len(m.caCert.SubjectKeyId) > 0 {
		leaf.AuthorityKeyId = append([]byte(nil), m.caCert.SubjectKeyId...)
	}

	if ip := net.ParseIP(host); ip != nil {
		leaf.IPAddresses = []net.IP{ip}
	} else {
		leaf.DNSNames = []string{host}
	}

	leafPrivateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	leafPublicKey := &leafPrivateKey.PublicKey

	der, err := x509.CreateCertificate(rand.Reader, leaf, m.caCert, leafPublicKey, m.caKey)
	if err != nil {
		return nil, err
	}

	leafCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	chainPEM := append([]byte(nil), leafCertPEM...)
	chainPEM = append(chainPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: m.caCert.Raw})...)

	keyPEM, err := marshalPrivateKeyPEM(leafPrivateKey)
	if err != nil {
		return nil, err
	}

	pair, err := tls.X509KeyPair(chainPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	parsedLeaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	pair.Leaf = parsedLeaf

	m.cache[host] = &pair
	return &pair, nil
}

// marshalPrivateKeyPEM 用于处理与 marshalPrivateKeyPEM 相关的逻辑。
func marshalPrivateKeyPEM(key any) ([]byte, error) {
	switch k := key.(type) {
	case *rsa.PrivateKey:
		return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}), nil
	case *ecdsa.PrivateKey:
		der, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			return nil, err
		}
		return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
	case ed25519.PrivateKey:
		der, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			return nil, err
		}
		return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
	default:
		return nil, errors.New("unsupported private key type")
	}
}

// loadCAFromPEM 用于处理与 loadCAFromPEM 相关的逻辑。
func loadCAFromPEM(certPEM, keyPEM []byte) (*x509.Certificate, crypto.PrivateKey, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, errors.New("invalid CA cert PEM")
	}
	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, errors.New("invalid CA key PEM")
	}

	var caKey crypto.PrivateKey
	switch keyBlock.Type {
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, nil, err
		}
		caKey = key
	case "EC PRIVATE KEY":
		key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, nil, err
		}
		caKey = key
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, nil, err
		}
		caKey = key
	default:
		return nil, nil, errors.New("unsupported CA key format")
	}

	if !caCert.IsCA || !caCert.BasicConstraintsValid || caCert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, nil, errors.New("certificate is not a valid signing CA")
	}
	signer, ok := caKey.(crypto.Signer)
	if !ok {
		return nil, nil, errors.New("CA private key cannot sign certificates")
	}
	certPublicKey, err := x509.MarshalPKIXPublicKey(caCert.PublicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal CA certificate public key: %w", err)
	}
	privatePublicKey, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return nil, nil, fmt.Errorf("marshal CA private key public key: %w", err)
	}
	if !bytes.Equal(certPublicKey, privatePublicKey) {
		return nil, nil, errors.New("CA certificate and private key do not match")
	}
	return caCert, caKey, nil
}

// normalizeHost 用于处理与 normalizeHost 相关的逻辑。
func normalizeHost(serverName string) string {
	serverName = strings.TrimSpace(serverName)
	if strings.Contains(serverName, ":") {
		h, _, err := net.SplitHostPort(serverName)
		if err == nil {
			serverName = h
		}
	}
	return serverName
}

// cloneBytes 用于处理与 cloneBytes 相关的逻辑。
func cloneBytes(src []byte) []byte {
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}
