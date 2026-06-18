package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os/signal"
	"syscall"
	"time"

	shopv1 "github.com/aegismesh/aegismesh/api/proto/demo/shop/v1"
	"github.com/aegismesh/aegismesh/demo/shop/runtime"
	"github.com/aegismesh/aegismesh/demo/shop/services"
	"google.golang.org/grpc"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:7101", "order-service listen and advertise address")
	controllerAddr := flag.String("controller", "127.0.0.1:9000", "aegis controller address")
	instanceID := flag.String("instance", "order-1", "service instance id")
	variant := flag.String("variant", "primary", "service variant label")
	ttl := flag.Duration("ttl", 15*time.Second, "registry lease TTL")
	slowProbability := flag.Float64("slow-probability", 0, "probability of injecting application-level slow calls")
	slowDuration := flag.Duration("slow-duration", 0, "application-level injected slow call duration")
	errorProbability := flag.Float64("error-probability", 0, "probability of injecting application-level errors")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen order-service: %v", err)
	}

	if err := runtime.StartRegistration(ctx, runtime.Registration{
		ControllerAddr: *controllerAddr,
		Service:        "order-service",
		InstanceID:     *instanceID,
		Address:        *addr,
		Variant:        *variant,
		TTL:            *ttl,
	}); err != nil {
		log.Fatalf("register order-service: %v", err)
	}

	server := grpc.NewServer()
	shopv1.RegisterOrderServiceServer(server, services.NewOrderServerWithFault(*variant, services.FaultProfile{
		SlowProbability:  *slowProbability,
		SlowDuration:     *slowDuration,
		ErrorProbability: *errorProbability,
	}))
	go func() {
		<-ctx.Done()
		server.GracefulStop()
	}()

	log.Printf("order-service %s listening on %s", *instanceID, *addr)
	if err := server.Serve(lis); err != nil {
		log.Fatalf("serve order-service: %v", err)
	}
}
