# 11. Telemetry、EWMA、p95 与指标上报

## 简单

### Q281【简单】SDK Recorder 记录哪些 Observation 字段？

`Observation` 是 SDK 本地记录一次 RPC 结果的结构。它在 `pkg/telemetry/recorder.go` 里定义，字段有这些：

```go
type Observation struct {
    Source      string
    Destination string
    Method      string
    Upstream    string
    Status      string
    Latency     time.Duration
    Error       bool
    Timeout     bool
}
```

我会按三个维度解释。

第一是身份维度：

- `Source`：调用方，比如 `frontend`。
- `Destination`：目标服务，比如 `user-service`。
- `Method`：gRPC full method，比如 `/demo.shop.v1.UserService/GetUser`。
- `Upstream`：实际打到的后端地址，比如 `172.18.0.5:7002`。

第二是结果维度：

- `Status`：gRPC status code 的字符串，例如 `OK`、`UNAVAILABLE`、`DEADLINE_EXCEEDED`。
- `Error`：这次调用是否是错误。
- `Timeout`：这次调用是否是超时。

第三是耗时维度：

- `Latency`：本次 RPC 从发起到返回的耗时。

SDK unary interceptor 会在调用完成后生成 `Observation`。它用 `peer.Peer` 拿到真实 upstream address，用 `status.Code(err)` 拿到 gRPC code，再把这些信息交给 Recorder 聚合。

### Q282【简单】EndpointStats 中 RequestCount、ErrorCount、TimeoutCount、Inflight 分别怎么计算？

`EndpointStats` 是一个窗口内的聚合结果，不是一条请求。

`RequestCount` 每记录一次完成的 observation 就加 1。无论成功还是失败，只要这次 RPC 结束并被记录，就算一次请求。

`ErrorCount` 的判断条件是：

```go
if obs.Error || obs.Status != "OK" {
    row.errorCount++
}
```

也就是说，只要显式标记了 `Error=true`，或者 status 不是 `OK`，都会记为错误。

`TimeoutCount` 的判断条件是：

```go
if obs.Timeout || obs.Status == "DEADLINE_EXCEEDED" {
    row.timeoutCount++
}
```

所以只要显式标记超时，或者 gRPC code 是 `DEADLINE_EXCEEDED`，就会记为 timeout。

`Inflight` 表示当前还没完成的请求数。Recorder 有一个 `Start` API，调用时 in-flight 加 1；它返回一个 finish 函数，finish 执行时 in-flight 减 1，并记录 latency。

有个实现细节要说清楚：当前 SDK unary interceptor 实际用的是 `Observe`，它在调用完成后一次性记录 observation，不走 `Start/finish`。所以 Recorder 的 `Start` 更像通用统计接口和测试覆盖路径；真正 adaptive balancer 里的实时 in-flight 还有一套本地 stats，用于 P2C cost 和 breaker。

### Q283【简单】EWMA 的 alpha 默认是多少？

默认是 0.2。

代码在 `pkg/telemetry/ewma.go`：

```go
const defaultEWMAAlpha = 0.2
```

EWMA 的更新公式是：

```text
newValue = alpha * sample + (1 - alpha) * oldValue
```

第一次样本比较特殊，不套公式，直接把第一个 latency 作为初始 EWMA。测试里有一个例子：

- 第一次观测 100ms，EWMA = 100ms。
- 第二次观测 200ms，alpha = 0.2。
- 新 EWMA = 0.2 * 200ms + 0.8 * 100ms = 120ms。

这个值比直接用最新 latency 稳，也比简单平均更快响应近期变化。

### Q284【简单】Snapshot 和 SnapshotAndReset 的区别是什么？

`Snapshot()` 只读取当前窗口统计，不清空数据。

`SnapshotAndReset()` 会先返回当前窗口统计，然后重置窗口内的计数和 latency 样本。

具体来说，reset 时会清掉这些字段：

- `requestCount`
- `errorCount`
- `timeoutCount`
- `latencies`
- `windowStart`

但它不会清掉 EWMA，也不会把 row 从 map 里删掉。EWMA 会继续保留历史平滑值。

这正好适合 telemetry reporter。SDK 每 5 秒调用一次 `SnapshotAndReset()`，把这 5 秒窗口的数据上报给 Controller。下一轮重新开始统计 request、error、timeout 和 p95，但 EWMA 仍然延续，不会每 5 秒从零开始。

### Q285【简单】为什么 telemetry 上报需要按 destination、method、upstream 聚合？

这三个维度分别回答不同问题。

`destination` 回答“哪个下游服务出问题”。比如 frontend 调 user-service 和 order-service，两个目标服务要分开看。

`method` 回答“哪个 RPC 方法出问题”。同一个服务里，`GetUser` 可能很快，`SearchUserHistory` 可能很慢。如果只按服务聚合，会把慢方法和快方法混在一起。

`upstream` 回答“哪个具体实例出问题”。AegisMesh 做的是 endpoint 级慢故障治理，必须知道请求打到了 `user-a` 还是 `user-b`。如果只知道 user-service 慢，不知道哪个实例慢，就没法做 adaptive P2C、DEGRADED、EJECTED 和 PROBING。

所以聚合 key 是：

```go
destination + method + upstream
```

Controller 后面会根据 service/address 解析出 instance ID，再按 service+instance 算 slow_score 和状态。

### Q286【简单】p95 是如何从窗口内 latency 样本计算的？

Recorder 会把窗口内每次完成 RPC 的 latency 追加到 `row.latencies` 里：

```go
row.latencies = append(row.latencies, obs.Latency)
```

计算 p95 时，它会复制一份样本，排序，然后取 `ceil(0.95 * n) - 1` 这个位置：

```go
idx := int(q*float64(len(values))+0.999999999) - 1
```

这个写法等价于用向上取整找分位点。比如一个窗口里有 2 个样本，100ms 和 300ms，p95 会取 300ms。测试里也是这个结果。

它的优点是简单，结果也容易解释。缺点是高 QPS 下要保存全部样本，Snapshot 时还要排序，性能成本会变高。

### Q287【简单】Start 返回 finish 函数的设计有什么意义？

`Start` 的设计是把“开始计时”和“结束记录”配成一对。

调用 `Start(destination, method, upstream)` 时，Recorder 会记录开始时间，并把 in-flight 加 1。它返回一个 `finish(status)` 函数。请求结束时调用 finish，Recorder 会计算 latency、减少 in-flight、更新 request/error/timeout/p95/EWMA。

这种写法有几个好处：

- 调用方不用自己保存开始时间。
- in-flight 的加减在同一个 Recorder 里完成，不容易写散。
- finish 内部用了 `sync.Once`，重复调用也只会记录一次。
- 很适合配合 `defer finish(status)` 使用。

当前 SDK unary interceptor 没有用 `Start`，而是请求结束后直接 `Observe`。但 `Start/finish` 这个接口对服务端统计、手写客户端统计、未来 streaming RPC 统计都很有用。

### Q288【简单】normalizeStatus 为什么把空 status 视为 OK？

这是为了让缺省成功路径更自然。

`normalizeStatus` 的逻辑是：

```go
status = strings.TrimSpace(status)
if status == "" {
    return "OK"
}
return status
```

很多调用方在成功时可能不会显式传 status。如果空字符串不处理，后面的错误判断是 `obs.Status != "OK"`，空 status 就会被误算成错误。

把空 status 视为 `OK`，可以避免这种误判。

但这个设计也有一个前提：失败路径必须正确传 status 或设置 `Error=true`。如果调用方失败了却什么都不填，Recorder 会把它当成功。所以 SDK interceptor 里不能偷懒，它会统一用 `status.Code(err).String()` 生成 status。

### Q289【简单】Prometheus metrics 和 telemetry report 有什么区别？

Prometheus metrics 是给人和监控系统看的。它通过 HTTP scrape 拉取，主要用于 dashboard、告警、排查问题。

AegisMesh 里 SDK 暴露的 Prometheus 指标包括：

- `aegis_rpc_requests_total`
- `aegis_rpc_latency_seconds`
- `aegis_endpoint_inflight`
- `aegis_endpoint_latency_ewma_seconds`

Controller 侧还有 health 指标：

- `aegis_endpoint_slow_score`
- `aegis_endpoint_state`

telemetry report 是 SDK 主动用 gRPC 上报给 Controller 的控制面输入。它进入 `TelemetryService.ReportEndpointStats`，Controller 再用这些样本计算 slow_score 和 endpoint 状态。

简单说：

- Prometheus metrics 用于观测和告警。
- telemetry report 用于控制面决策。

它们的数据有重叠，但用途不同。Prometheus 抓不到一轮指标，不应该直接影响路由；telemetry report 丢了，Controller 的健康状态就可能变旧。

### Q290【简单】为什么 metrics 和 control-plane telemetry 不能完全混为一谈？

因为它们的可靠性要求、数据模型和后果都不一样。

Prometheus metrics 是观测系统。它可以容忍某次 scrape 失败，也可以容忍短时间延迟。它的目标是让人看清系统发生了什么。

control-plane telemetry 是治理系统的输入。它会影响 slow_score、状态机、resolver 下发的 endpoint 状态，最后影响真实请求路由。这个路径不能随便接收高基数、重复、乱序或语义不清的数据。

举个例子，Prometheus 里加一个 `trace_id` label 会造成高基数，最多是监控系统爆炸；如果 control-plane telemetry 也把 trace 级别数据直接拿来做路由，就可能被噪声带偏，导致 endpoint 被误摘除。

所以我会把它们分开设计：

- metrics 面向观测，label 可以服务排查，但要控制基数。
- telemetry 面向决策，字段要少、稳定、可聚合，有窗口和来源信息。

## 深度

### Q291【深度】EWMA alpha 变大或变小对故障响应速度有什么影响？

alpha 越大，EWMA 越敏感，越接近最新样本。

比如 alpha=0.8，一次 500ms 的慢请求会很快把 EWMA 拉高。好处是故障响应快；坏处是容易被偶发长尾影响，正常服务偶尔抖一下也会让 EWMA 大幅波动。

alpha 越小，EWMA 越平滑，越依赖历史值。

比如 alpha=0.05，要连续很多个慢样本才能把 EWMA 拉高。好处是抗噪声，坏处是 fail-slow 刚发生时反应慢。

AegisMesh 默认 0.2 是一个折中。它不会完全跟着单个样本跳，但连续慢请求会比较快反映出来。

面试时我会补一句：EWMA 更适合做路由 cost 的平滑信号，真正的慢故障判定还要看窗口 p95、error、timeout、in-flight 和状态机的 consecutive windows。不能只靠一个 EWMA 值做摘除。

### Q292【深度】窗口 reset 后 EWMA 是否应该重置？项目当前行为有什么取舍？

项目当前不会重置 EWMA。

`SnapshotAndReset()` 会清掉窗口内的 request/error/timeout 和 latency 样本，但 row 里的 `ewma` 保留。这样每个上报窗口都有一个连续的延迟趋势，而不是每 5 秒重新从第一个样本开始。

好处是路由和控制面看到的延迟更稳。比如某个 endpoint 刚经历过慢故障，窗口切换后 EWMA 还保留一部分历史，系统不会马上忘记它刚才慢过。

坏处是恢复时也会慢一点。故障解除后，EWMA 需要靠新的快样本慢慢拉回来。如果 alpha 小，这个恢复过程会更明显。

要不要重置取决于用途：

- 用于趋势和路由，保留 EWMA 更合理。
- 用于严格窗口统计，应该只看当前窗口 p95。
- 用于恢复判断，可以用 EWMA + 当前窗口 p95 一起看。

AegisMesh 当前保留 EWMA，同时每轮重新计算 p95。这个组合比较实用：EWMA 给连续趋势，p95 给窗口尾延迟。

### Q293【深度】p95 用全部样本排序在高 QPS 下会有什么内存和 CPU 成本？

成本主要有两个。

第一是内存。Recorder 会把一个窗口内的所有 latency 都 append 到 slice。如果某个 endpoint/method 每秒 5 万请求，上报窗口 5 秒，就要保存 25 万个 latency 样本。再乘上多个 method、多个 upstream，内存会很快上去，还会增加 GC 压力。

第二是 CPU。计算 p95 时会复制一份样本再排序：

```go
values := append([]time.Duration(nil), samples...)
sort.Slice(values, ...)
```

排序复杂度是 `O(n log n)`。高 QPS 时，reporter 每个窗口都要做一次排序，可能让上报瞬间产生 CPU 尖峰。

在当前 demo 和单机实验里，这个实现没问题，简单、准确、容易验证。但生产上我不会这么做。高流量服务应该用 histogram 或 sketch 来近似分位数，把内存和 CPU 控制在固定范围内。

### Q294【深度】如何用 streaming quantile、HDR Histogram 或 DDSketch 改进 p95 计算？

可以把“保存全部样本”换成“在线摘要”。

Streaming quantile，比如 CKMS 或 t-digest，每来一个 latency 样本就更新摘要结构。查询 p95 时直接从摘要里估计。它的优点是内存远小于保存全部样本；缺点是结果是近似值，实现也比排序复杂。

HDR Histogram 适合记录延迟这类正数。它按数值范围分桶，能用固定精度记录很大的延迟范围，比如微秒到秒级。查询 p95、p99 很快，也适合导出。

DDSketch 也是一种分位数 sketch，特点是相对误差可控。对延迟这种跨数量级的数据比较友好，比如 10ms 和 10s 都能用相对误差描述。

如果我改 AegisMesh，我会优先选 histogram 或 DDSketch：

- 每个 stats row 不再保存 `[]time.Duration`。
- `Observe` 时更新 histogram/sketch。
- `SnapshotAndReset` 时读取 p95，然后 reset histogram/sketch。
- Prometheus histogram 可以继续用于 dashboard，控制面 telemetry 用同一个窗口摘要结果。

这样牺牲一点精确性，换来稳定的内存和 CPU。

### Q295【深度】SnapshotAndReset 会不会丢失正在进行请求的 latency？

按 `Start/finish` 这条路径看，不会直接丢，但会把长请求归到完成时所在的窗口。

假设一个请求在窗口 A 开始，`Start` 把 in-flight 加 1。窗口 A 结束时调用 `SnapshotAndReset()`，这个请求还没完成，所以它没有 latency 样本，只有 in-flight 会出现在快照里。reset 后，requestCount 和 latencies 清空。

请求后来在窗口 B 完成，finish 会计算从最初 start 到现在的完整 latency，然后把这个 latency 记录到当前 row。这样 latency 没丢，但它被记在窗口 B，而不是窗口 A。

这其实是大多数 RPC 统计的常见处理：完成时记账。它的好处是简单；坏处是跨窗口长请求会让完成窗口的 p95 突然变高。

当前 SDK unary interceptor 用的是 `Observe`，它只有在调用结束后才记录，所以没有“开始窗口”的概念。长请求在没结束前不会进入 Recorder 的 latency 样本，但 adaptive balancer 自己的 in-flight 会先看到堆积。

### Q296【深度】如果 Start 后业务调用 panic 或 context cancelled，finish 是否能保证执行？

`Start` 本身不能保证 finish 一定执行。它只是返回一个 finish 函数，调用方必须负责调用。

正确写法应该像这样：

```go
finish := recorder.Start(destination, method, upstream)
defer finish(status)
```

如果业务正常返回，defer 会执行。context cancelled 通常也会让 RPC 返回一个错误，defer 仍然会执行。

但如果调用方没有 defer，或者 panic 没有被恢复，finish 就可能漏掉。漏掉后 in-flight 不会减少，latency 也不会记录。

项目里 finish 内部用了 `sync.Once`，解决的是“重复调用”的问题，不解决“完全没调用”的问题。

当前 SDK unary interceptor 没有用 Start，它是在 invoker 返回后 `Observe`。如果 invoker panic，后面的 Observe 也不会执行，除非 interceptor 自己加 recover/defer。生产级 SDK 里我会把 telemetry 记录放到 defer 里，保证 panic、cancel、deadline 路径都能记录到。

### Q297【深度】以 upstream address 作为统计 key 时，服务重启导致 address 变化会怎样影响历史数据？

如果 upstream address 变化，Recorder 会把它当成一个新的统计 key。

比如原来是：

```text
user-service / GetUser / 172.18.0.5:7002
```

容器重启后变成：

```text
user-service / GetUser / 172.18.0.8:7002
```

SDK 本地会新建一行 stats，旧地址上的 EWMA、latency 样本和窗口计数不会自然迁移到新地址。

Controller 侧会尝试把 endpoint address 解析成 instance ID。先按完整 address 匹配 registry，匹配不上时还会按唯一端口兜底，比如 Docker 场景里 `172.20.0.4:7001` 可以映射到注册的 `user-a:7001`。

但这只是 Controller 的 instance 解析，本地 SDK 的 EWMA 仍然按 address 重新开始。

更稳的方案是让 resolver 地址属性里带稳定的 `instance_id`，并让 telemetry key 优先用 instance ID。address 适合连接，instance ID 适合健康历史和统计归因。

### Q298【深度】telemetry report 的粒度过细或过粗分别有什么问题？

粒度太粗，会看不出真正的问题。

如果只按 service 聚合，user-service 的 A、B 两个实例混在一起，A 慢了也会被 B 的正常请求稀释。Controller 只能知道 user-service 变慢，没法准确摘除 A。

如果不按 method 聚合，一个慢方法可能污染整个服务。比如 `Search` 很慢，但 `GetUser` 很快，合在一起会让健康判断变模糊。

粒度太细，也有问题。

如果按 trace_id、user_id、request_id 聚合，样本会变成高基数，Controller 内存和计算压力都会上升。更糟的是，每个分组样本数太少，p95、错误率、slow_score 都不稳定。

AegisMesh 当前选的是一个中间粒度：

```text
source + destination + method + upstream
```

这个粒度足够定位调用方、目标服务、方法和实例，也不会像 trace 级别那样爆炸。

### Q299【深度】如果 SDK 上报被阻塞，是否会影响业务请求路径？

正常情况下影响很小。

SDK reporter 是单独 goroutine，每 5 秒 tick 一次。它调用 `SnapshotAndReset()` 拿到窗口数据，然后通过 gRPC 上报 Controller。业务请求路径主要是在 interceptor 里调用 `Observe`，只是短时间拿一下 Recorder 的 mutex。

如果上报 RPC 本身阻塞，业务请求不会一直等它，因为 reporter 在自己的 goroutine 里阻塞。`SnapshotAndReset()` 已经完成后，锁也释放了。

但当前实现有两个工程风险。

第一，`SnapshotAndReset()` 是先重置再发送。如果发送失败，这个窗口的数据没有重试队列，等于丢了。它是 best-effort telemetry。

第二，如果窗口里数据很多，Snapshot 环节要复制和排序 p95 样本，拿锁时间可能变长。高 QPS 下会和业务请求的 `Observe` 争同一把锁。

生产上我会做三件事：

- 上报使用独立 timeout，不要用一个可能很长的 context。
- 发送失败时保留一个小的重试 buffer，或者至少记录 drop counter。
- p95 改成 histogram/sketch，缩短 Snapshot 持锁时间。

### Q300【深度】控制面接收 telemetry 时如何区分客户端观测偏差和真实 endpoint 慢？

当前实现是比较简单的：Controller 把 SDK 上报的样本转成 `EndpointSample`，按 service 内的 endpoint 做 relative scoring，比如 median/MAD，再叠加 absolute SLO、错误、in-flight 和网络信号。它没有单独建模“某个客户端自己网络差”。

这意味着有些观测偏差会影响结果。比如只有 frontend-A 到 user-B 的网络慢，其他客户端访问 user-B 都正常。如果只看 frontend-A 上报的数据，Controller 可能把 user-B 判断成慢 endpoint。

更稳的做法是保留 source 维度，并做交叉验证：

- 同一个 endpoint 是否被多个 source 同时观测到慢。
- 慢是否只出现在某个 source 到某个 endpoint 的路径上。
- eBPF 网络信号是否也支持这个判断，比如 retransmit 或 connect error 增加。
- request count 是否足够，低样本量不要直接摘除。
- 客户端本地 RT 慢，但服务端 CPU/队列正常时，可能是路径问题。

如果只有一个客户端看到慢，我会优先做局部路由调整，而不是全局 EJECTED。比如只让这个 source 降低 user-B 的权重。当前 AegisMesh 还没做到 per-source health state，这是后续可以增强的点。

## 拓展

### Q301【拓展】RED 指标和 USE 指标分别适合观测什么系统？

RED 指标主要适合请求型服务，特别是 HTTP/RPC 服务。

RED 是：

- Rate：请求速率。
- Errors：错误数或错误率。
- Duration：请求耗时。

AegisMesh 的 RPC 观测很接近 RED：`aegis_rpc_requests_total` 看 rate 和 errors，`aegis_rpc_latency_seconds` 看 duration。

USE 指标更适合资源型系统，比如 CPU、磁盘、网络、线程池、连接池。

USE 是：

- Utilization：资源利用率。
- Saturation：饱和程度，比如队列长度、等待数。
- Errors：资源层错误。

对 AegisMesh 来说，endpoint in-flight 更像 saturation，eBPF TCP retransmit/connect error 更像网络层 errors。CPU throttle、连接池等待数、队列长度也适合放进 USE 视角。

我一般会这样讲：RPC 层看 RED，资源层看 USE。慢故障治理要把两者合起来，否则只能看到“请求慢”，看不到“为什么慢”。

### Q302【拓展】Prometheus counter、gauge、histogram、summary 的区别是什么？

counter 是只增不减的计数器。适合请求总数、错误总数、重试总数。查询时通常用 `rate()` 算每秒速率。AegisMesh 的 `aegis_rpc_requests_total` 就是 counter。

gauge 是可增可减的当前值。适合 in-flight、当前连接数、slow_score、endpoint state。AegisMesh 的 `aegis_endpoint_inflight`、`aegis_endpoint_latency_ewma_seconds`、`aegis_endpoint_slow_score` 都是 gauge。

histogram 是服务端按 bucket 统计分布。适合 latency。Prometheus 可以用 `histogram_quantile()` 从 bucket 估算 p95/p99。AegisMesh 的 `aegis_rpc_latency_seconds` 是 histogram。

summary 也是分位数统计，但分位数通常在客户端计算。它的缺点是跨实例聚合困难。比如每个实例各自算 p95，不能简单平均成全局 p95。

所以 RPC latency 我更偏向 histogram。它牺牲一点精度，但更适合 Prometheus 聚合。

### Q303【拓展】高基数 label 为什么危险？项目里的 source、destination、method、upstream 是否有高基数风险？

高基数 label 会让时间序列数量爆炸。

Prometheus 的每组 label 都是一条独立时间序列。如果把 `user_id`、`trace_id`、`request_id` 放进 label，每个请求都可能创建新序列，内存、索引、查询都会被打爆。

AegisMesh 当前 label 有：

```text
source, destination, method, upstream, status
```

这些 label 有一定风险，但在 demo 和中小规模服务里是可控的。

- `source`：调用方服务数量通常有限。
- `destination`：下游服务数量有限。
- `method`：gRPC 方法数量有限，但大型系统里也可能不少。
- `upstream`：风险最大。实例频繁扩缩容、容器 IP 频繁变化，会让 upstream 序列不断增加。
- `status`：gRPC code 种类有限，风险低。

控制方法有几个：

- upstream 尽量使用稳定 instance ID，而不是频繁变化的临时地址。
- 不把 trace_id、user_id 放进 metric label。
- 对 method 做白名单或规范化，避免动态 path。
- 给 Prometheus 设置合理 retention。
- 对高基数排查用 trace/log，不要强塞进 metric。

### Q304【拓展】OpenTelemetry trace、metric、log 三者如何联动？

metric 告诉你“整体发生了什么”。比如 user-service p99 变高、错误率上升、slow_score 升高。

trace 告诉你“某个请求经过了哪里”。比如一次 checkout 请求先调 user-service，再调 order-service，其中 user-service 的第 2 次 attempt 打到了 `user-b`。

log 告诉你“某个点发生了什么事件”。比如 order-service 创建订单失败，错误原因是库存不足，或者 Controller 把 endpoint 从 `DEGRADED` 切到 `EJECTED`。

三者最好用同一个 trace context 串起来。AegisMesh 已经在 SDK 里注入了类似这样的 metadata：

- `x-aegis-trace-id`
- `x-aegis-span-id`
- `x-aegis-attempt`
- `x-aegis-upstream-id`
- `x-aegis-route-revision`

这样 dashboard 发现 p99 异常后，可以点进 trace 看真实路径，再用日志看状态迁移和业务错误。metric 负责发现问题，trace 负责定位路径，log 负责解释细节。

### Q305【拓展】tail-based sampling 和 head-based sampling 分别适合什么场景？

head-based sampling 是请求一开始就决定采不采样。

优点是实现简单，开销可控。缺点是它不知道这个请求后面会不会变慢、会不会失败，所以可能把最有价值的异常请求丢掉。

tail-based sampling 是请求结束后再决定采不采样。

它可以根据结果做选择，比如只保留错误请求、p99 请求、重试次数多的请求、经过 EJECTED/PROBING endpoint 的请求。缺点是系统要先暂存 trace，内存和实现复杂度更高。

对 AegisMesh 来说，tail-based sampling 更适合故障分析。因为我们最关心的是慢请求、重试请求、被治理策略影响的请求。但当前实现用 JSONL trace 已经够验证 verifier。要做生产化，再考虑接 OpenTelemetry collector 和 tail sampling。

### Q306【拓展】如何设计指标以支持 SLO burn rate 告警？

先要把 SLO 定义成可计算的 SLI。

比如：

```text
99% 的 checkout 请求在 300ms 内成功完成
```

那 bad event 可以定义为：

- status 不是 OK。
- latency 大于 300ms。

Prometheus 里需要有两个计数：

```text
total_requests
bad_requests
```

error budget burn rate 大概是：

```text
(bad_requests / total_requests) / allowed_error_rate
```

如果 SLO 是 99%，允许错误率是 1%。最近 5 分钟 bad rate 是 5%，burn rate 就是 5 倍。

告警一般用多窗口组合。比如：

- 5 分钟高 burn rate，抓快速故障。
- 1 小时或 6 小时中等 burn rate，抓持续退化。

AegisMesh 已经有 status 和 latency histogram，可以做这类指标。还可以把 slow_score、endpoint state 作为辅助告警：如果 p99 burn rate 上升，同时某个 endpoint slow_score 上升，排查会快很多。

### Q307【拓展】如何把 RPC metrics 和 kernel TCP metrics 做时间对齐？

要解决两个问题：时间窗口一致，endpoint 身份一致。

时间窗口上，RPC telemetry 有 `window_start_unix_millis` 和 `window_end_unix_millis`。eBPF TCP telemetry 也应该按相同窗口聚合，或者至少带事件时间戳。进入 Controller 后，统一按 5 秒或 10 秒窗口对齐。

endpoint 身份上，RPC 看到的是 upstream address，eBPF 看到的是 remote IP/port。需要一个 mapping，把 `ip:port` 映射到 service/instance。AegisMesh 现在已经有 endpoint map，后续更好的方式是 agent 自动从 Controller 拉 registry。

对齐后可以这样分析：

- RPC p95 上升，同时 TCP retransmit 上升：可能是网络慢故障。
- RPC p95 上升，但 TCP 正常，CPU/队列异常：可能是应用处理慢。
- connect error 上升，同时 `UNAVAILABLE` 上升：可能是连接层或实例不可达。

注意 scrape 时间和事件时间不一样。Prometheus scrape 晚一点不代表事件晚一点，所以控制面最好用样本自带的 window timestamp，而不是只用收到数据的时间。

### Q308【拓展】如果 telemetry 数据丢失，治理系统应该 fail-safe 还是继续使用旧状态？

我会用“有时间边界地使用旧状态”。

完全 fail-open，也就是 telemetry 一丢就把所有 endpoint 当健康，会让刚发生的慢故障被忘掉。完全 fail-closed，也就是 telemetry 一丢就拒绝或摘除 endpoint，又会因为观测系统故障影响业务。

比较稳的做法是：

1. 短时间内沿用旧状态。
2. 给 health state 加 staleness TTL。
3. 超过 TTL 后，不再继续升级惩罚。
4. 对 EJECTED 这类强状态设置最长保持时间，避免因为 telemetry 丢失一直不恢复。
5. resolver 侧保留最后一次可用地址列表，Controller 不可达时继续用缓存。

当前 AegisMesh reporter 是 best-effort 的：`SnapshotAndReset()` 后上报，如果 RPC 失败，这个窗口数据不会自动重放。HealthManager 也没有完整的 telemetry staleness 机制。当前实现可以接受，因为实验环境可控；生产化时必须补。

面试里我会主动说这个边界：AegisMesh 当前的 telemetry 适合证明闭环和实验效果，但如果做成生产系统，需要给上报失败、旧状态过期、Controller 不可达都设计明确退化策略。
