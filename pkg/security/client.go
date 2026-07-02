package security

import (
	"os"
	"strconv"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type ClientConfig struct {
	TLS               TLSConfig
	AuthToken         string
	AllowInsecureAuth bool
}

func ClientConfigFromEnv(prefix string) ClientConfig {
	return ClientConfig{
		TLS: TLSConfig{
			CertFile:   os.Getenv(prefix + "_TLS_CERT_FILE"),
			KeyFile:    os.Getenv(prefix + "_TLS_KEY_FILE"),
			CAFile:     os.Getenv(prefix + "_TLS_CA_FILE"),
			ServerName: os.Getenv(prefix + "_TLS_SERVER_NAME"),
		},
		AuthToken:         os.Getenv(prefix + "_AUTH_TOKEN"),
		AllowInsecureAuth: parseEnvBool(prefix + "_AUTH_ALLOW_INSECURE"),
	}
}

func (c ClientConfig) Merge(override ClientConfig) ClientConfig {
	if override.TLS.CertFile != "" {
		c.TLS.CertFile = override.TLS.CertFile
	}
	if override.TLS.KeyFile != "" {
		c.TLS.KeyFile = override.TLS.KeyFile
	}
	if override.TLS.CAFile != "" {
		c.TLS.CAFile = override.TLS.CAFile
	}
	if override.TLS.ServerName != "" {
		c.TLS.ServerName = override.TLS.ServerName
	}
	if override.AuthToken != "" {
		c.AuthToken = override.AuthToken
	}
	if override.AllowInsecureAuth {
		c.AllowInsecureAuth = true
	}
	return c
}

func ClientDialOptions(cfg ClientConfig) ([]grpc.DialOption, error) {
	creds, err := ClientTransportCredentials(cfg.TLS)
	if err != nil {
		return nil, err
	}
	if creds == nil {
		creds = insecure.NewCredentials()
	}
	opts := []grpc.DialOption{grpc.WithTransportCredentials(creds)}
	if cfg.AuthToken != "" {
		opts = append(opts, grpc.WithPerRPCCredentials(BearerTokenCredentials{
			Token:         cfg.AuthToken,
			AllowInsecure: cfg.AllowInsecureAuth,
		}))
	}
	return opts, nil
}

func parseEnvBool(key string) bool {
	raw := os.Getenv(key)
	if raw == "" {
		return false
	}
	value, err := strconv.ParseBool(raw)
	return err == nil && value
}
