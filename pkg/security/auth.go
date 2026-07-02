package security

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/url"
	"strings"
	"sync"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type Role string

const (
	RoleAdmin     Role = "admin"
	RoleRegistry  Role = "registry"
	RoleTelemetry Role = "telemetry"
	RolePolicy    Role = "policy"
	RoleReader    Role = "reader"
	RoleSDK       Role = "sdk"
)

type Principal struct {
	Role     Role
	Services []string
}

type principalContextKey struct{}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

func ContextWithPrincipal(ctx context.Context, principal Principal) context.Context {
	return contextWithPrincipal(ctx, principal)
}

func contextWithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

func (p Principal) Global() bool {
	return len(p.Services) == 0
}

func (p Principal) allowsService(service string) bool {
	if p.Global() {
		return true
	}
	if service == "" {
		return false
	}
	for _, allowed := range p.Services {
		if allowed == service {
			return true
		}
	}
	return false
}

const (
	authorizationHeader = "authorization"
	tokenHeader         = "x-aegis-token"
)

var DefaultControllerMethodRoles = map[string][]Role{
	aegisv1.RegistryService_RegisterInstance_FullMethodName:     {RoleRegistry},
	aegisv1.RegistryService_Heartbeat_FullMethodName:            {RoleRegistry},
	aegisv1.RegistryService_ListInstances_FullMethodName:        {RoleRegistry, RoleReader, RoleSDK},
	aegisv1.RegistryService_WatchInstances_FullMethodName:       {RoleRegistry, RoleReader, RoleSDK},
	aegisv1.TelemetryService_ReportEndpointStats_FullMethodName: {RoleTelemetry, RoleSDK},
	aegisv1.TelemetryService_ListEndpointHealth_FullMethodName:  {RoleTelemetry, RoleReader},
	aegisv1.PolicyService_GetPolicy_FullMethodName:              {RolePolicy, RoleReader, RoleSDK},
	aegisv1.PolicyService_WatchPolicy_FullMethodName:            {RolePolicy, RoleReader, RoleSDK},
}

type TokenAuthenticator struct {
	tokens map[string]Principal
	mtls   map[string]Principal
	rules  map[string][]Role
}

func NewTokenAuthenticator(tokens map[string]Role, rules map[string][]Role) *TokenAuthenticator {
	return NewPrincipalTokenAuthenticator(principalsFromTokenRoles(tokens), rules)
}

func NewPrincipalTokenAuthenticator(tokens map[string]Principal, rules map[string][]Role) *TokenAuthenticator {
	return NewPrincipalTokenAuthenticatorWithMTLS(tokens, nil, rules)
}

func NewPrincipalTokenAuthenticatorWithMTLS(tokens map[string]Principal, mtls map[string]Principal, rules map[string][]Role) *TokenAuthenticator {
	return &TokenAuthenticator{
		tokens: cloneTokenPrincipals(tokens),
		mtls:   cloneTokenPrincipals(mtls),
		rules:  cloneMethodRoles(rules),
	}
}

func NewControllerTokenAuthenticator(tokens map[string]Role) *TokenAuthenticator {
	return NewTokenAuthenticator(tokens, DefaultControllerMethodRoles)
}

func NewControllerPrincipalTokenAuthenticator(tokens map[string]Principal) *TokenAuthenticator {
	return NewPrincipalTokenAuthenticator(tokens, DefaultControllerMethodRoles)
}

func NewControllerPrincipalTokenAuthenticatorWithMTLS(tokens map[string]Principal, mtls map[string]Principal) *TokenAuthenticator {
	return NewPrincipalTokenAuthenticatorWithMTLS(tokens, mtls, DefaultControllerMethodRoles)
}

func (a *TokenAuthenticator) Enabled() bool {
	return a != nil && (len(a.tokens) > 0 || len(a.mtls) > 0)
}

func (a *TokenAuthenticator) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		method := ""
		if info != nil {
			method = info.FullMethod
		}
		principal, err := a.authorize(ctx, method, req)
		if err != nil {
			return nil, err
		}
		if principal.Role != "" {
			ctx = contextWithPrincipal(ctx, principal)
		}
		return handler(ctx, req)
	}
}

func (a *TokenAuthenticator) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		method := ""
		if info != nil {
			method = info.FullMethod
		}
		return handler(srv, &authorizingServerStream{
			ServerStream: stream,
			auth:         a,
			method:       method,
		})
	}
}

type authorizingServerStream struct {
	grpc.ServerStream
	auth       *TokenAuthenticator
	method     string
	mu         sync.RWMutex
	ctx        context.Context
	authorized bool
}

func (s *authorizingServerStream) Context() context.Context {
	s.mu.RLock()
	ctx := s.ctx
	s.mu.RUnlock()
	if ctx != nil {
		return ctx
	}
	return s.ServerStream.Context()
}

func (s *authorizingServerStream) RecvMsg(m any) error {
	if err := s.ServerStream.RecvMsg(m); err != nil {
		return err
	}
	s.mu.RLock()
	authorized := s.authorized
	s.mu.RUnlock()
	if authorized {
		return nil
	}
	principal, err := s.auth.authorize(s.ServerStream.Context(), s.method, m)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if !s.authorized {
		s.authorized = true
		if principal.Role != "" {
			s.ctx = contextWithPrincipal(s.ServerStream.Context(), principal)
		}
	}
	s.mu.Unlock()
	return nil
}

func (a *TokenAuthenticator) authorize(ctx context.Context, method string, req any) (Principal, error) {
	if !a.Enabled() {
		return Principal{}, nil
	}
	principal, err := a.principalFromContext(ctx)
	if err != nil {
		return Principal{}, err
	}
	if principal.Role == RoleAdmin {
		return principal, nil
	}
	if !roleAllowed(principal.Role, a.rules[method]) {
		return Principal{}, status.Error(codes.PermissionDenied, "role is not allowed for method")
	}
	if err := authorizePrincipalScope(principal, method, req); err != nil {
		return Principal{}, err
	}
	return principal, nil
}

func (a *TokenAuthenticator) principalFromContext(ctx context.Context) (Principal, error) {
	token, tokenAttempted := tokenFromContext(ctx)
	if tokenAttempted {
		if token == "" {
			return Principal{}, status.Error(codes.Unauthenticated, "invalid bearer token")
		}
		principal, ok := a.tokens[token]
		if !ok {
			return Principal{}, status.Error(codes.Unauthenticated, "invalid bearer token")
		}
		return principal, nil
	}
	if principal, ok := a.mtlsPrincipalFromContext(ctx); ok {
		return principal, nil
	}
	return Principal{}, status.Error(codes.Unauthenticated, "missing bearer token or authorized client certificate")
}

func (a *TokenAuthenticator) mtlsPrincipalFromContext(ctx context.Context) (Principal, bool) {
	if len(a.mtls) == 0 {
		return Principal{}, false
	}
	for _, identity := range certificateIdentitiesFromContext(ctx) {
		if principal, ok := a.mtls[identity]; ok {
			return principal, true
		}
	}
	return Principal{}, false
}

func certificateIdentitiesFromContext(ctx context.Context) []string {
	p, ok := peer.FromContext(ctx)
	if !ok || p.AuthInfo == nil {
		return nil
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 || len(tlsInfo.State.VerifiedChains) == 0 {
		return nil
	}
	return certificateIdentityCandidates(tlsInfo.State.PeerCertificates[0])
}

func certificateIdentityCandidates(cert *x509.Certificate) []string {
	if cert == nil {
		return nil
	}
	out := make([]string, 0, len(cert.URIs)+len(cert.DNSNames)+1)
	seen := make(map[string]struct{})
	add := func(identity string) {
		identity = strings.TrimSpace(identity)
		if identity == "" {
			return
		}
		if _, ok := seen[identity]; ok {
			return
		}
		seen[identity] = struct{}{}
		out = append(out, identity)
	}
	for _, uri := range cert.URIs {
		if uri != nil {
			add("uri:" + uri.String())
		}
	}
	for _, dns := range cert.DNSNames {
		add("dns:" + strings.ToLower(dns))
	}
	if len(cert.URIs) == 0 && len(cert.DNSNames) == 0 {
		add("cn:" + cert.Subject.CommonName)
	}
	return out
}

type requestScope struct {
	services   []string
	globalOnly bool
}

func AuthorizeControllerPrincipal(ctx context.Context, method string, req any) error {
	principal, ok := PrincipalFromContext(ctx)
	if !ok || principal.Role == "" {
		return nil
	}
	if principal.Role == RoleAdmin {
		return nil
	}
	if !roleAllowed(principal.Role, DefaultControllerMethodRoles[method]) {
		return status.Error(codes.PermissionDenied, "role is not allowed for method")
	}
	return authorizePrincipalScope(principal, method, req)
}
func authorizePrincipalScope(principal Principal, method string, req any) error {
	if principal.Global() {
		return nil
	}
	scope, ok := requestServiceScopeFor(method, req)
	if !ok {
		return status.Error(codes.PermissionDenied, "method does not expose service scope")
	}
	if scope.globalOnly {
		return status.Error(codes.PermissionDenied, "service-scoped principal cannot access cross-service request")
	}
	if len(scope.services) == 0 {
		return status.Error(codes.PermissionDenied, "service-scoped principal requires service-bound request")
	}
	for _, service := range scope.services {
		if !principal.allowsService(service) {
			return status.Error(codes.PermissionDenied, "principal is not scoped to requested service")
		}
	}
	return nil
}

func requestServiceScopeFor(method string, req any) (requestScope, bool) {
	switch method {
	case aegisv1.RegistryService_RegisterInstance_FullMethodName:
		r, _ := req.(*aegisv1.RegisterInstanceRequest)
		if r == nil || r.Instance == nil {
			return requestScope{}, true
		}
		return singleServiceScope(r.Instance.Service), true
	case aegisv1.RegistryService_Heartbeat_FullMethodName:
		r, _ := req.(*aegisv1.HeartbeatRequest)
		if r == nil {
			return requestScope{}, true
		}
		return singleServiceScope(r.Service), true
	case aegisv1.RegistryService_ListInstances_FullMethodName:
		r, _ := req.(*aegisv1.ListInstancesRequest)
		if r == nil {
			return requestScope{}, true
		}
		return singleServiceScope(r.Service), true
	case aegisv1.RegistryService_WatchInstances_FullMethodName:
		r, _ := req.(*aegisv1.WatchInstancesRequest)
		if r == nil {
			return requestScope{}, true
		}
		return singleServiceScope(r.Service), true
	case aegisv1.PolicyService_GetPolicy_FullMethodName:
		r, _ := req.(*aegisv1.GetPolicyRequest)
		if r == nil {
			return requestScope{}, true
		}
		return singleServiceScope(r.Service), true
	case aegisv1.PolicyService_WatchPolicy_FullMethodName:
		r, _ := req.(*aegisv1.WatchPolicyRequest)
		if r == nil {
			return requestScope{}, true
		}
		return singleServiceScope(r.Service), true
	case aegisv1.TelemetryService_ReportEndpointStats_FullMethodName:
		r, _ := req.(*aegisv1.ReportEndpointStatsRequest)
		if r == nil {
			return requestScope{}, true
		}
		return telemetryReportScope(r.Samples), true
	case aegisv1.TelemetryService_ListEndpointHealth_FullMethodName:
		r, _ := req.(*aegisv1.ListEndpointHealthRequest)
		if r == nil || r.Service == "" {
			return requestScope{globalOnly: true}, true
		}
		return singleServiceScope(r.Service), true
	default:
		return requestScope{}, false
	}
}

func singleServiceScope(service string) requestScope {
	if service == "" {
		return requestScope{}
	}
	return requestScope{services: []string{service}}
}

func telemetryReportScope(samples []*aegisv1.EndpointStatsSample) requestScope {
	seen := make(map[string]struct{}, 1)
	for _, sample := range samples {
		if sample == nil || sample.Service == "" {
			continue
		}
		seen[sample.Service] = struct{}{}
		if len(seen) > 1 {
			return requestScope{globalOnly: true}
		}
	}
	for service := range seen {
		return singleServiceScope(service)
	}
	return requestScope{}
}

func tokenFromContext(ctx context.Context) (string, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false
	}
	if values := md.Get(authorizationHeader); len(values) > 0 {
		for _, value := range values {
			if token, ok := bearerToken(value); ok {
				return token, true
			}
		}
		return "", true
	}
	if values := md.Get(tokenHeader); len(values) > 0 {
		for _, value := range values {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed, true
			}
		}
		return "", true
	}
	return "", false
}

func bearerToken(value string) (string, bool) {
	value = strings.TrimSpace(value)
	const prefix = "bearer "
	if len(value) <= len(prefix) || !strings.EqualFold(value[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(value[len(prefix):])
	return token, token != ""
}

func roleAllowed(role Role, allowed []Role) bool {
	for _, candidate := range allowed {
		if role == candidate {
			return true
		}
	}
	return false
}

type BearerTokenCredentials struct {
	Token         string
	AllowInsecure bool
}

var _ credentials.PerRPCCredentials = BearerTokenCredentials{}

func (c BearerTokenCredentials) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	if c.Token == "" {
		return nil, nil
	}
	return map[string]string{authorizationHeader: "Bearer " + c.Token}, nil
}

func (c BearerTokenCredentials) RequireTransportSecurity() bool {
	return !c.AllowInsecure
}

func ParseStaticTokens(raw string) (map[string]Role, error) {
	principals, err := ParseStaticTokenPrincipals(raw)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Role, len(principals))
	for token, principal := range principals {
		if !principal.Global() {
			return nil, fmt.Errorf("scoped auth token for role %q requires ParseStaticTokenPrincipals", principal.Role)
		}
		out[token] = principal.Role
	}
	return out, nil
}

func ParseStaticTokenPrincipals(raw string) (map[string]Principal, error) {
	return parseStaticPrincipalBindings(raw, false)
}

func ParseStaticMTLSPrincipals(raw string) (map[string]Principal, error) {
	return parseStaticPrincipalBindings(raw, true)
}

func parseStaticPrincipalBindings(raw string, certificateIdentity bool) (map[string]Principal, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	out := make(map[string]Principal)
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' }) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		roleRaw, subject, ok := strings.Cut(item, "=")
		if !ok {
			return nil, fmt.Errorf("auth principal entry %q must use role[:service+service]=subject", item)
		}
		role, services, err := parseRoleScope(roleRaw)
		if err != nil {
			return nil, err
		}
		subject = strings.TrimSpace(subject)
		if certificateIdentity {
			subject, err = normalizeCertificateIdentity(subject)
			if err != nil {
				return nil, err
			}
		} else if subject == "" {
			return nil, fmt.Errorf("auth token entry %q must include a token", item)
		}
		if role == RoleAdmin && len(services) > 0 {
			return nil, fmt.Errorf("admin auth role cannot be service-scoped")
		}
		if _, exists := out[subject]; exists {
			return nil, fmt.Errorf("duplicate auth principal")
		}
		out[subject] = Principal{Role: role, Services: services}
	}
	return out, nil
}

func normalizeCertificateIdentity(raw string) (string, error) {
	identity := strings.TrimSpace(raw)
	if identity == "" {
		return "", fmt.Errorf("mTLS principal entry must include a certificate identity")
	}
	lower := strings.ToLower(identity)
	switch {
	case strings.HasPrefix(lower, "spiffe://"):
		return normalizeURIIdentity(identity)
	case strings.HasPrefix(lower, "uri:"):
		return normalizeURIIdentity(strings.TrimSpace(identity[len("uri:"):]))
	case strings.HasPrefix(lower, "dns:"):
		value := strings.TrimSpace(identity[len("dns:"):])
		if value == "" {
			return "", fmt.Errorf("mTLS DNS identity cannot be empty")
		}
		return "dns:" + strings.ToLower(value), nil
	case strings.HasPrefix(lower, "cn:"):
		value := strings.TrimSpace(identity[len("cn:"):])
		if value == "" {
			return "", fmt.Errorf("mTLS CN identity cannot be empty")
		}
		return "cn:" + value, nil
	default:
		return "", fmt.Errorf("mTLS identity %q must use uri:, dns:, cn:, or spiffe://", raw)
	}
}

func normalizeURIIdentity(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("mTLS URI identity cannot be empty")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return "", fmt.Errorf("invalid mTLS URI identity %q", value)
	}
	return "uri:" + parsed.String(), nil
}

func parseRoleScope(raw string) (Role, []string, error) {
	roleRaw, scopeRaw, scoped := strings.Cut(strings.TrimSpace(raw), ":")
	role := Role(strings.TrimSpace(roleRaw))
	if role == "" {
		return "", nil, fmt.Errorf("auth token entry must include a role")
	}
	if !knownRole(role) {
		return "", nil, fmt.Errorf("unsupported auth role %q", role)
	}
	if !scoped {
		return role, nil, nil
	}
	scopeRaw = strings.TrimSpace(scopeRaw)
	if scopeRaw == "" {
		return "", nil, fmt.Errorf("auth token role %q has empty service scope", role)
	}
	seen := make(map[string]struct{})
	services := make([]string, 0, 1)
	scopeRaw = strings.ReplaceAll(scopeRaw, "+", "|")
	for _, service := range strings.Split(scopeRaw, "|") {
		service = strings.TrimSpace(service)
		if service == "" {
			return "", nil, fmt.Errorf("auth token role %q has empty service scope", role)
		}
		if _, exists := seen[service]; exists {
			return "", nil, fmt.Errorf("duplicate service scope %q", service)
		}
		seen[service] = struct{}{}
		services = append(services, service)
	}
	return role, services, nil
}

func knownRole(role Role) bool {
	switch role {
	case RoleAdmin, RoleRegistry, RoleTelemetry, RolePolicy, RoleReader, RoleSDK:
		return true
	default:
		return false
	}
}

func principalsFromTokenRoles(in map[string]Role) map[string]Principal {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]Principal, len(in))
	for token, role := range in {
		out[token] = Principal{Role: role}
	}
	return out
}

func cloneTokenRoles(in map[string]Role) map[string]Role {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]Role, len(in))
	for token, role := range in {
		out[token] = role
	}
	return out
}

func cloneTokenPrincipals(in map[string]Principal) map[string]Principal {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]Principal, len(in))
	for token, principal := range in {
		out[token] = Principal{
			Role:     principal.Role,
			Services: append([]string(nil), principal.Services...),
		}
	}
	return out
}

func cloneMethodRoles(in map[string][]Role) map[string][]Role {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]Role, len(in))
	for method, roles := range in {
		out[method] = append([]Role(nil), roles...)
	}
	return out
}
