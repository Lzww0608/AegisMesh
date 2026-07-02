# 23. 与业界系统和相关原理对比

## 简单

### Q617【简单】AegisMesh 和 Envoy outlier detection 的目标有什么相似？

两者都想解决同一类问题：后端实例还活着，但表现已经明显变差，继续给它正常流量会拉高调用方的尾延迟或错误率。

Envoy outlier detection 会根据连续 5xx、成功率、失败比例等规则，把异常 upstream host 临时 eject 出负载均衡池。AegisMesh 也是类似思路：Controller 根据 telemetry 计算 slow_score，状态机把 endpoint 从 `HEALTHY` 推到 `DEGRADED`、`EJECTED`、`PROBING`，SDK balancer 再减少或停止给它流量。

相似点是“先观测，再判断异常，再改变路由”。差异在实现形态：Envoy 在 sidecar/proxy 数据面里做，AegisMesh 当前在 Go/gRPC SDK 和 Controller 里做。AegisMesh 还把 fail-slow 作为主线，用 EWMA、p95、relative outlier、absolute SLO、inflight、网络信号一起算慢分。

### Q618【简单】AegisMesh 和 Hystrix/Sentinel 的核心差别是什么？

Hystrix 和 Sentinel 更像调用保护框架。它们重点解决超时、熔断、隔离、限流、降级这些问题，保护调用方不被慢依赖拖垮。Hystrix 典型能力是线程池隔离和 circuit breaker；Sentinel 更偏流量控制、熔断降级和规则管理。

AegisMesh 更偏 RPC 治理闭环。它除了本地保护调用，还做服务发现、endpoint telemetry、slow_score、状态机、adaptive P2C 路由、retry budget、PolicyService 和 verifier。它关心的不只是“这个调用要不要快速失败”，还包括“哪个 endpoint 慢、流量应该转到哪里、恢复时给多少探测流量”。

一句话：Hystrix/Sentinel 主要保护调用边界，AegisMesh 试图把观测、评分、路由和恢复串成一个控制闭环。

### Q619【简单】AegisMesh 和 Nacos/Consul/Eureka 的关系是什么？

Nacos、Consul、Eureka 主要是服务注册发现和配置管理系统。它们回答的是：服务实例在哪里，实例是否还在续约，客户端如何发现它们。

AegisMesh 里也有 Registry，但它不是项目的全部。AegisMesh 的 Registry 目前支持 memory 和 file-backed backend，适合 demo 和单机恢复。真正的重点在 Registry 之后：SDK 拿到实例后，还会结合 slow_score、endpoint state、retry budget 和 adaptive routing 做请求治理。

如果放到真实系统里，AegisMesh 可以接 Nacos/Consul/Eureka 作为 registry backend。它不一定要自己当完整注册中心。更合理的定位是：已有注册发现系统负责“有哪些实例”，AegisMesh 负责“这些实例现在应该怎么用”。

### Q620【简单】AegisMesh 和 API Gateway 的职责区别是什么？

API Gateway 主要处理入口流量，也就是 north-south traffic。它负责外部请求进入系统时的路由、认证、限流、协议转换、灰度、WAF、聚合等。

AegisMesh 主要处理服务之间的内部 RPC，也就是 east-west traffic。它关心的是 frontend 调 user-service、user-service 调 social-graph-service 这类内部链路。慢实例检测、endpoint 级负载均衡、retry budget、per-method policy 都发生在服务间调用里。

两者可以共存。Gateway 保护入口，AegisMesh 治理内部依赖。入口请求变慢时，Gateway 只能看到整体慢，AegisMesh 才能看到具体哪个 downstream endpoint 慢。

### Q621【简单】AegisMesh 和 Kubernetes readiness/liveness probe 有什么互补关系？

Kubernetes liveness probe 用来判断容器是不是需要重启。readiness probe 用来判断 Pod 是否应该接 Service 流量。它们是 Pod 生命周期管理的一部分。

AegisMesh 处理的是更细的运行时表现。一个 Pod readiness 可能一直是 ready，TCP 连接也能建，但业务 RPC p99 已经很高。readiness 很难表达“这个实例还能接少量流量，但不适合接正常流量”。AegisMesh 的 `DEGRADED`、`EJECTED`、`PROBING` 就是补这个空缺。

两者互补：Kubernetes probe 负责粗粒度可用性，AegisMesh 负责 RPC 视角的慢故障和路由权重。严重故障可以让 Pod unready；短时慢故障可以先由 AegisMesh 降权或探测，不一定立刻让 Kubernetes 重启 Pod。

### Q622【简单】AegisMesh 和普通 client-side load balancing 的区别是什么？

普通 client-side load balancing 常见做法是 round-robin、随机、P2C 或 weighted round-robin。它通常只根据地址列表和简单权重选后端，状态比较少。

AegisMesh 也是客户端侧路由，但它加了治理信息。resolver 从 Controller 获取实例时，会把 endpoint status 和 slow_score 放到 gRPC address attributes 里。adaptive P2C 在 Pick 时同时看本地 inflight、EWMA latency、slow_score、状态惩罚和 PROBING ratio。

所以它不是“换了个负载均衡算法”这么简单。它把 Controller 的全局观测和 SDK 的本地观测合在一起，用来处理 fail-slow、恢复探测和重试放大。

### Q623【简单】AegisMesh 和 OpenTelemetry 的关系是什么？

OpenTelemetry 是观测标准，主要提供 traces、metrics、logs 的数据模型、SDK 和 Collector。它解决的是怎么采集、传输和关联观测数据。

AegisMesh 当前自己定义了 SDK telemetry 和 JSONL trace，目的是服务治理。Controller 收到 telemetry 后会改变 slow_score 和 endpoint state，SDK 再根据这些状态改变路由。也就是说，AegisMesh 的 telemetry 不只是给人看的监控数据，它会进入控制闭环。

两者可以结合。AegisMesh 可以把自己的 trace metadata 迁到 OpenTelemetry span attributes，也可以把 SDK metrics 导出到 OTel Collector。这样 verifier 和 dashboard 可以使用标准 trace，而 AegisMesh 仍然保留治理决策逻辑。

### Q624【简单】AegisMesh 和 Cilium/Hubble 的 eBPF 观测有什么交集？

Cilium/Hubble 用 eBPF 做网络可观测性和服务通信可视化，能看到 flow、DNS、TCP、HTTP/gRPC 等不同层面的信息，尤其适合 Kubernetes 网络环境。

AegisMesh 的 eBPF 部分更窄。它当前采集 TCP retransmit、connect error、connect latency 这类网络信号，然后把这些信号融合进 Controller telemetry，用来辅助 slow_score。它不是完整网络平台，也不负责 CNI、NetworkPolicy 或集群流量可视化。

交集是都用 eBPF 看内核网络信号。差异是目标不同：Cilium/Hubble 更偏网络层平台和观测，AegisMesh 把网络信号拿来帮助 RPC 路由和慢故障判断。

### Q625【简单】为什么说 AegisMesh 是“治理系统”而不是“监控系统”？

监控系统主要是采集、存储、看板和告警。Prometheus、Grafana、OpenTelemetry 这类系统通常不会直接替业务请求做路由决策。

AegisMesh 会改变请求路径。SDK 上报 telemetry，Controller 计算 slow_score 和 endpoint state，resolver 把状态带回客户端，balancer 根据状态改变流量分配，retry budget 决定是否允许重试。这个链路会影响真实 RPC。

所以它不是只回答“系统现在怎么样”，而是进一步回答“接下来请求应该怎么走”。这就是治理系统和监控系统的区别。

### Q626【简单】如果已有 Prometheus，为什么还需要 SDK telemetry 上报到 Controller？

Prometheus 是 pull 模型，适合长期监控和 dashboard。Controller 要做实时路由决策时，直接从 Prometheus 查询不是很合适。

原因有几个。第一，Prometheus scrape 有间隔，数据延迟不稳定。第二，PromQL 查询和控制面决策耦合后，Controller 会依赖外部监控系统的可用性。第三，SDK 上报可以按治理需要组织窗口，比如 destination、method、upstream、retry、inflight。第四，Controller 收到上报后可以立刻更新状态机，并把结果返回给 SDK。

Prometheus 仍然有价值。它负责可视化、历史查询和告警。SDK telemetry 则负责控制面闭环。两者不是重复，而是用途不同。

## 深度

### Q627【深度】Envoy outlier detection 常见的 consecutive 5xx、success rate、failure percentage 与 slow_score 如何比较？

consecutive 5xx 看的是连续错误。它对 fail-stop 或明显错误很敏感，比如某个 upstream 连续返回 5xx，就可以快速 eject。缺点是对纯慢故障不敏感。服务如果一直返回 200，但每次都很慢，consecutive 5xx 看不到。

success rate 是横向比较。Envoy 会比较一组 host 的成功率，低于统计阈值的 host 可能被 eject。它比连续错误更稳，但仍然偏错误率。

failure percentage 是按失败比例判断。它适合错误率明显升高的实例，也需要足够请求量，否则低流量下容易误判。

AegisMesh 的 slow_score 更偏 fail-slow。它把 latency p95/EWMA、error、timeout、inflight、TCP retransmit/connect signal 合起来。latency 既有 relative median/MAD，也有 absolute SLO。这样即使没有 5xx，只要 p95 明显偏高，也能被识别。

代价是 slow_score 更复杂，参数也更多。Envoy outlier detection 的规则成熟、稳定、可运维；AegisMesh 的优势是把慢故障作为一等信号，适合这个项目要讲的主题。

### Q628【深度】Istio 的 control plane/data plane 模型和 AegisMesh 的 SDK 模型相比，运维成本如何？

Istio 的数据面通常是 Envoy sidecar，控制面负责下发 xDS 配置。好处是语言无关，业务代码少改；Java、Go、Python 服务都能统一治理。功能也很完整，包括 mTLS、流量管理、授权、观测、熔断、重试等。

成本也明显。每个 Pod 多一个 sidecar，会增加资源开销和运维复杂度。调试时要理解业务容器、Envoy、iptables、xDS、证书、控制面状态。升级 Istio 也要考虑数据面滚动、兼容性和平台团队能力。

AegisMesh SDK 模型更轻。Go/gRPC 服务接入 SDK 后，可以在进程内做 resolver、balancer、telemetry、retry。没有 sidecar 资源开销，调试路径也更直接。缺点是侵入业务代码，跨语言支持差，安全能力和流量拦截能力不如 sidecar。

所以权衡是：Istio 运维复杂但平台能力强，SDK 轻量但需要语言接入。AegisMesh 更像一个面向 Go/gRPC 场景的治理实验，不是 Istio 的替代品。

### Q629【深度】Hystrix 的线程池隔离和 AegisMesh 的 per-endpoint breaker 分别解决什么问题？

Hystrix 的线程池隔离解决的是依赖隔离。每个下游依赖可以有独立线程池。某个依赖慢了，只会耗尽自己的线程池，不会把整个应用的业务线程拖死。它保护的是调用方进程的资源。

AegisMesh 当前的 per-endpoint breaker 是 inflight 限制。它按 endpoint 统计正在进行的请求，超过上限就返回 `ResourceExhausted`。它保护的是某个 endpoint 不被过多并发压垮，也保护客户端不要无限堆积慢请求。

两者粒度不同。线程池隔离通常按 dependency 或 command 维度隔离资源；AegisMesh breaker 按 endpoint 维度限制并发。Hystrix 更偏本地 bulkhead，AegisMesh breaker 和 routing、slow_score、retry budget 一起工作。

如果生产化，AegisMesh 可以继续加 service-level bulkhead、method-level concurrency limit，把 Hystrix 的资源隔离思想融合进 SDK。

### Q630【深度】Sentinel 的流控规则和 AegisMesh 的 adaptive routing 是否可以组合？

可以，组合后边界会更清楚。

Sentinel 的强项是流控和熔断规则，比如按 QPS、并发数、调用关系、热点参数做限流。它可以在请求进入某个服务或某个方法前做 admission control。

AegisMesh 的 adaptive routing 主要决定请求发往哪个 endpoint。它根据 endpoint slow_score、inflight、EWMA 和状态做选择。它不是入口限流系统，也不擅长按用户、租户、热点参数限流。

组合方式可以是：Sentinel 或类似规则先判断“这个请求能不能进入系统/调用这个资源”，AegisMesh 再判断“如果允许调用，应该选哪个 endpoint”。当下游过载时，AegisMesh 减少慢 endpoint 流量，Sentinel 控制总请求量，避免把剩余健康实例压垮。

需要注意策略冲突。比如 Sentinel 已经在限流，AegisMesh 的 retry 不能再把被限流请求放大。两者要共享错误码和 retry 语义。

### Q631【深度】Kubernetes readiness 只表达 pod 是否接流量，为什么不足以表达慢故障？

readiness 是一个二值信号：ready 或 not ready。它适合表达“这个 Pod 是否应该被 Service endpoint 选中”。但慢故障不是二值问题。

一个实例可能只是比同组实例慢 3 倍，但仍然能处理少量请求。直接把它设成 not ready 会造成流量突变，可能把其他实例打满。另一个问题是 readiness probe 通常是固定 HTTP 或命令检查，它不一定覆盖真实业务方法。健康检查很快，不代表业务 RPC 快。

AegisMesh 的状态更细：`DEGRADED` 可以降权，`EJECTED` 可以临时摘除，`PROBING` 只给少量探测流量。它的判断来自真实 RPC telemetry，而不是单独的探针。

所以 readiness 是粗粒度入口，AegisMesh 是细粒度运行时调节。两者配合会更好。

### Q632【深度】如果用 Envoy 实现同样功能，需要扩展哪些 filter 或 xDS 配置？

基础能力可以直接用 Envoy 现有配置。EDS 提供 endpoint，outlier detection 做异常摘除，load balancing policy 可以用 least request，retry policy 可以配在 route 上，circuit breaker 可以限制 upstream 并发和 pending request。

要实现 AegisMesh 的 slow_score，需要扩展更多东西。Envoy 原生 outlier detection 主要围绕错误率、成功率和连续失败。要把 p95、EWMA、relative median/MAD、absolute SLO、TCP retransmit/connect signal 融进去，可能需要自定义 filter、外部处理服务，或者控制面根据外部 telemetry 动态调整 endpoint weight。

如果要做 PolicyService 类似的动态策略，可以走 xDS。Controller 生成 CDS/EDS/RDS 配置，下发到 Envoy。PROBING ratio 可以通过 endpoint weight 或 locality weight 控制，但要精细控制探测流量，可能还要自定义 LB policy。

所以用 Envoy 能复用很多成熟能力，但要完全复刻 slow_score + adaptive P2C + eBPF signal，仍然需要控制面和扩展点。

### Q633【深度】如果用 service mesh sidecar，实现 retry budget 的粒度和 SDK 实现会有什么差异？

SDK 实现的 retry budget 通常贴近业务 client。它可以按 ClientConn、method、调用方进程来统计，也更容易读取业务方法名和幂等性配置。缺点是多语言要重复实现，全局预算不容易统一。

sidecar 实现的 retry budget 语言无关。所有流量经过 sidecar，平台可以统一配置。它更适合多语言团队，也更容易和 mesh 控制面结合。

粒度差异在几个地方。

第一，sidecar 看到的是网络请求，不一定知道业务语义。gRPC 方法名可以看到，但业务层幂等性、idempotency key、租户分类未必自然可见。

第二，sidecar 可以按 workload 或 proxy 统计预算，但多个进程共享一个 sidecar 时，预算会被合并。SDK 则天然按进程或 ClientConn 分开。

第三，sidecar 做全链路预算可能更容易统一注入 headers，但 SDK 更容易在业务调用上下文里精确控制。

所以没有绝对优劣。AegisMesh 的 SDK budget 适合 Go/gRPC 实验和业务语义接入；sidecar budget 更适合多语言统一治理。

### Q634【深度】为什么部分公司选择 SDK governance，而不是全量 sidecar mesh？

常见原因是成本和控制力。

SDK governance 接入成本低一些。没有 sidecar 资源开销，不需要大规模改网络路径，也不需要处理复杂的 iptables、Envoy 配置和证书体系。对某些 Go/Java 技术栈统一的公司，SDK 可以快速覆盖主要服务。

SDK 也更贴近业务语义。它能直接知道 gRPC method、调用上下文、幂等性、业务错误码，做 retry 和 timeout 时更细。

缺点也明显。SDK 侵入业务代码，多语言维护成本高，升级要靠业务团队配合。安全、统一策略、全流量拦截也不如 sidecar。

所以很多公司会过渡性选择 SDK：先解决核心 RPC 治理问题，等组织和平台成熟后再考虑 sidecar 或 ambient mesh。AegisMesh 就适合讲这种路线：先把慢故障治理机制做清楚。

### Q635【深度】AegisMesh 的 eBPF telemetry 和应用层 telemetry 谁更可信？

它们可信的维度不同。

应用层 telemetry 更懂业务。它知道 destination、method、status、attempt、timeout、retry。它能直接回答“这个 RPC 对用户来说慢不慢”。但它依赖 SDK 正确上报，也容易受到客户端偏差影响。

eBPF telemetry 更接近内核网络事实。TCP retransmit、connect latency、connect error 不是业务代码随便伪造出来的。它适合判断网络层异常。缺点是它不懂业务语义。它看到连接和包，不天然知道这个连接对应哪个 gRPC method，也容易受 NAT、sidecar、Docker bridge、连接复用影响。

所以不要问谁绝对可信。正确用法是互相校验：应用层 latency 高但 eBPF 网络信号正常，更像应用或下游处理慢；两者同时异常，更像网络问题；eBPF 异常但应用层没变，可能是低流量或连接级噪声。

AegisMesh 当前把 eBPF 当增强信号，而不是唯一判断依据，这个定位是合理的。

### Q636【深度】如果业界已有成熟方案，个人项目的价值应该如何表达？

不要说“我做了一个比 Istio/Envoy 更好的 mesh”。这不可信，也没必要。

更好的表达是：我选了一个明确问题，即 fail-slow 微服务治理；围绕这个问题实现了一个小而完整的控制闭环；我能解释每个组件为什么存在，也能用实验数据说明机制有效。

价值可以放在四点上。

第一，问题建模。慢故障不是宕机，普通 health check 和固定阈值不够。

第二，算法实现。slow_score、hysteresis 状态机、adaptive P2C、retry budget、PROBING ratio、absolute SLO 是自己串起来的。

第三，工程闭环。Controller、SDK、policy、metrics、trace verifier、fault injector、eBPF、Docker benchmark 都能跑。

第四，结果和边界。能给出 p99、amplification、recovery 曲线，也能承认单机实验、非 HA、DeathStarBench runner 仍不是 measured benchmark。

成熟系统说明你知道 industry context；个人项目说明你能把一个具体技术问题拆开、实现、验证、复盘。

## 拓展

### Q637【拓展】服务治理的发展路径从库、SDK、sidecar 到 ambient mesh，核心权衡是什么？

库和 SDK 最早。优点是轻量、性能好、能接触业务语义。缺点是侵入代码，多语言维护困难，升级慢。

sidecar mesh 把治理能力从业务进程里拿出来，放到 Envoy 这类代理里。优点是语言无关，平台统一，安全和流量管理能力强。缺点是每个 Pod 多一个代理，资源和运维成本上升，排障路径变长。

ambient mesh 想减少 sidecar 的 per-pod 成本，把一部分能力下沉到节点级代理或共享数据面。它降低了 sidecar 注入和资源开销，但模型更复杂，能力边界也要重新理解。

本质权衡是：业务语义 vs 平台统一，性能开销 vs 运维便利，语言侵入 vs 透明治理。AegisMesh 选择 SDK，是因为项目聚焦 Go/gRPC fail-slow 治理，想先把算法和闭环做清楚。

### Q638【拓展】xDS、RDS、CDS、EDS、LDS 分别负责什么？AegisMesh 哪些模块可类比？

xDS 是 Envoy 使用的一组动态配置协议。它不是一个单一配置，而是一组 discovery service。

LDS 是 Listener Discovery Service，负责监听端口和 listener 配置。

RDS 是 Route Discovery Service，负责路由规则，比如某个 host/path/method 应该去哪个 cluster。

CDS 是 Cluster Discovery Service，负责 cluster 定义，比如上游集群、负载均衡策略、熔断、超时。

EDS 是 Endpoint Discovery Service，负责 cluster 里的 endpoint 列表，也就是具体实例地址和权重。

AegisMesh 可以类比，但不是 xDS 实现。RegistryService 的 `ListInstances` 类似简化版 EDS。PolicyService 里 routing、retry、circuit breaker 类似简化版 RDS/CDS 配置。Controller 类似控制面，SDK resolver/balancer 类似轻量数据面。AegisMesh 目前没有 LDS，因为它不管理代理监听端口。

### Q639【拓展】Envoy adaptive concurrency filter 的思想和项目的 inflight/slow_score 有何关系？

Envoy adaptive concurrency filter 的思路是根据延迟反馈动态调整并发上限，避免 upstream 被过多请求压垮。它关注的是“当前允许多少并发进入某个上游”。

AegisMesh 里有两个相关信号。inflight 表示当前 endpoint 上正在进行的请求数，adaptive P2C 会把 inflight 放进 cost。slow_score 里也有 inflight 组件，用来反映压力。Circuit breaker 还会按 endpoint 限制最大 inflight。

关系是：两者都把延迟和并发当成反馈信号，用来保护系统。差异是 Envoy adaptive concurrency 更像动态并发控制器，AegisMesh 当前更多是 routing cost + breaker 的组合。它会减少慢 endpoint 的流量，但还没有完整的自适应并发上限算法。

后续可以把 adaptive concurrency 引入 AegisMesh：为每个 endpoint 维护动态 max inflight，根据 latency SLO 和错误反馈调整，而不是固定 128。

### Q640【拓展】Google SRE 中 overload、brownout、load shedding 的原则如何应用到项目？

overload 是系统负载超过处理能力。AegisMesh 可以通过 inflight、latency、error rate 和 retry amplification 识别 overload。

brownout 是在压力大时降级一部分非必要功能，让核心请求继续可用。AegisMesh 本身不做业务降级，但可以通过 policy 支持不同 method 的优先级。比如低优先级查询少重试或快速失败，高优先级请求保留预算。

load shedding 是主动丢弃一部分请求，防止系统整体崩掉。AegisMesh 的 circuit breaker、retry budget、future rate limit 都属于这个方向。与其让慢请求排队到超时，不如在本地快速失败，把压力挡住。

应用到项目里，就是三条原则：不要无限排队，不要无限重试，不要把坏 endpoint 当正常 endpoint 用。慢故障治理不是让所有请求都成功，而是让系统在压力下以可控方式退化。

### Q641【拓展】分布式系统中的 feedback control 如何解释 AegisMesh 的闭环？

AegisMesh 可以看成一个反馈控制系统。

被控对象是微服务 endpoint。观测量是 latency、error、timeout、inflight、retransmit。控制器是 Controller 里的 slow_score calculator 和 state machine。执行器是 SDK resolver/balancer/retry interceptor。控制目标是降低尾延迟、减少错误放大、避免慢实例继续吃正常流量。

反馈链路是：SDK 观察 endpoint 表现，上报 Controller；Controller 更新 slow_score 和 state；SDK 拉取或接收状态；balancer 改变流量分配；新的流量分配又改变 endpoint 表现。

控制系统里最怕振荡。AegisMesh 用连续窗口、degraded/eject/recovery 不同阈值、ejection duration、PROBING ratio 来抑制抖动。代价是反应不会无限快，需要在收敛速度和稳定性之间取平衡。

这个视角很适合面试讲深度，因为它能解释为什么不能只靠一次延迟尖峰就摘除实例。

### Q642【拓展】如何比较“局部最优路由”和“全局最优调度”？

局部最优路由是每个客户端根据自己看到的状态做选择。AegisMesh 的 adaptive P2C 就是这种风格：SDK 根据本地 EWMA、inflight 和 Controller 下发的 state，选择当前看起来成本最低的 endpoint。

全局最优调度需要控制面知道所有客户端、所有 endpoint、所有请求的状态，然后统一分配流量。理论上更优，但现实里很难。信息延迟大、状态量大、决策频率高，控制面也容易变成瓶颈。

局部策略的优点是快、简单、可扩展。缺点是多个客户端可能同时避开同一个慢实例，又同时涌向同一个健康实例，造成新的热点。

工程上通常做折中：数据面做局部快速决策，控制面提供慢一点的全局信号和约束。比如最大摘除比例、endpoint 权重、全局 overload 状态。AegisMesh 当前就是这种方向，但全局约束还可以继续增强。

### Q643【拓展】尾延迟治理中 replicated requests、hedging、adaptive routing 的取舍是什么？

replicated requests 是同时发多个副本，请谁先返回用谁。它能明显降低尾延迟，但成本很高，因为每个请求都会放大下游负载。

hedging 是先发一个请求，如果超过某个延迟阈值还没返回，再发第二个副本。它比完全复制温和，但仍然会增加请求量。阈值设得太低，就会制造额外负载；设得太高，对尾延迟帮助有限。

adaptive routing 是尽量在发请求前选一个更可能快的 endpoint。它不天然增加请求数，成本低很多。缺点是它依赖观测和历史状态，遇到突发长尾时不如 hedging 直接。

AegisMesh 当前选择 adaptive routing + retry budget，而不是 hedging。这个选择比较保守：先避免把流量发到慢实例，再限制失败后的额外尝试。后续如果加 hedging，也必须配严格 budget 和幂等性策略，否则很容易把慢故障变成流量风暴。

### Q644【拓展】如果把项目写成论文，related work 会包括哪些系统？

我会按方向分组写。

第一组是 service mesh 和 proxy：Envoy、Istio、Linkerd。它们提供流量治理、outlier detection、retry、mTLS、observability，是 AegisMesh 最直接的系统参照。

第二组是 RPC 框架和客户端治理：gRPC load balancing、Finagle、Dubbo、Spring Cloud、Hystrix、Sentinel。它们和 AegisMesh 的 SDK 形态、熔断、限流、重试关系更近。

第三组是服务发现和配置：Consul、Eureka、Nacos、Kubernetes Service/EndpointSlice。AegisMesh 的 Registry 和 PolicyService 可以和这些系统对照。

第四组是尾延迟和重试控制：The Tail at Scale、hedged requests、retry budget、adaptive concurrency、load shedding。这里是 slow-fault 治理的理论背景。

第五组是 observability 和 eBPF：OpenTelemetry、Prometheus、Cilium/Hubble。AegisMesh 的 telemetry、trace verifier、eBPF TCP signals 可以放在这组里。

第六组是 benchmark 和验证：DeathStarBench、Sock Shop、MeshTest 类 trace-based verification。它们对应项目里的实验和 verifier。

写 related work 时不要把这些系统列成名单就结束，要说清差异：AegisMesh 是一个教学/实验型系统，重点是 fail-slow scoring、SDK adaptive routing 和可复现实验闭环。
