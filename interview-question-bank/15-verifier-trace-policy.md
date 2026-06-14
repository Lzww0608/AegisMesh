# 15. Verifier、Trace 与流量策略校验

## 简单

### Q393【简单】Verifier 校验哪些内容？

当前 verifier 主要校验三类东西。

第一类是 route distribution，也就是流量是否按预期打到不同 route 上。比如 canary 场景里，希望 `user-service:v1` 承接 90%，`user-service:v2` 承接 10%。verifier 会统计 trace 里的 `route` 字段，再和 spec 里的期望比例比较。

第二类是 retry attempts。spec 里有 `max_retry_attempts`，verifier 会逐条 trace 检查 `retry_attempts` 有没有超过上限。这个检查适合发现某个逻辑请求被重试太多次。

第三类是 forbidden edges。spec 里可以写禁止出现的调用边，比如：

```yaml
forbidden_edges:
  - frontend->payment-service
```

verifier 会从 trace 的 `path` 里拼出相邻调用边，如果发现命中禁止边，就把 report 标成失败。

所以我会把它定义成 trace-based policy verifier。它不是在线控制面，也不参与每次 RPC 决策；它读真实或样例 JSONL trace，判断流量结果是否符合策略预期。

### Q394【简单】TraceRecord 包含哪些字段？

verifier 真正消费的 `TraceRecord` 在 `pkg/verifier/spec.go` 里，字段是：

```go
type TraceRecord struct {
    TraceID       string   `json:"trace_id" yaml:"trace_id"`
    Route         string   `json:"route" yaml:"route"`
    Path          []string `json:"path" yaml:"path"`
    RetryAttempts int      `json:"retry_attempts" yaml:"retry_attempts"`
    Status        string   `json:"status" yaml:"status"`
}
```

也就是：

- `trace_id`：一次逻辑请求的 ID；
- `route`：这条 RPC 选中的 route；
- `path`：调用路径，用于生成边；
- `retry_attempts`：已经发生的重试次数；
- `status`：本次 RPC 的 gRPC status。

真实 SDK trace 里字段更多，`pkg/trace.Record` 还会写：

- `span_id`
- `parent_span_id`
- `timestamp_unix_ms`
- `source`
- `destination`
- `method`
- `upstream`
- `attempt`

这些字段对调试很有用。比如 `upstream` 能看到实际连到了哪个 `ip:port`，`attempt` 能区分第几次尝试。verifier 当前只读取自己需要的字段，其他字段会被 JSON 解析自然忽略。

### Q395【简单】route distribution 是如何计算的？

代码在 `routeDistribution(traces []TraceRecord)` 里。

逻辑很直接：

1. 遍历所有 trace。
2. 如果 `trace.Route` 不为空，就给这个 route 的计数加一。
3. 最后用每个 route 的计数除以 `len(traces)`。

也就是说：

```text
route_distribution[route] = route_count / total_trace_count
```

这里有个细节：分母是所有 trace 的数量，不是有 route 字段的 trace 数量。如果某些 trace 没有 route，它们不会进入任何 route 的分子，但仍然留在分母里。这样会把所有 route 的比例都压低。这个设计偏保守，因为缺失 route 本身就是观测数据问题，不应该被悄悄忽略掉。

### Q396【简单】tolerance 的默认值是多少？

默认是 `0.03`，也就是 3 个百分点。

在 `ParseSpec` 里，如果 YAML 没写 `expect.tolerance`，或者写出来解析后是 0，代码会设置：

```go
spec.Expect.Tolerance = 0.03
```

举个例子，期望 `user-service:v2` 是 10%，tolerance 是 3%，那么实际比例在 7% 到 13% 之间就算通过。

这个默认值适合本项目的本地实验。它给了随机采样一点空间，又不会宽到把明显错误的 80/20 当成 90/10。

### Q397【简单】MaxRetryAttempts 校验什么？

`MaxRetryAttempts` 校验单条 trace 里的 `retry_attempts` 有没有超过上限。

比如 spec 写：

```yaml
max_retry_attempts: 1
```

那 verifier 会遍历每条 trace：

```text
if trace.retry_attempts > 1 -> fail
```

这里的 `retry_attempts` 不是总 attempt 数，而是重试次数。第一次调用不算重试。`attempt=1` 时 `retry_attempts=0`，`attempt=2` 时 `retry_attempts=1`。

要注意一个边界：如果 spec 不写 `max_retry_attempts`，Go 的默认值是 0，verifier 就会按“不能重试”处理。当前示例 spec 都显式写了 1，避免歧义。

### Q398【简单】ForbiddenEdges 用来发现什么问题？

`ForbiddenEdges` 用来发现“不该出现的调用边”。

比如系统设计里 frontend 只能调 `user-service` 和 `order-service`，不应该直接调 `payment-service`。那 spec 可以写：

```yaml
forbidden_edges:
  - frontend->payment-service
```

verifier 会把 trace 的 `path` 转成边。比如：

```json
["frontend", "user-service", "payment-service"]
```

会生成：

```text
frontend->user-service
user-service->payment-service
```

只要其中一条命中 forbidden edge，report 就失败。

这个检查适合发现服务绕过网关、绕过中间层、错误调用新版本服务、或者策略配置把流量导到了不该去的下游。

### Q399【简单】为什么 sample trace 和 real SDK trace 都需要支持？

sample trace 适合做单元测试和规则调试。它是手写 JSONL，不需要启动 Controller、demo 服务、Docker Compose，也不依赖网络环境。想验证 route 分布、retry 上限、forbidden edge 的判断逻辑，sample trace 很方便。

real SDK trace 解决另一个问题：策略是否真的在运行时生效。

比如你写了 adaptive P2C、probe ratio、retry budget，单元测试只能说明代码分支对。真实 trace 能告诉你压测时请求实际打到了哪个 upstream、是否发生 retry、是否进入了禁止路径。

所以两者都要保留。sample trace 保证 verifier 自己正确；real trace 保证系统运行结果正确。前者是工具测试，后者是系统验证。

### Q400【简单】frontend-adaptive 为什么要写 JSONL trace log？

因为 `frontend-adaptive` 是实验里最常用的 AegisMesh 客户端入口，它使用 adaptive P2C、retry、telemetry、trace 这些 SDK 能力。让它写 JSONL trace，就能把真实 RPC 选择结果落到文件里。

在 `docker-compose.experiments.yml` 里，`frontend-adaptive` 启动时带了：

```text
--trace-log /traces/frontend-adaptive.jsonl
```

同时把容器内 `/traces` 挂载到宿主机的 `experiments/traces/`。所以实验跑完后，本地会有：

```text
experiments/traces/frontend-adaptive.jsonl
```

然后可以直接跑：

```bash
go run ./cmd/verifier \
  --spec experiments/verifier/real-trace-smoke.yaml \
  --traces experiments/traces/frontend-adaptive.jsonl
```

这样 verifier 不再只读手写样例，而是读 SDK 在真实压测中生成的 trace。

### Q401【简单】trace_id、span_id、attempt 分别有什么作用？

`trace_id` 表示一次逻辑请求。比如一次 HTTP `/checkout` 进入 frontend，之后它调用 user-service 和 order-service，这些 RPC 可以共享同一个 `trace_id`。如果 user-service 调用发生 retry，第 1 次和第 2 次 attempt 也应该属于同一个 `trace_id`。

`span_id` 表示一次具体 RPC attempt。SDK interceptor 每发起一次 unary RPC，会生成新的 span id，并写到 outgoing metadata：

```text
x-aegis-span-id
```

`attempt` 表示这是第几次尝试。第一次是 1，第二次是 2。SDK 会把它写到：

```text
x-aegis-attempt
```

这样看 trace 时就不会混淆。`trace_id` 告诉你“这些记录属于同一个用户请求”，`span_id` 告诉你“这是其中一次具体 RPC”，`attempt` 告诉你“这是第几次尝试”。

### Q402【简单】Verifier 输出 report 应该如何用于实验结论？

verifier 的输出是 JSON report，里面有：

- `passed`
- `checks`
- `route_distribution`
- `trace_count`

实验结论不能只写“verifier 通过了”。应该把 report 里的核心数字写出来。

比如 canary 实验可以写：

```text
期望 v2 10%，实际 v2 10.8%，tolerance=3%，route_distribution check passed。
```

retry 实验可以写：

```text
所有 trace 的 retry_attempts <= 1，未出现单请求超额重试。
```

forbidden edge 可以写：

```text
在 N 条 trace 中未观察到 frontend->payment-service。
```

如果 report 失败，也不要只说失败。要把失败的 check 名称、实际值、期望值和 trace_count 一起写出来。这样实验结果才可复盘。

## 深度

### Q403【深度】route distribution 以 trace 数量为分母时，缺失 route 的 trace 会造成什么影响？

当前实现里，route distribution 的分母是 `len(traces)`，也就是所有 trace 记录数。缺失 `route` 的 trace 不计入任何 route 的分子，但仍然计入分母。

这会带来一个明显结果：所有 route 的比例都会被压低。

举个例子，100 条 trace 里有 90 条 route 是 `v1`，10 条缺失 route。当前算法算出来是：

```text
v1 = 90 / 100 = 90%
```

如果按“有 route 的 trace”做分母，就会是：

```text
v1 = 90 / 90 = 100%
```

这两个解释完全不同。

我更倾向于当前这种偏保守的做法。因为缺失 route 不是正常情况，尤其在真实 SDK trace 里，route 缺失可能说明 interceptor 没拿到 upstream、trace 写入不完整，或者某类请求绕过了 SDK。把它留在分母里，会让分布检查更容易暴露数据缺口。

如果要做得更严格，可以在 verifier 里单独加一个 `missing_route_count` 检查。这样 report 会明确告诉你：到底是路由比例不对，还是 trace 数据缺字段。

### Q404【深度】如果只校验最终 route，是否能发现中间链路错误？

不能。

只看最终 route，只能回答“这次 RPC 最后打到了哪个 upstream”。如果系统里有多跳调用，比如：

```text
frontend -> user-service -> payment-service
```

只校验 frontend 调 user-service 的 route，看不到 user-service 后面有没有错误调用 payment-service。

这就是 `path` 和 `ForbiddenEdges` 的意义。`route` 适合做分流比例校验，`path` 适合做拓扑约束校验。

当前 AegisMesh 的 path 还是比较简单的。SDK trace 写的是：

```go
[]string{source, route}
```

也就是单跳记录。要校验完整调用链，需要把不同 span 按 `trace_id` 和 `parent_span_id` 组装起来。现在 `parent_span_id` 字段已经在 trace record 里预留，但 demo 链路还没有做完整 span tree 重建。

所以面试里我会说：当前 verifier 已经能校验单跳 route 和 forbidden edge；多跳端到端路径校验需要进一步把 trace 记录聚合成调用图。

### Q405【深度】ForbiddenEdges 基于 path 字符串拼接，会有什么标准化问题？

当前实现是把 path 相邻节点用字符串拼起来：

```go
path[i] + "->" + path[i+1]
```

这种方式简单，但对命名标准很敏感。

比如下面这些写法，在人看来可能是同一个服务，在 verifier 看来是不同节点：

```text
user-service
user-service:v1
user-service@10.0.0.2:7001
demo.shop.v1.UserService
```

还有一些边界：

- 大小写不统一；
- 端口变化导致 route 字符串变化；
- Kubernetes 里 pod IP 重建后 upstream 变化；
- 同一个服务有 version、zone、instance 等多个维度；
- path 里有空格或多余前后缀。

所以生产化 verifier 应该先做 canonicalization。比如把节点统一成：

```text
service
service/version
service/instance
```

再根据 spec 决定 forbidden edge 是按 service 粒度、version 粒度，还是 instance 粒度匹配。当前项目为了本地实验可读性，直接用字符串；这是可接受的 MVP，但不是长期最稳的表示方式。

### Q406【深度】MaxRetryAttempts 只能限制单 trace，如何验证全局 retry budget？

`MaxRetryAttempts` 只能回答一个问题：有没有某条 trace 重试次数超过上限。

它不能回答全局 retry budget 是否生效。因为 retry budget 关心的是一个时间窗口内的总重试量，例如：

```text
allowed_retries = max(min_budget, original_requests * budget_ratio)
```

要验证全局 budget，应该看窗口聚合数据：

- original requests；
- retry attempts；
- total attempts；
- retry amplification；
- budget denied 次数，如果系统导出了这个指标。

项目里的 retry 实验就是用 `experiments/scripts/run_retry_amplification.py` 从 metrics 里统计 attempts，然后写 `retry.csv`。之前结果是：没有 budget 时 amplification 是 2.000x，有 budget 时是 1.150x。

所以我会把两层校验分开：

- verifier 的 `MaxRetryAttempts` 防止单个请求无限重试；
- retry benchmark / metrics 校验整体预算有没有限制放大。

如果以后要把全局 budget 放进 verifier，可以让 trace 带 `timestamp_unix_ms`，按窗口聚合，再在 spec 里写：

```yaml
retry_budget:
  window_seconds: 10
  ratio: 0.15
  min_budget: 10
```

这样 verifier 才能直接检查窗口级预算。

### Q407【深度】如果 trace 采样不是 100%，route distribution 的置信区间如何估计？

如果 trace 是抽样来的，route distribution 就不能只看一个实际比例，还要看样本量。

最简单的估计可以用二项分布。比如金丝雀期望 v2 是 10%，采样到 n 条 trace，实际 v2 比例是 `p_hat`。标准误差大致是：

```text
SE = sqrt(p_hat * (1 - p_hat) / n)
```

95% 置信区间可以近似写成：

```text
p_hat ± 1.96 * SE
```

举个例子，n=1000，p_hat=0.10：

```text
SE = sqrt(0.1 * 0.9 / 1000) ≈ 0.0095
95% CI ≈ 10% ± 1.86%
```

这时 tolerance=3% 基本够用。

如果 n=100，p_hat=0.10：

```text
SE ≈ 3%
95% CI ≈ 10% ± 5.9%
```

这时 tolerance=3% 就太紧，正常随机波动也可能失败。

多 route 场景更像 multinomial，可以对每个 route 做近似，也可以用卡方检验。工程上我更建议 verifier 把 `trace_count`、每个 route 的 count 和比例都输出，再根据样本量选择 tolerance。不要拿 20 条 trace 去证明 90/10 分流精确成立。

### Q408【深度】Verifier 如何区分策略违规和观测数据缺失？

当前 verifier 区分得还不够细。比如 route distribution 失败，可能有两种原因：

- 策略真的没生效，流量比例错了；
- trace 缺 route 字段，导致分母变大、比例变低。

forbidden edge 也是类似。没有看到 forbidden edge，可能说明策略没违规，也可能说明 path 没采集完整。

我会把 verifier 的结果分成三类：

1. `PASS`：数据完整，检查通过。
2. `FAIL`：数据完整，检查不符合策略。
3. `INCONCLUSIVE`：数据不够或字段缺失，不能下结论。

要实现这个，需要补几类数据质量检查：

- trace_count 是否达到 spec.requests 或最小样本数；
- 缺失 `trace_id` 的数量；
- 缺失 `route` 的数量；
- 缺失 `path` 的数量；
- trace 时间范围是否覆盖实验窗口；
- source/destination/method 是否符合 spec。

当前项目的 verifier 已经能做策略检查，但数据质量检查还比较轻。报告里如果看到 trace_count 很低，或者 route_distribution 很怪，我不会直接说策略失败，会先检查 trace 是否完整。

### Q409【深度】real trace 中 extra fields 被保留但 verifier 忽略，有什么可扩展性好处？

这是一个比较实用的设计。

真实 SDK trace 写了很多字段，比如：

- `span_id`
- `timestamp_unix_ms`
- `source`
- `destination`
- `method`
- `upstream`
- `attempt`

verifier 当前只解析 `TraceRecord` 里的五个字段。Go 的 JSON unmarshal 会忽略未知字段，所以这些 extra fields 不会影响现有校验。

好处是 schema 可以向前演进。今天 verifier 只校验 route、retry 和 forbidden edge；明天可以加 method 级策略、时间窗口、PROBING ratio、route version、policy version，而不需要改 SDK 的日志格式。

这也让排查更方便。比如 route distribution 失败时，可以用 `upstream` 查是不是某个实例流量异常；用 `timestamp_unix_ms` 查是不是只在故障窗口失败；用 `attempt` 查是不是 retry 导致某个 route 被重复计数。

我比较喜欢这种做法：trace 记录尽量保留上下文，verifier 按当前规则消费其中一部分。

### Q410【深度】如果业务路径是 DAG 而不是线性 path，ForbiddenEdges 校验如何扩展？

线性 path 适合简单链路：

```text
frontend -> user-service -> order-service
```

但真实系统经常是 DAG。一个 checkout 请求可能同时调用 user、order、inventory、coupon，某些调用还会异步触发消息。

这时 path 不应该再表示成单个数组，而应该表示成 span graph：

```json
{
  "trace_id": "trace-1",
  "spans": [
    {"span_id": "a", "parent_span_id": "", "service": "frontend"},
    {"span_id": "b", "parent_span_id": "a", "service": "user-service"},
    {"span_id": "c", "parent_span_id": "a", "service": "order-service"}
  ]
}
```

verifier 可以先根据 `span_id / parent_span_id` 建图，再从图里提取所有边：

```text
frontend->user-service
frontend->order-service
```

然后再跑 forbidden edge 检查。

如果是更复杂的策略，还可以检查：

- 某条边必须存在；
- 某条边不能跨 version；
- 某个服务只能被特定 caller 调用；
- 某个 DAG 必须在有限深度内结束；
- 禁止出现环。

当前项目还没有做 DAG verifier。它的 path 模型更像单跳 RPC 记录，适合本地实验和 canary 校验。扩展到 DAG 后，verifier 会更接近真正的端到端流量策略验证器。

### Q411【深度】如何用 verifier 证明 PROBING endpoint 没有被大量恢复流量打爆？

这个实验要把 recovery 状态和 trace 放在一起看。

第一步，从 `recovery.csv` 找到目标 endpoint 处于 `PROBING` 的时间窗口。项目里的 `analyze_probe_ratio.py` 就是这么做的：筛选 state 为 `PROBING`、端口等于目标端口的行，然后取最早和最晚 timestamp。

第二步，在这个窗口里读 `frontend-adaptive.jsonl`，只统计 destination 是 `user-service` 的 trace。

第三步，按 `upstream` 判断哪些 trace 打到了 probing endpoint。当前脚本用端口匹配，比如 `7002`。

第四步，计算：

```text
probe_ratio = probing_trace_rows / trace_rows_in_window
```

再和上限比较。项目实验里用过 `max_probe_ratio=0.10`，结果是 `0.002177`，也就是 0.2177%，低于 10% 上限。

严格说，这个检查现在由 `analyze_probe_ratio.py` 做，不是 `cmd/verifier` 的核心 YAML 规则。后续可以把它纳入 verifier spec，例如：

```yaml
expect:
  probing:
    endpoint: user-service@7002
    max_ratio: 0.10
```

这样 route distribution、retry、forbidden edge、probe ratio 就能统一输出到一个 report。

### Q412【深度】如何设计 verifier 的 negative tests，证明它能发现错误？

negative test 要故意喂错数据，证明 verifier 会失败。

项目里已经有类似测试。比如 `TestVerifyTraceDistributionFailsOutsideToleranceAndForbiddenEdge` 构造了两条 trace：

- 两条都走 `user-service:v1`，但 spec 期望 v1/v2 各 50%；
- 第一条 path 是 `frontend -> payment-service`，命中 forbidden edge；
- 第一条 `retry_attempts=2`，超过 `max_retry_attempts=1`。

这条测试会同时触发三个失败：

- `route_distribution:user-service:v1`
- `retry_budget`
- `forbidden_edges`

我还会补几类 negative tests：

- trace 文件里有非法 JSON，`LoadTraceJSONL` 应该返回带行号的错误；
- spec 没写 route，但 trace 有 route，report 不应该误报 route distribution；
- trace 缺失 route，应该触发数据质量 warning；
- `path` 只有一个节点，forbidden edge 不应该误判；
- tolerance 边界值，刚好等于 tolerance 应该通过。

一个 verifier 如果只有正向样例，其实很难让人信。负向测试能证明它不是只会输出 PASS。

## 拓展

### Q413【拓展】MeshTest、chaos test、trace-based verification 的关系是什么？

这三个概念关注点不同，但可以组合。

MeshTest 更偏策略验证。它关心的是：流量是否按 mesh policy 走，比如 canary 90/10、禁止跨服务调用、重试次数不能超标。

chaos test 更偏故障注入。它关心的是：系统在 delay、loss、CPU throttle、服务错误下能不能保持可接受行为。

trace-based verification 是一种验证方法。它不只看配置，也不只看 dashboard，而是看真实请求实际经过哪里、重试了几次、命中了哪个 upstream。

AegisMesh 的组合方式是：

```text
故障注入 / 压测 -> SDK 写 JSONL trace -> verifier 校验策略结果
```

比如在慢实例恢复过程中，可以用 chaos test 制造故障，用 trace-based verifier 证明 PROBING endpoint 没拿到过量流量。这就比“我配置了 probe ratio”更有说服力，因为它检查的是运行结果。

### Q414【拓展】如何把 trace policy 写成 DSL，而不是固定 YAML 字段？

固定 YAML 字段适合项目早期，比如：

```yaml
routes:
  user-service:v1: 0.90
max_retry_attempts: 1
forbidden_edges:
  - frontend->payment-service
```

但策略多了以后，字段会越来越散。DSL 可以把校验规则写成更统一的表达式。

比如可以设计成：

```text
route("user-service:v2").ratio between 0.07 and 0.13
max(trace.retry_attempts) <= 1
not edge("frontend", "payment-service")
count(where destination == "user-service" and upstream.port == 7002) / count(where destination == "user-service") <= 0.10
```

这样 route、retry、edge、probe ratio 都能用同一套表达式表达。

工程上我会分阶段做：

1. 先保留当前 YAML 字段，保证简单场景易读。
2. 增加 `rules:` 字段，每条 rule 有 name、expression、severity。
3. 表达式引擎只暴露白名单函数，不允许任意代码执行。
4. report 里输出每条 rule 的实际值和失败样本。

不要一开始就做复杂 DSL。它很容易变成另一个配置语言项目。只有当固定字段开始限制表达能力时，再引入 DSL 才划算。

### Q415【拓展】如何用 OpenTelemetry span events 替代自定义 JSONL trace？

可以把 AegisMesh 当前的自定义 JSONL 字段映射到 OpenTelemetry span attributes 和 span events。

比如一次 RPC attempt 可以是一个 client span，属性包括：

```text
rpc.system = grpc
rpc.method = /demo.shop.v1.UserService/GetUser
aegis.source = frontend
aegis.destination = user-service
aegis.upstream = 10.0.0.2:7001
aegis.route = user-service@10.0.0.2:7001
aegis.attempt = 2
aegis.retry_attempts = 1
aegis.policy_version = ...
```

如果发生 retry，可以加 span event：

```text
event.name = aegis.retry
event.attributes.reason = UNAVAILABLE
event.attributes.next_attempt = 2
```

好处是可以接入 OpenTelemetry Collector、Jaeger、Tempo 这类现成生态，不用自己维护 trace 存储和查询。

但 JSONL 仍然有价值。它简单、可 diff、适合本地实验和离线 verifier。我的设计会是：SDK 支持两种 sink。实验默认 JSONL，生产环境优先 OTel exporter。verifier 可以先读 JSONL，后续再从 OTel backend 查询 span。

### Q416【拓展】分布式系统中 end-to-end invariant 如何表达和验证？

end-to-end invariant 是对完整请求路径的约束，不是对单个函数或单个服务的约束。

比如在 AegisMesh 里可以表达这些 invariant：

- 金丝雀版本只能接 10% 左右流量；
- 非幂等方法不能被 retry；
- frontend 不能直接调用 payment-service；
- PROBING endpoint 的流量不能超过 2% 或 10% 上限；
- 一个 checkout trace 必须包含 user-service 和 order-service；
- policy version 不一致的请求不能超过某个比例；
- 单个 trace 的 retry attempts 不能超过 1。

验证方法一般是：

1. 收集真实 trace。
2. 把 trace 组装成请求级视图。
3. 对每条 trace 或每个时间窗口执行 invariant。
4. 把失败样本保留下来，给出可复盘证据。

这类 invariant 的难点在于跨服务、跨时间窗口和数据缺失。配置正确不代表运行正确。end-to-end verification 要验证的是实际发生过的请求。

### Q417【拓展】如果 trace 丢失或乱序，策略验证如何保持可信？

先承认一点：trace 丢失或乱序会降低 verifier 结论的可信度。不能假装没影响。

处理方式有几类。

第一，输出数据质量指标。比如：

- trace_count；
- expected_requests；
- missing_trace_id_count；
- missing_route_count；
- missing_path_count；
- duplicate_span_count；
- time_range。

如果 trace_count 太低，报告应该是 inconclusive，而不是 PASS。

第二，用时间戳和 trace_id 去重、排序。JSONL 写入顺序不一定等于请求发生顺序，尤其多进程或多节点写入时。verifier 应该按 `timestamp_unix_ms` 和 span parent 关系重建。

第三，对采样 trace 使用统计口径。比如 route distribution 不能要求精确等于 10%，要根据样本量给 tolerance 或置信区间。

第四，保留失败样本。即使 trace 不完整，只要看到 forbidden edge，通常也能判定违规。反过来，没有看到 forbidden edge 不一定能证明不存在，除非 trace 覆盖率足够高。

所以 verifier 的报告要区分“没有观察到违规”和“可以证明没有违规”。这两个说法差很多。

### Q418【拓展】如何把 verifier 集成到发布流水线，阻止错误流量策略上线？

我会放在三个阶段。

第一阶段是离线策略检查。策略 PR 提交后，CI 用 sample trace 和 negative trace 跑 verifier，保证规则语法、边界和失败路径没问题。

第二阶段是 staging replay。把候选 policy 部署到 staging，跑一组固定流量，收集真实 JSONL trace，再执行 verifier。比如：

```bash
go run ./cmd/verifier \
  --spec experiments/verifier/canary-user-service.yaml \
  --traces experiments/traces/frontend-adaptive.jsonl
```

如果 route distribution、retry、forbidden edge 失败，流水线直接阻断。

第三阶段是 canary 后校验。新策略只对小流量生效，持续收集 trace。只有 verifier 连续几个窗口通过，才扩大流量。

报告里还要保存：

- policy version；
- trace 文件或查询条件；
- verifier report；
- 失败样本 trace_id；
- rollout 阶段。

这样出了问题能回滚，也能解释为什么当时允许或阻止上线。

### Q419【拓展】金丝雀发布中的 90/10 流量校验如何计算统计显著性？

90/10 校验本质上是在判断观测比例是否可能来自目标分布。

简单做法是二项分布近似。假设 v2 期望比例是 10%，观察到 n 条 trace，其中 k 条走 v2：

```text
p_hat = k / n
SE = sqrt(0.1 * 0.9 / n)
z = (p_hat - 0.1) / SE
```

如果 `|z|` 很大，比如超过 1.96，就说明在 95% 置信水平下偏离比较明显。

举个例子：

```text
n = 1000
k = 140
p_hat = 14%
SE = sqrt(0.1 * 0.9 / 1000) ≈ 0.95%
z ≈ 4.21
```

这个偏离就很难用随机波动解释。

如果 n 很小，比如 50 条 trace，k=8，也就是 16%，看起来偏离 10%，但样本太少，不能马上判断策略错了。这时可以用 Wilson interval 或 exact binomial test。

工程上我会给 verifier 加两个门槛：

- 最小样本数，比如至少 500 或 1000；
- 根据样本量动态计算 tolerance，而不是永远写死 3%。

当前项目用固定 tolerance=0.03，是为了本地实验简单可控。生产金丝雀最好加统计检验。

### Q420【拓展】如果服务调用中存在异步消息，trace path 如何建模？

异步消息不能简单塞进线性 RPC path。

比如 checkout 写入订单后发一条消息到 Kafka，后面由 inventory-consumer 异步扣库存。这个路径不是同步调用，但它仍然属于同一个业务流程的一部分。

建模时可以把消息当成特殊边：

```text
order-service --publish:OrderCreated--> kafka-topic
kafka-topic --consume:OrderCreated--> inventory-service
```

trace 里需要记录：

- producer span；
- message id；
- topic / queue；
- consumer span；
- causality link，也就是 consumer 由哪条消息触发；
- 时间戳。

OpenTelemetry 里可以用 span links 表示这种非父子关系。JSONL 里也可以加字段：

```json
{
  "trace_id": "trace-1",
  "span_id": "consumer-1",
  "links": ["producer-1"],
  "messaging_system": "kafka",
  "messaging_destination": "order-created"
}
```

verifier 做 forbidden edge 时，就不能只看同步 `parent_span_id`，还要把 message publish / consume 关系也转成边。比如禁止 `payment-service` 直接消费某个内部 topic，也可以通过这种图来检查。

对 AegisMesh 当前项目来说，demo 主要是同步 gRPC。异步 trace 建模属于下一步扩展，但方向很明确：从线性 path 升级到 trace graph，再在 graph 上跑策略校验。
