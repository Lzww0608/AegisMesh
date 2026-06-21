# Enterprise Traffic Architecture Components Followups

本文对应《AegisMesh 技术面试问题库》里的“企业流量架构组件追问（115 题）”这一大类。按同类问题放在同一个 Markdown 文件的要求，本文件从题号 1 开始连续编号；后续继续补这一组时，直接沿着当前编号往下接。

写法按面试口述组织：先给可以直接回答的口径，再展开组件在企业流量链路中的位置、工程边界、上下游协作、健康治理和观测方法。参考依据主要来自 AWS Route 53、Amazon CloudFront、AWS Elastic Load Balancing、Cloudflare Load Balancing、Cloudflare HTTP headers、Envoy 官方文档、F5 BIG-IP TMSH、gRPC、Amazon RDS Proxy、ProxySQL、PgBouncer、Apache Kafka、RabbitMQ、Kubernetes、Argo Rollouts、Flagger、OpenTelemetry 文档和相关 IETF/W3C 规范，链接放在文末。

## 1. DNS 轮询位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：DNS 轮询位于企业流量链路最前面的域名解析层，通常在用户真正建立 TCP、TLS、HTTP 或 gRPC 连接之前发生。它主要解决入口流量的初步落点选择，也就是“这个域名先解析到哪些地址”。它不是严格意义上的东西向服务治理手段，也不是应用路由组件，因为它看不到 HTTP path、Header、Cookie、请求体、租户、方法名，更不会参与一次请求进入后端之后的逐跳转发。

DNS 轮询的典型形式是给同一个域名配置多个 A 或 AAAA 记录，权威 DNS 或递归解析器在响应时返回多个地址，客户端或递归解析器再根据返回顺序、缓存状态和本地实现选择一个地址。这里有两个容易被忽略的点。第一，很多时候权威 DNS 看到的是递归解析器的地址，不是最终用户地址；如果递归解析器支持 EDNS Client Subnet，权威 DNS 可能拿到一段客户端网段信息，但这只是用于更贴近用户地理位置或网络位置的 DNS 决策，不等于拿到了完整的真实客户端连接上下文。第二，DNS 返回的是“地址候选集”，不是“每个请求的调度动作”。一旦解析结果被递归解析器、操作系统、应用 DNS cache 或连接池缓存下来，后续大量请求可能都复用同一个结果甚至同一条长连接。

所以面试里要把 DNS 轮询和真正的负载均衡区分开。DNS 轮询适合做粗粒度入口分散，例如一个域名后面挂多个公网 SLB、多个边缘入口、多个机房入口。它不适合单独承担精细的容量感知调度、请求级限流、会话保持、L7 灰度、故障隔离和长连接治理。AWS Route 53 的 multivalue answer routing 文档也把它说得很明确：多值应答可以返回多个可健康检查的 IP，用于改善可用性和负载分散，但它不是负载均衡器的替代品。

如果面试官继续追问“那内部服务发现能不能用 DNS 轮询”，答案是可以，但边界还是一样。Kubernetes Service、Consul DNS、CoreDNS 这类内部 DNS 可以用于服务名到实例或虚拟 IP 的解析，解决一部分东西向服务发现问题；真正的东西向负载均衡通常还要靠 kube-proxy、Sidecar、客户端负载均衡器、Service Mesh 或 RPC 框架 picker。DNS 在这里更像服务发现入口，不是完整的请求调度系统。

容易继续深挖的点是：TTL 会让故障摘除变慢，递归解析器可能不按预期遵守 TTL，客户端连接池会放大 DNS 决策的粘性，HTTP/2 和 gRPC 长连接会让一次 DNS 选择承载很久的流量。

## 2. DNS 轮询在高可用设计中如何避免单点故障？

可以先这样答：DNS 轮询要避免单点故障，不能只盯着后端 IP 列表，还要把“域名注册商、权威 DNS、NS 委派、DNS 配置发布、健康检查、递归缓存、客户端连接重试”都纳入高可用设计。一个常见误区是认为域名下配置多个 A 记录就天然高可用。实际上，如果权威 DNS 服务、某个 NS 记录、配置控制面或所有返回的地址背后共用同一个入口故障域，DNS 轮询仍然会变成单点。

第一层是权威 DNS 自身的冗余。生产环境至少要有多个 NS 记录，权威 DNS 节点要跨可用区、跨地域，最好使用 Anycast 或成熟托管 DNS 服务。对于极高可用的入口域名，还可以考虑主备 DNS 提供商或 secondary DNS，避免单一 DNS 厂商、单一控制台、单一 API 配置链路故障。注册商和域名续费也属于可用性边界，域名过期、NS 委派被误改、DNSSEC 配置错误，都会让后端再健康也没有意义。

第二层是记录集和后端入口的冗余。多个 A 记录必须落在独立的负载均衡器、边缘入口或机房入口上，不能只是同一台设备上的多个地址。AWS Route 53 支持 weighted routing，可以为同名同类型记录设置相对权重；权重调成 0 后可以停止向该资源发送常规流量。它也支持对记录关联健康检查，在多个资源提供同一功能时，可以把不健康资源的流量切到健康资源。这个能力比朴素 DNS 轮询更接近“健康感知 DNS 调度”，但仍然受 TTL 和缓存影响。

第三层是故障切换时延的治理。DNS 不是实时控制面，TTL 决定不了所有缓存行为。递归解析器可能有最小 TTL，客户端可能有自己的 DNS cache，JVM、Go resolver、浏览器、移动端网络栈也可能有不同策略。因此 DNS 层摘除故障地址以后，旧地址仍可能在一段时间内被访问。高可用设计要让旧地址背后的入口能做连接关闭、快速失败或返回可重试错误，而不是黑洞式超时。

第四层是配置发布的可靠性。DNS 变更要有版本化、灰度发布、回滚、审计和双人复核。很多入口事故不是 DNS 服务不可用，而是配置误删、权重打错、CNAME 指错、健康检查路径误判，或者自动化脚本把所有记录一起摘掉。面试时可以补一句：DNS 高可用既是数据面的可用，也是控制面的可用；前者靠多 NS、多地域、多入口，后者靠变更治理和失误兜底。

## 3. DNS 轮询与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：DNS 轮询本身不传递这些上下文。DNS 协议负责把名字解析成记录，它不会把真实客户端 IP、HTTP 协议、请求超时、trace id、span id 或业务 Header 转交给后端。真实客户端 IP、协议、超时和追踪上下文是在连接建立以后，由 CDN、L4/L7 负载均衡器、API Gateway、反向代理、Service Mesh 或应用客户端来传递。

真实客户端 IP 是最容易混淆的点。权威 DNS 通常看到递归解析器的地址，而不是最终用户 IP。EDNS Client Subnet 可以让递归解析器在 DNS 查询里附带一段客户端网络前缀，权威 DNS 可以据此返回更适合该网段的结果；但 RFC 7871 也明确把它放在隐私和缓存权衡里讨论，它不是端到端身份传递机制，更不是完整客户端 IP 透传。到了 HTTP 或代理层，常见做法才是用 `X-Forwarded-For`、`Forwarded`、`X-Real-IP`、Cloudflare 的 `CF-Connecting-IP`、AWS/ELB 的头部，或者 L4 的 PROXY protocol 把源地址交给下游。Cloudflare 文档里也明确说明，`CF-Connecting-IP` 是从 Cloudflare 边缘到源站流量中携带客户端 IP 的请求头，这已经是 CDN/代理层行为，不是 DNS 行为。

协议也类似。DNS 只能返回 A、AAAA、CNAME、ALIAS/ANAME、SRV、SVCB/HTTPS 等记录，最多影响客户端“去哪里连”和一部分连接参数发现。HTTP/1.1、HTTP/2、HTTP/3、gRPC、WebSocket 的实际协商发生在 TCP/TLS/QUIC 和应用层。DNS 轮询不能告诉后端“这是一次 gRPC 方法调用”，也不能根据 path 或 method 做应用路由。

超时和 deadline 更不能靠 DNS 传递。DNS 里有 TTL，但 TTL 是解析结果的缓存生命周期，不是请求超时时间。端到端 deadline 应该由客户端或入口网关设置，并在 RPC metadata、HTTP Header 或框架上下文里向后传播。DNS 查询超时只影响解析阶段，和业务请求的 per-try timeout、connect timeout、read timeout、overall deadline 是不同概念。

追踪上下文同样要从请求链路里传。常见做法是入口层生成或接收 `traceparent`、`tracestate`、`x-request-id`，再由网关、Sidecar、RPC 框架和应用代码跨线程、跨异步边界传播。DNS 层可以在日志里记录“某个解析器拿到了哪个答案”，但它通常无法自然加入一次业务调用的 trace。企业里如果想把 DNS 决策和后续请求关联起来，一般要靠边缘入口给请求打上 region、pop、route、pool、dns_answer_version 这类标签，再在日志和 trace 里归因。

## 4. DNS 轮询如何做健康检查、摘除、恢复和流量预热？

可以先这样答：朴素 DNS 轮询本身不做健康检查，它只按记录返回地址。要让 DNS 轮询具备健康感知能力，需要托管 DNS 的健康检查、外部探测系统或自动化控制面来动态调整记录、权重和返回集合。摘除和恢复不能只看“记录是否更新”，还要考虑 TTL、递归缓存、客户端 cache、连接池和后端连接排空。

健康检查至少要分三层看。第一层是入口连通性，例如 TCP 端口、TLS 握手、HTTP `/healthz`。第二层是服务可用性，例如依赖数据库、缓存、队列和关键下游是否可用，返回码和响应体是否符合预期。第三层是承载能力，例如实例虽然能返回 200，但 CPU 已满、队列很长、GC 抖动、连接池耗尽，这时继续给它分流会拖高 p99。Route 53 health check 可以监控指定资源、其他 health check 或 CloudWatch alarm，并把 DNS failover 连接起来；Cloudflare Load Balancing 的健康监控也会检查端点是否响应、是否足够快、状态码和响应体是否符合预期。

摘除一般有几种做法。对于加权记录，可以把目标权重降到 0；对于多值应答，可以让不健康记录不再被返回；对于简单记录，可以直接删除或替换 IP。这里的工程重点不是“能不能改 DNS”，而是“旧答案会存活多久”。如果 TTL 是 60 秒，也不能假设 60 秒后所有客户端都切走了，因为递归解析器和应用缓存不一定完全按你的预期工作。更稳的做法是先在下一层负载均衡器上 drain 连接，再降低 DNS 权重，等待 TTL 和连接排空窗口，最后再停机。

恢复要比摘除更谨慎。一个节点刚恢复时，缓存、JIT、连接池、TLS session、对象池、热点数据、本地索引、文件页缓存都可能是冷的。直接把它放回完整权重，会出现“健康检查刚通过，真实流量一上来又抖掉”的循环。更好的策略是连续多次健康检查通过后，先给很小权重，例如 1% 或一个低并发上限，再逐步提高；同时观察连接建立成功率、首字节时间、5xx、超时、队列长度和 p99。如果是 CDN 或边缘入口，还要注意 cache miss 暴涨带来的源站压力。

预热也要分清对象。DNS 层能做的是逐步调权和控制返回集合，真正的预热在下游完成：区域负载均衡器预建连接，应用加载配置和热点缓存，JVM 或 Go 服务跑 warm-up 路径，CDN 提前填充关键对象，服务网格更新 endpoint 后做慢启动。面试里可以补一句：DNS 健康治理是粗粒度、慢反馈、受缓存影响的治理，不能替代后端实例级的熔断、限流和连接排空。

## 5. DNS 轮询的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：DNS 轮询的观测不能只看“域名能不能解析”。要同时看权威 DNS 服务质量、解析结果分布、健康检查状态、变更传播、递归缓存行为，以及解析结果到真实业务流量之间的偏差。如果 DNS 层变成瓶颈，表现通常不是应用 CPU 飙高，而是解析延迟、SERVFAIL/NXDOMAIN、超时、错误委派、错误答案和流量分布异常。

权威 DNS 侧要看 QPS、响应延迟、超时率、SERVFAIL、NXDOMAIN、NOERROR 空答案、UDP/TCP 比例、EDNS 支持情况、响应截断率、DNSSEC 验证失败、各 NS 节点的可用性、各地域 Anycast 节点的响应质量。还要看每个记录集返回了哪些地址，各地址被返回的比例是否接近预期权重，健康检查状态变化是否和真实故障一致。Route 53 weighted routing 按记录权重占总权重比例分配返回结果；如果配置层权重是 1:1，真实返回却长期偏成 9:1，就要查权威 DNS、递归解析器、缓存或观测采样。

客户端和递归解析器侧要看解析耗时、DNS cache hit ratio、递归解析器分布、主要运营商/地域/企业出口的解析结果、TTL 生效情况、旧记录残留时间、ECS 使用比例。企业内网还要看 CoreDNS、NodeLocal DNSCache、systemd-resolved、nscd、JVM DNS cache 这些本地层。很多“DNS 配置已经改了但流量没切走”的问题，根因在递归缓存或应用连接池，而不是权威 DNS 没生效。

业务侧要把 DNS 结果和真实流量对账。比如某个 IP 在 DNS 返回比例里只有 10%，但应用访问日志里承载了 70% 请求，可能是长连接、连接池、客户端 cache、四层 NAT、HTTP/2 多路复用或某个大客户解析器导致倾斜。反过来，DNS 返回比例正常，但某个区域大量用户连接失败，可能是 BGP、运营商链路、ACL、TLS 证书或下游 SLB 问题。

判断 DNS 自身成为瓶颈，可以看几个信号：权威 DNS 响应 p95/p99 明显升高，解析超时或 SERVFAIL 突增，某些 NS 节点不一致，UDP 响应被截断导致 TCP fallback 激增，DNSSEC 签名或验证问题导致特定解析器失败，配置发布后长时间不同 NS 返回不同版本，监控探针从多个公网递归解析器拿到矛盾结果。排障时不要只在一台机器上 `dig`，要从不同地域、不同运营商、不同递归解析器做合成探测，并把结果和业务入口日志对齐。

## 6. GSLB 位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：GSLB，Global Server Load Balancing，通常位于企业流量链路的全局入口调度层，位置在用户发起访问和区域入口负载均衡器之间。它主要解决跨地域、跨机房、跨云、跨 CDN 或跨入口集群的入口流量分配问题，例如把北美用户导到美东或美西，把东南亚用户导到新加坡，把故障地域的流量切到灾备地域。它不是默认的东西向服务治理组件，也不是细粒度应用路由组件。

和普通 DNS 轮询相比，GSLB 多了策略和健康感知。常见策略包括地理位置、网络距离、探测延迟、区域容量、权重、成本、合规边界、主备优先级、运营商线路、故障域隔离。AWS Route 53 latency-based routing 会根据用户到 AWS Region 的延迟数据选择低延迟区域记录；Cloudflare Load Balancing 文档也把流量决策拆成 pool/endpoint health、global traffic steering 和 local traffic steering。也就是说，GSLB 不是简单地“多个 IP 轮流返回”，而是在全局层做“哪个区域或哪个池子现在更适合接这批入口流量”的决策。

GSLB 主要处理入口流量，因为它通常根据域名解析或全局代理入口做首次落点选择。东西向流量也可以有“全局服务发现”或“跨集群服务路由”，但那更接近多集群服务发现、服务网格、多集群 Gateway 或 RPC 框架治理，不要在面试里把它直接等同于互联网入口 GSLB。应用路由则更靠近 API Gateway、Ingress、Envoy、Nginx、Service Mesh 和业务代码，按 path、host、header、method、tenant、版本做决策。

一个比较准确的企业链路可以这样描述：用户先通过递归 DNS 查询域名，权威 DNS 或 GSLB 返回某个 CDN、某个区域 SLB 或某个入口地址；之后 CDN 或区域 LB 终止 TLS、做 WAF、限流、L7 路由、灰度和反向代理；再往内才是 Service Mesh、客户端负载均衡和服务实例。GSLB 决定“进哪个大门”，区域入口和应用路由决定“进门后走哪条走廊”。

## 7. GSLB 在高可用设计中如何避免单点故障？

可以先这样答：GSLB 的高可用要同时保护权威解析或全局代理数据面、健康检查系统、策略控制面、配置发布链路和兜底策略。因为 GSLB 往往站在全站入口前面，一旦它出问题，影响不是单个服务，而是整个地域、整个业务线甚至整个企业入口。

第一，GSLB 数据面要多点部署。DNS 型 GSLB 要有多个权威 NS、跨地域 Anycast、多个 PoP 或托管 DNS 服务；代理型 GSLB 要有多地域边缘入口和 BGP/Anycast 容灾。不要让所有 NS 都在同一云厂商同一地域，也不要让所有入口都依赖同一个控制节点。高要求场景可以做双 DNS 厂商、主从 zone 同步、备用域名、备用 NS 委派预案，但这会增加配置一致性和演练成本。

第二，健康检查不能有单探针偏差。GSLB 常根据探针结果决定区域是否接流量，如果探针都在同一个地域、同一个运营商或同一个云环境，可能把局部网络故障误判为服务故障，或者反过来漏掉真实用户路径上的问题。Cloudflare Load Balancing 会从所选健康监控区域内的多个数据中心发起请求，并用多数结果判断端点或区域健康；这类设计思路可以概括为“多探针、多地域、多信号、带迟滞”。

第三，控制面要能失败但不拖垮数据面。配置中心、策略引擎、发布流水线、API token、审批系统都可能故障。成熟做法是数据面继续使用最后一个已知好配置，控制面变更失败时不影响当前解析或转发；配置发布要支持版本化、灰度、快速回滚和审计。GSLB 策略本身也要有保护，例如权重总和校验、禁止一次性摘除所有主池、强制保留 fallback pool、对高危变更做人工确认。

第四，兜底策略要提前定义。所有区域都不健康时，是返回主站、返回只读灾备、返回维护页，还是故意 fail closed？这不是技术组件临时能决定的事情，要按业务风险设定。金融支付类业务可能宁愿快速失败，也不接受写入到错误地域；内容站点可能优先返回缓存或只读页面。Cloudflare 的 fallback pool 概念就是为“所有默认池都不可达时流量仍然需要去某处”准备的，但 fallback 不是万能保险，fallback 自己也要有容量和依赖治理。

最后，要定期演练。GSLB 高可用如果只停留在配置上，真正事故时常常卡在 TTL、缓存、健康检查误判、容量不足、灾备数据滞后和人工权限上。面试里可以强调：GSLB 避免单点故障的关键不是买一个全局负载均衡产品，而是把全局入口当成独立系统做数据面冗余、控制面隔离、变更治理和故障演练。

## 8. GSLB 与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：如果 GSLB 是 DNS 型，它和 DNS 轮询一样，基本不传递真实客户端 IP、协议、超时和追踪上下文；它只影响客户端最终连接到哪个区域入口。如果 GSLB 是代理型或和 CDN/边缘代理深度集成，它才可能在 L4 或 L7 层添加 `X-Forwarded-For`、`Forwarded`、`X-Forwarded-Proto`、`CF-Connecting-IP`、PROXY protocol、trace header 等上下文。

DNS 型 GSLB 的输入通常是查询域名、递归解析器地址、ECS 网段、地理库、延迟探测、健康状态和策略配置。它的输出是一个或多个记录，例如区域 SLB 的 CNAME、A/AAAA 或别名记录。这个模型下，GSLB 不在真实业务请求路径中，不会看到 HTTP Header，也就不能天然透传真实客户端 IP 或 trace id。它最多通过 ECS 近似判断用户网络位置，或者根据递归解析器来源做区域选择。

到了下游入口，才需要把上下文补齐。CDN 可以添加边缘标识、客户端 IP、协议和 Ray ID；L7 负载均衡器可以添加 `X-Forwarded-For`、`X-Forwarded-Proto`、`X-Request-Id`；L4 负载均衡器如果不终止协议，可以用 PROXY protocol 把源地址传给后端；API Gateway 或 Service Mesh 可以生成或续传 W3C Trace Context。Amazon CloudFront 对自定义源站请求会追加或设置 `X-Forwarded-For` 来携带 viewer IP，并且有 origin connection timeout、origin response timeout 这类源站超时配置。这说明上下文和超时治理是在 CDN/代理层完成的，不是 DNS 型 GSLB 的能力。

协议传递也要分层讲。GSLB 可以把用户导向支持 HTTP/3 的 CDN，或导向某个区域入口，但 HTTP/2、HTTP/3、gRPC 的协商发生在后续连接阶段。超时同样如此：GSLB 可以影响“连接到哪个区域”，进而影响 RTT 和失败概率；端到端 deadline、per-try timeout、connect timeout、read timeout 仍要由客户端、入口代理和服务框架设置并传播。

工程上有一个实用做法：在区域入口接到请求后，把 GSLB 决策结果显式写入日志和追踪标签，例如 `edge_pop`、`gslb_policy`、`selected_region`、`origin_pool`、`traffic_weight_version`。这样即使 DNS 型 GSLB 不在 trace 里，也能在后续排障时回答“这批请求为什么进了这个地域”。

## 9. GSLB 如何做健康检查、摘除、恢复和流量预热？

可以先这样答：GSLB 的健康治理对象通常不是单个进程，而是区域、机房、入口池、CDN 源站池或跨云入口。它要用多地域探针判断“这个入口是否能代表真实用户接流量”，再通过权重、优先级、地理策略、延迟策略或 fallback 策略完成摘除和恢复。预热则要同时照顾 DNS 缓存、区域入口容量、应用冷启动和数据一致性。

健康检查最好不要只做 ICMP 或 TCP 端口探测。一个区域 SLB 端口开着，只说明入口进程活着，不说明这个地域可以处理真实业务。更好的检查路径应该穿过 TLS、WAF、网关、核心服务、关键依赖，至少覆盖“能否建立连接、响应是否足够快、状态码是否正确、响应体是否符合预期”。Cloudflare 的健康监控也强调端点是否响应、是否在 timeout 内响应、HTTP 状态码和响应体是否符合预期。对于写请求，还要额外看数据库主从状态、复制延迟、分布式锁、幂等表、消息队列积压，否则 GSLB 可能把用户切到一个只能读不能写的地域。

摘除方式取决于策略。主备模式下，可以把主区域标记为 unhealthy 后切到备区域；加权模式下，可以把某个区域权重降到 0；地理或延迟模式下，可以把不健康 pool 从候选集中移除；代理型 GSLB 可以直接在边缘流量调度中停发。摘除动作要配合 TTL 和连接排空。DNS 型 GSLB 即使已经停止返回某区域地址，旧解析结果和已有连接仍会继续命中旧区域，所以旧区域入口必须能快速失败、返回可重试错误或做只读降级，而不是无限超时。

恢复阶段要有迟滞和预热。迟滞包括连续成功次数、最小健康保持时间、错误率回落阈值和人工确认；预热包括小权重放量、连接池预建、缓存预热、热点数据加载、限流阈值逐步打开、下游依赖容量确认。Cloudflare 支持 `consecutive_up` 和 `consecutive_down` 这类连续成功/失败参数，本质就是为了避免健康状态在边界抖动时频繁翻转。

还要注意全局切流的容量问题。一个地域故障后，其他地域不是天然能吃掉 100% 流量。GSLB 摘除前要知道每个区域的 spare capacity，恢复时也不能只看“主区域恢复了”，还要看“主区域有没有足够容量接回流量”。企业实践里常把 GSLB 和容量管理、自动扩容、限流熔断、灾备演练放在一起看，否则 GSLB 只是在更快地把故障从一个区域转移到另一个区域。

## 10. GSLB 的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：GSLB 的观测要覆盖三类问题：它是否可用，它是否把流量导向了正确位置，它的决策是否和真实业务效果一致。只看 GSLB 控制台显示“healthy”是不够的，因为用户体验取决于 DNS 解析、网络路径、区域入口、应用依赖和缓存共同作用。

基础指标包括权威 DNS 或全局代理 QPS、解析/转发延迟、错误率、超时率、各 NS 或边缘节点健康状态、配置版本、策略命中情况、健康检查成功率、探针延迟、pool/endpoint 状态、fallback pool 使用次数、failover 次数和持续时间。策略指标要看每个地域、机房、云厂商、运营商、ASN、递归解析器拿到的答案，以及这些答案和预期权重、地理策略、延迟策略是否一致。

更关键的是“决策到流量”的对账。GSLB 层看到某区域权重是 30%，真实入口日志里该区域可能承载 50%，原因可能是 DNS cache、长连接、大客户出口、运营商递归解析器集中、CDN 边缘复用、客户端连接池。反过来，GSLB 认为某区域健康，真实业务却有大量 5xx 或高 p99，说明健康检查太浅，或者区域内部依赖已经退化。要把 GSLB 选择的 region、pool、policy version 写入边缘日志、指标标签和 trace baggage，后续才能做归因。

判断 GSLB 成为瓶颈，可以看几个信号。第一，解析或边缘调度延迟升高，用户在建立连接前就耗时明显增加。第二，GSLB 健康检查大面积 flapping，导致流量在区域之间来回震荡。第三，配置发布延迟变长，故障切流命令下发后很久才生效。第四，fallback pool 突然承载大量流量，说明主策略下的 pool 被判定不可用或策略配置出了问题。第五，某些地域或运营商长期被导向远端区域，RTT 和 p99 明显异常。第六，GSLB 本身的 API、控制面、探针系统出错，导致无法安全变更或无法确认当前策略。

排障时可以用四组探针交叉验证：公共递归解析器探针，例如 Google、Cloudflare、运营商 DNS；地域探针，例如北美、欧洲、东南亚、中国大陆出口；真实用户监控 RUM；区域入口合成请求。GSLB 的问题经常伪装成“某地域用户访问慢”，但根因可能是 ECS 缺失、地理库错误、健康探针误判、权重配置漂移、递归缓存过期不一致或边缘到源站链路抖动。

## 11. CDN 边缘调度位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：CDN 边缘调度位于公网入口的边缘层，通常在 DNS/GSLB 之后、企业源站或区域入口负载均衡器之前。它主要解决互联网入口流量的就近接入、缓存命中、动态加速、TLS 终止、WAF、防护、边缘规则和源站选择问题。它不是东西向服务治理组件，但它可以承担一部分应用入口路由，例如按 host、path、header、设备类型或国家地区选择不同源站或缓存策略。

CDN 和 DNS/GSLB 的关系可以这样理解：DNS/GSLB 决定用户先到哪个 CDN 域名、哪个边缘网络或哪个全局入口；CDN 的边缘调度决定用户落到哪个 PoP、请求是否命中缓存、未命中时回哪个源站、源站失败时是否切到备用源、是否执行边缘函数或安全策略。对于静态资源，CDN 可能直接在边缘返回，不再访问源站；对于动态 API，CDN 更像反向代理和边缘加速层。

CDN 边缘调度能看到的信息比 DNS 多。它在 HTTP 或 HTTPS 请求路径上，可以看到 Host、Path、Query、部分 Header、Cookie、客户端 TLS/HTTP 协议、源站响应码、缓存状态、边缘 PoP、WAF 命中规则。Cloudflare HTTP headers 文档里列出了 `CF-Connecting-IP`、`X-Forwarded-For`、`X-Forwarded-Proto`、`Cf-Ray` 等请求头；CloudFront 自定义源站请求行为里也有 `X-Forwarded-For`、源站连接超时、源站响应超时等说明。这些能力都说明 CDN 已经进入请求路径，而不是单纯做名称解析。

但 CDN 也不应该被误认为完整的业务路由系统。它适合做入口层规则、缓存策略、边缘安全、灰度入口和源站选择；复杂的租户级路由、权限判断、事务一致性、服务间重试、下游熔断，仍然应该放在 API Gateway、Service Mesh 或应用层。尤其是有状态写请求，CDN 可以帮你把请求送到某个源站，但它不能替代后端对幂等性、事务隔离和数据复制延迟的处理。

面试里可以用一句话收束：CDN 边缘调度解决的是“用户从公网进入系统时，先在哪个边缘点被接住，缓存能不能命中，未命中时回哪个源站”的问题；东西向服务治理和深层业务路由不是它的主战场。

## 12. CDN 边缘调度在高可用设计中如何避免单点故障？

可以先这样答：CDN 边缘调度的高可用不能只依赖“CDN 厂商自己有很多节点”。企业要同时设计多边缘节点、多源站、多区域、源站 failover、缓存降级、配置回滚、证书管理和供应商故障兜底。CDN 本身通常是高可用网络，但你的域名、配置、源站、证书、WAF 规则和回源链路仍然可能形成单点。

第一，边缘网络要有供应商和接入兜底。大多数业务使用单一 CDN 已经足够，但核心入口可以考虑多 CDN、备用 CDN 或 DNS/GSLB 级别的 CDN 切换预案。多 CDN 不是简单多配一个 CNAME，它会带来缓存一致性、证书部署、WAF 规则同步、日志格式、实时 purge、灰度发布、成本和排障复杂度。对于高价值业务，这些复杂度是为了避免单一 CDN 控制面、边缘网络、配置发布或区域性故障导致全站入口不可用。

第二，源站要有冗余和自动 failover。CloudFront 支持 origin group，把一个源站设为 primary、另一个设为 secondary；当 primary 不可用或返回配置的失败状态码时，CloudFront 可以切到 secondary。它的文档也提醒，failover 通常要求至少两个 origin，并且可以按 400、403、404、416、429、500、502、503、504 等状态码配置切换条件；同时，不同 HTTP 方法的 failover 行为也有限制。面试时可以把这个点说清楚：CDN 源站高可用不是“回源地址写两个”这么简单，要看失败判定、请求方法、缓存行为和源站状态码。

第三，要用缓存能力做降级。静态资源可以设置合理 TTL、版本化 URL、stale-if-error、serve stale、cache shield 或区域缓存层，让源站短时不可用时边缘仍能返回旧对象。动态请求不能随便缓存，但可以把首页、配置、只读接口、降级页、维护页做边缘兜底。注意缓存降级要和业务一致性边界对齐，不能把用户私有数据、权限相关响应或强一致写结果错误缓存。

第四，证书、密钥和配置控制面要高可用。CDN 事故里很常见的一类是证书过期、证书链错误、SNI 配置错、WAF 规则误杀、边缘函数发布 bug、缓存规则误配、源站 Host Header 错误。解决方式是证书自动续期和告警、配置版本化、预发布环境、按小流量或小地域灰度、快速回滚、规则单元测试和变更审计。不要在全站域名上直接发布未验证的边缘函数或 WAF 大规则。

第五，要保护源站免受 cache miss 风暴。CDN 边缘本身很强，但如果大面积 purge、热门对象过期、区域缓存失效或攻击流量绕过缓存，所有请求会压到源站。高可用设计要有 origin shield、请求合并、回源限流、负载均衡、熔断、排队、静态化和自动扩容。CloudFront 文档提到 request collapsing：多个边缘请求同时等待同一个未缓存对象时，CloudFront 会暂停额外回源并复用第一个响应，这就是保护源站的一种方式。

最后，观测和演练要覆盖边缘到源站全链路。看 CDN 边缘 2xx/4xx/5xx、cache hit ratio、origin latency、origin error rate、TLS handshake、WAF block、边缘函数错误、回源连接超时、各 PoP 流量和异常地域。真正演练时要验证 CDN 故障切换、源站 primary 故障、证书回滚、WAF 误封回滚、缓存清空后的源站容量。CDN 高可用的核心不是“边缘节点足够多”，而是“边缘、源站、配置和降级策略没有共同单点”。

## 13. CDN 边缘调度与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：CDN 边缘调度已经在真实请求路径上，和 DNS/GSLB 不一样，它能在 HTTP/TLS 层补充或转发一部分上下文。真实客户端 IP 通常通过 `X-Forwarded-For`、`Forwarded`、`X-Real-IP`、`CF-Connecting-IP`、`True-Client-IP` 这类头部传给源站；协议通常通过 `X-Forwarded-Proto`、`Forwarded`、`CF-Visitor`、CloudFront 日志里的 `cs-protocol`、`cs-protocol-version` 体现；追踪上下文靠 `traceparent`、`tracestate`、`x-request-id`、`x-amz-cf-id`、`Cf-Ray` 这类请求 ID 或 trace header 继续向下游传。超时则不是“传给源站一个数字”这么简单，更多是 CDN 自己的 viewer 连接、origin connection timeout、origin response timeout、重试和 failover 策略。

真实客户端 IP 要先讲信任边界。用户可以伪造普通 HTTP Header，所以源站不能盲信客户端直传的 `X-Forwarded-For`。正确做法是只信任来自 CDN 回源 IP 段的请求，并由入口代理按固定规则清洗或重写头部：保留 CDN 注入的真实客户端 IP，丢弃不可信上游带来的同名头。Cloudflare 文档明确说明 `CF-Connecting-IP` 是 Cloudflare 边缘到源站请求中携带客户端 IP 的头；CloudFront 也会在转发到自定义源站时追加或设置 `X-Forwarded-For`。如果源站还在后面接 Nginx、Envoy、API Gateway 或 Service Mesh，这些组件要继续按可信代理链处理，否则日志里的客户端 IP 会变成 CDN 节点 IP、内网 LB IP，或者被伪造头污染。

协议上下文要分成 viewer 侧和 origin 侧。用户到 CDN 可能是 HTTP/2、HTTP/3、TLS 1.3，CDN 到源站可能降成 HTTP/1.1，也可能继续用 HTTPS、WebSocket、gRPC 或自定义回源协议。面试时不要简单说“CDN 透传协议”，更准确的说法是：CDN 终止或代理了 viewer 侧连接，再按配置建立到源站的新连接。下游如果需要知道用户原始协议，要依赖 `X-Forwarded-Proto`、`Forwarded`、Cloudflare 的相关头部或 CDN 日志字段，而不是从源站 socket 直接推断。

超时上下文也要小心。CloudFront 的文档把 origin connection timeout、origin connection attempts、origin response timeout 分开：连接超时和尝试次数决定它在连接源站失败前等待多久；响应超时决定转发请求后等待源站首包或后续包的时间。应用层 deadline 是另一回事，应该由客户端、API Gateway 或 RPC 框架通过 Header/metadata 传递。CDN 可以保护用户不被源站无限拖住，也可以在 GET/HEAD 等场景触发重试或 failover，但它不应该凭空制造一个业务 deadline 后让下游误以为这是端到端预算。

追踪上下文的最佳实践是入口处统一收敛。CDN 可以保留用户侧传来的 W3C Trace Context，也可以生成自己的请求 ID。之后边缘函数、WAF、源站网关、应用服务要把同一个请求 ID 写入日志和 trace。工程上我会建议同时保留两个 ID：一个是业务 trace id，用于服务链路；另一个是 CDN request id 或边缘节点 ID，用于定位边缘 PoP、缓存状态、回源路径和供应商工单。这样排查“用户慢”时，能从应用 trace 追到 CDN 边缘日志，而不是只看到源站收到了一次普通 HTTP 请求。

## 14. CDN 边缘调度如何做健康检查、摘除、恢复和流量预热？

可以先这样答：CDN 边缘调度的健康治理对象主要是源站、源站组、缓存行为、边缘配置和回源路径。它不像 Service Mesh 那样按单个 Pod 做精细摘除，也不像进程内负载均衡器那样按每次请求选择实例；它更常见的动作是判断某个 origin、origin group、区域源站或回源链路是否可用，然后通过源站 failover、权重、规则、缓存兜底或多 CDN/GSLB 切换来控制流量。

健康检查有主动和被动两类。主动检查是 CDN 或外部监控周期性访问源站的健康路径，检查 TCP/TLS、HTTP 状态码、响应体和响应时间。被动检查来自真实请求，例如源站连接失败、读超时、5xx、TLS 证书错误、DNS 解析失败、边缘函数异常。CloudFront origin failover 的做法是配置 origin group：cache miss 时先回 primary origin，如果 primary 不可用或返回配置的失败状态码，就切到 secondary origin。这里要注意请求方法限制和状态码范围，不能假设所有写请求都会安全自动 failover。

摘除源站有几种方式。第一，把源站从 CDN 规则或 origin group 中移除，或者把它的权重降到 0。第二，把 GSLB/DNS 层流量切到另一个 CDN 或另一个区域。第三，在源站入口处启用 drain，让已有连接尽快完成，新连接停止进入。第四，利用缓存策略在短时间内减少回源，例如延长静态资源 TTL、启用 serve stale、把降级页放到边缘。摘除时最忌讳只改 CDN 配置而不看回源连接，因为 WebSocket、gRPC、长轮询、视频分片下载和大文件下载可能还在旧路径上跑。

恢复要分两步。第一步是源站恢复健康，但不立刻满量接流。需要确认源站依赖、数据库复制、缓存、证书、WAF 放行、DNS、对象存储、队列和限流策略都回到可承载状态。第二步才是逐步放量。可以从少量 path、少量 host、少量地域、少量权重开始，再观察 cache hit ratio、origin first-byte latency、origin 5xx、回源连接数、源站 CPU、队列和 p99。CDN 恢复最容易出问题的地方是冷缓存：边缘节点都开始回源，源站瞬间承受比平时高得多的 miss 流量。

预热要按内容类型处理。静态资源可以提前发布版本化 URL、预加载热点对象、避免全量 purge，必要时使用 cache shield 或 origin shield 减少源站被多边缘节点同时打穿。动态接口不能简单预热缓存，但可以预热 TLS、连接池、应用配置、热点数据、JIT、对象池和下游连接。边缘函数或规则也要灰度发布，不能在全站一次性切到新函数。面试里可以补一句：CDN 健康治理的难点不在“发现源站挂了”，而在“切走、切回和缓存状态变化不会把源站打穿”。

## 15. CDN 边缘调度的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：CDN 边缘调度的观测要分 viewer 到边缘、边缘自身、边缘到源站、配置控制面四段看。只看源站 5xx 不够，因为很多问题会在到达源站之前就发生，例如 TLS 握手失败、WAF 误拦、边缘函数超时、缓存规则错误、PoP 局部拥塞、回源 DNS 失败。

viewer 到边缘侧，重点看请求量、地域/ASN/运营商分布、TLS 握手错误、HTTP 协议版本、首字节时间、下载完成时间、客户端断开、4xx、WAF block、Bot 管控命中、边缘 PoP 分布。CloudFront 实时日志里有 `c-ip`、`cs-protocol`、`cs-protocol-version`、`x-edge-location`、`x-edge-request-id`、`time-to-first-byte`、`time-taken` 等字段；这些字段能把“哪个用户、哪个边缘、哪个协议、耗时在哪里”串起来。

边缘自身要看 cache hit ratio、miss ratio、refresh hit、error、redirect、capacity exceeded、limit exceeded、边缘函数错误、规则命中、缓存键膨胀、对象大小分布、热点对象命中情况。CloudFront 的 `x-edge-result-type` 和 `x-edge-detailed-result-type` 能区分 Hit、Miss、Error、CapacityExceeded、OriginConnectError、OriginReadError、OriginDnsError 等类型；CloudWatch 也会发布分发和边缘函数的运行指标，并支持基于 5xxErrorRate 之类指标告警。Cloudflare 侧可以通过 Analytics / GraphQL 数据集查看 HTTP 请求、负载均衡请求、Workers、网络层等聚合数据。

边缘到源站侧，要看 origin first-byte latency、origin last-byte latency、origin 连接超时、origin response timeout、origin 5xx、回源连接数、回源带宽、源站 DNS 失败、证书错误、SNI/Host Header 错误、源站限流、回源重试和 failover 次数。CloudFront 实时日志里的 `origin-fbl` 和 `origin-lbl` 分别表示 CloudFront 到源站的首字节和末字节延迟；这些指标比单看用户侧耗时更适合判断瓶颈是不是在回源路径。

配置控制面要看变更发布时间、规则版本、证书状态、WAF 规则发布、边缘函数发布、purge 队列、配置回滚耗时和跨 PoP 生效一致性。很多 CDN 事故的症状像性能问题，根因却是配置错：缓存键包含了高基数字段导致 hit ratio 掉光，某条 WAF 规则误封，源站 Host Header 配错，或者一次 purge 把热点对象全清了。

判断 CDN 自身成为瓶颈，可以看几个信号。第一，源站健康但多个地域边缘出现 `CapacityExceeded`、边缘 5xx 或边缘函数错误。第二，用户侧 TTFB 升高，但 `origin-fbl` 没升高，说明慢在 viewer 到边缘或边缘处理。第三，某个 PoP、ASN、运营商异常，而其他区域正常。第四，hit ratio 突然下降，源站 QPS 和带宽同步上升。第五，配置变更或 purge 长时间不收敛，导致同一 URL 在不同边缘返回不同版本。第六，CDN 日志缺失或延迟过大，排障只能靠源站日志反推。一个成熟回答应该强调：CDN 是入口能力，不是黑盒；要把边缘 ID、请求 ID、缓存状态和源站耗时都打通。

## 16. BGP Anycast 位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：BGP Anycast 位于网络路由层，通常在 L3/IP 层通过多个位置对同一个服务地址或前缀发布可达性，让路由系统把客户端流量送到其中一个可达节点。它主要解决公网入口或大规模网络入口的就近接入、粗粒度负载分散、DDoS 流量分摊和区域级容灾问题。它不理解 HTTP path、Cookie、租户、RPC 方法，也不天然解决应用路由。

IETF RFC 4786 对 Anycast 的定义很适合面试引用：把一个服务地址放在多个离散、自治的位置上，发往这个地址的数据报会被路由到其中一个可用位置。这里的关键词是“路由系统选择节点”。DNS/GSLB 是先通过名字解析告诉客户端去哪里，BGP Anycast 是客户端已经连同一个 IP，网络路由把它导向某个 PoP、机房或边缘入口。常见用法包括 DNS 根服务器、公共递归 DNS、CDN 边缘 IP、DDoS 清洗入口、Anycast VIP、全球 API 入口。

它解决的是入口层问题，而且是比 CDN/网关更靠前的网络入口问题。用户包到达哪个 Anycast 节点，取决于 BGP 路由、AS_PATH、本地优先级、MED、社区、运营商策略、peering、IGP 成本和网络拓扑。这个“近”通常是拓扑近，不一定是地理近，也不一定是 RTT 最低。面试里如果只说“Anycast 会自动连最近节点”，是不够准确的。

东西向流量也可能使用 Anycast VIP，例如同城多机房内部服务入口、数据中心内网关 VIP、Kubernetes Node/网关通过 BGP 宣告服务地址。但这属于网络层入口冗余，不等同于 Service Mesh 的服务发现、重试、熔断和请求级路由。应用路由仍然要靠 Anycast 节点后面的 L4/L7 负载均衡器、Envoy、Nginx、API Gateway 或业务服务完成。

Anycast 的边界要讲清楚：它擅长把流量吸到一个“合适的入口点”，不擅长对每个请求做精细调度。RFC 4786 也指出，Anycast 节点之间的负载均衡通常很难做到精确，节点负载一般是不均衡的；它更适合做可靠性、扩展性和粗粒度流量分布。

## 17. BGP Anycast 在高可用设计中如何避免单点故障？

可以先这样答：BGP Anycast 避免单点故障的核心是多节点发布同一个服务前缀，并让每个节点只在本地服务真正可用时发布路由。它的高可用不只是“多个机房都宣告同一个 IP”，还包括路由发布控制、健康检查联动、覆盖前缀兜底、BGP 会话冗余、上游运营商冗余、防路由劫持和节点自治。

第一，Anycast 节点要分布在独立故障域。不同 PoP、不同机房、不同云、不同运营商、不同电力和网络上游，才能避免一个区域故障把所有入口打掉。每个节点内部也要有本地高可用：至少两台边界路由器、冗余交换、冗余负载均衡器、冗余应用实例。否则外部看是 Anycast，内部还是单台设备。

第二，路由发布必须和服务健康绑定。RFC 4786 在“Signalling Service Availability”里强调，节点开始收到流量前必须已经准备好接请求，因此路由信息和节点服务可用性最好有耦合。工程上常见做法是由本地 health checker 检查 L4/L7 服务、依赖和本地入口，然后通过 BGP speaker、ExaBGP、GoBGP、FRR、Bird 或路由器策略发布/撤销更具体前缀。只要服务坏了但路由还在，这个节点就会变成黑洞。

第三，要处理“撤太多”和“撤不掉”的两类风险。撤太多会导致大面积流量切走，其他节点容量不够；撤不掉会导致坏节点继续吸流。为此需要本地阈值、连续失败次数、人工保护、最大撤销比例、fallback 策略和覆盖前缀。覆盖前缀可以保证当更具体前缀撤销时，流量仍然落到某个全局兜底入口；但覆盖前缀也会把服务可用性和多个业务地址绑在一起，设计不好会扩大故障面。

第四，BGP 层自身要高可用。边界路由器和上游 BGP peer 要冗余，路由策略要版本化，prefix-list、route-map、community、RPKI/ROA、最大前缀限制要配置正确。RFC 4271 说明 BGP 通过 UPDATE 消息交换网络可达性和撤销信息；这意味着 Anycast 的高可用最终会受 BGP 收敛、路由策略、上游接受策略和全网传播影响。不能把它当作毫秒级健康切换。

第五，要防路由劫持和误公告。Anycast 前缀通常是核心入口地址，一旦被错误发布或被第三方劫持，用户流量可能被黑洞或导向错误位置。企业要做 RPKI ROA、路由过滤、上游白名单、监控 RouteViews/RIPE RIS/Cloudflare Radar 等外部视角，关键变更双人复核。面试里可以补一句：Anycast 高可用的本质是“用路由系统做入口冗余”，但路由系统自己也需要工程治理。

## 18. BGP Anycast 与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：BGP Anycast 本身不传递这些应用上下文。它只是让网络把发往某个 IP 前缀的包送到某个 Anycast 节点，源 IP、目的 IP、端口和协议号仍然在 IP/TCP/UDP 包头里；至于真实客户端 IP、HTTP 协议、超时、trace id，要由 Anycast 节点后的代理、负载均衡器、网关或应用层来处理。

真实客户端 IP 在 Anycast 场景里有两种情况。如果 Anycast 节点直接终止连接，例如 CDN 边缘、DNS 服务器、API 入口，那么节点能从连接源地址看到客户端或上游 NAT/代理地址。它再往下游转发时，需要用 `X-Forwarded-For`、`Forwarded`、`X-Real-IP` 或 PROXY protocol 把源地址交给源站。如果 Anycast 节点只是 L3/L4 转发或 DSR，源站可能直接看到原始客户端 IP，也可能看到隧道、NAT 或负载均衡器 IP，这取决于数据平面设计。不能笼统说 Anycast 会“透传真实 IP”。

协议上下文要看连接在哪里终止。Anycast 路由层只关心 IP 可达性，不知道 HTTP/2、HTTP/3、gRPC 方法、Host、Path。TLS 如果在 Anycast 节点终止，节点可以看到 SNI、ALPN、HTTP Header，并把协议信息写入下游 Header；如果 TLS 在源站终止，中间 Anycast 网络只做包转发，就看不到应用层上下文。DNS Anycast 更典型，它只处理 DNS 查询，不会承载后续业务请求的 trace。

超时也不由 BGP Anycast 传递。Anycast 影响的是路径和落点，可能改变 RTT、丢包和连接稳定性；connect timeout、read timeout、overall deadline 仍然由客户端、边缘代理或应用框架控制。Anycast 路由变化如果发生在长连接中间，连接可能抖动甚至断开。RFC 4786 在协议适用性里就提醒，单个 client-server 交互通常需要在同一个服务节点上完成，因此路由系统的节点选择要在交易时长内保持足够稳定。

追踪上下文要从 Anycast 节点进入应用链路时生成或续传。建议在边缘入口给请求打上 `anycast_node`、`pop`、`router`、`peer_asn`、`route_policy_version`、`edge_request_id`，再把业务 trace id 往后传。这样当用户报“同一个 IP 有时快有时慢”时，可以定位到底是哪个 Anycast 节点、哪个 BGP catchment、哪个上游运营商和哪个源站链路的问题。

## 19. BGP Anycast 如何做健康检查、摘除、恢复和流量预热？

可以先这样答：BGP Anycast 的健康治理是“服务健康决定路由是否发布”。健康检查不是只探测 BGP 会话是否 up，而是要确认本地 Anycast 节点上的服务、负载均衡器、应用依赖和回源链路是否能承载真实流量。摘除就是撤销或调整路由公告，恢复就是重新公告并让 catchment 慢慢稳定，预热则要处理 BGP 收敛、缓存、连接和节点容量。

健康检查至少要有三层。第一是节点基础设施：路由器、BGP session、接口、链路、服务器、负载均衡器。第二是服务探测：TCP/TLS、HTTP 健康路径、DNS 响应、WAF/网关、源站连接。第三是容量和质量：CPU、内存、队列、连接数、丢包、RTT、5xx、p99、回源失败率。如果只看进程存活，节点可能还在宣告路由，但真实用户已经大量超时。

摘除动作通常有几种。最直接是撤销该节点的具体 Anycast 前缀，让流量被其他节点或覆盖前缀接走。也可以通过 AS_PATH prepend、community、local preference、MED 等策略降低吸流能力，而不是一刀切撤掉。对于内部 Anycast，可以通过 IGP cost、BGP local preference 或 next-hop 权重调整流量。摘除要带迟滞，避免瞬时抖动导致路由 flap；BGP 有收敛和最小公告间隔，撤销不是实时请求级调度。

恢复比摘除更容易被低估。节点重新发布路由后，某些网络很快切回来，某些网络可能慢很多；流量回流不是线性的。恢复前要先确认本地服务热起来了：缓存、连接池、TLS 证书、应用配置、源站链路、DDoS 清洗规则、日志链路都正常。恢复时可以先只对本地或少数 peer 发布，或者通过更低优先级的路由策略小流量接入，再逐步扩大。对于 CDN/API 场景，还要预热边缘缓存和回源连接，避免新恢复节点一上来全是 miss。

还有一个 Anycast 特有问题：摘除节点会改变 catchment，其他节点突然接收这部分用户。容量规划必须按“最大一个或多个节点失效后剩余节点能不能吃下流量”来算。DDoS 场景更复杂，撤掉受攻击节点可能把攻击流量转移到其他节点；有时正确动作不是撤路由，而是在本地清洗，让攻击被局部吸收。RFC 4786 也把 Anycast 用于把攻击影响局部化，但这要求节点本地有足够的防护和自动化。

## 20. BGP Anycast 的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：BGP Anycast 的观测要同时看路由面、数据面、服务面和客户端视角。Anycast 最大的问题是“不同用户看到的是不同节点”，所以从一个机房探测成功，不能证明全网正常；从一个客户端慢，也不能证明所有节点都慢。

路由面要看每个 Anycast 前缀的公告状态、BGP peer 状态、UPDATE/withdraw 次数、收敛时间、AS_PATH、local preference、MED、community、ROA/RPKI 状态、路由泄漏和劫持告警、各外部 looking glass 看到的 best path。RFC 4786 建议从多个位置监控路由系统，并可利用 RIPE RIS、Route Views 这类公共路由测量设施；这正是因为 Anycast 可达性有明显视角差异。

数据面要看每个 PoP/节点的流量、pps、bps、连接数、新建连接速率、TCP 重传、丢包、RTT、队列、接口丢包、ECN、DDoS 清洗命中、ACL 丢弃、NAT/conntrack 使用率。如果某个节点的流量远超预期，而路由面看起来正常，可能是 catchment 过大、上游策略变化、某个大客户出口被吸到这个节点，或者其他节点撤路由后流量转移过来。

服务面要看应用层成功率、5xx、p99、DNS 响应时间、HTTP TTFB、TLS 握手、源站回源失败、WAF/网关错误、节点 ID。RFC 4786 建议分布式服务最好能在协议里暴露节点身份，便于知道哪个节点服务了请求。对于 HTTP 入口，可以在响应头或日志里记录 PoP；对于 DNS Anycast，可以用 NSID；对于内部系统，可以在 trace tag 里写 `anycast_node`。

判断 Anycast 自身成为瓶颈，有几个典型信号。第一，单个或少数 PoP 入口流量打满，其他节点空闲，说明 catchment 或路由策略不平衡。第二，路由频繁 flap，用户长连接断开或 RTT 抖动。第三，同一个服务 IP 在不同运营商表现差异很大，说明 peering、路径或上游策略有问题。第四，服务健康但某些地区完全访问不到，可能是前缀被过滤、ROA 错误、BGP 社区误用或运营商路由泄漏。第五，撤销路由后流量没有按预期迁移，说明外部网络缓存、策略或覆盖前缀设计不符合预期。排障要把应用日志、PoP 指标和外部 BGP 视角放在一起看，单看应用指标很容易误判。

## 21. ECMP 位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：ECMP，Equal-Cost Multi-Path，位于网络转发层，通常在 L3 路由或交换芯片的数据面里工作。它解决的是“到同一个目的前缀有多条等价下一跳时，如何把流量分摊到多条路径上”。它既可以出现在入口网络，也可以出现在数据中心东西向网络，还可以出现在服务器到网关、Leaf-Spine、边界路由器、负载均衡器集群前面的 underlay 网络里；但它不是应用路由。

RFC 2991 对 ECMP 的描述很直接：如果到同一目的存在多条 equal-cost route，转发设备可以发现并使用它们，在冗余路径之间做负载分担。注意这里的调度对象是下一跳或链路，不是应用实例。ECMP 看的是目的前缀、源/目的 IP、协议、端口等包头字段，常见实现会对五元组或三元组做 hash，把同一条 flow 固定到同一条路径，避免同一 TCP 连接的包乱序。

入口流量里，ECMP 常用于把进入机房的流量分散到多台边界设备、多台 L4 负载均衡器、多条上联链路或多个隧道。东西向流量里，它更常见：Leaf-Spine 网络会让任意两个机架之间有多条等价路径，ECMP 负责把大量 flow 分摊到这些路径上，提高带宽利用率和容错能力。应用路由里，ECMP 不知道 `/api/order`、tenant、gRPC method、JWT、Cookie，也不知道某台后端 CPU 高不高；这些要由 L7 网关、服务网格或应用负载均衡器处理。

它和负载均衡的关系要讲清楚。ECMP 是网络层负载分担，不是完整的服务负载均衡。它通常按 flow hash，不能保证每台后端实例请求数相同，也不能处理慢节点、半死节点、熔断、重试、预热、会话语义。举个例子，某条 elephant flow 会长期占用一条路径，即使其他路径很空，ECMP 也未必会把这条连接拆开迁移，因为迁移会造成乱序和状态问题。

## 22. ECMP 在高可用设计中如何避免单点故障？

可以先这样答：ECMP 通过多条等价路径、多台下一跳设备和快速故障收敛来避免单链路或单设备故障。它的价值不是“每个请求更聪明”，而是“任意一条链路或一个下一跳故障后，剩余路径还能继续转发，并把受影响 flow 重新映射到可用路径”。高可用设计要关注路径冗余、故障检测、hash 稳定性、容量冗余和流量重分布。

第一，物理和拓扑上要真冗余。Leaf-Spine 里服务器机架至少上联到多台 Leaf，Leaf 到 Spine 有多条链路，边界出口有多台路由器和多个上游。多条 ECMP path 如果共用同一根光纤、同一块线卡、同一个电源或同一个上游，其实只是逻辑冗余。路由协议也要能发现这些等价路径，例如 OSPF、IS-IS、BGP multipath 或静态多下一跳。

第二，故障检测要足够快但不能过度敏感。链路 down、BFD down、接口错误、下一跳不可达、BGP/IGP 邻居断开，都应该触发路径从 ECMP 集合里移除。检测太慢，会继续把流量打到坏路径；检测太敏感，会导致路径 flap，带来大量 flow 迁移。RFC 2991 提到，多路径下如果路由频繁增删，包乱序和丢包影响会比不用多路径更明显，因为更多活跃 flow 会受路径集合变化影响。

第三，hash 算法要尽量减少扰动。传统 modulo-N 在下一跳数量变化时会让大量 flow 改路；RFC 2991 和 RFC 2992 都讨论了 hash-threshold、HRW 等方法对 flow 扰动的影响。工程上常见的是 resilient hashing、flowlet switching 或厂商的 resilient ECMP，让某条路径故障时尽量只迁移原来落在坏路径上的 flow，而不是让所有 flow 重新洗牌。这样能减少 TCP 乱序、重传和长连接抖动。

第四，容量要按故障后重分布设计。四条等价路径平时每条 25% 负载，不代表坏一条后剩下三条一定安全。如果平时每条已经 70%，坏一条后剩余路径可能超过容量，丢包和排队会让应用 p99 飙升。高可用 ECMP 要保留 headroom，常见口径是按 N+1、N+2 或故障域级别评估，而不是只看正常状态下均衡。

第五，要把 ECMP 和上层健康治理分开。ECMP 能摘掉坏链路或坏下一跳，但它不知道后面的应用实例是否半死。比如某台 L4 负载均衡器进程还在转发，路由下一跳也可达，但后端池全挂了，ECMP 仍可能把流量发给它。解决方式是在下一跳设备本地做服务健康联动，或者让上层 LB/网关自己做熔断和摘除。面试里可以这样总结：ECMP 避免的是网络路径单点，不是业务实例单点；它是高可用流量架构的底座，不是最后一道调度逻辑。

## 23. ECMP 与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：ECMP 本身几乎不“传递”应用上下文。它在网络转发层根据路由表、下一跳集合和 hash 结果选路径，原始 IP 包里的源地址、目的地址、协议号、源端口、目的端口会跟着包走；但 HTTP Header、gRPC metadata、请求 deadline、trace id、span id 这些东西不是 ECMP 的数据结构。它最多把流量放到某条路径上，不会生成或修改应用语义。

真实客户端 IP 要分两层说。如果客户端到 ECMP 设备之间没有被 CDN、NAT、代理或隧道改写，那么包头里的源 IP 仍然是客户端地址，ECMP 只是转发它。问题是企业入口常常不是这么干净：上游可能先经过 CDN、WAF、NAT 网关、四层负载均衡器、VPN 或隧道封装。到了 ECMP 设备这里，源 IP 可能已经变成边缘代理、NAT 地址或隧道端点。ECMP 不会把“原始客户端 IP”从某个上游私有字段里恢复出来。后面如果需要真实用户地址，还是要靠 `X-Forwarded-For`、`Forwarded`、`X-Real-IP`、PROXY protocol，或者让更前面的代理把可信身份写入请求上下文。

协议上下文也只能看到很粗的一层。普通 ECMP hash 常用三元组或五元组，也就是源/目的 IP、协议号、源/目的端口。有些设备支持把 VXLAN、GRE、Geneve、MPLS 等封装里的 inner header 拿出来做 hash，这能让隧道里的流分布更均匀；但它仍然不知道 HTTP path、Host、Header、Cookie、gRPC 方法名或租户。TLS、QUIC 和 HTTP/2 多路复用还会把更多请求语义藏到连接里面，ECMP 更看不到。

超时不能靠 ECMP 传。ECMP 影响路径、丢包、RTT、队列和重传，从而间接影响上层请求是否超时；但 connect timeout、read timeout、per-try timeout、overall deadline 都在客户端、代理、网关或 RPC 框架里配置。路由收敛期间，如果某条路径被移出 ECMP group，已有 flow 可能被重映射，TCP 连接可能出现乱序、重传甚至断开。这是网络故障影响业务超时，不是 ECMP 在传递 deadline。

追踪上下文同样不在 ECMP 层。W3C Trace Context 定义的是 `traceparent` 和 `tracestate` 这类 HTTP 头，目标是让分布式调用链里的组件继续传递 trace。ECMP 设备既不解析这些头，也不参与 span 生命周期。企业里如果想把 ECMP 选择和业务 trace 对上，一般做法是从网络设备导出 flow telemetry、接口 counters、INT/IOAM 或采样日志，再在入口网关把 `edge_node`、`next_hop_group`、`route_version`、`az`、`rack` 这类标签写进日志或 trace。它是观测关联，不是 ECMP 原生上下文传播。

面试里可以补一句边界：ECMP 保住的是网络转发路径和带宽利用率，真实客户端身份、协议语义、超时预算和 trace 连续性要交给 L4/L7 代理、服务网格、RPC 框架和应用代码。把这些能力误放到 ECMP 上，会导致排障方向跑偏。

## 24. ECMP 如何做健康检查、摘除、恢复和流量预热？

可以先这样答：ECMP 的健康治理对象是路径和下一跳，不是应用实例。它关心某条链路、某个 next hop、某个隧道、某个 BGP/IGP 邻居是否可转发；发现不可用后，把它从等价路径集合里移除；恢复后，再把它加回集合。应用进程是否健康、某个接口是否 5xx、某个后端是否 fail-slow，通常不在 ECMP 自己的判断范围内。

健康检查最基础的是链路和邻居状态。接口 down、光模块故障、BFD down、ARP/ND 解析失败、BGP 邻居断开、OSPF/IS-IS 邻接消失，都应该让对应路径从 ECMP group 里消失。更成熟的网络还会看接口 error、CRC、FEC correction、微突发丢包、队列拥塞、BFD 抖动、隧道端点探测和下一跳可达性。只看路由协议邻居 up 不够，因为链路可以“还活着但已经大量丢包”。

摘除路径时要控制扰动。最简单的做法是路由协议撤掉那条 next hop，转发表重新编程，新的 flow 不再落到坏路径上。风险是传统 hash 在下一跳数量变化后会让大量 flow 重新映射，造成 TCP 乱序、重传和长连接抖动。RFC 2991/2992 讨论的核心问题之一就是多路径选择发生变化时，flow 到 path 的映射会被扰动。工程上常用 resilient ECMP、consistent hashing、flowlet switching 或厂商的 resilient hashing，让故障路径上的 flow 迁走，其他 flow 尽量不动。

恢复时不要只追求“马上加回来”。链路刚恢复、BGP/IGP 刚收敛、隧道刚建好、交换芯片刚下发表项时，路径质量可能还不稳定。更稳的做法是带迟滞：连续健康一段时间后再加入 ECMP group，先观察接口错误、BFD 稳定性、队列和丢包，再完全参与转发。对于会引起大规模 flow 重映射的场景，还要选择低峰窗口或开启 resilient ECMP，避免恢复动作本身制造抖动。

ECMP 的“预热”主要是网络层预热，不是应用层预热。它包括 BGP/IGP 邻接稳定、ARP/ND 表项准备、隧道端点可达、转发表下发完成、ACL/QoS/ECMP group 编程完成、监控和采样链路就绪。它不能替代后端服务的缓存预热、连接池预建、JIT 预热或限流半开。若 ECMP 后面接的是一组 L4/L7 负载均衡器，还要让那些负载均衡器先接入少量真实流量，再让上游 ECMP 全量分摊。

最后要考虑容量。四条等价路径坏一条后，剩余路径要承载更多流量；恢复路径加入后，也可能突然吸走大量 flow。健康检查、摘除、恢复都要和容量水位、故障域和变更窗口绑定。好的回答不要把 ECMP 说成请求级调度器，它是路径级的快速故障规避机制。

## 25. ECMP 的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：ECMP 的观测要分路由面、转发面、路径分布和业务侧影响四块看。它出问题时，应用层常看到的是偶发超时、长尾延迟、连接重置或某些地域慢；真正原因可能是某条等价路径拥塞、hash 分布不均、链路丢包、路由 flap 或设备转发表更新慢。

路由面要看 ECMP group 里有多少条 next hop、每条 next hop 的状态、BGP/IGP 邻居状态、BFD 会话状态、路由增删次数、收敛时间、路由策略版本、FIB 编程失败和 route churn。尤其要看“控制面以为有几条路径”和“数据面真正安装了几条路径”是否一致。有些故障不是路由协议没算出来，而是硬件资源、ACL、隧道或 next-hop resolution 导致数据面没装上。

转发面要看每条链路或下一跳的 bps、pps、flow 数、新建 flow 速率、队列深度、buffer drop、ECN mark、CRC/FEC error、接口 discard、microburst、ASIC drop、TCAM/flow table 资源、隧道封装/解封装错误。对服务器侧还要看 NIC 队列、RSS 分布、softirq、XDP/TC drop、conntrack 压力。ECMP 的瓶颈经常不是“算法慢”，而是某条路径、某个队列或某块转发芯片被打满。

路径分布要看 hash 是否均匀。正常情况下，大量小 flow 应该在下一跳之间近似分散；如果某条 path 长期承载远高于平均值的流量，要查 hash key、seed、隧道封装、NAT 汇聚、单一大客户出口、elephant flow、LAG hashing 和上游路径选择。ECMP 对 elephant flow 很敏感，一条大流可能把某条链路打满，而其他链路还很空。这种场景不能指望 ECMP 自动把一条 TCP 流拆到多条路径上。

业务侧要把网络指标和连接质量对起来。重点看 TCP retransmission、SYN retry、RTO、RTT 分位数、连接建立失败率、TLS handshake time、HTTP/gRPC p95/p99、特定 AZ/rack/ToR/Spine 的错误率。如果应用错误只集中在某个网络故障域，而服务进程本身健康，就要优先怀疑 ECMP 路径、链路或下一跳。

判断 ECMP 自身成为瓶颈，可以看几个信号。第一，少数路径利用率接近线速，其他路径空闲，说明 hash 或上游流量结构失衡。第二，ECMP group 频繁增删，业务长连接随之重传或断开。第三，路由面正常但数据面丢包，说明 FIB、ASIC、队列、ACL、隧道或 NIC 成了问题。第四，某些五元组稳定慢，换源端口或换客户端后恢复，说明 hash 落点有问题。第五，故障摘除后剩余路径瞬间过载，说明容量没有按 N+1 或故障域设计。面试里最好强调：ECMP 的观测不能只看平均带宽，必须看每条路径、每类 flow、每个故障域和业务尾延迟。

## 26. LVS/IPVS 位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：LVS 是 Linux Virtual Server，IPVS 是它在 Linux 内核里的四层负载均衡实现。它通常位于企业流量链路的 L4 入口转发层，用一个 VIP 加端口和协议表示 virtual service，再把连接或数据报调度到一组 real server。它主要解决入口流量的高性能四层分发，也可以用于内网东西向 VIP，但它不是按 HTTP path、Header、Cookie 或 gRPC 方法做决策的应用路由组件。

IPVS 的位置比 Nginx、Envoy、API Gateway 更低。客户端访问 VIP，director 根据 TCP/UDP/SCTP 等协议、端口和调度算法选择 real server。Keepalived 文档把 IPVS 描述成 Linux 内核里的 transport-layer load balancing，也就是常说的 Layer 4 switching。`ipvsadm` 的手册也能看到它围绕 virtual service、real server、scheduler、packet-forwarding method 和 weight 来配置，这些概念都在连接和包转发层。

它常见的三种转发模式是 NAT、TUN 和 DR。NAT 模式里 director 处理请求和响应路径，部署简单，但 director 更容易成为瓶颈。TUN 和 DR 模式通常让请求经过 director，响应由 real server 直接回客户端，扩展性更好，但对网络拓扑、源地址、ARP、VIP 绑定和路由配置要求更高。Keepalived 的 Load Balancing Techniques 文档也把 NAT 的瓶颈和 Direct Routing 的扩展性边界讲得很清楚。

入口场景里，LVS/IPVS 常放在公网入口、数据中心入口、四层 SLB、Nginx/Envoy 集群前面，承担高并发 TCP/UDP 分流。东西向场景里，它也可以作为内部 VIP 或 Kubernetes kube-proxy 的 IPVS 后端，用来把服务虚拟地址分发到 Pod 或实例。两种场景的共同点是：它处理的是服务地址到后端地址的 L4 映射，而不是应用语义。

面试里要避免把 IPVS 说成“万能网关”。它不终止 TLS，不解析 HTTP Header，不做 JWT 验证，不理解 REST/gRPC 路由，不直接做流量镜像、灰度规则和租户级限流。真正的应用路由一般在 Nginx、Envoy、Ingress、API Gateway、Service Mesh 或业务客户端里完成。LVS/IPVS 更像企业流量架构里的高性能四层底座。

## 27. LVS/IPVS 在高可用设计中如何避免单点故障？

可以先这样答：LVS/IPVS 的单点主要有两个：director 自己和它管理的 VIP。高可用设计通常用多台 director 加 VRRP/Keepalived 漂移 VIP，用 IPVS connection synchronization 尽量保留连接状态，再用多 real server、健康检查、配置一致性和容量冗余避免后端单点。只部署一台 Linux director，即使后面有十台 real server，也仍然是入口单点。

第一层是 VIP 和 director failover。典型做法是两台或多台 director 运行 Keepalived，通过 VRRP 选出 MASTER，由 MASTER 持有 VIP；BACKUP 监听 VRRP advertisement。主节点故障或健康检查失败时，VIP 漂到备节点。NGINX 的 HA 文档也用 Keepalived 解释过类似模式：VRRP 确保总有一个 primary 持有 VIP，服务健康失败后可以把 VIP 重新分配给 backup。LVS/IPVS 场景也是这个思路。

第二层是连接状态。四层负载均衡有连接表，主备切换时如果备节点完全不知道已有连接，老连接可能断掉。`ipvsadm` 支持启动 connection synchronization daemon，由 master 把连接变化同步给 backup，备节点接管后可以继续服务大部分已建立连接。这里要讲边界：连接同步不是分布式事务，也不是所有协议状态都能无损迁移；UDP、短连接、高速连接 churn、同步丢包和超大连接表都会影响接管质量。

第三层是避免 split-brain。VRRP 网络隔离、组播/单播配置错误、优先级配置错误、脚本误判，都可能让两台 director 同时认为自己是 MASTER，结果 VIP 冲突、ARP 抖动、流量来回跳。生产里要做唯一 `virtual_router_id`、明确 priority、认证或安全隔离、`track_script`、告警、必要时加 fencing 或上游路由约束。VIP 漂移后还要处理 ARP/GARP 刷新，否则上游交换机或客户端可能继续把流量发给旧 MAC。

第四层是配置和健康检查一致。两台 director 的 IPVS 规则、scheduler、real server 列表、weight、persistence、timeout、sysctl、内核版本都要一致或有明确差异。很多切换事故不是 VRRP 没切，而是备节点配置旧、证书旧、路由旧、防火墙旧，切过去以后能收包但不能正常转发。

第五层是容量冗余和故障域。主备模式下，备节点平时不接流量，但它必须有承载全量流量的能力；主主或 active-active 模式下，每个节点也要按 N+1 设计。real server 不能和 director 共用同一个单点上游、电源、交换机或存储依赖。面试里可以总结：LVS/IPVS 的 HA 不是一个 Keepalived 配置块就结束了，而是 VIP 漂移、连接同步、配置一致、ARP/路由收敛、健康检查和容量规划一起成立。

## 28. LVS/IPVS 与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：LVS/IPVS 是四层转发器，它能保留或改写 IP/端口层信息，但不会主动生成 HTTP Header、RPC metadata、trace header 或业务 deadline。真实客户端 IP 是否能被下游看到，取决于上游有没有先隐藏它、IPVS 使用哪种转发模式、链路里有没有 SNAT、隧道或代理。应用层上下文要靠更上层的代理和框架传。

真实客户端 IP 最容易被问细。DR 和 TUN 模式通常更容易让 real server 看到原始源地址，因为请求包的源地址没有被改成 director 地址，响应也可以直接回客户端。NAT 或 masquerading 模式下，director 会参与地址转换和回包路径；具体是否保留客户端源地址，还要看拓扑、iptables/nftables、SNAT、云厂商网络和 Kubernetes 等实现。更稳的回答是：IPVS 本身不提供类似 `X-Forwarded-For` 的应用层身份字段，如果链路需要可审计的真实客户端 IP，应由可信 L7 代理写入 Header，或在 L4 使用 PROXY protocol，但这需要前后组件都支持。

协议传递也分层。IPVS 支持按 TCP、UDP、SCTP 等传输层协议建 virtual service，`ipvsadm` 手册里也把 TCP/UDP service、firewall mark service、scheduler 和 forwarding method 作为核心配置项。它不会解析 HTTP/2 stream、gRPC method、WebSocket 消息或 TLS 里的应用数据。TLS 如果在 real server 终止，IPVS 看不到证书、SNI 之后的 HTTP 内容；如果 TLS 在前置 Nginx/Envoy/CDN 终止，IPVS 后面看到的可能只是代理到后端的连接。

超时方面，IPVS 有连接状态和协议超时，例如 TCP、TCP FIN、UDP 等连接表超时，也有 `/proc/sys/net/ipv4/vs/*` 里的若干 sysctl。它们控制的是内核连接表生命周期、防御策略、连接复用和同步行为，不是业务请求 deadline。一个 HTTP 请求的 `proxy_read_timeout`、gRPC deadline、客户端 overall timeout，不能指望 IPVS 传给后端。

追踪上下文更不属于 IPVS。`traceparent`、`tracestate`、`x-request-id` 这些信息在 HTTP/RPC 层，IPVS 不读也不改。实践里可以让前置网关生成 trace，并把 `vip`、`director`、`real_server`、`scheduler`、`conn_reused` 等网络标签写入访问日志或 metrics；如果要把 IPVS 选中的 real server 和业务 trace 对齐，通常靠 director 日志、`ipvsadm --stats/--rate`、eBPF flow 观测和应用日志做关联。

面试里可以把边界说得直接一点：LVS/IPVS 负责把连接从 VIP 调度到 real server；它能影响源地址是否保留、协议是否可转发和连接状态如何老化，但不会替应用层传 Header、deadline 和 trace。需要这些上下文时，要在 IPVS 上下游配 Nginx、Envoy、HAProxy、Service Mesh 或 RPC 中间件。

## 29. LVS/IPVS 如何做健康检查、摘除、恢复和流量预热？

可以先这样答：IPVS 内核负责按配置调度 virtual service 和 real server，本身不是完整的应用健康检查系统。生产里通常由 Keepalived、监控控制面或自研 agent 做 TCP、HTTP、SSL、脚本等健康检查，再通过 netlink/libipvs/ipvsadm 修改 real server 的状态、权重或是否存在。也就是说，健康判断在用户态，转发执行在内核态。

健康检查要分层。最浅的是 TCP 端口能不能连。稍深一点是 HTTP/HTTPS 健康路径是否返回预期状态码或内容。再往下要看应用关键依赖、连接池、队列、水位、错误率和尾延迟。Keepalived 的设计里，checker 会根据 L4 到 L7 的检查结果把 real server 从 LVS topology 里移除或加回；它支持 `TCP_CHECK`、`HTTP_GET`、`SSL_GET`、`MISC_CHECK` 等方式。面试里要指出：只做 TCP check 很容易把 fail-slow 或依赖故障的节点留在池子里。

摘除有几种粒度。直接删除 real server 最彻底，新连接不会再调度过去，但已有连接怎么处理要看连接表和转发模式。把 real server weight 设为 0 更像 quiesce，`ipvsadm` 手册把 weight 0 描述成不接新 job 但继续服务已有 job，这适合维护和优雅下线。还有 upper/lower connection threshold，可以让连接数超过阈值时停止分配新连接。对于有 persistence 的服务，要注意持久化模板可能继续把同一客户端导向原 real server，需要配合 `expire_quiescent_template` 或策略调整。

恢复要避免“刚健康就满血接流量”。IPVS 可以改 weight，所以常见做法是恢复初期给低权重，连续健康后逐步升权。Keepalived 配置里可以用 `rise`、`fall`、`retry`、`delay_before_retry` 之类的机制降低抖动；更复杂的慢启动要靠外部控制面按时间或指标调权。恢复时还要看应用缓存、数据库连接、线程池、JIT、页缓存、TLS session、下游限流是否准备好。健康检查通过只说明它能回答探测，不说明它能立刻吃完整流量。

连接排空也要认真处理。下线 real server 时，不要一边把它从 IPVS 里删掉，一边直接停进程。更稳的是先把 weight 降到 0 或从新连接候选里摘除，等待 active connection 降下来，再停服务。对长连接、WebSocket、数据库连接、TCP keepalive、UDP 会话和 persistence 场景，drain 时间要按业务语义设。`expire_nodest_conn` 这类 sysctl 可以改变目的 real server 不存在时连接如何处理，但它是故障兜底，不是优雅下线的替代。

流量预热同样主要发生在 real server 和上游控制面。IPVS 层可以做低权重、少量连接、分批加回；real server 要预热缓存、连接池、依赖、热点数据和应用 runtime。director 自己也要有预热：IPVS 规则加载、连接同步、conn table 容量、SNMP/metrics、日志、ARP/VIP 状态、路由和防火墙都要就绪。否则节点还没稳定，健康系统就把它加回，会形成摘除和恢复的循环。

## 30. LVS/IPVS 的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：LVS/IPVS 的观测要覆盖 virtual service、real server、director 内核转发、连接表、HA 状态和上下游链路。它成为瓶颈时，表现可能是新连接失败、连接建立慢、丢包、重传、active connection 堆积、NAT 回包路径打满，或者 director CPU softirq 飙高。

服务层指标包括每个 VIP:port/protocol 的连接数、connections per second、packets/second、bytes/second、入出方向流量、active/inactive connection、real server 分布、scheduler 命中比例、weight、upper/lower threshold、persistence 命中、错误和重试。`ipvsadm --stats`、`--rate`、`--connection`、`--thresholds` 以及 `/proc/net/ip_vs`、`/proc/net/ip_vs_conn`、`/proc/net/ip_vs_stats` 是最基础的入口。

director 内核层要看 CPU softirq、ksoftirqd、NIC RX/TX queue、ring buffer drop、RSS/RPS/XPS 分布、GRO/LRO、conntrack 是否被打开、IPVS connection hash table、内存、`sync_qlen_max`、同步消息发送和接收、sysctl 防御策略触发、iptables/nftables 规则开销。NAT 模式还要看回包方向，因为请求和响应都经过 director，带宽和包率压力通常比 DR/TUN 更高。

HA 层要看 Keepalived/VRRP 状态、MASTER/BACKUP 切换次数、VIP 当前在哪台机器、GARP 发送、脚本健康结果、`rise/fall` 计数、split-brain 告警、connection sync daemon 状态、同步延迟和丢包。Keepalived 的 SNMP 支持里也把 VRRP、check、virtual server、real server 和连接统计作为可观测对象。只看 IPVS 转发表，不看 VRRP 状态，很容易漏掉入口漂移问题。

上下游侧要看 real server 的连接接受率、SYN backlog、应用 p95/p99、5xx、TCP retransmission、RST、源地址分布、回包路径是否对称、MTU/PMTU、ARP 问题和防火墙丢包。DR 模式尤其要关注 ARP 和 VIP 配置，TUN 模式要关注隧道 MTU 和封装开销，NAT 模式要关注 director 的双向吞吐和 conntrack/状态表。

判断 IPVS 自身成为瓶颈，可以看几个信号。第一，real server 空闲，但 director 的 CPU softirq、NIC drop 或 PPS 已经到顶。第二，NAT 模式下回包方向打满，换成 DR/TUN 或增加 director 后明显改善。第三，connection table、hash 冲突或同步队列异常导致新连接延迟升高。第四，单个 VIP 的 active/inactive connection 分布严重倾斜，scheduler 或 persistence 把流量粘住。第五，VRRP 切换时连接大面积断开，说明连接同步、ARP/GARP 或备节点配置没有跟上。面试里最好把排障路径讲成“先看 director 是否丢包，再看 scheduler 分布，再看 real server 和上下游路径”，不要只看应用日志。

## 31. Nginx 位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：Nginx 通常位于企业流量链路的七层反向代理、入口网关或应用负载均衡层。它最常见的职责是接收 HTTP/HTTPS 请求，做 TLS 终止、虚拟主机、路径和 Header 路由、反向代理、静态资源、缓存、压缩、限流、访问控制和上游负载均衡。它主要解决入口流量和应用路由问题，也可以用于东西向调用，但它不是最底层的网络 ECMP 或纯 L4 IPVS。

从链路位置看，Nginx 往往在 DNS/GSLB/CDN/BGP Anycast/LVS 之后，在应用服务之前。外部用户先被全局调度或四层入口送到某个区域，再由 Nginx 根据 `server_name`、`location`、Header、URI、方法、变量和 upstream 配置把请求发给后端。NGINX 官方 HTTP Load Balancing 文档也明确把它放在 Layer 7 HTTP/HTTPS 负载均衡位置，支持 Round Robin、Least Connections、hash、IP Hash 等方法。

它和 LVS/IPVS 的差异要讲清楚。LVS/IPVS 通常按 VIP、端口、协议和连接调度，吞吐高、开销低，但不理解 HTTP 语义。Nginx 能看到 HTTP 请求，所以能做 `/api` 和 `/static` 分流、按 Host 路由、按 Header 灰度、按状态码重试、缓存、压缩和细粒度访问日志。代价是它终止或代理应用协议，CPU、内存、worker、连接池、buffer、TLS 和配置复杂度都会进入请求路径。

东西向流量里也能用 Nginx，例如内部 API 网关、服务反向代理、Kubernetes Ingress Controller、边车代理或服务前置代理。但如果企业已经采用 Service Mesh，东西向细粒度治理更多会落到 Envoy/Istio/Linkerd 或 RPC 框架；Nginx 常用于边界入口和少量明确的内部网关。不要把“能代理内部 HTTP”扩展成“所有东西向治理都靠 Nginx”。

应用路由是 Nginx 的强项，但也有边界。它适合基于 HTTP 可见信息做规则匹配，不适合把复杂业务决策都塞进配置。涉及用户权限、订单状态、复杂租户策略、动态实验分桶、跨服务事务时，Nginx 最多做粗路由和网关保护，真正决策还要在应用、控制面或专门的 API Gateway 里。面试里可以用一句话收束：Nginx 是 L7 入口和反向代理组件，适合把“看得见的协议语义”转成上游选择和网关行为。

## 32. Nginx 在高可用设计中如何避免单点故障？

可以先这样答：Nginx 避免单点故障，不能只在一台机器上把 worker 数调大。正确做法是多实例、多故障域部署，并让上游 DNS/GSLB/Anycast/LVS/云负载均衡器把流量分到多台 Nginx；如果是自建机房，也可以用 Keepalived/VRRP 漂移 VIP 做 active-passive 或 active-active。Nginx 本身要支持优雅 reload、健康检查、配置同步、容量冗余和故障演练。

第一层是数据面多实例。至少两台 Nginx，最好跨可用区、机架、宿主机和电源域。入口可以是云 LB、LVS/IPVS、BGP Anycast、DNS/GSLB 或硬件负载均衡器。单台 Nginx 再强，也扛不住主机故障、内核故障、网卡故障、证书文件损坏或误操作。多实例之后还要按 N+1 容量设计：少一台时，剩余实例不能立刻过载。

第二层是 VIP 或上游调度的 HA。On-prem 常见做法是 Nginx 加 Keepalived，VRRP 控制浮动 VIP。NGINX 官方 HA 文档里也介绍了基于 Keepalived 的 active-passive 方案，主节点健康失败时 VIP 可以切到备节点。云环境里通常不直接漂移本机 VIP，而是把多个 Nginx 放进云负载均衡 target group，由云控制面做健康检查和流量切换。两种方式的关键一样：上游必须能快速停止把新流量送到坏实例。

第三层是进程和配置可靠性。Nginx master/worker 模型支持平滑 reload，但配置错误会让 reload 失败，或者新配置加载后把业务路由打错。生产发布要先 `nginx -t` 校验，再灰度到少量实例，观察 4xx/5xx、upstream error、连接数、TLS 握手和延迟后再全量。配置、证书、Lua/njs 脚本、WAF 规则、限流规则、upstream 列表都要版本化和可回滚。多台 Nginx 如果全用同一份坏配置，也只是把单点故障变成批量故障。

第四层是上游和状态。Nginx 后面的 upstream 要有多个后端、健康检查、`max_fails`、`fail_timeout`、backup server、连接池和合理 timeout。Open Source 版本主要依赖被动失败检测和外部健康系统；NGINX Plus 提供主动健康检查、slow-start、API 动态配置、状态共享等能力。无论用哪个版本，都要避免 Nginx 自己把故障后端反复打穿，也要避免刚恢复的后端被瞬间打满。

第五层是本地资源保护。要看 `worker_processes`、`worker_connections`、`worker_rlimit_nofile`、accept queue、listen backlog、TLS session cache、proxy buffers、磁盘缓存、临时文件目录、日志磁盘、DNS resolver、上游 keepalive、文件描述符和端口耗尽。很多 Nginx 事故不是进程死了，而是文件描述符满、日志磁盘满、缓存盘抖、DNS 解析卡住、上游连接池耗尽，最后表现成入口 502/504。

最后是演练和观测。要定期测试单实例下线、配置回滚、证书轮换、上游后端全挂、DNS 解析失败、VIP 切换、云 LB 健康检查误判和机房级故障。观测上至少要有请求量、状态码、upstream response time、request time、upstream connect/header/response time、active/reading/writing/waiting、TLS handshake、reload 结果、worker 退出、文件描述符、CPU、内存和磁盘。面试里可以总结：Nginx 高可用靠的是“多入口实例加可靠发布”，不是某个单独指令。

## 33. Nginx 与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：Nginx 是七层代理时，可以通过 HTTP Header、PROXY protocol、日志变量和上游连接配置传递一部分上下文；但它不会自动保证这些上下文可信、完整、端到端一致。真实客户端 IP、协议、超时和 trace 都要明确配置，并且要区分“从上游接收”和“向下游传递”两个方向。

真实客户端 IP 有两种常见路径。第一种是 Nginx 前面还有 CDN、L4 LB、HAProxy、云 LB 或另一个 Nginx，这时 Nginx 看到的 TCP peer 可能只是上一跳代理。要恢复真实客户端地址，需要启用 `realip` 模块，配置 `set_real_ip_from` 信任哪些上游，再用 `real_ip_header X-Forwarded-For`、`X-Real-IP` 或 `proxy_protocol` 取地址。这里最容易出错的是信任边界：不能让任意公网客户端直接伪造 `X-Forwarded-For`。第二种是 Nginx 向后端传递客户端地址，常见配置是 `proxy_set_header X-Real-IP $remote_addr` 和 `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for`。如果后端支持 RFC 7239 的 `Forwarded`，也可以统一用 `for`、`proto`、`host` 这些参数，但企业里 `X-Forwarded-*` 仍然更常见。

协议上下文主要靠 Host、scheme、ALPN、SNI 和转发头。Nginx 终止 TLS 后，后端默认只看到 Nginx 到后端的连接，不知道客户端原来是 HTTP 还是 HTTPS，所以通常要显式传 `X-Forwarded-Proto $scheme`、`X-Forwarded-Host $host`、`Host $host`。如果要支持 WebSocket，需要处理 `Upgrade` 和 `Connection`；如果是 gRPC，则要区分 `grpc_pass`、HTTP/2、TLS 终止位置和上游是否也走 h2。Nginx 能转发协议语义，但不会替业务判断“这个 Header 是否可信”，后端应该只信任来自受控代理链的这些字段。

超时不能只靠一个参数。Nginx 到上游至少有 `proxy_connect_timeout`、`proxy_send_timeout`、`proxy_read_timeout`；其中 `proxy_read_timeout` 是两次读操作之间的空闲超时，不是整个响应必须在这个时间内完成。客户端侧还有 `client_header_timeout`、`client_body_timeout`、`keepalive_timeout`，重试侧还有 `proxy_next_upstream_timeout` 和 `proxy_next_upstream_tries`。端到端 deadline 最好由客户端或网关生成，通过业务 Header、gRPC deadline 或 `grpc-timeout` 继续往后传，Nginx 自己的 timeout 只是保护代理资源，不等价于业务调用预算。

追踪上下文建议按 W3C Trace Context 传 `traceparent` 和 `tracestate`，同时保留企业常用的 `x-request-id` 或 `x-correlation-id`。Nginx Open Source 通常是“接收什么就转发什么”，也可以用变量、`map`、njs、Lua 或 OpenTelemetry 模块生成和补齐请求 ID。关键是不要在每一跳都重新生成完全无关的 ID，否则 trace 会断；也不要无条件信任外部传入的 trace 字段，入口层要按安全策略决定是否接受、重写或采样。

面试里可以补一句：Nginx 的上下文传递是配置能力，不是默认语义。能不能拿到真实 IP、能不能保持 HTTPS 语义、能不能把 deadline 和 trace 连起来，取决于代理链的信任边界和每一跳的 Header/PROXY protocol 约定。

## 34. Nginx 如何做健康检查、摘除、恢复和流量预热？

可以先这样答：Nginx 的健康治理分 Open Source 和 NGINX Plus 两种能力。Open Source 主要依赖被动健康检查，也就是请求转发失败后按 `max_fails` 和 `fail_timeout` 暂时把 upstream server 标为不可用；NGINX Plus 支持主动健康检查，可以周期性请求健康 URI，并根据状态码、Header、Body 等条件判断上游是否健康。生产里常常还会配合外部服务发现、Ingress Controller、配置发布系统或云负载均衡器一起做摘除和恢复。

被动健康检查适合发现连接失败、超时、上游返回不可重试错误这类运行时问题。`max_fails` 控制在 `fail_timeout` 窗口内失败多少次才认为不可用，`fail_timeout` 同时也控制服务器被认为不可用的时间。这里要讲边界：被动检查只有真实请求打到坏节点后才知道失败，所以它会让一部分用户请求承担探测成本；如果只有一个 upstream server，很多失败参数也没有意义，因为 Nginx 没有别的节点可选。

主动健康检查更适合提前发现故障。NGINX Plus 的 `health_check` 可以配置 URI、interval、fails、passes 和 match 条件。比较稳的健康接口不应该只返回进程存活，而要覆盖关键依赖、只读/写入状态、队列水位、下游连接池和版本就绪状态。健康检查也不能太重，否则探测本身会压垮后端；也不能太宽松，否则数据库挂了还继续返回 200，入口层就会把流量送进故障域。

摘除时要先停新流量，再等已有请求和连接排空。做法可以是从 upstream 配置中移除节点、标记 `down`、降低权重、让服务发现不再返回该 endpoint，或者在 NGINX Plus/API/Ingress 控制面里动态摘除。对长连接、WebSocket、SSE、gRPC streaming，要给足 drain 时间。直接杀后端进程会让 Nginx 看到大量 502/504，再触发重试和错误放大。

恢复和预热要慢一点。NGINX Plus 的 slow start 可以让刚恢复的 upstream 权重从 0 逐步回到正常值，避免恢复节点一通过健康检查就被打满。Open Source 环境通常要靠外部控制面分批加回、先给低权重、先放小流量，或者先只让一部分 Nginx 实例看到新节点。后端自身要预热缓存、连接池、TLS session、JIT、热点数据和依赖连接。入口层还要观察 5xx、upstream response time、upstream connect time、队列和 p99，确认它不是“探测健康，真实流量不健康”。

最后要把重试纳入健康治理。`proxy_next_upstream` 可以在一些错误条件下换上游再试，但重试没有预算会放大故障。摘除、恢复和预热的目标不是让所有请求都“尽力再试”，而是让坏节点尽快少接新请求、恢复节点慢慢接流量、整个集群在故障期间保持可预测。

## 35. Nginx 的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：Nginx 的观测要同时看入口请求、连接状态、上游行为、本机资源和配置变更。只看业务 5xx 不够，因为 Nginx 成为瓶颈时，表现可能是 499、502、503、504、连接排队、TLS 握手慢、worker 连接打满、临时文件写盘、DNS resolver 卡住，或者 upstream 正常但入口 p99 飙升。

入口侧要看请求量、状态码分布、4xx/5xx、499、请求大小、响应大小、TLS 握手、HTTP/2 stream 数、连接复用率、每个 server/location/upstream 的 QPS 和延迟。访问日志里建议保留 `$request_time`、`$upstream_connect_time`、`$upstream_header_time`、`$upstream_response_time`、`$upstream_status`、`$upstream_addr`、`$request_id`。这些字段能把“客户端慢、Nginx 慢、上游慢”拆开。

连接侧可以用 `stub_status` 或 NGINX Plus API 看 active、reading、writing、waiting、accepted、handled、requests。`handled` 明显低于 `accepts`，常见原因是 worker 连接、文件描述符或系统资源不足；reading 长期高可能是慢请求头或客户端慢；writing 高可能是客户端接收慢、响应大或网络拥塞；waiting 高不一定坏，可能只是 keepalive 多，但要结合 worker_connections 和内存看。

上游侧要看每个 upstream server 的连接数、失败次数、重试次数、响应时间、健康状态、被摘除次数、max_fails/fail_timeout 触发、队列或 max_conns 命中。若 Nginx 到上游的 connect time 高，可能是上游 accept backlog、网络、DNS、连接池或端口耗尽；若 header time 高，通常是上游应用处理慢；若 response time 高但 header time 正常，可能是大响应、客户端慢或上游流式输出。

本机资源要看 CPU、worker 进程、文件描述符、`worker_connections`、listen backlog、accept queue、epoll/kqueue、内存、临时文件目录、缓存盘、日志盘、TLS session cache、证书加载、DNS resolver、端口范围和 TIME_WAIT。开启 gzip、brotli、WAF、Lua/njs、镜像流量、大量正则 location 或复杂 map 后，Nginx 自己的 CPU 也可能成为瓶颈。

判断 Nginx 自身成为瓶颈，可以看几个信号。第一，上游服务空闲，但 Nginx worker CPU、FD、连接数或 accept queue 打满。第二，`handled < accepts` 或错误日志里出现 `worker_connections are not enough`、`too many open files`。第三，`upstream_*` 时间不高但 `$request_time` 高，说明时间耗在客户端侧、Nginx buffering 或本机资源上。第四，reload、证书轮换、DNS 解析或日志写盘期间延迟抖动。第五，横向增加 Nginx 实例后入口延迟下降，而上游指标变化不大。这个时候瓶颈基本在入口代理层。

## 36. HAProxy 位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：HAProxy 是高性能 TCP/HTTP 代理和负载均衡器，既能工作在四层，也能工作在七层。放在企业流量链路里，它常见位置是公网入口、区域入口、内部 API 网关、服务前置代理、数据库或消息队列的 TCP 入口，以及 Nginx/Envoy/应用服务前面的负载均衡层。它可以解决入口流量，也可以解决东西向流量；在 `mode http` 下还能做一部分应用路由。

HAProxy 的基本模型是 frontend 接收连接，backend 表示一组后端，`listen` 可以把两者写在一起。`mode tcp` 下它更像四层代理，主要看连接、SNI、源地址、目标端口、TCP payload 早期字节和 stick table；`mode http` 下它能解析 HTTP 请求，使用 ACL、Header、path、method、Host、Cookie、状态码等做路由、重写、限流和负载均衡。官方配置手册里 `mode { tcp|http|log }` 的区分，就是回答这道题的核心。

入口流量里，HAProxy 常用于 TLS 终止、HTTP 路由、灰度、限流、WAF 前置、API 分流、后端池健康检查和粘性会话。东西向流量里，它可以作为内部服务 VIP、数据库读写入口、缓存集群入口、跨机房服务代理，或者老系统里比 Service Mesh 更轻的统一代理层。它的强项是稳定、性能高、可观测和运行时控制能力强。

但 HAProxy 不是 DNS/GSLB，也不是 ECMP。它不负责把全球用户解析到哪个地域，也不负责交换机层面的等价路径分担。它也不是完整业务网关，复杂鉴权、订单状态、租户策略、工作流编排仍然应该在应用或专门控制面里。HAProxy 能按可见协议字段做应用路由，但不要把所有业务逻辑都塞进 ACL 和配置文件。

一个清晰的链路说法是：DNS/GSLB/Anycast 先决定用户进哪个地域，LVS/IPVS 或云 LB 可以把连接分到多台 HAProxy，HAProxy 再按 TCP/HTTP 语义选择后端池，后面才是 Nginx、Envoy、Service Mesh 或应用服务。具体放哪一层，取决于它用 `mode tcp` 还是 `mode http`，以及企业希望它承担入口治理还是内部代理。

## 37. HAProxy 在高可用设计中如何避免单点故障？

可以先这样答：HAProxy 本身是单进程或多线程代理实例，避免单点故障要靠多实例部署和上游调度，而不是指望一台 HAProxy 永远不挂。常见方案是多台 HAProxy 置于云负载均衡器、LVS/IPVS、BGP Anycast、DNS/GSLB 或 Keepalived/VRRP VIP 后面；每台 HAProxy 内部再用健康检查、连接限制、运行时管理和优雅 reload 保持稳定。

第一层是实例冗余。至少两台 HAProxy，跨主机、机架、可用区和网络故障域。active-passive 可以用 Keepalived/VRRP 漂移 VIP，active-active 可以让上游 L4/L7 LB 或 Anycast 同时分流。active-passive 简单，但备节点必须能接全量流量；active-active 资源利用率更高，但要处理会话粘性、stick table 同步、配置一致和故障后容量重分布。

第二层是进程和 reload。HAProxy 支持 master-worker 模式和优雅 reload，生产发布要先做配置校验，再让新 worker 接流量，老 worker 排空已有连接。长连接、WebSocket、TCP tunnel 和数据库连接会让老 worker 存活很久，所以要设置合理的 drain 策略和 `hard-stop-after`，也要监控 reload 后旧进程数量。配置发布不能一次把所有实例打坏，必须灰度。

第三层是运行时状态。HAProxy 可以通过 Runtime API 调整 server 状态、权重、maxconn，也可以保存和加载 server state，避免 reload 后把原本处于维护、降权或失败状态的节点突然恢复成全量。stick table 如果用于限流、会话保持或反滥用，要用 peers 同步，否则切到另一台 HAProxy 后状态丢失，限流和粘性都会变形。

第四层是后端池高可用。每个 backend 要有多台 server，配置 `check`、`inter`、`fall`、`rise`、`backup`、`slowstart`、`maxconn`、`maxqueue` 等保护。上游 HAProxy 还要处理所有 server 都不可用时的策略：返回错误、维护页、只读后端、降级后端，还是把流量交给另一个地域。不要让 HAProxy 在后端全挂时无限排队。

第五层是共享依赖。证书、DNS resolver、配置仓库、Runtime socket、日志系统、Lua/SPOE 外部服务、OCSP、系统 ulimit、内核参数都可能变成单点。面试里可以总结：HAProxy 高可用不是“开两个进程”，而是多实例入口、优雅发布、运行时状态保留、后端健康治理和共享依赖隔离一起做。

## 38. HAProxy 与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：HAProxy 传递上下文的方式取决于它运行在 `mode tcp` 还是 `mode http`。HTTP 模式可以增删 Header，例如 `X-Forwarded-For`、`Forwarded`、`X-Forwarded-Proto`、`X-Request-ID`、`traceparent`；TCP 模式看不到完整 HTTP 语义，通常靠 PROXY protocol 把源地址、目标地址和 TLS 相关 TLV 传给下游。

真实客户端 IP 在 HTTP 模式下最常用 `option forwardfor`，它会向发给后端的请求追加 `X-Forwarded-For`，值是客户端 IP。新一些的配置也可以用 `option forwarded` 生成 RFC 7239 风格的 `Forwarded` Header。前提仍然是信任边界：如果 HAProxy 前面还有 CDN 或 L4 LB，要先确认上一跳传来的地址可信，再决定是覆盖、追加还是清洗旧 Header。TCP 模式下，后端如果需要知道源地址，常用 `send-proxy` 或 `send-proxy-v2`，后端监听端也必须支持 PROXY protocol，否则会把协议头当业务数据读坏。

协议上下文包括 scheme、Host、SNI、ALPN 和入口端口。TLS 在 HAProxy 终止时，可以向后端写 `X-Forwarded-Proto https`、`X-Forwarded-Host`、`X-SSL-*` 等内部约定，也可以通过 PROXY protocol v2 TLV 传递部分 SSL 信息。TLS 如果透传，HAProxy 只能基于 SNI 或早期字节做有限路由，看不到 HTTP Header。HTTP/2、HTTP/1.1、WebSocket、gRPC 的处理也要看 bind/server 侧 ALPN、backend 协议和是否做协议转换。

超时要分段配置。HAProxy 常见的是 `timeout connect`、`timeout client`、`timeout server`、`timeout http-request`、`timeout queue`、`timeout tunnel`、`timeout check`。这些 timeout 保护的是 HAProxy 与客户端、队列和后端之间的连接生命周期，不是天然的业务 deadline。真正端到端 deadline 应由调用方或入口网关通过 Header 或 RPC metadata 传给后端；HAProxy 可以保留这些字段，也可以在需要时用 `http-request set-header` 补充内部超时预算，但要避免每一跳各自重算导致总时长失控。

追踪上下文在 HTTP 模式下最直接：保留或补齐 `traceparent`、`tracestate`、`x-request-id`、`x-correlation-id`。HAProxy 也可以生成 unique ID 并写入 Header 或日志，便于把访问日志、后端日志和采样 trace 对齐。TCP 模式下如果没有应用协议解析，trace 主要靠 PROXY protocol TLV、连接日志、SNI、源地址和外部 eBPF/flow 观测关联，不能指望 HAProxy 自动把业务 trace 往下传。

面试里可以强调一个原则：HAProxy 很擅长在代理边界显式携带上下文，但它不会自动解决“字段可信”和“端到端一致”。真实 IP、协议和 trace 都要在入口层清洗一次，再在内部链路按统一规范传递。

## 39. HAProxy 如何做健康检查、摘除、恢复和流量预热？

可以先这样答：HAProxy 的健康治理能力很完整，核心是 server 行上的 `check`、检查间隔、失败阈值、恢复阈值，以及 TCP/HTTP/SSL/agent/external check 等不同检查方式。健康检查决定后端是否进入负载均衡集合；运行时命令和配置发布决定节点如何摘除、维护、恢复和预热。

最基础的是 TCP check，也就是能不能连上后端端口。HTTP 服务通常要用 `option httpchk`、`http-check send`、`http-check expect` 检查健康 URI、状态码、响应内容或 Header。官方教程里的例子会用 `GET /healthz` 和 `http-check expect status 200`，也会用 `fall`、`rise` 控制连续失败几次摘除、连续成功几次恢复。更复杂的场景可以用 `tcp-check` 做协议握手，用 `agent-check` 让后端主动报告权重或维护状态，用 `external-check` 跑自定义脚本。

摘除可以分硬摘除和软摘除。硬摘除是把 server disable、设为 maintenance，或者让健康检查失败后自动标记 DOWN；软摘除是把权重降到 0、设置 drain、降低 maxconn，让新连接少来或不来，但已有连接继续处理。生产维护通常先 drain，再观察 `scur`、队列、长连接数量和错误率，最后再停进程。对 WebSocket、TCP tunnel、数据库连接这类长连接，drain 时间不能按普通 HTTP 短请求估算。

恢复时要避免瞬间打满。HAProxy 的 `slowstart` 可以让之前失败过的 server 在恢复后逐步增加动态权重或连接能力。它只适用于已经被看到失败过的服务器，不是进程启动时的通用预热开关。实际项目里还会结合 agent-check、Runtime API `set server weight`、外部发布系统或灰度流量，先给 1% 到 5% 的连接，再观察错误、延迟和队列。

流量预热要看三类对象。后端服务要预热缓存、连接池、JIT、TLS、数据库连接和热点数据；HAProxy 要预热 DNS 解析、server state、stick table、证书、OCSP、Runtime socket、日志和监控；上游入口要确认新 HAProxy 或新后端加入后不会让 hash、stickiness、连接复用突然重排。reload 时建议保存 server state，避免新进程忘记旧进程里哪些节点已经失败、维护或降权。

最后要把重试、redispatch 和队列控制住。后端失败时，HAProxy 可以把请求转给其他 server，但重试和 redispatch 没预算会让故障扩大。健康检查不是为了“永远找到一个能试的节点”，而是为了让坏节点尽快离开候选集，让恢复节点慢慢回来，让调用方在集群过载时尽早得到可解释的失败。

## 40. HAProxy 的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：HAProxy 的观测要从 Runtime API、stats、日志、系统资源和后端状态一起看。它成为瓶颈时，不一定直接返回 5xx，也可能表现为 frontend 排队、backend 队列上涨、连接被拒、TLS 握手变慢、CPU 单核打满、文件描述符耗尽、stick table 膨胀或 reload 后旧 worker 长时间不退出。

stats 层要看 frontend、backend、server 三个视角。frontend 关注当前连接、连接上限、请求率、连接率、拒绝数、错误请求、入出流量。backend/server 关注 `qcur/qmax`、`scur/smax/slim`、`stot`、`bin/bout`、`status`、`check_status`、`lastchg`、`econ`、`eresp`、`wretr`、`wredis`、健康检查失败、权重、backup 状态。Runtime API 的 `show stat`、`show info`、`show activity`、`show sess`、`show errors`、`show servers state` 都是排障入口。

日志层要看 HAProxy 的计时字段。HTTP 日志里的队列时间、连接时间、响应头时间、总时间，能区分是排在 HAProxy、连不上后端、后端处理慢，还是客户端读得慢。termination state 也很重要，它能告诉你连接是客户端断、服务端断、超时、队列满、重试失败，还是代理主动拒绝。只看状态码会漏掉很多连接级问题。

系统层要看 CPU、线程利用率、每线程负载是否均衡、内存池、文件描述符、`maxconn`、`ulimit-n`、accept backlog、网卡队列、TLS session rate、压缩 CPU、日志写入、DNS resolver、Runtime socket 延迟、Lua/SPOE 外部服务。HAProxy 通常性能很强，所以一旦它成为瓶颈，常见原因是配置或资源边界：连接上限太低、单线程热点、SSL 开销、日志阻塞、stick table 太大、后端排队被代理层放大。

判断 HAProxy 自身成为瓶颈，可以看几个信号。第一，后端空闲，但 frontend 连接接近 `maxconn`，队列或拒绝上涨。第二，backend `qcur` 高，但 server `scur` 没到上限，说明调度、连接池、健康状态或粘性规则可能有问题。第三，HAProxy CPU 或某个线程长期打满，增加后端没有效果。第四，`Tc`、`Tw`、`Tt` 异常，但后端应用自己的处理时间不高。第五，横向增加 HAProxy 实例后延迟下降，后端指标几乎不变。第六，reload 后旧进程保留大量长连接，新进程和旧进程争资源。

比较稳的排障顺序是：先看是否到 HAProxy 的全局和 frontend 限额，再看 backend/server 队列，再看健康检查和权重，再看日志计时和 termination state，最后看系统 CPU、FD、网络和 TLS。这样能避免把代理层瓶颈误判成应用慢。

## 41. Envoy 位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：Envoy 是 L4/L7 代理，既可以做入口网关，也可以做东西向服务代理，还可以作为 Service Mesh 的 sidecar 或 node proxy。它的位置比 ECMP、BGP、DNS 更靠近应用协议，比纯 LVS/IPVS 更理解 HTTP/gRPC 语义。它解决的不只是负载均衡，还包括服务发现、健康检查、连接池、路由、重试、熔断、限流、可观测性和 xDS 动态配置。

Envoy 的核心模型是 listener、filter chain、cluster 和 endpoint。Downstream 连接到 Envoy 的 listener，Envoy 根据网络过滤器或 HTTP connection manager 解析协议，再把请求路由到 upstream cluster。Envoy 官方术语里，cluster 是一组逻辑相似的 upstream host，成员可以通过 service discovery 获取，健康状态可由 active health checking 判断，请求路由目标由 load balancing policy 决定。这个模型天然适合微服务和动态环境。

入口流量里，Envoy 常作为 edge proxy、Ingress Gateway、API Gateway、TLS 终止点、gRPC/HTTP 入口、东西向网格入口。它能做 Host/path/Header 路由、流量切分、重试、超时、JWT、外部授权、限流、访问日志和 trace。东西向流量里，Envoy 更常见于 Service Mesh sidecar：每个服务旁边一个 Envoy，接管出入站调用，用 xDS 接收集群、路由、端点和安全配置。

它和 Nginx/HAProxy 的区别不是简单的“谁更高级”。Nginx 更常见于传统 HTTP 入口和静态配置代理；HAProxy 在高性能 TCP/HTTP 代理和运行时控制上很强；Envoy 的优势是 xDS、动态服务发现、gRPC/HTTP/2 原生、Filter 体系、分布式可观测和网格生态。代价是控制面复杂度更高，配置对象更多，排障要同时看数据面和控制面。

所以面试里可以这样定位：Envoy 是现代服务治理的数据面代理。它可以服务入口，也可以服务东西向；当它启用 HTTP connection manager、RDS、CDS、EDS、LDS 这些能力时，应用路由和服务发现是它的核心工作之一。但它仍然不是 DNS/GSLB，也不是业务代码本身，复杂业务决策仍应留给应用或控制面。

## 42. Envoy 在高可用设计中如何避免单点故障？

可以先这样答：Envoy 的高可用要同时看数据面和控制面。数据面不能只有一个 Envoy 实例；控制面不能只有一个 xDS 服务；配置、证书、服务发现、健康检查、限流、授权和观测系统也不能成为隐性单点。Envoy 的优势是能在数据面继续转发已有配置，但这也要求控制面下发的配置可验证、可回滚、可灰度。

数据面入口场景里，要多实例部署 Envoy，并放在云 LB、LVS/IPVS、Anycast、DNS/GSLB 或 Kubernetes Service 后面。每个实例要跨可用区、节点和网络故障域，容量按 N+1 或更严格模型设计。sidecar 场景里，单个 Envoy 跟单个工作负载通常绑定，避免全局单点的方式是让每个服务副本都有自己的代理，并保证 Pod/VM 级别的滚动发布和健康检查可靠。

控制面是 Envoy 高可用的重点。LDS、RDS、CDS、EDS、SDS 等 xDS 服务要多副本、多地域或至少多可用区部署，并且能承受大量 Envoy 同时重连。Envoy 支持静态配置，也支持通过 xDS 动态获取 listener、route、cluster、endpoint 等配置。官方文档也强调，静态配置需要 hot restart 才能整体更新，而 EDS/RDS/CDS 可以让端点、路由和集群在运行时更平滑地变化。控制面挂掉时，数据面通常应继续使用最后一个已知好配置，而不是立刻清空路由。

配置发布要有防护。坏的 RDS 规则可能把流量打到错误集群，坏的 EDS 可能把所有 endpoint 摘掉，坏的 SDS 可能让 TLS 握手失败。成熟做法是配置校验、xDS ACK/NACK 监控、分批推送、金丝雀 Envoy、版本回滚、保留 fallback cluster、禁止一次性把所有 locality 或 priority 置空。Envoy 的动态能力很强，但错误配置传播也会很快。

上游健康治理要组合使用。Envoy 支持 active health checking，也支持 outlier detection 这种被动健康检查；还可以用 circuit breaking 限制连接、pending request、请求数和重试数。高可用设计要让主动健康检查、异常摘除、重试预算、熔断和 locality/priority failover 配合起来。没有预算的重试会把故障扩散；没有 outlier detection 的集群可能继续打半死节点；没有 circuit breaker 的代理会把自己和上游一起压垮。

实例生命周期也要处理。Envoy 支持 drain、hot restart、admin `/ready`、`/stats`、`/clusters` 等运维接口。发布时要先让实例停止接新连接，等待已有 HTTP/2 stream、gRPC stream、TCP proxy 连接排空，再退出。admin 接口要只暴露在安全网络或 localhost，因为它会暴露 stats、cluster、证书等敏感信息，某些操作还有破坏性。

最后要关注共享依赖。SDS 证书服务、外部授权服务、全局限流服务、DNS、服务发现、日志/metrics 后端、trace collector 都可能成为“Envoy 没挂但流量挂了”的原因。面试里可以收束成一句：Envoy 高可用不是把 Envoy 进程拉多副本就完了，而是数据面多副本、xDS 控制面高可用、配置渐进发布、健康治理和共享依赖降级一起成立。

## 43. Envoy 与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：Envoy 传递上下文的能力很强，但它不是“自动保证端到端语义”的魔法层。真实客户端 IP 主要靠 `X-Forwarded-For`、`x-envoy-external-address`、`Forwarded`、PROXY protocol 或原始连接源地址；协议主要靠 `:scheme`、`x-forwarded-proto`、SNI、ALPN 和 listener/filter chain 配置；超时靠 route、cluster、retry policy 和应用自己的 deadline 协议；追踪上下文靠 `x-request-id`、`traceparent`、`tracestate`、B3、Datadog、AWS X-Ray 等 header 在 HTTP/gRPC 链路里延续。关键不是能不能转发这些字段，而是每一跳是否明确信任边界、清洗规则和覆盖规则。

真实客户端 IP 要从部署位置讲起。Envoy 作为 edge proxy 时，`use_remote_address` 通常应开启，让 Envoy 把直接下游连接地址作为可信来源，并按规则追加或处理 `X-Forwarded-For`。如果 Envoy 前面还有 CDN、云 LB、F5、Nginx 或 HAProxy，就不能盲信客户端自带的 `X-Forwarded-For`，而要配置可信代理跳数或原始 IP 检测规则。Envoy 官方文档对 XFF 的说明很细：XFF 可以被恶意客户端伪造，只有受控代理追加的部分才可信；`xff_num_trusted_hops` 用来从右侧往左数可信代理链。面试里可以补一句：真实 IP 不是“取 XFF 第一个地址”这么简单，取错了会把伪造头当成审计身份。

协议上下文也要区分下游连接和上游连接。客户端到 Envoy 可能是 HTTPS/HTTP2/HTTP3/gRPC，Envoy 到上游可能是 HTTP/1.1、HTTP/2、明文 HTTP、mTLS 或 TCP proxy。HTTP/2 和 HTTP/3 请求里有 `:scheme`，HTTP/1.1 场景下常通过 `x-forwarded-proto` 让上游知道原始请求是 HTTP 还是 HTTPS。Envoy 可以在 TLS 终止后重新发起到上游的 TLS，也可以透传 TCP；一旦透传，它就看不到 HTTP header，也就不能做 L7 header 注入和 trace 传播。这个边界要讲清楚。

超时传递分两层。第一层是 Envoy 自己的超时，包括 route timeout、per-try timeout、connect timeout、idle timeout、max stream duration、retry timeout budget 等。第二层是业务或 RPC 框架的 deadline，例如 gRPC timeout header、应用自定义 deadline header、OpenTelemetry baggage 或服务内部上下文。Envoy 可以根据路由配置限制一次调用最多等多久，也可以把 gRPC timeout 作为 HTTP/2 metadata 继续向上游传；但如果业务要求“全链路剩余预算递减”，通常还要应用框架配合，不能只靠代理配置。

追踪上下文更适合由入口统一生成或规范化。Envoy 会处理 `x-request-id`，也支持常见追踪 header。实际企业链路里常见做法是：入口 Envoy 若发现请求没有 trace，就生成 request id 或 trace id；如果已经有 `traceparent`，就按 W3C Trace Context 继续传播；内部服务和 sidecar 不随便重写，只追加 span 或采样决策。日志里要同时记录 `x-request-id`、downstream remote address、selected cluster、upstream host、response flag、duration、route name。否则 trace 断了以后，很难判断是 Envoy 路由、上游实例还是应用逻辑出问题。

最后要强调清洗。外部请求进入 edge Envoy 时，应该删除或重写不可信的内部 header，例如 `x-envoy-internal`、内部服务身份、伪造的 `x-request-id` 强制采样标记、伪造的客户端证书 header。到了 mesh 内部，才可以基于 mTLS、SAN、SPIFFE ID 或受控代理链信任这些上下文。面试里答到这一层，能说明你不是只背 header 名称，而是理解代理链的安全边界。

## 44. Envoy 如何做健康检查、摘除、恢复和流量预热？

可以先这样答：Envoy 的健康治理由主动健康检查、被动异常检测、负载均衡权重、连接池、熔断、重试和 slow start 一起完成。主动健康检查定期探测 upstream host；outlier detection 根据真实请求里的连续失败、5xx、超时、reset、成功率或延迟异常把 host 临时摘出；恢复时要满足健康阈值和异常摘除时间；新 endpoint 或刚恢复的 endpoint 可以用 slow start 慢慢接流量，避免冷节点刚回来就被打满。

主动健康检查按 cluster 配置。Envoy 支持 HTTP、gRPC、L3/L4、Redis、Thrift 等类型。HTTP 检查默认期望 200，但响应码、路径、间隔、超时、unhealthy threshold、healthy threshold 都可以配置；gRPC 检查可走 gRPC health checking 语义；L3/L4 可以只做 connect 或发送指定字节再期待响应。健康检查走 cluster 的 transport socket，所以上游如果启用 TLS，健康检查也要按同样的 TLS 或单独 health check 配置处理。生产里不要只查端口，最好让 `/healthz` 或 gRPC health 同时反映关键依赖、只读状态、队列积压和实例是否正在 drain。

被动摘除靠 outlier detection。它观察真实请求结果，发现某个 host 和其他 host 表现不一致时，把它从健康负载均衡集合里移除一段时间。常见触发包括连续 5xx、gateway failure、连接失败、超时、reset、成功率偏离、延迟异常。这里有一个容易忽略的点：HTTP router 能区分上游返回 500 这类外部错误，也能感知连接超时、reset 这类本地错误；TCP proxy 不理解 HTTP，只能看到本地连接层失败。也就是说，同一套异常检测配置在 HTTP 和 TCP 场景下语义不同。

摘除不是越激进越好。异常检测要配 `max_ejection_percent`、base ejection time、检测间隔和 panic threshold，防止一次局部抖动把大部分 host 摘掉。主动健康检查和被动异常检测可以同时启用：主动检查负责发现明确不可用节点，被动检测负责处理“还能连上但真实请求很差”的 fail-slow 节点。若上游池太小，例如只有两个实例，outlier detection 的阈值要更保守，否则容易在短时间内把容量打没。

恢复时要带迟滞。主动健康检查通常要求连续成功若干次才重新标记 healthy；outlier detection 会在 eject 时间到期后让 host 回来，后续如果继续失败，摘除时间可能增长。恢复动作要配合连接池和重试预算。一个刚恢复的实例缓存是冷的，TLS session、数据库连接池、JIT、页缓存、热点索引都可能没准备好；直接恢复完整权重，会造成“刚健康又被打挂”的循环。

Envoy 的 slow start 正是为这个问题设计的。它会在一段 slow start window 内逐步提高新加入或从 unhealthy 变 healthy 的 endpoint 的有效权重。官方文档说明，slow start 会影响 upstream endpoint 的负载均衡权重，目前支持 round robin、least request 和客户端加权 round robin 等策略。它适合 Kubernetes 扩容时少量新 Pod 加入，也适合故障恢复后的冷实例回场；如果整个集群都是新实例，大家一样冷，slow start 的效果就有限。

还要把健康治理和发布流程连起来。Envoy 自己发布时要先 drain，让 listener 停止接新连接，等待 HTTP/2 stream、gRPC stream、TCP 连接排空；上游服务发布时，先从服务发现或 EDS 里降权、drain，再终止进程。没有 drain 的摘除会把正在处理的请求打断；没有重试预算的恢复会把瞬时错误放大；没有 circuit breaker 的代理会在上游慢时堆积 pending request，最后自己也成为故障点。

## 45. Envoy 的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：Envoy 的观测要同时看 listener、HTTP connection manager、cluster、upstream host、健康检查、outlier detection、circuit breaker、xDS、runtime、系统资源和访问日志。判断 Envoy 是否成为瓶颈，不能只看应用 5xx，要看延迟到底花在下游连接、Envoy 队列、路由匹配、TLS、连接池、上游连接建立、上游等待还是日志/过滤器扩展上。

入口侧指标要看 downstream 连接数、新建连接速率、TLS 握手失败、HTTP/1.1/HTTP/2/HTTP/3 请求量、active streams、连接关闭原因、请求大小、响应大小、response code、response flag、downstream reset。访问日志里 `response_code_details`、`response_flags`、`duration`、`upstream_service_time`、`route_name`、`cluster`、`upstream_host` 很有价值。比如 `UH`、`UF`、`UT`、`UO` 这类上游相关标记能快速区分“没健康 host”“连接失败”“上游超时”“熔断溢出”。

上游侧要看 cluster 统计。Envoy 的 cluster manager 统计里有 active/warming cluster、cluster 更新、upstream 连接总数、活跃连接、连接失败、connect timeout、连接溢出、pending request、request total、retry、timeout、health check、outlier ejection、circuit breaker overflow 等。特别要盯 `upstream_cx_connect_fail`、`upstream_cx_connect_timeout`、`upstream_rq_pending_overflow`、`upstream_rq_retry`、`upstream_rq_timeout`、`membership_healthy`、`membership_total`。如果上游健康数下降和 5xx 同步，问题可能在上游；如果上游健康、应用处理也快，但 Envoy pending 和 overflow 上升，代理层就可疑。

xDS 也是关键观测面。要看 LDS/RDS/CDS/EDS/SDS 的 ACK/NACK、配置版本、推送延迟、订阅失败、warming cluster/listener 数量、证书更新失败、endpoint 数量突变。很多 Envoy 事故不是进程崩溃，而是控制面下发了坏路由、空 endpoint、过期证书或不兼容 filter。admin `/config_dump`、`/clusters`、`/listeners`、`/stats`、`/ready` 很好用，但 admin 接口本身必须限制在受信网络或 localhost。

系统资源要看 CPU、内存、线程负载、文件描述符、连接跟踪、网卡队列、TLS 加解密、日志写入、磁盘、DNS resolver、filter 执行耗时、Lua/Wasm/ext_authz/ratelimit 这类外部扩展。Envoy 可能因为一个同步外部授权服务慢、一个 Wasm filter CPU 高、访问日志后端阻塞、过多高基数 stats 或 TLS 配置不当而变成瓶颈。代理越“可编程”，越要把扩展成本纳入观测。

判断 Envoy 自身成为瓶颈，可以看几个信号。第一，下游请求量没明显变，但 Envoy CPU 单核或线程长期打满，p99 上升，横向增加 Envoy 实例后延迟下降。第二，上游应用的处理时间正常，但 Envoy 访问日志里的总耗时明显高于 upstream service time。第三，cluster pending、overflow、circuit breaker 触发增加，而上游实际资源仍有余量。第四，TLS 握手、连接建立、DNS 解析或 xDS warming 指标异常。第五，关闭某个重 filter、降低日志采样或拆分 listener 后，延迟立刻改善。

排障顺序可以是：先看访问日志把错误归类，再看 listener/downstream 是否入口过载，再看 cluster/upstream 是否连接池或上游故障，再看 xDS 是否配置异常，最后看系统资源和扩展 filter。面试里要避免一句“看 QPS、延迟、错误率”就结束；Envoy 的价值和复杂度都在代理内部状态，观测也必须深入到这些状态。

## 46. F5/硬件 ADC 位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：F5 BIG-IP 这类硬件或专用 ADC 通常位于企业流量链路的区域入口、数据中心入口或关键业务入口层，核心职责是把客户端流量接到一个虚拟服务地址，再按 L4/L7 策略分发到后端 pool member。它最典型地解决南北向入口流量问题，也常用于数据中心内部的东西向入口 VIP；至于深层应用路由，它能做一部分，但不应该把复杂业务决策都塞进 ADC。

从对象模型看，F5 LTM 的典型链路是 virtual server、profile、pool、pool member、monitor。客户端连到 virtual server 的 VIP 和端口，系统根据 TCP/HTTP/SSL profile 处理连接，再把请求转给 pool 里的成员。virtual server 可以启用 SNAT、地址转换、端口转换、VLAN 绑定、service policy 等能力；pool 可以配置 members、monitor、负载均衡方法、优先级组、连接限制、slow-ramp-time 等。这个模型说明 ADC 站在“网络入口和后端服务池之间”，不是服务进程内部的治理框架。

入口场景最常见。公网或专线流量经过 DNS/GSLB/CDN/防火墙后进入数据中心，由 F5 承接 HTTPS、TCP、UDP、HTTP、WAF、SSL offload、证书、会话保持、源地址转换、后端池选择和健康检查。它在金融、政企、传统 IDC 里很常见，因为这些环境要求固定 VIP、硬件加速、合规边界、双机热备、细粒度网络策略和成熟运维界面。

东西向场景也有，但要讲边界。企业内部很多系统会用 F5 提供内部 VIP，例如数据库代理入口、核心 Java 服务入口、SOA/ESB 入口、跨网段服务入口。它解决的是“内部消费者访问一个稳定服务地址，再由 ADC 分发到后端实例”。但现代微服务内部的实例级发现、按方法路由、熔断、重试预算、调用链 trace，通常会交给 Service Mesh、RPC 框架或应用网关，而不是全靠硬件 ADC。

应用路由方面，F5 可以通过 HTTP profile、SSL profile、L7 policy、iRules、header/path/cookie 条件做很多事情，例如按 URI 切 pool、按 Host 做虚拟主机、插入 header、重写 URL、做 Cookie persistence。问题是，ADC 配置越像业务代码，变更风险越大，测试和版本管理也越难。面试里比较稳的说法是：F5 可以承担入口层应用路由和安全策略，但复杂租户策略、权限、订单状态、幂等、灰度实验规则，最好放在 API Gateway、服务网格控制面或应用层。

所以一句话定位是：F5/硬件 ADC 是企业流量架构里的高性能入口和数据中心负载均衡层，主场是南北向入口和关键内部 VIP；它能看懂一部分 L7，但不是微服务全链路治理的替代品。

## 47. F5/硬件 ADC 在高可用设计中如何避免单点故障？

可以先这样答：F5/硬件 ADC 避免单点故障，靠的不是单台设备很贵，而是设备冗余、配置同步、流量组 failover、健康检查、上游路由/DNS 配合、容量冗余和变更治理。常见设计是双机或多机 device group，active-standby 或 active-active，业务 VIP 归属于 traffic group；某台设备、链路、VLAN、网关或 pool 健康异常时，VIP 和连接入口切到可用设备。

F5 BIG-IP 的 device group 用于配置同步和故障切换。TMSH 文档里 `cm device-group` 有 `type sync-failover`、`auto-sync`、`network-failover`、`devices` 等配置项，说明高可用不是简单两台机器并排放着，而是要把哪些设备参与同步、是否自动同步、是否通过网络故障切换都配置清楚。同步失败、版本不一致、证书不同步、iRule 不一致，都会让主备切换后表现不一致。

数据面要有冗余路径。两台 ADC 要接入不同交换机、不同电源、不同机架或可用区，外侧和内侧链路都要冗余。上游可以通过静态路由、动态路由、VRRP/浮动地址、ECMP、GSLB 或防火墙策略把流量导到当前 active 设备。下游 pool member 也要跨故障域。如果两台 F5 共用同一个上联交换机、同一个防火墙策略、同一个证书存储或同一个控制网络，实际单点仍然存在。

故障检测要覆盖设备和业务两层。设备层包括 TMM 进程、CPU、内存、接口、VLAN、HA peer、配置同步、license、磁盘、温度、电源。业务层包括 virtual server 是否可用、pool 是否有足够 active member、monitor 是否通过、后端连接失败是否异常、SSL profile 是否正常。F5 pool 配置里有 `min-up-members` 和 `min-up-members-action`，可以在池内可用成员低于阈值时触发动作；这类机制能避免“ADC 活着但后端池已经不可用”的假健康。

active-active 要特别小心容量和故障域。平时两台都接流量，看起来资源利用率高；但一台故障后，另一台要吃下全部流量。如果平时每台已经 70% 到 80%，故障时很容易直接过载。active-standby 容量浪费一些，但故障模型简单。很多核心系统会按 N+1 或 2N 设计，并定期演练 failover，而不是只看设备面板显示 standby ready。

变更治理同样重要。ADC 配置集中、影响面大，错误 iRule、错误 pool、证书替换、SNAT 误配、monitor 路径写错、同步方向弄反，都可能造成大事故。上线前要有配置差异检查、单业务灰度、备份、回滚、只读审计和变更窗口。切换演练要验证会话保持、长连接、TLS session、源地址策略、下游防火墙状态是否符合预期。

面试里可以这样收束：F5 高可用不是“买两台做双机”四个字，而是设备 HA、链路冗余、配置一致、业务健康联动、容量预留和演练共同成立。任何一层没做好，F5 都可能从高可用组件变成高影响单点。

## 48. F5/硬件 ADC 与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：F5/硬件 ADC 能传递一部分上下文，但方式取决于它工作在 L4 还是 L7、是否做 SNAT、是否终止 TLS、是否启用 HTTP profile，以及后端是否理解这些约定。真实客户端 IP 可以保留在源 IP、插入 `X-Forwarded-For`、插入自定义 header，或通过透明/路由模式保留；协议可以通过 TLS 终止位置、HTTP profile、`X-Forwarded-Proto` 这类 header 或自定义规则传递；超时主要是 ADC 的连接、空闲、服务端响应和重试策略；追踪上下文通常是透传或由 iRules/策略补充，不是 F5 的天然分布式追踪模型。

真实客户端 IP 最容易出错。若 virtual server 不做 SNAT，后端从 TCP 源地址可能看到真实客户端 IP，但这要求回包路径经过 ADC 或网络路由对称，否则连接会出问题。若启用 SNAT automap 或 SNAT pool，后端看到的是 ADC 的地址，真实 IP 就要靠 L7 header 传递。F5 HTTP profile 支持 `insert-xforwarded-for`，可以向发往 pool member 的 HTTP 请求插入 XFF；它也有 `header-insert` 能插入自定义 header，用于在 SNAT 后保留客户端地址。这里仍然要处理信任边界：后端只应信任来自 ADC 的 header，ADC 入口最好清洗外部伪造的同名头。

协议上下文取决于 SSL 终止位置。F5 如果在客户端侧终止 TLS，再用明文或重新加密连接后端，后端 socket 看到的协议已经不是用户原始协议。要让后端知道用户原来走 HTTPS，通常需要 ADC 插入或保留 `X-Forwarded-Proto: https`、`X-Forwarded-Port`、Host、SNI 相关信息，或者用内部约定的 header。如果 F5 做 TCP passthrough，它不解密也不解析 HTTP，自然不能插入这些 HTTP header，只能按 TCP 连接或 SNI 做有限分流。

超时也不能说成“F5 把客户端超时传给后端”。ADC 有自己的 client-side timeout、server-side timeout、TCP idle timeout、HTTP keep-alive、connect/reselect/retry、pool queue、OneConnect 等连接治理参数。它可以保护后端不被慢客户端拖住，也可以在后端不可用时重选 pool member。但端到端 deadline 仍然要由客户端、API Gateway、RPC 框架或应用 header 表达。否则 F5 等了 30 秒、后端也等 30 秒、再下游又等 30 秒，全链路超时预算会被层层放大。

追踪上下文通常以透传为主。HTTP 模式下，F5 可以保留 `traceparent`、`tracestate`、`x-request-id`、`x-correlation-id`，也可以通过 iRules 或 L7 policy 补一个 request id。TCP 模式或 TLS passthrough 下，它看不到 HTTP header，只能在连接日志、源/目的地址、virtual server、pool member、SNAT 地址、TLS SNI 等维度做关联。现代 OpenTelemetry trace 的 span 生命周期通常由应用、Envoy、API Gateway 或服务网格承担，F5 更多是入口日志和流量转发视角。

工程上要把“传递”和“可信”分开。ADC 可以把字段传下去，但字段是否可信取决于是否来自受控上游、是否被清洗、是否可被公网客户端伪造、是否经过 TLS/mTLS 保护、日志是否同时记录上一跳地址和 header 值。面试里答到这里，比只列 `X-Forwarded-For` 更有工程含量。

## 49. F5/硬件 ADC 如何做健康检查、摘除、恢复和流量预热？

可以先这样答：F5/硬件 ADC 的健康治理围绕 pool member 和 monitor 展开。monitor 定期检查节点、服务端口或应用路径；失败达到阈值后，pool member 被标记 down，不再接新流量；恢复需要健康检查重新通过；流量预热可以靠 slow ramp、权重、优先级组、连接限制、drain 和发布系统配合完成。硬件 ADC 的优势是入口治理成熟，风险是健康检查如果太浅，会把半死应用当成健康节点。

健康检查可以分多层。基础层是 ICMP、TCP connect、TLS handshake；应用层是 HTTP/HTTPS 路径、状态码、响应体、登录前检查、关键依赖检查。F5 pool 配置支持 `monitor`，pool member 的状态会影响负载均衡候选集。一个好的 monitor 不应该只查 `/` 返回 200，而要查一个轻量但能代表服务可用性的路径；数据库不可写、配置未加载、依赖超时、线程池耗尽时，应返回不健康或 degraded。否则 ADC 会继续把请求发给“端口活着、业务已死”的实例。

摘除分自动摘除和维护摘除。自动摘除由 monitor 失败触发，节点从可用池里移出。维护摘除通常先禁用 pool member 或把权重降到很低，停止新连接进入，再等待已有连接排空。对 HTTP 短连接，排空窗口可以较短；对 WebSocket、长轮询、数据库连接、TCP 隧道，排空窗口要按真实连接寿命设计。直接 disable 可能会断开长连接，影响比健康检查失败还大。

恢复时要避免冷启动冲击。F5 pool 支持 `slow-ramp-time`，用于让新恢复或新加入的成员逐步接收更多连接，而不是一健康就按完整权重接流量。这个能力适合后端需要预热缓存、JIT、连接池、TLS、页缓存或热点数据的场景。若没有 slow ramp，也可以用较低 ratio/weight、连接限制、优先级组、外部发布系统逐步放量来替代。

还要注意优先级组和最小成员数。F5 pool 里有 `min-active-members`，可用于 priority group activation；也有 `min-up-members` 和对应 action，在可用成员低于阈值时触发 failover、reboot 或 restart-all 这类动作。实际生产里要慎用会重启设备的动作，但这个配置体现了一个重要思想：健康治理不能只看单个后端，要看整个池是否还有足够健康容量。

流量预热要覆盖 ADC 和后端两边。ADC 侧要确认配置已同步、monitor 稳定、证书和 profile 正常、SNAT pool 容量够、连接表和会话保持表没有异常；后端侧要预热缓存、数据库连接池、依赖连接、热点数据和线程池。发布系统最好把“注册到 pool”“健康检查通过”“慢速放量”“观察 p99/5xx/连接失败”“提升权重”做成连续步骤。

最后要防止重试和重选放大故障。ADC 可以在后端失败时 reselect 或 reset，但如果上游也在重试，F5 也在重选，应用内部又重试，很容易形成重试风暴。健康检查、摘除、恢复和预热要和全链路重试预算一起设计。

## 50. F5/硬件 ADC 的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：F5/硬件 ADC 的观测要看 virtual server、pool、pool member、profile、TMM、SSL、SNAT、连接表、接口、HA、配置同步和应用日志。判断它自身成为瓶颈，要把客户端侧耗时、ADC 内部处理、后端耗时和网络链路拆开看；只看后端 5xx 或设备 CPU 都不够。

业务对象层面要看每个 virtual server 的连接数、新建连接速率、请求/响应吞吐、HTTP TPS、状态码、L7 策略命中、SSL 握手、会话复用、丢弃和 reset。pool 层要看 active member 数、member 状态变化、monitor 成功/失败、当前连接、连接限制、队列、reselect、服务端连接失败、server latency。F5 analytics profile 支持按 application、pool-member、virtual-server 粒度做 page load time、request/response throughput、server latency、TPS、response codes、client IP、URL、method、user-agent 等采集或告警，这些指标比单纯设备资源更接近用户体验。

系统层面要看 TMM CPU、每核分布、内存、连接表、SNAT 端口耗尽、SSL TPS、加解密硬件利用率、接口流量、丢包、CRC、队列、VLAN、路由、ARP、日志写入、磁盘、HA peer、配置同步状态。硬件 ADC 很多瓶颈不是“总 CPU 100%”才出现，可能是某个 TMM 核倾斜、某个 VLAN 接口满、某个 SNAT pool 端口耗尽、SSL profile 限制、iRule 执行慢、日志远端阻塞或连接表接近上限。

判断 F5 自身成为瓶颈，可以看几个信号。第一，后端 pool member 仍有容量，但 virtual server 的客户端连接排队、reset、超时或握手失败上升。第二，后端应用日志显示处理很快，但客户端侧 TTFB 或总耗时在 ADC 前后差异很大。第三，增加后端实例没有改善，增加 ADC 实例或绕过某个 iRule/profile 后改善明显。第四，TMM 单核打满、SSL TPS 到上限、SNAT 端口耗尽、连接表满、接口丢包或 HA 同步异常。第五，健康检查 flapping 导致后端池频繁摘除恢复，流量在少数节点之间抖动。

排障时要把日志和指标对齐。访问日志里记录 client IP、XFF、virtual server、pool、pool member、SNAT 地址、request id、响应码、耗时；设备侧记录 monitor 事件、pool member 状态变化、HA failover、配置同步和 iRule 错误。若只有设备层 SNMP，没有请求级日志，很难判断是 F5 选错后端、后端慢、还是客户端网络差。

面试里可以补一条经验：F5 是强入口组件，越靠前，越容易被误判为“后端慢”或“网络慢”。判断它是否瓶颈，需要证明瓶颈发生在 ADC 这一跳，例如 ADC 前后延迟差、资源上限命中、横向扩 ADC 有效、绕过特定策略有效。没有这类证据，不要轻易把所有入口问题都归咎于 F5。

## 51. 云厂商 SLB 位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：云厂商 SLB 是云上托管负载均衡层，通常位于 VPC、公网入口或内网服务入口处。它主要解决入口流量接入、多可用区分发、托管弹性、健康检查和目标组分流问题；也可以用于东西向内网负载均衡。是否承担应用路由，取决于具体产品类型：四层 NLB/CLB 更偏连接和流，七层 ALB 更偏 HTTP/HTTPS/gRPC 路由。

以 AWS Elastic Load Balancing 的模型为例，负载均衡器接收客户端流量，再把请求转到一个或多个可用区里的注册目标。ALB 工作在 OSI 第七层，可以按 listener rule 的 priority、condition 和 action 把请求转到不同 target group；条件可以包括 host、path、header、method、query、source IP 等。NLB 更偏第四层，按流哈希把 TCP/UDP/TLS 连接分给 target，连接生命周期内通常保持在同一个 target 上。Gateway Load Balancer 则用于透明插入安全设备或网络虚拟设备。

在企业链路里，公网 SLB 常位于 DNS/GSLB/CDN 之后、应用服务之前，也可以直接作为互联网入口。内网 SLB 常位于服务消费者和服务集群之间，例如 Web 层访问应用层、应用层访问共享中间层、跨 VPC 或跨可用区访问内部服务。云厂商还会把 SLB 和证书管理、WAF、Auto Scaling、Kubernetes Ingress、服务发现、监控、日志集成起来，这也是它和自建 Nginx/HAProxy/F5 的差异：数据面和一部分控制面由云厂商托管。

它解决的不是同一种“负载均衡”。公网入口 SLB 解决客户端怎么稳定进入云上系统；内网 SLB 解决 VPC 内部服务地址稳定和实例变更；ALB 解决部分 L7 路由；NLB 解决低延迟、静态 IP、源地址保留或高连接数场景；全局负载均衡或 Global Accelerator 才解决跨地域入口。面试里要避免把“云厂商 SLB”说成单一产品，因为不同云的命名不同，同一云里也有 ALB、NLB、CLB、GWLB 多种形态。

一句话收束：云厂商 SLB 是云基础设施提供的托管入口和内网分发层。它的主场是入口和服务池分发；七层产品可以做一部分应用路由；更深的业务决策、服务间熔断、租户策略和链路治理，仍然要交给 API Gateway、Service Mesh、RPC 框架或应用。

## 52. 云厂商 SLB 在高可用设计中如何避免单点故障？

可以先这样答：云厂商 SLB 的高可用依赖云厂商托管的数据面、多可用区节点、健康检查、DNS/控制面、目标组容量和用户自己的架构选择。云产品本身通常消除了“单台负载均衡器主机”的问题，但不会自动消除单可用区、单 target group、单证书、单安全组、单配置错误、单下游依赖这些问题。

多可用区是基础。AWS 文档说明，启用某个 Availability Zone 后，ELB 会在该可用区创建负载均衡节点；ALB 要求至少启用两个可用区。若某个可用区不可用或没有健康目标，负载均衡器可以把流量路由到其他可用区的健康目标。这里有两个细节：第一，每个启用可用区里最好至少有一个健康 target，否则该可用区只是入口节点存在，后端容量并不完整；第二，cross-zone load balancing 的行为会影响流量是否跨区打到所有 target，不同产品默认值不同。

SLB 的 DNS 也是高可用路径的一部分。客户端访问云 SLB 通常先解析云厂商分配的域名，拿到一个或多个负载均衡节点 IP。AWS 的 ELB DNS 记录 TTL 为 60 秒，并会随流量变化和节点扩缩更新。用户如果在前面再套企业 DNS、GSLB 或 CDN，要确保 CNAME、别名记录、健康检查、缓存 TTL 和故障切换策略没有把云 SLB 的弹性能力抵消掉。

目标组要有容量冗余。云 SLB 能把流量从不健康 target 摘掉，但剩余 target 是否能承接流量是用户责任。自动扩容组、Kubernetes HPA、预留容量、跨 AZ 实例分布、连接排空、应用启动探针，都要配合。一个常见错误是 SLB 多可用区很漂亮，后端数据库、Redis、消息队列或关键服务却是单可用区，结果入口没挂，业务照样不可用。

控制面和配置也要治理。错误 listener rule、错误 target group、证书过期、安全组误删、健康检查路径写错、WAF 规则误拦、删除 target group，都可能造成入口事故。成熟做法是 IaC 管理、变更审查、灰度发布、访问日志、CloudWatch 告警、回滚预案，以及对高危动作做权限隔离。云厂商托管不等于配置不会出错。

还要设计区域级容灾。单个区域内的多 AZ SLB 解决的是可用区和节点故障；区域级故障要靠 Route 53/GSLB、Global Accelerator、多区域部署、数据复制和业务降级。金融、支付、核心 SaaS 这类系统通常会把“区域内高可用”和“跨区域灾备”分开验收，不能用一个多 AZ SLB 覆盖所有故障模型。

面试里可以这样回答边界：云厂商 SLB 已经把负载均衡器设备层做成托管高可用，但企业还要负责多 AZ 目标、容量冗余、配置安全、依赖高可用和跨区域预案。否则单点只是从“某台机器”转移到了“某个配置、某个 target group 或某个依赖”。

## 53. 云厂商 SLB 与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：云厂商 SLB 的上下文传递取决于产品层级。七层 ALB 通常通过 `X-Forwarded-For`、`X-Forwarded-Proto`、`X-Forwarded-Port`、Host、request id 或云厂商特定 header 传递客户端和协议信息；四层 NLB 通常尽量保留源 IP，或用 PROXY protocol v2 把连接源信息交给后端；超时由 listener、target group、idle timeout、deregistration delay 和应用 deadline 共同决定；追踪上下文通常透传 HTTP header，SLB 自身更多提供访问日志和请求 ID。

以 AWS ALB 为例，它会支持 `X-Forwarded-For`、`X-Forwarded-Proto`、`X-Forwarded-Port`。`X-Forwarded-For` 默认 append 客户端 IP，也可以配置 preserve 或 remove；官方文档也提醒，XFF 有安全风险，只有受保护网络中可信系统添加的条目才能信任。`X-Forwarded-Proto` 用于告诉后端客户端到负载均衡器使用的是 HTTP 还是 HTTPS；否则后端只能看到 SLB 到 target 的协议，容易生成错误重定向或错误绝对 URL。

NLB 的语义不同。它工作在连接层，很多场景下后端能看到客户端源 IP；但目标类型、协议、PrivateLink、NAT、跨 VPC、TLS 终止等都会影响源地址。AWS NLB 支持 PROXY protocol v2，启用后会在连接前部添加 PROXY header，后端必须能解析，否则健康检查或业务请求可能被当成坏请求。面试里要把“保留源 IP”和“用 header 传真实 IP”分开，前者是 L3/L4 地址路径，后者是应用协议约定。

协议传递要区分客户端侧和后端侧。ALB 可以终止 TLS，并用 HTTP/1.1、HTTP/2 或 gRPC 等协议版本转发到 target；NLB 可以做 TCP/TLS 转发或 TLS 终止。客户端到 SLB 的 HTTP/2，不代表 SLB 到后端也是 HTTP/2；客户端到 SLB 的 HTTPS，也不代表后端 socket 能知道原始 HTTPS。后端若需要原始协议、端口、Host，要依赖转发头、日志字段或云产品提供的 request context。

超时不是单字段传递。云 SLB 有 idle timeout、connection timeout、target response time、deregistration delay、健康检查 timeout、重试或 fail-open 行为；应用有自己的 request timeout 和 RPC deadline。ALB target deregistration 时默认会等待一段 deregistration delay 让 in-flight 请求完成；健康检查连接每次检查后关闭；这些都是负载均衡层生命周期，不等于业务 deadline。若需要端到端剩余时间，仍要由客户端或网关把 deadline 放进 HTTP/gRPC metadata，并让后端按它执行。

追踪上下文通常走普通 header。`traceparent`、`tracestate`、`x-request-id`、`x-amzn-trace-id` 等可以经过 ALB 继续下传，前提是规则没有删除或覆盖。SLB 访问日志和 CloudWatch 指标可以帮助关联请求量、目标响应时间、状态码和 target group，但细粒度 span 仍要由应用、OpenTelemetry SDK、Envoy/API Gateway 或服务网格生成。不要指望云 SLB 自动替每个内部服务补全 trace。

最终要讲信任链：后端只信任来自云 SLB 安全组或私网地址的连接，只解析云 SLB 注入或保留的 header；外部客户端自带的 XFF、Proto、Trace header 要么清洗，要么按策略接受。否则日志、限流、审计、风控都会被伪造头污染。

## 54. 云厂商 SLB 如何做健康检查、摘除、恢复和流量预热？

可以先这样答：云厂商 SLB 通常在 target group 级别做健康检查，周期性探测注册目标；健康检查失败后停止向不健康 target 发流量；恢复时要求目标重新通过健康检查；摘除或下线目标时用 deregistration delay 做连接排空；预热则依赖 slow start、自动扩容、目标注册节奏、缓存预热和逐步放量。云 SLB 能做的是入口层治理，后端应用仍要把 readiness 和容量状态暴露准确。

以 AWS ALB 为例，负载均衡节点会定期向 target 发送健康检查请求，使用 target group 的健康检查配置；目标注册后，通过一次健康检查才会被认为 healthy。每个 load balancer node 只向 enabled AZ 中健康 target 路由请求。如果一个 target group 里的所有目标都不健康，ALB 会 fail open，把请求路由到所有目标，不管健康状态。这一点面试里很容易加分：健康检查不是万能保险，全部不健康时的 fail-open 行为要纳入故障预案。

健康检查参数要按业务设置。路径、端口、协议、超时、间隔、healthy threshold、unhealthy threshold、matcher 都会影响摘除速度和误判概率。检查太浅，会把半死实例留在池里；检查太深，会因某个非关键依赖抖动把所有实例摘掉。实践里常把 liveness 和 readiness 分开：进程活着不代表可接流量，只有配置加载完成、依赖可用、连接池就绪、数据分片归属正确时，才返回 ready。

摘除和发布要用连接排空。ALB target group 的 deregistration delay 默认 300 秒，用于停止向正在 deregister 的目标发送新请求，同时给 in-flight 请求完成时间。若目标提前关闭连接，客户端可能收到 5xx。下线流程不应直接杀进程，而应先让实例从 target group 或 Kubernetes endpoint 中摘除，等待 drain，再关闭应用。长连接、WebSocket、gRPC streaming、大文件上传下载要单独估算排空时间。

恢复和预热要控制节奏。ALB 支持 target group slow start，让新注册或刚恢复的 target 在指定时间内逐步增加分到的请求量。但 slow start 和某些路由算法、黏性会有组合限制；AWS 文档也说明 weighted random 不能和 slow start 同用。若使用 NLB、CLB 或其他云产品没有同等 slow start，就要通过发布系统、Auto Scaling lifecycle hook、Kubernetes readiness gate、权重路由、灰度入口或应用限流逐步接流量。

还要利用异常缓解和容量信号。AWS ALB 的 Automatic Target Weights 可以在 weighted random 算法下对异常目标做流量缓解，逐步减少发往异常 target 的流量。无论使用哪家云，指标上都要看 healthy/unhealthy host、target response time、5xx、连接失败、TLS negotiation error、LCU/容量水位、目标 CPU/内存/队列。健康检查只回答“能不能接”，容量指标回答“接了会不会慢”。

面试里可以总结：云 SLB 的健康治理是 target group 级别的接入控制，不是应用内部自愈。正确流程是 readiness 暴露真实状态，SLB 健康检查做摘除，deregistration delay 做排空，slow start 或灰度做恢复，容量监控决定是否继续放量。

## 55. 云厂商 SLB 的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：云厂商 SLB 的观测要覆盖入口请求量、连接数、状态码、目标健康、目标响应时间、连接错误、TLS 错误、规则命中、容量单位、访问日志、可用区分布和后端指标。判断 SLB 自身成为瓶颈，要证明问题发生在 SLB 层或 SLB 配置层，而不是 DNS、客户端网络、后端应用、数据库或安全组。

基础指标包括 request count、active/new connection、processed bytes、HTTPCode_ELB_4XX/5XX、HTTPCode_Target_4XX/5XX、TargetResponseTime、TargetConnectionErrorCount、TargetTLSNegotiationErrorCount、HealthyHostCount、UnHealthyHostCount、RequestCountPerTarget、rule evaluations、LCU 或容量水位。AWS ALB 的 CloudWatch 指标把 load balancer 生成的响应码和 target 生成的响应码分开，这一点很重要：ELB 5xx 更像入口层或转发失败，Target 5xx 更像后端应用返回。

访问日志要和指标一起看。ALB access log 可以记录请求时间、客户端地址、target 地址、request processing time、target processing time、response processing time、ELB status code、target status code、received/sent bytes、request line、user agent、trace id 等。指标告诉你“哪里变差”，日志告诉你“哪些请求、哪些 target、哪些规则、哪些状态码”。没有日志时，很多 SLB 问题只能停留在猜测。

可用区和目标组维度不能丢。要按 AZ、target group、listener、rule、target type 拆开看流量和错误。某个 AZ 的 healthy host 降到 0、某个 target group 的 response time 飙升、某条 listener rule 命中异常、某个后端端口安全组被改，都会表现成“SLB 入口慢”。如果 cross-zone 行为、DNS 缓存和客户端连接池叠在一起，真实流量分布还可能和预期权重不一致。

判断云 SLB 自身瓶颈或配置瓶颈，可以看几个信号。第一，ELB 生成的 5xx、连接错误、TLS negotiation error 上升，而 target 侧没有对应请求或没有对应 5xx。第二，TargetResponseTime 不高，但客户端总耗时高，可能是连接建立、TLS、DNS、WAF、规则评估或客户端到 SLB 网络问题。第三，HealthyHostCount 正常，但某些 listener/rule 的固定响应、重定向、认证、WAF 或安全组导致请求没到后端。第四，LCU、连接数、TLS 握手、规则评估或带宽接近配额，申请预留容量或拆分入口后改善。第五，多可用区里某个 zonal 节点或某个 AZ 流量异常，切走该 AZ 后恢复。

也要注意“看起来像 SLB，实际不是 SLB”的情况。DNS 解析到旧地址、客户端复用长连接、CDN 回源慢、安全组/NACL 拦截、后端连接池满、数据库慢、证书链错误、MTU/PMTUD 问题，都可能让用户认为 SLB 慢。排障时要按链路分段：DNS 解析时间、TCP/TLS 建连、SLB 日志处理时间、target processing time、应用内部耗时、数据库耗时。能分段，才不会把所有入口问题都推给云厂商。

面试里可以用一句话收束：云 SLB 的观测核心是“入口层指标、目标层指标、访问日志、后端指标对账”。只有当 SLB 层错误、容量、规则、TLS、连接或可用区维度出现证据，并且后端指标无法解释时，才说它自身成为瓶颈。

## 56. NLB 位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：NLB，Network Load Balancer，位于四层负载均衡层，主要处理 TCP、UDP、TLS、QUIC 这类连接或流。它的主场是入口连接分发、固定 IP 接入、高并发长连接、低延迟转发和内网服务入口。它也能用于东西向流量，例如 VPC 内部服务通过一个稳定地址访问后端实例；但它不是应用路由组件，因为它不按 Host、Path、Header、Cookie、gRPC method 或租户信息做决策。

AWS 官方文档把 NLB 放在 OSI 第四层。客户端请求到达 NLB 后，NLB 从 listener 的默认 target group 里选择一个目标，再按指定协议和端口转发。对 TCP 流量，它使用 flow hash，输入包括协议、源 IP、源端口、目的 IP、目的端口和 TCP sequence number；一条 TCP 连接在生命周期内会固定到一个 target。UDP 按五元组保持同一 flow 的目标一致；QUIC 则会利用 Connection ID 里的 Server ID，初始阶段再退回到类似流哈希的逻辑。

这个定位决定了它和 ALB 的边界。NLB 适合需要静态 IP 或 Elastic IP、源地址保留、PrivateLink、TLS 透传或 TLS 终止、高吞吐、长连接、非 HTTP 协议的场景。比如数据库代理入口、MQTT、游戏长连接、专线入口、内部 TCP 服务、gRPC 但不需要按方法路由的入口，都可以考虑 NLB。ALB 更适合 HTTP/HTTPS/gRPC 的七层规则路由。

入口流量里，NLB 常放在 DNS/Route 53/GSLB 后面，也可以放在防火墙、WAF 或 CDN 的回源路径里。它给业务一个稳定的云上入口，每个启用可用区都有负载均衡节点和对应网络接口，公网 NLB 还可以给每个子网绑定 Elastic IP。内网场景里，它可以作为 VPC 内部服务 VIP，让调用方不用感知后端实例变更。

它不适合承担复杂应用路由。NLB listener 有协议和端口，target group 有目标和健康检查，但它不会解析 HTTP 请求内容。若要按 `/api/order`、Host、Header、JWT、灰度版本、租户、AB 实验切流，应把逻辑放到 ALB、API Gateway、Envoy、Nginx、Service Mesh 或应用里。NLB 可以站在这些七层组件前面，先解决高性能连接入口，再由下游做应用语义。

面试里可以这样总结：NLB 是云上四层入口和内网连接分发层，解决“连接先打到哪个健康目标”这个问题；它能服务南北向，也能服务东西向，但不负责深层应用路由。

## 57. NLB 在高可用设计中如何避免单点故障？

可以先这样答：NLB 的高可用依赖多可用区负载均衡节点、每个可用区的健康 target、DNS 对可用区节点 IP 的管理、可选的 cross-zone load balancing、连接排空和区域级容灾。云厂商已经托管了负载均衡器节点，但用户仍要保证 target 分布、容量冗余、安全组、路由、下游依赖和跨区域切换都能扛住故障。

启用可用区时，ELB 会在对应可用区创建 NLB 节点。NLB 默认每个节点只把流量分发给本可用区的注册目标；如果开启 cross-zone load balancing，每个节点可以把流量分发给所有启用可用区的健康目标。这个默认行为很重要：如果你只在一个可用区部署 target，却启用了三个可用区的 NLB，另外两个可用区入口节点并不能凭空提供后端容量。

AWS 文档还提到一个 DNS 层行为：如果某个 enabled AZ 里的 target group 没有健康目标，NLB 会从 DNS 中移除对应子网的 IP，让新解析尽量避开这个可用区。问题在于 DNS 不是实时控制面。客户端、递归解析器、连接池可能不遵守 TTL，或者继续连接旧 IP；官方文档也说明，如果客户端在 IP 从 DNS 移除后仍然发请求到该地址，请求会失败。所以高可用不能只依赖 DNS 摘除，还要让客户端有重试、连接重建和多地址解析能力。

每个可用区要有足够 target。NLB 多 AZ 只是入口节点多点，后端容量仍由目标组里的实例、IP 或 ALB 决定。生产设计至少要做到：每个 enabled AZ 有健康 target，单 AZ 故障后剩余 AZ 有足够容量，Auto Scaling 或 Kubernetes 能补足实例，健康检查不会因为浅探针误判，安全组/NACL/路由表不会只允许某个单点路径。

NLB 的静态 IP 不等于单点。每个启用可用区都有自己的网络接口和地址，公网 NLB 可绑定每 AZ 一个 Elastic IP。企业前面若有防火墙、合作方白名单、专线 ACL，应该把所有可用区地址都纳入，不要只放通其中一个地址。否则 NLB 自身是多 AZ，外部网络策略却把它变成单 AZ 入口。

跨区域故障要另做设计。NLB 解决的是一个 Region 内的多 AZ 可用性；区域级故障需要 Route 53、Global Accelerator、GSLB、多区域部署、数据复制和业务降级。AWS ARC 的 zonal shift 可以把某个负载均衡资源从受损 AZ 切走，但一次只能针对单个 AZ，且要先确认剩余可用区容量。面试里要把 AZ 内高可用和 Region 级容灾分开讲。

## 58. NLB 与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：NLB 是四层组件，上下文传递主要靠原始连接信息、客户端 IP 保留和 PROXY protocol v2。它不会像 ALB 那样自动插入 `X-Forwarded-For`、`X-Forwarded-Proto`，也不会理解 `traceparent` 这类 HTTP header。真实客户端 IP 能不能到后端，要看 target type、协议、是否开启 client IP preservation、是否经过 PrivateLink、是否启用 PROXY protocol，以及下游是否能解析。

客户端 IP 保留是 NLB 的核心能力之一。AWS 文档说明，NLB 可以在转发到后端 target 时保留客户端源 IP；对于不同 target type 和协议，默认行为不同。instance 类型目标通常保留，IP 类型的 TCP/TLS 目标默认不保留，UDP、TCP_UDP、QUIC、TCP_QUIC 场景默认保留且不能禁用。启用 client IP preservation 后，流量必须能从 NLB 直接到 target；如果经过 transit gateway、某些检查路径或 PrivateLink，源地址语义会变化。

如果不能或不想保留源 IP，可以启用 PROXY protocol v2。它会在连接开始处加一段协议头，把原始源地址、目的地址等连接信息交给后端。后端必须明确支持并解析 PROXY protocol，否则会把这段头当成业务数据，轻则健康检查失败，重则业务协议报错。AWS 文档也提醒，启用后健康检查连接也会带 PROXY protocol header，但健康检查里的 client connection information 不会按真实客户端填充。

协议上下文也要按四层理解。NLB 支持 TCP、UDP、TCP_UDP、TLS、QUIC、TCP_QUIC target group；它可以做 TLS listener，在负载均衡器处终止 TLS，然后重新建立到 target 的连接，也可以做 TCP/TLS 透传。它看不到 HTTP path、header 和 cookie；TLS 透传时也无法读取 HTTP 语义。若下游需要知道原始协议、SNI、ALPN 或客户端证书信息，要看 NLB listener、TLS 配置、PROXY protocol TLV、访问日志和后端协议是否支持。

超时不是通过 header 传递。NLB 处理的是连接生命周期、健康检查 timeout、deregistration delay、unhealthy draining interval、连接终止等。业务 deadline、gRPC timeout、HTTP request timeout 仍然要由客户端、ALB/API Gateway、Envoy 或应用框架携带。NLB 可以影响连接是否建立、是否被 reset、目标下线时是否继续让已有连接完成，但它不会帮业务计算剩余调用预算。

追踪上下文基本不在 NLB 层。HTTP 或 gRPC 的 `traceparent`、`tracestate`、`x-request-id` 会作为 TCP payload 穿过 NLB，NLB 不解析也不生成 span。要把 NLB 这一跳纳入排障，通常靠 VPC Flow Logs、NLB access logs、CloudWatch 指标、target 日志中的源地址或 PROXY protocol 信息，再和应用 trace 关联。面试里可以直接说：NLB 负责连接转发和连接元数据，不负责应用 trace 传播。

## 59. NLB 如何做健康检查、摘除、恢复和流量预热？

可以先这样答：NLB 使用主动和被动健康检查判断 target 是否可用。主动健康检查按 target group 配置 TCP、HTTP 或 HTTPS 探测；被动健康检查根据真实连接表现更早发现异常目标，但不能关闭、配置或单独监控。目标失败后，NLB 停止向它发新连接，必要时发送 TCP RST；恢复时要通过连续健康检查。NLB 没有 ALB 那种通用 slow start，预热更多依赖发布流程、扩容节奏和应用自我保护。

主动健康检查的参数包括协议、端口、路径、超时、间隔、healthy threshold、unhealthy threshold 和 matcher。NLB 默认 TCP 健康检查，HTTP/HTTPS 检查可以校验状态码。AWS 文档说明，NLB 健康检查是分布式的，并用共识机制判断 target health，所以目标收到的检查次数可能比配置值更多。若用 HTTP health check，路径要轻量，别让健康检查本身打到昂贵依赖。

被动健康检查补的是 fail-slow 和连接失败场景。NLB 会观察 target 对连接的响应，从而可能早于主动检查发现问题。它不支持 UDP，也不支持开启 stickiness 的 target group。这个细节适合在面试里讲：NLB 的健康机制不是只有定时探测，但被动检查不是用户可调的策略引擎，不能像 Envoy outlier detection 那样精细配置。

故障时有两个重要行为。第一，如果一个可用区里没有健康目标，NLB 会从 DNS 移除该可用区的地址，避免新请求继续打进来。第二，如果所有 enabled AZ 的所有 target 都不健康，或者 target group 为空，NLB 会 fail open，把流量发给所有目标，不管健康状态。这个行为不是漏洞，而是避免健康检查误杀导致全站彻底无路可走；但业务必须知道这意味着“全部不健康”时后端仍会收到流量。

摘除和下线要用 deregistration delay。NLB 在 target deregister 后停止创建新连接，并用 connection draining 给已有连接完成时间；默认 300 秒。对长连接、TCP 隧道、数据库连接、MQTT、WebSocket 后面的 TCP 流，排空时间要按真实连接寿命估算。NLB 还可以配置 deregistration 后连接终止，让超时结束后关闭连接。直接杀 target 进程会把连接错误暴露给客户端。

恢复和预热靠外部节奏。NLB target 通过初始健康检查后就可能接新连接，没有 ALB slow start 那种线性放量机制。工程上常用 Auto Scaling lifecycle hook、Kubernetes readiness gate、先少量注册 target、分批加入 target group、应用本地限流、连接池预热、缓存预热、下游依赖预热来控制回场速度。如果 NLB 后面接的是 ALB 或 Envoy，也可以把预热放在下游七层组件里做。

一句话收束：NLB 健康治理偏连接层，能摘掉明显坏目标，也能做连接排空；它不理解业务冷热状态。应用要通过 readiness、限流和发布系统把“刚启动但还不能吃满流量”这个状态表达出来。

## 60. NLB 的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：NLB 的观测要看流量、连接、包、字节、健康目标、可用区、TLS、端口分配、reset、拒绝连接、安全组阻断和访问日志。判断 NLB 自身成为瓶颈，重点看连接是否在 NLB 层被拒绝或 reset、LCU/流量是否接近容量、端口分配是否出错、某个可用区是否异常，以及 target 侧是否并没有对应压力。

基础 CloudWatch 指标包括 `ActiveFlowCount`、`NewFlowCount`、`ProcessedBytes`、`ProcessedPackets`、`PeakBytesPerSecond`、`PeakPacketsPerSecond`、`HealthyHostCount`、`UnHealthyHostCount`、`ZonalHealthStatus`、`ConsumedLCUs`。如果使用 TLS listener，还要看 `ClientTLSNegotiationErrorCount` 和 `TargetTLSNegotiationErrorCount`。这些指标要按 LoadBalancer、AvailabilityZone、TargetGroup 维度拆开，单看全局平均很容易掩盖某个 AZ 或某个 target group 的问题。

连接错误类指标很关键。`RejectedFlowCount` 表示 NLB 拒绝的 flow；`PortAllocationErrorCount` 表示客户端地址转换时临时端口分配失败，非零就意味着有客户端连接被丢。AWS 文档提到，在禁用 client IP preservation、执行客户端地址转换时，每个唯一 target 地址和端口有连接规模限制，超过后端口分配错误风险会上升。解决思路通常是增加 target、开启客户端 IP 保留、拆分端口或调整架构，而不是只扩应用线程池。

reset 指标能帮你判断断连接来自哪里。`TCP_Client_Reset_Count` 是客户端发出的 reset，`TCP_Target_Reset_Count` 是 target 发出的 reset，`TCP_ELB_Reset_Count` 是负载均衡器生成的 reset。若 ELB reset 上升，要查 target health、连接终止、空闲超时、不可路由、异常目标和 NLB 配置；若 target reset 上升，更可能是应用或后端系统主动关闭；若 client reset 上升，可能是客户端超时、移动网络、上游代理或重试逻辑。

访问日志和 VPC Flow Logs 用来补指标盲区。NLB access logs 尤其适合 TLS listener 和连接级排障，能提供连接时间、客户端、target、TLS、字节和错误信息；VPC Flow Logs 可以看安全组/NACL、ENI、流量方向和拒绝。若 NLB 前面还有 DNS、Global Accelerator、CDN 或防火墙，要把这些层的日志一起对齐，否则容易把外部网络或安全策略问题误认为 NLB。

判断 NLB 成为瓶颈，可以看几个信号。第一，`RejectedFlowCount`、`PortAllocationErrorCount`、`TCP_ELB_Reset_Count` 或 TLS negotiation error 上升，而 target 侧没有对应处理压力。第二，某个 AZ 的 `ZonalHealthStatus`、healthy host 或流量突然异常。第三，开启或关闭 cross-zone 后问题明显变化，说明容量或 AZ 分布不均。第四，增加 target 或调整 client IP preservation 后连接失败下降。第五，绕过 NLB 直连 target 正常，但经过 NLB 出现连接建立失败或 reset。证据要落在连接层，不要只凭“用户说慢”就把瓶颈扣给 NLB。

## 61. ALB 位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：ALB，Application Load Balancer，位于七层应用负载均衡层，主要处理 HTTP、HTTPS、HTTP/2、gRPC、WebSocket 这类应用协议入口。它既可以作为公网入口，也可以作为内网服务入口；和 NLB 相比，它更擅长应用路由，例如按 Host、Path、Header、Method、Query、Source IP 把请求转到不同 target group。

AWS 官方文档明确说 ALB 工作在 OSI 第七层。请求到达后，ALB 按 listener rule 的 priority 依次评估条件，匹配后执行 action，再从目标组里选 target。规则条件包括 `host-header`、`http-header`、`http-request-method`、`path-pattern`、`query-string` 和 `source-ip`。这就是 ALB 和 NLB 的核心差异：ALB 看得懂 HTTP 请求内容，NLB 主要看连接和 flow。

入口场景里，ALB 常放在 Route 53、CloudFront、WAF、Global Accelerator 或 NLB 后面，承担 TLS 终止、HTTP 路由、重定向、固定响应、OIDC/Cognito 认证、WAF 集成、访问日志、target group 健康检查。典型例子是一个域名下按 `/api`、`/admin`、`/static` 分发到不同服务，或者按 Host 把多个子域名挂在同一个 ALB 上。

东西向场景也可以用 ALB。比如 VPC 内部服务通过 internal ALB 暴露 HTTP/gRPC 接口，让调用方按稳定域名访问后端 ECS、EKS、EC2 或 IP target。它适合服务数量不太碎、HTTP 路由规则清楚、需要云托管入口的场景。若服务间调用特别密、需要细粒度熔断、重试预算、mTLS 身份、按方法治理，Service Mesh 或 RPC 框架会更合适。

ALB 不是完整 API Gateway。它能做 listener rule、认证、重定向和部分 header 操作，但复杂鉴权、配额、请求转换、版本治理、审计策略、租户隔离、业务编排，通常仍要放到 API Gateway、Envoy、应用服务或专门控制面。面试里不要把“七层负载均衡”说成“所有应用逻辑都该放在 ALB”。

一句话定位：ALB 是云上托管的 HTTP/HTTPS/gRPC 七层入口，既解决入口流量分发，也能解决一部分内网应用路由；它的边界在 HTTP 请求级路由，业务级决策仍应留给更靠近业务的层。

## 62. ALB 在高可用设计中如何避免单点故障？

可以先这样答：ALB 的高可用由多可用区负载均衡节点、每个可用区健康 target、cross-zone load balancing、健康检查、target group 容量、DNS、访问控制和变更治理共同保证。AWS 托管了 ALB 数据面，但用户仍要避免单 target group、单 AZ 后端、错误 listener rule、证书过期、安全组误配和下游依赖单点。

ALB 要至少启用两个可用区。ELB 会在启用的 AZ 创建负载均衡节点；如果某个 AZ 不可用或没有健康目标，ALB 可以把流量路由到其他 AZ 的健康 target。AWS 文档也建议所有负载均衡器启用多个可用区，并要求 ALB 至少启用两个。这里的关键是：每个 enabled AZ 里要有健康 target，或者 cross-zone 和剩余容量能覆盖故障。只有入口节点多 AZ，后端实例却集中在一个 AZ，不能算真正高可用。

cross-zone 行为要理解清楚。ALB 在负载均衡器级别总是启用 cross-zone，在 target group 级别可以关闭。开启后，每个负载均衡节点可以把流量发到所有 enabled AZ 的 target；关闭或按 target group 调整后，容量分布和故障影响会变得更复杂。面试里可以说：ALB 高可用不只是“选两个子网”，还要看 target group 的跨区分布和 cross-zone 策略。

健康检查和 fail-open 也影响可用性。ALB 每个节点按 target group 配置检查 target，只向健康目标发请求。但如果一个 target group 里所有 registered target 都不健康，ALB 会 fail open，把请求发给所有目标。这个行为可以避免健康检查配置误杀导致完全无流量，但也意味着后端全坏时仍会接到请求，所以应用要有降级、限流和清晰错误响应。

配置层是常见单点。一个错误 listener rule 可以把流量转错 target group，证书过期会让 HTTPS 入口失败，WAF 规则误拦会让请求没到后端，OIDC 配置错误会让用户无法登录，安全组/NACL 误配会让 ALB 不能连 target。成熟做法是 IaC、分环境验证、灰度 listener/rule、健康检查日志、访问日志、CloudWatch 告警和快速回滚。

区域级容灾要另做。ALB 是 Region 内资源，解决的是多 AZ 高可用。跨 Region 需要 Route 53、Global Accelerator、GSLB、多区域 ALB、数据复制和业务降级。若业务要求 RTO/RPO，不能只把 ALB 配成两个 AZ 就结束，还要演练区域切换、证书、DNS、WAF、身份系统和下游数据层。

## 63. ALB 与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：ALB 是七层代理，会终止客户端连接，再建立到 target 的连接。真实客户端 IP 通常通过 `X-Forwarded-For` 传给后端，原始协议通过 `X-Forwarded-Proto`，原始端口通过 `X-Forwarded-Port`；HTTP/gRPC trace header 通常透传；超时由 ALB idle timeout、target 响应、deregistration delay 和应用自己的 deadline 共同决定。

`X-Forwarded-For` 要按信任边界处理。AWS 文档说明，ALB 支持 append、preserve、remove 三种 XFF 处理模式，默认是 append：没有 XFF 时创建一个，有 XFF 时追加客户端 IP。文档也提醒，XFF 有安全风险，只有受控网络里的系统添加的条目才可信。后端不能简单取最左边 IP，而要知道 ALB 前面是否还有 CloudFront、NLB、代理或客户自带伪造头。

`X-Forwarded-Proto` 和 `X-Forwarded-Port` 用来修复后端视角。ALB 终止 HTTPS 后，后端可能只看到来自 ALB 的 HTTP 或 HTTPS 连接。如果应用要生成重定向 URL、回调地址、绝对链接或安全 Cookie，就要看这些 header，而不是只看后端 socket。否则常见问题是用户访问 HTTPS，后端却生成 HTTP 回跳地址。

协议传递还包括 HTTP/2、gRPC 和 WebSocket 的边界。ALB 支持前端 HTTP/2，也支持把 target group 协议版本配置为 HTTP/2 或 gRPC；但客户端到 ALB 的协议和 ALB 到 target 的协议不一定相同。WebSocket 升级后，连接会保持在选中的 target 上，后续消息走同一连接。面试里要说清楚：ALB 可以理解应用协议并路由，但它仍是代理，两侧连接是两段。

超时不要混成一个概念。ALB access log 里有 request_processing_time、target_processing_time、response_processing_time；target group 有 deregistration delay；listener/ALB 有连接和 idle timeout 语义；应用还有自己的 request timeout、RPC deadline、数据库 timeout。ALB 可以因为 target 不响应而返回错误，也可以在 target deregister 时等待 in-flight 请求完成，但它不会自动帮业务把剩余 deadline 逐跳递减。

追踪上下文通常由应用或入口网关生成，ALB负责透传。`traceparent`、`tracestate`、`x-request-id`、`x-amzn-trace-id` 可以随 HTTP 请求到后端；ALB 访问日志也有 trace 相关字段可用于关联。若需要每个服务都有 span，还是要 OpenTelemetry SDK、Envoy、API Gateway 或服务网格配合。ALB 本身不是分布式追踪系统。

## 64. ALB 如何做健康检查、摘除、恢复和流量预热？

可以先这样答：ALB 在 target group 级别做 HTTP、HTTPS 或 gRPC 健康检查。目标通过初始健康检查后才接流量；连续失败超过阈值会被摘除；连续成功超过阈值会恢复；下线时用 deregistration delay 做连接排空；恢复或扩容时可以用 slow start 逐步加流量。和 NLB 不同，ALB 的健康检查更理解 HTTP/gRPC 语义。

健康检查参数包括协议、端口、路径、超时、间隔、healthy threshold、unhealthy threshold 和 matcher。HTTP/HTTPS 检查使用 GET；HTTP/1.1 或 HTTP/2 路径默认是 `/`；gRPC 可以配置类似 `/package.service/method` 的健康检查方法。成功码也不同：HTTP/HTTPS 是 200 到 499 范围内的匹配规则，gRPC 是 0 到 99 的状态码范围。面试里提到 gRPC health check，会比只说“探测 /healthz”更准确。

摘除逻辑很直接：健康检查连续失败后，ALB 不再把新请求发给该 target。状态会显示 `unhealthy`，reason code 可能是 `Target.ResponseCodeMismatch`、`Target.Timeout`、`Target.FailedHealthChecks` 或 `Elb.InternalError`。当 target deregister 时，状态进入 `draining`，这会减少 HealthyHostCount，但不增加 UnHealthyHostCount。这个细节对监控告警很有用。

连接排空依赖 deregistration delay。ALB 默认等待 300 秒，让 in-flight 请求完成；如果没有活动连接，可以更快完成 deregistration，但状态显示可能仍等到超时结束。若 target 在 delay 结束前主动断连接，客户端可能收到 5xx。发布流程应先让实例从 target group 或 Kubernetes endpoint 中摘除，再等 drain，最后停进程。WebSocket、gRPC stream、大文件上传下载要把排空时间估得更长。

恢复和预热可以用 slow start。AWS 文档说明，默认情况下 target 通过初始健康检查后就接完整份额；开启 slow start 后，ALB 会在线性窗口内逐步增加发给该 target 的请求数。限制也要记住：least outstanding requests 和 weighted random 不能和 slow start 同用；空 target group 一次性注册第一批 target 时，这些 target 不会进入 slow start，因为没有已经健康且不在 slow start 的基准目标。

ALB 还有 routing algorithm 和 ATW。target group 默认 round robin，也可选 least outstanding requests 或 weighted random；weighted random 支持 Automatic Target Weights 异常缓解，会减少发往异常 target 的流量。它不是健康检查替代品，更像健康目标内部的异常缓解。真实生产还要配合应用 readiness、缓存预热、连接池预热、限流、Auto Scaling lifecycle hook 和灰度发布。

最后补一个边界：ALB health check 不支持 WebSocket。WebSocket 业务仍要用 HTTP/HTTPS 健康路径或旁路健康端口代表服务状态。不要把“WebSocket 连接还在”当成健康检查策略。

## 65. ALB 的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：ALB 的观测要把负载均衡器指标、target group 指标、访问日志、健康检查日志、WAF/认证日志和后端应用指标放在一起看。判断 ALB 自身成为瓶颈，要看请求是否卡在 ALB 处理、规则评估、TLS、WAF、认证、连接到 target、响应回写这些阶段，而不是只看用户端慢。

CloudWatch 里先看入口层指标。常用的有 `RequestCount`、`ActiveConnectionCount`、`NewConnectionCount`、`ProcessedBytes`、`HTTPCode_ELB_3XX/4XX/5XX_Count`、`ClientTLSNegotiationErrorCount`、`RuleEvaluations`、`ConsumedLCUs`、`DroppedInvalidHeaderRequestCount`、`GrpcRequestCount`。ELB 自己产生的 4xx/5xx 和 target 返回的 4xx/5xx 要分开看，前者更像规则、协议、WAF、认证、无健康目标或转发失败，后者更像后端应用响应。

target 维度要看 `HealthyHostCount`、`UnHealthyHostCount`、`RequestCountPerTarget`、`TargetConnectionErrorCount`、`TargetResponseTime`、`TargetTLSNegotiationErrorCount`、`HTTPCode_Target_2XX/3XX/4XX/5XX_Count`、`AnomalousHostCount`、`MitigatedHostCount`。如果 TargetResponseTime 飙升，后端处理慢或后端依赖慢更可疑；如果 TargetConnectionErrorCount 上升，可能是安全组、端口、进程监听、TLS 到 target、连接池或目标实例网络问题。

访问日志是 ALB 排障的主工具。它记录每个请求或连接的 type、time、elb、client:port、target:port、request_processing_time、target_processing_time、response_processing_time、elb_status_code、target_status_code、received/sent bytes、request line、user agent 等。三个 processing time 很有用：request_processing_time 高，可能是客户端上传慢、WAF/Lambda 认证或转发前处理；target_processing_time 高，通常是 target 慢；response_processing_time 高，可能是 ALB 到客户端回写、客户端接收慢或队列问题。

判断 ALB 自身或配置成为瓶颈，可以看几个信号。第一，`HTTPCode_ELB_5XX` 上升，但 target 没收到请求或 target_status_code 是 `-`。第二，`request_processing_time` 或 `response_processing_time` 高，而 `target_processing_time` 正常。第三，`RuleEvaluations`、LCU、TLS negotiation error、认证失败或 WAF 阻断指标同步上升。第四，某条 listener rule、redirect、fixed response、OIDC/Cognito、WAF 或 header 处理配置变更后问题出现。第五，增加后端 target 没改善，拆分 ALB、减少规则、关闭某个策略或提高容量预留后改善。

也要警惕误判。ALB 前面的 DNS、CloudFront、Global Accelerator、客户端网络、运营商链路、TLS 证书链、WAF、后端安全组、数据库慢，都可能表现成“入口慢”。排障时按链路切分：客户端 DNS/TCP/TLS、ALB request processing、target processing、ALB response processing、应用内部耗时、下游依赖耗时。能用 access log 和后端日志对齐的，尽量不要靠猜。

面试里可以收成一句：ALB 的观测核心是“ELB 生成的状态码、target 生成的状态码、三段处理时间、target 健康和 LCU 对账”。只有这些证据指向 ALB 层，才说 ALB 自身成为瓶颈；否则多数问题其实在规则配置、后端目标或链路其他层。

## 66. API Gateway 位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：API Gateway 位于七层应用入口和 API 治理层，通常在 DNS、CDN、全局负载均衡、四层或七层负载均衡之后，在具体后端服务之前。它的主场是入口流量，尤其是把公网客户端、移动端、第三方调用方、前端 BFF、合作伙伴系统的请求接进企业内部服务。它也可以用于内部东西向流量，但如果问题只是在集群内做服务发现和负载均衡，Kubernetes Service、Service Mesh 或 RPC 框架通常更贴近那个场景。

API Gateway 解决的不是“把连接分散到多台机器”这么简单。它更像企业 API 边界：统一认证鉴权、API Key、JWT、OAuth/OIDC、签名校验、限流、配额、请求校验、参数转换、协议适配、版本治理、灰度发布、审计日志、访问日志、追踪上下文注入、错误响应标准化。AWS API Gateway 官方文档把它描述成应用访问后端数据、业务逻辑或功能的 front door，并列出流量管理、授权访问控制、监控和版本管理这些任务，这个定位比普通负载均衡器更靠近 API 产品和安全边界。

入口链路里常见顺序是：用户先经过 DNS/GSLB，落到 CDN 或公网 LB；CDN 处理缓存、WAF、边缘规则；LB 把连接送到 API Gateway；API Gateway 再按 host、path、method、header、认证主体、租户、版本或 API 产品把请求发给后端服务、函数、消息入口或内部网关。API Gateway 可以和 ALB、Nginx、Envoy、Kong、Spring Cloud Gateway、AWS API Gateway 等形态重叠，但面试里要讲清楚你用它承担的是“API 治理”还是“普通反向代理”。

东西向场景也有 API Gateway。比如一个企业把所有内部服务调用统一经过内部 API Gateway，做服务级鉴权、审计、租户隔离和配额；或者把跨部门 API 以产品化方式暴露给其他业务线。这种做法适合调用边界清楚、治理诉求强的内部 API。若服务间调用非常密，且需要每跳 mTLS、自动重试、熔断、细粒度负载均衡和按实例观测，Service Mesh 或 RPC 框架一般更自然。把所有内部调用都强行绕到中心 API Gateway，容易把它做成性能热点和组织瓶颈。

应用路由方面，API Gateway 通常比 ALB/Ingress 更靠近业务语义。ALB 可以按 path、host、header 转发；Ingress 也能把 HTTP 路由映射到 Service。但 API Gateway 往往还知道 API 版本、消费者身份、套餐配额、请求 schema、签名、鉴权策略、审计字段和后端集成类型。它适合做“入口处必须统一执行”的策略，不适合放复杂业务决策。订单状态、库存一致性、用户权限细节、幂等写入和事务边界，仍应留在业务服务。

一句话定位：API Gateway 是企业流量架构里的七层 API 边界，主要解决入口 API 治理和一部分应用路由；它可以参与内部 API 暴露，但不是 Kubernetes Service、四层负载均衡或服务网格的替代品。

## 67. API Gateway 在高可用设计中如何避免单点故障？

可以先这样答：API Gateway 避免单点故障，要同时保护数据面、控制面、配置发布、依赖服务、证书和上游入口。数据面要多实例、多可用区、无状态或弱状态；控制面要支持配置版本化、灰度、回滚和最后可用配置；依赖的鉴权、限流、缓存、服务发现、证书和日志链路也不能变成新的单点。

数据面最基本的做法是把 Gateway 实例部署成多副本，跨节点、跨可用区，前面用云 LB、NLB、ALB、Anycast、DNS/GSLB 或 Kubernetes Service 分流。实例本身尽量无状态，不把登录会话、限流计数、路由表、租户配置只存在本地内存里。确实需要状态时，要使用高可用存储或缓存，例如多副本 Redis、分区限流服务、分布式配置中心，并明确这些依赖故障时是 fail open 还是 fail closed。安全类策略通常更偏 fail closed，普通观测或低风险限流可以按业务风险决定。

控制面和数据面要隔离。很多事故不是 Gateway 进程挂了，而是控制台、配置中心、证书发布、服务发现或自动化脚本出错，把所有路由、证书、上游地址或限流策略一起推坏。成熟做法是数据面继续使用最后一个已知可用配置，配置变更走校验、预发布环境、灰度实例、金丝雀流量、自动回滚和审计。AWS API Gateway 支持 canary release deployments 这个思路本质上也是先让小部分流量验证新配置，而不是一次性把所有入口切过去。

入口冗余不能只看 Gateway 副本数。DNS、CDN、外部 LB、自定义域名、证书、WAF、身份提供方、VPC Link、NAT、后端 ALB/NLB 都可能是单点。比如 Gateway 多副本跑得很好，但所有私有集成都通过一个 VPC Link 或一个内网负载均衡器；或者所有认证都依赖一个不可用的 OIDC issuer；或者证书到期导致 TLS 握手失败。这些都属于 API Gateway 高可用设计的一部分。

跨地域要看业务要求。单地域多 AZ 可以解决大多数区域内故障；如果有明确 RTO/RPO，应该准备多地域 API Gateway、区域自定义域名、Route 53/全局流量管理、后端数据复制和降级策略。多地域不是简单复制一个 Gateway 配置，因为认证回调地址、证书、WAF、配额状态、灰度版本、后端数据一致性都要一起设计。读 API、查询 API 可以更容易做多活；强一致写 API 往往需要主备、幂等键、冲突处理和降级页面。

发布和扩容时也要考虑可用性。Gateway 实例要支持优雅下线：先从上游 LB 摘除或 readiness 变 false，再等待 in-flight 请求、WebSocket 连接、gRPC stream 或大请求完成，最后停止进程。扩容回场要预热连接池、证书、路由配置、服务发现缓存、JIT/运行时缓存和限流本地缓存。新实例刚启动就接满流量，常见结果是认证服务、配置中心或下游连接池被打穿。

面试里可以补一句边界：托管 API Gateway 让云厂商承担了很多底层可用性，但用户仍要为域名、证书、路由配置、后端集成、限流策略、认证依赖、日志告警和跨地域容灾负责。不要把“使用托管产品”误说成“没有单点”。

## 68. API Gateway 与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：API Gateway 是七层代理时，会终止或接管客户端连接，再建立到下游的连接。真实客户端 IP 通常靠 `X-Forwarded-For`、`Forwarded`、`X-Real-IP` 或厂商自定义头传递；原始协议、Host 和端口靠 `X-Forwarded-Proto`、`X-Forwarded-Host`、`X-Forwarded-Port` 表达；追踪上下文靠 `traceparent`、`tracestate`、`x-request-id`、`x-b3-*` 等 header 或 gRPC metadata 透传；超时要分网关自身超时和业务 deadline，不能混成一个概念。

真实客户端 IP 要先建立信任链。客户端自己带来的 `X-Forwarded-For` 不可信，外部用户可以伪造。正确做法是由最外层可信代理清洗同名头，再把看到的源地址追加或重写；API Gateway 只信任来自 CDN、LB、Ingress、内部代理这些已知 CIDR 的头。AWS API Gateway 文档提到 HTTP API 会把传入的 `X-Forwarded-*` 转成标准 `Forwarded` header 并追加 egress IP、Host 和协议；这类行为说明 Gateway 会改写上下文，后端读取前必须知道产品或网关的具体规则。

协议传递也要分清“客户端到 Gateway”和“Gateway 到后端”。外部是 HTTPS，不代表后端也是 HTTPS；外部是 HTTP/2 或 WebSocket，也不代表后端连接形态完全一致。Gateway 可能做 TLS 终止、HTTP/2 到 HTTP/1.1 转换、REST 到 Lambda event、HTTP 到 gRPC 转码、WebSocket 消息路由。后端如果需要生成绝对 URL、重定向地址、Cookie Secure 属性或审计日志，应该看可信的 forwarded 头，而不是只看后端 socket 上的 scheme 和 remote address。

超时至少有四层：客户端超时、Gateway 入口处理超时、Gateway 到集成后端的连接和响应超时、业务服务自己的 deadline。Gateway 的 connect/read/write timeout 用来保护代理线程、连接池和下游资源；业务 deadline 表示这次调用还剩多少预算。HTTP 场景可以用自定义 `X-Request-Deadline`、`X-Timeout-Ms` 或企业统一 header；gRPC 场景常见是 `grpc-timeout` 和 metadata。关键是每一跳都要递减剩余预算，而不是每一层重新给 30 秒，否则整条链路会被层层放大。

追踪上下文要以透传为主，入口生成为辅。如果客户端已经带了符合信任规则的 `traceparent`，Gateway 应继续传给下游；如果没有，可以生成 trace id、request id，再写入访问日志和下游 header。Gateway 自己可以记录一个入口 span 或代理 span，但端到端 trace 仍要靠后端服务、OpenTelemetry SDK、Envoy/Service Mesh 或 RPC 框架继续创建子 span。只在 Gateway 生成一个请求 ID，不等于下游链路已经可观测。

还要注意 header 白名单和敏感头。API Gateway 常会删除、重命名或覆盖部分 hop-by-hop header、认证 header、Host、Connection、Transfer-Encoding 等字段。AWS API Gateway 的重要说明里列出了 REST API 对若干 header 的丢弃、重映射和覆盖行为。面试里如果能说出“上下文传递是契约，要按网关产品和信任边界显式配置”，比只背几个 header 名更可靠。

## 69. API Gateway 如何做健康检查、摘除、恢复和流量预热？

可以先这样答：API Gateway 的健康治理分两类：Gateway 自身是否能接请求，以及它后面的 upstream 是否适合接请求。Gateway 自身靠 liveness/readiness、负载均衡器健康检查、实例心跳和控制面状态保证；upstream 健康通常靠服务发现、主动健康检查、被动失败检测、熔断、异常摘除、灰度权重和连接排空。不同产品差异很大，托管 API Gateway、Envoy/Kong/Nginx 型网关、Spring Cloud Gateway 的能力边界不一样。

Gateway 自身健康检查不能只探一个 `/ping`。最少要检查进程存活、监听端口、配置是否加载成功、证书是否可用、关键线程池或 event loop 是否还能处理请求。如果 Gateway 依赖远程配置、限流服务、认证公钥缓存、服务发现、日志队列，也要决定这些依赖失败时 readiness 是否失败。不要把所有依赖都放进 liveness，否则外部依赖抖动会导致 Gateway 自己反复重启。

upstream 摘除有主动和被动两条路。主动健康检查会周期性请求后端健康端点，失败超过阈值后停止发新流量；被动检测会根据真实请求的连接失败、超时、5xx、reset、熔断溢出判断某个 upstream 或 endpoint 异常。Envoy 里对应 health checking、outlier detection、circuit breaking；Nginx/HAProxy/Kong 也有类似能力。若 Gateway 后面接 Kubernetes Service，还要看 EndpointSlice 和 Pod readiness，因为后端 Pod 是否 Ready 会影响可用 endpoint 集合。

摘除时要先停新请求，再排空旧请求。HTTP 短请求可以等待较短 drain 窗口；WebSocket、SSE、gRPC streaming、大文件上传下载需要更长时间或专门迁移机制。Gateway 关闭前应让上游 LB 不再分配新连接，同时保留已有连接直到超时或业务完成。后端下线时，也要先让服务发现或 endpoint 从 Gateway 的 upstream 集合里消失，再停服务。直接杀后端进程会把 Gateway 变成错误放大器。

恢复不能只看健康检查刚成功。刚恢复的实例可能缓存冷、连接池空、JIT 未热、证书会话冷、数据库连接未建立、限流本地桶未同步。更稳的做法是连续多次健康检查成功后，先低权重接流量，再逐步升高；或使用 slow start、canary、灰度 route、按租户放量、按地区放量。AWS API Gateway 的 canary deployment、Envoy slow start、云 LB 的 target slow start，本质都是避免冷实例一回来就吃满份额。

托管 API Gateway 还有一个边界：它通常不等同于完整的后端实例级健康检查系统。后端是 Lambda、HTTP endpoint、VPC Link、ALB/NLB、Kubernetes Ingress 或 Service 时，真实健康更多由这些下游组件负责。API Gateway 可以通过超时、错误映射、重试策略、集成状态、告警和流量切换做保护，但不能替数据库、缓存、消息队列和业务依赖判断“这个请求现在能不能正确完成”。

面试里可以用一句话收束：API Gateway 的健康治理要把入口实例、配置、依赖、upstream、连接排空和回场预热连起来看；健康检查只是信号，摘除和恢复才是完整的流量动作。

## 70. API Gateway 的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：API Gateway 的观测要覆盖入口流量、路由命中、认证鉴权、限流配额、缓存、后端集成、错误映射、延迟分解、资源使用和配置发布。判断它自身成为瓶颈，不能只看用户说慢，要比较总延迟、后端集成延迟、Gateway 处理开销、连接队列、线程或 event loop 饱和、限流和认证依赖耗时。

入口指标包括 request count、并发连接、活跃请求、请求体大小、响应体大小、TLS 握手失败、HTTP/2 stream 数、WebSocket 连接数、按 route/method/stage/consumer/status code 分组的 QPS 和错误率。错误要拆开看：客户端参数错误、认证失败、鉴权拒绝、限流 429、WAF 拦截、请求体过大、路由未命中、后端连接失败、后端超时、Gateway 自己生成的 5xx。把所有 4xx/5xx 混在一起，会遮住真正问题。

延迟要分段。AWS API Gateway 的 CloudWatch 指标里有 `Latency` 和 `IntegrationLatency`，前者包含 API Gateway 开销和集成延迟，后者表示它把请求转给后端到收到后端响应之间的时间。这个分解很有用：如果总延迟高而 integration latency 正常，问题更像 Gateway 的认证、映射、限流、缓存、日志、WAF 或客户端回写；如果 integration latency 高，后端服务或后端网络更可疑。

治理类指标也要单独看。认证服务耗时、JWKS 拉取失败、token 校验失败、authorizer timeout、API Key 查找耗时、配额命中、限流拒绝、缓存命中率、cache miss、请求/响应 mapping template 耗时、schema validation 错误、灰度版本命中比例、配置版本、配置下发延迟、回滚次数，都能帮助判断是流量问题还是策略问题。很多 Gateway “慢”其实是外部身份提供方慢或限流存储慢。

资源指标包括 CPU、内存、GC、event loop lag、worker 线程池队列、连接池占用、upstream pending request、文件描述符、端口耗尽、日志队列积压、磁盘 I/O、sidecar 资源、容器重启、Pod throttling。托管产品看不到所有底层指标时，要用可见指标间接判断，例如并发上升后 5xx/timeout 同步上升，后端没有对应压力，或者提高配额、拆分 stage/route、减少 mapping 和 authorizer 后立刻改善。

判断 Gateway 自身成为瓶颈，可以看几组证据。第一，总延迟高于后端耗时很多，且差值集中在 Gateway 层。第二，Gateway 自己生成的 5xx、429、authorizer timeout、integration timeout、header/body limit 错误上升，而后端日志没有同等请求。第三，横向扩 Gateway 实例或拆分热点 API 后延迟下降。第四，关闭重策略插件、降低日志采样、减少同步外部调用或优化路由规则后恢复。第五，Gateway 绕路直连后端明显正常，但经 Gateway 访问异常。没有这些证据，不要把所有入口问题都归因到 API Gateway。

## 71. Kubernetes Service 位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：Kubernetes Service 是集群内服务抽象和四层访问入口，核心作用是给一组 Pod 提供稳定的虚拟 IP、DNS 名称和端口，并把流量转到当前 Ready 的后端 endpoint。它主要解决东西向流量和集群内部服务发现问题；通过 `NodePort`、`LoadBalancer`、`ExternalName` 等类型也能参与入口暴露，但它本身不是七层应用路由组件。

Kubernetes 官方文档把 Service 描述成一种抽象，用来把网络服务暴露给客户端，背后 Pod 可以变化而 Service 名称和地址保持稳定。典型调用是 `orders.default.svc.cluster.local:8080`，调用方不需要知道当前有哪些订单服务 Pod、Pod IP 是否变化、Pod 是否被滚动替换。EndpointSlice 控制器根据 selector 维护 endpoints，kube-proxy、CNI eBPF 数据面或云集成再把流量转发到这些 endpoint。

从企业链路看，Service 更靠近集群内部。Pod 访问 Pod 不应直接依赖 Pod IP，因为 Pod 生命周期短；Service 提供稳定入口。`ClusterIP` 面向集群内调用，`Headless Service` 常用于 StatefulSet、客户端负载均衡或服务发现，`NodePort` 把节点端口暴露出来，`LoadBalancer` 让云厂商创建外部或内部负载均衡器。不同类型解决的是“怎么访问这组 Pod”，不是“按业务规则选择哪个 API 版本”。

入口流量里 Service 常作为 Ingress、Gateway、API Gateway、云 LB 的下游对象。比如 Ingress 规则把 `/api/order` 转到 `order-service:80`，Service 再把流量转到订单 Pod。这里七层路由发生在 Ingress 或 Gateway，Service 只负责 Service 到 endpoint 的 L4 转发。把 Service 叫作“内部负载均衡”可以，但不要把它说成 API Gateway。

应用路由不是 Service 的强项。Service 只理解协议、端口、目标 IP 和 endpoint 集合；它不理解 HTTP path、Header、Cookie、JWT、gRPC method、租户和灰度实验。Kubernetes 近年增加了 `appProtocol`、traffic policy、traffic distribution、EndpointSlice topology 等能力，但这些仍是网络和拓扑层面的提示，不是业务语义路由。需要七层路由时，应使用 Ingress、Gateway API、Service Mesh、Envoy、Nginx 或应用代码。

一句话总结：Kubernetes Service 是集群内稳定服务入口和 L4 负载分发抽象，主场是东西向服务发现和 Pod 组访问；它可以被入口组件引用，但不承担完整入口网关和应用路由职责。

## 72. Kubernetes Service 在高可用设计中如何避免单点故障？

可以先这样答：Kubernetes Service 本身不是一个中心化代理进程，它是 API 对象加数据面规则。高可用要保护三块：Service/EndpointSlice 控制面能持续更新，节点上的 kube-proxy 或 eBPF 数据面能正确转发，后端 Pod 分布在多个节点和故障域里。只创建一个 Service 对象，不等于业务已经高可用。

数据面上，传统 kube-proxy 会在每个节点把 Service 规则同步到 iptables、IPVS 或用户空间代理；很多 CNI 使用 eBPF 实现同类能力。这样 Service VIP 的转发不是依赖一个全局代理节点，而是分布在每个节点。某个节点故障只影响该节点上的 Pod 或流量路径，其他节点仍可工作。这里的单点风险来自 kube-proxy 挂掉、CNI 规则异常、conntrack 打满、节点网络故障，而不是 Service 对象本身。

控制面上，要保证 kube-apiserver、etcd、controller-manager、EndpointSlice controller、cloud-controller-manager、CoreDNS 都可靠。控制面短暂不可用时，已有节点转发规则通常还能继续工作，但 Pod 扩缩容、readiness 变化、endpoint 更新、LoadBalancer 状态更新会变慢或停住。事故里常见的表现是后端 Pod 已经不可用，但 EndpointSlice 更新没有及时传到所有节点，旧 endpoint 还在接流量。

后端容量是 Service 高可用的核心。Pod 要跨节点、跨可用区分布，用 Deployment 副本数、Pod anti-affinity、topology spread constraints、PDB、HPA、资源 requests/limits 和节点池容量保证故障后仍有足够 Ready endpoint。只把 Service 暴露成 `LoadBalancer`，但所有 Pod 都在同一台节点或同一可用区，并不高可用。Service 只会把流量转给 endpoint，不能替你创造剩余容量。

入口型 Service 还要看云负载均衡器和 `externalTrafficPolicy`。`LoadBalancer` Service 的外部健康检查行为由云厂商实现，Kubernetes API 不统一规定每个云如何探测。`externalTrafficPolicy: Local` 可以保留客户端源 IP，但要求每个接流节点本地有可用 endpoint，否则外部 LB 需要把没有本地 endpoint 的节点摘掉。`Cluster` 模式更容易分流到任意节点，但可能发生 SNAT，真实源 IP 处理方式不同。

CoreDNS 也不能忽略。很多服务调用先解析 `svc.cluster.local`，如果 CoreDNS 副本太少、缓存不足、NodeLocal DNSCache 异常或集群 DNS 被打满，Service 规则本身正常也会表现成调用失败。高可用设计里要给 CoreDNS 多副本、PDB、资源保障和监控；对高 QPS 服务可以考虑连接复用、客户端缓存和避免每次请求都查 DNS。

面试里可以收束为：Service 的高可用不是“Service 对象多副本”，而是控制面、节点数据面、DNS、endpoint 分布、云 LB 和后端容量共同可靠。

## 73. Kubernetes Service 与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：Kubernetes Service 是 L3/L4 抽象，不会主动添加 HTTP Header，也不会生成 trace 或传递业务 deadline。它能影响源 IP 是否保留、目标端口和协议如何映射、连接怎么被转发；真实客户端 IP、原始协议、超时和追踪上下文主要由上游 LB、Ingress、API Gateway、Service Mesh、RPC 框架或应用自己处理。

真实客户端 IP 要看路径。集群内 Pod 通过 `ClusterIP` 调另一个 Service，后端通常能看到调用方 Pod IP，具体取决于 kube-proxy 模式、CNI、是否经过 NAT。外部流量通过 `NodePort` 或 `LoadBalancer` 进来时，如果 `externalTrafficPolicy` 是 `Cluster`，流量可能跨节点转发并发生 SNAT，后端看到的可能是节点 IP；如果设为 `Local`，更容易保留客户端源 IP，但要求接流节点本地有 endpoint，并且外部 LB 能识别哪些节点可接流量。

Service 不会产生 `X-Forwarded-For`。如果业务需要 HTTP 层真实客户端 IP，应该由最外层可信七层代理写入，比如 CloudFront、ALB、Ingress NGINX、Envoy、API Gateway；然后后端按可信代理链读取。若是四层代理链，可以使用 PROXY protocol，但 Kubernetes Service 本身不解析也不生成这段协议，需要上游 LB 和下游应用或代理都支持。

协议层面，Service 的端口定义包含 `protocol`，常见是 TCP、UDP、SCTP；`appProtocol` 可以给实现提供应用协议提示，例如 HTTP、HTTP/2、gRPC，但 Service 仍不会解析应用流量。HTTP Header、gRPC metadata、WebSocket 消息、TLS 里的 SNI 是否可见，取决于上下游代理和应用，不取决于 Service。Service 最多把连接送到某个 endpoint。

超时同样不在 Service 对象里表达。Service 没有“请求超时”“后端响应超时”“重试预算”这类字段。客户端的 HTTP timeout、gRPC deadline、数据库连接超时、Ingress proxy timeout、Envoy route timeout、Service Mesh retry budget 才是超时治理的位置。Service 可能通过 conntrack、TCP 重传、节点网络异常影响实际耗时，但它不负责端到端 deadline 传播。

追踪上下文是普通应用数据。`traceparent`、`tracestate`、`x-request-id`、`b3`、gRPC metadata 会作为 HTTP/gRPC 流量经过 Service 继续到后端，只要中间代理不删除它们。Service 不会新建 span，也不会把 Kubernetes metadata 自动写入 trace。若需要把 namespace、pod、node、service、endpoint 信息和 trace 关联，一般靠 OpenTelemetry SDK、sidecar、eBPF 观测或代理访问日志补标签。

面试里不要把“Service 保留源 IP”和“应用拿到真实用户 IP”混为一谈。前者是网络转发路径问题，后者是可信代理链和应用协议问题。

## 74. Kubernetes Service 如何做健康检查、摘除、恢复和流量预热？

可以先这样答：Kubernetes Service 自身不周期性探测后端 HTTP 健康，它依赖 Pod readiness、EndpointSlice 条件、控制器和节点数据面来决定哪些 endpoint 接流量。Pod readiness 失败时，EndpointSlice controller 会把 Pod IP 从匹配 Service 的可用 endpoint 中移出；Pod 删除时，endpoint 会进入 terminating/ready false，正常 Service 流量不再把它当作普通可用目标。

健康信号来自 kubelet probe 和 Pod 状态。startup probe 用来保护慢启动应用，成功前不执行 liveness/readiness；liveness probe 用来判断是否要重启容器；readiness probe 用来判断是否可以接流量。Kubernetes 官方文档明确说明，readiness 失败时 Pod IP 会从匹配 Service 的 EndpointSlice 中移除。这个机制就是 Service 摘除的基础。

EndpointSlice 比旧 Endpoints 更能表达滚动发布状态。EndpointSlice 条件包括 `serving`、`terminating` 和 `ready`。对 Pod 来说，`serving` 映射 Pod Ready，`terminating` 表示 Pod 已开始删除，`ready` 可以理解成 serving 且未 terminating。Service 代理通常忽略 terminating endpoint，但在所有 endpoint 都 terminating 时可能仍路由到 serving+terminating 的 endpoint，以减少滚动更新期间流量完全丢失。

摘除流程要和 Pod 终止配合。正确下线通常是：应用收到终止信号或 preStop hook，先停止 readiness 或让 readiness 失败；EndpointSlice 更新后，新流量不再进来；应用在 `terminationGracePeriodSeconds` 内完成 in-flight 请求、关闭长连接或拒绝新请求；最后进程退出。直接杀容器会导致 kube-proxy、Ingress、Service Mesh、客户端连接池还没感知，错误会短时间放大。

恢复和预热不是 Service 内建 slow start。Pod readiness 一旦成功，Service 就可能把流量转给它。为了避免冷启动，要把 readiness 写得保守：等待配置加载、数据库连接池建立、缓存预热、依赖可用、迁移完成后再 Ready。Deployment 可以用 `minReadySeconds`、滚动更新比例、HPA 预扩容、PDB、readiness gates 控制节奏；Ingress、Envoy、ALB、Service Mesh 或应用限流可以在七层做更细的慢启动。

`LoadBalancer` Service 还涉及外部健康检查。Kubernetes API 不统一规定云负载均衡器如何检查节点或后端，云厂商集成会根据 Service、端口、`externalTrafficPolicy` 和实现创建健康检查。若外部 LB 认为节点健康，但节点本地没有可用 endpoint，或者健康检查路径太浅，就可能把流量送到错误位置。生产里要把云 LB 健康、Kubernetes readiness、Ingress/Gateway 健康和应用健康对齐。

一句话收束：Service 的健康治理是“readiness 控制 endpoint，EndpointSlice 表达可用性，节点数据面更新转发规则，应用负责优雅退出和预热”。Service 不是主动 HTTP 健康检查器，也不是冷启动流量调节器。

## 75. Kubernetes Service 的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：Kubernetes Service 的观测要看 Service 对象、EndpointSlice、kube-proxy 或 eBPF 数据面、CoreDNS、节点网络、conntrack、CNI、云负载均衡器和后端 Pod。判断 Service 自身成为瓶颈，要证明问题发生在 Service 虚拟 IP、转发规则、DNS 或节点数据面，而不是后端 Pod、Ingress、数据库或客户端。

对象层先看 Service 类型、selector、ports、targetPort、sessionAffinity、internal/external traffic policy、traffic distribution、EndpointSlice 数量、endpoint ready/serving/terminating 状态、每个 endpoint 的 zone/node 分布。如果 Service selector 写错、targetPort 对不上、所有 endpoint 都 NotReady，表现就是 Service 不通，但根因其实是配置或 Pod readiness。

数据面要看 kube-proxy 或 CNI。iptables 模式关注规则规模、sync 耗时、sync 失败、iptables-restore 错误、conntrack 使用率；IPVS 模式关注 virtual service、real server、连接数、调度算法、IPVS 表同步；eBPF 模式关注 BPF map 容量、drop、policy verdict、service map、endpoint map、程序加载错误。节点上还要看 CPU softirq、网卡丢包、MTU、路由、NAT、端口耗尽和安全策略。

DNS 是常见盲点。Service 名称解析慢、CoreDNS QPS 打满、cache miss 高、NodeLocal DNSCache 异常、搜索域过多导致多次查询，都会表现成“访问 Service 慢”。如果客户端每次请求都重新解析 Service DNS，DNS 问题会被放大。观测时要看 CoreDNS request count、rcode、latency、cache hit、forward 到上游耗时，以及应用侧 DNS 解析耗时。

入口型 Service 还要看外部 LB。`LoadBalancer` Service 背后的云 LB 有自己的健康目标、连接数、5xx、reset、目标可用区、源 IP 保留和安全组。Kubernetes 里 endpoint Ready，不代表云 LB 已经把节点或 Pod 加入目标；云 LB 健康，也不代表 Service selector 和 Pod readiness 没问题。两边要对账。

判断 Service 数据面成为瓶颈，可以看几个信号。第一，直连 Pod IP 正常，经 ClusterIP 或 NodePort 异常。第二，只有某些节点访问 Service 异常，迁移 Pod 或绕过该节点后恢复。第三，kube-proxy sync 错误、规则更新滞后、conntrack full、IPVS real server 不一致、eBPF map drop 和请求失败同步发生。第四，Service endpoint 数量或规则规模增长后，节点 CPU、规则同步延迟、p99 明显上升。第五，CoreDNS 或 NodeLocal DNSCache 指标异常导致大量调用在连接前耗时。

也要警惕误判。大多数“Service 慢”最后会落到后端 Pod 资源不足、应用锁、数据库慢、Ingress 重试、客户端连接池或网络策略。面试里稳妥的说法是：只有当绕过 Service 后端正常，并且 kube-proxy/CNI/DNS/conntrack 证据指向服务转发层，才说 Kubernetes Service 自身成为瓶颈。

## 76. Kubernetes Ingress 位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：Kubernetes Ingress 位于集群边界的七层 HTTP/HTTPS 入口路由层。它用 Kubernetes API 描述 host、path、TLS 和后端 Service 的映射，真正的数据面由 Ingress Controller 实现。它主要解决入口流量和一部分应用路由问题，不是通用东西向服务发现组件，也不暴露任意 TCP/UDP 端口。

Kubernetes 官方文档说得很直接：Ingress exposes HTTP and HTTPS routes from outside the cluster to services within the cluster，路由由 Ingress 资源里的规则控制。它可以给 Service 提供外部可访问 URL、做负载均衡、TLS 终止和基于名称的虚拟主机。但只有创建 Ingress 资源没有效果，集群里必须运行对应的 Ingress Controller。

企业链路中，Ingress 常在云 LoadBalancer、NLB、ALB、NodePort 或边缘 LB 后面，在 Kubernetes Service 前面。外部请求先到 Ingress Controller 的 Pod 或云集成数据面，Controller 按 Ingress 规则把请求转到某个 Service，再由 Service 转到 Pod。这个位置决定了它能做 HTTP 层入口路由，但它不是 DNS/GSLB，也不是后端服务内部的调用治理框架。

Ingress 的应用路由能力以 HTTP 为中心：按 host、path、TLS 证书、部分 controller-specific annotation 做路由和代理参数。像 ingress-nginx 可以通过注解配置 rewrite、backend protocol、proxy timeout、body size、auth、canary、rate limit 等行为；其他 Controller 可能用不同注解或 CRD。Kubernetes Ingress 规范本身相对保守，很多高级能力并不跨实现通用。

东西向流量也可能经过内部 Ingress，例如给集群内其他团队提供统一 HTTP 域名，或者跨 namespace 暴露内部 API。但这不是 Ingress 的核心定位。高频服务间调用、每跳 mTLS、细粒度熔断、重试预算、按方法路由和自动服务发现，更适合 Service Mesh、Gateway API、Envoy 或 RPC 框架。

还要提到当前演进方向：Kubernetes 项目建议使用 Gateway API 作为更强的新一代入口和路由 API，Ingress API 已经冻结但不会移除。这不影响 Ingress 在大量生产集群里的现实地位，但面试里说明这个边界，会显得你知道标准和生态的差异。

一句话定位：Ingress 是 Kubernetes 集群入口的 HTTP/HTTPS 七层路由抽象，主场是入口路由和 TLS 终止；它依赖 Controller 落地，不负责通用 L4 暴露和完整服务间治理。

## 77. Kubernetes Ingress 在高可用设计中如何避免单点故障？

可以先这样答：Ingress 高可用不是让 Ingress YAML 多写几份，而是让 Ingress Controller 的数据面多副本、多节点、多可用区，并保证外部 LB、Service、证书、配置同步、默认后端和后端 Service 都可靠。Ingress 资源只是期望状态，真正接流量的是 Controller 或云厂商实现。

Controller 部署形态决定高可用基础。常见是 Deployment 多副本加 Service `LoadBalancer` 或 `NodePort`，也可以是 DaemonSet 让每个边缘节点都有入口 Pod。多副本要配合 pod anti-affinity、topology spread、PDB、HPA、资源 requests、独立节点池，避免两个 Controller Pod 被调度到同一台节点或同一故障域。前面的外部 LB 也要跨 AZ，把不健康 Controller Pod 或节点摘掉。

控制面和数据面要分开看。Controller 监听 Ingress、Service、EndpointSlice、Secret、ConfigMap 等对象，生成 Nginx/Envoy/HAProxy 配置并热更新。即使 controller 的 watch 或 leader election 出问题，已有数据面可能继续按旧配置服务；但新路由、证书更新、endpoint 变化会滞后。高可用要监控同步延迟、配置生成失败、reload 失败、最后成功配置版本，而不是只看 Pod Ready。

证书和配置是高频单点。TLS Secret 缺失、证书过期、IngressClass 写错、annotation 拼错、默认后端不可用、WAF/auth 插件异常、ConfigMap 错误，都能让入口大面积失败。成熟流程应该有 admission 校验、配置预检、灰度 IngressClass、canary ingress、快速回滚和明确的变更审计。Ingress Controller 要能拒绝坏配置或保留最后可用配置，避免一次错误 annotation 影响所有域名。

后端 Service 也要高可用。Ingress 只是把请求送到 Service；如果 Service 下面只有一个 Pod、Pod 都在单节点、readiness 太浅、PDB 缺失、滚动更新一次下掉全部副本，Ingress 多副本也救不了。入口高可用要把后端 Deployment、Service、EndpointSlice、HPA、数据库和缓存一起纳入演练。

对跨地域或多集群场景，Ingress 通常不是全局容灾答案。你需要 DNS/GSLB、全局 LB、Gateway API 多集群方案、服务网格多集群或云厂商全局入口，把流量引到可用集群。单集群内的 Ingress 解决的是集群边界入口，不能替代区域级流量调度和数据复制。

面试里可以直接说：Ingress 避免单点，关键是 Controller 数据面冗余、外部 LB 冗余、配置和证书治理、后端 Service 冗余，以及控制面异常时数据面能继续使用最后可用配置。

## 78. Kubernetes Ingress 与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：Ingress 的上下文传递由具体 Ingress Controller 决定，Kubernetes Ingress 规范本身只定义 HTTP/HTTPS 路由到 Service。常见做法是 Controller 在反向代理层处理 `X-Forwarded-For`、`Forwarded`、`X-Real-IP`、`X-Forwarded-Proto`、`X-Forwarded-Host`、request id、trace header 和 proxy timeout；但这些能力大多通过 controller 参数、ConfigMap 或 annotation 配置。

真实客户端 IP 要看上游链路。若 Ingress 前面是云 L4 LB，Controller 看到的源地址可能是 LB 或节点地址；可以通过 PROXY protocol 把 TCP 源地址传给 Controller，也可以让上游 L7 代理写 `X-Forwarded-For`。ingress-nginx 文档提醒，默认会使用 `X-Forwarded-For` 获取客户端 IP，但前提是正确配置 `proxy-real-ip-cidr`、`use-forwarded-headers`，必要时启用 `use-proxy-protocol`。没有可信 CIDR，直接信任 XFF 就等于允许用户伪造 IP。

协议传递常靠 forwarded 头和 backend protocol。Ingress 终止 TLS 后，下游 Service 可能收到 HTTP 明文；后端如果要知道外部原始协议，要看 `X-Forwarded-Proto`。如果后端是 HTTPS、HTTP/2、gRPC 或 GRPCS，需要按 Controller 规则声明 backend protocol。以 ingress-nginx 为例，gRPC/GRPCS 会继承 proxy connect/send/read timeout 相关配置。不要默认认为 Ingress 到后端的协议和客户端到 Ingress 的协议完全一致。

超时通常由 Controller 参数或 annotation 管。ingress-nginx 里常见有 `proxy-connect-timeout`、`proxy-send-timeout`、`proxy-read-timeout`、`proxy-next-upstream-timeout`、`proxy-next-upstream-tries`。这些是代理层超时，保护 Ingress worker 和后端连接；业务 deadline 仍然应由客户端、API Gateway、gRPC metadata 或应用 header 传递。若 Ingress 配置 60 秒，后端服务又给下游 60 秒，调用链会被拉长，最终用户等待时间不可控。

追踪上下文一般透传。`traceparent`、`tracestate`、`x-request-id`、`x-b3-*` 会随 HTTP 请求进入 Service，Ingress Controller 也可以生成或覆盖 request id，并把 route、ingress、service、namespace、upstream、status、latency 写到访问日志。细粒度 span 仍要靠 OpenTelemetry、service mesh sidecar 或应用 SDK。Ingress 记录入口日志，不等于后端每个服务都有 trace。

还要注意 Controller 差异。Nginx Ingress、HAProxy Ingress、Traefik、Contour/Envoy、AWS Load Balancer Controller 对 header、PROXY protocol、timeout、WebSocket、gRPC、HTTP/2、rewrite、canary 的配置方式并不相同。面试里说“Ingress 会传 XFF”不够严谨，更准确的是“具体 Controller 在可信代理链配置正确时可以传递或生成这些头”。

## 79. Kubernetes Ingress 如何做健康检查、摘除、恢复和流量预热？

可以先这样答：Ingress 的健康治理有三层：外部 LB 到 Ingress Controller 的健康检查，Controller 自身的 readiness/liveness 和配置健康，后端 Service/Pod 的 readiness 与 endpoint 变化。Ingress 资源本身不执行健康检查，真正动作由 Controller、Kubernetes Service/EndpointSlice、云 LB 和后端应用共同完成。

Controller 自身要有健康端点。外部 LoadBalancer 应只把流量发给 Ready 的 Controller Pod 或节点；Controller 进程启动后，要等配置加载、监听端口、证书和基础路由可用，再对外 Ready。配置生成失败或 reload 失败时，好的实现会保留旧配置并暴露错误指标。若 readiness 太浅，Controller Pod 刚启动就接流量，可能出现短暂 404、503、证书缺失或默认后端误命中。

后端摘除主要依赖 Service 和 EndpointSlice。Pod readiness 失败或 Pod 开始删除后，EndpointSlice 里的 ready 条件会变化，Ingress Controller 通过 watch 感知 endpoint 变化，更新 upstream。这个链路有传播延迟：kubelet 更新 Pod 状态，API server 存储，EndpointSlice controller 更新对象，Controller 收到 watch，生成配置或更新动态数据面。发布时要给 preStop、readiness 失败和 terminationGracePeriod 留足时间。

不同 Controller 对后端健康有不同增强。开源 ingress-nginx 通常更多依赖 Kubernetes endpoint 和 NGINX 被动错误处理；NGINX Plus、Envoy、HAProxy 或云厂商 Controller 可能支持主动 upstream health check、异常检测、熔断或更细的权重。面试里不要把某个实现的能力说成 Kubernetes Ingress 标准能力。Ingress 标准只定义资源模型，不定义所有健康检查行为。

恢复时，后端 Pod 一旦 Ready，就可能重新进入 Ingress upstream。冷启动服务要把 readiness 写准：缓存未加载、数据库连接池未建立、依赖不可用、JIT 未热、模型未加载时不要 Ready。Ingress 侧可以配合 canary ingress、权重路由、限流、upstream keepalive、连接池预热和 HPA 预扩容。Service 层没有 slow start，很多时候要靠 Controller 或发布系统控制回场速度。

Controller 下线也要 drain。滚动升级 Ingress Controller 前，先让外部 LB 停止发新连接到旧 Pod，再等待现有连接结束。WebSocket、SSE、gRPC streaming 和大文件上传下载尤其要注意，短 termination grace 会制造大量 499、502、504。Controller 的 reload 也要观测，频繁 Ingress/Endpoint 变化导致 reload storm，会让入口短时间抖动。

一句话总结：Ingress 健康治理的关键路径是“外部 LB 只打健康 Controller，Controller 只路由到 Ready endpoint，应用用 readiness 和优雅终止表达能否接流量，恢复时用 canary/限流/预热慢慢回场”。

## 80. Kubernetes Ingress 的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：Ingress 的观测要同时看外部 LB、Ingress Controller、Ingress 规则、TLS、配置 reload、后端 Service/Endpoint、上游响应和应用指标。判断 Ingress 自身成为瓶颈，要证明请求卡在 Controller 数据面、配置层、TLS、WAF/auth、rewrite、buffering 或 upstream 选择，而不是后端 Pod 慢。

入口请求指标包括按 host、path、Ingress、namespace、Service、status code、method 分组的 QPS、p50/p95/p99、请求体大小、响应体大小、连接数、活跃连接、TLS 握手失败、HTTP/2 stream、WebSocket 连接、4xx/5xx、499、502、503、504。ingress-nginx 的监控文档说明 Prometheus 指标可暴露在 10254 端口，并包含 request duration 这类请求处理指标。无论用哪个 Controller，都要把指标维度和 Kubernetes 对象维度关联起来。

Controller 状态指标也很重要：配置 reload 次数和耗时、reload 成功/失败、最后成功配置时间、Ingress/Service/EndpointSlice watch 延迟、workqueue depth、reconcile error、leader election 状态、controller Pod CPU/内存、worker 连接数、文件描述符、Nginx/Envoy worker 饱和、日志队列积压、证书过期时间、Secret 读取错误。很多入口事故不是流量太大，而是配置不断变化导致 reload 风暴或错误配置反复回滚。

后端指标要和 Ingress 对齐。Ingress 访问日志里通常能看到 upstream address、upstream status、upstream response time、request time、route、service、namespace、ingress name。若 request time 高但 upstream response time 正常，问题更像客户端上传、TLS、Ingress buffering、响应回写或 Controller 自身；若 upstream response time 高，后端 Pod 或下游依赖更可疑。要把 Ingress 日志、Service endpoint、Pod 日志、应用 trace 放在同一条时间线。

判断 Ingress 自身成为瓶颈，可以看几个信号。第一，直连 Service 或 Pod 正常，经 Ingress p99 高、错误多。第二，Ingress Controller CPU、worker connection、event loop、Nginx reload、TLS handshake、buffering 或文件描述符接近上限。第三，Ingress 生成的 502/503/504 上升，而后端没有收到请求或后端处理时间正常。第四，减少 rewrite/auth/WAF/限流/日志策略后延迟下降。第五，增加 Controller 副本、拆分热点域名或提升 LB 到 Controller 带宽后改善。

还要警惕外部 LB 和客户端造成的假象。云 LB 到节点的健康检查、跨可用区流量、SNAT、PROXY protocol、DNS、CDN 缓存、客户端慢上传、移动网络抖动，都可能让 Ingress 看起来慢。Ingress Controller 只是链路中的七层入口，不是全链路唯一解释。排障时按“客户端到外部 LB、外部 LB 到 Controller、Controller 到 Service、Service 到 Pod、Pod 到下游依赖”切开。

面试里可以收成一句：Ingress 观测的核心是“请求维度、配置维度、Controller 资源维度、upstream 维度和 Kubernetes 对象维度对账”。只有这些证据指向 Controller 或 Ingress 配置，才说 Ingress 自身成为瓶颈。

## 81. Gateway API 位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：Kubernetes Gateway API 首先是 Kubernetes 里的流量治理 API 模型，不是某一个固定的数据面代理。它通过 GatewayClass、Gateway、Route 等资源描述“谁提供网关能力、在哪里监听、按什么规则把请求转给后端 Service”。在企业流量链路里，它主要处在集群入口和七层应用路由这一层；如果启用 Gateway API for Service Mesh，也可以覆盖一部分东西向服务间流量。

更准确地说，GatewayClass 表示一类网关实现，比如云厂商 LB、Envoy Gateway、Istio、Kong、NGINX 这类控制器提供的能力；Gateway 表示某个具体网关实例的期望状态，包括地址、监听端口、协议、TLS 配置和允许哪些 Route 绑定；HTTPRoute、GRPCRoute、TLSRoute、TCPRoute、UDPRoute 描述具体流量匹配和转发规则。用户写的是 Kubernetes 对象，真正接包、建连、转发、限流、熔断、打日志的是对应实现的数据面。

从南北向入口看，Gateway API 是 Ingress 的后继式增强。Ingress 只有一个相对薄的 HTTP 入口模型，很多能力要靠 controller-specific annotation 扩展；Gateway API 把角色拆得更清楚：平台团队管 GatewayClass/Gateway，应用团队管 Route，跨 namespace 绑定、listener、allowedRoutes、status condition 都有更明确的语义。因此它适合做集群入口、域名和路径路由、TLS 终止、灰度权重、请求 header 修改、超时等七层能力。

从东西向看，要分普通 Gateway API 和 mesh 场景。传统 Gateway 资源更多表达“流量从网关进入集群，再转到 Service”。Gateway API for Service Mesh 则允许 Route 直接挂到 Service 上，让 mesh 数据面在服务调用时应用路由规则。比如客户端 Pod 调用 reviews Service，sidecar 或 mesh 数据面拦截后，根据绑定到 Service 的 HTTPRoute/GRPCRoute 选择 v1、v2 或按权重分流。这个时候它解决的是东西向服务间路由，但前提是 mesh 实现支持相应能力。

所以面试里不要把 Gateway API 说成“又一个 API Gateway 产品”。API Gateway 通常是面向业务 API 的入口产品，强调鉴权、限流、开发者门户、API lifecycle；Kubernetes Gateway API 是 Kubernetes 网络资源规范，强调可移植的网关和路由模型。它可以被 API Gateway、Ingress Controller、service mesh 或云 LB 实现，但它本身不是那个代理进程。

如果一句话收束：Gateway API 位于 Kubernetes 流量治理的网关和七层路由抽象层，主战场是南北向入口和应用路由；在 GAMMA/mesh 模式下也能扩展到东西向服务路由。判断它在链路中的位置时，要把“API 对象”和“具体实现的数据面”分开。

## 82. Gateway API 在高可用设计中如何避免单点故障？

可以先这样答：Gateway API 自身是 Kubernetes API 对象，它不直接提供高可用；真正的高可用要落在 Gateway controller、数据面代理、外部负载均衡器、后端 Service、证书和配置依赖上。设计时要避免把“有 Gateway 资源”误认为“网关已经高可用”。

第一层是控制面的高可用。Gateway controller 负责 watch GatewayClass、Gateway、Route、Service、Secret 等对象，并把期望状态翻译成云 LB、Envoy、NGINX 或 mesh 数据面配置。controller 应该多副本部署，配 leader election 或者按实现支持的方式安全并发 reconcile；它的 RBAC、CRD、webhook、admission、status 写回都不能成为单点。还要监控 reconcile queue、失败次数、配置下发延迟、status.conditions 里的 Accepted、Programmed、ResolvedRefs 和 observedGeneration。若 observedGeneration 长期落后，说明用户看到的对象版本和控制器实际处理的版本可能不一致。

第二层是数据面的高可用。Gateway 背后的代理或 LB 应该多副本、多节点、多可用区部署，并放在稳定的 Service、云 LB、Anycast 或 L4 LB 后面。对于 Envoy Gateway、Istio ingress gateway、NGINX 等实现，至少要考虑 Pod 反亲和、PodDisruptionBudget、HPA、资源 request/limit、滚动升级 maxUnavailable、节点故障摘除、连接 drain 和证书热更新。对于云厂商实现，还要确认 Gateway 对应的 LB 是否跨 AZ、是否有健康检查、是否支持静态地址或 DNS failover。

第三层是配置依赖的高可用。Gateway 的 TLS 证书通常来自 Secret 或证书控制器；Route 可能跨 namespace 引用 Service、Secret 或 backend，需要 ReferenceGrant 或 allowedRoutes 这类授权关系；这些对象一旦缺失，Gateway API 的 status 可能变成 ResolvedRefs=False 或 Programmed=False。生产环境要把证书续期、Secret 分发、跨 namespace 引用、后端 Service 生命周期纳入变更流程，而不是只盯着 Gateway Pod 是否存活。

第四层是变更面的高可用。Gateway API 的好处是把平台配置和业务路由拆开，但这也意味着错误 Route、错误权重、错误 listener、错误 hostname 都可能影响入口。可以用命名空间隔离、allowedRoutes、route delegation、准入校验、灰度发布、先小流量权重再扩大、回滚模板和状态检查来控制风险。对热点域名或核心业务，不建议所有流量都压在单个 Gateway、单个 GatewayClass 或单个 controller 实现上，至少要有容量隔离和故障域划分。

还要区分控制面故障和数据面故障。controller 暂时不可用时，已经下发到数据面的旧配置通常还能继续跑；但新 Route 不会生效，证书更新、endpoint 更新或故障摘除可能延迟。数据面不可用时，即使 Kubernetes 对象状态看起来正常，请求也会失败。因此高可用排障要同时看 API 对象 status、controller 日志、数据面健康、外部 LB 健康检查和真实探测。

一句面试答案可以是：Gateway API 避免单点故障，不是靠这个 API 资源本身，而是靠“多副本 controller、多副本数据面、多可用区入口、健康检查和连接排空、配置依赖冗余、状态条件校验、灰度变更”一起保证。Gateway API 提供标准化控制平面语义，HA 仍然要由具体实现和部署拓扑兑现。

## 83. Gateway API 与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：Gateway API 能标准化一部分 HTTP/gRPC 路由、header 修改和 timeout 表达，但真实客户端 IP、原始协议、PROXY protocol、trace header 信任边界这些细节，往往取决于具体 Gateway 实现和前后置组件。回答时要说清楚“规范能表达什么”和“实现要怎么落地”。

客户端真实 IP 通常来自三类位置。第一类是 TCP 连接源地址，前提是前面没有 SNAT，或者外部 LB 以保留源地址的方式转发。第二类是 HTTP 层 header，比如 X-Forwarded-For、Forwarded、X-Real-IP；这要求 Gateway 只信任来自可信上游 LB/CDN 的 header，并在边界处覆盖或追加，而不能盲目信任公网客户端自带的值。第三类是 PROXY protocol，由 L4 LB 在连接前部写入源地址和端口，后端 Gateway 数据面需要显式启用解析。Gateway API 本身不会自动保证真实 IP 正确，它更多是让实现把请求转到 backendRefs；IP 传递策略一般是实现特定的 policy、annotation 或 controller 配置。

协议传递要看 listener 和 Route 类型。Gateway listener 会声明 protocol、port、hostname、TLS 模式等；HTTPRoute 使用 HTTP 请求信息做匹配和修改，GRPCRoute 面向 gRPC over HTTP/2，TLSRoute 可以做 TLS passthrough 或基于 SNI 的转发，TCPRoute/UDPRoute 面向四层。若 Gateway 终止 TLS，下游看到的是解密后的 HTTP/gRPC，请求可带 X-Forwarded-Proto、Forwarded proto 或类似 header；若是 passthrough，Gateway 通常只能看 SNI 和四层信息，不能改 HTTP header，也不能基于 path 做路由。

超时方面，Gateway API 的 HTTPRoute 已经有 timeouts 字段，可以表达 request timeout 这类七层行为。它描述的是网关从收到请求到完成响应的等待边界，具体执行仍由数据面实现。生产里还要把它和上游 CDN/LB 超时、Gateway 到后端连接超时、应用 server 超时、数据库/下游 RPC 超时对齐。常见问题是外层超时比内层短，导致 Gateway 先返回 504；或者内层超时比外层短，应用已经放弃但 Gateway 还在等待。面试里可以说“超时要按调用链从外到内逐层收敛，并且用 trace/log 验证是谁先超时”。

追踪上下文一般通过 HTTP header 传递，比如 W3C Trace Context 的 traceparent/tracestate、B3、x-request-id 或厂商自定义 header。Gateway API 的 HTTPRoute 支持 RequestHeaderModifier/ResponseHeaderModifier，可以添加、设置、删除 header；但是否自动生成 trace、是否采样、是否把 access log 和 trace 关联，取决于 Envoy、Istio、NGINX、云 LB 等实现。规范可以表达“改哪些 header”，不等同于完整的分布式追踪系统。

还要注意信任边界。真实客户端 IP 和 trace id 都可能被客户端伪造，因此入口 Gateway 应该在可信边界重写或校验这些 header；内部服务只信任来自 Gateway 或 mesh 的转发信息。跨多层代理时，要定义每一层是 append 还是 overwrite，日志里记录 remote address、XFF 链、Forwarded、request id、route、backend service，这样排障时才不会把 CDN、LB、Gateway、Service Mesh 的信息混在一起。

一句话收束：Gateway API 可以用 listener、Route、header modifier、timeout 等资源表达协议和七层路由意图；真实 IP、PROXY protocol、trace 注入、header 信任和超时执行则由具体实现负责。高质量答案要同时讲 API 能力、实现差异和边界安全。

## 84. Gateway API 如何做健康检查、摘除、恢复和流量预热？

可以先这样答：Gateway API 标准资源主要描述网关、监听器、路由和后端引用，它不是一个完整的健康检查协议规范。后端健康、摘除、恢复和预热通常由 Kubernetes Service/EndpointSlice、Pod readiness、外部 LB、Gateway 数据面和实现特定 policy 共同完成。

最基础的摘除来自 Kubernetes 自身。Gateway API 的 Route 通常把流量转给 Service，Service 背后由 EndpointSlice 维护可用 endpoint。Pod readiness probe 失败、Pod terminating、selector 不匹配或 endpoint 被移除后，Gateway 实现应通过 watch Service/EndpointSlice 更新后端集合。也就是说，对普通后端 Pod，第一道健康判断仍然是 readiness，而不是 Gateway API 资源自己去探测每个 Pod。

第二层是 Gateway 数据面的健康能力。很多实现底层是 Envoy、NGINX、云 LB 或 service mesh，它们可能支持主动健康检查、被动异常摘除、连接失败摘除、熔断、重试、outlier detection、panic threshold、慢启动等能力。Gateway API 核心规范不一定统一这些字段，所以常见做法是通过实现特定的 BackendPolicy、HealthCheckPolicy、annotation 或厂商 CRD 补充。面试时可以说：Gateway API 提供可移植的主干模型，但健康检查细节要看实现，不应把某个 controller 的扩展说成所有 Gateway API 都有。

流量摘除可以分几种。配置摘除是把 HTTPRoute 的 backendRefs 权重降为 0，或者从 Route 中移除某个 backend；健康摘除是 readiness 失败或主动健康检查失败后从负载均衡池移除；故障摘除是数据面根据 5xx、连接失败、超时等异常做临时 eject；发布摘除是 Pod 进入 termination 时先 readiness 变 false，再等待 endpoint 传播、连接 drain 和 terminationGracePeriod。真正稳定的摘除要给控制面传播和长连接排空留时间。

恢复也不能只看 Pod Running。至少要满足 readiness 通过、EndpointSlice 已更新、Gateway controller 已 reconcile、Gateway/Route status 里相关条件为 True、数据面已收到配置并完成 listener/cluster warming。Gateway API troubleshooting 文档强调可以看 Accepted、Programmed、ResolvedRefs、observedGeneration 等 status 条件；这些条件能告诉你配置是否被接受和编程，但不等价于后端业务已经健康。恢复验证还需要真实探测、合成请求、访问日志和后端指标。

流量预热主要靠权重和实现能力。HTTPRoute backendRefs 支持 weight，可以把新版本先设成 1%、5%、10% 逐步放量，也可以做 blue-green 和 canary。对于刚启动的后端，还要让应用完成缓存预热、连接池预建、JIT/类加载、配置加载，再把 readiness 打开。若底层数据面支持 slow start，可以让新 endpoint 在一段时间内逐步接收更多请求；如果不支持，就用 Route 权重、分批扩容和发布系统控制节奏。

一句话回答：Gateway API 的健康治理是“Kubernetes readiness/EndpointSlice 负责基础可用性，Gateway controller 负责把变化编程到数据面，具体实现负责主动检查、异常摘除、连接 drain 和慢启动，HTTPRoute 权重负责灰度预热”。不要把 Gateway API 的标准对象和某个实现的高级健康策略混为一谈。

## 85. Gateway API 的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：Gateway API 的观测要分三层看：Kubernetes API 对象状态、Gateway controller 状态、实际数据面请求指标。因为 Gateway API 是规范和控制面模型，真正的瓶颈通常出现在 controller reconcile、数据面代理、外部 LB 或后端 Service，而不是 YAML 对象本身。

第一类是对象状态指标。要看 GatewayClass、Gateway、HTTPRoute/GRPCRoute 等资源的 status.conditions，比如 Accepted、Programmed、ResolvedRefs；看 observedGeneration 是否等于当前 metadata.generation；看 listener 是否 attachedRoutes 符合预期；看 Route 是否成功绑定到 parentRefs；看 backendRefs 是否解析成功。若用户改了 Route 但 observedGeneration 一直没追上，或者 Programmed=False，问题更像控制器没有把配置推到数据面。

第二类是 controller 指标。包括 reconcile 次数和耗时、workqueue depth、reconcile error、API server watch 延迟、leader election 状态、配置生成耗时、配置下发失败、Secret/Service/EndpointSlice 读取失败、CRD 版本兼容问题、webhook/admission 延迟、controller Pod CPU/内存、重启次数。Gateway API 引入了更丰富的对象关系，跨 namespace 引用、allowedRoutes、ReferenceGrant、hostname/listener 匹配都可能导致配置没生效，所以 controller 层的错误原因必须能被定位到具体资源。

第三类是数据面请求指标。要看 QPS、并发连接、活跃 stream、p50/p95/p99、4xx/5xx、502/503/504、TLS 握手耗时和失败、请求体/响应体大小、连接建立失败、upstream connect time、upstream response time、重试次数、超时、限流、熔断、队列积压、文件描述符、CPU、内存、网络带宽。若实现基于 Envoy，还要看 listener、cluster、upstream_rq_timeout、upstream_rq_pending_overflow、cx_active、rq_active、xDS 连接和 warming 状态；若基于云 LB，则看云 LB 的 target health、LB 容量、跨 AZ、日志和 CloudWatch/厂商指标。

判断 Gateway API 相关组件成为瓶颈，要先做对比。第一，直连 Service 或通过集群内访问正常，经 Gateway 路径 p99 或错误率显著变差。第二，Gateway 数据面资源接近上限，或者扩容 Gateway 副本后延迟下降。第三，数据面日志显示请求没有到达 backend，或者 Gateway 生成 503/504，而后端没有对应慢请求。第四，controller 的 Programmed 延迟、xDS 下发延迟或配置 reload 风暴与故障时间吻合。第五，关闭复杂 header rewrite、auth、rate limit、WAF、mirroring 或过滤器后性能恢复。

也要避免误判。Gateway status 正常只能说明配置被接受和编程，不说明后端业务快；Gateway 请求慢也可能是客户端慢上传、外部 LB、DNS、CDN、跨区网络、后端数据库或应用锁竞争导致。排障时最好用同一个 request id 串联 access log、Gateway route、backend service、Pod log 和 trace span，并用直连、旁路、单 backend、降级策略做对照实验。

一句话收束：Gateway API 的观测核心是“对象状态证明配置是否成立，controller 指标证明配置是否及时下发，数据面指标证明请求是否卡在网关”。只有对照路径和指标都指向 controller 或数据面，才说 Gateway 这层成为瓶颈。

## 86. Service Mesh sidecar 位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：Service Mesh sidecar 位于应用实例旁边的服务间通信数据面，通常和业务容器在同一个 Pod 内，由 Envoy 这类代理拦截入站和出站流量。它主要解决东西向服务间流量治理，同时也承担应用层路由、mTLS、重试、超时、熔断、负载均衡和遥测。它不是企业公网入口的第一层，但会和 ingress gateway、egress gateway 一起组成完整 mesh 流量链路。

以 Istio 为例，官方架构把 Istio 分成 data plane 和 control plane。data plane 是一组 Envoy sidecar，负责在微服务之间调解和控制网络通信，并报告 telemetry；control plane 负责管理和配置这些代理。sidecar 的关键点是“贴着工作负载”，每个服务实例的流量都先经过本地代理，再到目标服务的代理或后端。这样做的好处是应用代码不必自己实现 mTLS、服务发现、灰度、熔断和统一日志。

从链路位置看，入口流量通常先到 DNS/CDN/L4 LB/API Gateway/Ingress/mesh ingress gateway，再进入集群内服务。一旦流量进入 mesh，sidecar 开始接管服务到服务的调用。比如 checkout 调用 payment，checkout Pod 的 outbound sidecar 根据服务发现和路由规则选择 payment 的某个 endpoint；payment Pod 的 inbound sidecar 再把请求交给业务容器。这里解决的是东西向 east-west 流量，而不是单纯的外部入口。

从能力看，sidecar 也做应用路由。它可以按 host、path、method、header、gRPC method、subset、权重、故障注入、mirror 等规则选择后端版本；可以做 HTTP/2、gRPC、TLS 终止或透传；可以基于 DestinationRule/VirtualService 或 Gateway API mesh 模式应用规则。但它做的是网络和协议层面的应用路由，不是业务代码里的订单状态流转，也不应该把业务分支逻辑塞进 sidecar。

它和 Kubernetes Service 的关系也要说清楚。Service 提供稳定服务名和 endpoint 抽象，sidecar 在此基础上接收 control plane 下发的服务发现、负载均衡和路由配置。没有 sidecar 时，调用方可能直接走 kube-proxy/IPVS/iptables 到 Service；有 mesh 后，流量常被 iptables、CNI 或 eBPF 重定向到本地代理，再由代理按更细的策略转发。

一句话总结：Service Mesh sidecar 是贴近应用实例的 L4/L7 服务通信代理，主战场是集群内东西向流量治理，同时提供应用层路由和可观测性。公网入口通常交给 ingress gateway 或 API Gateway，sidecar 负责流量进入服务体系后的细粒度治理。

## 87. Service Mesh sidecar 在高可用设计中如何避免单点故障？

可以先这样答：sidecar 模式的一个优势是数据面分散在每个工作负载旁边，不依赖一个中心代理转发所有服务间请求；但每个 Pod 又依赖自己的本地 sidecar，所以高可用要同时看 workload 副本、sidecar 注入、控制面、证书、配置下发和资源隔离。不能简单说“sidecar 没有单点”。

数据面层面，sidecar 是按 Pod 分布的。某个 sidecar 挂了，通常只影响它所在 Pod 的入站或出站流量，不会像中心代理那样拖垮全网；但对这个 Pod 来说，sidecar 就是本地通信路径上的关键组件。因此服务本身必须多副本部署，配合 Deployment、HPA、PDB、反亲和和滚动升级策略，让单个 Pod 或节点故障不会导致服务不可用。sidecar 容器的 CPU、内存、文件描述符、连接数也要独立 sizing，避免业务容器正常但代理被限流或 OOM。

控制面层面，Istio 这类系统由 istiod 或类似 control plane 向 sidecar 下发 xDS 配置、服务发现和证书。控制面应该多副本、多节点部署，保护好 webhook、CA、证书签发、配置推送和 leader election。控制面短时间不可用时，已经运行的 Envoy 往往还能用旧配置继续转发；但新 Pod 注入、新证书、新路由、新 endpoint 更新可能受影响。证书过期时间、SDS、xDS 连接状态、push error 都是高可用风险点。

注入链路也不能成为单点。sidecar 通常通过 mutating admission webhook 在 Pod 创建时自动注入，namespace label 或 pod label 决定是否启用。如果 webhook 不可用、配置错误、镜像拉取失败或 init container/CNI 失败，新 Pod 可能创建失败，或者创建出来没有加入 mesh。生产上要监控注入成功率、webhook 延迟、sidecar 镜像可用性、CNI daemonset 健康、iptables/eBPF 规则安装情况。

变更层面，mesh 配置是全局影响很强的系统。错误的 VirtualService、DestinationRule、PeerAuthentication、AuthorizationPolicy 或 Gateway API Route 可能让大量 sidecar 同时接收坏配置。要用配置校验、命名空间隔离、渐进发布、revision-based control plane、canary control plane、先小范围注入、再扩大范围的方式降低 blast radius。升级 sidecar 也要分批，不要一次性重启全网核心服务。

还要处理优雅下线。Pod termination 时要让 readiness 先变 false，endpoint 从服务发现里摘除，sidecar 完成 drain，再退出业务容器和代理。否则会出现控制面认为 endpoint 还可用、客户端 sidecar 继续发请求、服务端业务容器已经退出的 503/connection reset。高可用不仅是“挂了能切”，还包括“发布和缩容时不丢请求”。

一句话收束：sidecar 避免中心数据面单点，但每个 Pod 的本地 sidecar 仍是该实例的关键路径。高可用依赖服务多副本、control plane HA、注入链路可靠、证书和 xDS 稳定、资源隔离、渐进变更和优雅 drain。

## 88. Service Mesh sidecar 与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：sidecar 位于每个服务实例的入站和出站路径上，它既能看到 L4 连接，也能在识别出 HTTP/gRPC 时处理 L7 header。真实客户端 IP、协议、超时和 trace context 的传递，要区分外部入口到 mesh、mesh 内服务到服务、本地 sidecar 到应用容器这几段。

真实客户端 IP 在 mesh 里最容易被误解。外部用户的真实 IP 通常先由 CDN/LB/ingress gateway 写入 X-Forwarded-For 或 Forwarded，进入 mesh 后再由 sidecar 继续转发。服务间调用时，TCP 源地址可能是本地代理、远端代理或节点地址，不能简单把 socket remote address 当成最终用户 IP。mesh 更可靠的身份来源通常是 mTLS 工作负载身份，比如 SPIFFE/SAN，而不是原始 IP。若业务确实需要最终用户 IP，应在入口可信边界规范化 header，并禁止内部服务信任未经入口重写的客户端自带 XFF。

协议传递依赖协议识别和端口声明。sidecar 可以代理 TCP、HTTP/1.1、HTTP/2、gRPC、TLS 等流量；Istio/Envoy 通常通过端口命名、协议选择、ALPN、SNI 或 sniffing 判断协议。识别为 HTTP/gRPC 后，sidecar 才能做 path/header/method 级路由、trace header 传播、HTTP 重试和超时；如果只是 opaque TCP，它只能做四层负载均衡、连接级指标和 TLS/mTLS 相关处理。协议声明不准会导致路由规则不生效，或者把长连接协议误当 HTTP 处理。

超时通常从 mesh 路由策略进入 sidecar。Istio 的 VirtualService 可以为 HTTP route 设置 timeout；Gateway API 的 HTTPRoute 在实现支持时也能设置 request timeout；Envoy 还支持 per-request header，比如 x-envoy-upstream-rq-timeout-ms。要注意应用自己的 deadline 可能更短，客户端 SDK 的超时也可能更短，所以最终是谁先放弃，要看 client、sidecar、server、下游依赖的超时链。实践里建议外层超时略大于内层，并让业务 deadline 随请求传播，否则重试和超时会放大流量。

追踪上下文方面，Envoy sidecar 可以生成 span、记录 access log，并把指标上报给 telemetry 系统；但官方文档也强调，应用必须在出站请求中转发 trace header，sidecar 才能把跨服务调用串成完整 trace。常见 header 包括 W3C traceparent/tracestate、B3、x-request-id、x-b3-* 等。sidecar 可以在入口生成 request id，也可以按采样策略上报，但如果业务代码没有把上下文从入站请求带到出站请求，trace 会在服务边界断掉。

上下游传递还涉及安全边界。sidecar 可以追加或覆盖 X-Forwarded-Client-Cert、XFF、request id 等 header，但这些 header 哪些可信，要由入口网关和 mesh policy 定义。内部服务应优先信任 mTLS 身份和授权策略，而不是随便读取某个 header 当身份。对于跨 mesh、跨集群或出 mesh 的流量，还要通过 egress gateway 或边界代理统一处理 header、SNI、证书和审计。

一句话回答：sidecar 传递上下文靠协议识别、标准 header、mTLS 身份、路由 timeout 和 telemetry 机制共同完成。真实用户 IP 通常从入口 header 来，服务身份通常从 mTLS 来，trace header 需要应用配合转发，超时要和客户端、代理、服务端 deadline 对齐。

## 89. Service Mesh sidecar 如何做健康检查、摘除、恢复和流量预热？

可以先这样答：sidecar 的健康治理分两条线：一条是 Kubernetes 对业务 Pod 的 liveness/readiness/startup probe，另一条是 mesh 数据面对后端 endpoint 的发现、异常摘除、熔断和连接排空。两条线要配合，否则很容易出现“Pod 看起来活着，但 mesh 里不可用”或“mesh 代理活着，业务容器已经坏了”。

先看健康检查。启用 mTLS 后，Kubelet 直接探测业务容器的 HTTP endpoint 可能失败，因为 Kubelet 没有 mesh 里的证书；TCP probe 也可能被 sidecar 的端口重定向影响，出现代理端口可连但业务容器不健康的假阳性。Istio 针对这个问题会把应用探针改写到 sidecar agent，再由 agent 去探测本地应用。面试里要说清楚：健康检查不是简单照搬无 mesh 的 probe 配置，mTLS、iptables 重定向和探针改写都会改变行为。

摘除的第一步仍然是 readiness。业务容器 readiness 失败时，Pod 应从 Service endpoint 中移除，control plane 把 endpoint 变化推给各个 sidecar，客户端 sidecar 不再把新请求发到这个实例。第二步是数据面异常摘除，比如 Envoy/Istio 可以通过 outlier detection 根据连续 5xx、连接失败、网关错误等临时 eject 某个 endpoint；DestinationRule 还能配置 connection pool、pending requests、max connections 等熔断参数。readiness 处理“我声明自己不可用”，outlier detection 处理“调用方观察到你异常”。

恢复时也要等多个条件同时成立。业务容器要完成启动、依赖连接、缓存加载，readiness 通过；sidecar 要完成注入、拿到证书、连上 istiod 或控制面、收到 listener/cluster/route/endpoint 配置，并完成必要 warming；Service/EndpointSlice 变化要传播到其他客户端 sidecar。只要其中一环还没完成，提前放量就可能出现 503、NR、UF、UH、连接拒绝或首批请求超时。

优雅下线是 mesh 场景里的重点。Pod 准备退出时，应先让 readiness 变 false，等待 endpoint 从服务发现中消失，再让 sidecar drain 现有连接和请求，最后退出容器。对于 HTTP/2、gRPC、WebSocket 这类长连接，还要考虑连接 drain 时间、客户端重连和服务端 shutdown hook。若业务容器先退出、sidecar 后感知，客户端可能继续打到一个正在终止的实例。

流量预热可以靠三种方式。第一，发布系统层面分批扩容和分批发布，让新实例逐步进入 endpoint。第二，路由层面用 VirtualService 或 Gateway API Route 权重，把新版本从小权重开始放量。第三，数据面层面使用 slow start、连接池预建、异常摘除和熔断保护，避免刚恢复的实例瞬间被打满。应用层也要在 readiness 前完成热点缓存、数据库连接池和外部依赖初始化。

一句话收束：sidecar 的健康治理不是 sidecar 自己单独完成的，而是 Kubernetes probe、Service endpoint、control plane xDS、Envoy 异常摘除、连接 drain 和灰度权重共同完成。稳定的 mesh 发布要把“探测、摘除、恢复、预热、下线”都放进同一个时序里设计。

## 90. Service Mesh sidecar 的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：sidecar 的观测要同时覆盖应用请求、代理资源、xDS 配置、mTLS、连接池、重试超时和遥测链路。判断它自身成为瓶颈，关键是证明请求卡在本地代理或远端代理，而不是业务容器、后端依赖、节点网络或入口网关。

第一类是 mesh 标准请求指标。Istio 官方把观测指标组织在 latency、traffic、errors、saturation 四个黄金信号上。常见维度包括 source workload、destination workload、namespace、service、response code、response flag、protocol、method、route、request duration、request/response bytes、TCP sent/received bytes。它们用于回答“哪个服务调用哪个服务慢、错误率是否上升、是 HTTP 错误还是连接错误、影响哪个版本或命名空间”。

第二类是 Envoy/sidecar 代理指标。要看 proxy CPU、内存、重启、文件描述符、活跃连接、连接池连接数、pending request、upstream request active、retry、timeout、circuit breaker overflow、outlier ejection、TLS handshake、listener/cluster warming、downstream reset、upstream reset、response flag。Istio 默认只暴露一部分 Envoy 统计以降低 CPU 和内存开销，需要时可以开启额外统计，比如 circuit breakers、upstream connections、timeouts、retries 等。

第三类是控制面和配置指标。sidecar 是否连着 xDS，配置版本是否更新，SDS 证书是否正常，证书多久过期，istiod push 是否失败，proxy sync status 是否一致，VirtualService/DestinationRule/AuthorizationPolicy 是否被正确应用。很多 mesh 故障不是数据面算力不足，而是配置没有下发、下发了错误配置，或者某批代理和控制面的视图不一致。

第四类是日志和 trace。Envoy access log 的 response_code、response_flags、duration、upstream_service_time、upstream_host、route_name、authority、request_id 能帮助区分是本地代理拒绝、上游连接失败、上游超时，还是后端返回错误。分布式 trace 可以看到时间花在客户端应用、客户端 sidecar、网络、服务端 sidecar、服务端应用还是下游依赖。没有 trace 时，至少要把 sidecar access log 和应用 log 用 request id 对齐。

判断 sidecar 自身成为瓶颈，可以看几个证据。第一，业务容器内部处理时间正常，但客户端看到的端到端延迟高，sidecar access log 或 trace 显示代理段耗时高。第二，绕过 sidecar 或在同节点直连业务端口明显变快，而走 mesh 路径变慢。第三，sidecar CPU throttle、内存高、连接池 pending、upstream_rq_pending_overflow、retry/timeout、circuit breaker overflow 与故障同步。第四，给 sidecar 增加资源、拆分流量、降低 telemetry 采样或关闭高成本过滤器后延迟下降。第五，只有注入 sidecar 的版本有问题，未注入或旁路路径正常。

也要避免把 sidecar 当成所有问题的替罪点。后端数据库慢、应用线程池满、节点 conntrack 耗尽、CNI 网络抖动、DNS 慢、ingress gateway 饱和、外部依赖超时，都可能表现为 mesh 请求慢。可靠判断需要同一请求在客户端应用、客户端 sidecar、服务端 sidecar、服务端应用四个点都有时间戳或 trace span，再用对照路径验证。

一句面试答案可以收成：sidecar 观测的核心是“黄金信号看服务调用，Envoy 指标看代理资源和连接池，xDS/SDS 看配置和证书，access log/trace 做分段归因”。只有分段证据显示耗时、错误或队列发生在代理层，才说 sidecar 自身成为瓶颈。

## 91. 客户端 SDK 负载均衡位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：客户端 SDK 负载均衡位于调用方进程内，处在应用代码和传输协议之间。它通常和服务发现、RPC channel、连接池、重试、超时、熔断这些逻辑放在同一个客户端库里。它主要解决东西向服务调用里的实例选择问题，也就是一个服务调用另一个服务时，客户端应该把这次请求发到哪个后端实例。它不是公网入口层，也不是 CDN、API Gateway、Ingress 那种集中入口组件。

以 gRPC 为例，官方文档把客户端侧负载均衡拆得很清楚：name resolver 给出后端地址和 LB 配置，load balancing policy 维护到后端的 subchannel，picker 在每次 RPC 发起时选择一个可用 subchannel。这个模型说明客户端 SDK LB 不站在服务端前面收流量，而是分散在每个调用方里。每个调用方都有自己的地址视图、连接状态和选择器。

它解决的典型问题是内部服务到服务的分流。比如订单服务调用库存服务，客户端 SDK 可以根据服务发现拿到库存服务的多个实例，再按 round robin、pick first、P2C、EWMA、权重、可用区、灰度版本、健康状态来选一个实例。AegisMesh 这类系统更偏这一层：把负载均衡、端点状态、慢实例避让和重试预算放在调用方，减少对集中代理的依赖。

它也能参与应用路由，但边界要说清楚。SDK 可以按 RPC service/method、目标服务名、租户、region、版本标签、metadata 或 service config 选择不同 subset；也可以让不同 API 使用不同超时、重试和负载均衡策略。可是它不适合承接所有业务路由。订单状态机、权限决策、复杂 SQL 选择、支付风控分支仍应留在业务层或专门网关里。SDK 负责“这次调用走哪个后端”，不是负责“业务流程怎么走”。

入口流量场景里也可能出现“客户端 SDK 选入口”，例如移动端 SDK 在多个边缘域名、多个 region endpoint 之间做选择。但企业架构里更常见的入口治理仍在 DNS、GSLB、CDN、WAF、L4/L7 LB、API Gateway 和 Ingress 上完成。公网客户端环境复杂、版本不可控、升级慢，把入口核心切流逻辑压到外部客户端 SDK 里，风险通常比收益大。

一句话收束：客户端 SDK 负载均衡是调用方内嵌的数据面，主战场是东西向 RPC 和服务发现后的实例选择；它可以做一部分方法级或版本级应用路由，但不应被当成企业公网入口网关。

## 92. 客户端 SDK 负载均衡在高可用设计中如何避免单点故障？

可以先这样答：客户端 SDK LB 的优势是没有一个集中数据面代理承接所有请求，选路动作分散在每个调用方进程里；但它仍然可能被服务发现、配置中心、控制面、证书、SDK 版本和错误发布变成“逻辑单点”。所以高可用设计不是只说“没有中心代理”，而是要把数据面、控制面和发布面分开治理。

数据面首先要本地可用。每个客户端进程应保存 last-known-good 的 endpoint 列表、LB 配置和熔断状态。服务发现短暂不可用时，客户端不要马上清空地址表，也不要把所有 RPC 都打失败；更稳的做法是继续使用最近一次可用快照，同时标记配置过期、降低风险策略，后台继续重连发现系统。若地址表确实为空，要有清晰 fail-fast、wait-for-ready 或降级策略，不能在业务线程里无限等待。

控制面要多副本、多路径。服务注册中心、xDS 控制面、配置中心、证书系统和 DNS 都可能影响客户端 LB。生产里要部署多个控制面实例，跨节点或跨可用区；客户端连接控制面时要有重试、指数退避、抖动和连接上限；配置推送要版本化、可回滚、可灰度。gRPC 对自定义 LB 和 xDS 的支持说明客户端 LB 可以由控制面配置，但这也意味着控制面坏配置会被所有客户端一起执行，blast radius 很大。

还要避免“同一时刻同一决策”。客户端 LB 分散以后，另一个风险是全体客户端同时观察到同一个新实例、同一个低延迟 endpoint、同一个健康恢复信号，然后一起打过去。治理手段包括随机化、P2C、权重抖动、启动抖动、配置分批下发、按客户端分桶、slow start、最大连接增长速率和重试限流。分散数据面只有在决策也不过度同步时，才真的减少系统性故障。

SDK 版本也是高可用边界。服务端代理出问题可以集中回滚一组网关；客户端 SDK 出问题时，可能散落在多个语言、多个服务、多个版本里。工程上要有 feature flag、策略 kill switch、最小兼容配置、版本灰度、指标按 SDK 版本拆分，以及“新策略只对一小部分调用方生效”的发布路径。不要让一个新的 picker、resolver 或 retry policy 一上线就覆盖所有客户端。

最后是本地资源隔离。客户端 LB 不应在 RPC 热路径上做远程查询、全量排序、重锁竞争或大量分配。picker 应该足够轻，指标上报也不能把业务线程拖住。一个 SDK 如果因为锁、goroutine、线程池、DNS 查询或 telemetry 阻塞导致调用方一起抖，它就从“去中心化负载均衡”变成了每个进程里的本地单点。

一句话回答：客户端 SDK LB 避免单点故障，要靠本地快照、多副本控制面、渐进配置发布、随机化决策、SDK 版本治理和热路径资源控制，而不是简单地把中心代理删掉。

## 93. 客户端 SDK 负载均衡与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：客户端 SDK LB 传递上下文主要靠 RPC metadata、HTTP header、调用上下文和拦截器。它能把 trace、deadline、tenant、request id、认证信息和部分路由标签跟随一次 RPC 传到下游；但真实客户端 IP 要谨慎处理。内部服务间调用时，socket 看到的源地址通常只是上一个服务、sidecar 或 NAT 地址，不一定是最终用户。

真实客户端 IP 一般在入口可信边界产生或规范化。外部请求先经过 CDN、L7 LB、Ingress 或 API Gateway，由这些组件写入 `Forwarded`、`X-Forwarded-For`、`X-Real-IP` 或厂商特定头。进入内部服务后，客户端 SDK 可以继续把这个字段作为业务上下文传给下游，但不能盲目信任外部客户端自带的同名 header。更稳的做法是在入口覆盖这些 header，内部服务只信任来自入口网关或 mesh 的规范化字段。

协议上下文要看 SDK 所在的协议栈。gRPC metadata 本质上通过 HTTP/2 header 传递，官方文档也把 metadata 定位成 RPC 关联信息的 side channel，可用于认证、追踪和自定义 header。HTTP SDK 则通过 header、method、path、scheme、host、ALPN、TLS 信息和连接池状态表达协议信息。客户端 LB 可以按 service/method 或 route name 选择不同策略，但它不应把 HTTP 语义硬塞进纯 TCP 或数据库协议。

超时和 deadline 是客户端 SDK 最应该管好的上下文。gRPC 文档明确建议客户端显式设置 realistic deadline，并支持在服务继续调用下游时传播剩余时间。工程上要把 overall deadline、per-try timeout、connect timeout、read timeout、retry backoff 和 hedging delay 放在同一张预算表里。负载均衡器不能因为换了一个 endpoint，就让一次请求突破最外层 deadline。

追踪上下文通常由拦截器自动处理。入口生成或接收 `traceparent`、`tracestate`、B3、`x-request-id` 后，服务端拦截器把它放进本地 context；客户端拦截器再写入出站 metadata。SDK LB 还可以给 span 加上 `selected_endpoint`、`lb_policy`、`resolver_version`、`retry_attempt`、`subchannel_state` 这类标签，方便后续解释为什么请求被送到某个实例。注意不要把高基数字段随便打进指标标签，否则观测系统会先扛不住。

一句话收束：客户端 SDK LB 传递上下文靠标准 metadata/header 和拦截器；真实用户 IP 从入口可信边界来，deadline 必须随调用链递减传播，trace 要覆盖 resolver、picker、retry 和 endpoint 选择，而不是只记录业务方法耗时。

## 94. 客户端 SDK 负载均衡如何做健康检查、摘除、恢复和流量预热？

可以先这样答：客户端 SDK LB 的健康治理要结合服务发现状态、连接状态、主动健康检查、真实 RPC 结果和本地保护策略。不要只靠注册中心里一个 “healthy” 字段，也不要只靠客户端自己偶然碰到的错误。前者太粗，后者太慢。

服务发现负责给出候选集。实例下线、Pod readiness 失败、EndpointSlice 变更、xDS EDS 更新或注册中心摘除，都会让客户端从候选集中移除 endpoint。客户端收到新地址表后，要先做版本校验和去抖动，避免控制面短暂抖动导致地址集来回变化。旧 endpoint 从候选集中移除后，已有连接可以 drain，新 RPC 不再选择它。

主动健康检查适合发现“连接还能不能用”和“服务是否声明可用”。gRPC 官方 health checking 提供 `Check` 和 `Watch` 两种模式；客户端健康检查启用后，会在连接建立时调用 `Watch`，服务变成不健康时客户端停止向它发请求，恢复健康后再恢复调用。这个机制适合和 LB policy 配合，但也要注意规模：让海量客户端高频 unary probe 每个后端，会把健康检查本身做成负载。

被动健康来自真实请求。连续 `UNAVAILABLE`、连接失败、RST、deadline exceeded、5xx、p99 拉高、in-flight 堆积、重试后才成功，都应进入 endpoint 的本地评分。被动信号比主动探针更贴近用户体验，但容易受流量少、请求类型不同和瞬时网络抖动影响。成熟实现会用滑动窗口、最小样本数、半衰期、连续失败阈值和 ejection time，而不是一次失败就摘除。

恢复要分阶段。一个 endpoint 从不健康回到健康，先进入 probing 或 half-open 状态，只放少量请求；连续成功且延迟正常后，再逐步增加权重。新实例也一样。它可能刚完成启动，但连接池、JIT、TLS session、热点缓存、下游依赖和本地限流窗口还没热。可以预建少量连接、低权重放量、设置最大并发增长速率，等真实指标稳定后再进入正常权重。Envoy 的 slow start 思路虽然在代理侧常见，但客户端 SDK LB 也应实现同样的恢复节奏。

还要处理重试和健康状态的关系。某 endpoint 被摘除后，重试不应继续选它；半开探测失败后，ejection 时间要延长；所有 endpoint 都不健康时，要明确是 fail-fast、选择最不坏的节点，还是把错误交给上游降级。这个决策要和业务 SLO 对齐。对于读请求，可以更积极重试；对于非幂等写请求，健康摘除和重试必须更保守。

一句话回答：客户端 SDK LB 的健康闭环是“服务发现过滤候选、主动检查确认可用、被动观测发现慢坏、摘除后 drain、恢复时 half-open 和 slow start”。真正难的不是发现挂掉的节点，而是避免恢复节点被所有客户端一起打垮。

## 95. 客户端 SDK 负载均衡的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：客户端 SDK LB 的观测要覆盖五段：服务发现、连接管理、picker 选择、请求尝试、重试与健康状态。判断它自己成为瓶颈，需要证明耗时或错误发生在 SDK 的 resolver、picker、连接池、健康检查、重试或 telemetry 里，而不是后端服务本身慢。

服务发现侧要看 resolver 更新次数、地址数量、地址版本、控制面连接状态、配置过期时间、xDS 或注册中心错误、DNS 解析耗时、last-known-good 使用次数。地址表频繁抖动、版本长期不更新、不同客户端看到的 endpoint 集合差异很大，都会让负载均衡行为变得不可解释。

连接和 subchannel 侧要看连接建立耗时、TLS 握手耗时、HTTP/2 stream 数、连接池大小、READY/IDLE/CONNECTING/TRANSIENT_FAILURE 状态分布、keepalive 失败、连接重置、每个 endpoint 的 in-flight、pending、成功率和错误码。gRPC 的 OpenTelemetry 指标已经覆盖 client call duration、attempt duration、retry/hedging 和部分 LB policy 相关指标；如果做自定义策略，还应补 picker latency、pick result、endpoint score、ejection state、slow start weight 这些内部指标。

请求侧要拆 call 和 attempt。一次业务调用可能包含多次 attempt，必须分别记录整体耗时、每次 attempt 耗时、最终 status、重试次数、被选择的 endpoint、是否命中 wait-for-ready、是否被 deadline 截断、是否因 health check 阻塞。只看最终成功率会掩盖重试放大；只看后端延迟会漏掉 picker 和连接建立开销。

判断 SDK LB 成为瓶颈，可以看几个证据。第一，后端服务端处理时间正常，但客户端端到端延迟升高，且 trace 显示时间花在 resolver、picker、connect 或 retry backoff。第二，picker 热路径出现锁竞争、CPU 高、分配多、GC 增加，p50 也被拖慢。第三，控制面或健康检查抖动时，业务请求同步抖动，说明 SDK 没有很好地隔离控制面。第四，关闭新 LB 策略或切回简单 round robin 后延迟下降、错误率下降、连接数恢复正常。第五，同一个后端直接调用正常，经 SDK LB 路径异常。

还要看资源放大。客户端 LB 分散在每个进程里，单个进程多几个 goroutine 或线程看似没事，乘以几千个实例就可能变成控制面 QPS、健康检查 QPS、连接数和指标基数的巨大放大。健康检查流量、OpenTelemetry 标签、endpoint 级指标、per-method 维度、per-tenant 维度都要控制基数。

一句面试答案可以收成：客户端 SDK LB 的核心指标是 resolver 新鲜度、subchannel 状态、picker 开销、attempt 分布、重试放大、endpoint 健康和策略版本。只有分段证据显示请求卡在这些 SDK 内部环节，才说它自身成为瓶颈。

## 96. 数据库代理层位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：数据库代理层位于应用服务和数据库之间，属于后端数据访问链路，不是公网入口层。它解决的是应用到数据库这一段的连接复用、连接数保护、读写分离、主备切换、SQL 路由、认证收敛和数据库侧限流。严格说，它不是典型服务到服务的东西向 RPC 组件，但它运行在企业内部网络里，服务的是“应用访问数据存储”的内部流量。

典型数据库代理包括 RDS Proxy、ProxySQL、PgBouncer、数据库厂商自己的 proxy、云数据库 endpoint 前的连接池层，以及一些分库分表代理。RDS Proxy 文档把它的定位说得很直接：让应用共享数据库连接、降低频繁建连开销，并在数据库故障时保持应用连接。PgBouncer 则是 PostgreSQL connection pooler，应用像连接 PostgreSQL 一样连接 PgBouncer，再由它复用到真实 PostgreSQL 的连接。

和普通负载均衡不同，数据库代理理解数据库协议或至少理解连接池语义。RDS Proxy 会基于 SQL 操作和数据库结果调整行为；ProxySQL 有 hostgroup、query rule、read/write split、replication lag 检查；PgBouncer 有 session、transaction、statement 三种 pooling mode。也就是说，它不只是按 TCP 四元组转发，而是在数据库协议层处理连接生命周期。

它也有应用路由成分，但路由对象不是 HTTP path 或 RPC method，而是 SQL、用户、schema、database、事务状态、读写属性、分片键、租户、只读 endpoint、writer/reader 角色。比如读请求走 read-only endpoint，写请求走 writer；某类报表 SQL 走只读副本；某个租户或 shard 走特定库。这个能力很有用，也很危险，因为 SQL 改写、事务状态和 session 变量会影响语义。

面试里要强调边界。API Gateway 管用户认证、API lifecycle、HTTP 路由；Service Mesh 管服务间通信；数据库代理管应用到数据库的连接和 SQL 流量。把数据库代理放到公网入口前面是错误架构，直接暴露数据库协议更是安全风险。它应该在私有网络、受控安全组、最小权限和审计体系内工作。

一句话总结：数据库代理层是数据访问路径上的内部 L4/L7 数据库协议组件，主要解决应用到数据库的连接复用、容量保护、读写/故障路由和 SQL 级治理，不是企业公网入口。

## 97. 数据库代理层在高可用设计中如何避免单点故障？

可以先这样答：数据库代理避免单点故障，要同时保证代理实例本身多副本、入口地址可切换、后端数据库故障可感知、配置和凭据可恢复、连接池不会在故障时放大冲击。数据库代理在链路上离数据库很近，一旦代理挂掉，应用通常不是慢一点，而是直接连不上库。

第一层是代理数据面多副本。自建 ProxySQL 或 PgBouncer 不应单实例部署，通常会在每个应用节点本地放一个 sidecar/daemon，或者放多台共享代理，再用 NLB、VIP、DNS、Keepalived、HAProxy 或 Kubernetes Service 做入口。每种方式有取舍：本地代理延迟低、隔离好，但配置和升级数量多；共享代理便于治理，但要重点保护代理集群容量和入口高可用。

第二层是托管代理的多可用区能力。RDS Proxy 文档说明其基础设施高可用并部署在多个 AZ，计算、内存和存储资源独立于 RDS DB instance，并会根据数据库负载自动伸缩。使用托管代理时，仍不能只看产品说明，还要看子网选择、AZ 限制、endpoint 类型、默认 endpoint 覆盖范围、Secrets Manager、IAM、网络安全组和 CloudWatch 告警是否一起高可用。

第三层是后端故障切换。数据库代理要知道谁是 writer、谁是 reader、哪些副本延迟过高、哪些 target 不可用。ProxySQL 的 monitor module 会做 connect、ping、replication lag 和 read_only 检查，并把节点移到 writer 或 reader hostgroup。RDS Proxy 会自动确定当前 writer；RDS Proxy reader endpoint 在 reader 不可用时可以把后续查询路由到可用 reader。PgBouncer 的 FAQ 则很明确：它没有内置 failover-host 配置或检测，通常依赖 DNS 重配置、reload、RECONNECT 或外部 TCP LB。

第四层是连接和 session 语义。数据库代理常见的坑是 failover 后“连接还在，但 session 语义变了”。事务中的连接、session pinning、prepared statement、临时表、session variable、advisory lock、LISTEN/NOTIFY 都可能让连接无法安全迁移。RDS Proxy 文档里专门讨论 multiplexing 和 pinning；PgBouncer 的 feature map 也说明 transaction pooling 会破坏一些 session-based PostgreSQL 特性。高可用设计必须知道哪些连接能无感切换，哪些必须返回错误让应用重连。

第五层是配置和凭据高可用。代理依赖用户、密码、TLS、证书、IAM、Secrets Manager、路由规则、hostgroup、pool size 和限流配置。配置发布要有版本、审计、回滚和分批；凭据轮换要演练；代理集群节点之间的配置一致性要监控。ProxySQL Cluster 可以同步配置，但不能把它理解成自动共享所有连接状态的数据面集群。

一句话回答：数据库代理层高可用靠多副本代理、稳定入口、后端角色感知、故障切换演练、连接池保护、session 语义边界和配置凭据治理。不要只问“代理进程有没有两台”，还要问“failover 时应用连接会发生什么”。

## 98. 数据库代理层与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：数据库协议不像 HTTP 那样天然有一套通用 header，可以随手塞 `X-Forwarded-For`、trace id 和 deadline。数据库代理传递上下文要分三类：连接层信息、数据库会话信息、应用观测信息。混在一起会出事故。

真实客户端 IP 在数据库代理里通常不直接等于数据库看到的 peer address。应用如果通过 NLB、HAProxy 或共享代理访问数据库代理，数据库代理看到的可能是上游代理地址；后端数据库看到的通常是数据库代理地址。要保留原始连接信息，可以在可信网络里使用 PROXY protocol。ProxySQL 官方文档说明它支持 PROXY protocol v1，可以从受信任网络接收原始客户端 IP 和端口，并用于日志、安全和审计。这里的关键是“受信任网络”，不能允许任意客户端伪造 PROXY header。

协议上下文由数据库连接握手和代理配置表达。PostgreSQL 可以用连接参数 `application_name` 标识应用；MySQL 有 connection attributes，很多客户端会带 `_client_name`、`_client_version`、`program_name` 等字段。应用也可以通过数据库用户、database/schema、连接串、startup parameters、TLS client cert、ProxySQL user 或 PgBouncer database entry 表达来源。不要把最终用户身份直接拼进 SQL 注释或 session variable 后到处传，尤其在 transaction pooling 或 multiplexing 场景，session 状态可能复用到下一次请求。

超时要分层。应用有请求 deadline，数据库驱动有 connect timeout、socket timeout、query timeout；数据库本身有 `statement_timeout`、lock timeout、idle-in-transaction timeout；代理还有 connection borrow timeout、pool wait timeout、server idle timeout、health check timeout。RDS Proxy 有连接池借用等待和负载保护；PgBouncer 的 `SHOW STATS` 里能看到客户端等待 server 的时间。一个合理设计是外层业务 deadline 先到，内部数据库 statement timeout 更短或接近，代理等待不能无限排队。

追踪上下文通常不要指望数据库代理自动端到端传播。更现实的做法是应用层创建 DB client span，记录 SQL 模板、数据库系统、proxy endpoint、database、user、pool wait、query time、rows、错误码；数据库代理侧用 access log、query digest、processlist、CloudWatch 或 admin stats 做对账。若确实需要把 trace id 送到数据库侧，可以使用 `application_name`、连接属性或受控 SQL comment，但要处理采样、隐私、高基数和连接复用问题。

在下游传递上，数据库代理还要尊重事务和 session 边界。transaction pooling 下，一个客户端连接的连续事务可能使用不同后端连接；session pooling 下同一个客户端更容易保持后端连接。prepared statement、临时表、SET LOCAL、SET ROLE、LISTEN/NOTIFY 这类功能会影响代理能否 multiplex 或安全迁移。上下文传递如果依赖 session 状态，就必须确认当前 pooling mode 支持它。

一句话收束：数据库代理层传上下文不能照搬 HTTP 代理模型。真实 IP 靠可信 PROXY protocol 或代理日志，协议身份靠连接参数和认证信息，超时靠驱动、代理和数据库共同约束，trace 主要靠应用 instrumentation 和代理日志对齐。

## 99. 数据库代理层如何做健康检查、摘除、恢复和流量预热？

可以先这样答：数据库代理的健康治理要同时看代理自身和后端数据库。代理进程能接 TCP 连接，不代表它能认证、借出数据库连接、识别 writer、连接 reader，也不代表后端数据库有容量。数据库代理层的健康检查如果只做端口探测，很容易把故障藏到连接池等待和 SQL 超时里。

代理自身的健康检查至少要覆盖监听端口、管理端口、配置加载、凭据访问、TLS、CPU/内存、文件描述符、连接数、水位和事件循环。对于托管代理，要看 endpoint 状态、CloudWatch 指标、事件和日志；对于自建 ProxySQL/PgBouncer，要看进程、admin console、stats、error log 和配置版本。共享代理还要看每个租户、每个 database/user pool 是否可用，不能只看全局健康。

后端数据库健康检查更复杂。ProxySQL monitor module 会对后端做 connect、ping、replication lag 和 read_only 检查；read_only 可以驱动 writer/reader hostgroup 调整，replication lag 超阈值时可以 shun 读副本。RDS Proxy 会维护 target group，并在数据库故障时把连接转到当前可用目标。PgBouncer 没有内置数据库 failover 检测，通常依赖 DNS、reload、RECONNECT 或下游 TCP LB。

摘除要分“代理摘除”和“数据库摘除”。代理实例准备下线时，应先从上游 LB 或 Service 里移除，停止接新连接，等待已有客户端连接完成或到达最大 drain 时间，再退出。数据库 target 不健康时，代理应停止给它创建新后端连接，已绑定的连接视事务和 session 状态决定是等待完成、快速失败还是要求应用重连。对写库 failover，宁可让部分事务明确失败，也不要悄悄把写请求发到错误角色。

恢复要先小流量。数据库刚恢复或新 reader 刚加入时，buffer cache、plan cache、连接池、复制延迟、统计信息、存储层预热都可能不稳定。代理可以先降低 max connections、低权重接入、限制新连接速率、观察 replication lag 和 query latency，再逐步恢复。应用侧也要避免集体重连。故障后所有应用进程同时重建连接池，会把数据库刚恢复的 CPU 和认证路径再次打满。

流量预热还包括连接池预热和语义预热。可以预建少量 server connection，运行轻量 `server_check_query` 或初始化 SQL，提前验证凭据、schema search path、TLS 和角色；但不要用重查询“预热”数据库。prepared statement 和 session state 也要谨慎，特别是在 transaction pooling、RDS Proxy multiplexing 或 ProxySQL multiplexing 场景，过度依赖 session 可能导致 pinning，反而降低代理复用能力。

一句话回答：数据库代理健康治理是“代理可用、后端角色正确、连接池有容量、复制延迟可接受、session 语义不破坏”的组合判断。摘除要 drain，恢复要限速和预热，最怕的是健康刚绿就让全体应用一起重连。

## 100. 数据库代理层的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：数据库代理层的观测要把一次数据库访问拆成几段：客户端到代理建连、代理排队等待可用后端连接、代理到数据库执行、结果返回、连接归还池。判断代理自身成为瓶颈，关键是证明等待和耗时发生在代理或连接池，而不是数据库执行本身。

基础指标包括客户端连接数、服务端数据库连接数、连接建立成功/失败、认证失败、TLS 连接数、连接池大小、空闲连接、借出连接、等待连接、连接借用延迟、排队最长等待、查询数、事务数、query latency、database response latency、被拒绝连接、限流/丢弃、连接 pinning、multiplexing 命中率、读写 endpoint 流量和后端 target 可用率。RDS Proxy CloudWatch 指标里有 `ClientConnections`、`DatabaseConnections`、`DatabaseConnectionsBorrowLatency`、`DatabaseConnectionsCurrentlySessionPinned`、`QueryRequests`、`QueryResponseLatency` 等；这些正好对应连接池和查询路径。

自建代理也有自己的观测入口。ProxySQL 的 stats database 包含 `stats_mysql_connection_pool`、`stats_mysql_query_digest`、`stats_mysql_errors`、`stats_memory_metrics` 等表，monitor database 记录 connect、ping、replication lag、read_only 检查。PgBouncer 的 `SHOW STATS`、`SHOW POOLS`、`SHOW CLIENTS`、`SHOW SERVERS` 能看到事务数、查询数、客户端等待 server 的时间、`cl_waiting`、`sv_active`、`sv_idle`、`maxwait` 和 pool mode。

判断代理成为瓶颈，可以看几个典型证据。第一，数据库端实际 query time 正常，但应用端 p95/p99 升高，同时代理的 borrow latency、wait time、`cl_waiting`、`maxwait` 或 pending connections 上升。第二，数据库 CPU、IO、锁等待都不高，但代理 CPU、内存、fd、网络吞吐、事件循环延迟、TLS 握手或单线程 worker 打满。第三，增加代理副本、扩大 pool、降低 pinning、减少慢 query logging 或关闭高成本规则后，应用延迟明显下降。第四，某些用户或 database pool 等待严重，而其他 pool 正常，说明代理内部隔离或 pool 配置不足。

还要识别“代理看似瓶颈，其实数据库瓶颈”的情况。比如数据库锁等待、慢 SQL、复制延迟、磁盘 IO、buffer cache miss、连接数上限、writer failover、事务长时间不提交，都会让代理连接被长时间占用，表现成代理排队。此时扩代理副本没有用，只会让更多请求排进数据库。排障时要把 proxy wait、DB execution、DB lock wait、network round-trip、result transfer 分开看。

连接 pinning 是数据库代理观测里很关键的信号。RDS Proxy 会因为改变 session state 的操作把会话 pinned 到当前连接；长事务、prepared statement、临时表、session variable、某些大语句都会降低 multiplexing。pinning 高时，客户端连接和数据库连接的比例接近 1:1，代理就失去了连接复用价值。面试里可以说得直白一点：数据库代理不是魔法，如果应用每个请求都要求独占 session，它就只能退化成昂贵的 TCP 中转。

最后看错误和保护指标。连接被拒绝、auth 失败、TLS 失败、pool timeout、server login 慢、reader endpoint 不可用、replication lag shun、read/write role 错误、query rule 命中异常、配置版本不一致，都要进入告警。对数据库代理来说，错误率低但等待时间持续升高也很危险，因为这常常是“快要保护不住数据库”的前兆。

一句话收束：数据库代理的观测核心是连接池水位、等待时间、后端连接借用、查询耗时、pinning、后端健康和按 pool 拆分的容量。只有证明代理等待或代理资源先饱和，才说代理自身成为瓶颈；如果数据库执行已经慢，代理只是把慢暴露出来。

## 101. 消息队列消费者分组位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：消息队列消费者分组不在公网入口层，也不在同步 RPC 的 L4/L7 转发层，而是在应用后方的异步消息处理层。它处理的是“生产者已经把事件、任务或命令写入 broker 之后，哪些消费者实例来分摊消费”的问题。用企业流量架构的话说，它更接近东西向应用集成和后台 worker 调度，不是用户请求刚进系统时的入口调度，也不是 API Gateway 那种按 path、method、header 做即时路由的组件。

Kafka 的 consumer group 是最典型的模型。多个消费者使用同一个 `group.id` 订阅同一批 topic 时，Kafka 会把 topic 的 partition 分配给 group 内的成员；同一个 partition 在同一个 group 内同一时刻只会被一个消费者处理。这样做的结果是：组内横向扩容可以提高处理能力，组外另一个 consumer group 又能拿到同一批消息的一份独立消费视图。RabbitMQ 里常说的“多个消费者竞争同一个队列”也能达到类似的分摊效果，但它不是完全同名的 Kafka consumer group 抽象，工程回答时最好把具体消息系统说清楚。

它主要解决的是异步削峰、后台任务并行、事件订阅、跨服务解耦和失败重试后的消费恢复。入口网关面对的是一次正在等待响应的请求，消费者分组面对的是已经落到队列或日志里的消息。前者要在毫秒级决定往哪个后端发，后者要在可接受的消费延迟内把 backlog 消化掉。这个差异很重要：消费者分组不能替代入口限流、同步路由或会话保持；反过来，入口负载均衡也不能替代消息积压、offset、ack、重试和死信治理。

它也不是传统意义上的“应用路由”。生产者通常通过 topic、queue、routing key、message key 或事件类型决定消息进入哪里；consumer group 决定的是同一个订阅关系下的消费分工。Kafka 里 partition key 会影响消息落到哪个 partition，从而影响顺序性和并行度；消费者分组只是拿到 partition 后处理它。也就是说，consumer group 是消费侧负载分配和进度管理机制，不是按业务字段动态选择任意后端服务的路由器。

面试里可以补一个边界：消费者分组在链路上经常承接入口之后的异步分支。比如订单 API 收到请求后同步写库并发布 `OrderCreated` 事件，后面的库存、通知、风控、报表消费者分别用自己的 group 消费。用户入口流量已经在 API 层结束，消息队列里流动的是事件流或任务流。这个层次说清楚，后面谈高可用、上下文传播和观测指标才不会混到 SLB、Service Mesh 或数据库代理里。

## 102. 消息队列消费者分组在高可用设计中如何避免单点故障？

可以先这样答：消费者分组的高可用不是“多起几个消费者”这么简单，而是 broker、topic/queue、消费者实例、消费进度、处理副作用和下游依赖一起高可用。任何一个环节只有单点，都会让消费停住，或者更糟，造成重复处理、乱序处理和不可恢复的副作用。

第一层是 broker 和队列本身。Kafka 需要 topic partition 有合适的副本数、ISR、min.insync.replicas、跨机架或跨可用区分布，并确认 group coordinator、offset topic 这类内部依赖不是单点。RabbitMQ 则要关注 quorum queue、classic mirrored queue 的取舍、节点分布、磁盘和内存水位、网络分区策略。消费者组本身再可靠，也救不了一个单副本 partition 或单节点队列。

第二层是消费者实例分布。消费者要跨主机、跨故障域部署，不能所有副本都在同一台机器、同一个节点池、同一个 AZ，或者共享一个会同时过期的凭据。Kafka group 内消费者数通常不应长期大于 partition 数太多，否则多出来的实例只是空闲；如果处理能力不够，单纯加消费者但 partition 数不变，吞吐上限可能不会动。RabbitMQ 场景下则要用合理的 consumer 数量和 prefetch 限制，避免一个消费者一次拿太多未确认消息，故障时造成大量消息等待重投。

第三层是消费进度和处理语义。Kafka 官方文档强调 offset commit 决定了进程失败后从哪里恢复；实际工程里通常在业务处理成功之后再提交 offset，或者把业务结果和 offset 放进同一个事务性存储里。RabbitMQ 的 manual ack 也是类似思路：消费者处理成功后才 ack，失败时 nack/requeue 或投递到死信链路。这里要承认边界：大多数系统默认只能做到 at-least-once，重复消费必须靠业务幂等、去重表、事件 ID、幂等写接口或事务外盒来兜住。

第四层是 rebalance 和故障切换治理。消费者崩溃、心跳超时、滚动发布、扩缩容、partition 变化都会触发重新分配。Kafka 用 heartbeat、`session.timeout.ms`、`max.poll.interval.ms` 判断成员是否还活着；如果消费者线程还在心跳但处理不动，`max.poll.interval.ms` 会迫使它离组，让别的实例接管。生产环境还会用 cooperative rebalancing、static membership、分批发布和优雅停机，减少一次发布导致全组停顿。

最后是下游依赖和毒丸消息。消费者通常要写数据库、调 RPC、发通知、更新缓存。只保护 consumer 进程，不保护这些下游，消费仍然会卡在处理阶段。遇到不可解析消息、永久业务错误或下游持续失败，要有 retry topic、延迟队列、死信队列、人工补偿和告警。不要让一个毒丸消息在主队列里无限重试，也不要让所有消费者因为同一个下游故障同时阻塞，最后把 broker 的 lag 和 unacked 消息堆满。

## 103. 消息队列消费者分组与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：消息队列不会像 HTTP 代理那样天然保留真实客户端 IP、协议、超时和 trace header。这些信息如果对业务有用，必须在生产消息时显式写入 message headers、properties 或 payload 字段，并在消费时按信任规则读取。队列只负责存储、投递和确认消息，不会自动知道最初的公网客户端是谁，也不会自动继承一次 HTTP 请求的 deadline。

真实客户端 IP 要谨慎处理。入口层可以通过 `X-Forwarded-For`、`Forwarded`、`CF-Connecting-IP`、PROXY protocol 或云 LB 日志得到客户端地址，但到了消息队列阶段，消费者看到的网络 peer 通常只是 broker 地址。若确实需要审计或风控，可以在入口网关归一化后写入 `client_ip`、`source_region`、`user_agent`、`tenant_id`、`request_id` 这类字段。不要让任意外部请求直接伪造这些字段，也不要把 IP 当成授权依据；更稳妥的做法是入口层签名或只在可信服务内写入。

协议上下文要从“连接协议”转换成“事件契约”。HTTP/2、gRPC、WebSocket、MQTT 这些上游协议不会原封不动进入 Kafka record 或 RabbitMQ delivery。消息里应表达的是 `event_type`、`schema_version`、`content_type`、`source_service`、`operation`、`message_key`、`partition_key` 和业务实体 ID。消费者关心的是“这是什么事件、按什么 schema 解码、处理后要满足什么幂等规则”，而不是原始请求当时走的是 HTTP/1.1 还是 HTTP/2。

超时也要重新建模。DNS 的 TTL、HTTP 的 read timeout、RPC 的 deadline、broker 的 message TTL、Kafka 的 retention、消费者的 `max.poll.interval.ms` 都不是一回事。异步消息更适合显式携带 `deadline_at`、`expire_at`、`not_before`、`attempt`、`max_attempts`、`retry_after` 这类字段。消费者拿到消息后先判断剩余时间：已经过期的消息可以跳过、补偿或进死信；还有时间的消息再把剩余预算传给下游 RPC 或数据库操作。不要让一个已经失去业务意义的消息继续占用主消费通道。

追踪上下文要按消息语义传播。W3C Trace Context 的 `traceparent` 和 `tracestate` 可以放在 Kafka headers、RabbitMQ headers/properties 或其他消息系统的 metadata 里。OpenTelemetry 的 messaging semantic conventions 也明确区分 producer send、consumer receive/process、settle 等操作。异步场景里不要机械地把消费者 span 都做成生产者 span 的直接子 span；批量消费、延迟消费、fan-out、多 group 消费时，用 span links 或 message creation context 往往更准确。工程上要同时记录 topic/queue、partition、offset、message id、consumer group、attempt 和 trace id，排障时才能从“哪个请求产生了事件”追到“哪个消费者处理了它”。

## 104. 消息队列消费者分组如何做健康检查、摘除、恢复和流量预热？

可以先这样答：消费者分组的健康治理对象不是一个对外暴露的 endpoint，而是“消费者能否持续从 broker 拉到消息、按时处理、正确 ack/commit，并把副作用写到下游”。所以健康检查不能只看进程活着，也不能只看 broker 端口能连通。最有价值的检查是消费循环、处理线程池、broker 会话、下游依赖和 in-flight 消息状态。

健康检查可以分三层。liveness 只回答进程是否死锁、事件循环是否卡死、关键线程是否退出；它不应该因为短暂 lag 变高就杀进程。readiness 回答这个实例是否应该继续接新消息：broker 连接是否正常、订阅或分区分配是否可用、处理队列是否未满、下游依赖是否在可接受范围内、配置和凭据是否有效。startup probe 则适合处理消费者启动慢的问题，例如加载规则、建立连接池、预热本地缓存、恢复本地状态。Kubernetes 的探针模型可以借用，但具体语义要落在消费系统上。

摘除要先停止拿新消息，再处理已有消息。Kafka 消费者可以先把 readiness 置为 false，暂停拉取或降低处理并发，等当前 batch 处理完后提交已完成 offset，再调用 close 离开 group；如果使用 `ConsumerRebalanceListener`，在 partition 被 revoke 前要提交或保存进度。RabbitMQ 场景下可以取消 consumer、停止新 delivery、等待 in-flight 消息处理完成，成功的 ack，失败的 nack/requeue 或投递死信。直接 kill 进程会更快触发接管，但代价是重复处理和更长的恢复排障。

恢复不要一绿就满速。消费者刚重启时，连接池、JIT、缓存、下游限流窗口、批处理缓冲都可能是冷的；一个 group 同时大量实例加入还会触发 rebalance 抖动。更稳的做法是先小并发、小 prefetch、小 `max.poll.records` 或较低 worker 数接入，观察错误率、处理延迟、commit/ack 成功率和下游水位，再逐步放大。Kafka 场景还要注意 partition 分配是否稳定，RabbitMQ 场景要注意 unacked 是否被单个消费者吃满。

流量预热分 broker 侧和业务侧。broker 侧看消费速率、fetch size、prefetch、并发数和 rebalance 频率；业务侧看 DB 连接池、缓存命中、RPC 下游、幂等存储和批处理窗口。延迟队列、重试 topic、死信回放尤其要限速，因为它们常常携带历史积压，一放开就会把正常实时消费挤掉。面试里可以说得直接一点：消费者分组摘除和恢复的核心不是“实例上下线”，而是控制 in-flight 消息、offset/ack 边界、rebalance 抖动和下游承载能力。

## 105. 消息队列消费者分组的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：消费者分组的观测要同时看 backlog、消费进度、处理耗时、ack/commit、rebalance、错误重试和下游依赖。只看消费者 CPU 或 broker QPS 都不够。判断它是否成为瓶颈，关键是证明消息已经在 broker 里等着、下游还有可用能力，但 group 的拉取、处理、确认或分区分配跟不上。

最基础的是积压指标。Kafka 要看 group lag、每个 partition 的 lag、lag age、end offset、committed offset、assigned partition 数、records consumed rate、bytes consumed rate、fetch latency、poll 间隔、commit latency 和 commit failure。RabbitMQ 要看 ready messages、unacknowledged messages、deliver/get rate、ack/nack/requeue rate、consumer 数、prefetch 使用情况、redelivered rate 和 dead-letter rate。只看总 lag 容易误判，真正拖慢系统的常常是一个热 partition、一个热 routing key 或一个 poison message。

处理链路指标要拆细。消费者从 poll/delivery 到 handler 开始的排队时间、handler 执行时间、批处理 flush 时间、下游 RPC/DB 时间、序列化反序列化时间、ack/commit 时间都要分开。应用层还要记录成功数、业务失败数、可重试失败数、永久失败数、重复消息数、幂等命中数、死信数、重试次数分布和消息年龄。OpenTelemetry span 上至少要有 messaging system、destination、operation、consumer group、partition、offset 或 message id 这些标签，但要控制高基数字段，别把完整 payload 或用户标识直接打进指标标签。

rebalance 是消费者组特有的观测重点。要看 rebalance 次数、持续时间、原因、成员加入离开次数、心跳失败、`max.poll.interval.ms` 触发、partition revoked/assigned 数量、static membership 是否生效、滚动发布期间是否全组停顿。很多“消费慢”不是 handler 慢，而是频繁 rebalance 导致消费者反复丢分区、重建状态、重新拉取。Kafka 场景下，如果 lag 呈锯齿状上涨、处理线程空闲但 group 一直在 rebalance，瓶颈就在协调和实例稳定性上。

判断 consumer group 自身成为瓶颈，可以看几组证据。第一，生产速率长期高于成功处理速率，lag age 持续变大，而 broker、网络和下游没有先饱和。第二，消费者实例 CPU、GC、线程池、内部队列、反序列化或批处理耗时先达到上限。第三，增加消费者或提高并发后吞吐能上去，直到遇到 partition 数、queue shard 数或下游容量上限。第四，某些 partition 或 key 的 lag 远高于其他分区，说明瓶颈是分区倾斜，不是整个 broker。第五，ack/commit 延迟、失败率或 redelivery 明显上升，说明消费进度无法稳定推进。

也要识别“看起来像消费者瓶颈，其实不是”的情况。下游数据库锁等待、RPC 熔断、对象存储慢、schema registry 卡顿、消息过大、压缩解压耗时、broker 磁盘 I/O、网络丢包、认证限流，都可能让 lag 上升。排障时把一条消息的时间拆成 broker 等待、consumer 拉取、应用排队、handler 执行、下游等待、ack/commit 六段，再看哪一段先变长。只有证明慢发生在消费者组自身的拉取、调度、处理或确认边界，才说它是瓶颈；否则它只是把下游问题以 lag 的形式暴露出来。

## 106. 灰度发布平台位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：灰度发布平台通常不算真正转发数据包的数据面组件，而是发布控制面和应用流量治理层组件。它站在 Ingress、Gateway、Service Mesh、负载均衡、特征开关、服务发现这些执行组件之上，把“新版本能不能放量、放多少、按什么条件放、失败后怎么回滚”变成可审计、可自动化的发布策略。所以它主要解决的是应用发布期间的应用路由和风险控制问题，同时会影响入口流量和东西向流量，但它本身一般不应该成为每个请求都必须经过的同步转发节点。

如果灰度对象是面向公网的 Web/API 服务，灰度发布平台会更多作用在入口流量上，比如通过 ALB、Ingress、Gateway API、Envoy Gateway、Nginx、CDN 边缘规则或 API Gateway，把 1%、5%、10% 的用户请求切到 canary 版本。这个场景的关键不是“入口层有没有负载均衡”，而是发布平台能不能把入口层已有的路由能力组织成一个可回滚的发布流程：先预览，后小流量，再观察指标，最后全量。

如果灰度对象是内部微服务，灰度发布平台更多作用在东西向流量上。它会借助 Service Mesh 的 VirtualService/DestinationRule、Gateway API for Service Mesh、Envoy xDS、客户端 SDK 或服务发现元数据，把服务 A 调服务 B 的流量按版本、标签、Header、用户、租户或调用方拆开。Istio 这类服务网格把“路由规则”和“目标子集”拆开，灰度平台通常就是在更上层生成、校验、发布这些规则。

如果灰度对象是一个业务功能，而不是一个独立服务版本，它还可能退化成特征开关平台。此时它解决的是应用内部路由：同一个版本里按用户、组织、地区、实验分组决定走新逻辑还是旧逻辑。这类灰度不一定改变 L7 网关或服务网格配置，但它仍然属于应用流量治理，因为真实分流点在应用代码或 SDK 内部。

面试里要把它和普通负载均衡讲清楚。负载均衡负责“把请求送到健康后端”，灰度发布平台负责“围绕一次版本变更安全地改写流量分布”。Argo Rollouts 的 canary 策略里有 `setWeight`、`pause`、`analysis`、`trafficRouting` 这些概念；Flagger 也会周期性提升 canary 权重并基于成功率、延迟、WebHook 等检查决定继续还是回滚。这说明灰度发布平台关注的是发布生命周期，而不只是一次静态路由配置。

更准确地说，它不是单独解决入口、东西向或应用路由中的某一个点，而是跨这些层做“渐进式交付”。入口灰度适合控制外部用户暴露面，东西向灰度适合控制服务依赖风险，应用内灰度适合控制功能暴露和实验人群。一个成熟平台会同时支持这些模式，但会明确告诉使用者：真正承载流量的是下层数据面，发布平台负责策略、编排、观测和回滚。

## 107. 灰度发布平台在高可用设计中如何避免单点故障？

可以先这样答：灰度发布平台避免单点故障的核心原则是控制面可以短暂不可用，但数据面不能因为它不可用而中断。平台挂了，正在运行的网关、服务网格、负载均衡、Service、SDK 应继续使用最后一次已确认的配置；平台恢复后再继续推进、暂停或回滚发布。也就是说，灰度平台要做成“发布决策和配置下发中心”，不要做成所有请求的同步判定中心。

第一层是平台控制器自身高可用。以 Kubernetes 生态为例，Argo Rollouts、Flagger 这类控制器应该多副本部署，通过 leader election 或类似机制保证同一时间只有一个实例执行关键 reconcile，但备用实例可以快速接管。平台依赖的 API Server、CRD、配置存储、审计日志、镜像仓库、指标查询服务和通知通道也要有 HA 方案，否则控制器多副本只是表面高可用。

第二层是配置发布要有“最后一个好版本”。灰度平台向 Ingress、Gateway、Envoy、Istio 或云负载均衡发布规则时，应该先做 schema 校验、语义校验和 dry-run，确认不会出现权重总和错误、目标版本为空、路由条件互斥、TLS/Host 冲突、后端引用不存在等问题。真正下发后，也要能根据数据面的 ACK/NACK、Kubernetes Status Condition、网关编程状态或 xDS 响应判断配置是否生效。失败时保留旧规则，不把半成品配置推到生产流量路径。

第三层是回滚路径不能依赖太多脆弱组件。灰度发布中最重要的动作往往不是继续放量，而是快速把 canary 权重降到 0、恢复 stable 权重、停止新版本扩容，并保留现场供排查。Argo Rollouts 会维护 stable/canary 版本并支持 abort，Flagger 会在分析失败超过阈值后停止并回滚。工程上要保证这个动作在指标平台、通知系统、前端控制台部分故障时仍然能由控制器或命令行完成。

第四层是稳定版本不能过早被消耗掉。为了节省资源，有些平台会在 canary 放量时缩小 stable 副本数，甚至在即将全量时把稳定版本压得很低，这会让回滚失去承载能力。更稳妥的做法是保留足够 stable 容量，等新版本经过完整观察窗口后再缩容。Argo Rollouts 的动态缩放能力也提醒了一个边界：按权重缩放副本可以省资源，但如果流量路由和副本比例不一致，可能造成 canary 或 stable 某一侧被打爆。

第五层是依赖降级。灰度发布平台常依赖 Prometheus、Datadog、CloudWatch、日志平台、APM、合成探测、WebHook、审批系统。指标查询失败时到底 fail-open 还是 fail-closed，要按服务等级明确：核心交易服务通常宁愿暂停放量或回滚，也不要在看不到指标时继续推进；低风险后台任务可以暂停等待人工确认。不要让“指标平台短暂超时”直接触发大面积误回滚，也不要让“指标缺失”被当成健康。

最后还要做租户和发布批次隔离。一个团队的错误规则、一个命名空间的高频发布、一次指标查询风暴，不应该拖垮整个平台。可以用队列限速、命名空间级配额、发布并发限制、规则大小限制、WebHook 超时、审计和 RBAC，把故障控制在小范围内。真正高可用的灰度平台不是永远不出错，而是出错时数据面保持最后好状态，发布流程可暂停，回滚路径可执行，故障影响有边界。

## 108. 灰度发布平台与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：灰度发布平台本身通常不直接“传递”每个请求的真实客户端 IP、协议、超时和追踪上下文，它更多是生成和管理规则；真正传递这些上下文的是网关、代理、服务网格 sidecar、SDK 和应用代码。平台要做的是定义哪些上下文可以参与灰度匹配，怎样把发布元数据写入可观测数据，以及怎样保证下层组件传递上下文时不被伪造或丢失。

真实客户端 IP 要区分传递链路和信任边界。入口经过 CDN、WAF、ALB、Nginx、Envoy 或 API Gateway 时，常见做法是使用 `X-Forwarded-For`、`Forwarded`、`X-Real-IP` 或 PROXY protocol 保存来源地址。灰度平台如果支持“按地区、网段、用户来源”放量，不应该直接相信外部请求自带的这些 Header，而应该要求入口代理先根据可信上游列表重写或规范化，再让灰度规则读取规范后的字段。否则用户自己伪造 Header 就可能绕过灰度人群控制。

协议上下文要看执行层支持什么。Gateway API、Istio、Envoy 等 L7 组件可以按 Host、Path、Header、Method、Query、gRPC service/method 等条件做路由；L4 负载均衡更多只能看地址、端口、连接和 TLS 相关信息；DNS 或 GSLB 基本看不到单个 HTTP 请求语义。灰度发布平台的规则模型要显式表达这些能力差异，不能给 DNS 灰度配置一个按 Header 命中的规则，也不能假装 TCP 转发层能理解 gRPC 方法。

超时上下文也要分层。请求超时、连接超时、路由超时、重试超时、gRPC deadline、应用内部 deadline、灰度分析窗口、暂停时间、自动回滚等待时间不是同一个概念。灰度平台可以随灰度规则一起管理路由超时、重试、熔断或请求镜像策略，也可以设置每一步放量后的观察窗口，但不应该悄悄覆盖业务请求的端到端 deadline。一个常见错误是只把 canary 权重调小，却沿用过激的重试和过长的超时，导致小流量问题被重试放大。

追踪上下文一般遵循 W3C Trace Context 或各语言 APM 的上下文传播机制，例如 `traceparent`、`tracestate`，在 gRPC 中则通过 metadata 传递。灰度平台需要补充的是发布维度，而不是替代追踪协议。比较实用的做法是在代理、sidecar 或应用里给日志、指标和 span 加上 `rollout_id`、`release_id`、`route_id`、`stable_or_canary`、`canary_weight`、`policy_version`、`workload_version` 等属性。这样排查时可以看到同一个接口在 stable 和 canary 之间的错误率、延迟、下游依赖差异。

还要注意上下文的高基数风险。按用户 ID、订单 ID、完整 IP、完整 Header 值直接打指标标签，会把 Prometheus 或指标后端拖垮。灰度规则可以用这些字段做单次请求判定，但观测侧通常要聚合成版本、路由、租户、地区、灰度批次、状态码、异常类型这类低基数字段。也就是说，灰度平台要能利用请求上下文，但不能把所有上下文无脑变成全局指标标签。

## 109. 灰度发布平台如何做健康检查、摘除、恢复和流量预热？

可以先这样答：灰度发布平台的健康治理不是单一探针，而是“基础健康 + 流量健康 + 发布分析”的组合。基础健康来自 Kubernetes readiness、startup、liveness、Service endpoints、网关后端健康检查；流量健康来自错误率、延迟、饱和度、重试、熔断、业务失败率；发布分析来自 Argo Rollouts AnalysisRun、Flagger metric checks、WebHook、合成探测或人工审批。只有这些信号都支持继续放量，灰度才应该进入下一步。

发布前要先做可达性和预览检查。新版本 Pod 至少要通过 startup/readiness，Service 或 EndpointSlice 要能看到它，网关或 mesh 路由能引用到它，配置校验没有失败。很多平台会先给 canary 建 preview service 或 header-based route，只让测试流量、合成探测或内部用户命中，再进入真实用户百分比放量。这样可以在生产配置环境里验证新版本，却不马上暴露给普通用户。

摘除 canary 时要分几层做。第一步通常是把 canary 流量权重降到 0 或删除命中 canary 的路由条件；第二步是等待连接排空和指标收敛，尤其是 WebSocket、长轮询、HTTP/2、gRPC streaming、消息回调这类长连接场景；第三步再缩容 canary 副本或标记发布失败。直接杀 Pod 会让连接中断，直接删路由可能让仍在途请求出现不一致，所以发布平台要理解下层组件的 drain、terminationGracePeriod、preStop、连接池和重试行为。

恢复不是把权重一下拉回去，而是带滞后和阈值地恢复。比如 canary 连续多个窗口成功率、p95/p99 延迟、业务错误率都达标，才从 1% 到 5%、10%、25%、50% 推进；失败后也不要因为一个短窗口恢复正常就立刻继续放量，否则容易在抖动中反复放大故障。Flagger 的 interval、threshold、stepWeight、maxWeight 这类参数，本质上就是把恢复和推进变成有节奏的状态机。

流量预热要同时预热实例、依赖和数据面。实例侧包括 JVM/JIT、连接池、线程池、缓存、本地索引、模型加载、Schema 缓存；依赖侧包括数据库连接、下游 RPC 连接、对象存储客户端、消息生产者；数据面侧包括 Envoy cluster、endpoint、TLS session、DNS 缓存、负载均衡慢启动。只把权重从 0 调到 20%，但 canary 只有极少副本且没有连接池和缓存，看到的延迟可能是冷启动问题，不一定是代码逻辑问题。

还要防止权重和容量不匹配。灰度平台经常以百分比描述流量，但后端承载能力取决于副本数、HPA 反应速度、Pod readiness、CPU request、连接数和下游限额。如果 canary 只有 1 个 Pod，却突然给 10% 的高峰流量，它可能先被压垮，然后平台误判为版本有问题。比较稳的策略是先扩容并预热 canary，确认 readiness 和合成探测通过，再逐步放真实流量；如果需要省资源，也要把动态缩放和流量权重绑定起来验证。

## 110. 灰度发布平台的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：灰度发布平台的指标要分成四类：发布状态指标、业务对比指标、控制面指标和数据面生效指标。只看 canary 成功还是失败不够，因为失败可能是新版本问题、下游依赖问题、网关配置未生效、指标查询失败，也可能是灰度平台自己推进太慢或推错配置。

发布状态指标包括当前 rollout 阶段、期望权重、实际权重、stable/canary 副本数、已完成步骤、暂停时间、分析运行次数、分析成功/失败次数、失败原因、自动回滚次数、人工暂停/继续次数、发布总耗时、每一步等待时间。Flagger 会暴露 canary 权重、失败检查次数、状态条件等信息；Argo Rollouts 也有 rollout、analysis、experiment 等状态对象。这些指标能回答“发布流程卡在哪一步”。

业务对比指标要把 stable 和 canary 拆开看，包括请求量、成功率、HTTP/gRPC 状态码、业务错误码、p50/p95/p99 延迟、超时率、重试率、熔断率、限流率、CPU、内存、连接池、线程池、队列长度、GC、下游 RPC/DB/缓存耗时。灰度发布的价值就在于对比，如果指标只按服务聚合，不按版本或路由拆分，就会被 stable 的大流量稀释，canary 的小流量异常很难被发现。

控制面指标包括控制器 reconcile 次数、reconcile 时延、工作队列长度、重试次数、API Server 请求耗时、CRD 更新失败数、配置校验失败数、WebHook 调用耗时和失败率、指标查询耗时、指标查询错误率、通知失败数、审批等待时间、并发发布数、单个租户发布数、规则对象大小、指标标签基数。灰度平台自身的瓶颈常常先出现在这些地方，而不是业务请求延迟上。

数据面生效指标尤其关键。平台期望权重是 10%，不代表真实流量就是 10%。要看网关、Envoy、Istio、Ingress、ALB 或 SDK 的实际路由计数，比较 desired weight 和 observed weight；要看配置是否被接收、是否被拒绝、是否有 NACK、是否有旧版本配置残留；要看 Endpoint 是否 ready、是否被负载均衡器纳入、是否触发 outlier detection。Envoy xDS 的 ACK/NACK、配置更新时间和 cluster update 指标，在这类排查里很有价值。

判断灰度平台自身成为瓶颈，可以看几组证据。第一，业务服务直连或固定路由时正常，但每次修改灰度权重都要很久才生效，说明瓶颈在控制面传播链路。第二，控制器队列积压、reconcile 延迟升高、API Server 或指标后端调用超时，而应用本身资源不高。第三，desired weight 和 observed traffic 长时间不一致，且数据面有 NACK、配置冲突或状态条件未完成。第四，高峰发布时大量 rollout 卡在同一步，说明平台调度、指标查询或 WebHook 被并发拖慢。第五，指标标签基数、发布对象数量或规则复杂度上升后，平台 CPU/内存/数据库连接先打满。

也要避免误判。canary 延迟高可能是新版本代码慢、下游数据库锁等待、缓存未命中、HPA 冷启动、网络丢包，不一定是灰度平台瓶颈。只有当问题集中在“规则生成、配置下发、状态推进、指标判定、回滚动作”这些平台职责上，并且绕过或固定这些环节后业务恢复，才可以说灰度发布平台自身成为瓶颈。

## 111. 流量调度中心位于企业流量链路的哪一层？它主要解决入口、东西向流量还是应用路由问题？

可以先这样答：流量调度中心通常位于全局流量治理控制面，它不是一个固定的四层或七层转发组件，而是跨 DNS/GSLB、CDN、云负载均衡、Ingress/Gateway、Service Mesh、服务发现、客户端 SDK 的策略中心。它负责计算“流量应该去哪个地域、哪个集群、哪个机房、哪个版本、哪个后端池”，再把策略下发给真正承载请求的数据面。

从入口流量看，流量调度中心常解决公网用户如何进入系统的问题，比如多地域接入、就近访问、跨云容灾、机房故障切换、容量溢出、活动大促分流、地域合规隔离。它可能通过 DNS/GSLB 权重、CDN origin steering、Anycast、云负载均衡 target group、API Gateway 路由或边缘规则来实现。这个层面关注的是用户从互联网进入企业服务的第一跳或前几跳。

从东西向流量看，流量调度中心也可以解决内部服务之间的跨集群、跨区域、跨机房调用。比如服务 A 优先访问同可用区的服务 B，容量不足时溢出到邻近区域；某个集群故障时，mesh 或 SDK 自动把调用切到备用集群；某个下游依赖限流时，上游按租户或业务优先级降级。这个场景更依赖 Service Mesh、Envoy、服务发现和客户端负载均衡。

从应用路由看，流量调度中心可以承载一部分应用路由策略，比如按租户、产品线、用户等级、实验分组、地域法规、Header、Path 把流量送到不同服务池。但要注意它不应该无限上收业务判断。越接近业务语义的条件，越可能需要应用层或特征开关参与；调度中心更适合管理稳定、可审计、影响面大的流量策略。

它和灰度发布平台的区别在于目标不同。灰度发布平台围绕一次版本发布组织放量、观察和回滚；流量调度中心围绕长期流量拓扑组织地域、容量、容灾、优先级和策略分发。两者会重叠，比如灰度平台可能调用流量调度中心改权重，但灰度平台关心“这次发布是否继续”，流量调度中心关心“当前全局流量应该如何分配”。

工程上最重要的边界是：流量调度中心最好不要成为每个请求的同步远程依赖。它可以是控制面，负责计算和发布策略；真正的请求判定应该在 DNS、边缘、网关、代理、sidecar 或 SDK 本地完成。这样调度中心短暂故障不会让所有业务请求阻塞，数据面可以继续使用最后一次有效策略。

## 112. 流量调度中心在高可用设计中如何避免单点故障？

可以先这样答：流量调度中心避免单点故障的关键是控制面多活、数据面自治、配置有版本、失败能回退。调度中心可以短暂无法发布新策略，但已经下发到 DNS、CDN、网关、服务网格和 SDK 的策略必须继续工作；如果中心故障会导致请求无法路由，那说明它被设计成了同步数据面，这是高可用上的大忌。

控制面自身要做多副本、多可用区，关键存储要有一致性和备份。策略数据库、配置中心、健康状态库、容量模型、规则审计、发布队列和 API 层都要考虑主从切换、leader election、租约过期、时钟漂移和脑裂。对全局调度来说，还要考虑多地域控制面：一个区域的调度中心不可用时，其他区域能否继续发布本地策略，或者至少能冻结在最后好版本。

数据面要有最后有效配置和本地降级。Envoy xDS、Gateway、Ingress Controller、CDN、DNS、SDK 都应缓存已确认策略；新配置下发失败时保留旧配置；控制面断开时不要清空路由表。Envoy 的 xDS 协议里 ACK/NACK 机制就是为了让控制面知道配置是否被客户端接受。调度中心要把“配置已生成”和“配置已被数据面接受并生效”区分开。

策略发布要分阶段。全局流量规则不能直接一把推给所有地域和所有网关，应该先校验，再小范围下发，再观察，再扩大。发布前要做不变量检查，例如权重总和是否合理、主备池是否至少有一个健康目标、是否存在黑洞路由、是否把所有地域都导向同一个小集群、是否违反租户或合规边界。一次错误调度策略的破坏力通常比单个服务版本错误更大。

健康和容量信号也不能有单点。调度中心常依赖主动探测、SLI、RUM、网关统计、云监控、服务发现和容量平台。如果只有一个探测点或一个指标后端，误判会非常危险。更稳妥的做法是多来源采样、连续窗口确认、异常去抖、人工保护阈值和区域隔离。比如一个地区探测失败，不一定代表用户侧不可达；一个指标平台超时，也不应该直接把所有流量切走。

还要有明确的人工兜底和审计。高危策略需要审批、双人复核或变更窗口；紧急切流需要预案化命令；每次策略变更要能追溯操作者、版本、原因、影响范围和回滚点。调度中心越中心化，越需要权限隔离、租户限额、规则复杂度限制和演练。否则它虽然提高了日常调度效率，却会在错误配置或中心故障时变成全站级单点。

## 113. 流量调度中心与上游或下游组件之间如何传递真实客户端 IP、协议、超时和追踪上下文？

可以先这样答：流量调度中心通常传递的是策略和元数据，不是每个请求的上下文。真实客户端 IP、协议、超时和追踪上下文仍然由 DNS、CDN、负载均衡、网关、代理、sidecar、SDK 和应用在请求路径上维护。调度中心要做的是把策略写成适配不同执行层能力的配置，并要求执行层把调度决策记录到日志、指标和 trace 里。

真实客户端 IP 在不同层的可见性不同。DNS/GSLB 看到的是递归解析器地址，可能通过 EDNS Client Subnet 获得部分客户端网段，但它通常看不到完整 HTTP 请求；CDN、WAF、ALB、Nginx、Envoy 这类 L7 入口可以通过 `X-Forwarded-For`、`Forwarded`、`X-Real-IP` 或 PROXY protocol 传递来源；内部 mesh 或 SDK 看到的则可能是上一跳代理地址。调度中心如果按地域或网段做策略，必须基于可信入口归一化后的来源信息，而不能混用不同层的原始字段。

协议能力要被建模。DNS 只能做域名级或解析级调度；L4 负载均衡主要处理 TCP/UDP/TLS 连接；L7 网关能看 Host、Path、Header、Method、Cookie；服务网格还能按服务、版本、subset、gRPC 方法等维度治理；SDK 可以结合调用方、租户、实例元数据和本地负载反馈。调度中心的规则引擎要知道每条策略能下发到哪里，不能生成执行层不理解的规则。

超时也分为策略超时和请求超时。调度中心可以下发 route timeout、connect timeout、per-try timeout、retry budget、熔断阈值、健康探测超时和配置 TTL；应用和 RPC 框架负责传播端到端 deadline。比如 gRPC deadline 应该随调用链向下游传递，网关的 route timeout 只是其中一层保护。配置中心的租约或缓存 TTL 也不是请求超时，不能混为一谈。

追踪上下文方面，L7 网关、Envoy、sidecar 和应用应该透传 `traceparent`、`tracestate` 或对应 APM Header，gRPC 使用 metadata 传递。调度中心可以要求执行层给 span、访问日志和指标补充 `traffic_policy_version`、`route_id`、`decision_reason`、`target_region`、`target_cluster`、`backend_pool`、`fallback_used`、`health_snapshot_version` 等字段。这样一次请求为什么被调到某个区域、为什么走了备用池、是否命中降级策略，才有证据可查。

对于 DNS、GSLB、CDN 这类不一定能进入同一个分布式 trace 的层，还需要做关联日志。可以用请求 ID、边缘 Ray ID、访问日志时间窗口、用户区域、目标 origin、策略版本、权重快照，把入口决策和后端 trace 关联起来。面试中可以强调：调度中心不是简单塞几个 Header，而是要让不同执行层在各自能力范围内保留决策证据。

## 114. 流量调度中心如何做健康检查、摘除、恢复和流量预热？

可以先这样答：流量调度中心的健康治理要比单个负载均衡更宏观。它不仅看某个实例是否存活，还要看地域、机房、集群、服务池、链路、依赖和容量是否适合继续承接流量。健康检查来源一般包括主动探测、边缘访问质量、网关状态、服务发现、SLI/SLO、容量平台、云监控、RUM 和业务指标。

健康判断要多来源、带去抖。一个探测点失败可能是探测网络问题，一个指标窗口异常可能是采样延迟，一个地区用户报错可能是运营商链路问题。调度中心通常要设连续失败阈值、最小样本量、多个探测点投票、区域权重、冷却时间和人工保护线，避免在短暂抖动时来回切流。入口调度尤其要小心 DNS TTL、CDN 缓存和客户端连接复用造成的滞后。

摘除动作要按层执行。DNS/GSLB 场景可以把异常地域或 origin 的权重降为 0，或从答案集中移除；CDN/负载均衡场景可以摘除 origin、pool、target group；Gateway/mesh 场景可以删除 endpoint、调整 subset 权重、触发 outlier detection 或熔断；SDK 场景可以把实例标记为 ejected。摘除后要确认实际流量是否下降，而不是只看配置已经改了。

恢复要有滞后和预热，不要健康一恢复就全量切回。一个集群刚恢复时，缓存是冷的，连接池是空的，HPA 还没扩上来，数据库和下游依赖也可能刚从故障中恢复。如果立即把所有流量切回，容易造成二次故障。更稳妥的做法是先让目标池通过主动探测和合成交易，再给极小权重，观察错误率、延迟、饱和度和容量，再逐步恢复到正常权重。

流量预热要覆盖数据面和后端。数据面预热包括先下发但不启用规则、建立 Envoy cluster/listener、拉取服务发现、建立 TLS session、刷新 DNS 和证书；后端预热包括扩容实例、打开 readiness、预热缓存、预建连接池、加载模型或索引、验证下游限额。对于跨地域恢复，还要考虑网络路径、带宽、数据库复制延迟和数据一致性窗口。

还要监控期望流量和实际流量的偏差。调度中心设置了 10% 权重，不代表真实流量马上按 10% 到达，DNS 缓存、客户端长连接、HTTP/2 连接复用、运营商递归解析、CDN 边缘缓存都会造成滞后。健康摘除和恢复都应该以实际请求量、连接数、错误率和后端负载为准，配置状态只是其中一个信号。

## 115. 流量调度中心的观测指标应该包括哪些？如何判断它自身成为瓶颈？

可以先这样答：流量调度中心的观测指标要覆盖“策略计算、配置分发、数据面接受、实际流量、健康信号和业务结果”六个层面。只看某个服务的 QPS 或错误率，不足以判断调度中心是否工作正常；调度中心的核心风险是策略没有按预期生效，或者策略生效太慢、太频繁、太不稳定。

策略层指标包括策略版本号、变更次数、规则数量、规则大小、匹配条件复杂度、权重配置、目标池数量、冲突规则数、校验失败数、回滚次数、人工审批耗时、紧急切流次数、fallback 命中次数。配置太复杂时，调度中心可能在计算、校验、审计或渲染下游配置时变慢。

分发层指标包括配置生成时延、推送时延、各数据面订阅延迟、ACK/NACK 数量、NACK 原因、watch 断连次数、重连次数、全量推送次数、增量推送次数、配置版本滞后、控制器队列长度、API 错误率、存储读写延迟。Envoy xDS、Gateway Status、Ingress Controller 事件、CDN/API 调用返回和 SDK 配置拉取日志，都可以作为证据来源。

实际流量层指标包括 desired weight 与 observed weight 的偏差、各地域/集群/服务池 QPS、连接数、带宽、成功率、p95/p99 延迟、状态码分布、重试率、熔断率、限流率、黑洞路由数、无匹配路由数、降级路由命中数。调度中心最怕“控制面显示已经切走，实际流量还在旧路径上”或“权重看起来均匀，某个后端池已经饱和”。

健康信号层指标包括探测成功率、探测延迟、探测点分布、连续失败次数、健康状态翻转次数、抖动次数、容量利用率、可用副本数、Endpoint 变化、outlier ejection、SLO burn rate、RUM 区域质量、指标采样延迟。健康状态频繁翻转会导致调度策略振荡，最终表现成用户侧时好时坏。

判断调度中心自身成为瓶颈，可以看几个现象。第一，后端服务和网关处理能力正常，但策略变更从提交到生效的时间越来越长。第二，数据面大量停留在旧策略版本，ACK/NACK、watch lag、配置拉取失败或 SDK 配置过期明显上升。第三，调度中心 CPU、内存、数据库连接、队列长度、指标查询耗时先达到上限。第四，健康状态计算滞后，故障已经恢复或恶化，但调度策略仍按旧状态动作。第五，策略频繁振荡，流量在多个池之间来回摆动，引发更多连接重建、缓存失效和下游抖动。

排查时要把它和普通后端瓶颈分开。可以固定路由或绕过调度策略观察业务是否恢复；比较期望权重和真实流量；查看数据面是否拒绝配置；检查健康信号是否延迟或冲突；把一次切流拆成策略计算、审批、配置生成、配置推送、数据面接受、真实流量变化几个时间点。如果慢发生在这些调度链路上，才说明调度中心是瓶颈；如果慢发生在目标集群 CPU、数据库、缓存或下游 RPC，则调度中心只是把流量送到了一个已经不健康的地方。

## References

### Questions 1-115

- AWS Route 53: [Weighted routing](https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/routing-policy-weighted.html)
- AWS Route 53: [Creating Amazon Route 53 health checks](https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/dns-failover.html)
- AWS Route 53: [Multivalue answer routing](https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/routing-policy-multivalue.html)
- AWS Route 53: [Latency-based routing](https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/routing-policy-latency.html)
- Cloudflare Load Balancing: [Traffic steering](https://developers.cloudflare.com/load-balancing/understand-basics/traffic-steering/)
- Cloudflare Load Balancing: [How endpoints and pools become unhealthy](https://developers.cloudflare.com/load-balancing/understand-basics/health-details/)
- Cloudflare Fundamentals: [Cloudflare HTTP headers](https://developers.cloudflare.com/fundamentals/reference/http-headers/)
- Amazon CloudFront: [Request and response behavior for custom origins](https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/RequestAndResponseBehaviorCustomOrigin.html)
- Amazon CloudFront: [Optimize high availability with CloudFront origin failover](https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/high_availability_origin_failover.html)
- Amazon CloudFront: [Use real-time access logs](https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/real-time-logs.html)
- Amazon CloudFront: [Monitor CloudFront metrics with Amazon CloudWatch](https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/monitoring-using-cloudwatch.html)
- Cloudflare Analytics: [Datasets](https://developers.cloudflare.com/analytics/graphql-api/features/data-sets/)
- IETF: [RFC 4786, Operation of Anycast Services](https://www.rfc-editor.org/rfc/rfc4786.html)
- IETF: [RFC 4271, A Border Gateway Protocol 4](https://www.rfc-editor.org/rfc/rfc4271.html)
- IETF: [RFC 2991, Multipath Issues in Unicast and Multicast Next-Hop Selection](https://www.rfc-editor.org/rfc/rfc2991.html)
- IETF: [RFC 2992, Analysis of an Equal-Cost Multi-Path Algorithm](https://www.rfc-editor.org/rfc/rfc2992.html)
- IETF: [RFC 7871, Client Subnet in DNS Queries](https://www.rfc-editor.org/rfc/rfc7871.html)
- Linux Kernel: [IPvs sysctl documentation](https://www.kernel.org/doc/html/latest/networking/ipvs-sysctl.html)
- ipvsadm: [Virtual Server administration man page](https://manpages.debian.org/bookworm/ipvsadm/ipvsadm.8.en.html)
- Keepalived: [Software design](https://www.keepalived.org/doc/software_design.html)
- Keepalived: [Load balancing techniques](https://www.keepalived.org/doc/load_balancing_techniques.html)
- Keepalived: [IPVS scheduling algorithms](https://www.keepalived.org/doc/scheduling_algorithms.html)
- Keepalived: [IPVS protocol support](https://www.keepalived.org/doc/protocol_support.html)
- Keepalived: [Case study: Healthcheck](https://www.keepalived.org/doc/case_study_healthcheck.html)
- Keepalived: [Case study: Failover using VRRP](https://www.keepalived.org/doc/case_study_failover.html)
- Keepalived: [Configuring SNMP support](https://www.keepalived.org/doc/snmp_support.html)
- NGINX: [HTTP Load Balancing](https://docs.nginx.com/nginx/admin-guide/load-balancer/http-load-balancer/)
- NGINX: [Module ngx_http_upstream_module](https://nginx.org/en/docs/http/ngx_http_upstream_module.html)
- NGINX: [Module ngx_http_proxy_module](https://nginx.org/en/docs/http/ngx_http_proxy_module.html)
- NGINX: [Module ngx_http_realip_module](https://nginx.org/en/docs/http/ngx_http_realip_module.html)
- NGINX: [Module ngx_http_stub_status_module](https://nginx.org/en/docs/http/ngx_http_stub_status_module.html)
- NGINX: [HTTP Health Checks](https://docs.nginx.com/nginx/admin-guide/load-balancer/http-health-check/)
- NGINX: [High Availability Support for NGINX Plus in On-Premises Deployments](https://docs.nginx.com/nginx/admin-guide/high-availability/ha-keepalived/)
- HAProxy: [Configuration Manual](https://docs.haproxy.org/3.0/configuration.html)
- HAProxy: [Health checks](https://www.haproxy.com/documentation/haproxy-configuration-tutorials/reliability/health-checks/)
- HAProxy: [Runtime API show stat](https://www.haproxy.com/documentation/haproxy-runtime-api/reference/show-stat/)
- Envoy: [Terminology](https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/intro/terminology)
- Envoy: [HTTP connection management](https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/http/http_connection_management)
- Envoy: [xDS configuration API overview](https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/operations/dynamic_configuration)
- Envoy: [xDS REST and gRPC protocol](https://www.envoyproxy.io/docs/envoy/latest/api-docs/xds_protocol)
- Envoy: [Service discovery](https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/upstream/service_discovery)
- Envoy: [Health checking](https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/upstream/health_checking)
- Envoy: [Load balancing overview](https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/upstream/load_balancing/overview)
- Envoy: [Outlier detection](https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/upstream/outlier)
- Envoy: [Circuit breaking](https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/upstream/circuit_breaking)
- Envoy: [Administration interface](https://www.envoyproxy.io/docs/envoy/latest/operations/admin)
- Envoy: [HTTP header manipulation](https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_conn_man/headers)
- Envoy: [Slow start mode](https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/upstream/load_balancing/slow_start)
- Envoy: [Cluster manager statistics](https://www.envoyproxy.io/docs/envoy/latest/configuration/upstream/cluster_manager/cluster_stats)
- F5 BIG-IP TMSH: [ltm virtual](https://clouddocs.f5.com/cli/tmsh-reference/latest/modules/ltm/ltm_virtual.html)
- F5 BIG-IP TMSH: [ltm pool](https://clouddocs.f5.com/cli/tmsh-reference/latest/modules/ltm/ltm_pool.html)
- F5 BIG-IP TMSH: [ltm profile http](https://clouddocs.f5.com/cli/tmsh-reference/latest/modules/ltm/ltm_profile_http.html)
- F5 BIG-IP TMSH: [cm device-group](https://clouddocs.f5.com/cli/tmsh-reference/latest/modules/cm/cm_device-group.html)
- F5 BIG-IP TMSH: [ltm profile analytics](https://clouddocs.f5.com/cli/tmsh-reference/latest/modules/ltm/ltm_profile_analytics.html)
- AWS Elastic Load Balancing: [How Elastic Load Balancing works](https://docs.aws.amazon.com/elasticloadbalancing/latest/userguide/how-elastic-load-balancing-works.html)
- AWS Elastic Load Balancing: [What is an Application Load Balancer?](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/introduction.html)
- AWS Elastic Load Balancing: [HTTP headers and Application Load Balancers](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/x-forwarded-headers.html)
- AWS Elastic Load Balancing: [Health checks for Application Load Balancer target groups](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/target-group-health-checks.html)
- AWS Elastic Load Balancing: [Edit target group attributes for Application Load Balancer](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/edit-target-group-attributes.html)
- AWS Elastic Load Balancing: [CloudWatch metrics for Application Load Balancer](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/load-balancer-cloudwatch-metrics.html)
- AWS Elastic Load Balancing: [Access logs for Application Load Balancer](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/load-balancer-access-logs.html)
- AWS Elastic Load Balancing: [Condition types for Application Load Balancer listener rules](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/rule-condition-types.html)
- AWS Elastic Load Balancing: [What is a Network Load Balancer?](https://docs.aws.amazon.com/elasticloadbalancing/latest/network/introduction.html)
- AWS Elastic Load Balancing: [Health checks for Network Load Balancer target groups](https://docs.aws.amazon.com/elasticloadbalancing/latest/network/target-group-health-checks.html)
- AWS Elastic Load Balancing: [Edit target group attributes for Network Load Balancer](https://docs.aws.amazon.com/elasticloadbalancing/latest/network/edit-target-group-attributes.html)
- AWS Elastic Load Balancing: [CloudWatch metrics for Network Load Balancer](https://docs.aws.amazon.com/elasticloadbalancing/latest/network/load-balancer-cloudwatch-metrics.html)
- AWS Elastic Load Balancing: [Access logs for Network Load Balancer](https://docs.aws.amazon.com/elasticloadbalancing/latest/network/load-balancer-access-logs.html)
- AWS API Gateway: [What is Amazon API Gateway?](https://docs.aws.amazon.com/apigateway/latest/developerguide/welcome.html)
- AWS API Gateway: [API endpoint types for REST APIs](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-api-endpoint-types.html)
- AWS API Gateway: [Amazon API Gateway important notes](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-known-issues.html)
- AWS API Gateway: [Amazon API Gateway dimensions and metrics](https://docs.aws.amazon.com/apigateway/latest/developerguide/api-gateway-metrics-and-dimensions.html)
- Kubernetes: [Service](https://kubernetes.io/docs/concepts/services-networking/service/)
- Kubernetes: [EndpointSlices](https://kubernetes.io/docs/concepts/services-networking/endpoint-slices/)
- Kubernetes: [Liveness, Readiness, and Startup Probes](https://kubernetes.io/docs/concepts/workloads/pods/probes/)
- Kubernetes: [Deployments](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/)
- Kubernetes: [Ingress](https://kubernetes.io/docs/concepts/services-networking/ingress/)
- Kubernetes: [Ingress Controllers](https://kubernetes.io/docs/concepts/services-networking/ingress-controllers/)
- Kubernetes: [Kubernetes Metrics Reference](https://kubernetes.io/docs/reference/instrumentation/metrics/)
- Ingress-NGINX Controller: [Annotations](https://kubernetes.github.io/ingress-nginx/user-guide/nginx-configuration/annotations/)
- Ingress-NGINX Controller: [Prometheus and Grafana installation](https://kubernetes.github.io/ingress-nginx/user-guide/monitoring/)
- Ingress-NGINX Controller: [Miscellaneous](https://kubernetes.github.io/ingress-nginx/user-guide/miscellaneous/)
- Gateway API: [API Overview](https://gateway-api.sigs.k8s.io/concepts/api-overview/)
- Gateway API: [GatewayClass](https://gateway-api.sigs.k8s.io/api-types/gatewayclass/)
- Gateway API: [Gateway](https://gateway-api.sigs.k8s.io/api-types/gateway/)
- Gateway API: [HTTPRoute](https://gateway-api.sigs.k8s.io/api-types/httproute/)
- Gateway API: [HTTP traffic splitting](https://gateway-api.sigs.k8s.io/guides/user-guides/traffic-splitting/)
- Gateway API: [HTTP timeouts](https://gateway-api.sigs.k8s.io/guides/user-guides/http-timeouts/)
- Gateway API: [Troubleshooting and Status](https://gateway-api.sigs.k8s.io/concepts/troubleshooting/)
- Gateway API: [HTTP Header Modifiers](https://gateway-api.sigs.k8s.io/guides/user-guides/http-header-modifier/)
- Gateway API: [Gateway API for Service Mesh](https://gateway-api.sigs.k8s.io/mesh/)
- Istio: [Architecture](https://istio.io/latest/docs/ops/deployment/architecture/)
- Istio: [Traffic Management](https://istio.io/latest/docs/concepts/traffic-management/)
- Istio: [Observability](https://istio.io/latest/docs/concepts/observability/)
- Istio: [Installing the Sidecar](https://istio.io/latest/docs/setup/additional-setup/sidecar-injection/)
- Istio: [Health Checking of Istio Services](https://istio.io/latest/docs/ops/configuration/mesh/app-health-check/)
- Istio: [Envoy Statistics](https://istio.io/latest/docs/ops/configuration/telemetry/envoy-stats/)
- Istio: [Circuit Breaking](https://istio.io/latest/docs/tasks/traffic-management/circuit-breaking/)
- Istio: [Request Timeouts](https://istio.io/latest/docs/tasks/traffic-management/request-timeouts/)
- Argo Rollouts: [Canary deployment strategy](https://argo-rollouts.readthedocs.io/en/stable/features/canary/)
- Argo Rollouts: [Analysis](https://argo-rollouts.readthedocs.io/en/stable/features/analysis/)
- Argo Rollouts: [Traffic management](https://argo-rollouts.readthedocs.io/en/stable/features/traffic-management/)
- Flagger: [How it works](https://docs.flagger.app/main/usage/how-it-works)
- Flagger: [Metrics analysis](https://docs.flagger.app/main/usage/metrics)
- gRPC: [Custom Load Balancing Policies](https://grpc.io/docs/guides/custom-load-balancing/)
- gRPC: [Custom Backend Metrics](https://grpc.io/docs/guides/custom-backend-metrics/)
- gRPC: [Health Checking](https://grpc.io/docs/guides/health-checking/)
- gRPC: [Deadlines](https://grpc.io/docs/guides/deadlines/)
- gRPC: [Metadata](https://grpc.io/docs/guides/metadata/)
- gRPC: [Retry](https://grpc.io/docs/guides/retry/)
- gRPC: [Service Config](https://grpc.io/docs/guides/service-config/)
- gRPC: [OpenTelemetry Metrics](https://grpc.io/docs/guides/opentelemetry-metrics/)
- Apache Kafka: [KafkaConsumer API](https://kafka.apache.org/40/javadoc/org/apache/kafka/clients/consumer/KafkaConsumer.html)
- Apache Kafka: [Consumer configuration](https://kafka.apache.org/40/generated/consumer_config.html)
- RabbitMQ: [Consumers](https://www.rabbitmq.com/docs/consumers)
- RabbitMQ: [Consumer acknowledgements and publisher confirms](https://www.rabbitmq.com/docs/confirms)
- RabbitMQ: [Consumer prefetch](https://www.rabbitmq.com/docs/consumer-prefetch)
- RabbitMQ: [Monitoring](https://www.rabbitmq.com/docs/monitoring)
- OpenTelemetry: [Semantic conventions for messaging spans](https://opentelemetry.io/docs/specs/semconv/messaging/messaging-spans/)
- Kubernetes: [Configure Liveness, Readiness and Startup Probes](https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/)
- Kubernetes: [Pod termination](https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#pod-termination)
- Amazon RDS: [Amazon RDS Proxy](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/rds-proxy.html)
- Amazon RDS: [RDS Proxy concepts and terminology](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/rds-proxy.howitworks.html)
- Amazon RDS: [Working with Amazon RDS Proxy endpoints](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/rds-proxy-endpoints.html)
- Amazon RDS: [Monitoring RDS Proxy metrics with Amazon CloudWatch](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/rds-proxy.monitoring.html)
- Amazon RDS: [Troubleshooting for RDS Proxy](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/rds-proxy.troubleshooting.html)
- ProxySQL: [Main Runtime](https://proxysql.com/documentation/main-runtime/)
- ProxySQL: [MySQL Monitor Module](https://proxysql.com/documentation/backend-monitoring/)
- ProxySQL: [Stats / Statistics](https://proxysql.com/documentation/stats-statistics/)
- ProxySQL: [PROXY Protocol Support](https://proxysql.com/documentation/proxy-protocol/)
- ProxySQL: [ProxySQL Cluster](https://proxysql.com/documentation/proxysql-cluster/)
- PgBouncer: [Usage](https://www.pgbouncer.org/usage.html)
- PgBouncer: [Configuration](https://www.pgbouncer.org/config.html)
- PgBouncer: [Features](https://www.pgbouncer.org/features.html)
- PgBouncer: [FAQ](https://www.pgbouncer.org/faq.html)
- PostgreSQL: [libpq connection control functions](https://www.postgresql.org/docs/current/libpq-connect.html)
- MySQL: [Performance Schema connection attribute tables](https://dev.mysql.com/doc/refman/8.4/en/performance-schema-connection-attribute-tables.html)
- IETF: [RFC 7239, Forwarded HTTP Extension](https://datatracker.ietf.org/doc/html/rfc7239)
- W3C: [Trace Context](https://www.w3.org/TR/trace-context/)
