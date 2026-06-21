# 24. 面试防守、质疑与未来规划

## 简单

### Q645【简单】这个项目最容易被面试官质疑的点是什么？

最容易被质疑的点有四个。

第一，实验环境是单机 Docker Compose。它能验证机制，但不能直接代表多节点生产集群。单机里网络、CPU、容器调度都在同一台机器上，结果会受到本地资源竞争影响。

第二，Controller 不是 HA 控制面。现在有 memory registry 和 file-backed registry。file-backed 能解决 Controller 重启后的部分恢复问题，但不是 etcd/Consul 那种多副本一致性存储。

第三，DeathStarBench 目前是 integration planner，不是已经跑完的 benchmark。仓库能生成 Social Network 接入计划，但还没有完成外部 DeathStarBench 真实压测结果。

第四，eBPF 是增强信号，不是完整网络诊断平台。当前采集 TCP retransmit、connect error、connect latency，并融合进 telemetry。它不是 Cilium/Hubble 那种完整流量可视化系统。

我的回答会主动承认这些边界，然后把重点拉回已经完成的部分：Go/gRPC 控制面和 SDK、slow_score、状态机、adaptive P2C、retry budget、真实 trace verifier、实验脚本和结果报告都已经闭环。

### Q646【简单】你会如何解释 eBPF 部分目前不是完整生产级网络诊断系统？

我会直接说：项目里的 eBPF 目标很窄，是给 slow_score 提供网络层辅助信号，不是做一个完整网络诊断平台。

当前 agent 主要看 TCP 层事件，比如 retransmit、connect error、connect latency。它能帮助区分一部分网络慢和应用慢。例如应用层 RPC latency 高，同时 retransmit 也高，那网络更可疑；如果应用慢但 TCP 信号正常，可能是服务端业务处理慢。

但它还缺几块生产能力：自动 endpoint mapping 还不够完整，Docker bridge、NAT、sidecar 场景下归因会更复杂；没有覆盖 RTT、拥塞窗口、队列长度等更丰富信号；权限模型也需要最小化部署；跨内核发行兼容性要继续验证。

所以我不会把它说成生产级网络诊断系统。更准确的说法是：我实现了一个 Linux eBPF TCP telemetry path，并把网络信号接入慢故障评分，用来证明应用层 telemetry 可以和内核层信号结合。

### Q647【简单】你会如何解释 DeathStarBench 目前是集成规划而不是完整集成？

我会说得很清楚：当前仓库没有声明已经完成 DeathStarBench 测评。

现在的 `cmd/deathstarbench-adapter` 读取 `experiments/deathstarbench/social-network.yaml`，输出 compose command、workload command、Aegis Controller 地址和服务映射。它的作用是把接入计划结构化，告诉后续怎么接 Social Network。

真正完整集成还要做更多事：拉取外部 DeathStarBench repo，改造或包装服务调用路径，让流量经过 AegisMesh SDK 或代理，采集真实 trace 和 metrics，注入可重复故障，跑出 p99、throughput、retry amplification、recovery curve。

所以简历里我会写“DeathStarBench integration planner”，不会写“已在 DeathStarBench 上完成评测”。这个边界讲清楚，反而显得更可信。

### Q648【简单】你会如何解释单机实验和生产集群的差距？

单机实验的价值是可复现、可控、成本低。它适合验证机制是否有效，比如慢实例注入后 adaptive P2C 是否绕开，retry budget 是否控制放大，状态机是否进入 `DEGRADED/EJECTED/PROBING`。

但它和生产集群差距也很明显。生产里有跨节点网络、真实服务发现、Pod 调度、HPA、资源抢占、数据热点、多租户、发布过程和观测系统延迟。单机 Docker Compose 没法完整覆盖这些因素。

所以我会把实验结论限定在“单机可复现实验中有效”。比如可以说 adaptive P2C 在单实例 delay 场景把 median p99 从 348.682ms 降到 32.712ms。不能说它在所有生产微服务里都会降低 90%。

下一步要做的是固定多节点实验环境，接 Kubernetes 或 DeathStarBench，把现有结论放到更复杂拓扑里复验。

### Q649【简单】如果面试官说项目太大但每块不深，你怎么回应？

我会先承认项目横向模块多：Controller、Registry、SDK、metrics、slow_score、状态机、负载均衡、retry、breaker、verifier、eBPF、实验脚本都涉及到了。

然后我会把主线收回来：这个项目不是为了堆功能，而是围绕 fail-slow 治理做闭环。最深的线是“telemetry -> slow_score -> endpoint state -> adaptive routing -> recovery -> verifier/experiment”。其他模块服务于这条线。

如果面试官要深挖，我会优先带他看 slow_score 和 adaptive P2C。slow_score 解决如何识别慢实例，adaptive P2C 解决识别后如何改变流量。retry budget 解决重试不要把故障放大。这三块是项目的技术核心。

所以回应不是“每块都很深”，而是“我把系统端到端跑通，并且在慢故障检测、路由和重试控制上做了重点实现和实验验证”。

### Q650【简单】如果面试官只给你 3 分钟介绍，你如何取舍？

我会按这个顺序讲。

第一句话定义项目：AegisMesh 是一个用 Go/gRPC 写的自适应 RPC 治理系统，目标是处理 fail-slow 微服务实例。

第二段讲问题：普通健康检查能发现宕机，但发现不了“还能返回、只是很慢”的实例；固定阈值容易误伤；重试还会放大故障。

第三段讲方案：SDK 记录每个 endpoint 的 latency、error、timeout、inflight 和网络信号；Controller 计算 slow_score 和状态机；SDK 用 adaptive P2C 把流量从慢实例转走；retry budget 控制额外请求；PROBING 让恢复流量受限。

第四段讲数据：单机 Docker 实验里，慢实例场景 p99 从 round-robin 的 348.682ms 降到 adaptive P2C 的 32.712ms；retry amplification 从 2.0x 降到 1.15x；状态机能走完整恢复路径。

最后补一句边界：它不是生产级 mesh，当前是可复现实验系统；HA、TLS、多语言和真实 DeathStarBench 测评是后续路线。

### Q651【简单】如果面试官让你现场画架构图，你会画哪些组件和箭头？

我会画五块。

第一块是业务服务：frontend、user-service-a、user-service-b、order-service。frontend 通过 AegisMesh Go SDK 调下游。

第二块是 SDK 数据面：resolver、adaptive P2C balancer、telemetry interceptor、retry interceptor、retry budget、circuit breaker、trace writer。

第三块是 Controller 控制面：RegistryService、TelemetryService、PolicyService、HealthManager、slow_score、StateMachine。

第四块是观测：Prometheus/Grafana、trace JSONL、verifier、experiment recorder。

第五块是故障和网络信号：fault injector、eBPF agent。

箭头这样画：服务启动时向 Registry 注册和 heartbeat；SDK resolver 从 Controller 拉实例和 health state；业务 RPC 走 SDK balancer 到下游；SDK 上报 telemetry 到 Controller；Controller 计算 slow_score 和 state；state 再回到 resolver/balancer；PolicyService 把 retry/timeout/idempotency 下发给 SDK；eBPF agent 把 TCP 信号上报到 TelemetryService；verifier 读取 trace 检查策略是否生效。

### Q652【简单】如果面试官只看代码，你会带他看哪三个文件？

我会优先选这三个。

第一个是 `pkg/fault/slow_score.go`。这里能看到项目如何把 latency、error、inflight、network signal 和 absolute SLO 合成 slow_score。它回答“慢故障怎么判断”。

第二个是 `pkg/fault/state_machine.go`。这里能看到 `HEALTHY/DEGRADED/EJECTED/PROBING` 的状态迁移、连续窗口、ejection duration、recovery threshold。它回答“判断以后怎么稳定地处理，不抖动”。

第三个是 `sdk/go/aegisgrpc/adaptive_balancer.go`。这里能看到 SDK 如何在 Pick 时使用 endpoint state、slow_score、本地 EWMA、inflight 和 breaker 做路由。它回答“控制面结果怎么影响真实请求”。

如果还有时间，我会再看 `pkg/retry/budget.go` 和 `pkg/controller/telemetry_service.go`。前者讲 retry amplification，后者讲 telemetry 如何进入 Controller。

### Q653【简单】如果面试官只看实验，你会展示哪两张图或哪两个数字？

我会给出两个数字。

第一个是慢实例 delay 实验：round-robin 的 median p99 是 348.682ms，adaptive P2C 是 32.712ms，p99 降低 90.62%。这个数字最能说明 adaptive routing 对 fail-slow 有用。

第二个是 retry budget 实验：without budget 的 retry amplification 是 2.000x，with budget 是 1.150x。1000 个原始请求下，额外 retry 从 1000 次降到 150 次。这个数字说明系统不是靠无脑重试解决问题，而是在控制放大。

如果能补第三个，我会用 recovery curve：endpoint 从 `HEALTHY -> DEGRADED -> EJECTED -> PROBING -> HEALTHY`，说明不是只会摘除，还能受控恢复。

### Q654【简单】如果面试官问你项目失败点，你会说哪个真实限制？

我会说最大的真实限制是：当前实验仍然是单机 Docker 环境，没有完成多节点生产式验证。

这个限制很真实。AegisMesh 的核心机制已经跑通，也有数据支撑，但单机实验无法证明跨节点网络、Kubernetes 调度、多服务复杂拓扑下仍然有同样收益。尤其 eBPF 网络信号和 recovery 收敛时间，在多节点环境里可能会变。

我不会把它包装成“已经生产可用”。更好的回答是：项目当前完成的是可复现实验闭环，下一步最值得做的是 Kubernetes + DeathStarBench 或多节点 demo，把结论放到更接近真实环境里验证。

## 深度

### Q655【深度】项目的最大技术债是什么？

最大技术债是控制面还不是生产级控制面。

现在 Controller 是单进程，registry 可以 memory 或 file-backed。file-backed registry 能恢复未过期实例，但没有多副本一致性、leader election、watch 推送、认证授权和审计。PolicyService 也是 YAML 文件热加载，适合实验，但还没有完整的配置平台能力。

这会影响几个问题：Controller 重启后 health state 会丢，多个 Controller 不能安全共享文件 registry，SDK 只能靠轮询或 watch 简化实现，策略变更缺少审批和回滚。

如果只看算法，slow_score 和 adaptive P2C 是项目亮点；如果看生产化，控制面 HA 和安全是最该还的债。

### Q656【深度】如果要把项目生产化，第一步应该补 HA、TLS、K8s、还是多语言 SDK？

我会第一步先补 TLS/mTLS 和控制面 HA 的基础，不会先做多语言 SDK。

原因很简单：没有认证和 HA，系统不适合进入真实环境。SDK 再多语言，也只是把不安全、不可靠的控制面扩散到更多服务。

第一步可以这样排：

1. gRPC TLS/mTLS：SDK、agent、Controller 之间加身份认证，服务注册和 telemetry 按身份授权。
2. etcd-backed registry：替代 file-backed registry，支持 lease、watch、revision。
3. Controller 多副本：leader election 负责 sweep 和状态 tick，读请求可以多副本处理。
4. SDK fallback：Controller 不可用时使用缓存地址和最近 policy，避免业务请求立刻失败。

Kubernetes 接入可以和 HA 并行做，但它依赖前面的安全和存储。多语言 SDK 放第二步，等 API 和策略语义稳定后再做。

### Q657【深度】如果 slow_score 误判，如何提供解释性和回滚机制？

解释性要从数据结构开始做。Controller 不应该只输出一个 slow_score，还应该记录每个组成项：latency_score、error_score、inflight_score、network_score、absolute_slo_score，以及当时的 p95、EWMA、request_count、window 时间。

这样误判时可以回答：它是因为 latency 高被打慢，还是因为 error 高，还是因为 TCP retransmit。没有 breakdown，只给一个分数，很难复盘。

回滚机制分两层。

第一层是策略回滚。比如阈值设得太低，就回滚到上一个 PolicySnapshot revision。SDK watch 到旧修订后恢复旧策略。

第二层是状态回滚或紧急开关。可以按 service 清空 health state，或者临时关闭 outlier ejection，让系统 fail-open。还可以设置最大摘除比例，避免误判时摘光实例。

后续我会加 state transition audit：每次 `HEALTHY -> DEGRADED`、`DEGRADED -> EJECTED` 都记录 reason 和 score breakdown。这样误判不再是黑盒。

### Q658【深度】如果 adaptive routing 引发流量振荡，如何诊断和缓解？

先诊断是不是振荡。看几个时间序列：route weight、endpoint inflight、slow_score、p99、state transition。如果两个实例的流量来回切，slow_score 也跟着互相升降，就很像 routing feedback 振荡。

常见原因有几种：EWMA 太敏感，alpha 太大；状态机阈值太近；resolver refresh 太慢或太快；所有客户端同时看到同一个状态并一起切流；PROBING ratio 太高；样本窗口太短。

缓解方式也有几层。

1. 增加 hysteresis：degraded/eject/recovery 用不同阈值，连续窗口后才迁移。
2. 平滑权重变化：不要 slow_score 一变就大幅调整权重，可以限制每个窗口权重变化幅度。
3. 加随机性：P2C 本身有随机候选，PROBING 也要小比例采样，避免所有客户端一致行动。
4. 设置最小健康实例数和最大摘除比例。
5. 调整窗口：低 QPS 服务用更长窗口，避免样本太少导致抖动。

AegisMesh 现在已经有连续窗口、状态机和 PROBING ratio。生产化还可以继续加权重变化速率限制和全局摘除上限。

### Q659【深度】如果 retry budget 让部分请求不再重试，用户可用性下降如何解释？

这要从系统整体可用性解释。retry budget 不是为了让每个单请求都尽可能重试，而是为了防止故障时把下游打垮。

没有 budget 时，某个下游已经失败，所有客户端还继续重试，会把 1000 个原始请求变成 2000 次甚至更多下游尝试。短期看，个别请求多了一次机会；整体看，下游压力更大，恢复更慢，还可能拖垮其他依赖。

有 budget 时，一部分请求会少一次重试，可能看起来单请求成功机会变小。但系统避免了 retry storm。用户层面更希望系统稳定退化，而不是所有人一起变慢。

工程上可以配合优先级：高价值、幂等、短链路请求可以分到更多预算；低优先级或非幂等请求少重试或不重试。retry budget 的目标不是减少可用性，而是在故障中控制风险。

### Q660【深度】如果 eBPF 采集不到事件，系统是否仍然有效？

仍然有效。eBPF 在 AegisMesh 里是增强信号，不是主路径依赖。

核心治理闭环依赖的是 SDK 应用层 telemetry：request count、error count、timeout count、inflight、EWMA、p95。这些足够驱动 slow_score、状态机和 adaptive P2C。eBPF 采集不到事件时，network_score 可以为 0 或 unknown，系统仍然可以根据应用层 latency 做慢故障治理。

影响是网络故障归因会变弱。比如 packet loss 导致 RPC latency 上升，没有 eBPF 时 Controller 仍然能看到慢，但不容易判断是网络层还是业务层。实验里 eBPF packet loss 提升也比较小，所以我不会把 eBPF 说成效果主因。

生产里应该让 eBPF agent 可选。启动失败不能影响业务 RPC，也不能阻塞 Controller。它应该像额外传感器，而不是系统生命线。

### Q661【深度】如果 Controller 计算慢，是否应该把部分评分下沉到 SDK？

可以下沉一部分，但要小心一致性。

SDK 最适合做本地、低延迟、和当前连接强相关的计算，比如 EWMA、inflight、per-endpoint 本地 cost、retry budget、本地 circuit breaker。这些数据本来就在 SDK 里，放到 Controller 再回来会增加延迟。

Controller 适合做跨客户端聚合，比如同一个 endpoint 被多个 caller 看到的 p95、error rate、状态机迁移、policy 管理、审计和全局保护。Controller 能避免某个客户端的局部偏差直接影响全局状态。

如果 Controller 计算慢，我会先做优化：按 service 分片、异步处理 telemetry、压缩窗口、减少排序、用 histogram。真的需要下沉时，可以让 SDK 先计算 local score，Controller 再聚合多个 local score。不要让每个 SDK 独立决定 EJECTED，否则不同客户端看到的状态可能分裂。

### Q662【深度】如果服务实例数量非常多，Controller 和 SDK 的数据结构如何优化？

Controller 侧要避免全量扫描和全量返回。

Registry 可以按 service 分片，实例存储使用带 TTL 的索引。ListInstances 不应每次复制所有服务的所有实例，只返回目标 service。再进一步可以支持 WatchInstances，只推增量变更，而不是 SDK 每 3 秒全量拉取。

HealthManager 也要按 service 分片。slow_score 计算时，只计算有新 telemetry 的 service。p95 统计用 histogram 或 sketch，不保存所有 latency 样本。

SDK 侧也要控制地址列表大小。resolver 收到大量 endpoint 时，balancer 不应该每次 Pick 扫全部 endpoint。P2C 本来就是低开销算法，但候选列表维护、属性读取和 SubConn 状态也要优化。可以预计算 routable endpoints、按状态分组、缓存 effective weight。

Prometheus label 也要小心。大量 endpoint address 会造成高基数。生产里可能要用 instance ID、service、zone 这类稳定标签，避免 address 高频变化。

### Q663【深度】如果业务方不愿接入 SDK，这个项目如何演进？

如果业务方不愿改代码接 SDK，下一步就是把数据面从 SDK 迁到 sidecar 或代理层。

可以有三条路线。

第一，做 gRPC client wrapper，尽量把业务改动降到一行 Dial 替换。这是当前 SDK 路线里侵入性最低的改法。

第二，做 sidecar。服务仍然用原始 client，流量通过本地代理，代理负责服务发现、路由、retry、telemetry。这样跨语言友好，但要处理透明代理、连接管理和证书。

第三，接入现有 mesh，比如 Envoy/Istio。AegisMesh Controller 不再直接驱动 Go SDK，而是生成 xDS 或外部权重配置，把 slow_score 转成 endpoint weight 或 outlier ejection。

如果是个人项目，我会先做第一条，把 SDK 接入体验变好；如果是公司平台，多语言和低侵入会推动 sidecar 或 xDS 集成。

### Q664【深度】你在这个项目中做过哪些 tradeoff？哪一个最关键？

主要 tradeoff 有几个。

第一，SDK 模式 vs sidecar 模式。我选择 SDK，是因为 Go/gRPC 场景下实现快，能直接用 gRPC resolver/balancer/interceptor，也能拿到方法名和上下文。代价是跨语言能力弱。

第二，relative slow_score vs absolute SLO。我一开始用相对异常值识别慢实例，后来补 absolute SLO，解决所有实例都慢或单实例服务的盲区。代价是需要给服务配置 SLO。

第三，轮询 resolver vs watch 推送。轮询简单稳定，适合 demo；watch 收敛更快，但实现更复杂。当前 resolver 仍偏轮询，PolicyService 已经用了 WatchPolicy。

第四，file-backed registry vs etcd。file-backed 能快速补重启恢复，但不能 HA。它适合当前实现，不适合生产控制面。

第五，100% trace vs 性能开销。实验时写 JSONL trace 很方便，生产里要采样。

我认为最重要的 tradeoff 是 SDK 模式。它决定了项目能快速把 gRPC resolver、balancer、retry、telemetry 做到一条链路里，也决定了后续多语言和低侵入会成为挑战。

## 拓展

### Q665【拓展】未来如果做成开源项目，最应该补哪三类文档？

第一类是 quickstart。别人 clone 后，应该能在 10 分钟内跑起 demo：`make demo-up`、发一次 checkout、打开 Prometheus/Grafana、注入 delay、看到流量转移。命令要短，故障排查要写清楚。

第二类是 design docs。需要把 Controller、SDK、Registry、PolicyService、slow_score、state machine、adaptive P2C、retry budget、verifier、eBPF 的设计边界讲清楚。尤其要写“当前不是生产级 HA mesh”，避免误用。

第三类是 experiments docs。包括实验矩阵、参数、如何重复、如何合并结果、如何解读图表、哪些结论能写进简历、哪些不能夸大。这个项目的价值很大一部分来自实验，文档必须让别人复现。

如果还有精力，再补 CONTRIBUTING、SECURITY.md、release notes、API compatibility policy。这些是开源项目走向长期维护时需要的。

### Q666【拓展】如果把它作为硕士/论文项目，研究问题可以如何表述？

可以这样表述：

“在微服务系统中，fail-slow 实例不会触发传统健康检查，但会放大尾延迟和重试压力。本文研究如何结合客户端 RPC telemetry、网络层信号和控制面状态机，实现可解释、低开销、可恢复的慢故障治理。”

研究问题可以拆成三个。

第一个：如何在没有人工固定阈值的情况下识别慢实例？对应 relative slow_score、absolute SLO 和网络信号融合。

第二个：如何在识别慢实例后调整流量，同时避免振荡和误摘除？对应 endpoint state machine、adaptive P2C、PROBING ratio。

第三个：如何控制重试放大，并验证策略真的生效？对应 retry budget、trace verifier、实验矩阵。

论文式贡献不应该写“做了一个 mesh”，而应该写“提出并实现了一个面向 fail-slow 的 RPC 治理闭环，并在可复现实验中评估延迟、重试放大和恢复行为”。

### Q667【拓展】如果把它作为公司内部平台，Roadmap 应如何排期？

我会分四期。

第一期是安全和稳定：TLS/mTLS、身份认证、Controller 多副本、etcd-backed registry、SDK fallback、CI 和灰度发布。目标是能在内部小流量服务试用。

第二期是策略平台：PolicyService 接配置中心，支持审批、修订、回滚、dry-run、service owner、method idempotency、审计日志。目标是让业务团队能安全改策略。

第三期是生产观测：OpenTelemetry trace 接入、Prometheus dashboard、state transition audit、slow_score breakdown、自动实验报告。目标是出问题能解释。

第四期是规模和生态：Kubernetes EndpointSlice 接入、多语言 SDK 或 sidecar、xDS/Envoy 集成、多租户隔离、DeathStarBench 或真实业务 benchmark。目标是从单技术栈扩展到平台能力。

顺序不能反过来。先做多语言很酷，但没有安全、HA 和策略回滚，业务团队不敢用。

### Q668【拓展】如果要支持多语言、多集群、多租户，模块边界如何重构？

多语言要求把核心治理语义从 Go SDK 里抽出来。Policy proto、Telemetry proto、Trace schema、slow_score 输出、endpoint state 都要语言无关。Go SDK 只是其中一个实现。Java、Python、Rust SDK 共享同一套 proto 和行为测试。

多集群要求控制面分层。每个集群有本地 Controller 或 agent，负责本地 registry、telemetry 和路由状态；全局控制面负责策略、租户、跨集群流量和审计。不能让所有 SDK 都跨地域访问一个中心 Controller。

多租户要求每个核心对象带 tenant/namespace：ServiceInstance、PolicySnapshot、EndpointStatsSample、TraceRecord、AuditLog。Controller 的存储、查询、metrics、dashboard 都要按 tenant 隔离。

模块上，我会拆成：identity/auth、registry backend、policy store、telemetry ingestion、scoring engine、state manager、distribution layer、SDK adapters。这样每块可以独立替换，而不是所有逻辑都绑在 Go SDK 和单机 Controller 上。

### Q669【拓展】如果要和 Kubernetes Gateway API 或 service mesh 集成，切入点在哪里？

和 Kubernetes 集成，最自然的切入点是 EndpointSlice 和 CRD。

Controller 可以 watch EndpointSlice，把 Kubernetes service endpoints 转成 AegisMesh instances。这样业务服务不需要主动 Register。策略可以定义成 CRD，比如 `AegisPolicy`，再由 Controller 转成 PolicySnapshot。

和 Gateway API 集成，可以从入口策略和 backend policy 入手。Gateway API 主要管 north-south 流量，AegisMesh 管服务间 RPC。可以把 timeout、retry、traffic split 这类策略做映射，但 endpoint 级 slow_score 仍然要靠 AegisMesh 自己的 telemetry。

和 service mesh 集成，切入点是 xDS 或 Envoy endpoint weight。AegisMesh Controller 计算 slow_score 后，不直接给 Go SDK，而是把 endpoint 权重或 outlier 状态写进 Envoy 配置。这样可以把算法迁到 sidecar 数据面。

最现实的路线是先 K8s EndpointSlice + CRD，再考虑 xDS。

### Q670【拓展】如何设计一个公开 Demo，让面试官不用本地搭环境也能看到效果？

我会做一个录屏 + 在线静态报告 + 可下载结果包。

录屏控制在 3 到 5 分钟：启动 demo，打开 dashboard，注入 user-b 800ms delay，画面里能看到 p99 上升、slow_score 上升、endpoint 进入 EJECTED、route weight 下降，故障恢复后进入 PROBING 和 HEALTHY。录屏比让面试官本地跑更稳。

在线静态报告放核心图：slow-instance p99 对比、retry amplification 对比、recovery curve、probe ratio、absolute SLO。每张图旁边放命令和结果 CSV 路径。

结果包包含 `experiments/results/combined`、run_meta、plot 脚本、README。面试官想验证时可以下载后运行 check_results，而不必起 Docker。

如果要更进一步，可以做一个只读 Grafana snapshot 或 GitHub Pages 页面，把实验报告和架构图放上去。公开 Demo 的目标不是让所有人复现实验，而是让人快速相信项目能跑、结果有证据。

### Q671【拓展】如果让你重写一遍，哪些模块会保留，哪些模块会重做？

我会保留几块。

slow_score 的方向会保留，尤其是 relative outlier + absolute SLO 的组合。状态机也会保留，`DEGRADED/EJECTED/PROBING` 这套恢复路径是有价值的。adaptive P2C 和 retry budget 也会保留，因为实验结果能说明它们解决了实际问题。

我会重做几块。

第一，控制面存储从一开始就抽象成 backend，memory/file/etcd 都走同一套接口，避免后面补 HA 时改太多。

第二，SDK 生命周期要更完整。现在 `DialService` 返回 `*grpc.ClientConn`，后台 reporter/watcher 的生命周期主要靠外部 context。重写时我会返回一个包装类型，Close 时统一关闭连接、watcher、reporter、trace writer。

第三，policy 模型会更早做 validation、revision、rollback 和 method/caller 多维匹配。

第四，实验框架会更早固定 run metadata 和图表生成，避免后面整理结果花太多时间。

eBPF 我会继续做，但会把它明确放成 optional module，避免让主线依赖过多内核环境。

### Q672【拓展】你如何把这个项目和你申请的岗位职责建立直接关联？

如果申请后端基础架构或中间件岗位，我会强调 RPC 治理、服务发现、负载均衡、重试控制、可观测性和故障恢复。这些都是基础架构工程师会遇到的问题。

如果申请云原生或平台工程岗位，我会强调控制面/数据面拆分、PolicyService、Prometheus/Grafana、Docker Compose、未来 K8s EndpointSlice/CRD/xDS 演进。说明我理解平台不是只写业务代码，还要考虑接入、运维和灰度。

如果申请 SRE 或稳定性岗位，我会强调 fail-slow、tail latency、retry storm、recovery curve、fault injection、实验报告和 SLO。这个项目能直接对应稳定性治理。

如果申请高性能网络或 eBPF 相关岗位，我会强调 eBPF TCP telemetry、应用层 telemetry 和网络信号融合，但也会承认当前 eBPF 还不是完整网络诊断系统。

面试时我会把项目和岗位 JD 对齐。比如 JD 写“微服务治理、服务网格、可观测性”，我就讲 Controller/SDK/metrics/verifier；JD 写“高可用和稳定性”，我就讲 retry budget、状态机、故障注入和恢复曲线。
