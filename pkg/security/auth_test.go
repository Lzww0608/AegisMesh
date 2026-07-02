package security

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"net/url"
	"testing"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func TestParseStaticTokens(t *testing.T) {
	tokens, err := ParseStaticTokens("admin=root, registry=reg ; reader=read")
	if err != nil {
		t.Fatalf("parse tokens: %v", err)
	}
	if tokens["root"] != RoleAdmin || tokens["reg"] != RoleRegistry || tokens["read"] != RoleReader {
		t.Fatalf("unexpected token map: %+v", tokens)
	}
}

func TestParseStaticTokensRejectsInvalidInput(t *testing.T) {
	tests := []string{
		"admin",
		"unknown=secret",
		"reader=",
		"reader=same,policy=same",
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseStaticTokens(raw); err == nil {
				t.Fatalf("expected %q to be rejected", raw)
			}
		})
	}
}

func TestParseStaticMTLSPrincipals(t *testing.T) {
	principals, err := ParseStaticMTLSPrincipals("sdk:user-service=spiffe://aegis/ns/default/sa/user, reader=dns:OPS.EXAMPLE, policy=cn:policy-client")
	if err != nil {
		t.Fatalf("parse mtls principals: %v", err)
	}
	if got := principals["uri:spiffe://aegis/ns/default/sa/user"]; got.Role != RoleSDK || got.Global() || !got.allowsService("user-service") {
		t.Fatalf("unexpected spiffe principal: %+v", got)
	}
	if got := principals["dns:ops.example"]; got.Role != RoleReader || !got.Global() {
		t.Fatalf("unexpected dns principal: %+v", got)
	}
	if got := principals["cn:policy-client"]; got.Role != RolePolicy || !got.Global() {
		t.Fatalf("unexpected cn principal: %+v", got)
	}
}

func TestParseStaticMTLSPrincipalsRejectsInvalidInput(t *testing.T) {
	tests := []string{
		"sdk:user-service=",
		"admin:user-service=cn:root",
		"reader=subject-without-prefix",
		"reader=dns:",
		"reader=cn:same,policy=cn:same",
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseStaticMTLSPrincipals(raw); err == nil {
				t.Fatalf("expected %q to be rejected", raw)
			}
		})
	}
}
func TestTokenAuthenticatorAllowsConfiguredRole(t *testing.T) {
	auth := NewControllerTokenAuthenticator(map[string]Role{"reg-token": RoleRegistry})
	resp, err := auth.UnaryServerInterceptor()(
		incomingTokenContext("reg-token"),
		nil,
		&grpc.UnaryServerInfo{FullMethod: aegisv1.RegistryService_RegisterInstance_FullMethodName},
		func(context.Context, any) (any, error) { return "ok", nil },
	)
	if err != nil {
		t.Fatalf("expected registry role to be allowed: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("unexpected response %v", resp)
	}
}

func TestTokenAuthenticatorSDKRoleHasLeastPrivilegeForSDKControlPlane(t *testing.T) {
	auth := NewControllerTokenAuthenticator(map[string]Role{"sdk-token": RoleSDK})
	interceptor := auth.UnaryServerInterceptor()
	handler := func(context.Context, any) (any, error) { return "ok", nil }

	allowed := []string{
		aegisv1.RegistryService_ListInstances_FullMethodName,
		aegisv1.TelemetryService_ReportEndpointStats_FullMethodName,
		aegisv1.PolicyService_GetPolicy_FullMethodName,
	}
	for _, method := range allowed {
		if _, err := interceptor(incomingTokenContext("sdk-token"), nil, &grpc.UnaryServerInfo{FullMethod: method}, handler); err != nil {
			t.Fatalf("expected sdk role to access %s: %v", method, err)
		}
	}

	_, err := interceptor(incomingTokenContext("sdk-token"), nil, &grpc.UnaryServerInfo{FullMethod: aegisv1.RegistryService_RegisterInstance_FullMethodName}, handler)
	assertCode(t, err, codes.PermissionDenied)
}
func TestTokenAuthenticatorRejectsMissingInvalidAndWrongRole(t *testing.T) {
	auth := NewControllerTokenAuthenticator(map[string]Role{
		"reg-token":   RoleRegistry,
		"admin-token": RoleAdmin,
	})
	interceptor := auth.UnaryServerInterceptor()
	handler := func(context.Context, any) (any, error) { return nil, nil }

	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: aegisv1.RegistryService_RegisterInstance_FullMethodName}, handler)
	assertCode(t, err, codes.Unauthenticated)

	_, err = interceptor(incomingTokenContext("bad-token"), nil, &grpc.UnaryServerInfo{FullMethod: aegisv1.RegistryService_RegisterInstance_FullMethodName}, handler)
	assertCode(t, err, codes.Unauthenticated)

	_, err = interceptor(incomingTokenContext("reg-token"), nil, &grpc.UnaryServerInfo{FullMethod: aegisv1.TelemetryService_ReportEndpointStats_FullMethodName}, handler)
	assertCode(t, err, codes.PermissionDenied)

	_, err = interceptor(incomingTokenContext("reg-token"), nil, &grpc.UnaryServerInfo{FullMethod: "/aegis.v1.NewService/NewMethod"}, handler)
	assertCode(t, err, codes.PermissionDenied)

	if _, err = interceptor(incomingTokenContext("admin-token"), nil, &grpc.UnaryServerInfo{FullMethod: "/aegis.v1.NewService/NewMethod"}, handler); err != nil {
		t.Fatalf("expected admin to access unknown method: %v", err)
	}
}

func TestParseStaticTokenPrincipalsScoped(t *testing.T) {
	tokens, err := ParseStaticTokenPrincipals("sdk:user-service+order-service=sdk, reader=read")
	if err != nil {
		t.Fatalf("parse scoped tokens: %v", err)
	}

	scoped := tokens["sdk"]
	if scoped.Role != RoleSDK || scoped.Global() || !scoped.allowsService("user-service") || !scoped.allowsService("order-service") || scoped.allowsService("billing-service") {
		t.Fatalf("unexpected scoped principal: %+v", scoped)
	}
	if reader := tokens["read"]; reader.Role != RoleReader || !reader.Global() {
		t.Fatalf("unexpected global reader principal: %+v", reader)
	}

	if _, err := ParseStaticTokens("reader:user-service=read"); err == nil {
		t.Fatalf("expected legacy role parser to reject scoped token")
	}
}

func TestParseStaticTokenPrincipalsRejectsInvalidScopes(t *testing.T) {
	tests := []string{
		"admin:user-service=root",
		"reader:=read",
		"reader:user-service+=read",
		"reader:user-service+user-service=read",
		"unknown:user-service=bad",
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseStaticTokenPrincipals(raw); err == nil {
				t.Fatalf("expected %q to be rejected", raw)
			}
		})
	}
}

func TestTokenAuthenticatorEnforcesServiceScopes(t *testing.T) {
	auth := NewControllerPrincipalTokenAuthenticator(map[string]Principal{
		"sdk-user":    {Role: RoleSDK, Services: []string{"user-service"}},
		"reader-user": {Role: RoleReader, Services: []string{"user-service"}},
	})
	interceptor := auth.UnaryServerInterceptor()
	handler := func(context.Context, any) (any, error) { return "ok", nil }

	_, err := interceptor(
		incomingTokenContext("sdk-user"),
		&aegisv1.ListInstancesRequest{Service: "user-service"},
		&grpc.UnaryServerInfo{FullMethod: aegisv1.RegistryService_ListInstances_FullMethodName},
		handler,
	)
	if err != nil {
		t.Fatalf("expected scoped sdk to list its service: %v", err)
	}

	_, err = interceptor(
		incomingTokenContext("sdk-user"),
		&aegisv1.ListInstancesRequest{Service: "order-service"},
		&grpc.UnaryServerInfo{FullMethod: aegisv1.RegistryService_ListInstances_FullMethodName},
		handler,
	)
	assertCode(t, err, codes.PermissionDenied)

	_, err = interceptor(
		incomingTokenContext("sdk-user"),
		&aegisv1.GetPolicyRequest{Service: "user-service"},
		&grpc.UnaryServerInfo{FullMethod: aegisv1.PolicyService_GetPolicy_FullMethodName},
		handler,
	)
	if err != nil {
		t.Fatalf("expected scoped sdk to get its policy: %v", err)
	}

	_, err = interceptor(
		incomingTokenContext("sdk-user"),
		&aegisv1.ReportEndpointStatsRequest{Samples: []*aegisv1.EndpointStatsSample{{Service: "user-service"}}},
		&grpc.UnaryServerInfo{FullMethod: aegisv1.TelemetryService_ReportEndpointStats_FullMethodName},
		handler,
	)
	if err != nil {
		t.Fatalf("expected scoped sdk to report its telemetry: %v", err)
	}

	_, err = interceptor(
		incomingTokenContext("sdk-user"),
		&aegisv1.ReportEndpointStatsRequest{Samples: []*aegisv1.EndpointStatsSample{{Service: "user-service"}, {Service: "order-service"}}},
		&grpc.UnaryServerInfo{FullMethod: aegisv1.TelemetryService_ReportEndpointStats_FullMethodName},
		handler,
	)
	assertCode(t, err, codes.PermissionDenied)

	_, err = interceptor(
		incomingTokenContext("reader-user"),
		&aegisv1.ListEndpointHealthRequest{},
		&grpc.UnaryServerInfo{FullMethod: aegisv1.TelemetryService_ListEndpointHealth_FullMethodName},
		handler,
	)
	assertCode(t, err, codes.PermissionDenied)

	_, err = interceptor(
		incomingTokenContext("reader-user"),
		&aegisv1.ListEndpointHealthRequest{Service: "user-service"},
		&grpc.UnaryServerInfo{FullMethod: aegisv1.TelemetryService_ListEndpointHealth_FullMethodName},
		handler,
	)
	if err != nil {
		t.Fatalf("expected scoped reader to list service health: %v", err)
	}
}

func TestTokenAuthenticatorPropagatesUnaryPrincipal(t *testing.T) {
	auth := NewControllerPrincipalTokenAuthenticator(map[string]Principal{
		"sdk-user": {Role: RoleSDK, Services: []string{"user-service"}},
	})
	var got Principal
	var ok bool
	handler := func(ctx context.Context, _ any) (any, error) {
		got, ok = PrincipalFromContext(ctx)
		return "ok", nil
	}

	resp, err := auth.UnaryServerInterceptor()(
		incomingTokenContext("sdk-user"),
		&aegisv1.ListInstancesRequest{Service: "user-service"},
		&grpc.UnaryServerInfo{FullMethod: aegisv1.RegistryService_ListInstances_FullMethodName},
		handler,
	)
	if err != nil {
		t.Fatalf("expected scoped sdk request to pass: %v", err)
	}
	if resp != "ok" || !ok || got.Role != RoleSDK || !got.allowsService("user-service") || got.allowsService("order-service") {
		t.Fatalf("expected propagated sdk principal, resp=%v ok=%v principal=%+v", resp, ok, got)
	}
}

func TestTokenAuthenticatorPropagatesStreamPrincipalAfterReceive(t *testing.T) {
	auth := NewControllerPrincipalTokenAuthenticator(map[string]Principal{
		"sdk-user": {Role: RoleSDK, Services: []string{"user-service"}},
	})
	var got Principal
	var ok bool
	handler := func(_ any, stream grpc.ServerStream) error {
		var req aegisv1.WatchPolicyRequest
		if err := stream.RecvMsg(&req); err != nil {
			return err
		}
		got, ok = PrincipalFromContext(stream.Context())
		return nil
	}
	stream := &authTestServerStream{
		ctx: incomingTokenContext("sdk-user"),
		msg: &aegisv1.WatchPolicyRequest{Service: "user-service"},
	}

	if err := auth.StreamServerInterceptor()(nil, stream, &grpc.StreamServerInfo{FullMethod: aegisv1.PolicyService_WatchPolicy_FullMethodName}, handler); err != nil {
		t.Fatalf("expected scoped stream to pass: %v", err)
	}
	if !ok || got.Role != RoleSDK || !got.allowsService("user-service") || got.allowsService("order-service") {
		t.Fatalf("expected propagated stream principal, ok=%v principal=%+v", ok, got)
	}
}
func TestTokenAuthenticatorAuthorizesStreamRequestScope(t *testing.T) {
	auth := NewControllerPrincipalTokenAuthenticator(map[string]Principal{
		"sdk-user": {Role: RoleSDK, Services: []string{"user-service"}},
	})
	interceptor := auth.StreamServerInterceptor()
	handler := func(_ any, stream grpc.ServerStream) error {
		var req aegisv1.WatchPolicyRequest
		if err := stream.RecvMsg(&req); err != nil {
			return err
		}
		return nil
	}

	allowed := &authTestServerStream{
		ctx: incomingTokenContext("sdk-user"),
		msg: &aegisv1.WatchPolicyRequest{Service: "user-service"},
	}
	if err := interceptor(nil, allowed, &grpc.StreamServerInfo{FullMethod: aegisv1.PolicyService_WatchPolicy_FullMethodName}, handler); err != nil {
		t.Fatalf("expected scoped watch to pass: %v", err)
	}

	denied := &authTestServerStream{
		ctx: incomingTokenContext("sdk-user"),
		msg: &aegisv1.WatchPolicyRequest{Service: "order-service"},
	}
	err := interceptor(nil, denied, &grpc.StreamServerInfo{FullMethod: aegisv1.PolicyService_WatchPolicy_FullMethodName}, handler)
	assertCode(t, err, codes.PermissionDenied)
}
func TestTokenAuthenticatorAuthorizesMTLSPrincipalScope(t *testing.T) {
	u, err := url.Parse("spiffe://aegis/ns/default/sa/user")
	if err != nil {
		t.Fatalf("parse uri: %v", err)
	}
	auth := NewControllerPrincipalTokenAuthenticatorWithMTLS(nil, map[string]Principal{
		"uri:" + u.String(): {Role: RoleSDK, Services: []string{"user-service"}},
	})
	interceptor := auth.UnaryServerInterceptor()
	handler := func(context.Context, any) (any, error) { return "ok", nil }
	ctx := incomingCertificateContext(&x509.Certificate{URIs: []*url.URL{u}})

	_, err = interceptor(
		ctx,
		&aegisv1.ListInstancesRequest{Service: "user-service"},
		&grpc.UnaryServerInfo{FullMethod: aegisv1.RegistryService_ListInstances_FullMethodName},
		handler,
	)
	if err != nil {
		t.Fatalf("expected scoped mtls principal to list its service: %v", err)
	}

	_, err = interceptor(
		ctx,
		&aegisv1.ListInstancesRequest{Service: "order-service"},
		&grpc.UnaryServerInfo{FullMethod: aegisv1.RegistryService_ListInstances_FullMethodName},
		handler,
	)
	assertCode(t, err, codes.PermissionDenied)
}

func TestTokenAuthenticatorAuthorizesMTLSDNSAndCNIdentities(t *testing.T) {
	auth := NewControllerPrincipalTokenAuthenticatorWithMTLS(nil, map[string]Principal{
		"dns:sdk.example": {Role: RoleSDK, Services: []string{"user-service"}},
		"cn:ops-client":   {Role: RoleReader},
	})
	interceptor := auth.UnaryServerInterceptor()
	handler := func(context.Context, any) (any, error) { return "ok", nil }

	_, err := interceptor(
		incomingCertificateContext(&x509.Certificate{DNSNames: []string{"SDK.EXAMPLE"}}),
		&aegisv1.GetPolicyRequest{Service: "user-service"},
		&grpc.UnaryServerInfo{FullMethod: aegisv1.PolicyService_GetPolicy_FullMethodName},
		handler,
	)
	if err != nil {
		t.Fatalf("expected dns mtls principal to authorize policy read: %v", err)
	}

	_, err = interceptor(
		incomingCertificateContext(&x509.Certificate{Subject: pkix.Name{CommonName: "ops-client"}}),
		&aegisv1.ListEndpointHealthRequest{},
		&grpc.UnaryServerInfo{FullMethod: aegisv1.TelemetryService_ListEndpointHealth_FullMethodName},
		handler,
	)
	if err != nil {
		t.Fatalf("expected cn reader principal to authorize global health read: %v", err)
	}
}
func TestTokenAuthenticatorUsesCNOnlyWhenCertificateHasNoSAN(t *testing.T) {
	u, err := url.Parse("spiffe://aegis/ns/default/sa/user")
	if err != nil {
		t.Fatalf("parse uri: %v", err)
	}
	auth := NewControllerPrincipalTokenAuthenticatorWithMTLS(nil, map[string]Principal{
		"cn:sdk-client": {Role: RoleSDK, Services: []string{"user-service"}},
	})
	interceptor := auth.UnaryServerInterceptor()
	handler := func(context.Context, any) (any, error) { return "ok", nil }

	_, err = interceptor(
		incomingCertificateContext(&x509.Certificate{
			Subject:  pkix.Name{CommonName: "sdk-client"},
			DNSNames: []string{"sdk.example"},
		}),
		&aegisv1.GetPolicyRequest{Service: "user-service"},
		&grpc.UnaryServerInfo{FullMethod: aegisv1.PolicyService_GetPolicy_FullMethodName},
		handler,
	)
	assertCode(t, err, codes.Unauthenticated)

	_, err = interceptor(
		incomingCertificateContext(&x509.Certificate{
			Subject: pkix.Name{CommonName: "sdk-client"},
			URIs:    []*url.URL{u},
		}),
		&aegisv1.GetPolicyRequest{Service: "user-service"},
		&grpc.UnaryServerInfo{FullMethod: aegisv1.PolicyService_GetPolicy_FullMethodName},
		handler,
	)
	assertCode(t, err, codes.Unauthenticated)
}

func TestTokenAuthenticatorTokenTakesPrecedenceOverMTLSPrincipal(t *testing.T) {
	auth := NewControllerPrincipalTokenAuthenticatorWithMTLS(
		map[string]Principal{"bad-role-token": {Role: RoleRegistry}},
		map[string]Principal{"cn:sdk-client": {Role: RoleSDK, Services: []string{"user-service"}}},
	)
	_, err := auth.UnaryServerInterceptor()(
		metadata.NewIncomingContext(
			incomingCertificateContext(&x509.Certificate{Subject: pkix.Name{CommonName: "sdk-client"}}),
			metadata.Pairs("authorization", "Bearer bad-role-token"),
		),
		&aegisv1.GetPolicyRequest{Service: "user-service"},
		&grpc.UnaryServerInfo{FullMethod: aegisv1.PolicyService_GetPolicy_FullMethodName},
		func(context.Context, any) (any, error) { return "ok", nil },
	)
	assertCode(t, err, codes.PermissionDenied)
}
func TestTokenAuthenticatorInvalidTokenDoesNotFallBackToMTLSPrincipal(t *testing.T) {
	auth := NewControllerPrincipalTokenAuthenticatorWithMTLS(
		map[string]Principal{"good-token": {Role: RoleSDK, Services: []string{"user-service"}}},
		map[string]Principal{"cn:sdk-client": {Role: RoleSDK, Services: []string{"user-service"}}},
	)
	_, err := auth.UnaryServerInterceptor()(
		metadata.NewIncomingContext(
			incomingCertificateContext(&x509.Certificate{Subject: pkix.Name{CommonName: "sdk-client"}}),
			metadata.Pairs("authorization", "Bearer invalid-token"),
		),
		&aegisv1.GetPolicyRequest{Service: "user-service"},
		&grpc.UnaryServerInfo{FullMethod: aegisv1.PolicyService_GetPolicy_FullMethodName},
		func(context.Context, any) (any, error) { return "ok", nil },
	)
	assertCode(t, err, codes.Unauthenticated)
}
func TestTokenAuthenticatorMalformedTokenMetadataDoesNotFallBackToMTLSPrincipal(t *testing.T) {
	auth := NewControllerPrincipalTokenAuthenticatorWithMTLS(
		map[string]Principal{"good-token": {Role: RoleSDK, Services: []string{"user-service"}}},
		map[string]Principal{"cn:sdk-client": {Role: RoleSDK, Services: []string{"user-service"}}},
	)
	interceptor := auth.UnaryServerInterceptor()
	handler := func(context.Context, any) (any, error) { return "ok", nil }
	certCtx := incomingCertificateContext(&x509.Certificate{Subject: pkix.Name{CommonName: "sdk-client"}})

	for _, tc := range []struct {
		name   string
		header string
		value  string
	}{
		{name: "empty bearer", header: authorizationHeader, value: "Bearer "},
		{name: "missing bearer token", header: authorizationHeader, value: "Bearer"},
		{name: "non bearer authorization", header: authorizationHeader, value: "Basic abc"},
		{name: "empty legacy token", header: tokenHeader, value: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := interceptor(
				metadata.NewIncomingContext(certCtx, metadata.Pairs(tc.header, tc.value)),
				&aegisv1.GetPolicyRequest{Service: "user-service"},
				&grpc.UnaryServerInfo{FullMethod: aegisv1.PolicyService_GetPolicy_FullMethodName},
				handler,
			)
			assertCode(t, err, codes.Unauthenticated)
		})
	}
}

func TestTokenAuthenticatorRejectsUnmappedMTLSPrincipal(t *testing.T) {
	auth := NewControllerPrincipalTokenAuthenticatorWithMTLS(nil, map[string]Principal{
		"cn:known-client": {Role: RoleReader},
	})
	_, err := auth.UnaryServerInterceptor()(
		incomingCertificateContext(&x509.Certificate{Subject: pkix.Name{CommonName: "unknown-client"}}),
		&aegisv1.ListEndpointHealthRequest{},
		&grpc.UnaryServerInfo{FullMethod: aegisv1.TelemetryService_ListEndpointHealth_FullMethodName},
		func(context.Context, any) (any, error) { return "ok", nil },
	)
	assertCode(t, err, codes.Unauthenticated)
}
func TestTokenAuthenticatorRejectsUnverifiedMTLSPrincipal(t *testing.T) {
	auth := NewControllerPrincipalTokenAuthenticatorWithMTLS(nil, map[string]Principal{
		"cn:sdk-client": {Role: RoleSDK, Services: []string{"user-service"}},
	})
	_, err := auth.UnaryServerInterceptor()(
		incomingUnverifiedCertificateContext(&x509.Certificate{Subject: pkix.Name{CommonName: "sdk-client"}}),
		&aegisv1.GetPolicyRequest{Service: "user-service"},
		&grpc.UnaryServerInfo{FullMethod: aegisv1.PolicyService_GetPolicy_FullMethodName},
		func(context.Context, any) (any, error) { return "ok", nil },
	)
	assertCode(t, err, codes.Unauthenticated)
}

func TestTokenAuthenticatorAuthorizesStreamMTLSPrincipal(t *testing.T) {
	auth := NewControllerPrincipalTokenAuthenticatorWithMTLS(nil, map[string]Principal{
		"cn:sdk-client": {Role: RoleSDK, Services: []string{"user-service"}},
	})
	interceptor := auth.StreamServerInterceptor()
	handler := func(_ any, stream grpc.ServerStream) error {
		var req aegisv1.WatchPolicyRequest
		if err := stream.RecvMsg(&req); err != nil {
			return err
		}
		return nil
	}
	stream := &authTestServerStream{
		ctx: incomingCertificateContext(&x509.Certificate{Subject: pkix.Name{CommonName: "sdk-client"}}),
		msg: &aegisv1.WatchPolicyRequest{Service: "user-service"},
	}
	if err := interceptor(nil, stream, &grpc.StreamServerInfo{FullMethod: aegisv1.PolicyService_WatchPolicy_FullMethodName}, handler); err != nil {
		t.Fatalf("expected mtls scoped stream to pass: %v", err)
	}
}
func TestBearerTokenCredentials(t *testing.T) {
	creds := BearerTokenCredentials{Token: "secret"}
	md, err := creds.GetRequestMetadata(context.Background())
	if err != nil {
		t.Fatalf("request metadata: %v", err)
	}
	if md["authorization"] != "Bearer secret" {
		t.Fatalf("unexpected authorization metadata: %+v", md)
	}
	if !creds.RequireTransportSecurity() {
		t.Fatalf("expected bearer token credentials to require transport security")
	}
}

func TestClientConfigFromEnvAndMerge(t *testing.T) {
	t.Setenv("AEGIS_CONTROLLER_TLS_CA_FILE", "ca.pem")
	t.Setenv("AEGIS_CONTROLLER_AUTH_TOKEN", "from-env")
	t.Setenv("AEGIS_CONTROLLER_AUTH_ALLOW_INSECURE", "true")

	cfg := ClientConfigFromEnv("AEGIS_CONTROLLER").Merge(ClientConfig{
		TLS:       TLSConfig{ServerName: "controller.internal"},
		AuthToken: "override",
	})
	if cfg.TLS.CAFile != "ca.pem" || cfg.TLS.ServerName != "controller.internal" {
		t.Fatalf("unexpected TLS config: %+v", cfg.TLS)
	}
	if cfg.AuthToken != "override" {
		t.Fatalf("expected explicit token override, got %q", cfg.AuthToken)
	}
	if !cfg.AllowInsecureAuth {
		t.Fatalf("expected env allow-insecure flag to survive merge")
	}
}

type authTestServerStream struct {
	ctx      context.Context
	msg      any
	received bool
}

func (s *authTestServerStream) SetHeader(metadata.MD) error  { return nil }
func (s *authTestServerStream) SendHeader(metadata.MD) error { return nil }
func (s *authTestServerStream) SetTrailer(metadata.MD)       {}
func (s *authTestServerStream) Context() context.Context     { return s.ctx }
func (s *authTestServerStream) SendMsg(any) error            { return nil }

func (s *authTestServerStream) RecvMsg(m any) error {
	if s.received {
		return io.EOF
	}
	s.received = true
	switch dst := m.(type) {
	case *aegisv1.WatchPolicyRequest:
		if src, ok := s.msg.(*aegisv1.WatchPolicyRequest); ok {
			dst.Service = src.Service
		}
	case *aegisv1.WatchInstancesRequest:
		if src, ok := s.msg.(*aegisv1.WatchInstancesRequest); ok {
			dst.Service = src.Service
		}
	}
	return nil
}
func incomingCertificateContext(cert *x509.Certificate) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{cert},
			VerifiedChains:   [][]*x509.Certificate{{cert}},
		}},
	})
}

func incomingUnverifiedCertificateContext(cert *x509.Certificate) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}},
	})
}
func incomingTokenContext(token string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
}

func assertCode(t *testing.T, err error, code codes.Code) {
	t.Helper()
	if status.Code(err) != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func TestAuthorizeControllerPrincipalNoopsWithoutPrincipalAndChecksScope(t *testing.T) {
	if err := AuthorizeControllerPrincipal(context.Background(), aegisv1.RegistryService_ListInstances_FullMethodName, &aegisv1.ListInstancesRequest{Service: "order-service"}); err != nil {
		t.Fatalf("expected no principal direct call to remain compatible: %v", err)
	}
	ctx := ContextWithPrincipal(context.Background(), Principal{Role: RoleSDK, Services: []string{"user-service"}})
	if err := AuthorizeControllerPrincipal(ctx, aegisv1.RegistryService_ListInstances_FullMethodName, &aegisv1.ListInstancesRequest{Service: "user-service"}); err != nil {
		t.Fatalf("expected scoped principal to access own service: %v", err)
	}
	err := AuthorizeControllerPrincipal(ctx, aegisv1.RegistryService_ListInstances_FullMethodName, &aegisv1.ListInstancesRequest{Service: "order-service"})
	assertCode(t, err, codes.PermissionDenied)
	err = AuthorizeControllerPrincipal(ctx, aegisv1.RegistryService_RegisterInstance_FullMethodName, &aegisv1.RegisterInstanceRequest{Instance: &aegisv1.ServiceInstance{Service: "user-service"}})
	assertCode(t, err, codes.PermissionDenied)
}
