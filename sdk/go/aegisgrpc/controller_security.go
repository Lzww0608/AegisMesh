package aegisgrpc

import (
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/aegismesh/aegismesh/pkg/security"
	"google.golang.org/grpc"
)

const controllerSecurityTargetKey = "controller_security"

var (
	// controllerSecurityIDs identifies the controller security i ds constant used by this package.
	controllerSecurityIDs  atomic.Uint64
	controllerSecurityByID sync.Map
)

// effectiveControllerSecurityConfig provides the shared effective controller security config helper for resolver, picker, and reporter state.
func effectiveControllerSecurityConfig(options DialOptions) security.ClientConfig {
	return security.ClientConfigFromEnv("AEGIS_CONTROLLER").Merge(options.ControllerSecurity)
}

// registerControllerSecurityConfig registers register controller security config with the controller or local registry.
func registerControllerSecurityConfig(cfg security.ClientConfig) string {
	id := strconv.FormatUint(controllerSecurityIDs.Add(1), 10)
	controllerSecurityByID.Store(id, cfg)
	return id
}

// loadControllerSecurityConfig reads controller security config state from the configured backing source and returns a caller-owned view.
func loadControllerSecurityConfig(id string) (security.ClientConfig, bool) {
	if id == "" {
		return security.ClientConfigFromEnv("AEGIS_CONTROLLER"), true
	}
	if value, ok := controllerSecurityByID.Load(id); ok {
		if cfg, ok := value.(security.ClientConfig); ok {
			return cfg, true
		}
	}
	return security.ClientConfig{}, false
}

// unregisterControllerSecurityConfig unregisters unregister controller security config and releases its process-local handle.
func unregisterControllerSecurityConfig(id string) {
	if id != "" {
		controllerSecurityByID.Delete(id)
	}
}

// controllerDialOptions provides the shared controller dial options helper for resolver, picker, and reporter state.
func controllerDialOptions(cfg security.ClientConfig) ([]grpc.DialOption, error) {
	opts, err := security.ClientDialOptions(cfg)
	if err != nil {
		return nil, err
	}
	return append(opts, grpc.WithDefaultServiceConfig(controllerFailoverConfig)), nil
}
