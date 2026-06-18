# AegisMesh Resume Material

## 中文简历长稿

**AegisMesh：面向微服务慢故障的自适应 RPC 治理系统**

- 基于 Go/gRPC 实现 Controller 控制面和 SDK 数据面，包含服务注册/TTL 心跳、gRPC resolver/balancer、Prometheus/Grafana 指标、PolicyService YAML 策略、真实 SDK trace verifier、故障注入和 Linux eBPF TCP telemetry。
- 针对 fail-slow 实现 EWMA 延迟窗口、relative + absolute SLO slow_score、`HEALTHY/DEGRADED/EJECTED/PROBING` 状态机、PROBING probe ratio、adaptive P2C 路由、retry budget 和 endpoint circuit breaker。慢节点会先降权，再摘除，恢复时只给少量探测流量。
- 构建单机 Docker benchmark 矩阵，注入单实例 800ms delay、CPU throttle、packet loss、持续失败上游和恢复曲线；`check_results` 汇总 61 条 latency、18 条 retry、747 条 recovery 记录，覆盖 baseline、慢实例、CPU 慢故障、retry amplification、eBPF 网络信号和状态恢复。
- 实验显示：慢实例场景下 adaptive P2C 相比 round-robin 将 median p99 latency 从 348.682 ms 降至 32.712 ms，降低 90.62%；CPU throttle 场景下 slow_score 相比 static threshold 将 median p99 从 46.596 ms 降至 26.559 ms，降低 43.00%。
- retry budget 将持续失败上游下的 retry amplification 从 2.000x 控制到 1.150x，额外 retry 从每 1000 个原始请求 1000 次降至 150 次；recovery 实验中故障 endpoint 按 `HEALTHY -> DEGRADED -> EJECTED -> PROBING -> HEALTHY` 恢复，max slow_score 为 0.95。

## 中文简历短稿

**AegisMesh：面向微服务慢故障的自适应 RPC 治理系统**

- 实现 Go/gRPC Controller + SDK 架构，支持服务发现、PolicyService 动态策略、Prometheus/Grafana telemetry、真实 SDK trace verifier、故障注入和 eBPF TCP 网络信号采集。
- 设计 EWMA + relative/absolute SLO slow_score、endpoint 状态机、PROBING probe ratio、adaptive P2C、retry budget 与 circuit breaker，用于慢故障检测、路由避让、重试限流和恢复探测。
- 单机 Docker 实验中，adaptive P2C 在慢实例场景将 median p99 从 348.682 ms 降至 32.712 ms，降低 90.62%；slow_score 在 CPU throttle 场景将 p99 降低 43.00%；retry budget 将 amplification 从 2.000x 降至 1.150x。

## English Resume

**AegisMesh: Adaptive RPC governance system for fail-slow microservices**

- Built a Go/gRPC control plane and SDK for fail-slow RPC governance: TTL service discovery, custom resolver/balancer, PolicyService YAML snapshots, Prometheus/Grafana metrics, SDK JSONL trace verifier, fault injection, and Linux eBPF TCP telemetry.
- Implemented EWMA latency windows, relative + absolute SLO slow_score, endpoint state machine with PROBING probe ratio, adaptive P2C routing, retry budget, and endpoint circuit breaker. Slow endpoints are down-weighted first, then ejected, probed, and returned after recovery.
- In a reproducible single-machine Docker benchmark, adaptive P2C reduced median p99 latency from 348.682 ms to 32.712 ms versus round-robin under a slow-instance fault, slow_score reduced CPU-throttle p99 by 43.00%, and retry budgets bounded retry amplification from 2.000x to 1.150x.

## Performance Evidence

| Experiment | Baseline | AegisMesh mechanism | Result |
| --- | --- | --- | --- |
| Slow instance delay | round-robin p99 348.682 ms | adaptive P2C p99 32.712 ms | 90.62% lower p99 |
| CPU throttle | static threshold p99 46.596 ms | slow_score p99 26.559 ms | 43.00% lower p99 |
| Retry storm | no budget 2.000x amplification | 15% retry budget 1.150x | extra retries 1000 -> 150 |
| Recovery curve | delayed endpoint kept serving | state machine + probing | `HEALTHY -> DEGRADED -> EJECTED -> PROBING -> HEALTHY` |
| PROBING traffic | normal recovery risk | probe ratio | 560 / 257258 traces = 0.2177%, below 10% bound |
| Absolute SLO | relative-only max score 0.377 | SLO-enabled max score 1.007 | all-slow case triggers `DEGRADED` |
| Packet loss | no eBPF score p99 27.539 ms | eBPF score p99 26.456 ms | 3.93% lower p99, modest result |

## Technical Highlights

- Fail-slow detection is based on endpoint telemetry, not only health checks. The score uses EWMA, p95, error rate, in-flight pressure, and optional TCP retransmit/connect signals.
- Relative scoring catches one slow peer. Absolute SLO scoring catches the cases where all peers are slow, or where the service has only one instance.
- Adaptive P2C uses effective weight, local EWMA, in-flight count, slow_score, and endpoint state. Ejected endpoints leave normal routing; probing endpoints get limited traffic.
- Retry budget caps extra attempts per time window. Method-level idempotency policy keeps unsafe RPCs from being retried by default.
- SDK trace JSONL feeds the verifier directly. The verifier checks route distribution, retry attempts, and forbidden service edges.
- The repo includes Docker Compose, Makefile targets, CI, experiment scripts, a result checker, Prometheus config, Grafana dashboard, and reproducibility tests.

## Interview Talking Points

Fail-slow is not fail-stop. TCP connections and health checks may still pass while one endpoint dominates p99 latency. AegisMesh uses endpoint telemetry to compute slow_score, then feeds that score into Controller health state and SDK routing cost.

For routing, lead with the slow-instance result: round-robin p99 was 348.682 ms, adaptive P2C p99 was 32.712 ms. The drop comes from weight reduction first, hard ejection later, and controlled probing during recovery.

For reliability, separate retry and recovery. Retry budget prevents amplification: the forced-failure experiment went from 2000 total attempts without budget to 1150 with budget for 1000 original requests. Recovery shows the Controller state loop: slow_score rose to 0.95, the endpoint entered `DEGRADED/EJECTED/PROBING`, and returned to stable `HEALTHY` after the fault reset.

For newer mechanisms, mention the two validation runs. PROBING traffic stayed at 0.2177% of user-service trace rows. Absolute SLO scoring raised max slow_score from 0.377 to 1.007 in an all-slow control case, which is the concrete reason peer comparison alone is not enough.

## Claims to avoid

- It is not production-grade or highly available. The file-backed registry improves restart recovery, but it is not an HA control plane.
- DeathStarBench has not been measured yet. The repo has an integration planner; measured results come from the self-contained Docker benchmark.
- Describe eBPF carefully. The single-machine packet-loss delta is 3.93% p99 improvement, so the fair claim is integrated network telemetry with a modest measured benefit.
- Not every policy field is fully hot-applied. Retry, timeout, idempotency, and routing initialization are covered; some deeper outlier/circuit-breaker fields still come from local configuration.
