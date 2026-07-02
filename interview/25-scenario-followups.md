# 25. 场景化追问专项

## 深度/系统设计混合

### Q673【场景】如果用户服务 user-b 延迟 300ms 但仍然成功返回，AegisMesh 的治理链路会经历哪些观测和决策环节？

这个场景是典型 fail-slow，不是 fail-stop。user-b 没有宕机，也没有大量 5xx，只是响应变慢。

链路会先从 SDK 观测开始。frontend 调 user-service 时，telemetry interceptor 会记录本次 RPC 的 destination、method、upstream、status、latency、timeout、inflight 等信息。因为 user-b 仍然成功返回，error count 可能不高，但 latency EWMA 和 p95 会升高。

接着 reporter 把窗口统计上报到 Controller 的 `TelemetryService.ReportEndpointStats`。Controller 的 HealthManager 会把 user-a 和 user-b 放在同一个 service 里比较。user-b 的 p95 明显高于同组 median 时，relative latency score 会升高。如果配置了 absolute SLO，300ms 超过 SLO 时 absolute score 也会抬高。

然后进入状态机。第一次慢窗口通常不会立刻 EJECTED，而是累计 consecutive slow windows。达到 degraded threshold 后，user-b 进入 `DEGRADED`；如果持续更严重，才进入 `EJECTED`。这就是 hysteresis，避免一次抖动就摘除实例。

最后回到 SDK 路由。resolver 拉到 user-b 的 slow_score 和 state 后，adaptive P2C 会提高 user-b 的路由 cost，降低它的 effective weight。流量会先逐步偏向 user-a；如果 user-b 被 EJECTED，就不再作为普通候选；恢复时进入 PROBING，只吃少量探测流量。

所以这条链路是：真实 RPC latency 上升 -> SDK 统计 -> Controller slow_score -> 状态机 -> resolver/balancer -> 流量转移。

### Q674【场景】如果 user-a 和 user-b 同时被注入 500ms 延迟，relative scoring 和 absolute SLO scoring 分别会如何触发？

如果两个实例同时变慢，relative scoring 会变弱。原因很简单：relative scoring 是和同一 service 内部的 peers 比。user-a 和 user-b 都变成 500ms 后，它们的 median 也会升高，MAD 可能还很小。单看相对差异，系统会觉得“大家差不多慢”，不一定能把某个实例识别成异常点。

这就是相对异常检测的盲区：它擅长发现“一个实例比其他实例慢”，不擅长发现“所有实例都慢”。

absolute SLO scoring 就是为这个场景补的。假设 user-service 的 p95 SLO 是 150ms，两个实例都到 500ms，那么 `p95_latency / slo_latency` 大约是 3.3。即使 relative score 不高，absolute score 也会把 latency component 拉高。

当前设计里 latency score 取 relative 和 absolute 的较大值。这样一个实例慢时 relative 起作用，全部实例慢时 absolute SLO 起作用。

不过路由效果要分清。两个实例都慢时，adaptive P2C 没有真正的“好实例”可选。它可以降低过慢实例权重、触发 DEGRADED、保护重试和并发，但不能凭空变出健康容量。这时更重要的是告警、限流、load shedding 或扩容，而不是只靠负载均衡。

### Q675【场景】如果 order-service 是非幂等 CreateOrder，为什么 retry policy 必须和 method policy 绑定？

因为“能不能重试”不是 service 级别能简单决定的，它和具体方法语义有关。

`GetUser` 这类读请求通常是幂等的。第一次失败或超时后，再发一次，一般不会改变业务状态。`CreateOrder` 不一样，它可能已经在服务端创建了订单，只是响应在网络中丢了，或者客户端等超时了。客户端如果再重试一次，就可能创建两笔订单。

所以 retry policy 必须和 method policy 绑定。AegisMesh 的 Policy YAML 可以把 `/demo.shop.v1.OrderService/CreateOrder` 标成 `idempotent: false`，并禁用 retry 或把 max attempts 限制为 1。对 `/demo.shop.v1.UserService/GetUser` 则可以允许预算内重试。

这也是面试里很容易被追问的点。正确回答不是“所有 RPC 都自动重试”，而是“重试必须尊重方法幂等性；非幂等写请求要禁用重试，或者引入 idempotency key 和服务端去重”。

### Q676【场景】如果 Controller 在 policy watcher stream 中断后 10 秒恢复，SDK 在这段时间应该如何处理请求？

SDK 不应该因为 policy watcher 断了就停止业务请求。

合理行为是：继续使用最后一次成功收到的 PolicySnapshot。如果从来没拿到过策略，就使用 SDK 内置默认策略。watcher 在后台按 backoff 重连，Controller 恢复后重新建立 stream，拿到新的 snapshot 再原子切换。

业务 RPC 仍然走现有 ClientConn、resolver 地址和本地 balancer。retry、timeout、idempotency 规则也继续按旧策略执行。telemetry reporter 如果也暂时上报失败，应该丢弃或短暂缓存，不能阻塞业务调用。

这个设计的核心是 control plane failure 不进入每次业务请求的同步路径。Controller 短时间不可用会让策略更新和健康状态变钝，但不应该让已建立的业务流量立刻失败。

生产里还要加 policy TTL 和告警。如果 watcher 断开 10 秒可以接受，断开 10 分钟就要报警；如果策略太旧，SDK 可以进入 degraded control-plane mode，并暴露 metrics。

### Q677【场景】如果 Prometheus 指标显示 p99 已下降，但业务错误率上升，如何定位 adaptive routing 是否引入了新问题？

这种情况不能只看 p99。p99 下降可能是因为慢请求被快速失败了，也可能是因为流量转移后成功请求变快了。错误率上升说明需要继续查。

我会先看错误码分布。如果 `ResourceExhausted` 或 breaker open 增多，可能是 MaxInflightPerEndpoint 太低，或者流量转到少数 endpoint 后被 breaker 拦住。如果 `Unavailable` 增多，可能是 resolver 还在返回已退出实例，或者 EJECTED 后健康候选不足。如果 `DeadlineExceeded` 仍然高，说明 routing 没解决根因。

再看 route distribution 和 endpoint state。如果 adaptive P2C 把大量流量集中到 user-a，user-a 的 inflight 和 error 同时升高，那可能是“绕开慢实例”后把健康实例打满了。此时需要最大摘除比例、最小健康实例数、限流或更平滑的权重变化。

还要看 retry amplification。p99 下降但 retry 增多，可能是失败被重试掩盖了。retry budget 的使用率能说明系统是否在靠额外请求撑住表面指标。

最后用 trace verifier 看真实路径。确认请求是否按预期避开 user-b，有没有进入 forbidden edge，attempt 是否超限。只有把 latency、error、route、state、retry 放在一起看，才能判断 adaptive routing 是帮忙还是引入新问题。

### Q678【场景】如果某 endpoint 处于 PROBING，但健康 endpoint 全部下线，是否应该把 PROBING endpoint 作为正常候选？

这取决于系统选择 fail-open 还是 fail-closed。对大多数在线业务，我会倾向有保护的 fail-open。

正常情况下，PROBING endpoint 只应该接少量探测流量，不能直接回到正常流量池。因为它刚从 EJECTED 过来，还没有足够证据证明恢复。

但如果所有 HEALTHY/DEGRADED endpoint 都不可用，只剩 PROBING endpoint，就不能简单返回“没有可用后端”。这会把系统直接打成全失败。更合理的是把 PROBING 作为 least-bad fallback，允许它接一部分或全部必要流量，同时保留严格的 breaker、timeout 和 retry budget。

也就是说：有健康候选时，PROBING 受 probe ratio 限制；没有健康候选时，可以提升为应急候选，但要打上降级状态，并暴露 metrics。这样既避免恢复流量打爆实例，也避免系统在还有可能成功的情况下直接不可用。

### Q679【场景】如果 registry 中实例未过期，但容器已经退出，ListInstances 会返回什么？如何改进？

如果实例 lease 还没过期，当前 registry 仍可能在 `ListInstances` 里返回它。Registry 只知道这个实例上次 Register 或 Heartbeat 的时间和 TTL，不会自动知道容器已经退出。

接下来客户端会尝试连接这个地址，RPC 可能返回 `UNAVAILABLE`、连接失败或超时。SDK telemetry 会记录错误，Controller 后续可能把这个 endpoint 打成异常。但在 TTL 到期或 telemetry 生效前，客户端仍可能踩到这个旧地址。

改进有几种。

第一，服务优雅退出时增加 Deregister，退出前主动从 registry 删除或标记 draining。

第二，缩短 TTL 和 heartbeat interval，减少僵尸实例存在时间，但太短会增加控制面压力，也可能因为短暂抖动误删。

第三，Controller 集成容器/Kubernetes 状态。比如 watch EndpointSlice 或 Pod readiness，Pod 消失时主动删除实例。

第四，SDK 侧对连接失败做本地短期隔离，不等 Controller 下一轮状态。

所以 TTL 是兜底，不是完整生命周期管理。生产里最好是 Deregister + TTL + 平台状态 watch 一起做。

### Q680【场景】如果服务实例频繁重启导致 address 变化，adaptiveStats 按 address 缓存会出现什么历史污染？

当前 adaptive stats 以 address 为 key 缓存本地 EWMA 和 inflight。如果实例频繁重启并拿到新地址，旧地址对应的 stats 可能留在 `sync.Map` 里，造成内存增长。

如果地址被复用，还可能出现历史污染。比如旧 user-b 在 `172.18.0.5:7002` 上很慢，后来容器重启后新实例复用了同一地址。SDK 仍然保存这个 address 的高 EWMA，新实例刚上线就可能被当成慢实例，流量被错误降权。

更稳的做法是按 service + instance_id + address 组合 key，或者给 stats 加 generation/revision。resolver 收到新的 instance_id 时，重置对应本地 stats。也可以给 stats 加 lastSeen TTL，长时间没出现在 resolver 结果里的地址自动清理。

生产里最好不要只依赖 IP:port 表示身份。Pod UID、instance ID、revision 这类稳定身份更适合作为缓存边界。

### Q681【场景】如果 BPF ringbuf 丢事件，slow_score 中网络信号的权重应该如何设计才稳健？

网络信号不能设计成“一票否决”。ringbuf 丢事件说明 eBPF 采集是 best-effort，不能保证每个 TCP 事件都上报。

权重上应该保守。AegisMesh 当前 slow_score 里网络信号权重较低，适合作为辅助证据。即使 ringbuf 丢了一些 retransmit 事件，latency、error、timeout、inflight 仍然能反映用户侧慢故障。

处理方式可以有几条。

第一，把 ringbuf dropped events 暴露成 agent metric。如果丢事件率高，就降低 network_score 的可信度。

第二，网络信号用窗口聚合，不看单个事件。比如看每 5 秒 retransmit rate，而不是某一次 retransmit。

第三，网络信号只提高怀疑度，不直接 EJECT。真正状态迁移仍然要看请求数、latency 和连续窗口。

第四，缺失数据和 0 要区分。没有采集到事件不等于网络完全健康。

这样设计后，eBPF 失真最多让网络归因变弱，不会让整个 slow_score 崩掉。

### Q682【场景】如果 retry budget 已耗尽，但当前错误是短暂网络错误，系统应该牺牲单请求成功率还是保护下游？

默认应该保护下游。retry budget 的意义就是在故障时限制额外请求，而不是每个请求都尽量重试。

短暂网络错误确实可能通过一次重试恢复。问题是客户端无法准确知道“这一次”是不是短暂错误。如果大量客户端都认为自己遇到的是短暂错误，并在预算耗尽后继续重试，就会形成 retry storm。

所以预算耗尽后，系统应该拒绝新的 retry，让请求按当前错误返回。这样会牺牲一部分单请求成功率，但保护了下游和整个系统。

可以做更细的策略：高优先级、幂等、低成本请求有独立预算；明显本地连接复用问题可以允许少量紧急 retry；不同错误码有不同预算。但这些都应该在预算框架内做，不能绕过预算。

面试里可以这样说：retry budget 是系统级保险丝，它不是为了让每个请求都更幸运，而是为了让故障时系统不被自己的重试打垮。

### Q683【场景】如果 frontend 的 telemetry reporter 无法连接 Controller，SDK 是否应该影响业务 RPC？

不应该。telemetry reporter 是控制面上报路径，不应该阻塞业务 RPC。

业务请求仍然通过已经解析到的 SubConn 和本地 balancer 发送。reporter 连不上 Controller 时，可以记录错误、增加 metrics、丢弃当前窗口或短暂缓冲。但不能在用户请求路径里等待上报成功。

影响是治理闭环会变弱。Controller 收不到最新 latency 和 error，就无法及时更新 slow_score 和状态机。SDK 还能用本地 EWMA/inflight 做一定程度的 adaptive routing，但全局健康状态会变旧。

生产里应该暴露 `telemetry_report_failures`、`last_successful_report_time` 这类指标。如果上报失败持续很久，需要告警。短时失败则按 control plane degraded 处理，不影响业务路径。

### Q684【场景】如果业务请求在客户端超时，但服务端仍继续执行，重试会产生什么一致性风险？

风险是重复副作用。

客户端看到 `DEADLINE_EXCEEDED`，只能说明它等不到响应，不能说明服务端没有执行。服务端可能已经写库成功、创建订单成功、扣减库存成功，只是响应包丢了，或者客户端 deadline 到了。

如果客户端这时自动重试非幂等方法，比如 `CreateOrder`，就可能创建两笔订单。这个问题不是 gRPC 独有，所有 RPC 重试都会遇到。

AegisMesh 的处理是把 retry 和 method policy 绑定。非幂等方法默认禁用重试，或者必须使用 idempotency key。服务端收到同一个 key 时，要能返回同一个结果或拒绝重复执行。

所以面试里一定要强调：timeout 不等于失败未发生；重试安全性来自幂等性设计，不是来自 retry interceptor 本身。

### Q685【场景】如果 round_robin 在 no-fault 场景比 adaptive P2C 更快，如何解释治理层 overhead？

这是正常的。没有故障时，round_robin 很简单，几乎只是在 ready SubConn 之间轮换。adaptive P2C 要读取 endpoint attributes、慢分、状态、本地 inflight、EWMA，还要维护 Done 回调和 telemetry。它天然有额外开销。

项目实验里 no-fault 场景也能看到 AegisMesh p99 有一定 overhead。这个 overhead 是治理能力的成本。问题不在于它是否为 0，而在于它是否可接受，以及故障场景收益是否覆盖成本。

解释时要把 no-fault 和 fault 分开。no-fault 下，AegisMesh 可能比 round_robin 慢一点；慢实例故障下，adaptive P2C 能把 p99 从 348.682ms 降到 32.712ms。这个 tradeoff 对追求稳定性和尾延迟的服务是有意义的。

后续优化可以从热路径做：减少 Pick 分配、缓存 effective weight、降低 metrics/trace 开销、用 histogram 替代 latency slice、采样 trace。

### Q686【场景】如果实验中 CPU throttle 导致两个实例都变慢，如何判断 slow_score 是否仍然有效？

先看 relative score。如果两个实例都被 CPU throttle 到类似程度，relative score 可能不高，因为它们彼此差不多。这时不能指望 relative outlier 找到“坏实例”。

再看 absolute SLO score。如果 p95 超过服务 SLO，absolute score 应该升高，并推动状态进入 DEGRADED。项目里的 absolute SLO 实验就是为了验证这种 all-slow case：disabled 时 max slow_score 只有 0.377，enabled 时 max slow_score 到 1.007 并出现 DEGRADED。

判断是否有效，不是看它能不能选出一个“好实例”。两个实例都慢时，本来就没有好实例。要看它能否识别服务整体退化，并触发降级、告警、限流或扩容信号。

所以在 all-slow 场景里，slow_score 的目标从“实例级避让”变成“服务级退化检测”。这是 relative + absolute 组合的意义。

### Q687【场景】如果高流量 endpoint 的 p95 更稳定，低流量 endpoint 的 p95 更随机，评分算法如何改进？

低流量 endpoint 的 p95 样本少，很容易被单个慢请求拉高。直接按同一规则比较高低流量实例，会误伤低流量实例。

可以从几方面改。

第一，加最小样本量。request_count 低于阈值时，不直接用 p95 推动 EJECTED，只能标成 low-confidence。

第二，加置信度权重。样本越多，score 权重越高；样本越少，score 更保守。

第三，用更长窗口或滑动窗口。低 QPS 服务需要更长时间积累样本，不能和高 QPS 服务用同样 5 秒窗口。

第四，结合 EWMA 和 error。少量 p95 抖动不应该单独决定状态，至少要和连续窗口、error、timeout、inflight 一起看。

第五，按 method 分开。有些低流量方法天然慢，不应该污染整个 instance 分数。

一句话：评分算法要有统计置信度。没有足够样本时，宁愿慢一点决策，也不要因为一次异常样本摘除 endpoint。

### Q688【场景】如果某个调用方到 endpoint 的网络路径异常，而其他调用方正常，应该做全局 ejection 还是调用方局部避让？

更合理的是调用方局部避让，而不是全局 ejection。

如果只有 frontend-a 到 user-b 的网络路径有问题，frontend-b 调 user-b 正常，把 user-b 全局 EJECTED 会误伤其他调用方。这个问题在跨 AZ、跨节点、跨地域网络里很常见。

可以按 source + destination + endpoint 维度保留局部观测。Controller 聚合时不要简单把所有 caller 的 telemetry 混成一个全局结论。可以同时维护 global health 和 caller-local health。

路由时，SDK 优先使用自己的本地 EWMA 和 local network signal。如果本地路径异常，即使全局状态 HEALTHY，也可以局部降权。只有多个调用方都看到 user-b 慢，才更适合全局 ejection。

当前 AegisMesh 更偏全局 endpoint health 加本地 EWMA 的混合。要生产化，caller-aware scoring 是一个很有价值的扩展。

### Q689【场景】如果 policy YAML 配错 MaxAttempts=10，retry budget 是否足够防止放大？

retry budget 能限制放大，但不能完全替代策略校验。

如果 `MaxAttempts=10`，单个请求理论上最多尝试 10 次。但只要启用了 budget，额外 retry 仍然要经过 `AllowRetry`。预算耗尽后，即使 max attempts 还没用完，也不会继续重试。所以它能防止 total attempts 无上限增长。

但问题仍然存在。第一，低流量服务有 MinBudget，比例上可能仍然偏高。第二，高优先级请求可能把预算打光，后续请求没有 retry 机会。第三，错误配置本身会让系统行为难以解释。

所以生产里必须加 policy validation。比如 max attempts 上限只能是 2 或 3；非幂等方法不能配置重试；高风险策略需要审批；配置发布前做 dry-run。

预算是保险丝，validation 是防止错误配置上线。两者都需要。

### Q690【场景】如果 MaxInflightPerEndpoint 过低，adaptive P2C 会如何表现？

如果 MaxInflightPerEndpoint 过低，breaker 会过早打开。Pick 时选中了某个 endpoint，但 `Acquire` 发现 inflight 到上限，就返回 `ResourceExhausted`。这会让业务错误率上升。

从指标上看，可能出现 p99 下降但错误率升高。原因是请求没有排队等慢，而是更快失败了。这个结果不能被解释成治理成功。

adaptive P2C 本身会倾向选择 inflight 低的 endpoint，但如果所有 endpoint 的上限都太低，候选都很容易被 breaker 拒绝。负载越高，错误越明显。

解决方式是按 endpoint capacity 配置 inflight 上限，而不是固定一个过小值。还可以引入排队上限、短等待、adaptive concurrency，让系统在“快速失败”和“有限排队”之间取平衡。

### Q691【场景】如果 endpoint 长时间没有流量，恢复探测如何获得成功率样本？

如果没有流量，PROBING 就很难判断恢复。状态机可以从 EJECTED 到 PROBING，但没有探测请求，就没有 success rate 样本，也无法确认是否能回 HEALTHY。

有几种办法。

第一，让 PROBING endpoint 接少量真实流量。这是当前 probe ratio 的思路。只要服务还有请求，就能慢慢获得样本。

第二，主动探测。Controller 或 SDK 发轻量 health RPC，但要注意这个探测必须接近真实业务，否则只能证明 health endpoint 正常。

第三，低流量服务放宽恢复策略。比如 PROBING 期间只要连续少量请求成功就恢复，但标记 confidence 较低。

第四，超时策略。PROBING 太久没有样本，可以保持 PROBING 或回到 DEGRADED，而不是直接 HEALTHY。

生产里我会把真实流量探测和主动探测结合。只靠被动流量，低 QPS 服务恢复会很慢。

### Q692【场景】如果真实生产中存在跨 AZ 调用，locality 和 slow_score 的权重如何组合？

locality 和 slow_score 要一起进入 cost function。

同 AZ 调用通常 latency 更低、成本更低、故障域更小。正常情况下应该优先同 AZ endpoint。但如果同 AZ endpoint slow_score 很高，而跨 AZ endpoint 健康，就应该允许跨 AZ fallback。

可以把 cost 设计成：

```text
cost = locality_penalty + inflight_cost + latency_ewma_cost + slow_score_penalty
```

同 AZ locality_penalty 低，跨 AZ 高。slow_score 高时 penalty 也高。这样正常情况下流量留在本地，局部慢故障时能跨 AZ 避让。

还要加容量和故障域保护。不能因为一个 AZ 慢，就把全部流量打到另一个 AZ，导致跨 AZ 也崩。可以设置跨 AZ 最大比例、优先级、熔断和全局 overload 信号。

一句话：locality 是偏好，不是硬约束；slow_score 是健康信号，也不能无视容量。

### Q693【场景】如果 Controller 节点之间 health state 不一致，客户端可能看到什么路由行为？

如果多个 Controller 节点各自维护 health state，又没有一致性存储，客户端连到不同 Controller 时会看到不同 endpoint 状态。

一个 SDK 可能看到 user-b 是 EJECTED，另一个 SDK 看到 user-b 仍然 HEALTHY。结果是流量分配不一致：部分客户端避开 user-b，部分客户端继续打 user-b。更糟的是，状态来回变化会造成路由抖动。

Policy revision 也可能不一致。某些客户端使用新阈值，某些客户端使用旧阈值，实验和排障会很难解释。

解决方式是用共享一致性存储，比如 etcd，把 registry、policy revision、health state 或至少状态事件放进去。Controller 多副本要么通过 leader 统一推进状态机，要么用 CAS/revision 防止旧状态覆盖新状态。

客户端侧也要记录自己当前连接的 Controller 和 policy/state revision，方便排查。

### Q694【场景】如果服务端开启 keepalive，但业务线程池已满，RPC 健康与业务健康如何区分？

keepalive 只能说明连接还活着，HTTP/2/TCP 层没有断。它不能说明业务线程池有空，也不能说明方法能在 SLO 内返回。

业务线程池满时，连接可能仍然正常，health check 也可能成功，但真实 RPC 会排队、超时或变慢。这就是 fail-slow 的典型来源。

区分方式要看真实业务指标：method latency、queue length、inflight、timeout、server-side processing time、线程池 active count。AegisMesh 从客户端看到的是 RPC latency 和 timeout；如果能补服务端 metrics，就能更准确判断是不是线程池耗尽。

所以 RPC 健康不能只看连接。连接健康是 transport health，业务健康要看真实请求是否能及时处理。

### Q695【场景】如果 trace 文件很大，verifier 的内存和流式处理应该如何优化？

当前 verifier 如果一次性把 trace 全读进内存，大文件会有压力。优化方向是流式处理。

route distribution 不需要保存所有 trace，只需要按 route 计数和总数。retry max 也只需要检查每条记录的 attempt。forbidden edge 可以边读边检查 path。也就是说，大部分 verifier 规则都可以单 pass 完成。

实现上可以用 `bufio.Scanner` 或 `bufio.Reader` 按行读 JSONL。每读一行解析一个 TraceRecord，更新 counters 和 failed checks。最后输出 report。

如果需要按 trace_id 聚合多 span，就不能完全逐行丢掉。可以做 bounded map：只保留当前窗口内未完成 trace，超过 TTL 的 trace flush 掉。也可以先外部排序或按 trace_id 分片。

报告里也不要保存所有失败样本。保存前 N 条 examples，加总数就够了。这样 verifier 可以处理 GB 级 trace，而不是只适合 demo 文件。

### Q696【场景】如果公司要求不能使用高权限 eBPF agent，你还能保留哪些网络故障信号？

可以保留应用层和用户态网络信号。

第一，gRPC status code。`UNAVAILABLE`、`DEADLINE_EXCEEDED`、`RESOURCE_EXHAUSTED` 能反映一部分网络或下游问题。

第二，connect latency 和 dial error。即使不用 eBPF，SDK 在建立连接或 RPC 失败时也能记录连接错误和超时。

第三，客户端侧 RTT 近似值。如果能在 gRPC/HTTP 层记录请求开始到 headers 返回的时间，可以作为端到端网络和服务处理的混合信号。

第四，node exporter 或 cAdvisor。它们能提供网卡错误、丢包、TCP retransmit 统计，虽然粒度可能是节点级，不是 endpoint 级。

第五，service mesh 或 sidecar metrics。如果公司已有 Envoy，可以从 upstream connect failure、upstream rq time、cx metrics 里拿网络相关指标。

没有 eBPF 时，网络归因会粗一些，但 AegisMesh 的主链路仍能靠应用层 telemetry 工作。eBPF 是加分项，不是硬依赖。

### Q697【场景】如果面试官要求你删掉一个模块保持项目聚焦，你会删哪个？为什么？

我会删 DeathStarBench adapter，至少从主线介绍里删掉。

原因是当前它是 integration runner (metadata overlay + artifact validation)，不是完整实测闭环。它对未来扩展有价值，但如果面试官觉得项目太大，它最容易被认为是“看起来很高级但没跑完”的部分。

我会保留 slow_score、状态机、adaptive P2C、retry budget、verifier 和实验脚本。这些都直接服务 fail-slow 主线，也有结果支撑。eBPF 我会作为 optional enhancement 讲，不放在最前面。

删掉 DeathStarBench 主线后，项目会更集中：自研 demo + 可复现实验 + 治理闭环。等以后真正跑完 DeathStarBench，再把它放回核心卖点。

### Q698【场景】如果要求你下一周继续完善项目，最优先的三个 issue 是什么？

第一个 issue：Controller 安全和 HA 的最小闭环。加 TLS/mTLS 配置入口，设计 etcd-backed registry 接口，至少把 memory/file/etcd backend 的边界定下来。这样项目从实验系统往生产化走一步。

第二个 issue：Verifier 流式处理和 trace 聚合。把 JSONL verifier 改成 streaming，支持大文件，输出失败样本和统计摘要。这个投入不算大，但能让真实 trace 闭环更可信。

第三个 issue：策略校验和紧急开关。Policy YAML 加 validation，限制 max attempts、probe ratio、eject threshold；Controller/SDK 加按 service 关闭 outlier ejection 或 fail-open 的开关。这个能解决面试里最常见的“策略配错怎么办”。

如果只能选一个，我会选策略校验和紧急开关。因为它直接关系到治理系统误判时能不能救回来。

### Q699【场景】如果要写系统设计文档，你会如何定义需求、非功能目标和拒绝做的范围？

需求我会这样定义。

功能需求：服务注册与发现；SDK 侧 gRPC resolver/balancer；endpoint telemetry 上报；slow_score 慢故障评分；endpoint 状态机；adaptive P2C 路由；retry budget；method-level policy；Prometheus metrics；trace verifier；故障注入实验。

非功能目标：无故障场景 overhead 可控；慢实例故障下 p99 降低；retry amplification 有上限；Controller 短时不可用不阻塞业务 RPC；实验可复现；策略变更可回滚；日志和指标能解释路由决策。

拒绝做的范围也要写。第一，不做完整生产级 service mesh，不承诺替代 Istio/Envoy。第二，不做多语言 SDK 的第一步。第三，不做完整 eBPF 网络诊断平台。第四，不声称已完成 DeathStarBench 真实测评。第五，不把业务幂等性问题完全交给 SDK 自动解决。

设计文档里写清“不做什么”很重要，否则项目会被无限扩展，主线反而不清楚。

### Q700【场景】如果要把 slow_score 解释给非技术面试官，你会使用什么类比？

我会用“外卖骑手配送表现评分”的类比。

假设一个平台有多个骑手都在送同一片区域。某个骑手没有失联，也没有拒单，但他每单都比别人慢很多。只看“是否在线”看不出问题，因为他在线；只看“有没有完成订单”也看不出，因为他最终都送到了。

slow_score 就像一个综合评分：看他最近送单耗时是不是比同组骑手慢很多，看有没有超时，看手上是不是积压太多单，看路上是不是有网络或道路问题。分数高了，平台不会立刻永远封掉他，而是先少派单；如果持续很差，就暂时不派正常订单；过一会儿再给少量订单测试他是否恢复。

这个类比对应 AegisMesh：骑手是 endpoint，订单是 RPC，请求耗时是 latency，少派单是降权，暂时不派是 EJECTED，少量测试单是 PROBING。

### Q701【场景】如果要把 retry budget 解释给业务方，你会如何说明它牺牲了什么、保护了什么？

我会这样说：retry budget 是给重试设置一个额度，防止系统在故障时用更多请求把自己打得更坏。

它牺牲的是少数请求的额外重试机会。预算用完后，有些请求本来可以再试一次，现在会直接失败返回。这个对单个用户请求来说，可能看起来少了一次成功机会。

它保护的是整体系统。没有预算时，下游已经慢或失败，所有上游还一起重试，流量会被放大。一个 1000 请求的故障窗口可能变成 2000 次下游尝试，甚至更多。下游更难恢复，其他用户也被拖慢。

所以 retry budget 的业务含义是：我们不追求每个失败请求都赌一次重试，而是控制整个系统的损失。对下单、支付这类非幂等请求，还要默认不重试，避免重复副作用。

### Q702【场景】如果要把 AegisMesh 与简历其他项目串联，你会如何突出基础设施能力？

我会把它放在“后端基础设施和稳定性工程”这条线上讲。

如果其他项目是业务系统，AegisMesh 可以说明我不只会写业务接口，还能处理服务之间的通信、治理和故障恢复。比如服务发现、gRPC、负载均衡、超时重试、指标、trace、故障注入，这些都是业务系统规模变大后必须面对的问题。

如果其他项目偏数据库或缓存，AegisMesh 可以补上分布式调用链这一块。数据库项目讲存储一致性和性能，AegisMesh 讲 RPC 链路的尾延迟和重试放大。

如果其他项目偏云原生，AegisMesh 可以和 Kubernetes、Prometheus、eBPF、service mesh 连接起来，说明我理解控制面/数据面、观测和平台化。

简历串联时我会用一句主线：我关注的是系统在压力和故障下的行为，不只是功能能跑。AegisMesh 正好把这条线集中在一个项目里。
