# 09. Retry、Retry Budget 与超时控制

## 简单

### Q225【简单】为什么重试会导致放大效应？

重试的问题在于，它会把一个用户请求变成多个下游请求。正常情况下，一个 checkout 请求只调用一次 user-service；如果第一次失败后又重试一次，下游看到的请求量就翻倍了。

这在服务健康时影响不大，但在下游已经慢了、线程池已经满了、网络已经抖动的时候，重试会继续往故障点打流量。故障越严重，客户端越容易超时；客户端越超时，重试越多；重试越多，下游排队越长。这就是 retry amplification。

在调用链里这个问题更明显。假设每一层都最多尝试 2 次，frontend 到 A，A 到 B，B 到 C，三层叠起来，最坏情况下 C 可能收到 2 * 2 * 2 = 8 次请求。用户只发了 1 次请求，但最下游要扛 8 次压力。

AegisMesh 做 retry budget，就是为了把重试从“失败就继续打”改成“只能在预算内重试”。这样重试仍然能处理小抖动，但不会在故障时把系统压垮。

### Q226【简单】项目默认最大尝试次数是多少？

项目默认 `MaxAttempts=2`。这里的 `MaxAttempts` 指总尝试次数，不是额外重试次数。

所以默认行为是：

- 第 1 次是原始请求。
- 第 2 次是最多允许的一次重试。

如果 `RetryOff` 生效，SDK 会把 `MaxAttempts` 设成 1，也就是只发原始请求，不做重试。

这个默认值比较保守。项目的目标不是用大量重试硬顶故障，而是用一次有限重试覆盖短暂抖动，再配合 retry budget、adaptive P2C 和状态机把慢端点降权或摘除。

### Q227【简单】哪些 gRPC code 被认为可重试？

默认可重试的 gRPC code 是两个：

- `UNAVAILABLE`
- `DEADLINE_EXCEEDED`

`UNAVAILABLE` 通常表示连接不可用、后端暂时不可达、resolver 或 balancer 没有可用连接。这类错误往往是瞬时的，重试有机会成功。

`DEADLINE_EXCEEDED` 表示本次调用超过 deadline 或 per-try timeout。它也可能是瞬时慢请求，所以项目允许重试。但这个 code 要小心，特别是写请求。因为服务端可能已经执行成功，只是响应回来得太晚；客户端重试后，可能造成重复写入。

所以项目后面引入了 method-level policy。像 `GetUser` 这类读接口可以开启重试；像 `CreateOrder` 这类有副作用的接口，默认应该禁用重试，或者使用 idempotency key。

### Q228【简单】retry budget 的基本公式是什么？

AegisMesh 的预算公式是：

```text
allowedRetries = max(floor(originalRequests * budgetRatio), minBudget)
```

默认配置是：

- `budgetRatio = 0.15`
- `minBudget = 10`
- `window = 10s`

含义是，在一个 10 秒窗口里，允许的重试次数最多是原始请求数的 15%，但至少给一个最小预算。

举个例子，如果 10 秒内有 1000 个原始请求，`floor(1000 * 0.15) = 150`，那最多允许 150 次重试。下游看到的总请求量大约是 1150，而不是没有预算时的 2000。

项目实验里也验证了这一点：`without_budget` 的重试放大约是 2.0x，`with_budget` 被控制在约 1.15x。这组数据很适合在面试里讲。

### Q229【简单】MinBudget 的作用是什么？

`MinBudget` 是给低流量服务留一点重试空间。

如果只用比例预算，低流量服务很容易没有预算。例如 10 秒内只有 5 个原始请求，`floor(5 * 0.15)=0`，这意味着一个重试都不允许。可是低流量服务也可能遇到一次短暂网络抖动，如果完全不能重试，用户体验会很差。

`MinBudget` 解决的就是这个问题。它允许系统在请求量很低时也保留少量重试能力。

但它也有风险。低流量下 `MinBudget` 可能让实际重试比例偏高。例如 5 个原始请求，`minBudget=10`，理论预算比原始请求还多。项目默认 `MaxAttempts=2`，所以每个请求最多只会多发一次，实际放大不会无限增长，但低流量场景仍然需要按服务调小 `MinBudget`。

### Q230【简单】retry budget 的窗口为什么默认是 10 秒？

10 秒是一个折中。

窗口太短，预算会频繁重置。故障刚发生时，很多客户端可能每隔一两秒就拿到一批新预算，重试流量会变得很抖。

窗口太长，预算恢复又太慢。一个短暂故障过去后，之前消耗掉的预算还占着窗口，后续正常的小抖动可能没有重试空间。

项目默认 10 秒，是为了和 telemetry 上报、实验负载窗口、状态机判断窗口处在同一个量级。它不是生产环境的唯一答案。真正上线时，应该按服务 QPS、RT、调用链长度和故障恢复目标来调。

### Q231【简单】RecordOriginal、AllowRetry、RecordRetry 的调用顺序为什么重要？

顺序是：

1. 原始请求开始前，调用 `RecordOriginal()`。
2. 请求失败后，先用 `AllowRetry()` 判断还有没有预算。
3. 如果允许重试，再调用 `RecordRetry()`，然后发起下一次尝试。

这个顺序不能乱。

`RecordOriginal()` 要先执行，因为预算是按原始请求数算出来的。如果不先记录原始请求，当前请求就不会贡献预算，低流量场景可能被错误拒绝。

`AllowRetry()` 要在真正重试前执行，否则预算就失去限流作用。

`RecordRetry()` 要在预算允许后立刻执行，避免多个并发请求都看到“还有预算”，然后一起放行，导致实际重试次数超过预算。

项目里的 retry interceptor 就是按这个顺序做的：先记录原始请求，每次额外尝试前检查预算，通过后再记录 retry。

### Q232【简单】RetryOff、RetryWithoutBudget、RetryWithBudget 有什么区别？

`RetryOff` 是关闭重试。SDK 会把 `MaxAttempts` 设成 1，请求只发一次。这适合非幂等写接口，比如创建订单、扣款、发券。

`RetryWithoutBudget` 是允许重试，但不检查预算。它仍然受 `MaxAttempts` 和可重试错误码约束，但只要错误满足条件就会继续尝试。项目里保留这个模式，主要是为了做实验对比，证明没有预算时会出现明显的重试放大。

`RetryWithBudget` 是默认模式。它同时受三层限制：

- `MaxAttempts`
- retryable gRPC code
- retry budget

这个模式比较接近工程上能接受的默认策略。它允许小规模自愈，但不让重试在故障时失控。

### Q233【简单】PerTryTimeout 解决什么问题？

`PerTryTimeout` 限制的是单次尝试的耗时。

如果没有 per-try timeout，一个请求可能在第一次尝试里卡住很久，直到整体 deadline 用完。这样就算后面还有重试机会，也已经没有时间了。

项目的做法是，每次 attempt 都用 `context.WithTimeout(parentCtx, PerTryTimeout)` 创建一个尝试级 context。默认 `PerTryTimeout=750ms`。如果一次尝试超过这个时间，就结束这次尝试，再根据错误码和预算决定是否重试。

这里要注意，per-try timeout 不等于 overall timeout。overall timeout 应该由调用方的 parent context 控制。比较合理的组合是：整体 deadline 限制用户请求的总耗时，per-try timeout 限制每一次下游调用不要拖太久。

### Q234【简单】为什么非幂等方法要禁用或限制重试？

非幂等方法的风险是重复执行。

比如 `CreateOrder`。客户端第一次调用时，服务端可能已经把订单写入数据库，只是响应在网络上丢了，或者客户端等超时了。客户端看到的是失败，但服务端其实已经成功。如果这时自动重试，就可能创建第二笔订单。

所以项目里把 method-level policy 加进来了。一个方法如果标记为非幂等，并且没有显式 retry policy，SDK 会默认关掉重试。

工程上还有另一种做法：给非幂等请求加 idempotency key。比如 `x-aegis-idempotency-key` 或业务侧的 order token。服务端用这个 key 做去重，同一个 key 重复提交时返回同一份结果，而不是再执行一次写入。

## 深度

### Q235【深度】如果所有请求都失败，without_budget 和 with_budget 的下游压力有什么不同？

如果所有请求都失败，`without_budget` 会把重试压力完整打到下游。

项目默认 `MaxAttempts=2`。假设有 1000 个原始请求，且全部遇到可重试错误：

- `without_budget`：每个请求都重试一次，下游总请求约 2000 次，放大 2.0x。
- `with_budget`：默认预算比例 0.15，最多允许 150 次重试，下游总请求约 1150 次，放大 1.15x。

这就是 retry budget 的价值。它不会假装故障不存在，也不会让每个请求都“努力一下”。当系统已经失败时，它会主动减少额外压力，把重试留给有限的、有机会成功的请求。

项目实验结果和这个推导一致：`without_budget` 的 median retries 是 1000，median total 是 2000，amplification 是 2.0x；`with_budget` 的 median retries 是 150，median total 是 1150，amplification 是 1.15x。

### Q236【深度】retry budget 按 ClientConn 维度统计，有什么优点和缺点？

项目里的 retry budget 是 SDK 本地维护的。更准确地说，它跟 `DialServiceFromWithOptions` 创建出来的连接和策略源绑定。动态策略场景下，预算还会按 method 和 policy revision 做区分。

优点很直接：

- 不依赖 Controller 每次参与请求。
- 本地加锁和计数，开销低。
- Controller 短暂不可用时，客户端仍然可以按已有策略工作。
- 不同方法可以有自己的预算，读接口和写接口可以分开治理。

缺点也很清楚：它不是全局预算。

如果一个服务有 100 个客户端进程，每个进程都有自己的 `MinBudget`，那么全局最小预算可能被放大 100 倍。比例部分通常会随流量一起扩展，问题没那么大；真正需要警惕的是 `MinBudget`、窗口不同步和大量客户端同时失败。

生产上如果要严格控制全局重试量，可以把预算做成控制面下发的服务级配额，或者在网关、sidecar、服务端 admission 层再加一层全局保护。AegisMesh 当前选择本地预算，是为了低开销和可运行性。

### Q237【深度】如果多个实例或多个进程各自维护预算，全局 retry amplification 是否仍然受控？

只能说“局部受控”，不能说“全局严格受控”。

每个客户端本地看，重试量会被自己的预算限制住。但多个客户端并不知道彼此花了多少预算。如果它们同时遇到故障，就会各自放出一部分重试流量。

举个简单例子，100 个客户端进程，每个进程 `minBudget=10`。哪怕每个进程原始流量都很低，整个系统一个窗口内也可能有 1000 次最小重试预算。

要把全局放大控制住，可以有几种办法：

- 降低大规模部署下的 `MinBudget`。
- 按服务 QPS 自动调整预算，而不是写死默认值。
- 让 Controller 下发全局 retry quota，客户端本地只消费分片后的额度。
- 在下游服务端加 admission control 或 rate limit，防止被上游重试打穿。
- 把 retry budget context 沿调用链传递，避免每一层都重新生成预算。

AegisMesh 当前实现适合说明“客户端级 retry storm 抑制”。如果面试官追问全局严格控制，我会直接说：当前还不是全局配额系统，后续可以通过控制面令牌桶或 sidecar 统一预算来增强。

### Q238【深度】MinBudget 在低流量服务中可能导致什么比例上的放大？

`MinBudget` 在低流量下可能让实际重试比例远高于 `budgetRatio`。

假设一个服务 10 秒内只有 5 个原始请求，`budgetRatio=0.15`，比例预算是 `floor(5 * 0.15)=0`。如果 `minBudget=10`，允许预算就变成 10。

不过项目还有 `MaxAttempts=2` 这一层限制。每个请求最多只会多发一次，所以 5 个原始请求最多产生 5 次重试，总请求最多 10 次，实际放大 2.0x，而不是 3.0x 或更高。

这说明 `MinBudget` 的风险要和 `MaxAttempts` 一起看。`MinBudget` 解决低流量服务完全不能重试的问题，但它不适合无脑设很大。生产里我会按服务类型配置：

- 低 QPS、强一致写接口：`RetryOff` 或很低的 `MinBudget`。
- 低 QPS、只读接口：可以保留少量 `MinBudget`。
- 高 QPS 服务：主要依靠比例预算，`MinBudget` 可以设得很小。

### Q239【深度】使用 floor(originalRequests * ratio) 会造成什么边界行为？

`floor` 会让预算呈阶梯变化。

以 `ratio=0.15` 为例：

- 1 到 6 个原始请求，比例预算都是 0。
- 7 到 13 个原始请求，比例预算是 1。
- 14 到 19 个原始请求，比例预算是 2。

好处是保守。不会因为算出 0.9 次重试，就提前放行 1 次。坏处是低流量时预算很容易为 0，所以项目又引入了 `MinBudget`。

代码里还有一个很小的 `1e-9`，是为了避免浮点误差。例如数学上应该是 15.0 的结果，浮点计算可能变成 14.999999999，直接 `floor` 会少给一次预算。

这类边界在实验里一般不明显，但面试时可以讲出来，说明你知道比例预算不是连续函数，低流量服务要单独调参。

### Q240【深度】如果 policy 策略更新导致 budget 重建，会不会短时间重置预算？

会。

项目的动态策略里，retry budget 是按 method 和 policy revision 管理的。如果 policy snapshot 的 revision 变了，SDK 会为这个 method 创建新的 budget。这样做的好处是策略变更能立刻生效，比如预算比例从 0.15 改成 0.05 后，不会继续沿用旧窗口里的计数。

但它也有一个副作用：预算计数会被清空。假设故障正在发生，控制面频繁推送新修订，客户端可能在短时间内多次拿到新的 `MinBudget`，造成一小段额外重试流量。

更稳的做法是区分“兼容变更”和“强制重建”：

- 如果只是修改 routing policy，可以保留 retry budget 计数。
- 如果 retry ratio、window、minBudget 变化，可以迁移一部分计数，而不是完全清零。
- 控制面可以限制 policy 发布频率，避免故障期间反复刷新修订。

当前实现选择了简单清晰的修订隔离，适合本地实验和代码讲解。生产环境里，我会补预算迁移或策略发布节流。

### Q241【深度】per-try timeout 小于服务正常 p99 时会发生什么？

会把正常慢请求误判成失败。

比如某个接口正常 p99 是 300ms，你把 `PerTryTimeout` 设成 100ms。大量请求会在 100ms 被客户端取消，然后进入重试逻辑。结果是：

- 客户端看到更多 `DEADLINE_EXCEEDED`。
- retry budget 被快速消耗。
- 下游收到更多重复请求。
- telemetry 里的超时数增加，slow_score 可能升高。
- 状态机可能把健康实例误判成 DEGRADED。

这不是在治理慢故障，而是在制造慢故障。

合理设置一般要参考方法级 SLO。读接口可以让 per-try timeout 略低于 overall deadline，给重试留一点时间；写接口更保守，很多时候直接禁用 retry，只设置整体 deadline。

### Q242【深度】重试应该发生在负载均衡选择前还是选择后？每次重试是否应重新选 endpoint？

在客户端 SDK 里，重试通常包在一次逻辑 RPC 外层。每次 attempt 都重新调用 gRPC invoker，balancer 会重新执行 pick。也就是说，重试发生在逻辑调用层，endpoint 选择发生在每次尝试里。

这样做的好处是，第一次请求如果打到了慢实例，第二次有机会被 adaptive P2C 选到另一个更健康的实例。对 fail-slow 场景，这很有价值。

但不是所有场景都应该重新选：

- 读请求：重新选 endpoint 通常合理。
- 非幂等写请求：最好不重试，或者必须带 idempotency key。
- 有 session 亲和性的请求：可能需要固定到同一个 endpoint。
- streaming RPC：不能简单按 unary retry 的方式处理。

AegisMesh 当前主要治理 unary RPC。每次 attempt 会带 `x-aegis-attempt` 一类的 trace 信息，方便后面 verifier 判断某次逻辑请求到底尝试了几次、打到了哪些 upstream。

### Q243【深度】如果第一次请求已经在服务端执行成功但响应丢失，重试会造成什么一致性问题？

这就是非幂等重试最典型的问题。

客户端看到的是失败：可能是 `DEADLINE_EXCEEDED`，也可能是连接断开。可服务端那边可能已经执行完业务逻辑，比如订单写入成功、库存扣减成功、支付请求已经发出。客户端再发一次请求，服务端如果没有去重，就会重复执行。

结果可能是：

- 创建两笔订单。
- 扣两次库存。
- 发两张券。
- 调用第三方支付两次。

解决思路不是“永远不重试”，而是让重试有语义基础：

- 客户端生成 idempotency key。
- 服务端用唯一索引或去重表记录这个 key。
- 同一个 key 重复提交时，返回第一次执行结果。
- 对跨系统写入，用事务消息或 outbox/inbox 模式保证最终一致。

所以在 AegisMesh 里，我会明确区分读方法和写方法。读方法可以默认重试，写方法默认不重试，除非业务实现了幂等。

### Q244【深度】如何防止 retry storm 和 cascading failure？

要从几层一起做。

第一层是重试本身要受限。包括 `MaxAttempts`、retryable code、retry budget、per-method idempotency policy。AegisMesh 已经实现了这部分。

第二层是时间控制。要有 overall deadline，也要有 per-try timeout。不能让上游无限等，也不能让每次 attempt 卡住太久。

第三层是路由避让。adaptive P2C 会结合 inflight、EWMA latency、slow_score 和 endpoint state，尽量把流量从慢端点上移开。

第四层是本地保护。circuit breaker 和 inflight limiter 可以让客户端在 endpoint 明显不可用时快速失败，避免继续堆积请求。

第五层是节奏控制。生产里还应该加指数退避和 jitter，避免大量客户端在同一时间点同时重试。

最后是调用链治理。不要让每一层都无脑重试。上游传递 deadline，下游尊重 deadline；如果链路很长，就要明确哪一层负责重试，其他层只做快速失败。

## 拓展

### Q245【拓展】retry、hedging、timeout、circuit breaker、rate limit 应该如何排序？

我会按一次请求的生命周期来讲。

请求进入客户端后，先看整体 deadline 和 method policy。非幂等方法如果没有幂等保证，直接不走 retry。

然后做 rate limit 或 admission control，先判断这个客户端现在能不能发请求。接着进入负载均衡，选择一个 endpoint。选中 endpoint 后，本地 circuit breaker 或 bulkhead 判断这个 endpoint 是否还能接请求，比如 inflight 是否超过阈值。

真正发起一次 attempt 时，套上 per-try timeout。attempt 返回后，telemetry 记录状态、延迟和 endpoint。失败时再判断错误码是否可重试、是否还有 retry budget、是否超过最大尝试次数。如果都满足，等待 backoff+jitter 后重新 pick endpoint，再发下一次 attempt。

hedging 和 retry 不太一样。retry 是失败后再发，hedging 是请求还没失败时，为了降低尾延迟提前发第二份请求。hedging 对下游压力更大，所以它更需要预算和并发上限。AegisMesh 当前做的是 retry，不是 hedging。

### Q246【拓展】Google SRE 中 retry budget 的思想和 error budget 有什么关系？

两者思路很像，都是把“可靠性动作”变成有边界的预算。

error budget 是 SLO 允许的错误量。比如可用性目标是 99.9%，那剩下 0.1% 就是可以消耗的错误预算。它用来平衡发布速度和稳定性。

retry budget 是允许客户端制造的额外请求量。比如设置 15%，意思是每 100 个原始请求，最多额外发 15 个重试请求。它用来平衡自愈能力和系统压力。

二者的共同点是：不要无限追求“成功”。如果系统已经不稳定，继续加压只会让故障变大。预算用完后，就应该停止重试、快速失败、让上层降级，或者把流量切走。

### Q247【拓展】幂等性 token、去重表、事务消息如何降低重试副作用？

幂等性 token 的做法是，客户端为一次业务操作生成一个唯一 key。比如创建订单时生成 `order_request_id`。服务端第一次收到这个 key，执行业务并记录结果；后面再收到同一个 key，就直接返回之前的结果。

去重表是服务端实现幂等的常见方式。表里有一列唯一 key，插入时依赖数据库唯一约束防止重复执行。它比只在内存里缓存可靠，因为服务重启后仍然能识别重复请求。

事务消息通常用于跨系统场景。比如订单服务写库后，需要发消息给库存服务。直接“写库 + 发消息”容易出现一边成功一边失败。outbox 模式会先把业务数据和待发送消息写在同一个本地事务里，再由后台任务投递消息。消费端再用 inbox 或去重表保证重复消息不会重复扣库存。

这些机制的目的都是让“重试同一个操作”变成安全行为。RPC 层只能决定要不要重试，真正的业务一致性还要靠服务端设计。

### Q248【拓展】指数退避和 jitter 为什么重要？AegisMesh 当前是否需要引入？

指数退避是让每次重试之间的等待时间逐渐变长。比如第一次等 20ms，第二次等 50ms，第三次等 100ms。它的作用是给下游一点恢复时间，而不是失败后立刻继续打。

jitter 是在等待时间上加随机扰动。没有 jitter 时，很多客户端可能在同一时间失败，然后在同一时间重试，形成整齐的流量尖峰。加了 jitter 后，重试会分散开。

AegisMesh 当前 retry interceptor 是立即重试，只受 `MaxAttempts`、错误码和 retry budget 限制。项目默认最大尝试次数只有 2，所以风险比多次重试小；但如果要往生产方向走，我会加入 method-level backoff policy：

```yaml
retry:
  max_attempts: 2
  budget_ratio: 0.15
  backoff:
    base_ms: 20
    max_ms: 200
    jitter: true
```

这样 retry budget 控制总量，backoff+jitter 控制节奏。

### Q249【拓展】客户端重试和服务端重试哪种更容易导致放大？

两种都会放大，风险点不同。

客户端重试的特点是靠近用户请求。它知道整体 deadline，也能重新走负载均衡，避开慢 endpoint。缺点是如果很多客户端同时失败，就会一起重试，下游压力很大。

服务端重试的特点是隐藏在服务内部。上游只看到一次调用，但服务端可能对下游发了多次请求。它的好处是服务端更了解自己的依赖和业务语义；风险是调用链上每一层都偷偷重试，最下游会被乘法放大打穿。

我更倾向于让重试责任明确。一个边界上只能有一层主导重试，其他层要尊重传下来的 deadline 和 retry budget。否则 frontend、service A、service B 都重试，最后没人知道流量为什么突然放大。

### Q250【拓展】微服务链路中每一层都重试会产生什么乘法效应？

如果每一层最多尝试 2 次，4 层调用链最下游最多会收到：

```text
2 * 2 * 2 * 2 = 16
```

也就是说，一个用户请求，最坏情况下会变成 16 次最下游调用。

如果每层最多尝试 3 次，3 层就是：

```text
3 * 3 * 3 = 27
```

这就是很多线上故障里最危险的地方。表面上每个服务都只是“重试一次”，看起来很克制；但链路叠起来后，下游看到的是指数级压力。

解决办法是把 retry policy 做成链路级设计：

- 上游传递整体 deadline。
- 每层不要重新创建很长的 timeout。
- 明确只有某一层负责重试。
- retry budget 可以随请求传播，而不是每层重新生成。
- 对非幂等写链路，默认禁用自动重试。

### Q251【拓展】如何为不同错误码、不同方法配置不同重试策略？

方法级策略是必须的，因为不同 RPC 的语义不同。

比如：

```yaml
services:
  user-service:
    methods:
      /demo.shop.v1.UserService/GetUser:
        idempotent: true
        timeout_ms: 150
        retry:
          enabled: true
          max_attempts: 2
          budget_ratio: 0.1

  order-service:
    methods:
      /demo.shop.v1.OrderService/CreateOrder:
        idempotent: false
        retry:
          enabled: false
```

AegisMesh 当前已经支持 PolicyService 和 method-level policy，可以配置方法是否幂等、是否启用 retry、最大尝试次数、per-try timeout、预算比例、最小预算和窗口。

错误码这块，SDK 内部 `RetryPolicy` 有 `RetryableCodes` 字段，默认是 `UNAVAILABLE` 和 `DEADLINE_EXCEEDED`。当前动态 YAML policy 主要覆盖 retry 开关、次数、预算和 timeout，没有把错误码列表完整暴露成配置项。如果要继续增强，我会把 retryable codes 加到 PolicyService 的 proto 里，让不同方法可以配置不同错误码：

- 查询接口可以重试 `UNAVAILABLE` 和短超时。
- 写接口只允许在连接根本没发出去时重试，或者直接不重试。
- `INVALID_ARGUMENT`、`PERMISSION_DENIED` 这类业务错误不应该重试。
- `RESOURCE_EXHAUSTED` 是否重试要看服务端是否返回 `Retry-After` 或 backoff 建议。

这样策略才不会只按“失败”二字粗暴处理。

### Q252【拓展】在队列系统和 RPC 系统里，重试语义有什么不同？

RPC 重试是同步的。用户请求还在等结果，客户端要在有限 deadline 内决定是否再试一次。它关注的是尾延迟、用户可见错误率、下游瞬时压力和业务副作用。

队列重试是异步的。消息处理失败后，可以延迟投递、修改可见时间、进入死信队列。用户通常不在原地等待这次处理结果。它关注的是最终处理成功、重复消费、消息顺序和 poison message。

两者都要处理幂等，但方式不同：

- RPC 里，幂等 key 通常来自一次用户操作。
- 队列里，幂等 key 往往来自 message id、业务 id 或 outbox event id。

两者的失败处理也不同。RPC 里预算用来限制短时间内的额外请求；队列里重试次数、延迟策略和死信队列用来防止坏消息被无限消费。

所以不能把队列系统的“反正以后再试”直接搬到 RPC。RPC 的时间窗口很短，调用链也更容易出现乘法放大。
