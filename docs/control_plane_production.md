# Control Plane HA and Security Runbook

This page describes the production-oriented control-plane shape now supported by the repository. It is separate from the single-machine experiment report: the Docker benchmark remains the measured evidence path, while this page is the deployment and verification path for multi-controller safety.

## Supported Shape

- Controller server TLS with optional mTLS client certificate verification.
- Static bearer-token or verified mTLS-certificate principal authentication with controller method RBAC, optional service scopes, and an `sdk` role for least-privilege SDK runtime access.
- SDK, agent, experiment-recorder, and demo registration clients can use the same `AEGIS_CONTROLLER_*` TLS and auth environment variables.
- SDK control-plane dials accept multiple controller addresses through `DialOptions.ControllerAddrs` or `AEGIS_CONTROLLER_ADDRS`.
- Control-plane client connections use ordered failover (`pick_first`) rather than round-robin so a single SDK process keeps registry, policy, and telemetry traffic on the same controller while that controller is reachable.
- Registry, policy, and non-stale endpoint health snapshots can be shared by multiple controllers with etcd backends. The file registry backends remain local restart recovery only, and file policy remains a single-controller/local-config mode.

## Controller Server

By default the controller requires TLS plus auth: either bearer-token principals or mTLS certificate-principal mappings. Local demos/tests must pass `--insecure-dev` explicitly to bypass the startup TLS/auth requirement; treat it as a local-only no-auth mode even if TLS flags are also present. Without `--auth-allow-insecure`, the controller refuses bearer-token auth on plaintext gRPC. Certificate-principal auth is never accepted on plaintext.

```bash
go run ./cmd/controller \
  --addr :9000 \
  --registry-backend etcd \
  --registry-etcd-endpoints https://etcd-1:2379,https://etcd-2:2379,https://etcd-3:2379 \
  --registry-etcd-tls-ca-file /etc/aegis/ca.pem \
  --registry-etcd-tls-cert-file /etc/aegis/controller-etcd.pem \
  --registry-etcd-tls-key-file /etc/aegis/controller-etcd-key.pem \
  --registry-etcd-password-env AEGIS_REGISTRY_ETCD_PASSWORD \
  --tls-cert-file /etc/aegis/controller.pem \
  --tls-key-file /etc/aegis/controller-key.pem \
  --tls-ca-file /etc/aegis/ca.pem \
  --tls-require-client-cert \
  --auth-tokens-file /etc/aegis/controller-tokens.txt \
  --policy-backend etcd \
  --policy-etcd-endpoints https://etcd-1:2379,https://etcd-2:2379,https://etcd-3:2379 \
  --policy-etcd-prefix /aegismesh/policy/v1 \
  --policy-etcd-tls-ca-file /etc/aegis/ca.pem \
  --policy-etcd-tls-cert-file /etc/aegis/controller-etcd.pem \
  --policy-etcd-tls-key-file /etc/aegis/controller-etcd-key.pem \
  --policy-etcd-password-env AEGIS_POLICY_ETCD_PASSWORD
```

`controller-tokens.txt` is a comma- or semicolon-separated list of token entries. Prefer `--auth-tokens-file` or `--auth-tokens-env` over `--auth-tokens` so secrets are not exposed in the process list. Source precedence is `env > file > flag` for both bearer-token principals and mTLS certificate-principal mappings; an environment variable with the default name overrides an explicit CLI flag.

Token syntax:

| Syntax | Meaning |
| --- | --- |
| `role=token` | Global token for that role |
| `role:service=token` | Token scoped to one service |
| `role:service-a+service-b=token` | Token scoped to multiple services |

Service-scoped tokens use exact service-name matching for registry, telemetry, and policy requests. Mixed-service telemetry batches and all-service health reads require a global token. `admin` cannot be service-scoped because it bypasses method checks.
`controller-cert-principals.txt` uses the same role and service-scope prefix, but the right side is a verified client certificate identity:

| Syntax | Meaning |
| --- | --- |
| `role=uri:spiffe://...` | Global role mapped from a URI SAN, including SPIFFE URIs |
| `role:service=dns:client.example` | Service-scoped role mapped from a DNS SAN |
| `role:service-a+service-b=cn:legacy-client` | Service-scoped role mapped from the certificate CommonName only when the cert has no URI or DNS SAN |

Certificate identity matching is exact after normalization: DNS identities are lower-cased, URI identities are parsed through Go's URL parser, and CN is an explicit legacy fallback. If a bearer token is present it is authoritative: a valid token chooses the token principal, and an invalid token fails without falling back to the certificate. Cert-only auth requires `--tls-require-client-cert`; mixed token+cert auth may use `--tls-ca-file` without requiring client certs so token-only clients can migrate gradually.

Supported roles:

| Role | Allowed methods |
| --- | --- |
| `admin` | all methods, including future methods |
| `registry` | `RegisterInstance`, `Heartbeat`, `ListInstances`, `WatchInstances` |
| `telemetry` | `ReportEndpointStats`, `ListEndpointHealth` |
| `policy` | `GetPolicy`, `WatchPolicy` |
| `reader` | read-only registry, telemetry health, and policy reads |
| `sdk` | SDK runtime minimum: registry reads/watches, policy reads/watches, and telemetry reports |

## Clients

All control-plane clients read these environment variables through `security.ClientConfigFromEnv("AEGIS_CONTROLLER")`:

```bash
export AEGIS_CONTROLLER_ADDRS=controller-a:9000,controller-b:9000,controller-c:9000
export AEGIS_CONTROLLER_TLS_CA_FILE=/etc/aegis/ca.pem
export AEGIS_CONTROLLER_TLS_CERT_FILE=/etc/aegis/client.pem
export AEGIS_CONTROLLER_TLS_KEY_FILE=/etc/aegis/client-key.pem
export AEGIS_CONTROLLER_TLS_SERVER_NAME=controller.internal
export AEGIS_CONTROLLER_AUTH_TOKEN=...
```

For local plaintext tests only, set `AEGIS_CONTROLLER_AUTH_ALLOW_INSECURE=true` when sending a bearer token over an insecure connection.

## Registry Backends

| Backend | Use case | HA semantics |
| --- | --- | --- |
| `memory` | local tests | no restart recovery, no cross-controller sharing |
| `file` / `file-v2` | single-controller restart recovery | local disk only; not a replicated control plane |
| `etcd` | multi-controller registry sharing | shared leases, CAS heartbeats, watch updates, and TTL expiry through etcd |

The etcd backend supports TLS/mTLS and password injection through env or file. Live etcd tests are gated by `AEGIS_TEST_ETCD_ENDPOINTS`.

## Policy Backends

| Backend | Use case | HA semantics |
| --- | --- | --- |
| `file` | local experiments and single-controller deployments | local file reload only; every controller must receive the same file out of band |
| `etcd` | multi-controller policy sharing | one startup prefix list plus one background prefix watch per controller; RPC watchers read the in-memory cache |

The etcd policy backend stores one protobuf `PolicySnapshot` per service under `/prefix/services/<url-escaped-service>/snapshot`. Physical deletion removes the service from `List()` and causes existing SDK policy watches to receive a one-shot empty tombstone snapshot. Policy writes are intentionally out of band in this version; there is no controller write API yet.

## Health State Backend

| Backend | Use case | HA semantics |
| --- | --- | --- |
| `none` | local experiments and single-controller deployments | endpoint health is held only in the controller process |
| `etcd` | failover recovery and multi-controller health snapshot sharing | per-endpoint snapshots are loaded at startup, written after telemetry/state-machine changes, watched by peer controllers, and filtered by `--health-state-max-age` |

The health backend stores one JSON endpoint snapshot under `/prefix/services/<url-escaped-service>/instances/<url-escaped-instance>`. It preserves state, slow_score, consecutive-window counters, `EjectedAt`, transition time, and snapshot update time, so an `EJECTED` endpoint can continue its remaining ejection/probing flow after controller failover. This state is an observation signal, not a strongly consistent configuration source: newest `UpdatedAt` wins, stale rows are ignored, and telemetry gaps can still cause short-lived routing drift.

## Policy Hot Apply

With either policy backend, the controller hot-applies `outlier_detection` from the local policy cache to the service-scoped health state machine. The SDK watcher hot-applies `circuit_breaker.max_inflight_per_endpoint` to existing endpoint limiters. `routing_policy` remains dial-time only for this version.

## Remaining Boundaries

- The benchmark evidence is still single-machine Docker evidence, not a measured multi-node production benchmark.
- Registry, policy, and non-stale health snapshots can be shared through etcd. Health is still an eventually consistent observation signal, not a leader-coordinated source of truth: active-active controllers can briefly race on the same endpoint, stale rows are bounded by `--health-state-max-age`, and telemetry gaps can still require re-learning after failover. Keep SDK control-plane dials on ordered failover so registry/policy/telemetry normally stay on one controller until failover.
- Policy writes, validation, approval, rollback, and audit are still out of band. The etcd backend is a shared read/distribution backend, not a full policy management plane.
- Static bearer tokens and static mTLS certificate principal mappings are enough for local and small controlled deployments; production identity integration would normally use a real secret manager or workload-identity issuer. Dynamic SPIFFE/SPIRE registration, automatic rotation policy, IAM tenancy, and audit-backed owner mapping are still out of scope.

## Verification

Local gates:

```bash
go test ./pkg/security ./pkg/registry ./pkg/policy ./pkg/fault ./pkg/controller ./sdk/go/aegisgrpc ./cmd/controller
go test -race ./...
go vet ./...
```

Live etcd gate:

```bash
set AEGIS_TEST_ETCD_ENDPOINTS=https://etcd-1:2379,https://etcd-2:2379,https://etcd-3:2379
go test ./pkg/registry ./pkg/policy -run TestEtcd
```