# 05. SDK Resolver、DialService 与客户端集成

## 简单

### Q113【简单】DialService 和 DialServiceFrom 的区别是什么？

`DialService` 是更简化的入口，调用方只传 `ctx`、Controller 地址和 service 名。它内部会把 source 写成 `"unknown"`，再转到 `DialServiceFrom`。

`DialServiceFrom` 多了一个 `source` 参数。这个 source 很重要，它会进入 telemetry 和 trace，比如 frontend 调 user-service 时，source 就应该是 `"frontend"`。这样 Controller 和 verifier 才能知道“是谁在调用谁”。

所以面试里可以这样说：两者都创建 AegisMesh 管理的 gRPC `ClientConn`，区别是 `DialServiceFrom` 会带调用方身份。做 demo 可以用 `DialService`，做真实链路观测和 verifier 时应该用 `DialServiceFrom` 或 `DialServiceFromWithOptions`。

项目里还有更底层的 `DialServiceFromWithOptions`。它可以显式控制 routing policy、retry mode、retry budget、trace log、是否关闭 telemetry、是否关闭 policy。这是实验和生产配置更常用的入口。

### Q114【简单】TargetForService 生成的 aegis scheme 目标长什么样？

`TargetForService("127.0.0.1:9000", "user-service")` 会生成：

```text
aegis://127.0.0.1:9000/user-service
```

这里 `aegis` 是自定义 scheme，`127.0.0.1:9000` 是 Controller 地址，path 里的 `user-service` 是逻辑服务名。

gRPC 看到这个 target 后，不会走普通 DNS resolver，而是走 AegisMesh 注册的自定义 resolver。resolver 会从 target 里解析出 Controller 地址和 service 名，再调用 `RegistryService.ListInstances` 拉实例列表。

### Q115【简单】SDK 为什么要注册自定义 resolver？

因为默认 gRPC 不知道 `aegis://...` 这种 target 该怎么解析。

普通 DNS resolver 只能把域名解析成 IP，最多解决“连到哪里”。AegisMesh 需要的不只是地址，还要拿到 instance id、endpoint status、slow_score 这些治理信息。它们都来自 Controller，不来自 DNS。

所以 SDK 在 `DialServiceFromWithOptions` 里调用 `registerDefaultResolver()`。这个函数通过 `resolver.Register(newRegistryResolverBuilder())` 注册 `aegis` scheme。之后业务代码只要 dial `aegis://controller/service`，gRPC 就会调用 Aegis resolver 去 Controller 拉服务发现结果。

这个设计的好处是业务 stub 仍然用原生 gRPC。业务代码不用自己先查 registry、再手工拼地址列表。

### Q116【简单】resolver 从 Controller 拉取实例后，如何转换成 gRPC 地址？

流程在 `instancesToAddresses` 里。

resolver 调 `ListInstances` 拿到一批 `ServiceInstance`。然后逐个检查：实例不能为空，address 不能为空，状态必须是可路由状态。通过检查后，SDK 会构造 `resolver.Address`。

一个地址大概长这样：

```go
resolver.Address{
    Addr:       inst.Address,
    ServerName: inst.Id,
    Attributes: addressAttributes(inst.Id, inst.Status, inst.SlowScore),
}
```

`Addr` 是真正连接的地址，比如 `172.18.0.5:7002`。`ServerName` 这里放实例 ID。`Attributes` 里带 AegisMesh 自己的元信息，比如 `instance_id`、`status`、`slow_score`。

转换完成后，resolver 调 `cc.UpdateState(resolver.State{Addresses: ...})` 把地址列表交给 gRPC。后面的 balancer 会基于这些地址建立 SubConn 并做 pick。

### Q117【简单】为什么 resolver 地址属性里要带 instance_id、status、slow_score？

因为 balancer 做决策时需要这些信息。

`instance_id` 用来稳定标识 endpoint。address 可能因为容器重启、端口映射、网络变化而改变，但 instance id 更接近“这个实例是谁”。如果缺失，项目会退化成用 address 当 endpoint ID。

`status` 告诉 balancer 这个 endpoint 当前是 `HEALTHY`、`DEGRADED` 还是 `PROBING`。同样是可路由，三种状态的成本不一样。`DEGRADED` 要降权，`PROBING` 只能拿少量探测流量。

`slow_score` 是 Controller 根据 telemetry 算出来的慢故障分数。adaptive P2C 会把它放进路由成本里。分数越高，被选中的概率越低。

把这些信息放进 `resolver.Address.Attributes` 比较自然，因为它跟着地址一起进入 gRPC balancer，不需要额外查全局 map。

### Q118【简单】哪些 endpoint 状态会被加入 resolver 地址列表？

当前实现会加入这几类：

```text
空状态、HEALTHY、DEGRADED、PROBING
```

空状态按默认健康处理，主要是为了兼容没有显式 status 的实例。

`HEALTHY` 是正常候选。`DEGRADED` 仍然可路由，但会被 adaptive P2C 降权。`PROBING` 会被放回地址列表，但不应该当普通健康实例使用，balancer 会通过 probe ratio 限制它的流量。

`EJECTED` 和 `DEAD` 不会进入 resolver 地址列表。也就是说，它们不会被普通业务请求选中。

### Q119【简单】为什么 EJECTED 或 DEAD 不应该作为普通候选地址？

因为这两个状态都表示“不能接正常流量”。

`EJECTED` 是慢故障状态机主动摘除的 endpoint。它可能还活着，但在最近窗口里表现太差，继续给它正常流量会拖高 p99，也可能把故障放大。

`DEAD` 更直接，表示实例不可用或不应被路由。

如果把它们继续放进普通候选列表，balancer 很难保证避开它们。尤其是 round-robin 这类策略，只要地址还在，就可能把请求打过去。AegisMesh 的做法是：`EJECTED/DEAD` 直接过滤；等 `EJECTED` 过了隔离时间进入 `PROBING`，再给少量探测流量。

### Q120【简单】SDK 默认使用什么负载均衡策略？

默认使用 `aegis_adaptive_p2c`。

在 `DefaultDialOptions()` 里，`RoutingPolicy` 默认是 `RoutingAdaptiveP2C`。`DialServiceFromWithOptions` 会根据这个 routing policy 生成 gRPC service config：

```json
{"loadBalancingConfig":[{"aegis_adaptive_p2c":{}}]}
```

这个 balancer 会在每次 RPC pick 时，从候选 endpoint 中做 adaptive P2C。它会参考本地 in-flight、本地 EWMA 延迟、Controller 下发的 status 和 slow_score。比普通 round-robin 更适合慢故障场景。

项目也支持 `round_robin`。实验里会用它做对照，比如单实例 delay 场景下比较 round-robin 和 adaptive P2C 的 p99。

### Q121【简单】DisableTelemetry 和 DisablePolicy 分别会关闭什么？

`DisableTelemetry` 会关闭 SDK 的 telemetry recorder 和 reporter。也就是说，客户端不会把 endpoint stats 定期上报给 Controller，Controller 也就拿不到这条客户端视角的延迟、错误、p95、in-flight 数据。

但它不会自动关闭 trace。trace 由 `TraceLogPath` 或 `AEGIS_TRACE_LOG` 控制。如果配置了 trace log，即使 telemetry 关闭，interceptor 仍然可以写 JSONL trace。

`DisablePolicy` 会关闭初始 `GetPolicy` 和后续 `WatchPolicy`。SDK 不再从 Controller 拉动态策略，而是使用本地 `DialOptions` 和默认值。比如默认 routing 还是 adaptive P2C，默认 retry 还是带 budget 的 2 次尝试。

这两个选项常用于实验对照。比如要测没有 telemetry 的行为，或者想固定 SDK 本地配置，不让 Controller policy 影响实验结果。

### Q122【简单】SDK 的 trace log 在项目中起什么作用？

trace log 是 verifier 的真实流量输入。

SDK 的 telemetry interceptor 会把每次 RPC attempt 写成 JSONL 记录。记录里有 `trace_id`、`span_id`、`source`、`destination`、`method`、`route`、`path`、`upstream`、`attempt`、`retry_attempts`、`status`。

它和 Prometheus 指标不一样。Prometheus 更适合看聚合趋势，比如 p99、slow_score、状态数量；trace log 更适合复盘每一次请求到底走了哪条路径、重试了几次、有没有访问 forbidden edge。

项目里的 verifier 会读取这些 trace，再检查 canary distribution、retry attempts、forbidden edges 等规则。这样 verifier 就不是只检查手写样例，而是能检查真实 SDK 产生的调用记录。

## 深度

### Q123【深度】resolver refresh interval 设置为 3 秒会带来什么收敛速度和控制面压力的取舍？

3 秒是一个折中。

间隔短，SDK 能更快看到 Controller 的服务发现和健康状态变化。比如 endpoint 从 `HEALTHY` 变成 `EJECTED`，resolver 下一轮刷新后就能把它从地址列表里移除。恢复时从 `EJECTED` 到 `PROBING`，客户端也能更快拿到探测地址。

但间隔越短，Controller 的 `ListInstances` 压力越高。假设有 1000 个客户端，每 3 秒拉一次，就是每秒 300 多次服务发现请求；如果每个请求还返回很多 endpoint，序列化和网络开销也会上来。

间隔长，Controller 压力低，但控制面状态传播慢。慢节点已经被 Controller 标记，SDK 可能还要等一个刷新周期才知道。AegisMesh 还有 telemetry 上报周期，默认是 5 秒，再加状态机连续窗口，所以整体故障收敛不是只由这 3 秒决定。

我会这样解释：3 秒适合项目实验和小规模 demo。规模变大后，应该做 `WatchInstances` 或 long-poll，用修订号推增量变化，而不是所有客户端固定轮询。

### Q124【深度】如果 ListInstances 返回空列表，gRPC balancer 会进入什么状态？

如果 `ListInstances` 成功返回空列表，resolver 会调用 `UpdateState`，把空 `Addresses` 交给 gRPC。

后面会出现两种情况。已有地址会被移除，新的 Ready SubConn 为空；adaptive picker 里没有 item 时，`Pick` 会返回 `balancer.ErrNoSubConnAvailable`。从业务调用看，最终通常会表现成没有可用后端，RPC 失败，常见状态是 `UNAVAILABLE`。

这和 `ListInstances` 调用失败不一样。调用失败时 resolver 只 `ReportError`，不会主动清空旧地址；返回空列表则表示 Controller 明确告诉 SDK“这个服务现在没有可用实例”。

所以空列表是一个很强的信号。生产里要谨慎区分“控制面失败”和“服务确实没有实例”。前者应该保留旧地址，后者可以让调用快速失败。

### Q125【深度】resolver.ReportError 和 UpdateState 的错误处理应该如何影响上层调用？

`ReportError` 是告诉 gRPC：resolver 当前解析失败了。它不等于清空地址，也不应该直接关闭已有业务连接。

AegisMesh 当前在 `ListInstances` 出错时调用 `r.cc.ReportError(err)`，然后返回。这样已有地址和 SubConn 还能继续服务，上层调用可能不受影响；如果客户端是冷启动、还没有任何地址，那么调用就可能因为没有 SubConn 而失败。

`UpdateState` 是正常更新地址列表。如果它返回错误，说明 gRPC 没接受这次 resolver state，SDK 也会 `ReportError`。这时最好不要假装更新成功，否则 balancer 和 resolver 对状态的理解会不一致。

面试里我会强调：resolver error 应该影响观测和报警，但不要轻易把数据面打断。控制面失败和业务后端失败要分开处理。

### Q126【深度】为什么 resolver 属性比全局 map 更适合传递 endpoint 状态？

因为 endpoint 状态本来就是地址的一部分。

resolver 从 Controller 拿到某个 address 时，同时拿到了它的 instance id、status、slow_score。把这些信息放在 `resolver.Address.Attributes` 里，gRPC 会把它和这个地址一起交给 balancer。balancer 构建 picker 时直接读取 attributes，不需要再查别的地方。

如果用全局 map，会有几个麻烦。第一，key 怎么选很容易出问题，用 address 会受 IP 漂移影响，用 instance id 又要处理 address 变化。第二，生命周期难管理，地址删了以后 map 里的旧状态可能还在。第三，多 service、多 ClientConn 并发更新时容易出现锁竞争和陈旧读。

attributes 的边界更清楚：这次 resolver update 里的每个地址自带自己的治理元信息。它更贴合 gRPC 的扩展模型。

### Q127【深度】如果 instance_id 缺失，项目如何选择 endpoint ID？这种 fallback 有什么隐患？

在 adaptive balancer 里，`newAdaptivePickerItem` 会先从 address attributes 读 `instance_id`。如果读不到，就退化成用 `address.Addr` 当 endpoint ID。

这样做的好处是系统还能跑。即使 Controller 没给 instance id，只要有地址，balancer 仍然能维护 in-flight 和 EWMA。

隐患是 address 不一定稳定。容器重启后 IP 可能变，端口也可能变；NAT 或代理后面也可能多个实例共享某个地址。address 被复用时，旧实例的 EWMA 或状态可能影响新实例。address 变化时，同一个真实实例又会被当成新 endpoint，历史观测丢掉。

所以 fallback 只适合兜底。真正可靠的做法是让 Registry 和 telemetry 都带稳定 instance id，最好再加 generation 或 lease revision，避免旧实例和新实例混在一起。

### Q128【深度】多个 ClientConn 指向同一服务时，本地 EWMA 和 retry budget 是否共享？利弊是什么？

当前实现里要分开看。

adaptive balancer 的本地 endpoint stats 存在包级 `sync.Map` 里，key 是 address。也就是说，同一进程内多个 `ClientConn` 如果连到同一个 address，会共享这份 in-flight/EWMA 统计。这个设计能让不同连接更快共享“这个地址最近变慢了”的本地经验。

retry budget 则不是这样。`DialServiceFromWithOptions` 每次都会创建新的 `dynamicRetrySource`，里面的 budget map 是按 method 存的，属于这次 dial 创建出来的连接上下文。多个 `ClientConn` 通常不会共享 retry budget。

利弊也很清楚。共享 EWMA 的好处是收敛快，坏处是不同调用方、不同方法的延迟特征可能互相影响。retry budget 不共享的好处是隔离性好，一个 ClientConn 的重试不会吃掉另一个的预算；坏处是全进程或全服务视角下，多个连接加起来仍可能放大重试。

如果要更严格控制 retry amplification，可以把 budget 提升到 source + destination + method 维度的共享对象，或者由 Controller 下发全局重试预算。

### Q129【深度】如果 policy watcher 更新 routing policy，已经创建的 ClientConn 能否切换 balancer？

当前实现里，不能完整热切 balancer。

原因是 routing policy 会在 `DialServiceFromWithOptions` 建连时转换成 gRPC service config，比如 `aegis_adaptive_p2c` 或 `round_robin`。这个默认 service config 是创建 `ClientConn` 时传进去的。

policy watcher 运行后，会把新的 `PolicySnapshot` 写进 `policyManager`。动态 retry source 每次 RPC 前都会读取最新 snapshot，所以 retry、method-level timeout、idempotency 这类策略可以动态生效。

但 balancer 不会因为 watcher 更新了 `RoutingPolicy` 就自动切换。要支持这个能力，需要让 resolver 或 policy watcher 触发新的 service config update，或者实现一个自定义 balancer，在内部按 policy 改选择逻辑。更简单的方式是重建 `ClientConn`。

所以我会如实讲：当前动态策略主要覆盖 retry/method policy；routing balancer 的热切换还不是完整实现。

### Q130【深度】客户端 SDK 的侵入性体现在哪里？业务代码需要改哪些地方？

侵入性主要在建连和上下文上，不在业务 proto 上。

业务代码不需要改 `.proto`，也不需要改生成的 gRPC stub。原来可能是：

```go
grpc.DialContext(ctx, "user-a:7001", ...)
```

接入 AegisMesh 后会改成：

```go
aegisgrpc.DialServiceFromWithOptions(ctx, "frontend", controllerAddr, "user-service", options)
```

也就是说，业务方要引入 AegisMesh SDK，使用 AegisMesh 的 dial 入口，传 Controller 地址、目标 service、source 名和可选策略。

如果要打通 verifier，还要在入口请求上生成或透传 trace id，比如 frontend 收到 HTTP 请求时放入 `ContextWithNewTraceID` 或 `ContextWithTraceID`。如果要配置实验，还会加 trace log path、retry mode、routing policy 这些参数。

所以它比 sidecar 更侵入业务代码，但比改业务 RPC 方法小很多。主要改的是客户端初始化和 context 传递。

### Q131【深度】如果业务方直接使用原生 grpc.Dial 绕过 SDK，会丢失哪些治理能力？

会丢掉 AegisMesh 的大部分数据面能力。

首先是服务发现。原生 `grpc.Dial` 不会解析 `aegis://controller/service`，也不会从 Controller 拉实例列表、status 和 slow_score。

然后是负载均衡。它不会使用 `aegis_adaptive_p2c`，也拿不到 endpoint status、PROBING probe ratio、本地 EWMA 和 slow_score 成本。

还会丢 telemetry 和 trace。Controller 收不到这个客户端的 endpoint stats，verifier 也拿不到真实 RPC trace。retry budget、method-level idempotency、per-try timeout、circuit breaker 这些 interceptor 逻辑也不会生效。

换句话说，原生 gRPC 仍然能发请求，但它只是普通 RPC 调用，不再是 AegisMesh 治理下的调用。

### Q132【深度】SDK goroutine 生命周期如何和外部 context 绑定，避免泄漏？

当前有三类后台 goroutine。

第一类是 telemetry reporter。`startReporter` 会启动 goroutine，内部 `reporter.Run(ctx)` 监听传入的 `ctx.Done()`。调用方 cancel context 后，它会退出，并关闭连接 Controller 的 `ClientConn`。

第二类是 policy watcher。`startPolicyWatcher` 也是用外部 ctx 控制。ctx 取消后，watch loop 会停止，watch stream 和连接也会退出。

第三类是 resolver 自己的 watch goroutine。resolver 在 `Build` 里创建内部 context，`Close()` 时 cancel，并关闭它连 Controller 的 `ClientConn`。gRPC 在 `ClientConn.Close()` 时会关闭 resolver，所以业务方必须记得关闭返回的 `ClientConn`。

这里有一个当前实现的边界：trace writer 是按 `TraceLogPath` 打开的，但 `DialServiceFromWithOptions` 只返回 `*grpc.ClientConn`，没有额外返回一个 SDK client wrapper 来统一关闭 trace writer。demo 里进程退出会释放文件句柄；如果做成长生命周期 SDK，我会封装成 `AegisClientConn`，提供 `Close()` 同时关闭 gRPC conn、cancel context、trace writer 和后台 goroutine。

实践上，业务方应该用可取消的 context 创建连接，并在服务退出时调用 `conn.Close()` 和 cancel。

## 拓展

### Q133【拓展】gRPC Java、Go、C++ 在 resolver/balancer 扩展机制上有什么差异？

大方向是一样的：都有“名字解析”和“负载均衡”两个扩展点，但暴露方式和工程体验不一样。

Go 这边比较直接。AegisMesh 用的就是 Go gRPC 的 `resolver.Builder`、`resolver.Resolver`、`balancer.Builder` 和 base balancer。resolver 把 target 解析成 `resolver.Address`，balancer 从 Ready SubConn 里 pick 一个。

Java 也有类似概念，通常是 `NameResolver`、`LoadBalancer`、`ClientInterceptor` 这类扩展点。Java 生态里客户端拦截器和 channel builder 很常用，适合做企业内部 SDK，但实现细节和 Go 不一样。

C++ 的扩展更贴近 gRPC core 和 channel 配置。它能做 resolver 和 LB policy，但对普通业务 SDK 来说门槛更高，和编译、插件注册、channel args 关系更紧。

所以跨语言迁移时，我不会强求每种语言代码结构一样，而是保持控制面协议一致：`RegistryService`、`TelemetryService`、`PolicyService`、trace schema 和 policy schema 一致。每种语言再用自己的 gRPC 扩展点实现数据面。

### Q134【拓展】如果语言 SDK 能力不一致，如何用 sidecar 统一治理逻辑？

可以把数据面能力从 SDK 移到 sidecar。

业务进程只需要把请求发到本地 sidecar，比如 `localhost:15001`。sidecar 负责服务发现、负载均衡、retry、circuit breaker、telemetry、trace 和 policy watch。Controller 继续下发 registry、health state、policy。

这样 Go、Java、Python、Rust 业务都不用分别实现完整 SDK。语言差异被 sidecar 屏蔽，治理能力集中在一个代理里。

代价也明显。sidecar 多一跳，部署和运维复杂度上升；如果要做方法级策略，sidecar 需要解析 gRPC HTTP/2 metadata，或者要求业务传明确的 method/trace metadata。SDK 能直接接触 context、method、deadline，sidecar 要拿到这些信息会麻烦一些。

我会把 sidecar 看成多语言规模化方案，把 Go SDK 看成低开销、强应用语义的方案。

### Q135【拓展】客户端负载均衡与服务端负载均衡分别如何影响连接数和热端点问题？

客户端负载均衡是每个客户端直接连接多个后端。好处是少一层代理，客户端能看到自己的 deadline、method、in-flight 和失败情况，决策更细。问题是连接数会变多：客户端数乘以后端数，规模大了后端连接压力明显。

服务端负载均衡通常是客户端连一个 LB 或代理，由代理转发到后端。好处是客户端连接简单，后端连接由代理集中管理。问题是代理可能变成瓶颈，应用层语义也可能丢掉。

热端点问题也不同。客户端 LB 如果算法不好，比如 pick_first 或连接复用不均，某些后端会变热。服务端 LB 如果代理的后端连接池或哈希策略不均，也会形成热端点。

AegisMesh 选择客户端 LB，是因为它要在 RPC 层利用 method、attempt、本地 EWMA、in-flight、slow_score 这些信息。代价是要控制连接数量，并处理客户端之间状态不完全一致的问题。

### Q136【拓展】如果连接池复用导致某些 SubConn 长时间热，P2C 是否足够？

P2C 有帮助，但不一定完全够。

P2C 每次 pick 会随机抽两个候选，再选成本更低的那个。AegisMesh 的成本里有 in-flight、EWMA、slow_score 和状态惩罚，所以热 SubConn 的 in-flight 上升后，被选中的概率会下降。

问题在于长连接或 streaming。一个 stream 选中某个 SubConn 后，负载不会被中途迁走。如果一些请求很长，P2C 只能影响后续新请求，不能重新平衡已经在跑的请求。

还有一种情况是采样不足。P2C 每次只看两个候选，实例很多时，短时间内仍可能不均匀。

更完整的方案会加几个东西：长连接计入更高权重的 in-flight；对 stream 做最大并发限制；给 SubConn 加连接级 bulkhead；必要时主动 drain 热连接；对 P2C 加 least-loaded fallback 或更大的 sample size。AegisMesh 当前的 adaptive P2C 适合 unary RPC 慢故障实验，streaming 场景还需要专门增强。

### Q137【拓展】如何为 SDK 设计向后兼容的配置模型？

我会坚持三个原则。

第一，默认值稳定。老业务升级 SDK 后，如果没有显式配置，行为不能突然改变。AegisMesh 当前 `DefaultDialOptions` 就给了默认 routing、retry 和 budget。

第二，配置要能分层。全局默认、service policy、method policy、调用方本地 override 要有明确优先级。比如 method policy 可以覆盖 service retry，非幂等方法默认关闭重试。

第三，新增字段要向后兼容。proto 可以新增字段，但不能复用字段号；YAML 配置新增字段时，老 SDK 忽略它，新 SDK 用默认值处理缺失字段。

具体到 SDK，可以给 `DialOptions` 加修订字段或 capability 上报。Controller 下发 policy 时，根据 SDK capability 决定是否下发新特性。这样新控制面不会把老 SDK 不认识的强制策略硬塞过去。

### Q138【拓展】如何让 SDK 在 Controller 暂时不可用时使用本地缓存策略？

可以缓存两类东西：服务发现结果和 policy snapshot。

服务发现方面，resolver 每次成功 `ListInstances` 后，把地址列表和修订写到本地内存，必要时也写磁盘。下一次 Controller 不可用时，不清空地址，继续用最后一次成功结果。AegisMesh 当前已经做了“不因 ReportError 清空旧地址”，但还没有 SDK 磁盘缓存。

policy 方面，`GetPolicy/WatchPolicy` 成功后保存最新 snapshot。Controller 不可用时，继续用旧 snapshot。当前 policy manager 在内存里就是这么做的；如果要跨进程重启保留，就要把 snapshot 写到本地文件。

缓存要有 TTL 或 stale 标记。太旧的地址不能无限使用，否则实例已经下线很久，客户端还在打旧地址。比较稳的做法是：短时间不可用时继续使用缓存；超过 stale TTL 后降级报警，并限制只用健康连接或 fallback 地址。

还可以在 trace 和 metrics 里暴露 `registry_cache_age`、`policy_cache_age`，让运维知道客户端是不是在用旧控制面数据。

### Q139【拓展】如何在 SDK 中加入 circuit breaking、rate limiting、timeout、bulkhead 的组合策略？

这些策略要分层，不要互相抢职责。

timeout 控制单次调用最多等多久，包括 overall timeout 和 per-try timeout。retry 要尊重 timeout，不能让多次 attempt 超出总预算。

rate limiting 控制进入 SDK 的请求速率，通常按 source、destination、method 做限流。它保护下游，也保护客户端自己。

bulkhead 控制并发隔离，比如每个 endpoint、每个 method、每个下游服务最多多少 in-flight。AegisMesh 当前的 circuit breaker 已经有 endpoint inflight limiter 的形态。

circuit breaker 根据错误率、超时率、in-flight 或连续失败快速失败。它应该在请求真正发出前判断，如果 endpoint 已经打开，就不要继续排队。

组合顺序可以是：先检查 overall context 和 rate limit，再做负载均衡选择 endpoint，然后做 endpoint bulkhead/circuit breaker，再发 RPC；失败后根据 method policy、retry budget 和剩余 deadline 决定是否重试。每一层都要把拒绝原因写进 telemetry，否则排查时会分不清是限流、熔断还是超时。

### Q140【拓展】如果需要给业务方暴露调试接口，你会在 SDK 提供哪些 introspection 信息？

我会提供一个只读调试视图，不让业务随便改内部状态。

服务发现方面，暴露当前 service 的 resolver target、最后一次成功刷新时间、地址列表、instance id、status、slow_score、labels，以及最近一次 resolver error。

负载均衡方面，暴露每个 endpoint 的 in-flight、本地 EWMA、最近 pick 次数、circuit breaker 状态、是否处于 PROBING、当前有效权重。

策略方面，暴露当前 policy revision、routing policy、retry mode、method-level retry、timeout、budget 剩余情况，以及 policy watcher 最近一次更新时间。

trace 和 telemetry 方面，暴露 trace log path、telemetry report interval、最近一次上报成功/失败时间、缓存样本数。

接口形式可以有几种：SDK 内部 debug HTTP endpoint、Prometheus metrics、日志 dump，或者一个 `DumpState()` 方法返回结构化 JSON。面试里我会强调一点：debug 接口必须脱敏，并且默认关闭或只绑定 localhost，不能把服务拓扑和策略暴露给不可信调用方。
