package security

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerAndClientTransportCredentials(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := writeCertificateAuthority(t, dir)
	serverCert, serverKey := writeLeafCertificate(t, dir, "server", caCert, caKey, []string{"controller.test"})
	clientCert, clientKey := writeLeafCertificate(t, dir, "client", caCert, caKey, nil)
	caPath := filepath.Join(dir, "ca.pem")

	serverCreds, err := ServerTransportCredentials(TLSConfig{
		CertFile:          serverCert,
		KeyFile:           serverKey,
		CAFile:            caPath,
		RequireClientCert: true,
	})
	if err != nil {
		t.Fatalf("server credentials: %v", err)
	}
	if serverCreds == nil {
		t.Fatalf("expected server credentials")
	}

	clientCreds, err := ClientTransportCredentials(TLSConfig{
		CertFile:   clientCert,
		KeyFile:    clientKey,
		CAFile:     caPath,
		ServerName: "controller.test",
	})
	if err != nil {
		t.Fatalf("client credentials: %v", err)
	}
	if clientCreds == nil {
		t.Fatalf("expected client credentials")
	}
}

func TestTransportCredentialsValidateRequiredFiles(t *testing.T) {
	if _, err := ServerTransportCredentials(TLSConfig{CertFile: "cert.pem"}); err == nil {
		t.Fatalf("expected server TLS to require cert and key")
	}
	if _, err := ClientTransportCredentials(TLSConfig{CertFile: "cert.pem"}); err == nil {
		t.Fatalf("expected client TLS to require cert and key")
	}
	if _, err := ServerTransportCredentials(TLSConfig{RequireClientCert: true}); err == nil {
		t.Fatalf("expected mTLS to require a CA file")
	}
}

func writeCertificateAuthority(t *testing.T, dir string) (*x509.Certificate, *rsa.PrivateKey) {
	t.Helper()
	key := newKey(t)
	cert := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "AegisMesh Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	writePEM(t, filepath.Join(dir, "ca.pem"), "CERTIFICATE", der)
	return cert, key
}

func writeLeafCertificate(t *testing.T, dir, name string, caCert *x509.Certificate, caKey *rsa.PrivateKey, dnsNames []string) (string, string) {
	t.Helper()
	return writeLeafCertificateWithURIs(t, dir, name, caCert, caKey, dnsNames, nil)
}

func writeLeafCertificateWithURIs(t *testing.T, dir, name string, caCert *x509.Certificate, caKey *rsa.PrivateKey, dnsNames []string, uriStrings []string) (string, string) {
	t.Helper()
	uris := make([]*url.URL, 0, len(uriStrings))
	for _, raw := range uriStrings {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse certificate URI %q: %v", raw, err)
		}
		uris = append(uris, parsed)
	}
	key := newKey(t)
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     dnsNames,
		URIs:         uris,
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create %s certificate: %v", name, err)
	}
	certPath := filepath.Join(dir, name+".pem")
	keyPath := filepath.Join(dir, name+"-key.pem")
	writePEM(t, certPath, "CERTIFICATE", der)
	writePEM(t, keyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key))
	return certPath, keyPath
}

func newKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if err := pem.Encode(file, &pem.Block{Type: typ, Bytes: der}); err != nil {
		_ = file.Close()
		t.Fatalf("write PEM %s: %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}
