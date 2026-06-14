# 07. slow_score、异常检测与评分算法

## 简单

### Q169【简单】slow_score 由哪些指标组成？

AegisMesh 当前的 slow_score 由四类分数组成：latency、error、inflight、network retransmit。

代码里默认权重是：

```text
LatencyWeight    = 0.45
ErrorWeight      = 0.25
InflightWeight   = 0.20
RetransmitWeight = 0.10
```

最终分数是：

```text
score =
  0.45 * latencyScore +
  0.25 * errorScore +
  0.20 * inflightScore +
  0.10 * retransmitScore
```

latencyScore 来自 endpoint 的 p95 延迟。它不是只看绝对值，而是先和同 service 其他实例比较，用 median/MAD 算相对异常；如果配置了 absolute latency SLO，还会算 `p95 / latency_slo`，最后取两者最大值。

errorScore 反映错误率是否高于 service 平均水平。inflightScore 反映这个 endpoint 当前并发占容量的比例。retransmitScore 反映 TCP retransmit 和 connect error 这类网络异常。

所以 slow_score 不是单一延迟指标。它把应用层延迟、错误、并发压力和网络层异常放在一个分数里，给状态机和路由使用。

### Q170【简单】为什么 latency 使用 p95 而不是平均值？

因为慢故障主要伤害尾延迟。

一个 endpoint 可能大多数请求都很快，少数请求特别慢。平均值会把这些慢请求摊平，看起来问题不大。但用户体验和系统级 SLO 往往被尾部请求拖垮，尤其是 checkout 这种多 RPC 串联的链路，一跳 p95/p99 升高会放大到整体请求上。

举个例子，100 个请求里 95 个是 20ms，5 个是 800ms。平均值大概 59ms，看起来还能接受；但 p95 已经接近慢请求边界，说明尾部明显变坏。

AegisMesh 的目标是治理 fail-slow，而 fail-slow 的典型表现就是 p95/p99 先变坏。用 p95 比均值更敏感，也比 p99 稍微稳定一点，适合单机 demo 和中等窗口的数据量。

### Q171【简单】为什么要使用 median 和 MAD？

median 是中位数，MAD 是 median absolute deviation，中位数绝对偏差。AegisMesh 用它们来做相对异常检测。

原因是它们比平均值和标准差更抗异常值。慢故障检测里，异常点本身可能非常大。如果用平均值和标准差，一个特别慢的 endpoint 会把平均值和标准差都拉高，反而让自己看起来没那么异常。

median/MAD 的思路更直接：先看同一个 service 里大多数实例的典型 p95 是多少，再看某个实例离这个典型值有多远。

代码里的相对延迟分数是：

```text
relativeLatencyScore =
  max(0, sampleLatencyP95 - medianLatency) / max(madLatency, 0.001)
```

也就是说，比中位数更快不会被惩罚；比中位数慢，慢得越多，分数越高。

### Q172【简单】ErrorCount、TimeoutCount、Inflight、TCPRetransmit 在评分中分别代表什么信号？

`ErrorCount` 是应用层失败信号。比如 gRPC 返回 `UNAVAILABLE`、`DEADLINE_EXCEEDED` 或其他非 OK 状态，说明请求已经失败或不可用。代码里 errorScore 用的是错误率相对 service 平均错误率的倍率。

`TimeoutCount` 是超时信号。它通常表示调用超过 deadline。当前 slow_score 代码没有给 TimeoutCount 单独设一个权重，但在 SDK recorder 里，超时请求通常也会计入 ErrorCount，并且会拉高 latency p95。所以它现在主要通过 error 和 latency 间接影响 slow_score。后续如果要更细，可以加 TimeoutWeight，把超时和普通错误分开看。

`Inflight` 是压力信号。请求还没完成时，latency 样本还没出来，但 inflight 已经能说明这个 endpoint 正在堆积。代码里 inflightScore 是 `Inflight / Capacity`。

`TCPRetransmit` 是网络层异常信号。它表示 TCP 有重传，可能是丢包、链路拥塞或网络质量下降。代码里还会把 `ConnectError` 和 `TCPRetransmit` 合在一起算 network events，再转成 retransmitScore。

这几类信号覆盖了不同层面：业务错误、超时、排队压力、网络异常。只看其中一个都容易漏判。

### Q173【简单】absolute latency SLO 解决了什么问题？

它解决的是“所有实例一起变慢”的盲区。

只用 relative outlier 时，算法会把实例互相比较。如果 user-service 的三个实例 p95 分别是 100ms、110ms、600ms，那 600ms 很容易被识别出来。但如果三个实例都变成 450ms、430ms、460ms，它们彼此差不多，相对异常分数可能不高。

可是从用户角度看，430ms 也已经慢了。这个时候就需要 absolute SLO。

AegisMesh 支持配置 latency SLO，比如 100ms。absolute latency score 是：

```text
p95_latency / latency_slo
```

如果 p95 是 450ms，SLO 是 100ms，absolute score 就是 4.5。最终 latencyScore 取：

```text
max(relativeLatencyScore, absoluteLatencyScore)
```

这样既能发现“某个实例比其他实例慢”，也能发现“所有实例都超过服务 SLO”。

### Q174【简单】默认权重为什么不是全部给 latency？

因为慢故障不只表现为延迟。

如果全部给 latency，有些故障会发现得太晚。比如 endpoint 正在堆积 inflight，但请求还没完成，p95 还没更新；或者网络开始重传，但业务延迟还没明显升高；或者错误率已经上升，但延迟看起来还行。

默认权重里 latency 占 0.45，是最大的一项，因为 AegisMesh 最关心 fail-slow。error 占 0.25，inflight 占 0.20，retransmit 占 0.10，用来补充不同层面的异常。

这个权重不是说永远正确。对读服务，latency 可以更重；对写服务，timeout/error 可能更重；对网络实验，retransmit 可以提高。项目选择这个默认值，是为了在 demo benchmark 里同时覆盖应用慢、错误、并发堆积和网络异常。

### Q175【简单】score key 为什么使用 service/instanceID？

代码里的 key 是：

```go
ScoreKey(service, instanceID) = service + "/" + instanceID
```

这样可以唯一定位一个 service 下的一个实例。只用 instanceID 可能冲突，因为不同 service 都可能有 `a`、`b` 这样的实例名。只用 address 也不稳，因为容器重启后 IP 可能变，NAT 和端口映射也可能让 address 不适合作为长期身份。

用 `service/instanceID` 还有一个好处：Registry、Telemetry、HealthManager、resolver 都能用同一个身份关联状态。Telemetry 上报的 endpoint address 可以解析成 instanceID，HealthManager 存健康状态，RegistryService 在 `ListInstances` 时再把健康状态叠加回实例列表。

当前边界是：这个 key 没有包含 method。如果一个实例只在某个方法上慢，实例级 score 会影响整个实例的路由。这个问题在后面的 method 维度题里要主动说明。

### Q176【简单】Capacity 缺省值为什么需要 fallback？

inflightScore 的公式是：

```text
Inflight / Capacity
```

如果 Capacity 没上报，或者上报成 0，直接除会出错，或者得到没有意义的结果。代码里 `capacity(value)` 做了 fallback：如果 Capacity <= 0，就按 100 处理。

这个默认值的作用是让系统在没有容量配置时仍然能工作。比如一个 endpoint inflight 是 20，capacity 缺失时，inflightScore 就是 0.2，而不是崩掉或变成无穷大。

100 只是保守默认，不代表每个服务真实容量都是 100。生产里更好的做法是通过 policy 或 registry metadata 配置每个实例的 capacity，比如按 CPU、连接池大小、服务类型或实例权重来设置。

### Q177【简单】score 为 0 表示什么？

score 为 0 表示当前窗口里没有被评分器识别出的异常信号。

具体说，可能是这个 endpoint 的 p95 不高于 service median，error count 是 0，inflight 是 0，网络异常也是 0。如果没有配置 absolute SLO，延迟低于或等于中位数时 latencyScore 也会是 0。

但它不等于“绝对健康”。它只表示“从当前采样数据和当前规则看，没有发现慢故障”。如果样本太少、指标没上报、某个 method 没被调用，score 也可能是 0。

面试时可以这样说：slow_score 是一个观测驱动的风险分数，不是医学诊断。score 低说明没有看到问题，不代表问题一定不存在。

### Q178【简单】慢分数是用于报警、路由、还是状态机？

三者都会用，但使用方式不同。

状态机直接用 slow_score。HealthManager 计算出 score 后，把它传给 state machine。默认阈值是 degraded 1.5、eject 2.5，连续多个窗口超过阈值才会迁移到 `DEGRADED` 或 `EJECTED`。

路由也会用 slow_score。RegistryService 在 `ListInstances` 时把 slow_score 叠加到实例返回给 SDK，resolver 放进 address attributes，adaptive P2C 再用它降低 effective weight、提高 cost。

报警和观测也会用。Prometheus 会导出 `aegis_endpoint_slow_score`，Grafana 可以画 slow_score 曲线。实验报告里的 recovery curve 也会看 slow_score、route weight、state 随时间变化。

所以 slow_score 是连接观测、控制面状态和数据面路由的中间信号。

## 深度

### Q179【深度】MAD 为 0 时用最小分母 0.001，会导致什么边界行为？

MAD 为 0 说明同一个 service 里的 p95 延迟非常接近，甚至完全一样。这个时候如果直接除 MAD，会除以 0。

代码里用了：

```text
denominator = max(madLatency, 0.001)
```

注意延迟单位是秒，所以 0.001 就是 1ms。这样做能避免除零，也让非常小的延迟差异不会产生无限大分数。

边界行为是：当 MAD 很小的时候，轻微延迟差也可能被放大。比如 median 是 100ms，MAD 是 0，某个实例 p95 是 105ms，那么 relativeLatencyScore 是：

```text
(0.105 - 0.100) / 0.001 = 5
```

这会让 5ms 的差异看起来很大。它的好处是对稳定服务很敏感，坏处是低噪声场景里可能误判。

工程上可以加几个保护：最小样本数、最小绝对差值、score cap、按服务配置最小 MAD，或者要求连续多个窗口超过阈值。AegisMesh 现在用状态机连续窗口来缓解这个问题，但评分层本身还可以再加置信度控制。

### Q180【深度】相对异常分数在实例数量很少时是否稳定？

不太稳定。

median/MAD 适合有多个 peer 的场景。如果一个 service 有 10 个实例，大多数都正常，一个实例慢，median/MAD 很容易识别异常。但如果只有 2 个实例，就很难说谁是基准。一个 100ms，一个 300ms，中位数是两者平均，MAD 也会受这两个值影响，分数解释会变弱。

只有一个实例时，相对异常检测基本没有意义。它没有 peer 可以比较，median 就是它自己，relativeLatencyScore 往往是 0。

所以 AegisMesh 加了 absolute SLO。实例数量少时，尤其是一两个实例时，SLO 比相对分数更重要。比如 p95 超过 100ms SLO，就算没有 peer，也能给出 absolute latency score。

生产里还可以做历史 baseline：拿这个实例过去一段时间的正常延迟做对照，而不是只和当前 peer 比较。

### Q181【深度】error rate / avgErrorRate 的设计在整体错误率很低时会不会放大噪声？

会有这个风险。

代码里的 errorScore 是：

```text
errorRate / max(avgErrorRate, epsilon)
```

如果某个 endpoint 有错误，且 service 平均错误率很低，那么这个比值会很大。举个例子，10 个 endpoint 里只有一个 endpoint 在 100 个请求里错了 1 次。它自己的错误率是 1%，service 平均错误率大约是 0.1%，errorScore 就接近 10。

这种设计的好处是能抓住“只有一个实例在出错”的情况。坏处是低 QPS 或偶发错误时，噪声会被放大。

缓解方法有几个。第一，设置最小请求数，低于样本量不计算 errorScore 或降低权重。第二，对 errorScore 做上限，比如 cap 到 5 或 10。第三，把错误类型区分开，`UNAVAILABLE`、`DEADLINE_EXCEEDED`、业务 4xx 不应该一概而论。第四，状态机要求连续窗口，避免一次偶发错误直接改变状态。

AegisMesh 当前靠连续窗口和权重降低误判，但如果要生产化，我会补最小样本量和 errorScore cap。

### Q182【深度】networkRate 在 requestCount 为 0 时使用 count 本身，有什么合理性和风险？

代码里的 `networkRate(count, total)` 是这样处理的：如果 network event count <= 0，返回 0；如果 requestCount <= 0，就直接返回 count；否则返回 count / requestCount。

这么做有合理性。eBPF 网络信号不一定能精确归因到某个业务请求窗口。比如 TCP retransmit 或 connect error 被采集到了，但这个窗口里 SDK telemetry 的 requestCount 是 0。如果直接返回 0，就会丢掉网络异常。用 count 本身，可以让“没有请求但有网络异常”的情况仍然进入 slow_score。

风险是尺度不统一。requestCount > 0 时是比例，requestCount = 0 时是绝对次数。10 次 retransmit 在高 QPS 下可能不严重，在无请求窗口里却会变成 10，分数可能很高。

更稳的做法是给网络事件单独建模，比如按连接数、时间窗口、TCP session 数归一化，或者要求网络事件必须映射到具体 endpoint 和时间窗口。当前实现更偏实验可用，能把网络异常纳入 score，但归一化还可以继续优化。

### Q183【深度】慢故障评分应该按 service 聚合，还是按 service+method 聚合？

这要看你想治理什么问题。

按 service 聚合更简单。一个实例如果整体变慢，比如 CPU 被打满、网络丢包、进程卡住，那么所有 method 都会受影响。service 级 slow_score 能更快把这个实例降权或摘除。

按 service+method 聚合更精细。如果只有某个 method 慢，比如 `CreateOrder` 慢但 `GetUser` 正常，service 级评分会把整个实例都标慢，可能误伤其他方法。method 级评分可以做到“只对这个方法避让或关闭重试”。

当前 AegisMesh 的 telemetry recorder 是按 destination + method + upstream 聚合的，样本里也有 Method 字段。但 `ScoreKey` 是 `service/instanceID`，HealthManager 也是实例级状态。所以现在更接近 service+instance 健康，而不是 method+instance 健康。

我会把当前设计解释为 MVP 的保守选择：先解决实例级 fail-slow。下一步可以把 score key 扩展成 `service/method/instanceID`，并让 PolicyService 决定哪些 method 需要独立 outlier detection。

### Q184【深度】如果一个实例只在某个 method 上慢，全局实例级 slow_score 会带来什么误伤？

会把局部问题扩大成实例级问题。

比如 user-service 有两个方法：`GetUser` 很快，`SearchUserHistory` 很慢。如果某个实例只是在 `SearchUserHistory` 上慢，实例级 slow_score 可能把这个实例标成 `DEGRADED`。结果 SDK 调 `GetUser` 时也会降低这个实例权重，甚至在严重时把它摘除。

这就是误伤。它牺牲了一部分正常容量，换来更简单的治理模型。

如果要避免这个问题，需要 method-level health。具体做法是：评分 key 加 method；状态机也按 method 维护；resolver 或 policy 下发 method-level route cost；balancer 在 pick 时知道当前 RPC method，再用对应 method 的 endpoint 状态。

这个改造不小，因为 gRPC balancer 的 pick 能拿到 `PickInfo`，但要把 method-level policy、resolver attribute 和 telemetry 对齐。当前项目已经有 method-level retry policy，后续可以沿着这个方向扩展 method-level slow_score。

### Q185【深度】如何处理高 QPS 和低 QPS endpoint 的统计置信度差异？

高 QPS endpoint 样本多，p95、错误率、重传率都更稳定。低 QPS endpoint 样本少，一个慢请求或一个错误就可能把分数拉得很高。

如果不处理置信度，低 QPS endpoint 容易误判，高 QPS endpoint 的小幅退化又可能被平均掉。

常见做法是加样本量门槛。比如 requestCount 低于 N 时，不触发 ejection，只记录观测；或者把 score 乘以 confidence factor：

```text
confidence = min(1, requestCount / minSamples)
adjustedScore = score * confidence
```

也可以拉长低 QPS 服务的窗口，让它积累更多样本。对错误率和网络事件可以用贝叶斯平滑，比如给每个 endpoint 加一个先验，避免 1/1 的错误率被当成 100% 故障。

AegisMesh 当前评分层没有显式 confidence，主要靠状态机连续窗口降低误判。面试时我会承认这一点，并说明生产化会补最小样本量、score cap 和置信度。

### Q186【深度】slow_score 是否应该做时间平滑？如果要做，放在 SDK、Controller 还是 state machine？

应该做，但要放对位置。

SDK 已经有本地 latency EWMA，它负责快速反应当前客户端看到的延迟变化。这个平滑是局部的、数据面内的。

Controller 的 slow_score 可以做跨窗口平滑，比如对每个 endpoint 的 score 做 EWMA。这样可以减少单个窗口噪声，但也会带来反应变慢。适合用于状态机输入，不一定适合每个路由瞬间的判断。

state machine 本身也在做一种离散平滑：连续多个窗口超过阈值才进入 `DEGRADED/EJECTED`，低于 recovery threshold 才恢复。这比单纯对 score 做 EWMA 更容易解释。

我倾向于三层都保留，但职责不同：SDK EWMA 做快速局部避让；Controller score 可以轻微平滑；state machine 用连续窗口和滞回做最终状态迁移。不要把平滑全部塞进一个地方，否则要么反应太慢，要么太抖。

### Q187【深度】absolute SLO 与 relative outlier 同时启用时，max 组合是否总是合理？

不总是合理，但它是一个清楚、保守的选择。

`max(relative, absolute)` 的含义是：只要一个 endpoint 相对同伴明显慢，或者绝对延迟超过 SLO，就应该提高 latencyScore。这样不会漏掉“单实例 outlier”，也不会漏掉“全体超过 SLO”。

风险在于 SLO 配错时会放大问题。如果 SLO 设得过低，所有 endpoint 都会得到很高 absolute score，状态机可能把整个服务都推向 DEGRADED。此时路由避让没有太多帮助，因为没有更好的 endpoint。

还有一个风险是 relative 和 absolute 的尺度不完全一致。relative 是按 MAD 标准化的偏离程度，absolute 是 SLO 倍数。两者取 max 简单，但不一定校准得很好。

生产里可以做得更细。比如对 absolute score 做 cap；只有 requestCount 达标才启用 absolute；或者分开输出 relativeLatencyScore 和 absoluteLatencyScore，让状态机根据策略决定组合方式。AegisMesh 当前用 max，是为了让两个盲区都能被覆盖，实验里也能清楚验证 absolute SLO 的效果。

### Q188【深度】如果所有实例都慢，route weight 降低对用户体验有什么实际帮助？

如果所有实例都慢，单纯换 endpoint 帮助有限。没有健康实例时，路由算法没法凭空创造容量。

但是 slow_score 仍然有用。第一，它能让系统知道“这是整体 SLO 退化”，而不是某个单点 outlier。第二，它能触发状态机、报警和 dashboard，告诉运维服务整体变慢。第三，它能配合 retry budget 和 circuit breaker，避免继续用重试放大压力。第四，它可以作为上游限流或降级的输入。

route weight 降低在这种场景下主要不是为了“找到更快实例”，而是为了让数据面少信任这些 endpoint。比如 adaptive P2C 会更依赖 inflight 和 least-bad，PROBING/DEGRADED 状态也能约束恢复流量。

真正改善用户体验，需要额外动作：限流、降级、缓存、关闭重试、扩容、切换下游、熔断非核心接口。slow_score 在这里更像故障信号和保护触发器，而不是唯一解决方案。

## 拓展

### Q189【拓展】异常检测中 median/MAD、z-score、IQR、EWMA control chart 各自适合什么数据分布？

z-score 适合接近正态分布的数据。它用平均值和标准差衡量偏离程度，解释简单。但它对异常值敏感，尾延迟这种长尾分布下容易被极端值拉偏。

median/MAD 更适合长尾、有 outlier 的数据。中位数和 MAD 不容易被一个极端慢实例影响，所以适合 AegisMesh 这种“同 service 多实例对比”的慢故障检测。

IQR 用的是四分位距，通常看 Q1 到 Q3 的范围，再判断超过上界的点。它也比较抗 outlier，适合离线分析或数据分布不太稳定的场景。

EWMA control chart 更适合时间序列。它关注某个指标相对过去自己的变化，适合发现“这个实例比自己平时慢了”。它不要求同一时间有很多 peer，但需要历史 baseline。

AegisMesh 当前用了 median/MAD 做横向比较，又加 absolute SLO 补全体变慢的问题。后续如果要更强，可以加 EWMA control chart 做纵向 baseline。

### Q190【拓展】尾延迟治理为什么通常关注 p95/p99 而不是均值？

用户体验常常被尾部请求决定，不是被平均请求决定。

微服务调用链里，一个用户请求可能经过多个下游 RPC。即使每一跳只有 5% 慢请求，串起来之后，整体请求遇到慢节点的概率会明显上升。平均值可能还不错，但 p95/p99 已经影响用户。

均值还有一个问题：它会把少数极慢请求摊薄。比如 99 个请求 20ms，一个请求 2s，平均值大约 39.8ms，看起来还行。但那个 2s 请求对用户来说就是明显卡顿。

慢故障治理关心的是“哪些节点还活着但会拖尾部”。p95/p99 正好能看到这种尾部变化。AegisMesh 选 p95，是因为它比均值敏感，又比 p99 在小窗口里更稳定。

### Q191【拓展】SLO、SLA、SLI 的关系是什么？slow_score 更接近哪一类？

SLI 是指标，比如延迟 p95、错误率、可用性、超时率。它回答“怎么量”。

SLO 是目标，比如 p95 < 100ms、错误率 < 0.1%。它回答“希望达到什么水平”。

SLA 是对外承诺，通常带赔付或合同约束。它回答“没达到目标要承担什么责任”。

slow_score 不是 SLA，也不是单个 SLI。它更像一个内部健康评分，由多个 SLI 派生出来。里面用到了 latency p95、error rate、inflight、network events；absolute latency SLO 也可以参与评分。

所以我会说：slow_score 是面向治理的 composite health score。它把多个 SLI 和部分 SLO 信息融合后，给路由和状态机使用。

### Q192【拓展】如何把 RED 指标、USE 指标和网络信号融合到一个健康分数里？

RED 是 request rate、error rate、duration，常用于服务视角。USE 是 utilization、saturation、errors，常用于资源视角。网络信号包括 retransmit、connect error、connect latency、packet loss 等。

融合时不能简单把所有指标相加。要先统一方向和尺度：越大越坏的指标可以直接归一化；越大越好的指标要反向；不同单位要变成 ratio、z-score、MAD score 或 SLO 倍数。

AegisMesh 当前做了一个简化版。duration 用 latency p95 的 relative/MAD 和 absolute SLO；error 用 error rate 相对 service 平均值；saturation 用 inflight/capacity；网络用 retransmit/connect error rate。

更完整的系统会把指标分组。应用层慢、资源饱和、网络异常分别给分，再输出总分和分项原因。总分用于自动路由，分项用于解释：到底是应用慢、错误多、连接失败，还是网络重传。

### Q193【拓展】如果引入机器学习异常检测，会面临哪些可解释性和稳定性问题？

机器学习可以捕捉复杂模式，但在服务治理里有几个现实问题。

第一是可解释性。模型说某个 endpoint 异常，工程师需要知道原因：是 p95 高、错误多、inflight 堆积，还是网络重传。没有解释，自动摘除会很难让人放心。

第二是稳定性。流量模式、版本发布、节假日、租户行为都会变。模型如果频繁误判，会让路由震荡；如果太钝，又发现不了慢故障。

第三是训练数据。真实故障样本少，而且标签不干净。很多慢故障没有明确开始和结束时间，拿它训练很容易学偏。

第四是上线风险。异常检测结果会影响流量，模型更新本身也要灰度、回滚、审计。

所以我会先保留规则型 slow_score。它简单、可解释、容易调参。ML 可以作为辅助，比如自动学习 baseline 或给出异常建议，但不应该一开始就直接控制 ejection。

### Q194【拓展】如何为不同服务自动学习 baseline，而不是手工设置 SLO？

可以从历史正常窗口里学习每个 service 或 method 的 baseline。

最简单的是按时间窗口统计 p50、p95、p99、错误率、inflight 分布，取过去几天同一时段的分位数作为基线。这样能处理日夜流量差异，比如白天高峰和凌晨低谷不强行用同一个阈值。

还可以按版本、zone、租户、method 分维度学习。一个重接口不能和轻接口共用 baseline；一个跨地域调用也不能和本地调用共用 baseline。

自动 baseline 要有保护。发布新版本、流量突增、故障期间的数据不能直接学进去，否则会把坏状态当成新正常。通常需要排除告警窗口、发布窗口和异常窗口，并设置上下限。

在 AegisMesh 里，可以把自动 baseline 转成 PolicyService 的 latency SLO 或 degraded threshold。Controller 仍然用可解释的 slow_score，只是阈值不再完全手工配置。

### Q195【拓展】在多租户服务中，某个租户的慢请求是否应该影响全局 endpoint 健康？

不应该直接影响全局健康，至少不能无脑影响。

多租户服务里，一个租户可能请求特别重、数据特别大，或者自己的下游依赖慢。如果把这个租户的慢请求直接混进全局 endpoint score，可能导致整个实例被降权，影响其他正常租户。

更合理的做法是按 tenant 维度打标签，至少在 telemetry 里保留 tenant 或 workload class。评分时可以分两层：全局 endpoint health 看所有租户的整体表现；租户级 health 看某个租户是否在某些 endpoint 上异常。

如果只有某个租户慢，可以做租户级限流、隔离、路由或降级，而不是摘除整个 endpoint。只有当多个租户都看到这个 endpoint 慢，才更像 endpoint 本身问题。

这也是为什么生产系统里 policy 需要 namespace、tenant、method 这些维度。AegisMesh 当前 demo 没有多租户，但扩展方向很明确。

### Q196【拓展】如何评估 slow_score 的 precision、recall、false positive、false negative？

要先定义 ground truth，也就是“什么时候 endpoint 真的慢故障”。实验里可以通过 fault injector 注入已知故障，比如给 user-b 加 200ms delay、CPU throttle、packet loss、connect error。注入窗口就是正样本，没有故障的窗口就是负样本。

然后按时间窗口比较 slow_score 或状态机输出。比如某个窗口里故障正在发生，如果 slow_score 超过阈值或状态进入 `DEGRADED/EJECTED`，算 true positive；故障发生但没触发，算 false negative；没有故障却触发，算 false positive；没有故障也没触发，算 true negative。

precision 是触发的告警里有多少是真的：

```text
precision = TP / (TP + FP)
```

recall 是真实故障里抓到了多少：

```text
recall = TP / (TP + FN)
```

还要看检测延迟和恢复延迟。慢故障治理不只是“有没有检测到”，还要看多久检测到、多久摘除、恢复后多久回到 HEALTHY。

AegisMesh 的实验已经能提供一部分证据，比如 single_instance_delay、cpu_throttle、packet_loss、recovery curve。后续可以把实验脚本输出的 recovery.csv 和 latency.csv 对齐，自动算 precision、recall、false positive、false negative、mean detection time 和 mean recovery time。
