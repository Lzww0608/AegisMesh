package aegisgrpc

import (
	"context"
	"net"
	"testing"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestCompilePolicySnapshotIncludesCircuitBreakerMaxImmutable locks the compile policy snapshot includes circuit breaker max immutable contract so future changes do not regress it.
func TestCompilePolicySnapshotIncludesCircuitBreakerMaxImmutable(t *testing.T) {
	snapshot := &aegisv1.PolicySnapshot{
		Revision:      11,
		RoutingPolicy: string(RoutingRoundRobin),
		CircuitBreaker: &aegisv1.CircuitBreakerPolicy{
			MaxInflightPerEndpoint: 3,
		},
	}

	compiled := compilePolicySnapshot(snapshot)
	snapshot.CircuitBreaker.MaxInflightPerEndpoint = 9

	if compiled.version != 11 {
		t.Fatalf("expected revision 11, got %d", compiled.version)
	}
	if compiled.routing != RoutingRoundRobin {
		t.Fatalf("expected compiled routing to preserve snapshot value, got %s", compiled.routing)
	}
	if compiled.circuitBreaker.maxInflightPerEndpoint != 3 {
		t.Fatalf("expected immutable circuit breaker max 3, got %d", compiled.circuitBreaker.maxInflightPerEndpoint)
	}
}

// TestPolicyTombstoneClearsCircuitBreakerPolicy locks the policy tombstone clears circuit breaker policy contract so future changes do not regress it.
func TestPolicyTombstoneClearsCircuitBreakerPolicy(t *testing.T) {
	pool := newAdaptiveLimiterPool(1)
	manager := &policyManager{circuitBreaker: pool}

	manager.Update(&aegisv1.PolicySnapshot{
		Service:  "user-service",
		Revision: 7,
		CircuitBreaker: &aegisv1.CircuitBreakerPolicy{
			MaxInflightPerEndpoint: 3,
		},
	})
	if got := pool.MaxInflightPerEndpoint(); got != 3 {
		t.Fatalf("expected hot-applied max 3, got %d", got)
	}

	manager.Update(&aegisv1.PolicySnapshot{Service: "user-service", Revision: -1})
	if got := pool.MaxInflightPerEndpoint(); got != adaptiveDefaultMaxInflightPerTarget {
		t.Fatalf("expected tombstone to restore default max %d, got %d", adaptiveDefaultMaxInflightPerTarget, got)
	}
	if policy := manager.Load(); policy == nil || policy.version != -1 || policy.circuitBreaker.maxInflightPerEndpoint != adaptiveDefaultMaxInflightPerTarget {
		t.Fatalf("expected compiled tombstone policy to hold defaults, got %+v", policy)
	}
}

// TestPolicyWatcherUsesConnectionLifecycleAfterDialContextCancel locks the policy watcher uses connection lifecycle after dial context cancel contract so future changes do not regress it.
func TestPolicyWatcherUsesConnectionLifecycleAfterDialContextCancel(t *testing.T) {
	policyUpdates := make(chan *aegisv1.PolicySnapshot, 1)
	addr, stop := startPolicyTestServer(t, policyUpdates)
	defer stop()

	dialCtx, cancelDial := context.WithCancel(context.Background())
	conn, err := grpc.DialContext(dialCtx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial policy test server: %v", err)
	}
	connCtx := contextForClientConn(conn)
	cancelDial()

	manager := &policyManager{}
	startPolicyWatcher(connCtx, addr, "user-service", manager, []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())})
	policyUpdates <- &aegisv1.PolicySnapshot{
		Service:  "user-service",
		Revision: 2,
		CircuitBreaker: &aegisv1.CircuitBreakerPolicy{
			MaxInflightPerEndpoint: 7,
		},
	}

	deadline := time.After(2 * time.Second)
	for {
		if policy := manager.Load(); policy != nil && policy.circuitBreaker.maxInflightPerEndpoint == 7 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("policy watcher did not apply update after dial context was canceled")
		case <-time.After(10 * time.Millisecond):
		}
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("close lifecycle connection: %v", err)
	}
	select {
	case <-connCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatalf("expected connection lifecycle context to stop after ClientConn.Close")
	}
}

// scriptedPolicyServer carries scripted policy server state for resolver, picker, and reporter state.
type scriptedPolicyServer struct {
	aegisv1.UnimplementedPolicyServiceServer
	updates <-chan *aegisv1.PolicySnapshot
}

// WatchPolicy streams policy changes to callers until the source or context closes.
func (s scriptedPolicyServer) WatchPolicy(req *aegisv1.WatchPolicyRequest, stream aegisv1.PolicyService_WatchPolicyServer) error {
	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case snapshot := <-s.updates:
			if snapshot == nil || snapshot.Service != req.Service {
				continue
			}
			if err := stream.Send(snapshot); err != nil {
				return err
			}
		}
	}
}

// startPolicyTestServer exposes a policy stream fixture that lets hot-apply tests push snapshots on demand.
func startPolicyTestServer(t *testing.T, updates <-chan *aegisv1.PolicySnapshot) (string, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen policy test server: %v", err)
	}
	server := grpc.NewServer()
	aegisv1.RegisterPolicyServiceServer(server, scriptedPolicyServer{updates: updates})
	go func() {
		_ = server.Serve(lis)
	}()
	return lis.Addr().String(), func() {
		server.Stop()
		_ = lis.Close()
	}
}
