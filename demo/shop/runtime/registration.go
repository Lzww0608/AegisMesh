package runtime

import (
	"context"
	"log"
	"time"

	aegisv1 "github.com/aegismesh/aegismesh/api/proto/aegis/v1"
	"github.com/aegismesh/aegismesh/pkg/registry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Registration struct {
	ControllerAddr string
	Service        string
	InstanceID     string
	Address        string
	Variant        string
	TTL            time.Duration
}

func StartRegistration(ctx context.Context, cfg Registration) error {
	if cfg.TTL <= 0 {
		cfg.TTL = 15 * time.Second
	}

	conn, err := grpc.DialContext(ctx, cfg.ControllerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	client := aegisv1.NewRegistryServiceClient(conn)

	instance := &aegisv1.ServiceInstance{
		Id:      cfg.InstanceID,
		Service: cfg.Service,
		Address: cfg.Address,
		Status:  string(registry.InstanceHealthy),
		Labels: map[string]string{
			"variant": cfg.Variant,
		},
	}
	if _, err := client.RegisterInstance(ctx, &aegisv1.RegisterInstanceRequest{
		Instance:        instance,
		LeaseTtlSeconds: int64(cfg.TTL.Seconds()),
	}); err != nil {
		_ = conn.Close()
		return err
	}

	go heartbeatLoop(ctx, conn, client, cfg)
	return nil
}

func heartbeatLoop(ctx context.Context, conn *grpc.ClientConn, client aegisv1.RegistryServiceClient, cfg Registration) {
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
			_, err := client.Heartbeat(ctx, &aegisv1.HeartbeatRequest{
				Service:         cfg.Service,
				InstanceId:      cfg.InstanceID,
				LeaseTtlSeconds: int64(cfg.TTL.Seconds()),
			})
			if err != nil {
				log.Printf("heartbeat %s/%s failed: %v", cfg.Service, cfg.InstanceID, err)
			}
		}
	}
}
