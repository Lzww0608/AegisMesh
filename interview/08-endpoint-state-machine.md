# 08. Endpoint 状态机、熔断恢复与探测

## 简单

### Q197【简单】Endpoint 有哪些状态？

AegisMesh 里定义了五个 endpoint 状态：`HEALTHY`、`DEGRADED`、`EJECTED`、`PROBING`、`DEAD`。

`HEALTHY` 是正常状态。endpoint 可以承接普通流量，adaptive P2C 只会按 inflight、EWMA、slow_score 等正常 cost 来选择它。

`DEGRADED` 是退化状态。endpoint 仍然可以被路由到，但会被降权。代码里的 cost 会对 `DEGRADED` 额外加惩罚，resolver 也会把这个状态下发给 SDK。

`EJECTED` 是摘除状态。它表示 endpoint 近期持续很差，不应该承接正常业务流量。resolver 会过滤 `EJECTED`，SDK 正常候选列表里不会有它。

`PROBING` 是恢复探测状态。endpoint 从 `EJECTED` 经过一段隔离时间后，不会直接回到 `HEALTHY`，而是先进入 `PROBING`。SDK 会给它少量探测流量，观察成功率和 slow_score。

`DEAD` 表示不可用或不应路由。当前 `StateMachine.Apply` 主要实现 `HEALTHY/DEGRADED/EJECTED/PROBING` 的自动迁移，`DEAD` 更像外部系统、注册中心或人工操作给出的终止状态。面试里我会主动说明这个边界。

### Q198【简单】HEALTHY 到 DEGRADED 的条件是什么？

默认情况下，endpoint 需要连续多个窗口 slow_score 超过 `DegradedThreshold`，才会从 `HEALTHY` 变成 `DEGRADED`。

默认配置是：

```text
DegradedThreshold  = 1.5
ConsecutiveWindows = 3
```

也就是说，slow_score 不是一次超过 1.5 就立刻降级，而是要连续 3 个窗口都达到退化阈值。代码里会维护 `ConsecutiveSlowWindows`。当 slow_score >= degraded threshold 时，这个计数加 1；如果下一次又低于阈值，计数会清零。

还有一个细节：如果 slow_score 直接超过 `EjectThreshold`，代码会同时增加 slow counter 和 eject counter。如果连续多个窗口都超过 eject threshold，endpoint 可能从 `HEALTHY` 直接进入 `EJECTED`，不一定先停在 `DEGRADED`。

所以 `DEGRADED` 的含义不是“一次慢了”，而是“连续窗口里已经出现稳定慢信号，但还没严重到必须摘除”。

### Q199【简单】DEGRADED 到 EJECTED 的意义是什么？

`DEGRADED` 到 `EJECTED` 的意义是从“降权使用”升级到“正常流量摘除”。

`DEGRADED` 状态下，endpoint 还在 resolver 地址列表里，adaptive P2C 仍然可能选它，只是 cost 更高。这样系统不会因为轻微退化马上损失容量。

`EJECTED` 不一样。它表示这个 endpoint 已经持续严重异常，继续让它接普通流量会拖慢 p99，甚至让重试、排队、超时一起放大。所以 resolver 会过滤它，SDK 不再把它放进正常候选集合。

默认触发条件是 slow_score 连续 `ConsecutiveWindows` 个窗口达到 `EjectThreshold`，默认 eject threshold 是 2.5。进入 `EJECTED` 时，状态机会记录 `EjectedAt`，后面靠它判断什么时候进入 `PROBING`。

### Q200【简单】EJECTED 什么时候进入 PROBING？

`EJECTED` 不会马上恢复。它要先等一个隔离时间，也就是 `EjectionDuration`。

默认配置是：

```text
EjectionDuration = 30s
```

代码逻辑很直接：如果当前状态是 `EJECTED`，并且 `now - EjectedAt >= EjectionDuration`，状态机就把它转成 `PROBING`。

这里不看当前 slow_score 是否已经变低。原因是 `EJECTED` 期间没有正常流量，样本通常不足，不能因为一两个缺失或旧样本就直接恢复。先进入 `PROBING`，再用少量真实请求验证，比直接回 `HEALTHY` 稳。

### Q201【简单】PROBING 如何回到 HEALTHY？

`PROBING` 回到 `HEALTHY` 要同时满足两类条件：探测成功率够高，slow_score 足够低。

代码里先做失败检查：

```text
successRate < ProbeSuccessThreshold
或
slow_score >= DegradedThreshold
```

只要满足其中之一，就会从 `PROBING` 回到 `EJECTED`。默认 `ProbeSuccessThreshold` 是 0.95，也就是探测请求成功率不能太低。

如果没有失败，再检查恢复条件：

```text
slow_score <= RecoveryThreshold
```

默认 `RecoveryThreshold` 是 1.0。满足这个条件时，endpoint 才能回到 `HEALTHY`。

这意味着 `PROBING` 不是走个流程。它必须用真实探测流量证明自己稳定。如果 success rate 低，或者 slow_score 又升到 degraded threshold 以上，就会被重新摘除。

### Q202【简单】为什么需要 ConsecutiveWindows？

因为单个窗口里的 telemetry 可能有噪声。

比如一个 endpoint 偶然遇到一次慢请求，或者某个窗口请求数太少，slow_score 可能短暂升高。如果状态机看到一次高分就立刻降级或摘除，会造成频繁误判。

`ConsecutiveWindows` 的作用是要求异常持续出现。默认值是 3。只有连续 3 个窗口都达到阈值，才说明它更像稳定退化，而不是单次抖动。

这也是状态机的第一层抗抖设计。它牺牲了一点检测速度，换来更低的 false positive。对慢故障来说，这个取舍通常可以接受，因为 fail-slow 往往不是毫秒级瞬时故障，而是持续一段时间的退化。

### Q203【简单】为什么需要 EjectionDuration？

`EjectionDuration` 是摘除后的冷却时间。

如果没有它，endpoint 一被摘除，下一次看到 score 变低就可能马上恢复。流量打回去后又变慢，再摘除，再恢复，就会产生路由震荡。

有了 `EjectionDuration`，endpoint 进入 `EJECTED` 后至少隔离一段时间。这个时间给服务端喘息，也让客户端流量稳定转移到其他 endpoint。

默认是 30 秒。实验里为了更快看到状态曲线，可以把它调短；生产里一般会更保守，尤其是资源过载、网络抖动这种恢复不确定的故障。

### Q204【简单】RecoveryThreshold 和 DegradedThreshold 为什么要分开？

这是为了做 hysteresis，也就是滞回。

默认配置是：

```text
DegradedThreshold = 1.5
RecoveryThreshold = 1.0
```

进入退化要求 slow_score >= 1.5，恢复健康要求 slow_score <= 1.0。中间的 1.0 到 1.5 是缓冲区。

如果进入和恢复用同一个阈值，比如都是 1.5，slow_score 在 1.49 和 1.51 附近波动时，状态会频繁切换。分开以后，进入要更高，恢复要更低，状态会稳定很多。

在 AegisMesh 里，如果 endpoint 已经是 `DEGRADED`，它不会因为 score 刚刚低于 1.5 就立刻恢复；必须低到 recovery threshold 以下才回 `HEALTHY`。

### Q205【简单】ProbeSuccessThreshold 用来防止什么问题？

它防止“延迟看起来恢复了，但请求其实还在失败”的问题。

`PROBING` 环节不只看 slow_score，还看 success rate。默认 `ProbeSuccessThreshold` 是 0.95。如果探测请求成功率低于 95%，状态机会把 endpoint 重新打回 `EJECTED`。

这个条件很重要。比如某个 endpoint 响应很快，但大量返回错误；或者连接偶尔成功，大部分失败。单看延迟可能觉得它不慢，但它不能稳定承接业务。

所以恢复判断要同时看 latency/slow_score 和成功率。AegisMesh 里的逻辑是：只要 success rate 不够，或者 slow_score 又高了，探测失败。

### Q206【简单】LastTransitionAt 和 EjectedAt 分别记录什么？

`LastTransitionAt` 记录最近一次状态变化发生的时间。不管是 `HEALTHY -> DEGRADED`、`DEGRADED -> EJECTED`，还是 `PROBING -> HEALTHY`，只要状态真的变了，都会更新它。

这个字段适合做观测和复盘。比如实验报告里想看“从故障注入到 DEGRADED 用了多久”、“从 EJECTED 到 PROBING 用了多久”，就需要状态迁移时间。

`EjectedAt` 只在进入 `EJECTED` 时设置。它的作用更具体：判断 ejection duration 是否已经到了。代码里从 `EJECTED` 转到 `PROBING` 时，就是看 `now - EjectedAt >= EjectionDuration`。

当 endpoint 回到 `HEALTHY` 时，`EjectedAt` 会被清空。这样下次再被摘除时，可以重新记录新的 ejection 时间。

## 深度

### Q207【深度】状态机 hysteresis 如何减少抖动？

AegisMesh 的 hysteresis 不是一个单独字段，而是几组设计叠在一起。

第一是进入和恢复阈值分开。进入 `DEGRADED` 默认要 slow_score >= 1.5，恢复 `HEALTHY` 默认要 <= 1.0。中间这段缓冲区可以吸收阈值附近的波动。

第二是连续窗口。slow_score 达到阈值一次不够，要连续多个窗口都异常。这样一次慢请求、一次网络抖动、一个低样本窗口不会立刻触发状态迁移。

第三是 ejection duration。进入 `EJECTED` 后，状态机不会马上根据新样本恢复，而是先隔离一段时间。这个冷却期能避免刚摘除就打回流量。

第四是 PROBING。恢复不是直接回 `HEALTHY`，而是先接少量探测流量。探测通过才恢复，探测失败就回 `EJECTED`。

这些机制组合起来，状态变化会慢一点，但更稳定。慢故障治理里，这比过度敏感更重要。一个误摘除可能让容量下降，多个客户端一起误摘除还可能引发级联问题。

### Q208【深度】如果 slow_score 在阈值附近来回波动，状态机会如何表现？

要分状态看。

如果 endpoint 还在 `HEALTHY`，slow_score 偶尔超过 `DegradedThreshold` 会让 `ConsecutiveSlowWindows` 加 1。但只要下一次低于 degraded threshold，这个计数就会清零。也就是说，在阈值附近来回跳，不会轻易进入 `DEGRADED`。

如果 endpoint 已经是 `DEGRADED`，表现更保守。score 低于 degraded threshold 后，慢窗口计数会清零，但状态不会马上回 `HEALTHY`。它必须降到 `RecoveryThreshold` 以下，才会恢复。

如果 endpoint 在 `PROBING`，要求更严格。只要 slow_score 再次达到 degraded threshold，或者 success rate 低于 probe threshold，就会回到 `EJECTED`。如果 score 位于 recovery threshold 和 degraded threshold 之间，并且成功率正常，它会继续保持 `PROBING`。

所以状态机不是围着一个阈值来回跳。它有进入门槛、恢复门槛、连续窗口和探测门槛，专门用来处理阈值附近的抖动。

### Q209【深度】为什么 EJECTED 状态下不继续根据新样本立即恢复？

因为 `EJECTED` 状态下的样本通常不可靠。

endpoint 被摘除后，正常流量已经不打过去了。这个时候拿到的样本可能很少，甚至没有。如果因为“没有错误”或“没有慢请求”就认为它恢复，那是错误的。没有流量不等于健康。

另一个原因是避免 ping-pong。假设 endpoint 刚被摘除，压力下降，少量指标看起来变好；如果马上恢复，大流量打回去后又变慢，又被摘除。用户看到的是 p99 来回抖，系统也会频繁重建路由状态。

AegisMesh 的做法是：`EJECTED` 期间只看 ejection duration。时间到了，进入 `PROBING`，再通过受限真实流量验证。这样恢复路径更可控。

代码里也能看到这一点：`StateEjected` 分支只判断 `EjectionDuration`，然后 return，不会继续走 slow_score 的普通恢复逻辑。

### Q210【深度】PROBING 失败后重新 EJECTED，会不会形成恢复饥饿？

会有这个风险，尤其在低 QPS 或 probe ratio 太低的服务里。

恢复饥饿的意思是：endpoint 实际上已经恢复了，但一直拿不到足够探测样本，或者偶发一次失败就被重新打回 `EJECTED`，导致它长期回不到正常流量。

AegisMesh 当前的逻辑是保守的。`PROBING` 环节 success rate 低于阈值，或者 slow_score 达到 degraded threshold，就会回 `EJECTED`。这个策略能保护用户流量，但对低样本场景比较敏感。

工程上可以加几个补救。第一，要求最小 probe 样本数，样本不足时保持 PROBING，而不是马上判失败。第二，给 PROBING 设置最长停留时间，超过后人工介入或降级为 least-bad。第三，低 QPS 服务不要只用比例探测，要保证每个窗口至少 N 个 probe。第四，重复 ejection 时可以逐步增加 ejection duration，但也要设置上限，避免永远没有恢复机会。

所以答案是：有可能形成恢复饥饿，当前实现适合实验和中等 QPS 场景，生产里要补最小样本和最大探测窗口。

### Q211【深度】状态机阈值在生产默认值和单机实验值之间为什么不同？

生产默认值通常更保守。原因很简单：误摘除的成本很高。一个 endpoint 被误判慢故障，容量会下降；如果多个 endpoint 一起被误摘除，整个服务可能反而更不稳定。

AegisMesh 默认 degraded threshold 是 1.5，eject threshold 是 2.5，consecutive windows 是 3，ejection duration 是 30 秒。这些值要求异常持续存在，才会改变状态。

单机实验不一样。实验目标是证明状态机能走出 `DEGRADED/EJECTED/PROBING/HEALTHY` 的完整曲线。如果还用生产保守参数，可能要等很久，或者单机模拟的故障强度不够，状态迁移不明显。

所以实验 compose 和实验脚本会调低阈值、缩短窗口或加重故障。这样能在几分钟内看到状态曲线。但报告里要说明这是实验配置，不能直接说生产也应该这么配。

面试时我会强调：实验参数是为了可观察性，生产参数是为了稳定性。这两个目标不一样。

### Q212【深度】如果 endpoint 没有流量，状态机 Tick 是否足以推动 EJECTED 到 PROBING？

能推动 `EJECTED -> PROBING`，但不一定足以证明恢复。

HealthManager 有 `Tick()`，它会对已有 health 状态再次调用 state machine。对 `EJECTED` 来说，只要 `EjectedAt` 到了 ejection duration，Tick 就能让它进入 `PROBING`。这个迁移不依赖新业务流量。

但 `PROBING -> HEALTHY` 最好依赖真实探测样本。当前 Tick 在没有新样本时，会用已有的 `health.SlowScore` 和 successRate=1 调用 Apply。这里有边界：如果旧 slow_score 仍然高，PROBING 可能马上回到 EJECTED；如果旧 slow_score 已经低，理论上可能回 HEALTHY，但这并不代表真的有新流量验证。

更严谨的生产设计应该要求 PROBING 状态下有最小探测样本数，不能只靠 Tick 判断恢复。Tick 适合推动时间型迁移，比如 ejection duration 到期；健康恢复最好靠真实 probe。

### Q213【深度】StateMachine.Apply 的输入 successRate 由请求成功率计算，这对低流量端点有什么影响？

低流量端点的 successRate 噪声会很大。

HealthManager 里的 `successRate(sample)` 是按窗口算的：如果 requestCount <= 0，就返回 1；否则用 `(requestCount - errorCount) / requestCount`。

这有两个边界。

第一，没有请求时 successRate 被当成 1。这样能避免无流量时被错误当成失败，但也可能让“没有证据”看起来像“成功”。所以 PROBING 不能只靠 successRate=1，还应该看最小样本数。

第二，低请求数时一个错误影响很大。比如一个窗口只有 2 个 probe，请求失败 1 个，successRate 就是 0.5，远低于默认 0.95，会被判为探测失败。对高 QPS 服务这很合理，对低 QPS 服务就可能太敏感。

改进方式是给 successRate 加置信度：样本数不足时保持当前状态，不做恢复或失败判断；或者使用贝叶斯平滑，避免 1/1、1/2 这种极小样本把状态推得太激进。

### Q214【深度】状态机是否应该区分网络慢、CPU 慢、应用错误慢？

状态机本身不一定要拆成很多状态，但应该保留原因信息。

当前 AegisMesh 的状态机只看总 slow_score 和 successRate。它不关心 slow_score 是 latency、error、inflight 还是 TCP retransmit 推高的。这样状态机简单，迁移规则也容易解释。

但运维和策略层需要知道原因。网络慢和 CPU 慢的处理方式不同：网络慢可能要切 zone、换链路或检查丢包；CPU 慢可能要扩容或限流；应用错误慢可能要回滚修订或关闭某个 method。

我会把状态和原因分开设计。状态仍然是 `HEALTHY/DEGRADED/EJECTED/PROBING`，但每次状态迁移都记录 reason，例如 `latency_outlier`、`network_retransmit`、`error_rate`、`inflight_saturation`。路由可以只看状态，排障和策略可以看原因。

如果把每种原因都做成状态，比如 `NETWORK_DEGRADED`、`CPU_DEGRADED`，状态机会膨胀，迁移组合也很难维护。

### Q215【深度】如果某个端点被错误 EJECTED，如何通过探测尽快纠正？

首先要承认误摘除可能发生。低样本、短暂抖动、SLO 配太紧、网络采集误归因，都可能让 endpoint 被错误 ejected。

纠正路径主要靠 `EjectionDuration -> PROBING -> HEALTHY`。如果希望纠正更快，可以把 ejection duration 设得不要过长，让 endpoint 有机会尽早进入 PROBING。进入 PROBING 后，用小比例真实流量验证 successRate 和 slow_score。

为了更快纠错，还可以加几类机制。

第一，给 PROBING 保证最小探测请求数。否则低 QPS 服务拿不到样本，恢复会慢。

第二，允许人工触发 probe 或人工恢复，但要带 TTL 和审计，不能永久覆盖自动状态。

第三，状态迁移事件里记录 score 分项和样本数。如果发现是低样本误判，可以调高 consecutive windows 或最小样本数。

第四，保留 least-bad 或 min healthy 保护。避免一个服务所有 endpoint 都被误摘除后完全不可用。

所以“尽快纠正”不是直接把 EJECTED 改回 HEALTHY，而是缩短验证路径，同时保证恢复是被真实 probe 证明的。

### Q216【深度】如果所有端点都 EJECTED，系统应该 fail closed 还是 fail open？

这要看业务目标。fail closed 更保护下游，fail open 更保护可用性。

fail closed 的意思是：既然所有 endpoint 都被判定为严重异常，那就不再给它们普通流量，调用快速失败。优点是避免继续打爆下游，缺点是用户请求可能全部失败。

fail open 的意思是：即使都被 ejected，也选一个 least-bad endpoint 继续发请求。优点是还有机会成功，缺点是可能把已经过载的服务继续打穿。

AegisMesh 当前更接近 fail closed。resolver 会过滤 `EJECTED/DEAD`，如果所有 endpoint 都是 `EJECTED`，正常候选列表会变空。过了 ejection duration 后，它们可以进入 `PROBING`，但在完全 ejected 的窗口里，普通请求可能没有可用地址。

生产里我会做成可配置策略。支付、写订单这种保护一致性和下游安全的场景更偏 fail closed；读缓存、推荐、非核心查询可以 fail open 或 degrade 到 stale cache。还要加 `max ejection percent`、`min healthy endpoints`，避免自动机制把整个服务摘空。

## 拓展

### Q217【拓展】Hystrix/Sentinel 的 circuit breaker 状态机和 AegisMesh endpoint 状态机有什么异同？

相同点是都有“正常、打开、半开”这类思路。它们都不希望在故障持续时继续把请求打到坏目标，也都需要通过少量请求判断是否恢复。

Hystrix 或 Sentinel 更偏本地资源或调用级保护。比如某个 command、resource、method 的错误率或慢调用比例过高，就打开熔断；过一段时间进入 half-open，放少量请求试探。

AegisMesh 的 endpoint 状态机更偏服务发现和路由。它管理的是某个 service 下的某个 instance。`EJECTED` 后会影响 resolver 地址列表和 SDK 路由，不只是本地拒绝某个函数调用。

指标也有差异。传统 circuit breaker 常看错误率、慢调用比例、并发数、最小请求数。AegisMesh 用 slow_score，把 p95 延迟、error、inflight、TCP retransmit、absolute SLO 都合成一个分数，再用状态机做迁移。

所以它们很像，但粒度不同：Hystrix/Sentinel 多数是本地调用保护，AegisMesh 是 endpoint 级慢故障治理和流量避让。

### Q218【拓展】熔断中的 closed、open、half-open 和 HEALTHY、EJECTED、PROBING 如何映射？

可以粗略这样映射：

```text
closed    -> HEALTHY / DEGRADED
open      -> EJECTED
half-open -> PROBING
```

`closed` 表示请求正常通过。AegisMesh 的 `HEALTHY` 对应这个状态。`DEGRADED` 也还在接流量，只是被降权，所以也可以看成一种带惩罚的 closed。

`open` 表示熔断打开，正常请求不再通过。AegisMesh 的 `EJECTED` 类似 open，resolver 会把它从正常候选里过滤掉。

`half-open` 表示少量试探请求。AegisMesh 的 `PROBING` 就是这个角色，通过 probe ratio 给少量流量，成功后回 `HEALTHY`，失败后回 `EJECTED`。

不同点是：传统 circuit breaker 往往在调用方本地直接拒绝请求；AegisMesh 的状态会通过 Controller、RegistryService、resolver、balancer 传到数据面，影响负载均衡和服务发现。

### Q219【拓展】Outlier ejection 的 base ejection time 和 max ejection percent 如何设计？

base ejection time 是第一次摘除的基础隔离时间。AegisMesh 里的 `EjectionDuration` 就是这个角色，默认 30 秒。

如果一个 endpoint 反复探测失败，可以逐步增加 ejection time，比如 30s、60s、120s，但要设置上限。这样能避免坏节点频繁进入 PROBING，又不会永远不给恢复机会。

max ejection percent 是为了防止摘除过度。比如一个 service 有 10 个实例，策略规定最多自动摘除 50%，那即使 8 个实例 slow_score 都高，也只摘除其中最差的 5 个，其余进入 `DEGRADED` 或 least-bad。

还要配合 min healthy endpoints。比如最少保留 2 个正常候选，不然所有实例都被摘除，服务会从慢变成不可用。

我会把设计分成两层：单 endpoint 的 ejection duration 控制恢复节奏；service 级 max ejection percent 控制容量底线。只做前者不够，容易在全体退化时把服务摘空。

### Q220【拓展】如何设计全局最小健康实例数，避免过度摘除？

可以在 Controller 的状态机外面加一个 service-level guard。

状态机可以先给每个 endpoint 计算候选状态，比如哪些应该 `EJECTED`。然后 service-level guard 再检查：如果执行这些摘除后，健康或可路由实例数低于 `minHealthyEndpoints`，就不要把所有候选都摘掉。

策略可以这样配置：

```yaml
outlier_detection:
  min_healthy_endpoints: 2
  max_ejection_percent: 50
```

当实例很多时，可以用百分比限制；实例很少时，最小数量更重要。比如两个实例的服务，max ejection percent 50% 表示最多摘一个；一个实例的服务，可能不能自动 EJECTED，只能 DEGRADED + 限流 + 告警。

保留的 endpoint 不一定都当健康看。可以把它们标成 `DEGRADED`，让 adaptive P2C 选 least-bad，同时触发报警和限流。这样服务不会因为自动摘除变成 0 容量。

### Q221【拓展】如果服务拓扑有依赖链，某个下游被 ejected 会如何影响上游健康判断？

依赖链里最容易出现误归因。

假设 frontend 调 order-service，order-service 又调 payment-service。payment-service 慢了以后，order-service 的响应也会变慢。如果只看 frontend 到 order-service 的 telemetry，可能会误以为 order-service 本身慢，于是把 order-service endpoint 也降级或摘除。

这会造成级联。下游慢导致上游慢，上游又被 ejected，流量转移后其他上游实例压力变大，故障范围扩大。

解决办法是把调用边和 trace 串起来。AegisMesh 的 trace 里有 source、destination、path、upstream，可以用来判断慢是发生在哪条边上。Telemetry 也应该按 caller -> callee 的 edge 记录，而不是只记录本服务总体延迟。

状态机层面可以保留原因。比如 order-service 被标慢时，同时看到 payment-service 已经 EJECTED，就要降低对 order-service 的惩罚，或者把它标为 downstream-induced degradation。这样不会把下游故障简单扩大成上游故障。

### Q222【拓展】如何把人工运维操作纳入状态机，例如手动隔离、手动恢复？

人工操作应该是状态机的一层 override，而不是直接乱改内部字段。

可以增加几类操作：手动隔离、手动恢复、冻结状态、强制进入 PROBING。每个操作都要带 operator、reason、TTL、创建时间和审批信息。

手动隔离的优先级应该高于自动恢复。比如 endpoint 被人工隔离 30 分钟，即使 slow_score 降下来了，也不能自动回 HEALTHY，除非 override 到期或被人工解除。

手动恢复要谨慎。最好不是直接改成 HEALTHY，而是强制进入 PROBING，让真实流量验证。如果确实需要立即恢复，也要记录审计事件，并在 dashboard 上标清楚这是人工操作。

实现上可以在 Controller 里维护 `manual_override` map 或 PolicyService 下发 override。状态机 Apply 时先看 override，再看自动规则。这样自动状态和人工状态不会互相覆盖得不明不白。

### Q223【拓展】状态机事件如何持久化，便于事故复盘？

要持久化的不是只有当前状态，还要有状态变化事件。

每次 transition 都应该记录一条事件，字段至少包括：service、instance_id、address、old_state、new_state、slow_score、success_rate、thresholds、consecutive counters、timestamp、policy revision、reason。

如果能拿到 score 分项，也应该记录 latencyScore、errorScore、inflightScore、retransmitScore。这样事故后可以回答：它为什么被摘除？是延迟高，错误多，还是网络重传？

存储形式可以先用 JSONL append-only 文件，项目实验里容易处理；生产里可以写数据库、Kafka 或事件日志系统。Prometheus 适合看当前指标和趋势，但不适合完整保存每次迁移的上下文。

复盘时，状态事件要能和 trace、fault injection、policy change 对齐。比如某次 `EJECTED` 前后，trace 里 p99 怎么变，policy 修订是不是刚改过，是否有人手动 override。这比只看最终状态有价值。

### Q224【拓展】如何用形式化方法或模型检查验证状态机没有死锁状态？

可以把状态机抽象成一个有限状态模型，然后写不变量和活性条件。

状态集合是：

```text
HEALTHY, DEGRADED, EJECTED, PROBING, DEAD
```

输入包括 slow_score 区间、successRate 区间、timePassed 是否超过 ejection duration。然后定义迁移规则：高分连续窗口进入 DEGRADED/EJECTED，EJECTED 到时间进入 PROBING，PROBING 成功回 HEALTHY，失败回 EJECTED。

要验证的不变量包括：状态永远在合法集合内；进入 EJECTED 时必须设置 EjectedAt；回到 HEALTHY 时 EjectedAt 被清空；transition 后 counters 被重置；PROBING 不会直接跳到 DEGRADED。

活性条件更重要。比如在时间持续推进的前提下，EJECTED 最终应该能进入 PROBING；在 PROBING 中，如果持续成功且 score 低于 recovery threshold，最终应该回 HEALTHY；如果持续失败，应该回 EJECTED。

工具上可以用 TLA+/PlusCal 写模型，也可以用 property-based testing 在 Go 里随机生成输入序列，检查不变量。AegisMesh 当前有单元测试覆盖几个关键路径，但还不是完整模型检查。后续如果状态机变复杂，比如加入 manual override、max ejection percent、多原因状态，就更值得用模型检查。
