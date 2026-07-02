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
	controllerSecurityIDs  atomic.Uint64
	controllerSecurityByID sync.Map
)

func effectiveControllerSecurityConfig(options DialOptions) security.ClientConfig {
	return security.ClientConfigFromEnv("AEGIS_CONTROLLER").Merge(options.ControllerSecurity)
}

func registerControllerSecurityConfig(cfg security.ClientConfig) string {
	id := strconv.FormatUint(controllerSecurityIDs.Add(1), 10)
	controllerSecurityByID.Store(id, cfg)
	return id
}

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

func unregisterControllerSecurityConfig(id string) {
	if id != "" {
		controllerSecurityByID.Delete(id)
	}
}

func controllerDialOptions(cfg security.ClientConfig) ([]grpc.DialOption, error) {
	opts, err := security.ClientDialOptions(cfg)
	if err != nil {
		return nil, err
	}
	return append(opts, grpc.WithDefaultServiceConfig(controllerFailoverConfig)), nil
}
