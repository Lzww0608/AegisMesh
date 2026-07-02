package security

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func TestSecureGRPCServerAuthorizesMTLSCertificatePrincipal(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := writeCertificateAuthority(t, dir)
	serverCert, serverKey := writeLeafCertificate(t, dir, "server", caCert, caKey, []string{"controller.test"})
	adminClientCert, adminClientKey := writeLeafCertificateWithURIs(t, dir, "admin-client", caCert, caKey, nil, []string{"spiffe://aegis/controller/admin"})
	unknownClientCert, unknownClientKey := writeLeafCertificateWithURIs(t, dir, "unknown-client", caCert, caKey, nil, []string{"spiffe://aegis/controller/unknown"})
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
	auth := NewControllerPrincipalTokenAuthenticatorWithMTLS(
		map[string]Principal{"valid-token": {Role: RoleAdmin}},
		map[string]Principal{"uri:spiffe://aegis/controller/admin": {Role: RoleAdmin}},
	)
	server := grpc.NewServer(
		grpc.Creds(serverCreds),
		grpc.ChainUnaryInterceptor(auth.UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(auth.StreamServerInterceptor()),
	)
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(server, healthServer)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = server.Serve(lis) }()
	defer server.Stop()

	adminClient := dialHealthClient(t, lis.Addr().String(), ClientConfig{
		TLS: TLSConfig{
			CertFile:   adminClientCert,
			KeyFile:    adminClientKey,
			CAFile:     caPath,
			ServerName: "controller.test",
		},
	})
	defer adminClient.close()
	if _, err := adminClient.client.Check(context.Background(), &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("expected mapped mTLS admin request to pass: %v", err)
	}

	unknownClient := dialHealthClient(t, lis.Addr().String(), ClientConfig{
		TLS: TLSConfig{
			CertFile:   unknownClientCert,
			KeyFile:    unknownClientKey,
			CAFile:     caPath,
			ServerName: "controller.test",
		},
	})
	defer unknownClient.close()
	_, err = unknownClient.client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	assertStatusCode(t, err, codes.Unauthenticated)

	invalidTokenClient := dialHealthClient(t, lis.Addr().String(), ClientConfig{
		TLS: TLSConfig{
			CertFile:   adminClientCert,
			KeyFile:    adminClientKey,
			CAFile:     caPath,
			ServerName: "controller.test",
		},
		AuthToken: "invalid-token",
	})
	defer invalidTokenClient.close()
	_, err = invalidTokenClient.client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	assertStatusCode(t, err, codes.Unauthenticated)
}
func TestSecureGRPCServerRequiresMTLSAndAuthorizedToken(t *testing.T) {
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
	auth := NewControllerTokenAuthenticator(map[string]Role{
		"admin-token":  RoleAdmin,
		"reader-token": RoleReader,
	})
	server := grpc.NewServer(
		grpc.Creds(serverCreds),
		grpc.ChainUnaryInterceptor(auth.UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(auth.StreamServerInterceptor()),
	)
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(server, healthServer)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = server.Serve(lis) }()
	defer server.Stop()

	adminClient := dialHealthClient(t, lis.Addr().String(), ClientConfig{
		TLS: TLSConfig{
			CertFile:   clientCert,
			KeyFile:    clientKey,
			CAFile:     caPath,
			ServerName: "controller.test",
		},
		AuthToken: "admin-token",
	})
	defer adminClient.close()
	if _, err := adminClient.client.Check(context.Background(), &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("expected mTLS admin request to pass: %v", err)
	}

	readerClient := dialHealthClient(t, lis.Addr().String(), ClientConfig{
		TLS: TLSConfig{
			CertFile:   clientCert,
			KeyFile:    clientKey,
			CAFile:     caPath,
			ServerName: "controller.test",
		},
		AuthToken: "reader-token",
	})
	defer readerClient.close()
	_, err = readerClient.client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	assertStatusCode(t, err, codes.PermissionDenied)

	noTokenClient := dialHealthClient(t, lis.Addr().String(), ClientConfig{
		TLS: TLSConfig{
			CertFile:   clientCert,
			KeyFile:    clientKey,
			CAFile:     caPath,
			ServerName: "controller.test",
		},
	})
	defer noTokenClient.close()
	_, err = noTokenClient.client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	assertStatusCode(t, err, codes.Unauthenticated)

	_, err = dialHealthClientConn(lis.Addr().String(), ClientConfig{
		TLS: TLSConfig{
			CAFile:     caPath,
			ServerName: "controller.test",
		},
		AuthToken: "admin-token",
	})
	if err == nil {
		t.Fatalf("expected mTLS server to reject client without certificate")
	}
}

type healthClientConn struct {
	client healthpb.HealthClient
	close  func() error
}

func dialHealthClient(t *testing.T, addr string, cfg ClientConfig) healthClientConn {
	t.Helper()
	conn, err := dialHealthClientConn(addr, cfg)
	if err != nil {
		t.Fatalf("dial health client: %v", err)
	}
	return healthClientConn{client: healthpb.NewHealthClient(conn), close: conn.Close}
}

func dialHealthClientConn(addr string, cfg ClientConfig) (*grpc.ClientConn, error) {
	opts, err := ClientDialOptions(cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return grpc.DialContext(ctx, addr, append(opts, grpc.WithBlock())...)
}

func assertStatusCode(t *testing.T, err error, code codes.Code) {
	t.Helper()
	if status.Code(err) != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}
