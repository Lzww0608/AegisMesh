package security

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"google.golang.org/grpc/credentials"
)

// TLSConfig describes file-backed TLS material for controller clients and servers.
type TLSConfig struct {
	CertFile          string
	KeyFile           string
	CAFile            string
	ServerName        string
	RequireClientCert bool
}

func (c TLSConfig) Enabled() bool {
	return c.CertFile != "" || c.KeyFile != "" || c.CAFile != "" || c.ServerName != "" || c.RequireClientCert
}

func ServerTransportCredentials(cfg TLSConfig) (credentials.TransportCredentials, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	if cfg.CertFile == "" || cfg.KeyFile == "" {
		return nil, fmt.Errorf("server TLS requires both cert and key files")
	}
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, err
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	if cfg.CAFile != "" {
		pool, err := certPoolFromFile(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		tlsCfg.ClientCAs = pool
		tlsCfg.ClientAuth = tls.VerifyClientCertIfGiven
	}
	if cfg.RequireClientCert {
		if cfg.CAFile == "" {
			return nil, fmt.Errorf("mTLS requires a CA file")
		}
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return credentials.NewTLS(tlsCfg), nil
}

func ClientTLSConfig(cfg TLSConfig) (*tls.Config, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		ServerName:   cfg.ServerName,
		RootCAs:      nil,
		Certificates: nil,
	}
	if cfg.CAFile != "" {
		pool, err := certPoolFromFile(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		tlsCfg.RootCAs = pool
	}
	if cfg.CertFile != "" || cfg.KeyFile != "" {
		if cfg.CertFile == "" || cfg.KeyFile == "" {
			return nil, fmt.Errorf("client TLS requires both cert and key files")
		}
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, err
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	return tlsCfg, nil
}

func ClientTransportCredentials(cfg TLSConfig) (credentials.TransportCredentials, error) {
	tlsCfg, err := ClientTLSConfig(cfg)
	if err != nil || tlsCfg == nil {
		return nil, err
	}
	return credentials.NewTLS(tlsCfg), nil
}

func certPoolFromFile(path string) (*x509.CertPool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("no PEM certificates found in %s", path)
	}
	return pool, nil
}
