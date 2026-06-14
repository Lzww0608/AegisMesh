# 02. RPC、gRPC 与通信协议

## 简单

### Q029【简单】RPC 和 HTTP REST 的核心差异是什么？

RPC 更像“调用远程函数”。客户端关心的是 service、method、request、response，比如 `UserService.GetUser`。HTTP REST 更强调资源和 HTTP 语义，常见形式是 `GET /users/{id}`、`POST /orders`。

我会从抽象和治理两个角度讲。RPC 面向方法，REST 面向资源；gRPC 通常用 proto 定义强类型接口，REST 常用 OpenAPI 或文档约定 JSON 字段。更关键的是，RPC 框架天然知道方法名、状态码、deadline、metadata，更适合做方法级 timeout、retry、负载均衡和 trace。

在 AegisMesh 里，我需要在客户端 SDK 里拿到 gRPC method、status code、attempt、deadline 和 upstream，所以 gRPC 比普通 HTTP JSON 更合适。

### Q030【简单】为什么项目选择 gRPC 而不是 HTTP JSON？

主要是为了用 gRPC 已经提供好的扩展点和强类型协议。

gRPC 有 resolver、balancer、interceptor、metadata、deadline、status code 这些机制。AegisMesh 的服务发现、adaptive P2C、retry budget、telemetry 和 trace 都能直接挂上去。如果用普通 HTTP JSON，就要自己封装大量客户端逻辑，而且不同 HTTP client 的行为不统一。

protobuf 也让控制面协议更稳定。Registry、Telemetry、Policy 这些接口都有明确 schema，比手写 JSON 字段更适合长期演进。

### Q031【简单】gRPC 中 channel、ClientConn、SubConn、resolver、balancer 分别是什么？

在 Go gRPC 里，`ClientConn` 可以理解为客户端到一个逻辑目标的 channel。业务 stub 基于 `ClientConn` 发起 RPC。

resolver 负责把逻辑目标解析成地址列表。AegisMesh 的目标形如 `aegis://127.0.0.1:9000/user-service`，resolver 会向 Controller 查询 `user-service` 的实例列表。

balancer 负责根据 resolver 给出的地址管理连接并选择一个可用后端。AegisMesh 默认注册了 `aegis_adaptive_p2c` balancer。

`SubConn` 是 balancer 管理的后端连接抽象，通常对应某个 backend address 的连接状态。每次 RPC 发送前，picker 会选一个 `SubConn`。

### Q032【简单】protobuf 的好处是什么？它相比 JSON 有哪些取舍？

protobuf 的好处是 schema 明确、二进制编码紧凑、字段编号稳定，还能生成代码。客户端和服务端都从 `.proto` 生成代码，接口类型清楚，运行时字段名写错的问题会少很多。

相比 JSON，它的可读性差一些，调试时不如直接看文本方便；schema 演进也要更小心，比如不能随便复用已经删除的字段号。JSON 的优势是简单、通用、便于人工调试；protobuf 的优势是更适合内部 RPC、控制面协议和跨语言 SDK。

AegisMesh 里 Registry、Telemetry、Policy 都适合 protobuf，因为这些接口字段固定，而且要被 Controller 和 SDK 长期共同使用。

### Q033【简单】gRPC 的 unary call、server streaming、client streaming、bidirectional streaming 分别适合什么场景？

unary call 就是一个请求对应一个响应，适合普通查询、注册、心跳、上报一批数据。AegisMesh 的 `RegisterInstance`、`Heartbeat`、`ListInstances`、`ReportEndpointStats` 都是 unary。

server streaming 是客户端发一个请求，服务端持续返回多个响应，适合配置 watch、日志订阅、事件流。AegisMesh 的 `WatchPolicy` 就是 server streaming。

client streaming 是客户端持续上传多条消息，服务端最后返回一个响应，适合批量上传指标或日志。AegisMesh 现在 telemetry 是 unary 批量上报；如果以后上报频率更高，可以考虑 client streaming。

bidirectional streaming 是两边都可以持续收发，适合双向控制通道、实时协商、长连接代理等场景。AegisMesh 当前还没有必须使用 bidi streaming 的接口。

### Q034【简单】项目里哪些接口适合 unary，哪些接口适合 streaming？

当前项目里，demo 业务接口 `GetUser` 和 `CreateOrder` 都是 unary，因为它们就是普通的一次请求一次响应。

Registry 的 `RegisterInstance`、`Heartbeat`、`ListInstances` 也适合 unary。注册和心跳都是离散操作，`ListInstances` 当前由 resolver 定时拉取，返回一批实例就够了。

Telemetry 的 `ReportEndpointStats` 当前也是 unary，一次上报一个窗口内的多条 endpoint sample。高 QPS 或更细粒度场景下，可以改成 client streaming。

PolicyService 里 `GetPolicy` 适合 unary，用于 SDK 初始拉取；`WatchPolicy` 适合 server streaming，用于策略热更新。

### Q035【简单】gRPC 状态码 UNAVAILABLE 和 DEADLINE_EXCEEDED 在重试策略中有什么意义？

`UNAVAILABLE` 通常表示服务暂时不可用，比如连接失败、后端不可达、负载均衡没有可用 SubConn。它经常可以重试，但前提是方法幂等，而且有 retry budget。

`DEADLINE_EXCEEDED` 表示调用超过了客户端设置的 deadline 或 timeout。它可能是后端慢、网络慢，也可能是客户端设置太短。AegisMesh 默认把 `UNAVAILABLE` 和 `DEADLINE_EXCEEDED` 放进 retryable codes，但仍然通过 max attempts 和 retry budget 限制放大。

这里要特别注意 `DEADLINE_EXCEEDED`。对非幂等方法来说，它很危险。比如 `CreateOrder` 如果服务端已经创建订单但响应超时，客户端再重试就可能创建重复订单，所以项目里支持 method-level idempotency policy。

### Q036【简单】deadline、timeout、context cancellation 在 gRPC 里有什么区别？

deadline 是一个绝对时间点，意思是“到这个时间还没完成就取消”。timeout 是相对时长，意思是“最多等多久”。在 Go 里通常通过 `context.WithDeadline` 或 `context.WithTimeout` 表达，最后都会变成 context 的取消信号。

context cancellation 更通用。它可能来自 deadline/timeout，也可能来自调用方主动取消，比如 HTTP 请求断开、服务关闭、上游不再需要结果。

AegisMesh 里 frontend 会给 checkout 请求设置 overall timeout；retry interceptor 又会给每次尝试设置 per-try timeout。两者叠加时，谁先到期谁生效。

### Q037【简单】HTTP/2 对 gRPC 有什么关键支持？

HTTP/2 给 gRPC 提供了几件关键能力：二进制 framing、多路复用、header/trailer、流控和长连接。gRPC 的 metadata、status、streaming 都依赖这些能力。

多路复用让一个 TCP 连接上可以同时跑多个 RPC；header 和 trailer 让 gRPC 可以传 metadata 和最终 status；流控避免某个流无限制占用发送窗口。

但 HTTP/2 不是万能的。它解决了应用层每个请求一个连接的问题，但底层如果还是 TCP，丢包时仍可能出现 TCP 层面的队头阻塞。

### Q038【简单】为什么 RPC 治理需要方法级别的策略？

因为同一个服务里的方法语义不同。`GetUser` 是读请求，通常可以重试；`CreateOrder` 是写请求，如果没有幂等 key，重试可能产生副作用。

timeout 也一样。查询用户信息可能要求 150ms 内返回，创建订单可能允许更长时间。错误率阈值、retry attempts、budget、circuit breaker 也可能按方法不同。

AegisMesh 的 PolicyService 支持 method policy：可以把 `GetUser` 配成 retryable，把 `CreateOrder` 标成 non-idempotent 并限制为单次尝试。这比只做 service-level 策略更安全。

## 深度

### Q039【深度】gRPC resolver 如何把逻辑服务名转换成后端地址列表？AegisMesh 的 resolver 做了哪些扩展？

gRPC resolver 的职责很直接：把 target string 解析成地址列表，必要时再带上 service config。最常见的是 DNS resolver，输入域名，输出 IP 地址。

AegisMesh 自定义了 `aegis` scheme。客户端 dial 的目标是 `aegis://<controller-addr>/<service>`，比如 `aegis://127.0.0.1:9000/user-service`。resolver 解析出 Controller 地址和 service 名，然后调用 `RegistryService.ListInstances`。

关键扩展在地址属性上。resolver 不只是返回 `ip:port`，还把 `instance_id`、`status`、`slow_score` 写进 `resolver.Address.Attributes`。后面的 adaptive balancer 会读取这些属性，计算路由成本。

另外，resolver 会过滤掉 `EJECTED` 和 `DEAD`，保留 `HEALTHY`、`DEGRADED`、`PROBING`。当前实现是默认 3 秒轮询 Controller，而不是 server push。

### Q040【深度】gRPC balancer pick 的时机是什么？PickResult 的 Done 回调适合记录哪些数据？

Pick 发生在每次 RPC 需要选后端的时候。resolver 更新地址后，balancer 维护 Ready SubConn 集合，并构建 picker；真正发送 RPC 前，gRPC 会调用 picker 选一个 SubConn。

AegisMesh 的 `Pick` 会把每个 ready endpoint 的 in-flight、local EWMA、status、slow_score 组装成 routing endpoint，然后用 adaptive P2C 选一个地址。选中后会先通过 circuit breaker 的 `Acquire`，再增加本地 in-flight。

`PickResult.Done` 在 RPC 完成后调用，适合记录本次 attempt 的耗时，释放 in-flight，更新 EWMA，再释放 circuit breaker token。AegisMesh 当前 Done 里做的就是这些本地状态更新。更细的成功率统计也可以从 `DoneInfo` 里拿，不过项目里的主要 RPC telemetry 放在 interceptor 里记录。

### Q041【深度】在 gRPC 里实现客户端拦截器时，重试拦截器和 telemetry 拦截器的顺序为什么重要？

顺序很关键，因为它决定 telemetry 看到的是“一个逻辑请求”，还是“每一次 attempt”。

AegisMesh 现在使用：

```go
grpc.WithChainUnaryInterceptor(
    newRetryUnaryInterceptorFromSource(retrySource),
    newTelemetryUnaryInterceptor(source, service, recorder, tracer),
)
```

retry interceptor 在外层，telemetry interceptor 在内层。这样 retry 每发起一次 attempt，都会调用一次内层 telemetry。telemetry 就能记录每次尝试的 latency、status、upstream 和 attempt。

如果顺序反过来，telemetry 只包住整个逻辑调用，可能只能看到最终结果和总耗时，无法区分第 1 次失败、第 2 次成功，也不利于计算 retry_count 和 trace。

### Q042【深度】如果一个请求被重试多次，trace、attempt、status 应该如何记录才不混淆？

我会这样记录：同一个逻辑请求保持同一个 trace_id，attempt 逐次递增，span_id 按 attempt 生成。这样既能看出“这些 attempt 属于同一个用户请求”，也能分清每次实际 RPC 的结果。

AegisMesh 的 retry interceptor 会先 `ensureTraceID`，然后每次 attempt 写入 `contextWithAttempt`。telemetry interceptor 会生成新的 span_id，把 `x-aegis-trace-id`、`x-aegis-span-id`、`x-aegis-attempt` 放进 outgoing metadata，并写 JSONL trace。

trace 记录里包含 `trace_id`、`span_id`、`method`、`upstream`、`attempt`、`retry_attempts`、`status`。如果第 1 次 `UNAVAILABLE`、第 2 次成功，应该能看到同一个 trace_id 下两条 attempt 记录，而不是把它们混成一个状态。

### Q043【深度】per-try timeout 与 overall timeout 应该如何组合？项目现在的设计可能有什么边界问题？

overall timeout 是整个逻辑请求的预算，per-try timeout 是单次 attempt 的预算。合理组合应该是：每次 attempt 不超过 per-try timeout，所有 attempt 加起来也不能超过 overall timeout。

AegisMesh 当前 retry interceptor 对每次 attempt 使用 `context.WithTimeout(parentCtx, PerTryTimeout)`。如果外层 parent context 已经有更短 deadline，Go context 会先按 parent 取消；如果外层没有 overall deadline，总耗时可能接近 `MaxAttempts * PerTryTimeout`。

边界在于预算分配还比较简单。比如剩余 overall 时间已经不足一次完整 per-try timeout，当前实现没有显式计算“剩余总预算再决定是否发起下一次重试”。后续可以在 retry loop 里检查 parent deadline，把 per-try timeout 截断到剩余时间；剩余时间太短时直接停止重试。

### Q044【深度】gRPC 的连接复用会如何影响负载均衡效果？

gRPC 基于 HTTP/2，一个 SubConn 上可以复用同一条连接承载多个 RPC。好处是连接数少、握手开销低；问题是负载均衡不是“每个 TCP 连接一个请求”，而是看 balancer 有没有在每次 RPC 前选 SubConn。

如果使用 `pick_first`，ClientConn 可能长期使用第一个可用连接，负载可能集中到一个后端。`round_robin` 和 AegisMesh 的 adaptive P2C 都是在 RPC 级别 pick SubConn，可以把请求分散到多个后端。

长连接和 streaming 会带来额外影响。一个长时间运行的 stream 选中某个 SubConn 后会一直占用它，后续负载均衡不会把这个 stream 中途迁移到别的后端。因此治理系统既要看每次 pick，也要看 in-flight 和长连接压力。

### Q045【深度】如果服务端长连接保持成功但业务处理变慢，gRPC 层能否自然感知？

只靠连接层感知不到。HTTP/2 连接还活着、TCP 没断、keepalive 正常，并不代表业务处理快。

gRPC 能感知的主要是调用结果：成功、错误、deadline exceeded、连接异常等。如果服务端只是处理慢但仍然返回 OK，gRPC 不会自动认为它不健康。

所以 AegisMesh 不把连接存活当作健康结论，而是记录真实业务 RPC 的 latency EWMA、p95、timeout、error、in-flight，再由 Controller 计算 slow_score。

### Q046【深度】resolver 周期性拉取和服务端 watch 推送各有什么优缺点？

周期性拉取简单，客户端定时 `ListInstances` 就行，Controller 也不用维护大量长连接。AegisMesh 当前 resolver 默认 3 秒刷新一次。缺点是状态传播有延迟，刷新间隔内客户端可能继续使用旧地址或旧状态。

watch 推送延迟更低。Controller 一旦发现 endpoint 状态变化，就可以推给 SDK。它更适合频繁变化的服务发现和健康状态，但实现复杂度更高，要处理连接断开、重连、版本号、backpressure 和大量客户端订阅。

AegisMesh 已经在 PolicyService 上用了 `WatchPolicy`。如果下一步优化 resolver，我会增加 `WatchInstances` 或 endpoint health watch，把现在的轮询改成“初始拉取 + 增量推送”。

### Q047【深度】如果 Controller 不可达，已有 gRPC 连接和 resolver 状态应该怎样退化？

理想的退化方式是保留最后一次可用的地址列表和策略快照，让已有业务连接继续服务；新状态拉取失败只报警，不立刻清空地址。

AegisMesh 当前 resolver 在 `ListInstances` 失败时调用 `ReportError`，不会主动用空列表覆盖已有地址。也就是说，已经解析过的 ClientConn 通常还能继续用旧 SubConn 发请求。Policy watcher 失败后会 backoff 重试，已有 policy manager 里的快照还在。

边界是冷启动。如果客户端第一次启动时 Controller 就不可达，resolver 拿不到地址，新请求就没有可用后端。生产化可以加本地 bootstrap cache、DNS fallback，或者把最近一次 registry snapshot 缓存在 SDK 侧。

### Q048【深度】在拦截器里做重试时，如何避免对非幂等方法产生副作用？

第一，默认不要对非幂等方法重试。方法级策略要明确标出 `idempotent=false` 或 `retry.enabled=false`。AegisMesh 的 policy 支持 method-level retry，`CreateOrder` 这类写请求可以强制单次尝试。

第二，对必须重试的写请求，要引入 idempotency key。比如订单创建请求携带 `x-aegis-idempotency-key` 或业务 order token，服务端用这个 key 去重。

第三，重试条件要收窄。对 `UNAVAILABLE` 可以谨慎重试，对 `DEADLINE_EXCEEDED` 要更小心，因为服务端可能已经执行成功但响应超时。

项目现在的重点是通过 method policy 避免误重试；idempotency key 是后续可以补强的方向。

## 拓展

### Q049【拓展】HTTP/2 多路复用为什么不能完全解决队头阻塞？TCP 层的 HOL blocking 还会怎样影响 gRPC？

HTTP/2 多路复用解决的是 HTTP/1.1 那种应用层排队问题：一个连接上前一个响应没回来，后一个响应只能等着。HTTP/2 里多个 stream 可以共享一个连接，并交错发送 frame。

但 HTTP/2 通常跑在 TCP 上。TCP 要按字节序交付数据，如果某个包丢了，后面的字节即使已经到达，也不能先交给上层。于是同一条 TCP 连接上的多个 HTTP/2 stream 都可能被这个丢包拖慢。

对 gRPC 来说，这会表现为多个看似独立的 RPC 同时延迟升高。AegisMesh 的 eBPF TCP retransmit telemetry 就是为了给这类网络层异常提供额外信号。

### Q050【拓展】gRPC over HTTP/2 与 HTTP/3/QUIC 在尾延迟方面可能有什么差异？

HTTP/3/QUIC 基于 UDP，在传输层支持 stream 级别的独立恢复。某个 stream 丢包时，理论上不会像 TCP 那样阻塞同一连接上的所有 stream，所以在丢包场景下可能降低尾延迟。

但这不等于 QUIC 一定更快。QUIC 有自己的拥塞控制、加密、连接迁移和实现开销，部署上也受负载均衡、防火墙和观测工具影响。对于内网 RPC，HTTP/2/TCP 的生态和稳定性仍然很强。

如果 AegisMesh 未来支持 HTTP/3/QUIC，我会重点比较 packet loss 场景下的 p99、重传、连接迁移和 CPU 成本，而不是只比较平均延迟。

### Q051【拓展】xDS 协议如何把控制面配置下发给 Envoy/gRPC 客户端？

xDS 可以理解成一组控制面发现 API。控制面通过 xDS 向 Envoy 或支持 xDS 的 gRPC 客户端下发 listener、route、cluster、endpoint、负载均衡和安全配置。

粗略对应是：LDS 管 listener，RDS 管路由，CDS 管 cluster，EDS 管 endpoint。客户端或代理和 xDS management server 建立长连接，订阅资源，控制面用版本化配置推送更新。

如果 AegisMesh 接入 xDS，可以让 Controller 输出 endpoint weight、outlier state、retry policy 或 circuit breaker 配置，由 Envoy/gRPC xDS client 执行。这样 AegisMesh 可以专注 slow_score 和策略生成，不必自己实现所有数据面能力。

### Q052【拓展】如果要支持 hedged requests，和 retry 相比有什么不同风险？

retry 是前一次失败或超时以后再发下一次。hedged request 更激进：原请求还没失败，只是等了一小段时间还没回来，就并发发第二份请求，谁先成功用谁。

hedging 对尾延迟很有效，但风险更高。它会主动增加并发请求量，如果没有预算控制，可能比 retry 更容易放大下游压力。它也更依赖幂等性，因为两份请求可能都到达服务端。

如果 AegisMesh 支持 hedging，我会要求：只允许幂等方法，设置 hedge budget，限制最大并发副本，收到一个成功响应后取消其他 attempt，并把 hedged attempts 计入 telemetry 和 trace。

### Q053【拓展】为什么有些系统在 RPC 层做负载均衡，有些系统在 L4/L7 代理层做？

RPC 层负载均衡能看到方法名、deadline、status code、attempt、metadata 和本地 in-flight，决策更细。缺点是每种语言都要 SDK，升级成本和一致性会变成问题。

L4/L7 代理层负载均衡语言无关，部署统一，适合做 mTLS、限流、流量镜像、跨语言治理。缺点是代理未必知道业务方法语义，某些 per-method 策略需要解析协议或依赖额外 metadata。

AegisMesh 选择 RPC SDK，是因为项目重点是 fail-slow 和方法级治理。若要多语言落地，可以把 slow_score 控制面保留，数据面迁移到 Envoy sidecar 或 xDS。

### Q054【拓展】protobuf schema 演进中字段删除、字段保留和兼容性要注意什么？

protobuf 兼容性里最重要的一条是不要复用旧字段号。字段号是 wire format 的核心，删除字段后应该用 `reserved` 保留字段号和字段名，避免未来新字段误读旧数据。

新增字段通常是兼容的，老客户端会忽略未知字段，新客户端要给缺失字段设计默认行为。修改字段类型要小心，尤其是 wire type 不兼容的修改可能直接破坏解析。

在 AegisMesh 里，Registry、Telemetry、Policy 都是控制面协议。比如 `EndpointStatsSample` 未来新增网络指标可以加新字段，但不应该改已有字段号含义，也不应该复用删除字段。

### Q055【拓展】如何在 gRPC 中传递分布式 trace context？

gRPC 通常通过 metadata 传递 trace context。可以用标准的 W3C `traceparent`，也可以用系统内部约定的 metadata key。

AegisMesh 当前使用自定义 metadata：`x-aegis-trace-id`、`x-aegis-span-id`、`x-aegis-attempt`。frontend 如果收到上游 trace id，会放进 context；SDK interceptor 再把它写入 outgoing metadata，并同时写 JSONL trace。

生产系统里我会优先兼容 OpenTelemetry/W3C trace context。Aegis 自定义字段可以作为补充，用来记录 attempt、route version、upstream id 等治理信息。

### Q056【拓展】如果 gRPC 服务使用 TLS/mTLS，AegisMesh SDK 需要如何改造？

当前 SDK 默认使用 `insecure.NewCredentials()`，适合本地实验。支持 TLS/mTLS 时，要把 transport credentials 做成 DialOptions，而不是写死 insecure。

TLS 场景下，SDK 要支持 CA bundle、server name、证书校验和证书轮换。mTLS 还要加载客户端证书和私钥，控制面和数据面都要明确证书来源。

还要考虑服务身份。比如用 SPIFFE ID 或 Kubernetes ServiceAccount 表示 workload identity，Controller 下发实例信息时也要带身份信息，SDK 或 sidecar 校验“我连到的 endpoint 是否真的是这个 service”。

落到代码上，就是在 `DialServiceFromWithOptions` 里允许传入 `grpc.WithTransportCredentials(...)` 或 Aegis 自己的 TLS 配置，并分别处理连接 Controller 和连接业务 upstream 的证书策略。
