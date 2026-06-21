# 17. 实验设计、Benchmark 与结果可信度

## 简单

### Q449【简单】项目的必跑实验矩阵包含哪些场景？

必跑矩阵在 `experiments/config/experiment_matrix.json` 和 `docs/experiments.md` 里。主矩阵有六组：

1. `baseline`：`no_mesh` vs `aegismesh`
2. `single_instance_delay`：`round_robin` vs `adaptive_p2c`
3. `cpu_throttle`：`static_threshold` vs `slow_score`
4. `retry_budget`：`without_budget` vs `with_budget`
5. `packet_loss`：`no_ebpf_network_score` vs `ebpf_network_score`
6. `recovery_curve`：`adaptive_p2c`

这六组覆盖了项目最想证明的几件事：无故障时额外开销是多少，慢实例下 adaptive P2C 有没有收益，CPU 慢故障下 slow_score 有没有收益，retry budget 能不能限制放大，eBPF 网络信号有没有接入效果，以及 endpoint 状态机能不能走完恢复路径。

后面又补了两组机制实验：

- `probe-ratio`：验证 `PROBING` endpoint 是否只拿少量探测流量。
- `absolute-SLO`：验证所有实例一起变慢时，absolute SLO score 能不能补上 relative outlier 的盲区。

所以面试里我会把实验分成两层讲：主矩阵证明整体治理收益，补充实验证明两个后来加的机制没有停留在代码层。

### Q450【简单】baseline 实验比较什么？

baseline 比较的是无故障场景下，直接调用和 AegisMesh 调用的开销。

具体是：

- `no_mesh`：走 `frontend-direct`，不经过 Aegis resolver、balancer、retry、telemetry。
- `aegismesh`：走 `frontend-adaptive`，使用 AegisMesh SDK、adaptive P2C、retry budget、telemetry。

指标主要看：

- throughput RPS；
- p50 / p95 / p99 latency；
- error rate。

当前合并结果里，baseline 的 median p99 是：

```text
no_mesh   = 24.721 ms
aegismesh = 28.053 ms
```

也就是 AegisMesh 在无故障场景下 p99 多了 13.48%。这个数字很重要，因为治理系统不能只讲故障收益，也要承认正常路径成本。

我的解释是：这部分 overhead 来自 resolver/balancer、拦截器、telemetry 记录、retry 策略判断这些额外逻辑。它不是免费能力。真正要看的是：正常开销能不能接受，故障场景收益是否覆盖这部分成本。

### Q451【简单】single_instance_delay 实验为什么比较 round_robin 和 adaptive_p2c？

因为这个实验要验证负载均衡策略对 fail-slow 的处理能力。

round-robin 很公平，但它不看 endpoint 是否变慢。两个 user 实例里，如果 `user-b` 被注入 200ms delay，round-robin 仍然会持续把一半流量打到它上面。结果是用户侧 p99 被慢实例拖高。

adaptive P2C 的逻辑不同。它每次从候选 endpoint 中抽两个，再按 cost 选更好的。cost 里有 local inflight、EWMA latency、Controller 下发的 slow_score、endpoint state。慢实例的 cost 会升高，effective weight 会下降，流量就会转到健康实例。

当前结果很典型：

```text
round_robin  median p99 = 348.682 ms
adaptive_p2c median p99 = 32.712 ms
```

adaptive P2C 把 p99 降了 90.62%。这组实验是整个项目最容易讲清楚的核心证据：慢故障不是宕机，普通轮询会继续踩坑，自适应路由能把正常流量挪走。

### Q452【简单】cpu_throttle 实验为什么比较 static_threshold 和 slow_score？

CPU throttle 模拟的是资源层面的慢故障。服务还活着，端口还能连，RPC 也可能成功，但处理速度变慢，inflight 可能升高。

static threshold 是一个很朴素的基线：用固定阈值判断慢不慢。它的问题是不同服务、不同负载、不同机器上的正常 p99 差别很大。阈值太高会漏报，阈值太低会误伤。

slow_score 是项目自己的评分方法。它不只看一个固定 p99，而是把 latency、error、inflight、network signals 组合起来，并用 service 内部 median/MAD 做相对比较。后面还加了 absolute SLO，用来处理所有实例一起变慢的场景。

当前 CPU throttle 实验结果是：

```text
static_threshold median p99 = 46.596 ms
slow_score       median p99 = 26.559 ms
```

slow_score 降低了 43.00% p99。这个实验说明项目不是只换了一个 balancer，而是控制面评分也在起作用。

### Q453【简单】retry_budget 实验如何衡量 amplification？

retry amplification 的定义是：

```text
retry_amplification = total_downstream_attempts / original_requests
```

项目里的 `run_retry_amplification.py` 会先 scrape frontend 的 Prometheus metrics，记录 `aegis_rpc_requests_total` 中 destination 为 `retry-user-service` 的值。压测结束后再 scrape 一次，两者差值就是这段时间内的下游调用次数。

然后计算：

```text
original_requests = 压测请求数
total_attempts    = metrics after - metrics before
retry_attempts    = total_attempts - original_requests
amplification     = total_attempts / original_requests
```

实验里有一个专门的 `retry-user-service`，它始终返回 `UNAVAILABLE`。这样失败原因稳定，方便观察重试放大。

当前结果是：

```text
without_budget: 1000 original -> 1000 retries -> 2000 total -> 2.000x
with_budget:    1000 original -> 150 retries  -> 1150 total -> 1.150x
```

这个结果和默认 budget ratio 0.15 是对得上的：1000 个原始请求允许约 150 次额外重试。

### Q454【简单】packet_loss 实验为什么要分 no_ebpf_network_score 和 ebpf_network_score？

这个实验要隔离 eBPF 网络信号的作用。

`no_ebpf_network_score` 表示在 packet loss 下，只依赖 SDK 层面的 RPC telemetry，比如 latency、error、timeout、inflight。

`ebpf_network_score` 表示在同样 packet loss 下，额外启动 eBPF agent，把 TCP retransmit 和 connect error 上报到 Controller，让 slow_score 的 network component 也参与评分。

如果只跑一个 packet loss 实验，就很难说明 eBPF 到底有没有贡献。分成两个 variant，才能比较“只看应用/RPC 层”和“加入内核 TCP 信号”的差别。

当前结果是：

```text
no_ebpf_network_score median p99 = 27.539 ms
ebpf_network_score    median p99 = 26.456 ms
```

改善约 3.93%。这个数字不大，所以报告里不能夸大。更稳的说法是：eBPF 路径已经接入 slow_score，单机 packet-loss 实验显示小幅改善；更强结论需要多节点网络故障实验。

### Q455【简单】recovery_curve 需要记录哪些列？

`recovery_curve` 主要看状态机和路由权重随时间的变化。项目里的 `recovery.csv` 需要这些列：

```text
experiment
variant
timestamp_unix_ms
endpoint
slow_score
p99_latency_ms
route_weight
state
```

这些列分别回答几个问题：

- `timestamp_unix_ms`：什么时候发生状态变化；
- `endpoint`：哪个实例受影响；
- `slow_score`：Controller 评分有没有升高；
- `p99_latency_ms`：延迟是否真的变坏；
- `route_weight`：流量权重有没有下降；
- `state`：是否走过 `HEALTHY / DEGRADED / EJECTED / PROBING`。

只看最终状态不够。recovery curve 要证明的是过程：故障注入后 slow_score 升高，route_weight 下降，endpoint 被降级或摘除，故障清理后进入探测并恢复。

### Q456【简单】check_results.py 的作用是什么？

`check_results.py` 是实验结果的完整性检查脚本。

它会读取：

```text
latency.csv
retry.csv
recovery.csv
```

再读取：

```text
experiments/config/experiment_matrix.json
```

然后检查必跑 scenario/variant 是否都有证据行。

它还会输出派生结果：

- baseline 的 no_mesh vs aegismesh p99；
- single_instance_delay 的 round_robin vs adaptive_p2c p99；
- cpu_throttle 的 static_threshold vs slow_score p99；
- packet_loss 的 no_ebpf vs ebpf p99；
- retry 的 retries、total attempts、amplification；
- recovery 的状态集合和 max slow_score。

如果缺少某个实验行，它会列出 gap。默认情况下有 gap 会退出失败；加 `--allow-partial` 可以允许只检查部分实验。

这个脚本的价值很实际：防止我拿不完整结果写报告。它不是统计分析工具，但能保证“该有的对照组都存在”。

### Q457【简单】probe-ratio 实验验证了什么？

probe-ratio 实验验证 `PROBING` endpoint 不会一恢复就收到正常流量。

状态机里，一个 endpoint 从 `EJECTED` 进入 `PROBING` 时，不能马上当作健康实例使用。否则它刚恢复就可能被大量请求打爆，再次变慢。

项目里的做法是限制 PROBING 流量。实验流程是：

1. 注入 delay，让目标 endpoint 进入慢状态。
2. 等它被 EJECTED。
3. reset fault，让它进入 PROBING。
4. 读取 `recovery.csv` 找到 PROBING 时间窗口。
5. 读取 `frontend-adaptive.jsonl` trace，统计这个窗口内打到 probing endpoint 的比例。

当前结果是：

```text
probing endpoint traces = 560
user-service trace rows = 257258
probe_ratio = 0.2177%
```

实验检查上限设为 10%，结果通过。实现默认 probe ratio 是 2%，实测比 2% 还低，说明在这个窗口里 PROBING endpoint 只收到很少探测流量。

### Q458【简单】absolute-SLO 实验验证了什么？

absolute-SLO 实验验证的是：当所有实例一起变慢时，relative outlier scoring 会有盲区，而 absolute SLO score 能补上。

relative score 是横向比较。一个实例比同服务其他实例慢，它就能被发现。但如果 `user-a` 和 `user-b` 同时被注入 500ms delay，大家都慢，median 也变慢，relative score 可能不高。

所以项目加入：

```text
absolute_latency_score = p95_latency / latency_slo
latency_score = max(relative_score, absolute_latency_score)
```

实验跑两次：

- `absolute-slo-disabled`：`AEGIS_HEALTH_LATENCY_SLO=0s`
- `absolute-slo-enabled`：`AEGIS_HEALTH_LATENCY_SLO=100ms`

结果是：

```text
disabled: max slow_score = 0.377401, states = HEALTHY
enabled:  max slow_score = 1.007183, states = DEGRADED, HEALTHY
```

disabled 是 negative control。它证明只靠 relative score 时，all-slow 场景确实可能不触发状态变化。enabled 触发 DEGRADED，说明 absolute SLO 解决了这个盲区。

## 深度

### Q459【深度】单机 Docker Compose 实验的外部有效性有哪些限制？

单机 Docker Compose 适合验证机制，但不能等同于生产环境。

主要限制有这些：

- 所有容器共享一台机器的 CPU、内存、磁盘和 Docker bridge。
- 网络路径很短，不是真正跨机器、跨机房、跨 AZ 的网络。
- 容器调度竞争会影响 latency，尤其是 CPU throttle 实验。
- Docker bridge 的 packet loss 和真实网络丢包不完全一样。
- eBPF endpoint mapping 需要手工配置，容器 IP 也可能重建后变化。
- demo 服务逻辑很轻，没有真实数据库、缓存、消息队列、磁盘 IO。
- gRPC 长连接复用在单机里更稳定，生产里连接抖动更复杂。
- Controller、frontend、backend、observability 组件都在一台机器上，故障域没有隔离。

所以我不会说“这证明生产环境一定提升 90%”。更准确的说法是：单机实验能证明控制闭环和对照组差异，能说明算法方向有效；生产级外推还需要多节点、更真实 workload、更长时间窗口和更严格统计分析。

面试时主动讲这个限制反而更可信。系统项目最怕只报漂亮数字，不讲实验边界。

### Q460【深度】为什么无故障场景 AegisMesh 可能有 p99 overhead？这个 overhead 是否可接受？

AegisMesh 在正常路径上多做了不少事：

- 自定义 resolver 拉 Controller；
- balancer 做 adaptive P2C pick；
- interceptor 记录 telemetry；
- retry interceptor 判断是否可重试、是否有 budget；
- 本地 EWMA、inflight、breaker 状态更新；
- Prometheus metrics 和可选 JSONL trace 写入；
- Controller health state 周期刷新。

这些逻辑都会增加一点 CPU 和内存开销，也会影响 p99。当前无故障结果是：

```text
no_mesh   p99 = 24.721 ms
aegismesh p99 = 28.053 ms
overhead  = 13.48%
```

我认为这个 overhead 在项目目标里可以接受。AegisMesh 不是给极低延迟撮合系统做的，它解决的是微服务 fail-slow 下的尾延迟和恢复问题。在 slow-instance delay 场景里，p99 从 348.682ms 降到 32.712ms，这个收益远大于正常路径的 3ms 多开销。

但这不是无条件可接受。如果业务是单次 RPC 只有 1ms 预算的场景，13% p99 overhead 可能太高。工程上可以通过关闭 trace、降低 telemetry 频率、采样、减少高基数字段、优化 balancer hot path 来压低开销。

### Q461【深度】p99 latency 降低 90% 的实验中，如何排除负载生成器或连接复用造成的假象？

我会从实验设计和结果证据两边排除。

实验设计上，round-robin 和 adaptive P2C 使用同一套 Docker Compose、同一个 Controller、同一组 user/order 服务、同样的 fault 参数、同样的请求入口逻辑。不同的是 frontend 端口和 routing policy：

```text
8082 -> frontend-round-robin
8083 -> frontend-adaptive
```

压测脚本使用相同的 requests、concurrency 和 URL 路径。故障也只打到同一个目标：`aegis-user-b`。

连接复用方面，gRPC 确实会复用 HTTP/2 长连接，但负载均衡 pick 是 RPC 级别的。也就是说，不是每次新建 TCP 连接才做负载均衡，已有 SubConn 之间仍然可以按策略 pick。

结果证据上，不能只看 p99 一列。还要看：

- recovery.csv 里 slow endpoint 的 slow_score 是否升高；
- route_weight 是否下降；
- trace 或 upstream 分布是否显示流量离开慢实例；
- error_rate 是否相近；
- 多次重复的 median 是否方向一致。

如果只有一次 p99 低，我不会把它当强结论。当前报告用的是合并结果的 median，并且有 recovery/state 证据配合，所以比单次截图可信。

### Q462【深度】吞吐上升和尾延迟下降同时出现时，应该如何解释？

这在 fail-slow 场景里是合理的。

round-robin 继续把请求打到慢实例，很多请求要等 200ms 或更久。对于闭环压测器来说，请求完成慢了，单位时间能完成的请求也会减少，所以吞吐下降、p99 上升会同时出现。

adaptive P2C 避开慢实例后，请求更多落到健康实例上。健康实例完成得快，客户端等待时间短，压测器也能更快发出后续请求。所以会看到吞吐上升、尾延迟下降。

项目结果里，single slow instance 场景就是这样：

```text
round_robin  p99 = 348.682 ms, throughput = 254.478 RPS
adaptive_p2c p99 = 32.712 ms,  throughput = 1631.996 RPS
```

但解释时要小心。吞吐上升不等于系统总容量真的增长了几倍。它更多说明：慢实例不再拖住大量请求，闭环负载生成器能完成更多请求。真正做容量评估，还需要 open-loop rate-based 压测，固定 arrival rate，看不同策略下的成功率和排队情况。

### Q463【深度】retry amplification 从 2.0x 到 1.15x 的结论如何验证不是偶然？

这组实验比较容易验证，因为它有明确公式。

无 budget 时，默认最大尝试次数是 2，`retry-user-service` 又稳定返回 `UNAVAILABLE`。所以每个原始请求都会失败一次再重试一次。理论上 1000 个原始请求会变成 2000 次下游调用，amplification 接近 2.0x。

有 budget 时，默认 budget ratio 是 0.15。1000 个原始请求允许大约 150 次额外重试，所以总调用接近 1150，amplification 接近 1.15x。

当前结果是：

```text
without_budget median amplification = 2.000x
with_budget    median amplification = 1.150x
```

同时结果来自 18 条 retry rows，两个 variant 各 9 条。这个不是只跑了一次。

验证时还要看 metrics 口径。脚本不是根据客户端猜测重试次数，而是 scrape `aegis_rpc_requests_total`，只统计 destination 为 `retry-user-service` 的下游 attempts。也就是说，它数的是实际打到下游的 RPC attempts。

如果还想增强可信度，可以再跑 5 到 10 轮 retry-only，输出每轮 amplification，报告 median、min/max、IQR。由于这个实验是强确定性的，波动应该很小。

### Q464【深度】packet loss 提升只有小幅度时，应该如何在简历和面试中表述？

不要把它写成“eBPF 大幅降低 p99”。这会被追问打穿。

当前结果只有：

```text
27.539 ms -> 26.456 ms
3.93% lower
```

这个数字说明不了“强性能收益”。更稳的表达是：

```text
实现 Linux eBPF TCP telemetry，采集 retransmit/connect error 并接入 Controller slow_score；在单机 packet-loss 实验中验证网络信号链路可用，p99 小幅下降 3.93%。
```

面试时我会补一句：eBPF 在这个项目里的定位是增强观测和辅助归因，不是主性能来源。主性能收益来自 adaptive P2C 和 slow_score 对慢实例的避让。eBPF 的价值在网络故障更真实、跨节点链路更复杂时会更明显，但我当前还没有跑多节点实验，所以结论要克制。

这种说法比夸大数字更好。面试官通常更看重你是否知道实验结果能支持什么、不能支持什么。

### Q465【深度】为什么 recovery 实验需要降低阈值？这会不会削弱生产可信度？

生产默认阈值比较保守：

```text
degraded_threshold = 1.5
eject_threshold = 2.5
consecutive_windows = 3
ejection_duration = 30s
```

这适合生产思路：不要因为一两个窗口抖动就摘除实例。

但单机演示有时间限制，fault duration、load duration、resolver refresh、telemetry report、state machine tick 都比较短。如果还用生产阈值，endpoint 可能只表现为 slow_score 升高，但不会走到 DEGRADED/EJECTED/PROBING，演示看不到完整状态路径。

所以 recovery 实验降低阈值：

```text
AEGIS_DEGRADED_THRESHOLD=0.5
AEGIS_EJECT_THRESHOLD=0.8
AEGIS_CONSECUTIVE_WINDOWS=1
AEGIS_EJECTION_DURATION=10s
AEGIS_RECOVERY_THRESHOLD=0.3
```

这不是为了伪造结果，而是为了在可控短实验里观察状态机行为。算法没变，变的是策略参数。

它会削弱“生产默认值下恢复时间就是这个数字”的可信度。报告里不能说生产会 3s DEGRADED、8s EJECTED。应该说：在单机实验阈值下，状态机能走完 `HEALTHY -> DEGRADED -> EJECTED -> PROBING -> HEALTHY`；生产阈值需要按 SLO 和误摘除成本重新设置。

### Q466【深度】probe ratio 实测低于默认 2% 时，如何解释采样窗口和 resolver 刷新的影响？

默认 probe ratio 是 2%，但实验测到的是 0.2177%。这不矛盾。

原因在于实测比例不是对一个理想无限长窗口算的，而是在 Controller 记录到 `PROBING` 的时间窗口里，从真实 trace 里统计打到目标 endpoint 的比例。

中间有几个时序因素：

- Controller 看到状态变化后，resolver 不是瞬间收到，它按周期刷新。
- SDK 可能在某些窗口还没拿到最新 `PROBING` 状态。
- PROBING 窗口可能很短，trace 行数集中在健康 endpoint 上。
- adaptive P2C 只有在 pick 时才按 probe ratio 抽样。
- 如果健康 endpoint 足够多，PROBING endpoint 只作为少量探测候选。

所以实测值低于 2% 是合理的。它说明在这个实验窗口里，PROBING endpoint 没有被正常流量打爆。

实验检查用的是 `MAX_PROBE_RATIO=0.10`，也就是 10% 上限。这个上限比默认 2% 宽，是为了容忍单机采样窗口、resolver refresh 和 trace 时间对齐的误差。判断重点不是“必须刚好 2%”，而是“不能大量恢复流量打到 PROBING endpoint”。

### Q467【深度】absolute-SLO disabled 作为 negative control 的意义是什么？

negative control 的作用是证明现象不是随便一个 fault 都会触发。

在 absolute-SLO 实验里，故障是对 `aegis-user-a` 和 `aegis-user-b` 同时注入 500ms delay。这个场景下所有 peer 都慢，relative median/MAD score 容易失效。

disabled run 设置：

```text
AEGIS_HEALTH_LATENCY_SLO=0s
```

结果是：

```text
max slow_score = 0.377401
states = HEALTHY
```

enabled run 设置：

```text
AEGIS_HEALTH_LATENCY_SLO=100ms
```

结果是：

```text
max slow_score = 1.007183
states = DEGRADED, HEALTHY
```

这个对照说明：状态变化不是因为“压测脚本刚好让系统慢了”，而是因为 absolute SLO score 打开后，p95/SLO 进入了 latency score。disabled 作为负对照，证明 relative score 的盲区真实存在；enabled 作为正对照，证明新增机制补上了这个盲区。

### Q468【深度】如何设计多次重复、置信区间、显著性检验来增强说服力？

我会按实验类型分开做。

对 latency/p99 类实验，单次 p99 很容易受抖动影响，所以至少跑 5 到 10 次。每次写独立 run_id，不覆盖旧结果。报告里不要只写平均值，优先写：

- median；
- IQR；
- min/max；
- bootstrap 95% confidence interval。

p99 分布通常不太正态，直接用 t-test 不一定合适。bootstrap 对面试项目更够用，也容易解释。

对 retry amplification，结果更接近确定性。可以跑多轮，报告每轮 amplification。如果 without budget 稳定在 2.0x、with budget 稳定在 1.15x，再加上它和公式一致，可信度就很强。

对 route distribution 或 canary 90/10，可以按二项分布估算置信区间。样本量太小时不要下结论。

对 recovery time，要报告每轮 transition time，比如 first DEGRADED、first EJECTED、first PROBING、return HEALTHY。然后给 median 和范围。恢复曲线更适合画时间序列，不适合只报一个数字。

还有一点很重要：重复实验要固定环境。并发、请求数、fault 参数、容器修订、commit、机器负载、Docker Compose 配置都要记录。否则重复次数再多，也只是把不可控变量混在一起。

## 拓展

### Q469【拓展】微服务基准测试中如何处理 warm-up、GC、JIT、容器冷启动、CPU 频率变化？

warm-up 要单独处理。服务刚启动时会有连接建立、resolver 初始化、policy 拉取、Prometheus 初始化、文件缓存预热，这些不应该混进正式结果。项目里的 recovery 实验有 pre-fault 环节，就是这个思路。

Go 没有 JIT，但有 GC。压测时要关注 GC pause、堆增长和对象分配。如果某个变体多写 trace 或 metrics，GC 也可能影响 p99。可以用 `GODEBUG=gctrace=1` 或 pprof 进一步看。

如果接入 DeathStarBench 里的 Java/Node 服务，JIT 就要考虑了。Java 服务前几分钟性能可能和稳定后不同，必须先预热再采样。

容器冷启动也会影响结果。镜像刚启动、服务刚注册、gRPC 连接刚建立时，latency 会偏高。正式实验前应该先确认所有 `/healthz` 通过，再跑一段预热流量。

CPU 频率和温度也会影响单机实验。最好在实验前关闭其他重负载程序，保持电源模式稳定，避免一边跑 benchmark 一边编译大项目。

更严格的做法包括：

- 丢弃前几个统计窗口；
- 固定 `GOMAXPROCS`；
- 记录 CPU 型号、内核、Docker、Go 工具链；
- 每个变体交替运行，避免顺序偏差；
- 实验前后都 reset faults；
- 对结果用 median，而不是挑最好的一次。

### Q470【拓展】wrk、hey、vegeta、k6 在 RPC/HTTP 压测中各有什么差异？

`wrk` 性能很高，适合 HTTP 高吞吐压测。它支持 Lua 脚本，可以构造一些动态请求。缺点是安装和脚本门槛比 hey 高，默认更偏 HTTP。

`hey` 很简单，适合快速压一个 HTTP endpoint。命令短，输出直观。缺点是复杂场景、环节负载、脚本化能力弱。

`vegeta` 的优势是 rate-based。它可以固定请求到达率，比如每秒 1000 个请求，然后看系统排队、失败和 latency。做容量和过载实验时，vegeta 比闭环并发模型更清楚。

`k6` 更像完整负载测试平台。它支持 JS 脚本、场景编排、环节 ramp-up/ramp-down、阈值检查和云端报告。代价是比 hey/wrk 重。

AegisMesh 当前用的是项目内 Python 脚本，原因是它要把 latency rows 写成固定 CSV 格式，并和 experiment/variant/run_id 对齐。HTTP 层用 `/checkout` 入口，真实 RPC 压力发生在 frontend 到 user/order 的 gRPC 调用。

如果直接压 gRPC 服务，可以考虑 `ghz` 或 Fortio 这类工具。这里只问 HTTP 压测工具，我会补一句：AegisMesh 的业务入口是 HTTP，治理对象是内部 gRPC，所以工具选择要看压的是入口层还是 RPC 层。

### Q471【拓展】如何构造更真实的服务拓扑和 workload mix？

当前 demo 是 shop 风格：frontend 调 user-service 和 order-service。它适合讲清机制，但拓扑还很小。

更真实的 workload 可以从几方面扩展。

拓扑上，引入 fan-out：

```text
frontend -> user
frontend -> order
frontend -> inventory
frontend -> coupon
order -> payment
order -> shipment
```

再加一些状态依赖，比如数据库、Redis、消息队列。这样慢故障可能来自 RPC，也可能来自 DB 连接池、缓存 miss 或下游队列积压。

请求 mix 上，不要所有请求都是 `/checkout`。可以设计：

- 70% read；
- 20% checkout；
- 5% create/update；
- 5% admin 或 heavy query。

方法级别也要区分。读请求可以 retry，创建订单默认不能 retry，除非有 idempotency key。

数据分布上，要有 hot key 和长尾 payload。真实系统里少数用户、商品或商家会更热，P2C 和 cache 行为都会受影响。

故障上，要混合网络 delay、CPU throttle、应用 sleep、数据库慢查询、连接池耗尽。只测一种 fault，结论比较窄。

最终目标是让 workload 能暴露治理系统的 tradeoff：它是否能避开慢点，是否会误伤正常点，是否会引发 retry storm，恢复是否会抖动。

### Q472【拓展】如果把 DeathStarBench 纳入真实实验，需要改哪些部署和观测脚本？

当前项目里的 DeathStarBench 是 integration planner，不是已经跑完的 benchmark。要做成真实实验，需要补几层工作。

第一，部署层。要 clone DeathStarBench，选择一个基准，比如 Hotel Reservation 或 Social Network。然后改 Compose 或 Kubernetes YAML，让 AegisMesh 能接入服务发现和 RPC 路径。DeathStarBench 里很多服务不是 Go/gRPC，需要考虑 sidecar 或网关方式，而不是直接复用 Go SDK。

第二，命名和映射。要把 DeathStarBench 的服务名映射成 AegisMesh service / instance，比如：

```text
frontend -> frontend
profile -> profile-service
reservation -> reservation-service
```

eBPF endpoint-map 也要从实际容器 IP/port 或 Kubernetes EndpointSlice 自动生成，不能手填一堆地址。

第三，负载生成。DeathStarBench 自带 workload generator，脚本要把它的输出转换成项目统一的 latency/retry/recovery CSV，至少要有 p50/p95/p99、throughput、error rate。

第四，故障注入。要能对某个 DeathStarBench 服务注入 delay、loss、CPU throttle，最好也能注入应用层慢故障。

第五，观测和验证。Prometheus scrape config 要收集 Aegis Controller、SDK/sidecar、DeathStarBench 服务指标。trace/verifier 要能拿到真实路径，至少能做 route distribution 和 forbidden edge。

最后是报告口径。如果没有真实跑出 CSV，就只能写“提供 DeathStarBench 接入规划”。跑出一组真实结果后，才能写“在 DeathStarBench 某个场景上评测”。

### Q473【拓展】如何做 A/B 对照，确保只有一个变量变化？

A/B 对照的核心是只改一个变量。

比如 single_instance_delay：

- A：`round_robin`
- B：`adaptive_p2c`

其他东西都要保持一致：同一批容器、同一故障目标、同一 delay/jitter、同一并发、同一请求数、同一压测脚本、同一时间长度。

retry_budget 也是：

- A：without budget
- B：with budget

下游都调用同一个 `retry-user-service`，它都返回 `UNAVAILABLE`。这样差异才能归因到 budget admission，而不是下游行为不同。

还要注意顺序偏差。可以交替运行 A/B，比如 A-B-B-A，或者多轮随机顺序。每轮前后都 reset faults。结果写到独立 run_id，最后 merge。

如果两个变体必须使用不同 frontend 端口，也要保证它们启动参数只差一个策略字段。这个项目通过 `frontend-round-robin`、`frontend-adaptive`、`frontend-retry-unbudgeted`、`frontend-retry-budgeted` 来实现。

报告里应该写清楚控制变量，不然面试官会怀疑是环境波动。

### Q474【拓展】如何把实验结果转成面试官信任的图表和复现实验步骤？

我会准备四类图。

第一，p99 对比图。横轴是 scenario，纵轴是 median p99。重点呈现：

```text
round_robin 348.682 ms vs adaptive_p2c 32.712 ms
static_threshold 46.596 ms vs slow_score 26.559 ms
```

第二，retry amplification 图。画 without budget 和 with budget 的 total attempts：

```text
2000 vs 1150
2.000x vs 1.150x
```

第三，recovery 曲线。x 轴是时间，y 轴分别画 slow_score、route_weight、state。状态可以用背景色标出 DEGRADED/EJECTED/PROBING。

第四，补充机制表。probe ratio 和 absolute SLO 不一定需要复杂图，一张表就够：

```text
probe ratio = 0.2177%
absolute SLO disabled max slow_score = 0.377401
absolute SLO enabled max slow_score = 1.007183
```

复现实验步骤要短。面试官不会愿意看十页命令。我会给最小路径：

```bash
make experiments-up
RUNS_DIR=experiments/results/runs REQUESTS=1000 CONCURRENCY=32 make bench-single-machine
make merge-results
python experiments/scripts/check_results.py --results experiments/results/combined
make report
```

再附上机器环境、commit、Docker/Go 工具链和 raw CSV 路径。图表负责讲结论，CSV 和命令负责让结论可信。

### Q475【拓展】如何处理 benchmark 中失败请求与 latency 百分位的统计口径？

失败请求不能简单扔掉，也不能简单混进成功 latency 里不说明。

如果只对成功请求算 p99，结果可能很好看，但错误率已经很高。比如 retry-user-service 实验里 error_rate 是 1.0，这是预期，因为它专门用于测 retry amplification。如果只看 latency，就会误导。

我会分开报告：

- successful latency p50/p95/p99；
- error_rate；
- timeout_count；
- total attempts；
- retry amplification。

对 timeout 请求，是否计入 latency 要提前定义。常见做法有两种：

1. 成功 latency 只统计成功请求，失败单独报 error_rate。
2. all-request latency 把 timeout 记成 deadline 值，反映用户等待成本。

两种都可以，但不能混着用。项目的 CSV 里有 `error_rate`，所以 p99 结论要和 error_rate 一起看。

还有闭环压测的陷阱。大量失败请求如果很快返回，latency 可能下降，但这不代表系统变好。所以 retry budget 实验报告里我会强调：它不是可用性实验，而是放大控制实验。error_rate=1.0 是设计出来的。

### Q476【拓展】如果面试官要求现场复现一个最小实验，你会选择哪个实验？

我会优先选 retry budget 实验。

原因很简单：它不依赖 root、不依赖 eBPF、不依赖 tc，也不需要调复杂状态机阈值。只要 Docker Compose 跑起来，`retry-user-service` 稳定返回 `UNAVAILABLE`，结果就很容易复现。

最小命令是：

```bash
make experiments-up

RUNS_DIR=experiments/results/runs \
RUN_ID=retry-demo \
REQUESTS=1000 \
CONCURRENCY=32 \
REPETITIONS=1 \
make bench-retry-repeat

make merge-results
python experiments/scripts/check_results.py --results experiments/results/combined
```

我会现场解释预期：

```text
without_budget 接近 2.000x
with_budget 接近 1.150x
```

如果机器是 Linux，并且 Docker 容器有 `NET_ADMIN`，第二选择是 single_instance_delay。它的现场效果更直观：round-robin 被慢实例拖住，adaptive P2C 避开慢实例。但它需要 `tc` 故障注入，现场风险比 retry budget 高。

所以现场演示我选 retry budget，报告里重点讲 single_instance_delay。一个稳，一个冲击力强。
