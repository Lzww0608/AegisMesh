package runtime

import (
	"context"
	"testing"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestHeartbeatOnceReregistersWhenLeaseIsMissing locks the heartbeat once reregisters when lease is missing contract so future changes do not regress it.
func TestHeartbeatOnceReregistersWhenLeaseIsMissing(t *testing.T) {
	client := &fakeRegistryClient{heartbeatErr: status.Error(codes.NotFound, "missing")}
	cfg := Registration{
		Service:    "user-service",
		InstanceID: "user-a",
		Address:    "127.0.0.1:7001",
		Variant:    "v1",
	}
	epoch, token, err := heartbeatOnce(context.Background(), client, cfg, "old-epoch", "old-token")
	if err != nil {
		t.Fatalf("heartbeat once: %v", err)
	}
	if client.registers != 1 {
		t.Fatalf("expected one re-register, got %d", client.registers)
	}
	if client.lastRegister.GetInstance().GetId() != "user-a" {
		t.Fatalf("unexpected register request: %+v", client.lastRegister)
	}
	if epoch != "registered-epoch" || token != "registered-token" {
		t.Fatalf("unexpected refreshed owner credentials: epoch=%q token=%q", epoch, token)
	}
}

// TestHeartbeatOnceDoesNotReregisterOnOwnerMismatch locks the heartbeat once does not reregister on owner mismatch contract so future changes do not regress it.
func TestHeartbeatOnceDoesNotReregisterOnOwnerMismatch(t *testing.T) {
	client := &fakeRegistryClient{heartbeatErr: status.Error(codes.FailedPrecondition, "stale owner")}
	cfg := Registration{Service: "user-service", InstanceID: "user-a"}

	epoch, token, err := heartbeatOnce(context.Background(), client, cfg, "old-epoch", "old-token")
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected failed precondition, got %v", err)
	}
	if client.registers != 0 {
		t.Fatalf("expected no re-register, got %d", client.registers)
	}
	if epoch != "old-epoch" || token != "old-token" {
		t.Fatalf("owner credentials should be preserved on error: epoch=%q token=%q", epoch, token)
	}
}

// TestHeartbeatOnceSendsOwnerCredentials locks the heartbeat once sends owner credentials contract so future changes do not regress it.
func TestHeartbeatOnceSendsOwnerCredentials(t *testing.T) {
	client := &fakeRegistryClient{}
	cfg := Registration{Service: "user-service", InstanceID: "user-a"}

	epoch, token, err := heartbeatOnce(context.Background(), client, cfg, "epoch-1", "token-1")
	if err != nil {
		t.Fatalf("heartbeat once: %v", err)
	}
	if client.lastHeartbeat.GetRegistrationEpoch() != "epoch-1" || client.lastHeartbeat.GetOwnerToken() != "token-1" {
		t.Fatalf("heartbeat did not send owner credentials: %+v", client.lastHeartbeat)
	}
	if epoch != "heartbeat-epoch" || token != "token-1" {
		t.Fatalf("unexpected returned owner credentials: epoch=%q token=%q", epoch, token)
	}
}

// fakeRegistryClient defines the client calls required for fake registry client.
type fakeRegistryClient struct {
	aegisv1.RegistryServiceClient
	heartbeatErr  error
	registers     int
	lastRegister  *aegisv1.RegisterInstanceRequest
	lastHeartbeat *aegisv1.HeartbeatRequest
}

// RegisterInstance registers register instance with the controller or local registry.
func (c *fakeRegistryClient) RegisterInstance(_ context.Context, req *aegisv1.RegisterInstanceRequest, _ ...grpc.CallOption) (*aegisv1.RegisterInstanceResponse, error) {
	c.registers++
	c.lastRegister = req
	return &aegisv1.RegisterInstanceResponse{
		Instance:   &aegisv1.ServiceInstance{Id: req.GetInstance().GetId(), RegistrationEpoch: "registered-epoch"},
		OwnerToken: "registered-token",
	}, nil
}

// Heartbeat refreshes the instance lease using the current registration fence.
func (c *fakeRegistryClient) Heartbeat(_ context.Context, req *aegisv1.HeartbeatRequest, _ ...grpc.CallOption) (*aegisv1.HeartbeatResponse, error) {
	c.lastHeartbeat = req
	if c.heartbeatErr != nil {
		return nil, c.heartbeatErr
	}
	return &aegisv1.HeartbeatResponse{Instance: &aegisv1.ServiceInstance{Id: req.GetInstanceId(), RegistrationEpoch: "heartbeat-epoch"}}, nil
}
