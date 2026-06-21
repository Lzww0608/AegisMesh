# 10. Circuit Breaker、限流与背压

## 简单

### Q253【简单】项目中的 circuit breaker 是按什么维度统计 in-flight？

当前项目里的 breaker 按 endpoint 字符串统计 in-flight。

代码在 `pkg/circuitbreaker/breaker.go` 里，内部结构是一个 `map[string]int64`：

```go
inflight map[string]int64
```

SDK 的 adaptive balancer 在 Pick 时选出 endpoint 后，会调用：

```go
release, err := p.breaker.Acquire(selected.Address)
```

所以这里的 key 不是 service name，也不是 method name，而是 `selected.Address`，也就是 resolver 下发给 gRPC 的后端地址，例如 `172.18.0.5:7002`。

这个维度的好处是很直观：某个具体实例已经堆了太多请求，就不要再把新请求发给它。坏处是它还没有按 method、caller、tenant 细分。如果一个 endpoint 上既有很轻的查询，也有很重的聚合请求，当前 breaker 会把它们都当成同一种 in-flight。

### Q254【简单】MaxInflightPerEndpoint 默认值是多少？

默认值是 128。

`NewBreaker` 里如果发现 `MaxInflightPerEndpoint <= 0`，会自动改成 128：

```go
if cfg.MaxInflightPerEndpoint <= 0 {
    cfg.MaxInflightPerEndpoint = 128
}
```

SDK 的默认 adaptive balancer 也创建了一个全局默认 breaker：

```go
defaultBreaker = circuitbreaker.NewBreaker(
    circuitbreaker.Config{MaxInflightPerEndpoint: 128},
)
```

实验 policy 里也写了这个值：

```yaml
circuit_breaker:
  max_inflight_per_endpoint: 128
```

面试里我会补一句：128 是项目 demo 的保守默认值，不是生产通用答案。真正上线要按 endpoint 的处理能力、请求耗时、QPS、方法成本来调。

### Q255【简单】Acquire 返回 release 函数的设计有什么好处？

这是典型的“拿令牌、还令牌”设计。

调用方只需要关心两件事：

1. 调用 `Acquire(endpoint)`，看能不能拿到本次请求的并发名额。
2. 请求结束后调用返回的 `release()`，把名额还回去。

好处是调用方不用再保存 endpoint，也不用直接操作 breaker 内部计数。breaker 把“如何释放这个 endpoint 的计数”封装进闭包里，外部只要在 RPC 完成时调用它。

在 AegisMesh 里，这个 release 函数被放进 `PickResult.Done` 里：

```go
Done: func(done balancer.DoneInfo) {
    item.stats.DecrementInflight()
    item.stats.ObserveLatency(time.Since(started))
    release()
}
```

这个位置很合适，因为 Done 发生在一次 RPC attempt 完成后，正好可以减少本地 in-flight、更新延迟 EWMA，再释放 breaker 名额。

### Q256【简单】ErrOpen 转换成什么 gRPC 状态码？

`ErrOpen` 会被转换成 `codes.ResourceExhausted`。

adaptive balancer 里是这样处理的：

```go
release, err := p.breaker.Acquire(selected.Address)
if err != nil {
    return balancer.PickResult{}, status.Error(codes.ResourceExhausted, err.Error())
}
```

也就是说，当某个 endpoint 的 in-flight 达到上限时，Pick 时直接失败，上层看到的是 `RESOURCE_EXHAUSTED`。

这个状态码比较贴切。它表达的不是“连接不可用”，而是“当前资源已经满了”。这和 `UNAVAILABLE` 不一样：`UNAVAILABLE` 更像后端不可达，`RESOURCE_EXHAUSTED` 更像容量保护触发。

### Q257【简单】为什么 release 要用 sync.Once？

`sync.Once` 是为了防止重复释放。

`Acquire` 成功后，breaker 会把 endpoint 的 in-flight 加 1。如果后面 `release()` 被调用两次，计数就可能减两次，导致统计变小，甚至让 breaker 误以为还有更多容量。

代码里把 release 包了一层：

```go
var once sync.Once
return func() {
    once.Do(func() {
        b.release(endpoint)
    })
}, nil
```

这样即使调用方因为异常路径、defer、回调重复调用了 release，真正的释放逻辑也只会执行一次。

严格说，gRPC 的 Done 正常只会调用一次。但工程代码里这种保护很值得加，因为 breaker 的计数一旦错了，后面所有限流判断都会跟着错。

### Q258【简单】如果 endpoint 字符串为空，为什么要 fallback 到 unknown？

这是为了避免空 key 进入统计逻辑，也方便排查。

`Acquire` 里有这段逻辑：

```go
if endpoint == "" {
    endpoint = "unknown"
}
```

如果不处理，空字符串也能作为 map key，但日志和调试时很难看出来它代表什么。统一写成 `unknown` 后，至少能明确看到“有请求没有拿到 endpoint 标识”。

还有一个效果：所有未知 endpoint 会共享同一个并发上限。这个选择偏保守。如果 endpoint 信息缺失，系统不会无限放行，而是把这些请求统一算到 `unknown` 下面，超过上限就拒绝。

更完整的做法是继续往前排查为什么 endpoint 为空。正常情况下，resolver 下发的地址应该有 `Addr`，adaptive balancer 用的也是这个地址。

### Q259【简单】inflight breaker 和 slow_score state machine 有什么区别？

它们解决的是不同时间尺度的问题。

inflight breaker 是本地、即时的保护。它只看一个 endpoint 当前有多少请求还没完成。超过上限，就在 Pick 时快速失败，不再把新请求发出去。

slow_score state machine 是控制面上的慢故障治理。SDK 上报 telemetry，Controller 根据 p95 延迟、错误、超时、in-flight、网络信号、absolute SLO 算 slow_score，再推动 endpoint 在 `HEALTHY/DEGRADED/EJECTED/PROBING` 之间迁移。

简单说：

- breaker 反应快，但信号少。
- state machine 信号更全，但需要窗口和上报周期。

它们是互补关系。breaker 防止单个客户端继续堆并发；slow_score 和状态机负责把多客户端观测汇总起来，影响后续路由和服务发现。

### Q260【简单】breaker 是保护客户端、服务端还是整个系统？

三者都会受益，但最直接保护的是客户端和被选中的 endpoint。

从客户端角度看，breaker 可以快速失败，避免本地 goroutine、连接、内存继续被慢请求占住。

从服务端角度看，它少收到一部分已经超过并发上限的请求，能减少排队和上下文切换。

从整个系统角度看，breaker 是背压的一种形式。它把“下游已经扛不住”这件事尽早反馈给上游，而不是把请求堆在网络、线程池、队列里。

但要说清楚：当前 AegisMesh breaker 是客户端本地保护，不是全局流控系统。多个进程各自有自己的 breaker，无法天然保证全局并发上限。

### Q261【简单】inflight 到达上限时应该立即失败还是排队？

AegisMesh 当前选择立即失败。

原因是 RPC 治理里，排队经常会把问题藏起来。请求虽然没有失败，但一直等在客户端队列里，用户看到的还是高延迟。队列越长，超时越多，重试越多，下游压力也更难判断。

立即失败的好处是明确：这个 endpoint 当前满了，调用方立刻拿到 `RESOURCE_EXHAUSTED`，上层可以降级、返回错误，或者在策略允许时选择其他 endpoint。

排队不是完全不能做，但必须是有边界的：

- 队列长度要有限。
- 排队时间要有 timeout。
- 要区分不同调用方或租户，避免一个流量源占满队列。
- 排队结果要进入 telemetry，否则排障时看不到。

当前项目为了简单和可解释，直接 fail fast。

### Q262【简单】breaker 和 retry budget 如何互相影响？

当前默认策略下，breaker 触发后不会继续重试。

原因是 breaker 返回的是 `RESOURCE_EXHAUSTED`，而 AegisMesh 默认只重试：

- `UNAVAILABLE`
- `DEADLINE_EXCEEDED`

所以一次请求如果在 Pick 时被 breaker 拒绝，retry interceptor 会看到 `RESOURCE_EXHAUSTED`，判断它不是默认可重试错误码，然后停止。

这点很重要。如果 breaker 拒绝后又被立刻重试，就可能出现一个坏循环：endpoint 已经满了，客户端还继续挑 endpoint、继续重试，最后把其他 endpoint 也打满。

如果未来要把 `RESOURCE_EXHAUSTED` 配成可重试，必须同时满足几个条件：有 retry budget、有 backoff+jitter、每次 retry 重新 pick endpoint，并且最好尊重服务端的 `Retry-After` 或 overload 信号。

## 深度

### Q263【深度】只用 inflight 上限作为 breaker，能否处理高错误率但低并发的故障？

不能完全处理。

只看 in-flight 的 breaker 适合处理“请求堆住了”的情况。比如某个 endpoint 变慢，请求迟迟不返回，in-flight 会升高，达到上限后 breaker 开始拒绝。

但有些故障不是这样。比如服务端很快返回错误，或者业务依赖失败导致大量 `INTERNAL`、`UNAVAILABLE`，每个请求都很快结束。这个时候 in-flight 可能一直不高，breaker 就不会打开。

所以当前 breaker 不是完整的 failure-rate circuit breaker。它更像 endpoint 维度的并发保护。

AegisMesh 里高错误率主要由 slow_score 和状态机处理。telemetry 会上报错误数、超时数，Controller 计算 error score，慢分数升高后 endpoint 会进入 `DEGRADED` 或 `EJECTED`。如果要增强本地 breaker，就应该加入这些字段：

- 最近窗口请求数。
- 失败率。
- 慢调用比例。
- 最小请求量。
- open / half-open / closed 状态。
- sleep window 和试探请求数。

这样才能处理“错误很多，但并发不高”的故障。

### Q264【深度】默认全局 breaker 在多个 ClientConn 之间共享，会有什么影响？

当前 SDK 里有一个包级变量：

```go
defaultBreaker = circuitbreaker.NewBreaker(
    circuitbreaker.Config{MaxInflightPerEndpoint: 128},
)
```

adaptive picker 默认都用这个 `defaultBreaker`。这意味着同一个进程里的多个 ClientConn，只要打到同一个 endpoint address，就会共享同一个 in-flight 上限。

这个设计有好处。它能防止一个进程里开多个 ClientConn 后绕过并发限制。比如业务代码不小心创建了多个 user-service 连接，最终打到 `172.18.0.5:7002` 的 in-flight 仍然会被统一统计。

也有副作用。不同业务模块、不同调用方、甚至不同逻辑服务如果复用了同一个 address，就会互相影响。一个高流量 ClientConn 把 endpoint 预算占满后，另一个低流量 ClientConn 也会收到 `RESOURCE_EXHAUSTED`。

面试里我会说：这是当前实现的简化方案。更细的方案应该让 breaker scope 可配置，比如：

- process-level：一个进程内共享。
- ClientConn-level：每条连接独立。
- service-level：按目标服务共享。
- method-level：不同方法分开。

默认用 process-level 是为了避免本地过载，但生产里要让 scope 明确可控。

### Q265【深度】如果 Done 回调没有执行，inflight 会泄漏吗？项目如何降低这种风险？

会有这个风险。

当前流程是：

1. Pick 选中 endpoint。
2. breaker `Acquire` 成功，in-flight 加 1。
3. 返回 `PickResult`，把 release 放进 Done 回调。
4. RPC 完成后，gRPC 调用 Done，SDK 减 in-flight 并释放 breaker token。

如果 Done 永远不执行，breaker 和本地 stats 的 in-flight 都不会释放。这个 endpoint 会慢慢被“假 in-flight”占满，最后一直返回 `RESOURCE_EXHAUSTED`。

正常情况下，gRPC 会在 attempt 结束时调用 Done，包括成功、失败、deadline、context cancel 这些路径。所以项目主要依赖 gRPC 的生命周期保证。

项目里还做了两点降低风险：

- release 用 `sync.Once`，避免重复释放。
- 本地 stats 的 `DecrementInflight` 做了下限保护，in-flight 已经是 0 时不会继续减成负数。

但这还不是完整防护。更强的实现可以给 breaker token 加 lease timeout，或者在 SDK 暴露 debug metric，发现某个 endpoint 的 in-flight 长时间不下降时报警。

### Q266【深度】ResourceExhausted 返回给重试逻辑时是否应该可重试？

默认不应该。

`RESOURCE_EXHAUSTED` 的语义是资源耗尽。它可能来自客户端 breaker，也可能来自服务端限流。如果收到这个错误后立刻重试，很可能只是把压力转移到另一个 endpoint，或者在同一个服务上继续制造排队。

AegisMesh 当前默认可重试 code 只有 `UNAVAILABLE` 和 `DEADLINE_EXCEEDED`，没有把 `RESOURCE_EXHAUSTED` 放进去。我认为这个默认是合理的。

也有少数场景可以重试 `RESOURCE_EXHAUSTED`，但条件要严格：

- 方法必须幂等。
- 必须有 retry budget。
- 必须有 backoff 和 jitter。
- 每次重试要重新 pick endpoint。
- 如果服务端返回 retry-after，要尊重它。
- 最好限制只重试一次。

否则 breaker 本来是保护阀，最后会被 retry 逻辑绕过去。

### Q267【深度】breaker 上限应该静态配置还是按 endpoint capacity 动态计算？

项目当前是静态上限，默认 128。Policy proto 和 YAML 里也有 `max_inflight_per_endpoint` 字段，但当前 SDK 的 adaptive balancer 还没有把这个字段完整热更新到 breaker 实例上。

静态配置的好处是简单。面试讲解、实验复现、单机 demo 都很稳定。问题是不同 endpoint 的真实能力可能差很多：

- 大实例和小实例不该用同一个并发上限。
- CPU 密集方法和轻量查询不该用同一个上限。
- 故障期间 capacity 会变化，静态值反应不过来。

更好的做法是动态计算：

```text
endpointLimit = baseLimit * endpointWeight * capacityFactor
```

其中 `endpointWeight` 可以来自 registry 或 policy，`capacityFactor` 可以来自服务端负载、历史延迟、错误率、CPU、队列长度。

我会保留静态配置作为兜底，再逐步加动态调节。这样系统启动时有明确默认值，运行时又能根据 endpoint 状态调整。

### Q268【深度】如何让 breaker 支持 half-open 探测？

要把当前的“并发计数器”升级成真正的状态机。

每个 endpoint 至少要有三种状态：

- `CLOSED`：正常放行，请求失败率和慢调用比例在阈值以内。
- `OPEN`：熔断打开，普通请求直接失败，不再打到这个 endpoint。
- `HALF_OPEN`：过了 sleep window 后，放少量试探请求进去。

迁移逻辑可以这样设计：

1. `CLOSED` 状态下，如果最近窗口失败率、慢调用比例或 in-flight 持续超过阈值，进入 `OPEN`。
2. `OPEN` 保持一段 `sleepWindow`，避免刚熔断就马上恢复。
3. 时间到了进入 `HALF_OPEN`，只允许 N 个 trial request。
4. trial request 成功率够高，回到 `CLOSED`。
5. trial request 失败或超时，再回到 `OPEN`。

AegisMesh 已经有 endpoint 状态机里的 `PROBING`，它和 half-open 很像。区别是当前 `PROBING` 是 Controller 侧 endpoint health state，影响路由权重；breaker half-open 是客户端本地保护状态，影响是否放行请求。

如果要融合，我会让 `PROBING` 控制“路由层给多少探测流量”，让本地 breaker half-open 控制“单个客户端最多放几个试探请求”。

### Q269【深度】如果慢请求堆积导致 inflight 升高，breaker 和 P2C 哪个先起作用？

通常是 P2C 先起作用，breaker 后兜底。

adaptive P2C 每次 Pick 前都会读取 endpoint 当前 in-flight，并把它放进 routing cost：

```text
inflightCost = inflight / effectiveWeight
```

所以某个 endpoint 的慢请求开始堆积时，它的 in-flight 会先升高，cost 变大，P2C 会倾向于选其他 endpoint。

如果流量继续上升，或者所有 endpoint 都很慢，P2C 可能仍然会选到已经很忙的 endpoint。这时 breaker 的硬上限开始起作用：一旦 in-flight 达到 128，`Acquire` 失败，Pick 返回 `RESOURCE_EXHAUSTED`。

所以两者关系是：

- P2C 是软避让。
- breaker 是硬保护。

软避让尽量减少拒绝，硬保护保证不会无限堆并发。

### Q270【深度】按 endpoint 限制 inflight 和按 service 限制 inflight 各有什么作用？

按 endpoint 限制，保护的是单个实例。比如 user-service 有 A、B 两个实例，B 变慢了，B 的 in-flight 很快升高。endpoint breaker 可以只限制 B，不影响 A。

按 service 限制，保护的是整个下游服务。比如 user-service 所有实例都慢了，如果只做 endpoint 限制，每个实例都可以接 128 个 in-flight，总量可能还是太高。service-level limit 可以控制“这个客户端对 user-service 总共最多发多少并发”。

两者最好组合：

```text
service limit >= sum(endpoint limit 的一部分)
endpoint limit 保护单实例
service limit 保护整个依赖
```

再细一点，还可以加 method-level limit。比如 `GetUser` 很轻，可以给高一点；`SearchUserHistory` 很重，就要低一点。

AegisMesh 当前实现的是 endpoint-level。它适合慢实例隔离，但还不是完整的 bulkhead 体系。

### Q271【深度】如果下游服务自身也有限流，客户端 breaker 应如何配合？

客户端 breaker 应该做前置保护，下游服务端限流是最后防线。

比较理想的配合方式是：

- 客户端根据本地 in-flight 和历史延迟，尽量不要把请求打到已经很忙的 endpoint。
- 服务端如果发现自己过载，返回明确的 `RESOURCE_EXHAUSTED`，最好带 retry-after 或 overload metadata。
- 客户端收到服务端限流后，不要立刻重试；要降低该 endpoint 权重，或者按 backoff 等待。
- telemetry 把服务端限流次数上报给 Controller，Controller 可以把它纳入 slow_score 或 capacity 调整。

如果客户端和服务端都各自限流，但互相不知道，就容易出现误判。客户端以为服务端挂了，疯狂切流；服务端以为客户端恶意打流，继续拒绝。比较好的方向是把 overload 信号标准化，让客户端知道这是“容量不足”，不是普通错误。

### Q272【深度】如何避免 breaker 在瞬时流量峰值下误伤正常请求？

第一，要把上限设得有依据。可以用 Little's Law 粗算：`inflight ≈ throughput * latency`。如果一个 endpoint 正常处理 1000 RPS，平均耗时 80ms，正常 in-flight 大概是 80。上限设 128 就有一些余量；如果正常 in-flight 就经常到 120，那 128 会太紧。

第二，可以引入短时 burst 机制。比如允许短时间超过软上限，但不能超过硬上限。软上限触发降权，硬上限才拒绝。

第三，可以加很短的有界队列。不是无限排队，而是最多排几十毫秒。如果很快有请求完成，就放行；超过排队时间就失败。

第四，按 method 加权。重请求计作多个 in-flight，轻请求计作 1 个。这样不会因为轻请求的瞬时峰值过早挡住所有流量，也不会让重请求低估资源占用。

第五，结合 retry budget。被 breaker 拒绝的请求不要无限重试，否则误伤会被放大。

当前 AegisMesh 用的是简单硬上限，适合 demo 和故障实验。生产里我会把软硬阈值、短队列、method cost 和 adaptive concurrency 加进去。

## 拓展

### Q273【拓展】Bulkhead isolation 和 circuit breaker 的区别是什么？

Bulkhead isolation 是资源隔离。它的核心问题是：一个依赖、一个租户、一个方法出问题时，不能把整个客户端的资源都吃完。

比如给 user-service 最多 100 个并发，给 order-service 最多 50 个并发。user-service 慢了，也不能占满所有 goroutine 和连接，让 order-service 跟着受影响。

Circuit breaker 更偏故障检测和快速失败。它看到某个依赖最近错误率高、慢调用多、并发打满，就打开熔断，后续请求直接失败或只放探测请求。

AegisMesh 当前这个 endpoint in-flight breaker，其实更接近 bulkhead/concurrency limiter，而不是完整的 Hystrix 式 circuit breaker。它按 endpoint 分隔并发，并在达到上限时拒绝请求，但还没有错误率窗口、open/half-open/closed 状态。

面试时可以这样说：当前实现是 breaker 的并发保护部分，后续可以扩展成完整状态机式熔断。

### Q274【拓展】漏桶、令牌桶、并发数限制分别适合哪些保护目标？

漏桶适合平滑流量。它按固定速度出水，请求进来后排队，队列满了就丢。它能把突发流量变得更平稳，但会增加排队延迟。

令牌桶适合“允许一定突发，但限制长期平均速率”。桶里有令牌，请求拿到令牌才能通过。平时流量低时令牌会积累，突发时可以快速消耗。很多 API rate limit 用这个模型。

并发数限制限制的是同时在处理的请求数量。它不直接限制每秒多少请求，而是限制系统里正在运行的请求数。它特别适合保护线程池、连接池、内存和下游处理能力。

AegisMesh 当前 breaker 用的是并发数限制。原因是 fail-slow 的核心现象之一就是请求不返回，in-flight 堆积。只看 RPS 可能看不出来，但 in-flight 会很快升高。

### Q275【拓展】服务端 overload control 和客户端 adaptive routing 如何协同？

服务端最清楚自己的真实负载，客户端最适合决定下一跳。两边要配合，不能各做各的。

服务端可以输出这些信号：

- 当前是否 overload。
- 建议的 retry-after。
- 当前 queue length。
- CPU、内存、线程池、连接池使用率。
- 请求被拒绝的原因。

客户端拿到这些信号后，可以做几件事：

- 降低该 endpoint 的 effective weight。
- 暂停对它的普通流量，只保留探测。
- 对 `RESOURCE_EXHAUSTED` 不立即重试。
- 把 overload 事件上报给 Controller，参与 slow_score。

AegisMesh 目前主要靠客户端观测和 Controller 汇总。如果继续增强，我会让 demo service 或真实服务端也上报 overload signal，这样客户端就不用只从延迟和错误里反推服务端状态。

### Q276【拓展】排队论中 Little's Law 如何解释 inflight 上限？

Little's Law 是：

```text
L = λ * W
```

其中：

- `L` 是系统中的平均并发数，可以近似看成 in-flight。
- `λ` 是吞吐量，比如每秒完成多少请求。
- `W` 是平均响应时间。

这条公式很好解释 fail-slow。假设一个 endpoint 每秒处理 1000 个请求，平均延迟 50ms，那么平均 in-flight 大概是：

```text
1000 * 0.05 = 50
```

如果服务变慢，平均延迟变成 200ms，吞吐还没立刻下降，那么 in-flight 会变成：

```text
1000 * 0.2 = 200
```

也就是说，延迟变高会自然推高 in-flight。breaker 设置 in-flight 上限，本质是在限制系统里最多堆多少未完成请求，防止排队继续放大。

这也是 AegisMesh 同时看 latency 和 in-flight 的原因。latency 告诉你请求变慢了，in-flight 告诉你慢请求已经堆了多少。

### Q277【拓展】如果引入 adaptive concurrency limit，可以用哪些反馈信号？

可以用几类信号。

第一类是延迟信号。比如平均延迟、p95、p99、排队延迟、EWMA latency。延迟开始明显高于基线时，说明 endpoint 可能接近饱和。

第二类是错误和拒绝信号。比如 timeout、`RESOURCE_EXHAUSTED`、`UNAVAILABLE`、服务端 overload response。错误多了，limit 应该收缩。

第三类是并发和吞吐信号。比如当前 in-flight、完成 RPS、成功 RPS。如果增加并发后吞吐不再上升，只是延迟变高，就说明已经过了甜点区。

第四类是资源信号。比如 CPU、内存、线程池队列、连接池等待数、GC pause。服务端愿意上报的话，这些信号比客户端猜测更准。

第五类是网络信号。AegisMesh 已经有 eBPF TCP retransmit、connect error、connect latency，这些可以帮助判断是网络层拥塞，还是应用层处理慢。

真正做 adaptive concurrency 时，不能把这些信号简单相加就完事。要有平滑、上下限、冷启动、快速下降和慢速恢复机制，否则 limit 本身也会抖。

### Q278【拓展】Netflix Concurrency Limits 的思想如何应用到 AegisMesh？

Netflix Concurrency Limits 的核心思路是：不要预先假设一个固定并发上限，而是根据延迟反馈动态调整。

大致可以理解成这样：

- 系统健康时，慢慢增加并发上限，试探还能不能接更多请求。
- 一旦发现延迟相对最小延迟明显升高，说明开始排队，就降低上限。
- 降低要快，恢复要慢，避免过载扩大。

放到 AegisMesh 里，可以按 endpoint 做 adaptive limit：

```text
endpointLimit = adaptiveLimit(endpoint)
```

每次 `PickResult.Done` 都能拿到本次 attempt 的耗时，正好可以作为反馈样本。SDK 已经在 Done 里更新 EWMA 和释放 breaker token，所以这里是接入 adaptive concurrency 的自然位置。

和现有 slow_score 的关系也很清楚：

- adaptive concurrency 是本地快速保护。
- slow_score 是控制面全局判断。

本地 limit 可以先收缩并发，避免继续排队；Controller 看到多客户端 telemetry 后，再决定是否把 endpoint 标记为 `DEGRADED` 或 `EJECTED`。

### Q279【拓展】如何把 CPU、内存、队列长度纳入 breaker 判断？

需要先让这些信号进入控制面或数据面。

一种做法是服务端直接上报负载：

```text
cpu_usage
memory_usage
worker_queue_depth
thread_pool_active
connection_pool_waiters
gc_pause
```

Controller 收到后，把它们和 latency、error、in-flight 一起合成 endpoint health。然后 resolver 把 capacity 或 overload 状态下发给 SDK，SDK 用它调整 breaker 上限和 routing weight。

另一种做法是客户端从响应 metadata 里拿服务端负载。比如服务端每次返回当前 queue depth 或 overload level。客户端可以更快做本地调整，不必等 Controller 下发。

还可以从 eBPF 或 cgroup 层拿一些系统信号，比如容器 CPU throttle、TCP retransmit、connect error。但 CPU、队列长度这类应用内部指标，最好还是服务端主动暴露。

我的设计倾向是：服务端指标不直接决定熔断，而是先进入一个平滑后的 overload score。这样不会因为 CPU 瞬间到 90% 就立刻全拒绝。breaker 可以根据 overload score 逐步降低并发上限。

### Q280【拓展】如果要做公平限流，按用户、租户、方法还是调用方维度切分？

要看系统最想保护什么。

按用户切分，适合面向终端用户的系统。某个用户请求异常多，不应该影响其他用户。

按租户切分，适合 SaaS。一个大客户或者异常租户不能打满整个服务。租户级限流通常还要配额度、套餐和优先级。

按方法切分，适合 RPC 治理。不同方法成本差异很大，`GetUser` 和 `GenerateReport` 不应该共享同一个简单 QPS 上限。

按调用方切分，适合微服务内部。比如 recommendation-service 调 user-service 的流量异常，不应该影响 checkout-service 调 user-service。

真正的平台通常要做分层限流：

```text
service global limit
-> caller limit
-> tenant/user limit
-> method limit
-> endpoint concurrency limit
```

AegisMesh 当前已经有 source、destination、method、upstream 这些 telemetry 维度。继续扩展公平限流时，可以把 tenant 或 user 放进 metadata，再让 SDK 和 Controller 按这些维度统计预算和并发。

我不会一开始就把所有维度都做满。更实际的路径是先做 caller + method，因为这两个维度和 RPC 治理最贴近；再根据业务场景加入 tenant 或 user。
