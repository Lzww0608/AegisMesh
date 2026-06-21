# 06. Adaptive P2C 负载均衡与路由代价函数

## 简单

### Q141【简单】P2C 是什么？为什么“两选一”比纯随机更好？

P2C 是 Power of Two Choices，意思是每次请求不在所有节点里完整排序，而是随机挑两个候选 endpoint，然后选 cost 更低的那个。

它比纯随机好，是因为纯随机完全不看当前负载。一个 endpoint 已经很忙，纯随机仍然可能继续打过去。P2C 只多看一个候选，就能显著降低请求集中到单个节点的概率。

它也比每次全量 least-loaded 更轻。全量 least-loaded 要扫描所有 endpoint，实例多时开销更高，还容易因为所有客户端同时看到同一个“最空节点”而一起打过去。P2C 只采样两个 endpoint，开销是常数级，同时能接近 least-loaded 的效果。

在 AegisMesh 里，P2C 不是只看请求数，而是看综合 cost：inflight、本地 latency EWMA、Controller 下发的 slow_score 和 endpoint 状态都会影响选择。

### Q142【简单】AegisMesh 的 adaptive P2C 和 gRPC round_robin 有什么区别？

`round_robin` 基本按顺序轮流选后端。只要地址在列表里，它就会比较平均地分流。它的优点是简单、稳定；缺点是不了解哪个 endpoint 当前更慢、更忙。

AegisMesh 的 adaptive P2C 会在每次 pick 时重新看 endpoint 的状态。它会把 Ready SubConn 转成 `routing.Endpoint`，读入本地 in-flight、本地 EWMA、resolver attributes 里的 status 和 slow_score，然后随机抽两个 endpoint，选 cost 更低的。

所以在慢故障场景下，两者差别很明显。round-robin 仍然会持续把一部分请求打到慢节点；adaptive P2C 会把慢节点的 cost 提高，让它被选中的概率下降。项目实验里，单实例 delay 场景下 round-robin 的 median p99 是 348.682 ms，adaptive P2C 降到 32.712 ms。

一句话：round-robin 是“平均分”，adaptive P2C 是“边观察边避让”。

### Q143【简单】路由 cost 中为什么要考虑 inflight？

inflight 表示这个 endpoint 当前还有多少请求没完成。它是负载的即时信号。

只看延迟会有滞后。请求还没完成时，latency 样本还没产生，但这个 endpoint 可能已经堆了很多并发。如果继续把请求打过去，排队会越来越长。

AegisMesh 的 cost 里有：

```text
inflightCost = inflight / effectiveWeight
```

也就是说，同样的 inflight，权重大一点的实例可以承受更多请求；权重小或 slow_score 高的实例，inflight 会更快推高 cost。

这样做可以让路由对“正在变忙”的 endpoint 更敏感，不必等到它已经慢出明显延迟后才避让。

### Q144【简单】路由 cost 中为什么要考虑 latency EWMA？

latency EWMA 是本地客户端观察到的近期延迟趋势。它比单次延迟更平滑，又比长期平均更敏感。

如果某个 endpoint 最近几次调用明显变慢，它的 EWMA 会升高。AegisMesh 会把它转成：

```text
latencyCost = latencyEWMA.Seconds() * latencyPenalty
```

这样即使 in-flight 还不高，只要这个客户端实际感受到它变慢，balancer 也会减少选择它。

EWMA 是本地信号，反应快。它不用等 SDK 上报 telemetry、Controller 计算 slow_score、resolver 再刷新回来。慢故障刚出现时，本地 EWMA 可以先做第一层避让。

### Q145【简单】slow_score 为什么会影响 effective_weight？

slow_score 是 Controller 根据 telemetry 算出来的慢故障分数。它越高，说明这个 endpoint 越可疑。

AegisMesh 的 effective weight 是这样算的：

```text
effectiveWeight = weight / (1 + slow_score)
```

如果一个实例原始 weight 是 1，slow_score 是 0，那 effective weight 还是 1。如果 slow_score 变成 3，effective weight 就会变成 0.25。后面 inflight cost 又会除以 effective weight，所以同样的 inflight 会产生更高 cost。

这个设计的含义是：slow_score 不只是额外加一个惩罚，它还会降低实例的有效承载能力。慢节点不一定马上被摘除，但会更难被选中。

### Q146【简单】DEGRADED endpoint 是否完全不接流量？

不是。

`DEGRADED` 表示 endpoint 有退化迹象，但还没有被完全摘除。resolver 会把它加入地址列表，adaptive P2C 也会把它作为可路由候选。

不过它不会和 `HEALTHY` 一样被看待。AegisMesh 的 cost 里，如果 endpoint 状态是 `DEGRADED`，会额外加 1 的 slow cost。同时它通常还会带更高的 slow_score，effective weight 也会下降。

所以 `DEGRADED` 的行为是“少接流量”，不是“完全不接流量”。这样可以避免因为短暂波动就把容量一下子砍掉，也能继续收集样本判断它是否恢复或继续恶化。

### Q147【简单】PROBING endpoint 为什么只接少量探测流量？

`PROBING` 是从 `EJECTED` 恢复前的试探状态。它不是健康状态。

如果一个 endpoint 被摘除一段时间后直接回到 `HEALTHY`，流量会马上打回去。万一它只是短暂恢复，p99 会再次升高，状态也会来回震荡。

AegisMesh 的做法是：resolver 会把 `PROBING` 放回地址列表，但 adaptive P2C 在有正常 endpoint 时，只按 probe ratio 给它少量流量。默认 probe ratio 是 2%。

这点在实验里也验证过。PROBING 流量实测保持在很低比例，用来判断 endpoint 是否真的恢复，而不是让它马上承接正常流量。

### Q148【简单】circuit breaker 在 Pick 时起什么作用？

它在真正返回 SubConn 之前做本地保护。

adaptive P2C 先选出一个 endpoint，然后 SDK 会调用 breaker 的 `Acquire(selected.Address)`。如果这个 endpoint 当前 inflight 已经超过上限，`Acquire` 会返回错误，Pick 直接失败，gRPC 上层会看到 `RESOURCE_EXHAUSTED`。

当前 breaker 是 endpoint inflight limiter，默认每个 endpoint 最多 128 个 inflight。它还不是完整的 CLOSED/OPEN/HALF_OPEN 状态机式熔断器，但已经能防止某个 endpoint 被单个客户端打出过高并发。

成功 Acquire 后，Pick 会增加本地 inflight；RPC 完成时 `Done` 会释放 breaker token。

### Q149【简单】PickResult.Done 为什么适合更新本地 latency 和 inflight？

因为 `Done` 正好发生在一次 RPC attempt 完成之后。

Pick 时知道请求被发给了哪个 SubConn，也能记录开始时间。RPC 完成后，gRPC 会调用 `PickResult.Done`。这时 SDK 能算出本次 attempt 的耗时，也知道该把刚才增加的 inflight 减掉。

AegisMesh 在 `Done` 里做三件事：减少 endpoint inflight，更新本地 latency EWMA，释放 circuit breaker token。

这比在拦截器里猜 endpoint 更直接。拦截器适合记录业务 method、status、trace 和 upstream；balancer 的 Done 更适合更新“这个 SubConn 本地负载如何”。

### Q150【简单】adaptive P2C 的随机源为什么需要可替换？

主要是为了测试和可复现。

P2C 每次要随机选两个候选。如果随机源写死，单测就很难稳定验证“当候选是 A 和 B 时，应该选 cost 更低的 A”。AegisMesh 把随机源抽象成 `RandomSource`，测试里可以传 `sequenceRandom`，让它按固定序列返回。

这样可以写出确定性测试，比如固定抽中 fast 和 slow，验证 picker 会选 fast；固定 probe 抽样值，验证 PROBING 是否按配置比例进入候选。

生产运行时，如果没有传随机源，就使用基于当前时间种子的 `rand.New(...)`。测试和生产各用合适的随机来源，代码会更可靠。

## 深度

### Q151【深度】P2C 如何在低开销下逼近 least-loaded？它在哪些场景会失效？

P2C 的关键是“少量采样带来足够好的负载信息”。每次只看两个 endpoint，不扫描全量列表，但只要随机抽样足够频繁，繁忙 endpoint 被抽中时就会输给更空的 endpoint。长期看，请求会自然向低 cost endpoint 倾斜。

它的开销是常数级。无论后端有 10 个还是 1000 个，每次 pick 都只比较两个候选。这点很适合客户端负载均衡，因为每个客户端都要频繁 pick，不能每次都扫描全部 endpoint。

它会在几个场景里变差。第一，QPS 太低，采样次数不足，随机性会很明显。第二，候选 endpoint 很少，比如只有两个，那 P2C 就退化成每次比较固定小集合。第三，请求耗时差异极大，只用请求数 inflight 不足以表示真实负载。第四，长连接或 streaming 占用 SubConn 很久，P2C 只能影响新请求，不能迁移已经在跑的请求。

还有一种典型问题：所有客户端同时看到同一个 endpoint cost 很低，可能短时间一起打过去。P2C 比全量 least-loaded 好一些，因为每个客户端采样不同，但不是完全免疫。工程上还要加 EWMA、inflight、jitter、连接预热和限流。

### Q152【深度】如果两个候选 endpoint 的 slow_score 都很高，LeastBadFallback 会带来什么行为？

在 AegisMesh 当前实现里，`DEGRADED` 仍然属于可路由状态，`EJECTED/DEAD` 才会被过滤。所以如果候选 endpoint 都是 `DEGRADED`，picker 不会直接失败，而是继续比较 cost，选那个相对没那么差的 endpoint。

这就是“least bad”的行为：没有好选择时，选损害最小的那个。比如一个 endpoint inflight 10、EWMA 300ms、slow_score 2.0；另一个 inflight 1、EWMA 100ms、slow_score 1.2，P2C 会选后者。

这里要说明一个实现细节：代码里的 `LeastBadFallback` 配置当前不是一个复杂的独立分支，主要行为来自 `DEGRADED` 被保留在候选集合里，以及 cost 函数对它加惩罚。真正没有候选时，比如全部是 `EJECTED` 或 `DEAD`，picker 仍然会返回 `ErrNoEndpoint`。

这个设计比“只要不是 HEALTHY 就全部拒绝”更柔和。它适合容量紧张时继续提供服务，但会牺牲一部分延迟。生产里通常还要配合 timeout、circuit breaker 和限流，避免所有 endpoint 都很差时继续无限打流量。

### Q153【深度】inflight / effective_weight 的设计如何避免大权重实例被过早降载？

关键在这个公式：

```text
inflightCost = inflight / effectiveWeight
```

如果一个实例权重更大，说明它理论上能承接更多请求。比如 weight 是 4，另一个实例 weight 是 1，那么同样 4 个 inflight，前者的 inflight cost 是 1，后者是 4。这样大权重实例不会因为“绝对 inflight 数更高”就被误认为更忙。

slow_score 会进一步修正这个承载能力：

```text
effectiveWeight = weight / (1 + slow_score)
```

这表示权重大不等于永远优先。如果大实例开始变慢，slow_score 上升，它的 effective weight 会下降，inflight cost 会被放大。

所以这个设计同时表达了两层含义：静态容量用 weight 表示，动态退化用 slow_score 折减。正常情况下，大权重实例可以吃更多流量；退化时，即使它原始权重大，也会逐步降载。

### Q154【深度】latency EWMA 使用本地观测，slow_score 使用 Controller 观测，二者如何互补？

本地 EWMA 反应快，但视角窄。它只代表当前客户端到某个 endpoint 的近期体验。比如某个客户端和 endpoint 之间网络路径变差，本地 EWMA 能很快升高，但其他客户端可能没有这个问题。

slow_score 反应慢一点，但视角更全。它来自多个 SDK 上报到 Controller 的 telemetry，能综合 p95、错误、超时、inflight、网络信号和 absolute SLO。它适合判断 endpoint 是否真的进入 `DEGRADED/EJECTED/PROBING`。

AegisMesh 把两者都放进 cost。EWMA 负责快速局部避让，slow_score 负责跨客户端的慢故障判断和状态机控制。

如果两者冲突，我会这样理解：本地 EWMA 是“我现在感受到它慢不慢”，slow_score 是“控制面认为它整体健康不健康”。路由时应该同时看。本地很慢就少选它；全局 slow_score 高，也要少选它，即使当前客户端暂时还没观察到明显慢。

### Q155【深度】如果某个 endpoint 暂时没有历史 EWMA，它的初始 cost 应该如何设定？

当前实现里，如果 endpoint 还没有历史 EWMA，`LatencyEWMA()` 会返回 0。这样 latencyCost 也是 0，新 endpoint 在延迟维度上看起来很便宜。

这个做法简单，但有冷启动风险。新 endpoint 没有历史样本，不代表它真的快。如果它刚加入时直接拿到大量流量，可能还没预热就被打满。

更稳的做法有几种。可以用服务级平均 EWMA 或默认延迟作为初始值；也可以给新 endpoint 一个 warmup 权重，随着成功请求逐步放大；还可以把无历史样本的 endpoint 先当作 PROBING，拿少量流量后再进入正常候选。

在 AegisMesh 当前项目里，实验主要是稳定实例上的慢故障，所以 zero EWMA 没有造成明显问题。但如果要上生产，我会给新 endpoint 增加 warmup 和最小样本量。

### Q156【深度】PROBING 2% 的默认比例如何选择？过高或过低分别有什么风险？

2% 是一个保守默认值，不是数学上唯一正确的值。它表达的是：恢复探测需要真实流量，但不能把正常流量一下子打回刚被摘除的 endpoint。

过高的风险是恢复误判。一旦 endpoint 只是短暂变好，过高 probe ratio 会让它重新承接明显流量，p99 可能再次上升，甚至把状态打回 `EJECTED`。这会造成路由震荡。

过低的风险是恢复太慢。endpoint 已经恢复了，但拿不到足够样本，Controller 很难判断成功率和 latency 是否达标。低 QPS 服务尤其明显，2% 可能长时间采不到几个请求。

当前实现用 `Intn(100)` 做探测抽样，所以比例粒度大约是 1%。默认 2% 对高 QPS demo 合适；生产里应该按服务 QPS 和恢复目标调整。低 QPS 可以用“每个窗口至少 N 个 probe”的策略，高 QPS 可以继续用比例控制。

### Q157【深度】在低 QPS 服务中，P2C 的随机性和采样不足会导致什么问题？

低 QPS 下，P2C 的统计优势不容易发挥出来。

请求少，随机抽样次数也少。某个慢 endpoint 可能很久才被抽到，也可能连续几次被抽到，流量分布会显得很跳。EWMA 样本也少，一两个慢请求就可能把本地延迟估计拉高，或者反过来，慢故障出现后很久都没有足够样本发现。

PROBING 也会受影响。默认 2% 在低 QPS 下可能几分钟都没有几个 probe，请求量不足时状态机恢复判断会慢。

解决办法不是简单把 P2C 改掉，而是加最小样本量和窗口保护。比如低 QPS 服务可以降低状态迁移敏感度，probe 用固定最小请求数，EWMA 加更慢的衰减，或者按时间窗口聚合更多样本。必要时也可以用主动健康探测补充真实流量样本。

### Q158【深度】在长尾请求很多的服务里，用完成后的 latency 更新 EWMA 是否会滞后？

会滞后。

EWMA 只有在请求完成后才更新。如果一个请求已经卡住 5 秒，在它完成之前，本地 EWMA 还不知道这次请求很慢。此时能提前反映压力的是 inflight，因为请求没完成会一直占着 inflight。

所以长尾服务里，只靠完成后的 latency 不够。AegisMesh 同时使用 inflight，就是为了在慢请求还没结束时先看到并发堆积。

更进一步，可以引入 request age。比如某个 endpoint 上有很多已经运行超过 p95 预算的请求，即使它们还没完成，也应该提高 cost。也可以把 timeout、deadline exceeded、排队时间和服务端队列长度纳入 telemetry。

当前实现对 unary RPC 已经够用，但长尾特别重或 streaming 很多的服务，需要更细的“运行中请求年龄”信号。

### Q159【深度】如果请求大小差异很大，只按请求数 inflight 衡量负载是否足够？

不够。

inflight 请求数默认每个请求权重一样。但真实业务里，一个请求可能只是查用户头像，另一个请求可能做复杂聚合。它们都算 1 个 inflight，会低估重请求的成本。

如果请求大小差异很大，应该引入 weighted inflight。权重可以来自请求类型、method、payload size、预估 CPU 成本、历史平均耗时，甚至业务方显式传入的 cost hint。

比如 `Search` 请求比 `GetUser` 重很多，就可以让 `Search` 计作 5 个 inflight。这样 endpoint 承接重请求时，路由成本会更快升高。

AegisMesh 当前按请求数计算 inflight，适合 demo 和同质 unary 请求。要支持复杂生产流量，我会把 method-level cost 加进 policy，让 SDK 按方法或 metadata 给 inflight 加权。

### Q160【深度】如果 endpoint 被 ejected 后本地 SubConn 仍然存在，resolver 和 balancer 如何协同收敛？

Controller 把 endpoint 标成 `EJECTED` 后，下一次 `ListInstances` 会带回这个状态。AegisMesh 的 resolver 在转换地址时会过滤 `EJECTED`，所以新的 resolver state 不再包含这个 address。

gRPC balancer 收到新的地址列表后，会基于剩余地址构建新的 Ready SubConn 集合和 picker。旧的 SubConn 可能不会马上物理断开，但它不再出现在新的 picker 里，也就不会承接新的普通请求。

已经发出去的请求不会被强行中断。它们会正常完成，`PickResult.Done` 会减少 inflight、更新 EWMA、释放 breaker token。这点很重要，否则摘除动作会破坏已经在执行的 RPC。

后面如果状态机把 endpoint 从 `EJECTED` 转成 `PROBING`，resolver 会重新把它放回地址列表。balancer 再按 probe ratio 给少量探测流量。这样 resolver 负责候选集合收敛，balancer 负责候选内部的选择和保护。

## 拓展

### Q161【拓展】P2C、least_request、EWMA、ring hash、Maglev、一致性哈希分别适合什么流量模型？

P2C 适合大多数无状态、请求相对独立的 RPC。它开销低，能避开明显繁忙的 endpoint，适合 AegisMesh 这种客户端侧负载均衡。

least_request 更直接地看活跃请求数，适合请求耗时比较接近、inflight 能代表负载的服务。它的问题是如果所有客户端都追同一个最小值，可能造成瞬时拥挤，所以通常也会做采样。

EWMA 适合延迟变化明显的场景。它能把“最近慢”纳入路由，但要注意长尾请求完成后才更新的问题。

ring hash、一致性哈希、Maglev 更适合需要会话粘性或 key 粘性的流量，比如缓存、用户会话、分片存储。它们的目标不是每次都找最空节点，而是让同一个 key 尽量落到同一个 endpoint，并在节点增删时减少迁移。

所以选择算法要先看流量模型。无状态查询优先 adaptive P2C/least_request；缓存和有状态分片优先 hash 类算法；慢故障治理则需要在算法上叠加 outlier detection、slow_score 和 circuit breaker。

### Q162【拓展】Envoy 的 least request 和 outlier detection 如何组合？

可以把它们理解成两层。

outlier detection 先决定哪些 host 不该进入正常负载均衡集合。它会根据错误率、连续失败、成功率异常等信号，把异常 host 暂时 eject 出去。

least request 在剩下的可用 host 中做选择，优先选活跃请求更少的 host。也就是说，outlier detection 是“先把坏的拿掉”，least request 是“在可选集合里选更空的”。

AegisMesh 的思路类似，但落点不同。Controller 的 slow_score 和状态机类似 outlier detection，resolver 会过滤 `EJECTED/DEAD`。SDK 的 adaptive P2C 类似采样版 least request，不过它不只看 request 数，还看 EWMA、slow_score 和状态惩罚。

面试时我会这样类比：Envoy 是代理侧成熟实现，AegisMesh 是 SDK 侧实验系统；两者都遵循“异常剔除 + 负载选择”的组合思路。

### Q163【拓展】Locality-aware routing 如何和 adaptive P2C 组合？

locality-aware routing 应该先定义候选范围，再在范围内做 adaptive P2C。

比如客户端在 `zone-a`，策略可以优先选择同 zone endpoint。同 zone 有健康实例时，只在这些实例里做 P2C；同 zone 容量不足或全部退化时，再扩展到同 region；最后才跨 region。

也可以把 locality 做成 cost 的一部分。跨 zone 加一点成本，跨 region 加更高成本。这样 P2C 仍然能比较 endpoint，但会天然偏向本地。

我更倾向于“两环节”：先用 locality policy 过滤或分层，再用 adaptive P2C 处理同一层内部的负载和慢故障。这样策略更容易解释。否则 locality、slow_score、latency、weight 全混在一个公式里，调参会很难。

### Q164【拓展】如果要支持 weighted canary，weight、slow_score、variant policy 的优先级如何定义？

我会把优先级拆清楚。

第一层是安全过滤。`DEAD/EJECTED` 不接正常流量，最多等进入 `PROBING` 后接探测流量。这个优先级最高。

第二层是 variant policy。比如 secondary 只接 5% canary 流量，primary 接 95%。这个规则决定每个变体的基础流量份额。

第三层是变体内部的 endpoint weight。比如同为 secondary，有的实例权重大，有的权重小。

第四层是 slow_score 和本地 cost。它在变体份额内进一步调整，避开慢 endpoint。

例子是：policy 允许 secondary 接 5%，但 secondary 某个 endpoint 被 `EJECTED`，那它的正常流量应该是 0。如果 secondary 有多个健康 endpoint，5% 流量再按 weight 和 adaptive P2C 分配。如果 secondary 只剩 PROBING endpoint，是否继续 canary 要看策略，通常应该暂停正常 canary，只做探测。

### Q165【拓展】如果出现热点 key，P2C 会不会把热点扩散？需要什么额外机制？

如果请求是无状态的，P2C 会把热点 key 的请求分散到多个 endpoint，这通常是好事。

但如果热点 key 对应缓存或状态，情况就复杂了。P2C 可能把同一个 key 打到不同 endpoint，导致缓存命中率下降，甚至让多个节点同时承受同一个热点 key 的下游压力。这叫“热点扩散”，不一定是好事。

要处理这种场景，需要 key-aware routing。比如用一致性哈希让同一个 key 稳定落到某个 endpoint；热点 key 再单独做副本扩展、请求合并、缓存预热或读写分离。

AegisMesh 当前的 adaptive P2C 更适合无状态 RPC。如果要支持缓存服务或有状态分片，我会加 route key 提取和 hash policy，再把 slow_score 作为异常时的逃逸机制：正常按 key 粘住，目标 endpoint 慢到一定程度时才转移。

### Q166【拓展】负载均衡算法如何处理 streaming RPC 的长期连接？

streaming RPC 的难点是请求一旦选中 SubConn，后面可能持续很久。负载均衡算法只能在 stream 创建时 pick，不能轻易把一个正在进行的 stream 迁到另一个 endpoint。

这会带来两个问题。第一，启动时分配不均会长期存在。第二，inflight 数不能只看请求个数，因为一个 stream 可能占用资源很久。

处理方式一般有几类。可以给 streaming 方法单独的 policy，比如限制每个 endpoint 最大 stream 数；把 stream 持续时间或活跃消息数纳入 cost；对长 stream 做连接 draining；必要时把 streaming 和 unary 分开连接池。

AegisMesh 当前主要面向 unary RPC。它的 `PickResult.Done` 在 stream 结束前不会被调用，所以 EWMA 更新会更晚。要支持 streaming，需要在拦截器或 stream wrapper 里记录 stream 生命周期、消息数和持续时间。

### Q167【拓展】自适应路由可能造成振荡，工程上有哪些抑制手段？

振荡的本质是反馈太敏感。某个 endpoint 刚变慢，所有客户端一起避开；它压力下降后又看起来变快，客户端再一起打回来，反复循环。

抑制手段有很多。状态机要有滞回，比如进入 `DEGRADED/EJECTED` 的阈值高一点，恢复到 `HEALTHY` 的阈值低一点，并要求连续窗口。AegisMesh 里有 consecutive windows、ejection duration、recovery threshold 和 PROBING。

路由权重要渐变，不要一步从 100% 到 0 再从 0 回 100%。slow_score 可以通过 EWMA 或窗口平滑，probe ratio 也要限制恢复流量。

客户端之间要避免同步。resolver refresh、telemetry report、policy watch 可以加 jitter，避免所有客户端同一秒更新。新实例也要 warmup。

最后要有保护阀：retry budget、circuit breaker、rate limit 和 bulkhead。自适应路由本身不是万能的，如果下游整体过载，还要减少进入系统的流量。

### Q168【拓展】如何用排队论解释 inflight、latency 和吞吐之间的关系？

可以用 Little's Law 来解释：

```text
L = λW
```

`L` 是系统中的并发请求数，可以近似看成 inflight。`λ` 是吞吐，也就是每秒完成多少请求。`W` 是平均响应时间，也就是 latency。

在吞吐差不多的情况下，latency 变高，inflight 往往也会升高，因为请求在系统里停留更久。反过来，inflight 堆积也会带来排队，继续推高 latency。

当服务利用率接近 100% 时，排队延迟通常不是线性上升，而是会突然变得很大。这就是为什么慢故障经常先反映在 p95/p99 上，而平均延迟可能还没那么夸张。

AegisMesh 的 cost 同时看 inflight 和 latency，就是在同时看“队列里已经堆了多少请求”和“请求实际耗时是否变长”。inflight 更快，latency 更稳；两者结合，比只看一个指标更可靠。
