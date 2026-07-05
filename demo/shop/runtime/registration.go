package runtime

import (
	"context"
	"log"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/registry"
	"github.com/aegismesh/aegismesh/pkg/security"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Registration carries registration state for this package call path.
type Registration struct {
	ControllerAddr     string
	Service            string
	InstanceID         string
	Address            string
	Variant            string
	TTL                time.Duration
	ControllerSecurity security.ClientConfig
}

// StartRegistration registers the service and keeps its lease alive until the context ends.
func StartRegistration(ctx context.Context, cfg Registration) error {
	if cfg.TTL <= 0 {
		cfg.TTL = 15 * time.Second
	}

	controllerSecurity := security.ClientConfigFromEnv("AEGIS_CONTROLLER").Merge(cfg.ControllerSecurity)
	conn, err := security.DialController(ctx, cfg.ControllerAddr, controllerSecurity)
	if err != nil {
		return err
	}
	client := aegisv1.NewRegistryServiceClient(conn)

	registrationEpoch, ownerToken, err := registerInstance(ctx, client, cfg)
	if err != nil {
		_ = conn.Close()
		return err
	}

	go heartbeatLoop(ctx, conn, client, cfg, registrationEpoch, ownerToken)
	return nil
}

// registerInstance registers register instance with the controller or local registry.
func registerInstance(ctx context.Context, client aegisv1.RegistryServiceClient, cfg Registration) (string, string, error) {
	instance := &aegisv1.ServiceInstance{
		Id:      cfg.InstanceID,
		Service: cfg.Service,
		Address: cfg.Address,
		Status:  registry.InstanceHealthy.String(),
		Labels: map[string]string{
			"variant": cfg.Variant,
		},
	}
	resp, err := client.RegisterInstance(ctx, &aegisv1.RegisterInstanceRequest{
		Instance:        instance,
		LeaseTtlSeconds: int64(cfg.TTL.Seconds()),
	})
	if err != nil {
		return "", "", err
	}
	return resp.GetInstance().GetRegistrationEpoch(), resp.GetOwnerToken(), nil
}

// heartbeatOnce refreshes the instance lease using the current registration fence.
func heartbeatOnce(ctx context.Context, client aegisv1.RegistryServiceClient, cfg Registration, registrationEpoch, ownerToken string) (string, string, error) {
	resp, err := client.Heartbeat(ctx, &aegisv1.HeartbeatRequest{
		Service:           cfg.Service,
		InstanceId:        cfg.InstanceID,
		LeaseTtlSeconds:   int64(cfg.TTL.Seconds()),
		RegistrationEpoch: registrationEpoch,
		OwnerToken:        ownerToken,
	})
	if status.Code(err) == codes.NotFound {
		return registerInstance(ctx, client, cfg)
	}
	if err != nil {
		return registrationEpoch, ownerToken, err
	}
	if nextEpoch := resp.GetInstance().GetRegistrationEpoch(); nextEpoch != "" {
		registrationEpoch = nextEpoch
	}
	return registrationEpoch, ownerToken, nil
}

// heartbeatLoop refreshes the instance lease using the current registration fence.
func heartbeatLoop(ctx context.Context, conn interface{ Close() error }, client aegisv1.RegistryServiceClient, cfg Registration, registrationEpoch, ownerToken string) {
	defer conn.Close()

	interval := cfg.TTL / 2
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nextEpoch, nextToken, err := heartbeatOnce(ctx, client, cfg, registrationEpoch, ownerToken)
			if err != nil {
				log.Printf("heartbeat %s/%s failed: %v", cfg.Service, cfg.InstanceID, err)
				continue
			}
			registrationEpoch = nextEpoch
			ownerToken = nextToken
		}
	}
}
