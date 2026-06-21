# 01. 项目定位、业务问题与讲述

## 简单

### Q001【简单】AegisMesh 一句话怎么定义？它是 RPC 框架、服务治理系统、还是 Service Mesh？

AegisMesh 可以一句话说成：面向微服务慢故障的自适应 RPC 治理系统。

它不是从零写一个 RPC 协议，底层还是 gRPC；也不是完整的 sidecar Service Mesh，因为主要治理逻辑在 Go SDK 和 Controller 里。

面试里我会把它定义成 SDK-based service governance system。客户端 SDK 负责 resolver、balancer、telemetry、retry 和 circuit breaker；Controller 负责 registry、policy、slow_score、endpoint 状态和健康信息下发。它解决的是服务调用过程中“节点没挂，但明显变慢”的问题。

### Q002【简单】这个项目主要解决 fail-slow 还是 fail-stop？两者有什么区别？

主要解决 fail-slow。

fail-stop 是服务直接不可用，比如进程退出、端口断开、健康检查失败。fail-slow 是服务还活着，也能返回结果，但延迟显著升高，最终拖慢整体 p99。普通熔断和健康检查对 fail-stop 比较有效，但对 fail-slow 不够敏感，因为慢节点并没有彻底失败。

AegisMesh 要处理的就是这种情况：endpoint 还能响应，但已经在拖慢尾延迟。系统会先降低它的权重，严重时摘除，之后再用 PROBING 给少量恢复流量。

### Q003【简单】为什么普通健康检查发现不了慢故障？

因为健康检查通常只看服务是否存活，比如 TCP 能否连通、HTTP `/healthz` 是否返回 200、gRPC health check 是否 OK。慢故障下这些检查都可能正常。

真正的问题发生在业务 RPC 上：某个实例可能只是在数据库、CPU、网络或锁竞争上变慢，健康接口仍然很快。AegisMesh 直接统计真实 RPC 的 endpoint 级延迟、错误、in-flight 和网络信号，所以能看到健康检查看不到的慢调用。

### Q004【简单】项目里的 Controller、SDK、Registry、Telemetry 分别承担什么职责？

这几个组件可以这样讲。

Controller 是控制面入口，接收注册和 telemetry，计算 slow_score，维护 endpoint 状态，再把实例和健康信息返回给 SDK。

SDK 是数据面，嵌入在 Go gRPC 客户端里，负责服务发现、resolver、adaptive P2C 负载均衡、retry budget、circuit breaker、RPC 指标记录和 trace 输出。

Registry 负责服务注册和 TTL 租约。项目里有 in-memory registry，也有 file-backed registry，用于 Controller 重启后的本地恢复。

Telemetry 是反馈通道。SDK 把每个 upstream 的请求数、错误数、EWMA、p95、in-flight 等窗口数据报给 Controller，Controller 再据此更新健康状态和路由权重。

### Q005【简单】你为什么选择 Go 和 gRPC 来实现这个项目？

Go 适合写这类网络控制面和 SDK：并发模型简单，标准库和 Prometheus 生态成熟，写服务、CLI、实验工具都比较方便。

选 gRPC 主要是因为它的扩展点够直接。resolver、balancer、interceptor、metadata、deadline 这些机制都能用上，不需要改业务 proto 就能接入治理逻辑。另一个原因是它在内部微服务 RPC 里很常见，项目结构比普通 HTTP 反向代理 demo 更接近真实调用链。

### Q006【简单】这个项目和普通的微服务 Demo 最大区别是什么？

普通微服务 Demo 通常只说明“服务能互相调用”。AegisMesh 关心的是调用过程中发生慢故障以后系统怎么反应。

项目里有 Controller、SDK、Telemetry、slow_score、状态机、adaptive P2C、retry budget、circuit breaker、fault injector、Prometheus/Grafana、verifier 和实验脚本。user/order/frontend 只是载体，真正要验证的是慢故障出现后，系统能不能发现、避让、恢复和复盘。

### Q007【简单】你在项目中最核心的技术贡献是哪三点？

我会讲三点。

第一是 fail-slow 检测链路。SDK 上报 endpoint 级 telemetry，Controller 计算 relative + absolute SLO slow_score，再用状态机管理 `HEALTHY/DEGRADED/EJECTED/PROBING`。

第二是自适应路由。SDK 的 adaptive P2C 会结合 in-flight、EWMA、slow_score、状态惩罚和 probe ratio，把流量从慢节点上移走。

第三是可靠性保护和验证。retry budget 限制重试放大，circuit breaker 做本地保护，verifier 用真实 SDK trace 检查路由、重试和 forbidden edge 是否符合预期。

### Q008【简单】从一次前端 checkout 请求开始，请描述请求路径和治理路径。

业务路径是：用户请求 HTTP frontend 的 `/checkout`，frontend 通过 AegisMesh Go SDK 调用 `user-service` 和 `order-service` 的 gRPC 方法。SDK 不直接写死 upstream 地址，而是向 Controller 查询服务实例，再通过 gRPC resolver 和 balancer 选 endpoint。

治理路径是：SDK 在每次 RPC 后记录 upstream、延迟、状态码、attempt、in-flight 等信息，并定期上报 telemetry。Controller 根据窗口数据计算 slow_score，推进 endpoint 状态。下一轮 resolver 刷新时，SDK 拿到新的状态和 slow_score，adaptive P2C 根据这些信息调整路由。

### Q009【简单】项目里哪些功能属于控制面，哪些属于数据面？

控制面包括 Controller、Registry、PolicyService、TelemetryService、slow_score 计算、endpoint 状态机、健康状态指标和策略下发。

数据面包括 Go SDK、gRPC resolver、adaptive P2C balancer、interceptor、retry budget、circuit breaker、trace metadata/JSONL 输出，以及业务 RPC 的实际调用路径。

简单说，控制面回答“哪些实例健康、策略是什么”；数据面回答“这次请求发给谁、要不要重试、要不要快速失败”。

### Q010【简单】这个项目如果放到简历上，你希望面试官看到什么关键词？

我希望面试官一眼看到这些词：fail-slow、RPC governance、Go/gRPC SDK、Controller control plane、EWMA、slow_score、endpoint state machine、adaptive P2C、retry budget、circuit breaker、Prometheus/Grafana、real trace verifier、eBPF TCP telemetry。

性能结果也很重要：慢实例场景 adaptive P2C 相比 round-robin 将 median p99 从 348.682 ms 降到 32.712 ms；CPU throttle 场景 slow_score 将 p99 降低 43.00%；retry budget 将 amplification 从 2.000x 控制到 1.150x。

## 深度

### Q011【深度】AegisMesh 的核心假设是什么？这个假设在哪些生产场景里成立，在哪些场景里可能不成立？

核心假设是：同一个 service 的不同 endpoint 在同一时间窗口内应该表现得大致接近。如果某个 endpoint 的延迟、错误率、in-flight 或网络异常明显高于同组实例，它很可能是慢节点，应该被降权或摘除。

这个假设在多副本无状态服务里比较成立，比如 user-service、推荐服务、搜索查询服务、读多写少的业务服务。它也适合实例间硬件、资源配额和下游依赖差异不大的场景。

这个假设也有边界。实例如果是异构的，比如不同机型、不同地域、不同发布变体混跑，横向比较就会有偏差。请求如果天然不均匀，比如某个实例固定处理更重的租户或更复杂的 key，也可能误判。还有两种典型盲区：所有实例同时变慢，或者服务只有一个实例，没有 peer 可以比较。

项目里用 absolute SLO score 处理后两类问题：最终 latency score 不是只看相对异常，而是 `max(relative_score, p95_latency / latency_slo)`。

### Q012【深度】为什么慢故障治理不能只靠超时和重试？

超时和重试只能处理“这次请求已经慢了或失败了”之后的单点补救，不能主动识别某个 endpoint 正在持续变慢。

只靠超时会有两个问题。超时时间设短了会误杀正常请求，设长了 p99 已经被拖垮。只靠重试也有风险：如果慢节点仍然被负载均衡选中，请求会持续打到它；如果下游已经过载，重试会放大流量，甚至形成 retry storm。

AegisMesh 的处理方式是把超时和重试放在更大的控制逻辑里：slow_score 先识别慢节点，adaptive P2C 降低流量，状态机做 ejection/probing，retry budget 限制额外请求。这样不是盲目重试，而是先减少打到坏 endpoint 的概率。

### Q013【深度】你如何证明 AegisMesh 不是把故障从一个端点转移到了另一个端点？

我不会只看 p99 是否下降，会同时看三类证据。

先看健康 endpoint 的 in-flight、错误率和延迟有没有被打爆。如果只是把压力挪到另一个 endpoint，那健康实例的 p95/p99 和 in-flight 会明显上升。

再看整体吞吐和错误率。慢实例实验里 adaptive P2C 的 p99 从 348.682 ms 降到 32.712 ms，同时吞吐明显高于 round-robin，不是靠丢请求换来的。

最后看恢复曲线。故障 endpoint 被降权和摘除后，不是永久不用，而是进入 PROBING。项目里 PROBING 流量实测为 0.2177%，说明恢复窗口是小流量探测，不会突然把压力打回原 endpoint。

更严谨的做法是加 per-endpoint traffic share、in-flight 和 error rate 对比图，看健康 endpoint 的负载增加是否仍在可承受范围内。

### Q014【深度】如果所有实例同时变慢，基于相对异常值的检测会有什么盲区？项目如何处理这个问题？

相对异常值检测依赖 peer comparison。如果所有实例都变慢，service 内部的 median 也会变高，每个实例看起来都“不比别人差”，relative slow_score 可能上不去。

项目用 absolute SLO score 补这个盲区。Controller 支持配置 latency SLO，例如 `100ms`。最终 latency score 取 `max(relative_median_mad_score, p95_latency / latency_slo)`。这样即使所有实例都慢，只要 p95 超过 SLO，score 也会升高。

实验里做了对照：关闭 absolute SLO 时 max slow_score 是 0.377401，状态保持 HEALTHY；开启 `AEGIS_HEALTH_LATENCY_SLO=100ms` 后 max slow_score 到 1.007183，并出现 DEGRADED。

### Q015【深度】这个项目的控制闭环是什么？采样、评分、状态迁移、路由调整之间的反馈延迟是多少？

控制闭环是：SDK 采样 RPC -> 定期上报 telemetry -> Controller 计算 slow_score -> 状态机迁移 -> Registry/ListInstances 返回健康状态 -> SDK resolver 刷新地址属性 -> balancer 调整流量。

延迟由几个周期叠加：SDK telemetry reporter 默认 5s 左右上报一次，resolver 默认几秒级刷新，状态机还可能要求连续多个窗口才迁移。生产默认更保守，实验里为了能在单机短时间观察状态迁移，会降低阈值和 consecutive window。

所以 AegisMesh 不是毫秒级故障切换系统，而是秒级自适应治理系统。它适合 fail-slow 这种持续性异常，不适合代替本地 timeout 处理单个请求的瞬时抖动。

### Q016【深度】如果 telemetry 报告延迟、resolver 刷新间隔和状态机窗口不同步，会出现什么现象？

最直接的现象是状态滞后。Controller 里状态已经变了，但 SDK 还没刷新 resolver，短时间内仍可能把流量打到慢 endpoint。

窗口错位时，还会看到“分数和状态不同步”。比如 slow_score 已经先上升又下降，但状态机因为连续窗口和恢复阈值还没有迁移。

恢复窗口也会有短暂抖动。Controller 已经进入 PROBING，但客户端还在用旧地址属性；不同客户端刷新时间不同，流量分布也不会完全一致。

项目里通过 EWMA、连续窗口、ejection duration、recovery threshold 和 probe ratio 来降低抖动。后续如果要更接近生产，可以把 resolver 轮询改成 WatchInstances 流式推送，缩短控制面到数据面的传播延迟。

### Q017【深度】如果面试官质疑这是“轮子项目”，你会怎样说明它和简单封装 gRPC 的差异？

我会承认它不是要替代 gRPC。gRPC 已经解决了 RPC 编码、连接、超时、拦截器和基本负载均衡。AegisMesh 做的是 gRPC 之上的治理层。

差异在反馈控制。简单封装 gRPC 通常只是统一 dial、加日志、加 timeout。AegisMesh 有 endpoint 级 telemetry、Controller 评分、状态机、adaptive P2C、retry budget、probe ratio、PolicyService、真实 trace verifier 和实验矩阵。它不是把 gRPC API 包一层，而是把“观测 -> 判断 -> 调整路由 -> 验证结果”串起来。

我也不会把它说成生产级 service mesh。更准确的说法是：这是一个 SDK-based RPC governance prototype，并且围绕 fail-slow 做了成体系的实验。

### Q018【深度】你认为这个项目最接近生产系统的部分是什么，最像原型验证的部分是什么？

最接近生产系统的是 SDK 数据面、telemetry、retry budget、method-level idempotency、Prometheus 指标、PolicyService 和状态机。这些模块的设计和真实 RPC 治理系统比较接近，也有测试和实验支撑。

最像原型验证的是控制面的高可用和外部生态接入。当前 file-backed registry 只能解决本地重启恢复，不是 etcd/Raft 级别的 HA。DeathStarBench 目前是 integration planner，不是完成的外部 benchmark。eBPF telemetry 有真实采集代码，但 endpoint mapping 和多机网络实验还可以继续加强。

所以我会把项目定位为“有治理链路和实验结果的系统项目”，而不是生产级 service mesh。

### Q019【深度】项目设计中哪些地方偏保守，哪些地方偏激进？为什么？

偏保守的是状态机和重试。默认 degraded/eject threshold、连续窗口、ejection duration 都不会设得太敏感，避免因为短暂抖动频繁摘除 endpoint。retry budget 也偏保守，因为重试一旦失控会直接放大故障。

偏激进的地方是实验环境里的阈值。为了在单机几分钟实验里观察 `DEGRADED/EJECTED/PROBING`，实验 compose 会降低阈值、减少 consecutive window。这不是生产默认值，而是为了让故障曲线可观察。

adaptive P2C 介于两者之间。它先通过权重和成本函数渐进避让慢节点，不是立刻摘除；但一旦状态到 EJECTED，就会从正常路由里移除。

### Q020【深度】如果只保留一个亮点用于面试深挖，你会选择 slow_score、adaptive P2C、retry budget、eBPF、还是 verifier？为什么？

我会选 slow_score。

原因是 slow_score 能把项目讲通。adaptive P2C 需要 slow_score 作为路由成本输入，状态机需要 slow_score 做迁移依据，PROBING 恢复也依赖 score 回落，eBPF 网络信号也是进入 slow_score 的一个维度。顺着它讲，可以自然带出 telemetry、控制面、数据面和实验结果。

面试时从 slow_score 展开，可以讲相对异常值、MAD、absolute SLO、错误率、in-flight、网络信号、状态机滞后、误判和漏判。这个问题最容易讲出系统设计能力，而不只是讲一个负载均衡算法。

## 拓展

### Q021【拓展】从 AegisMesh 延伸，Service Mesh、RPC SDK 和 API Gateway 的边界分别是什么？

RPC SDK 工作在客户端进程内，直接接入语言运行时和 RPC 框架。优点是能拿到方法名、attempt、status、local in-flight 等细粒度信息，缺点是跨语言要分别实现 SDK。

Service Mesh 通常通过 sidecar 或 node proxy 接管东西向流量，语言无关，适合统一治理、mTLS、流量策略和观测。缺点是部署和控制面复杂度更高，对应用层语义的获取不如 SDK 直接。

API Gateway 主要处理南北向流量，比如外部用户进入系统的认证、限流、路由、灰度和协议转换。它不适合替代内部每一跳 RPC 的 endpoint 级慢故障治理。

AegisMesh 当前更接近 RPC SDK + Controller，而不是 API Gateway 或完整 sidecar mesh。

### Q022【拓展】如果把 AegisMesh 改造成 sidecar 模式，需要重构哪些模块？

首先要把 SDK 里的 resolver、balancer、retry、circuit breaker 从进程内移到 sidecar 代理里。业务进程不再直接执行 adaptive P2C，而是把请求交给 localhost sidecar。

其次要定义 sidecar 和 Controller 的配置协议，类似 xDS：Controller 下发 service discovery、route policy、health state 和 retry/circuit breaker 策略，sidecar 订阅并执行。

第三要补协议处理。现在 SDK 天然知道 gRPC method 和 status；sidecar 需要解析 HTTP/2 gRPC metadata，或者通过 filter 获取这些信息。

最后是部署形态：需要 Kubernetes 注入、证书、sidecar lifecycle、热更新和多语言支持。Controller 的 slow_score 和状态机可以保留，但数据面执行位置会变化。

### Q023【拓展】如果公司已经有 Istio/Envoy/xDS，你的项目还能提供什么差异化能力？

如果公司已有 Istio/Envoy，AegisMesh 不应该重复做通用流量代理。差异化可以放在 fail-slow 检测和策略验证上。

第一，AegisMesh 的 slow_score 可以补强 outlier detection，尤其是 relative + absolute SLO、retry amplification 和 endpoint 状态恢复。

第二，真实 trace verifier 可以用来验证策略是否真的生效，比如 canary 分流、retry attempt 和 forbidden edge。

第三，eBPF TCP telemetry 可以作为网络异常信号补充应用层指标，帮助区分应用慢和网络慢。

落地方式可以是把 AegisMesh Controller 做成一个策略/评分服务，输出 xDS-compatible endpoint weight 或 outlier 状态，由 Envoy 执行流量调度。

### Q024【拓展】RPC 治理系统和数据库连接池的故障隔离机制有哪些相似点？

两者都要避免把请求继续打到表现差的资源上。RPC endpoint 像连接池里的连接或后端节点：如果延迟高、错误多、in-flight 堆积，就要降权、熔断或摘除。

两者也都要控制重试和排队。数据库连接池会限制最大连接数、等待队列和获取连接超时；RPC 治理会限制 in-flight、per-try timeout、retry budget 和 circuit breaker。

恢复也类似。连接池不会立刻把坏连接放回主路径，而是先探测；AegisMesh 里的 PROBING endpoint 也是小流量恢复。

### Q025【拓展】慢故障治理和自适应流控之间有什么关系？

慢故障治理关注“哪个 endpoint 不该继续承接正常流量”，自适应流控关注“当前系统还能承接多少流量”。两者的信号有重叠，比如延迟、错误率、in-flight 和队列长度。

AegisMesh 当前更多是 endpoint 级治理：发现某个 endpoint 慢，就降低它的权重或摘除它。自适应流控可以进一步做 service 级或 client 级限流：如果所有 endpoint 都慢，不只是换节点，而是降低进入系统的请求速率。

所以 slow_score 可以作为流控输入，但不能完全替代流控。真正的平台级系统应该同时有 endpoint routing 和 admission control。

### Q026【拓展】如果要做成平台级产品，租户隔离、权限、审计应该怎么设计？

租户隔离上，Registry、Policy、Telemetry 和 Trace 都要带 tenant 或 namespace。不同租户的服务实例、策略和指标不能混在一起，Controller 的查询也要按租户过滤。

权限上，服务注册、策略发布、故障注入和 dashboard 查询要分角色。比如普通服务只能注册自己，平台管理员才能修改全局策略，故障注入只能在测试环境或白名单服务上启用。

审计上，所有策略变更、endpoint ejection、人工 override、fault injection 操作都要记录操作者、时间、diff 和影响范围。出了事故以后要能回答：谁改了策略，哪些 endpoint 被摘除，什么时候恢复。

### Q027【拓展】如果服务调用链跨语言，Go SDK 的治理逻辑如何迁移到 Java、Python 或 Rust？

可以把治理逻辑拆成两层。

第一层是语言无关的控制面协议：RegistryService、TelemetryService、PolicyService、trace schema、retry budget 参数、endpoint state 和 slow_score 语义都保持一致。

第二层是语言相关的数据面 SDK：每种语言实现自己的 resolver/balancer/interceptor。Java gRPC 可以实现 NameResolver、LoadBalancer、ClientInterceptor；Python gRPC 的扩展点较弱，可能需要 wrapper 或 sidecar；Rust tonic 可以在 tower middleware 和 channel 层接入。

如果多语言成本太高，就可以转向 sidecar 模式，把治理逻辑从 SDK 下沉到代理里，业务语言只需要改目标地址。

### Q028【拓展】如果面试官让你把项目扩展到 Kubernetes 环境，你会从哪些对象和控制器入手？

我会先从 Service、EndpointSlice、Pod、Deployment 和 Namespace 入手。Registry 可以从 Kubernetes API watch EndpointSlice，而不是让服务手动注册地址。Pod labels 可以映射 service、revision、zone 和 tenant。

然后做一个 AegisMesh Controller。它 watch Pod/EndpointSlice/Service，维护 endpoint 状态，把 slow_score、route weight 和 ejection 状态写入自己的 CRD 或直接下发给 SDK/sidecar。

策略层可以设计 CRD，比如 `AegisPolicy`，包含 timeout、retry budget、outlier detection、probe ratio 和 SLO。实验里的 YAML policy 可以迁移成这个 CRD。

观测上接 Prometheus Operator，dashboard 走 Grafana；故障注入可以先用测试 namespace 的 Job 或 Chaos Mesh。真正上线前还要处理 RBAC、多租户隔离、灰度发布和控制面 HA。
