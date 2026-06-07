# AegisMesh Resume Material

## Chinese Resume Version

**AegisMesh：面向微服务慢故障的自适应 RPC 治理系统**

- 设计并实现 Go/gRPC 数据面 SDK 与 Controller 控制面，支持服务注册与 TTL 心跳、gRPC resolver/balancer、Prometheus/Grafana 可观测性、EWMA 延迟窗口、slow_score 慢故障评分、`HEALTHY/DEGRADED/EJECTED/PROBING` endpoint 状态机、adaptive P2C 路由、retry budget、endpoint circuit breaker、故障注入器、eBPF TCP telemetry 和 MeshTest-style 离线 verifier。
- 针对 fail-slow 场景构建可复现实验矩阵，在单机 Docker benchmark 中注入网络延迟、CPU throttle、packet loss 和强制失败上游；`check_results` 汇总 67 条 latency、18 条 retry、1344 条 recovery 记录，覆盖 baseline、慢实例、CPU 慢故障、retry budget、eBPF 网络信号和恢复曲线。
- 在单实例 800ms delay/慢实例场景下，相比 round-robin，adaptive P2C 将 median p99 latency 从 348.682 ms 降至 32.712 ms，降低 90.62%；CPU throttle 场景下，slow_score 相比 static threshold 将 median p99 从 46.596 ms 降至 26.559 ms，降低 43.00%。
- 实现 retry budget 抑制 retry storm：在 1000 个原始请求、上游持续 `UNAVAILABLE` 的压力实验中，将 retry amplification 从无预算的 2.000x 控制到 1.150x，额外 retry 次数从 1000 降至 150。
- 实现 slow_score 驱动的 endpoint 恢复闭环：在 recovery 实验中故障 endpoint 的 slow_score 最高达到 0.95，状态完成 `HEALTHY -> DEGRADED -> EJECTED -> PROBING -> HEALTHY` 迁移，故障解除后最终采样窗口全部恢复为 `HEALTHY`。

## Shorter Resume Version

**AegisMesh：面向微服务慢故障的自适应 RPC 治理系统**

- 基于 Go/gRPC 实现 Controller + SDK 架构，支持服务发现、Prometheus telemetry、EWMA 延迟统计、slow_score 慢故障检测、endpoint 状态机、adaptive P2C、retry budget、circuit breaker、故障注入、eBPF TCP telemetry 和离线 verifier。
- 构建单机 Docker benchmark 矩阵并完成实验汇总；相比 round-robin，adaptive P2C 在慢实例场景将 median p99 从 348.682 ms 降至 32.712 ms，降低 90.62%；slow_score 在 CPU throttle 场景将 p99 降低 43.00%。
- 通过 retry budget 将持续失败上游下的 retry amplification 从 2.000x 控制到 1.150x；recovery 实验中 endpoint 完成 `HEALTHY -> DEGRADED -> EJECTED -> PROBING -> HEALTHY` 状态闭环，max slow_score 为 0.95。

## English Resume Version

**AegisMesh: Adaptive RPC governance system for fail-slow microservices**

- Designed and implemented a Go/gRPC control-plane and data-plane system with service discovery, TTL heartbeats, SDK resolver/balancer, Prometheus/Grafana telemetry, EWMA latency windows, slow_score fail-slow detection, endpoint state machine, adaptive P2C routing, retry budgets, circuit breaker, fault injector, eBPF TCP telemetry, and an offline MeshTest-style verifier.
- Built a reproducible single-machine Docker benchmark matrix covering no-fault baseline, single slow instance, CPU throttle, retry amplification, packet loss, and recovery curves. Adaptive P2C reduced median p99 latency from 348.682 ms to 32.712 ms versus round-robin under a slow-instance fault, a 90.62% reduction.
- Bounded retry amplification from 2.000x to 1.150x under an always-unavailable upstream using a 15% retry budget, and demonstrated a full endpoint recovery loop from `HEALTHY` through `DEGRADED/EJECTED/PROBING` back to `HEALTHY`.

## Interview Talking Points

The main story is that fail-slow faults are not fail-stop faults. A normal health check can keep passing while one instance dominates tail latency. AegisMesh addresses this by collecting endpoint-level RPC telemetry, computing a relative slow_score, feeding that score into both the Controller state machine and the SDK adaptive balancer, and bounding retries so that the system does not create a retry storm during partial failure.

When discussing the routing result, use the slow-instance experiment first. It is the cleanest quantitative result: round-robin median p99 was 348.682 ms, while adaptive P2C median p99 was 32.712 ms. Then explain why the improvement happens: the slow endpoint's effective weight falls as slow_score rises, and once it reaches ejection state it is removed from normal resolver output.

When discussing reliability, emphasize retry budget and recovery separately. Retry budget is local protection against amplification: in the forced failure test, unbudgeted retry doubled downstream attempts, while the budgeted path limited total attempts to 1.150x. Recovery is the Controller-side health loop: slow_score rose to 0.95, route weight fell to 0.512821, the endpoint moved through degraded/ejected/probing states, and after fault reset the final sampled rows were all healthy.

Be careful with eBPF and DeathStarBench claims. The project includes Linux eBPF telemetry code and a DeathStarBench integration planner, but the current measured eBPF improvement is modest on one machine, and DeathStarBench has not been claimed as a completed external benchmark. In interviews, say that eBPF signals are integrated into the scoring path and that DeathStarBench is prepared as a next-step integration target.

## Claims To Avoid

Do not call the system production-grade. The Controller registry is currently in-memory and the policy layer is mostly static SDK defaults. Do not claim high availability for the control plane. Do not claim online verifier closure unless SDK trace collection is wired into the verifier. Do not claim DeathStarBench evaluation until a real DeathStarBench workload has produced measured results. Do not present the packet-loss eBPF result as a major win; the measured single-machine delta is 3.93% p99 improvement, which should be described as a small positive result.
