# 12. Prometheus、Grafana 与可观测性

## 简单

### Q309【简单】项目导出了哪些 SDK 侧 Prometheus 指标？

SDK 侧指标在 `pkg/telemetry/prometheus.go` 里定义，主要有四个。

第一个是请求总数：

```text
aegis_rpc_requests_total{source,destination,method,upstream,status}
```

这是 counter。它记录 SDK 观察到的 RPC 调用次数，label 里带调用方、目标服务、方法、真实 upstream 和 gRPC status。用 `rate()` 可以看每秒请求量，也可以按 status 算错误率。

第二个是 RPC latency histogram：

```text
aegis_rpc_latency_seconds_bucket{source,destination,method,upstream,status}
```

这是 histogram。Grafana 里用它算 p99：

```promql
histogram_quantile(
  0.99,
  sum by (le, destination, upstream) (
    rate(aegis_rpc_latency_seconds_bucket[5m])
  )
)
```

第三个是当前 in-flight：

```text
aegis_endpoint_inflight{source,destination,method,upstream}
```

这是 gauge。它表示某个调用方到某个 upstream 的当前未完成 RPC 数。

第四个是 endpoint EWMA latency：

```text
aegis_endpoint_latency_ewma_seconds{source,destination,method,upstream}
```

这也是 gauge。它记录 SDK 本地计算出来的 EWMA 延迟。

这些指标由 demo frontend 的 `/metrics` 暴露。默认 demo 里是：

```text
http://127.0.0.1:8080/metrics
```

实验 compose 里还有 `frontend-direct:8081`、`frontend-round-robin:8082`、`frontend-adaptive:8083`、retry 相关 frontend 等目标。

### Q310【简单】Controller 侧导出了哪些 endpoint health 指标？

Controller 侧指标在 `pkg/fault/prometheus.go` 里定义，主要有两个。

第一个是慢故障分数：

```text
aegis_endpoint_slow_score{service,instance,endpoint}
```

它是 gauge，表示 Controller 当前认为某个 endpoint 的 slow_score 是多少。这个值来自 SDK 上报的 telemetry window，里面包含 p95、EWMA、错误数、超时数、in-flight 和网络信号。

第二个是 endpoint 状态：

```text
aegis_endpoint_state{service,instance,endpoint,state}
```

它也是 gauge，用 one-hot 方式编码状态。比如某个实例当前是 `EJECTED`，那：

```text
aegis_endpoint_state{state="EJECTED"} = 1
aegis_endpoint_state{state="HEALTHY"} = 0
aegis_endpoint_state{state="DEGRADED"} = 0
aegis_endpoint_state{state="PROBING"} = 0
aegis_endpoint_state{state="DEAD"} = 0
```

Controller 默认在独立 HTTP 端口暴露 metrics：

```text
http://127.0.0.1:9100/metrics
```

这和 Controller 的 gRPC 端口 `9000` 分开，避免业务控制面 RPC 和 metrics scrape 混在同一个监听端口里。

### Q311【简单】Grafana dashboard 中哪些面板最能证明治理效果？

当前 dashboard 在 `dashboard/grafana/aegismesh-overview.json`，标题是 `AegisMesh Overview`。它有六个面板：

- `RPC throughput by destination`
- `p99 RPC latency`
- `Endpoint EWMA latency`
- `Endpoint slow score`
- `Endpoint state`
- `In-flight RPCs`

最能说明治理效果的是这几类组合。

第一，看 `p99 RPC latency`。慢实例故障注入后，如果 adaptive P2C 生效，用户侧 p99 应该比 round-robin 低很多。项目实验里这个结论已经在 CSV 报告里给出：慢实例场景下 p99 从 348.682 ms 降到 32.712 ms。

第二，看 `Endpoint slow score`。故障 endpoint 的 slow_score 应该升高，健康 endpoint 不应该一起升高太多。这样能说明 Controller 不是盲目摘实例，而是发现了具体慢点。

第三，看 `Endpoint state`。好的演示不是只看分数，而是看状态从 `HEALTHY` 进入 `DEGRADED`、`EJECTED`、`PROBING`，故障恢复后回到 `HEALTHY`。

第四，看 `In-flight RPCs` 和按 upstream 的请求分布。慢 endpoint 如果请求堆积，in-flight 会先升高；adaptive P2C 生效后，它的普通流量应该下降。

面试现场我会先讲 p99，因为它直接对应用户体验；然后用 slow_score/state/in-flight 解释系统为什么能把 p99 降下来。

### Q312【简单】aegis_endpoint_state 这类状态指标应该如何编码？

项目里用 one-hot gauge 编码。

每个 endpoint 对每个可能状态都有一条时间序列：

```text
aegis_endpoint_state{service="user-service",instance="user-b",state="HEALTHY"}
aegis_endpoint_state{service="user-service",instance="user-b",state="DEGRADED"}
aegis_endpoint_state{service="user-service",instance="user-b",state="EJECTED"}
aegis_endpoint_state{service="user-service",instance="user-b",state="PROBING"}
aegis_endpoint_state{service="user-service",instance="user-b",state="DEAD"}
```

当前状态对应的那条值为 1，其他状态为 0。

这样做比把状态编码成数字更适合 Prometheus。假如用数字：

```text
HEALTHY=0
DEGRADED=1
EJECTED=2
PROBING=3
```

Grafana 里看起来像连续数值，但状态本身没有大小关系。`PROBING=3` 并不表示比 `EJECTED=2` 更严重，它只是另一个离散状态。

one-hot 的好处是查询直观：

```promql
aegis_endpoint_state{state="EJECTED"} == 1
```

这个表达式能直接找出被摘除的 endpoint。

### Q313【简单】为什么要同时看 p99 latency、slow_score、state 和 inflight？

因为它们回答的问题不一样。

`p99 latency` 回答用户是否真的变慢。它是最接近用户体验的指标。慢故障最怕的就是平均值看起来还行，但尾延迟已经很差。

`slow_score` 回答 Controller 是否识别到了异常。它把 latency、error、in-flight、网络信号合成一个分数。

`state` 回答控制面采取了什么动作。slow_score 升高只是判断，`DEGRADED/EJECTED/PROBING` 才是治理闭环里的状态变化。

`inflight` 回答请求是否正在堆积。慢请求还没完成时，p95 和 p99 可能还没更新，但 in-flight 会先升高。

只看一个指标容易误判。比如 p99 降了，可能是服务恢复了，也可能是流量被路由走了；slow_score 高，可能是短暂抖动，也可能是真的慢故障。把这几个指标放在一起，才能判断“发现、降权、摘除、恢复”是不是都发生了。

### Q314【简单】Prometheus scrape config 在本地 Demo 中起什么作用？

scrape config 告诉 Prometheus 去哪里拉指标、多久拉一次。

本地非 compose 配置在：

```text
dashboard/prometheus/prometheus.yml
```

它默认每 5 秒 scrape：

```yaml
scrape_interval: 5s
```

目标是：

```yaml
- 127.0.0.1:9100
- 127.0.0.1:8080
```

也就是 Controller metrics 和 demo frontend metrics。

compose 配置在：

```text
dashboard/prometheus/prometheus.compose.yml
```

它用容器服务名作为 target，比如：

```yaml
- controller:9100
- frontend:8080
- frontend-adaptive:8083
- frontend-retry-budgeted:8086
```

这个配置的作用很简单：Prometheus 不知道 AegisMesh 的服务在哪里，scrape config 就是它的地址簿。没有它，Grafana dashboard 里不会有数据。

### Q315【简单】metrics endpoint 与业务 HTTP endpoint 放在同一进程有什么利弊？

demo frontend 当前是同一个 HTTP server 同时挂：

```text
/checkout
/metrics
/healthz
```

好处是简单。一个进程、一个端口，部署和本地演示都方便。指标也和业务进程同生命周期，服务启动后 `/metrics` 自然可用。

坏处也明显。

第一，metrics scrape 和业务请求共享同一个 HTTP server。正常情况下 scrape 很轻，但如果指标很多、label 基数很高，抓取 `/metrics` 也会消耗 CPU 和内存。

第二，安全边界弱。业务端口如果暴露到外部，`/metrics` 也可能一起暴露出去。指标里可能有服务名、方法名、endpoint 地址，这些不一定适合公开。

第三，服务过载时 metrics 也可能跟着变慢。最需要排障的时候，`/metrics` 反而可能 scrape 不到。

Controller 做得稍微好一点：gRPC 控制面默认在 `9000`，metrics 默认在 `9100`。生产里我更倾向于把 metrics 端口绑定到内网，或者只允许 Prometheus 所在网络访问。

### Q316【简单】Dashboard 如何帮助你在面试中现场演示？

Dashboard 适合讲“动态过程”。

我会这样演示：

1. 先 `make dashboard` 启动 demo、Prometheus 和 Grafana。
2. 打开 `http://127.0.0.1:3000`。
3. 对 `frontend-adaptive` 持续打流量。
4. 对 `aegis-user-b` 注入 delay 或 CPU throttle。
5. 看 p99、EWMA、slow_score、state 和 in-flight 的变化。

如果系统正常，面板上会看到几个现象：

- 慢 endpoint 的 EWMA 或 p99 上升。
- Controller 侧 slow_score 上升。
- state 从 `HEALTHY` 进入异常状态。
- 正常 endpoint 承接更多请求，慢 endpoint 普通流量减少。
- 故障解除后，endpoint 进入探测并恢复。

面试现场最怕只讲架构图。Dashboard 能把“我实现了治理闭环”变成一条时间线。

不过我也会提醒一句：正式结论还是以实验 CSV 和报告为准。Dashboard 用来现场观察，实验报告用来给量化数据。

### Q317【简单】如果指标没有数据，排查顺序是什么？

我会按从近到远排。

第一，直接访问 metrics endpoint：

```bash
curl http://127.0.0.1:9100/metrics
curl http://127.0.0.1:8080/metrics
```

如果这里都没有数据，先看进程有没有启动、端口有没有暴露、日志有没有报 Prometheus collector 注册错误。

第二，看 Prometheus targets：

```text
http://127.0.0.1:9090/targets
```

确认 `aegis-controller`、`aegis-demo-frontend` 或实验 frontends 是 `UP`。

第三，确认有没有真实流量。SDK 指标不是凭空出现的，要先访问 `/checkout`。没有请求，就没有 `aegis_rpc_requests_total`。

第四，看是不是走了 SDK。`frontend-direct` 用的是原生 `grpc.Dial`，不会产生 Aegis SDK 的 resolver、balancer、telemetry 指标。要看治理指标，应该打 `frontend` 或 `frontend-adaptive`。

第五，看 Grafana 时间范围。默认 dashboard 看最近 15 分钟，如果实验很短或刚切换时间范围，可能只是窗口没覆盖。

第六，看 label。比如 dashboard 按 `destination`、`upstream` 分组，如果查询条件写错，数据存在但面板为空。

最后再看容器网络。compose 里 Prometheus target 是 `controller:9100`，不是 `127.0.0.1:9100`。容器内访问 localhost 指的是 Prometheus 自己。

### Q318【简单】如何判断 adaptive P2C 是否真的降低了慢实例流量？

不能只看整体 p99。整体 p99 降了，只能说明用户体验好了，不一定说明慢实例流量减少。

我会看三类证据。

第一，看按 upstream 分组的请求速率：

```promql
sum by (upstream) (
  rate(aegis_rpc_requests_total{destination="user-service"}[1m])
)
```

在 round-robin 下，两个 user-service 实例的流量应该比较接近。注入故障后，round-robin 仍然会继续打慢实例。

在 adaptive P2C 下，慢实例的 `upstream` 请求速率应该下降，健康实例的请求速率上升。

第二，看 slow endpoint 的 in-flight 和 latency。慢实例一开始可能 in-flight 升高，随后因为路由避让，普通流量下降。

第三，看 trace JSONL 的 route distribution。项目已经有真实 SDK trace verifier，能统计请求到底打到了哪个 upstream。这比只看 dashboard 更适合写实验结论。

所以我的判断标准是：p99 降低，加上慢 upstream 的请求占比下降，再加上 trace 或 metrics 的 route distribution 证据。

## 深度

### Q319【深度】Prometheus pull 模型对短生命周期任务有什么问题？

Prometheus 是 pull 模型。它按 scrape interval 定期去目标拉 `/metrics`。

短生命周期任务的问题是，它可能在两次 scrape 之间启动又退出。Prometheus 没来得及拉，就完全看不到这次任务的数据。

比如 scrape interval 是 5 秒，一个实验 helper 只跑 1 秒就结束。它暴露过 `/metrics`，但 Prometheus 没碰上，结果就是零数据。

解决办法看场景。

如果是长期服务，比如 Controller、frontend、user-service，用 pull 模型很好。

如果是 batch job，可以考虑：

- 让任务运行时间覆盖至少一个 scrape 周期。
- 使用 Pushgateway，但要小心清理旧指标。
- 用 OpenTelemetry Collector 或日志文件收集任务结果。
- 让实验脚本直接产出 CSV，不依赖 Prometheus scrape。

AegisMesh 当前实验结果主要来自实验脚本和 CSV，Prometheus/Grafana 用于观察运行时过程。这是合理的，因为 benchmark 结论不应该依赖短命任务是否刚好被 scrape 到。

### Q320【深度】Histogram bucket 设计不合理会如何影响 p99 估算？

Prometheus histogram 的 p99 是从 bucket 估算出来的，不是精确排序样本。

如果 bucket 太粗，p99 会很不准。比如真实 p99 在 120ms，但 bucket 只有 100ms 和 250ms，`histogram_quantile` 只能在这两个边界之间插值。

如果最大 bucket 太小，很多请求都落到 `+Inf`，p99 会失去解释价值。你只知道“超过最大 bucket”，但不知道到底是 1 秒还是 10 秒。

如果 bucket 太密，精度变高，但时间序列数量也会变多。每个 bucket 都带上 `source/destination/method/upstream/status` 这些 label，bucket 多了会放大 Prometheus 存储和查询压力。

项目当前用的是 `prometheus.DefBuckets`，适合 demo，但不一定适合所有 RPC 延迟。生产里我会按 SLO 设计 bucket。比如目标是 150ms，就应该在 50ms、100ms、150ms、200ms、300ms、500ms 附近有更细的 bucket。

### Q321【深度】endpoint label 如果使用 IP:port，会不会导致 time series churn？

会。

`upstream` label 当前是实际地址，比如：

```text
172.18.0.5:7002
```

容器重启、Pod 重建、IP 漂移后，地址可能变成：

```text
172.18.0.8:7002
```

Prometheus 会把它当成一条新的时间序列。旧序列变 stale，新序列开始增长。这就是 time series churn。

churn 的问题有两个。

第一，存储和索引压力会变大。实例越频繁重建，历史时间序列越多。

第二，历史趋势断裂。Grafana 里看 `user-b`，如果只用 IP:port，很难把重启前后的同一个逻辑实例连起来。

AegisMesh Controller 侧已经有 `instance` label，因为它能把 endpoint address 解析回 registry instance ID。SDK metrics 侧还主要用 upstream address。更稳的做法是让 SDK metrics 也带稳定 `instance_id`，或者通过 Prometheus relabel / Controller registry 做 address 到 instance 的映射。

短期 demo 用 IP:port 可以接受。生产环境里，我会优先使用稳定 instance ID、Pod name 或 workload identity。

### Q322【深度】如何从指标中区分“服务真的变快”和“流量被路由走了”？

要同时看 latency 和 request rate。

如果服务真的变快，通常会看到这个 endpoint 的请求量没有明显下降，但它自己的 latency、EWMA、slow_score 都下降。

如果是流量被路由走了，通常会看到慢 endpoint 的 request rate 明显下降，健康 endpoint 的 request rate 上升。慢 endpoint 自己的 latency 可能仍然很高，只是它收到的请求少了，用户整体 p99 被拉低。

PromQL 可以这样看每个 upstream 的流量：

```promql
sum by (upstream) (
  rate(aegis_rpc_requests_total{destination="user-service"}[1m])
)
```

再看每个 upstream 的 p99：

```promql
histogram_quantile(
  0.99,
  sum by (le, upstream) (
    rate(aegis_rpc_latency_seconds_bucket{destination="user-service"}[5m])
  )
)
```

如果慢 upstream 流量掉了，而 p99 仍高，那是路由避让。如果慢 upstream 流量没掉，但 p99 降了，那更像服务本身恢复。

再结合 `aegis_endpoint_state`，可以判断是不是 Controller 把 endpoint 标成了 `DEGRADED/EJECTED/PROBING`。

### Q323【深度】只看平均延迟为什么可能掩盖慢故障？

慢故障影响的是尾部，不一定影响均值到很夸张。

举个例子，100 个请求里 95 个是 10ms，5 个是 1000ms。平均延迟是：

```text
(95 * 10 + 5 * 1000) / 100 = 59.5ms
```

59.5ms 看起来不算太差。但 p95 或 p99 会直接落到 1000ms 这一档，用户里最慢的一部分已经很痛苦。

还有一个问题是聚合会稀释。两个 endpoint 里只有一个慢，如果只看服务平均延迟，健康 endpoint 的大量快请求会把慢 endpoint 的问题盖住。

所以 AegisMesh 不把平均延迟作为核心故障信号，而是看 p95、p99、EWMA、in-flight 和 slow_score。慢故障治理的目标不是让平均值好看，而是把尾延迟压下去。

### Q324【深度】如果 Grafana 显示 state 抖动，你会检查哪些模块？

我会按控制闭环查。

第一，看 telemetry 输入是否抖。检查 `aegis_rpc_latency_seconds`、EWMA、in-flight、错误率是不是在阈值附近来回跳。如果 QPS 很低，p95 样本少，state 抖动很可能是采样不足。

第二，看 slow_score 计算。确认 absolute SLO、median/MAD、error score、in-flight score 有没有把噪声放大。尤其是实例数量少时，相对异常值更容易不稳定。

第三，看状态机阈值。`DegradedThreshold`、`EjectThreshold`、`RecoveryThreshold` 如果太接近，就容易 HEALTHY 和 DEGRADED 来回切。`ConsecutiveWindows` 太小也会让短暂抖动直接触发迁移。

第四，看 `EjectionDuration` 和 `ProbeSuccessThreshold`。如果 ejection 太短，endpoint 刚被摘掉就进入 PROBING；如果 probe 成功阈值太低，可能很快回到 HEALTHY，然后又被打慢。

第五，看 resolver 和 balancer。resolver refresh、telemetry report、state-machine tick 不同步时，Grafana 上可能看到 state 已变，但客户端还在用旧地址一小段时间。

最后看 endpoint mapping。容器地址变化、instance ID 解析不稳定，会让同一个物理实例被当成多个 endpoint，状态看起来像抖动。

### Q325【深度】如何设计一个面板证明 retry budget 减少了放大效应？

最理想的面板是直接画 amplification：

```text
total_downstream_attempts / original_user_requests
```

项目现有 Prometheus 指标能看到 SDK 侧 RPC attempts：

```promql
sum by (instance) (
  rate(aegis_rpc_requests_total{destination="retry-user-service"}[1m])
)
```

实验 compose 里有：

- `frontend-retry-unbudgeted:8084`
- `frontend-retry-off:8085`
- `frontend-retry-budgeted:8086`

在相同压测输入下，unbudgeted 变体的 downstream attempts 应该接近 2 倍，budgeted 变体应该接近 1.15 倍。

但我要说清楚：当前 SDK Prometheus 指标没有单独导出 `retry_attempts_total` 或 `retry_budget_denied_total`。所以 dashboard 只能通过“相同原始负载下的下游请求速率”做近似对比。

更完整的面板应该补三个指标：

```text
aegis_retry_original_requests_total
aegis_retry_attempts_total
aegis_retry_budget_denied_total
```

然后直接画：

```promql
rate(aegis_retry_attempts_total[1m])
/
rate(aegis_retry_original_requests_total[1m])
```

项目的正式 retry 结论现在来自实验 CSV：without budget 是 2.000x，with budget 是 1.150x。Dashboard 可以辅助观察，量化结论以实验报告为准。

### Q326【深度】如何设计一个面板证明 PROBING 流量被限制？

要证明 PROBING 流量被限制，需要同时画状态和流量占比。

第一条曲线是目标 endpoint 是否处于 PROBING：

```promql
aegis_endpoint_state{service="user-service",instance="user-b",state="PROBING"}
```

第二条曲线是这个 endpoint 的请求占比：

```promql
sum(rate(aegis_rpc_requests_total{destination="user-service",upstream=~".*:7002"}[1m]))
/
sum(rate(aegis_rpc_requests_total{destination="user-service"}[1m]))
```

如果 PROBING 生效，这个占比应该很低，接近配置的 probe ratio，而不是马上恢复到 50%。

当前项目实验已经用 trace JSONL 做过更精确的验证：PROBING endpoint traffic 是 0.2177%，低于 10% 的检查上限。

Dashboard 面板要做得更稳，需要让 SDK metrics 的 upstream label 和 Controller health 的 instance label 能直接关联。现在一个是 address，一个是 instance ID。短期可以按端口或地址匹配；长期应该把稳定 instance ID 加到 SDK metric label 里。

### Q327【深度】Controller 指标和 SDK 指标时间戳不一致时如何分析？

先接受一个事实：Prometheus 里的时间戳通常是 scrape 时间，不一定是事件发生时间。

SDK 指标是在 frontend 进程里记录的，Prometheus 可能 5 秒 scrape 一次。Controller 指标来自 telemetry report、HealthManager update 和 health tick，再由 Prometheus scrape。两条路径天然会有延迟。

分析时不要逐秒硬对齐，而要看时间窗口。

比如：

- SDK p99 在 10:00:05 左右升高。
- SDK reporter 每 5 秒上报一次。
- Controller 处理后 slow_score 在 10:00:10 左右升高。
- state 可能再经过 consecutive windows 才变化。

这不一定是 bug，而是闭环延迟。

如果要精确分析，应该看带窗口时间的 telemetry report 或实验 `recovery.csv`，而不是只看 Prometheus scrape 点。单机环境没有明显时钟偏差，多机环境还要考虑 NTP 和节点 clock skew。

面试里可以这样说：dashboard 用来观察趋势，恢复时间和状态迁移时间以实验 recorder 的 CSV 为准。

### Q328【深度】生产环境中 metrics 暴露端口如何做安全控制？

第一，metrics 端口不要暴露到公网。最基本的是 bind 到内网地址，或者只在 Kubernetes cluster 内可访问。

第二，用网络策略限制访问来源。只有 Prometheus、OpenTelemetry Collector 或监控网段能访问 `/metrics`。

第三，必要时加 TLS 或 mTLS。尤其是跨集群、跨 VPC scrape 时，不能裸 HTTP 直接拉。

第四，控制 label 内容。不要把用户 ID、token、请求参数、订单号放进 metric label。metrics endpoint 经常被很多系统抓取，敏感信息一旦进 label，很难清理。

第五，防止高基数 DoS。恶意流量如果能制造大量 label 值，比如动态 method 或 upstream，就可能把 Prometheus 打爆。

第六，区分业务端口和观测端口。Controller 已经把 gRPC 和 metrics 分成 9000/9100。frontend demo 为了简单把 `/checkout` 和 `/metrics` 放在同一个 server，生产里我会拆端口或者放到 sidecar 代理后面做访问控制。

## 拓展

### Q329【拓展】Prometheus federation、remote write 和 long-term storage 适合什么规模？

单机 demo 只需要一个 Prometheus。AegisMesh 当前就是这个级别，`make dashboard` 启一个 Prometheus 和一个 Grafana 就够了。

多服务、多命名空间时，可以用一个集群内 Prometheus scrape 本地服务，再用 recording rules 做聚合。

多集群或多地域时，federation 可以把每个集群的聚合指标拉到上层 Prometheus。它适合拉低基数、聚合后的指标，不适合把所有原始高基数序列都拉上来。

长期存储用 remote write 更合适。Prometheus 本地保留几天或几周，远端写入 Thanos、Cortex、Mimir、VictoriaMetrics 这类系统，用来做长期查询和跨集群视图。

我会这样分层：

```text
local Prometheus: 负责 scrape 和短期查询
recording rules: 负责本地聚合
remote write: 负责长期存储
global query layer: 负责跨集群查询
```

AegisMesh 的高基数 endpoint 指标不应该无脑全量 federation。更好的做法是本地保留细粒度，向上只传 service/method 级聚合。

### Q330【拓展】Grafana dashboard 和自动化实验报告之间如何避免指标口径不一致？

要先承认两者口径本来就可能不同。

Grafana 的 p99 来自 Prometheus histogram：

```promql
histogram_quantile(0.99, ...)
```

实验报告里的 p99 通常来自压测脚本记录的客户端 latency CSV。一个是 SDK 侧 RPC latency，一个是 HTTP checkout 端到端 latency；一个是 histogram 估算，一个可能是原始样本分位数。它们不能直接混用。

避免口径不一致，我会做几件事。

第一，在文档里明确每个指标的来源。比如“dashboard p99 是 SDK RPC p99”，“evaluation 表格 p99 是 load generator 看到的 checkout p99”。

第二，把 PromQL 固化在 dashboard JSON 或 recording rules 里，不要每次手写。

第三，实验脚本输出 CSV 时也写清楚字段含义，比如 `latency_p99_ms` 是 HTTP 请求 p99 还是 RPC p99。

第四，报告里引用 dashboard 图时，标注查询表达式和时间窗口。

第五，用测试保护 dashboard 关键查询。项目里已经有 `pkg/dashboard/dashboard_test.go`，会检查 dashboard JSON 包含关键指标名。

这样 dashboard 和报告可以互相解释，但不互相替代。

### Q331【拓展】如果要接入 OpenTelemetry Collector，需要改造哪些组件？

要改几个点。

SDK 侧，当前 metrics 用 Prometheus client，trace 用 JSONL writer。接入 OTel 后，可以把 RPC metrics 改成 OTel Meter，把 trace metadata 改成 W3C TraceContext 或兼容 OpenTelemetry 的 span。

Controller 侧，当前 health metrics 也是 Prometheus client。可以继续暴露 Prometheus，也可以让 OTel Collector scrape Prometheus endpoint。更进一步，可以直接用 OTLP exporter 把 health metrics 发给 Collector。

Trace 侧，当前 verifier 能读 JSONL。接入 OTel 后，可以有两条路：

- 继续保留 JSONL，作为实验和 verifier 输入。
- 同时把 trace 发到 OTel Collector，用 tail sampling 和 trace UI 做排查。

eBPF agent 侧，可以把 TCP retransmit、connect error、connect latency 转成 OTel metrics，或者继续走 Controller telemetry path。两者都可以，但要保证 endpoint mapping 一致。

部署侧要加 Collector 配置：

```text
receivers: otlp, prometheus
processors: batch, memory_limiter, tail_sampling
exporters: prometheusremotewrite, logging, otlp
```

我不会一次性把 Prometheus 全替换掉。更实际的做法是：先让 OTel Collector scrape 现有 Prometheus endpoint，再逐步把 SDK trace 接到 OTLP。

### Q332【拓展】高频控制指标是否适合 Prometheus，还是应该用专门的流式系统？

高频控制指标不适合直接依赖 Prometheus。

Prometheus 默认是秒级 scrape，适合观测、告警和趋势分析。它不适合做亚秒级控制回路，也不适合在请求路径里实时查询。

AegisMesh 的治理闭环没有把 Prometheus 放进控制路径。SDK telemetry 是通过 gRPC 上报 Controller，Controller 计算 slow_score 和状态；Prometheus 只是观察这些结果。

如果需要更高频的控制信号，比如每 100ms 更新一次 endpoint congestion，就应该用专门的数据通道：

- gRPC streaming
- NATS / Kafka
- eBPF ring buffer 到本地 agent
- 内存共享或 sidecar 本地 API

Prometheus 可以继续抓取这些系统的聚合结果，但不要让 balancer 每次 Pick 都去查 Prometheus。那会把观测系统变成请求路径上的依赖。

### Q333【拓展】如何做多维度 SLO：按服务、方法、调用方、区域？

我会先分主 SLO 和诊断维度。

主 SLO 不宜太多。比如：

```text
checkout-service / Checkout
99% requests < 300ms
success rate >= 99.9%
```

这是对用户承诺的指标。

诊断维度可以更多：

- `service`
- `method`
- `source`
- `region`
- `zone`
- `upstream`
- `tenant`

但不能把所有维度都拿来做强告警，否则会出现大量低流量组合，误报很多。

比较稳的做法是：

1. 服务级 SLO 做主告警。
2. 方法级 SLO 做定位。
3. 区域级 SLO 用于判断是否是局部故障。
4. 调用方维度用于识别某个上游打出的异常流量。
5. endpoint 维度用于治理，不直接作为用户承诺。

AegisMesh 当前已经有 `source/destination/method/upstream`，后续如果要支持 region/tenant，可以放进 policy 和 telemetry label，但要控制基数。

### Q334【拓展】如何避免观测系统本身成为治理系统的瓶颈？

第一，业务请求路径不能同步等待观测系统。AegisMesh 的 Prometheus 是 pull，SDK telemetry reporter 也是后台 goroutine，这个方向是对的。

第二，本地聚合后再上报。SDK 不把每个请求都发给 Controller，而是按窗口聚合成 request count、error count、p95、EWMA、in-flight。

第三，限制高基数。不要把 trace ID、用户 ID、订单 ID 放进 metric label。高基数会拖垮 Prometheus，也会让 Controller 做无意义的分组。

第四，上报失败要有边界。不能因为 Controller 或 Prometheus 不可用就阻塞业务。可以丢弃、降采样、保留小 buffer，但不能无限堆内存。

第五，计算成本要稳定。当前 Recorder p95 保存全部样本并排序，demo 没问题；高 QPS 下应该换成 histogram 或 sketch。

第六，监控和治理要分开。Prometheus 用于观察，Controller telemetry 用于决策。不要让数据面 balancer 去查 Grafana 或 Prometheus。

### Q335【拓展】告警应该基于 slow_score、state、p99、error rate 还是组合条件？

我会用组合条件。

只看 p99，能发现用户慢，但不知道是哪个 endpoint 或哪类故障。

只看 slow_score，可能发现了局部慢点，但不一定已经影响用户体验。

只看 state，可能会漏掉还没进入状态迁移的早期故障。

只看 error rate，对 fail-slow 不够敏感。慢故障经常是连接没断、错误不多，但延迟很高。

比较实用的告警分两层。

用户影响告警：

```text
p99 SLO burn rate 高
或 error rate 高
```

治理动作告警：

```text
endpoint_state{state="EJECTED"} == 1 持续超过 N 分钟
或 slow_score 持续高于阈值
或 PROBING 长时间无法恢复
```

这样值班时先知道用户是否受影响，再知道治理系统正在做什么。slow_score 更适合做根因辅助和自动治理信号，不适合单独作为所有告警的入口。

### Q336【拓展】如果 dashboard 显示治理有效但用户仍然慢，下一步看哪些信号？

先确认“治理有效”指的是什么。

如果 user-service 慢实例已经被避让，但用户仍然慢，说明瓶颈可能不在这个 endpoint。

我会继续看这些信号。

第一，看 frontend HTTP 端到端 latency。SDK RPC p99 降了，不代表 `/checkout` 整体变快。order-service、JSON 编解码、HTTP handler、客户端等待都可能是瓶颈。

第二，看其他下游。checkout 里既调 user-service，也调 order-service。user-service 治理有效，但 order-service 慢，用户照样慢。

第三，看是否所有实例都慢。adaptive P2C 擅长避开单个慢实例，但如果所有 endpoint 都慢，路由没有更好的去处。这时要看 absolute SLO slow_score、CPU、队列、GC、数据库和外部依赖。

第四，看 retry 和 timeout。重试次数、per-try timeout、overall timeout 设置不合理，可能让用户请求卡得更久。

第五，看客户端或网络。浏览器到 frontend、压测机到容器、Docker bridge、宿主机 CPU 争抢都可能影响端到端 latency。

第六，看 trace。metric 只能告诉你哪条曲线高，trace 能告诉你一次慢请求到底卡在哪一段。

所以我不会看到 p99 面板变好就结束排查。治理指标说明 AegisMesh 对某个慢 endpoint 做了正确动作；用户仍然慢时，要沿完整请求路径继续查。
