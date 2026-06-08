package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	shopv1 "github.com/aegismesh/aegismesh/api/proto/demo/shop/v1"
	"github.com/aegismesh/aegismesh/sdk/go/aegisgrpc"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type checkoutResponse struct {
	User  *shopv1.GetUserResponse     `json:"user"`
	Order *shopv1.CreateOrderResponse `json:"order"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "frontend HTTP listen address")
	controllerAddr := flag.String("controller", "127.0.0.1:9000", "aegis controller address")
	meshMode := flag.String("mesh-mode", "aegis", "routing mode: aegis or direct")
	routingPolicy := flag.String("routing-policy", string(aegisgrpc.RoutingAdaptiveP2C), "Aegis routing policy: adaptive_p2c or round_robin")
	retryMode := flag.String("retry-mode", string(aegisgrpc.RetryBudget), "Aegis retry mode: budget, without_budget, or off")
	userService := flag.String("user-service", "user-service", "Aegis user service name")
	orderService := flag.String("order-service", "order-service", "Aegis order service name")
	userTarget := flag.String("user-target", "127.0.0.1:7001", "direct user-service target for --mesh-mode=direct")
	orderTarget := flag.String("order-target", "127.0.0.1:7101", "direct order-service target for --mesh-mode=direct")
	traceLog := flag.String("trace-log", "", "JSONL path for real SDK trace records")
	timeout := flag.Duration("timeout", 2*time.Second, "per request timeout")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	userConn, err := dialService(ctx, frontendDialConfig{
		MeshMode:      *meshMode,
		Controller:    *controllerAddr,
		Service:       *userService,
		DirectTarget:  *userTarget,
		RoutingPolicy: *routingPolicy,
		RetryMode:     *retryMode,
		TraceLogPath:  *traceLog,
	})
	if err != nil {
		log.Fatalf("dial user-service: %v", err)
	}
	defer userConn.Close()

	orderConn, err := dialService(ctx, frontendDialConfig{
		MeshMode:      *meshMode,
		Controller:    *controllerAddr,
		Service:       *orderService,
		DirectTarget:  *orderTarget,
		RoutingPolicy: *routingPolicy,
		RetryMode:     *retryMode,
		TraceLogPath:  *traceLog,
	})
	if err != nil {
		log.Fatalf("dial order-service: %v", err)
	}
	defer orderConn.Close()

	userClient := shopv1.NewUserServiceClient(userConn)
	orderClient := shopv1.NewOrderServiceClient(orderConn)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/checkout", checkoutHandler(userClient, orderClient, *timeout))

	server := &http.Server{Addr: *addr, Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("frontend listening on http://%s", *addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve frontend: %v", err)
	}
}

type frontendDialConfig struct {
	MeshMode      string
	Controller    string
	Service       string
	DirectTarget  string
	RoutingPolicy string
	RetryMode     string
	TraceLogPath  string
}

func dialService(ctx context.Context, cfg frontendDialConfig) (*grpc.ClientConn, error) {
	if strings.EqualFold(cfg.MeshMode, "direct") {
		return grpc.DialContext(ctx, cfg.DirectTarget, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	options := aegisgrpc.DefaultDialOptions()
	options.RoutingPolicy = aegisgrpc.RoutingPolicy(cfg.RoutingPolicy)
	options.RetryMode = aegisgrpc.RetryMode(cfg.RetryMode)
	options.TraceLogPath = cfg.TraceLogPath
	return aegisgrpc.DialServiceFromWithOptions(ctx, "frontend", cfg.Controller, cfg.Service, options)
}

func checkoutHandler(userClient shopv1.UserServiceClient, orderClient shopv1.OrderServiceClient, timeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.URL.Query().Get("user_id")
		if userID == "" {
			userID = "u-100"
		}
		itemIDs := parseItems(r.URL.Query().Get("items"))

		reqCtx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		if traceID := r.Header.Get("x-aegis-trace-id"); traceID != "" {
			reqCtx = aegisgrpc.ContextWithTraceID(reqCtx, traceID)
		} else {
			reqCtx = aegisgrpc.ContextWithNewTraceID(reqCtx)
		}

		user, err := userClient.GetUser(reqCtx, &shopv1.GetUserRequest{UserId: userID})
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}

		order, err := orderClient.CreateOrder(reqCtx, &shopv1.CreateOrderRequest{
			UserId:  userID,
			ItemIds: itemIDs,
		})
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}

		writeJSON(w, http.StatusOK, checkoutResponse{User: user, Order: order})
	}
}

func parseItems(raw string) []string {
	if raw == "" {
		return []string{"sku-1", "sku-2"}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item != "" {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return []string{"sku-1", "sku-2"}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("write json response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
