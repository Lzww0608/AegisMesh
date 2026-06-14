# 13. Dynamic Policy、YAML 配置与 WatchPolicy

## 简单

### Q337【简单】PolicySnapshot 中包含哪些策略？

`PolicySnapshot` 是 Controller 下发给 SDK 的一次策略快照。它在 `api/proto/aegis/v1/policy.proto` 里定义，主要包含这些字段：

```proto
message PolicySnapshot {
  string service = 1;
  int64 version = 2;
  string routing_policy = 3;
  RetryPolicy retry = 4;
  OutlierDetectionPolicy outlier_detection = 5;
  CircuitBreakerPolicy circuit_breaker = 6;
  map<string, MethodPolicy> methods = 7;
}
```

我会按层次讲。

`service` 是策略所属服务，比如 `user-service`。`version` 是这份快照的版本，用来判断策略有没有变化。

`routing_policy` 决定客户端用哪种负载均衡策略，当前支持 `adaptive_p2c` 和 `round_robin`。

`retry` 是服务级重试策略，字段包括：

- `enabled`
- `max_attempts`
- `budget_ratio`
- `min_budget`
- `window_seconds`
- `per_try_timeout_millis`

`outlier_detection` 是慢故障状态机相关配置，比如 degraded/eject 阈值、连续窗口数、摘除时间、恢复阈值、探测成功率阈值。

`circuit_breaker` 目前主要有 `max_inflight_per_endpoint`。

`methods` 是方法级策略 map。key 是 gRPC full method，比如 `/demo.shop.v1.UserService/GetUser`。它可以配置这个方法是否幂等、timeout，以及方法级 retry。

当前 SDK 真正热应用得比较完整的是 retry、retry budget、per-method timeout 和 idempotency-aware retry。routing policy 的初始选择在 Dial 时生效，outlier/circuit breaker 这些字段已经在 proto/YAML 暴露，但还没有全部作为在线热更新策略接入 SDK。

### Q338【简单】YAML policy file 如何映射到 protobuf policy？

YAML 文件由 `pkg/policy/store.go` 解析。顶层结构是：

```yaml
services:
  user-service:
    routing_policy: adaptive_p2c
    retry:
      enabled: true
      max_attempts: 2
      budget_ratio: 0.15
      min_budget: 10
      window_seconds: 10
      per_try_timeout_millis: 750
    outlier_detection:
      degraded_threshold: 1.5
      eject_threshold: 2.5
      consecutive_windows: 3
      ejection_duration_seconds: 30
      recovery_threshold: 1.0
      probe_success_threshold: 0.95
    circuit_breaker:
      max_inflight_per_endpoint: 128
    methods:
      /demo.shop.v1.UserService/GetUser:
        idempotent: true
        timeout_millis: 150
        retry:
          enabled: true
          max_attempts: 2
```

`FileStore.Reload()` 会把 YAML 先 unmarshal 到内部 Go struct，再转换成 protobuf：

- `routing_policy` -> `PolicySnapshot.RoutingPolicy`
- `retry` -> `RetryPolicy`
- `outlier_detection` -> `OutlierDetectionPolicy`
- `circuit_breaker` -> `CircuitBreakerPolicy`
- `methods` -> `map<string, MethodPolicy>`

每个 service 都会生成一份 `PolicySnapshot`。它的 `Version` 用 policy 文件的 `ModTime().UnixNano()`。

这个设计的优点是直观，YAML 适合实验和本地演示。缺点是没有完整 schema 校验，也没有专门的策略发布系统。生产环境里，我会把 YAML 作为输入格式之一，但发布时还要加 validation、审计、灰度和回滚。

### Q339【简单】GetPolicy 和 WatchPolicy 的区别是什么？

`GetPolicy` 是一次性读取。SDK 在 Dial 时调用它，拿当前服务的初始策略快照。

`WatchPolicy` 是流式订阅。SDK 和 Controller 建立一个 stream，Controller 发现策略 version 变化后，把新的 `PolicySnapshot` 推给 SDK。

代码上，Controller 的 `WatchPolicy` 会先发送一次当前快照，然后每隔 `reloadInterval` 检查文件有没有变化：

```go
if err := s.sendIfChanged(req.Service, &lastVersion, stream); err != nil {
    return err
}

ticker := time.NewTicker(s.reloadInterval)
```

默认 reload interval 是 3 秒。

所以两者的关系是：

- `GetPolicy` 解决启动时拿不到初始策略的问题。
- `WatchPolicy` 解决运行中策略变化的问题。

AegisMesh SDK 两个都用：先 `GetPolicy`，再启动 watcher。

### Q340【简单】SDK 在 Dial 时为什么先加载 initial policy？

因为有些策略必须在 `ClientConn` 创建前决定。

最典型的是 routing policy。gRPC 的 balancer 是通过 service config 选择的。AegisMesh 在 Dial 时会根据 routing policy 生成：

```go
grpc.WithDefaultServiceConfig(serviceConfig)
```

如果一开始没拿到 policy，就只能用默认 `adaptive_p2c`。后面 watcher 收到 `round_robin`，当前代码也不会自动重建 `ClientConn` 或切换 gRPC balancer。

所以 SDK 在 Dial 时先调用 `GetPolicy`，用 500ms 超时尽量拿到初始策略：

```go
const defaultPolicyFetchTimeout = 500 * time.Millisecond
```

如果拿到了，就先应用到 DialOptions，再创建连接。拿不到也不会阻塞启动，SDK 会用本地默认策略继续工作。

这就是项目当前的边界：初始 policy 对 routing 很重要；后续 WatchPolicy 主要热更新 retry、retry budget、per-method timeout 和幂等性重试控制。

### Q341【简单】policy watcher 的 backoff 作用是什么？

watcher 失败后不能立刻无限重连，否则 Controller 不可用时，客户端会疯狂打连接。

SDK 里有一个默认 backoff：

```go
const defaultPolicyWatchBackoff = 2 * time.Second
```

`startPolicyWatcher` 的循环逻辑是：调用一次 `watchPolicyOnce`，如果 stream 断了、连接失败了、Controller 返回错误，就等 2 秒再试。

这个 backoff 的作用是：

- 降低 Controller 故障时的重连压力。
- 避免日志刷屏。
- 给网络或 Controller 重启一点恢复时间。
- 保证 SDK 不会因为 policy stream 断掉就退出业务流程。

它现在是固定 2 秒。生产里更好的做法是指数退避加 jitter，比如 1s、2s、4s，再加随机抖动，避免大量客户端同时重连。

### Q342【简单】方法级 policy 可以覆盖哪些服务级配置？

当前方法级 policy 能覆盖 retry 和 timeout 相关配置。

`MethodPolicy` 里有：

```proto
message MethodPolicy {
  string method = 1;
  bool idempotent = 2;
  int64 timeout_millis = 3;
  RetryPolicy retry = 4;
}
```

SDK 的合并顺序是：

```text
DefaultDialOptions
-> service-level PolicySnapshot
-> method-level MethodPolicy
```

方法级 `timeout_millis` 会覆盖 retry policy 的 `PerTryTimeout`。

方法级 `retry` 可以覆盖：

- 是否启用 retry
- 最大尝试次数
- per-try timeout
- retry budget ratio
- min budget
- budget window

如果方法没有显式 retry policy，并且 `idempotent=false`，SDK 会默认把 retry 关掉。这是为了保护 `CreateOrder` 这类有副作用的 RPC。

方法级 policy 目前不覆盖 routing policy。也就是说，不能在当前实现里让同一个 ClientConn 对不同 method 使用不同 balancer。

### Q343【简单】idempotent 字段为什么重要？

因为重试是否安全，取决于方法语义。

读接口通常是幂等的。比如 `GetUser`，同一个请求重试一次，最多多查一次用户信息，不会改变业务状态。

写接口可能不是幂等的。比如 `CreateOrder`，第一次请求可能已经在服务端创建了订单，只是响应丢了。客户端如果自动重试，就可能创建两笔订单。

AegisMesh 的方法级 policy 里有 `idempotent` 字段。SDK 的逻辑是：如果某个 method 没有显式 retry policy，并且 `idempotent=false`，就把 retry 关掉：

```go
if !method.Idempotent {
    options.RetryMode = RetryOff
    options.RetryPolicy.MaxAttempts = 1
}
```

这个字段让项目能主动回答“非幂等写请求怎么处理”这个面试追问。我的回答会很直接：默认不重试，除非业务提供 idempotency key 或去重机制。

### Q344【简单】policy version 使用文件 modTime 有什么作用？

version 用来判断策略是否变化。

`FileStore.Reload()` 里会读取 policy 文件的修改时间：

```go
version := info.ModTime().UnixNano()
```

然后写入 `PolicySnapshot.Version`。

Controller 的 `WatchPolicy` 维护一个 `lastVersion`。如果当前 snapshot 的 version 和上次一样，就不发送；如果变了，就推给 SDK：

```go
if snapshot.Version == *lastVersion {
    return nil
}
*lastVersion = snapshot.Version
return stream.Send(...)
```

SDK 侧也用 version 管理 retry budget。`dynamicRetrySource` 会按 method 和 policy version 维护 budget。如果 version 变了，它会为这个 method 重建预算。

所以 version 至少有两个作用：减少重复推送，让 SDK 知道策略已经变更。

### Q345【简单】ReloadIfChanged 用什么判断配置是否变化？

`ReloadIfChanged()` 用文件修改时间判断。

逻辑是：

```go
info, err := os.Stat(s.path)
if info.ModTime().UnixNano() == s.currentModTime() {
    return nil
}
return s.Reload()
```

`currentModTime()` 是 FileStore 上一次成功加载的 modTime。

如果文件 modTime 没变，就不重新解析。变了，就调用 `Reload()`。

`Reload()` 成功后才会更新 `s.modTime` 和 `s.policies`。如果 YAML 解析失败，函数返回错误，旧策略仍然留在内存里。

### Q346【简单】如果 PolicyService 不可用，SDK 应该使用什么默认策略？

当前 SDK 会使用本地默认策略。

默认策略来自 `DefaultDialOptions()`：

- `RoutingPolicy = adaptive_p2c`
- `RetryMode = budget`
- `MaxAttempts = 2`
- retryable codes 默认是 `UNAVAILABLE` 和 `DEADLINE_EXCEEDED`
- retry budget 是 `ratio=0.15`、`minBudget=10`、`window=10s`

`loadInitialPolicy` 如果连接失败、超时、GetPolicy 返回错误，都会返回 nil。Dial 流程不会失败，会继续用默认策略创建连接。

watcher 也一样。WatchPolicy 断开后，SDK 保留旧 snapshot，等 2 秒再重连。

这是一个偏可用性的设计：策略系统挂了，业务请求还能跑。代价是策略可能变旧，比如紧急关闭某个方法重试的配置暂时下不去。

## 深度

### Q347【深度】用文件 modTime 作为 version 是否足够可靠？有哪些边界情况？

本地实验够用，生产不够。

modTime 最大的优点是简单。文件改了，时间变了，version 也变了。对单机 demo、Docker Compose 实验、手动改 YAML 来说，这个方案很方便。

边界也不少。

第一，文件系统时间精度不一定稳定。有些环境 modTime 精度比较粗，短时间连续写多次，可能看起来是同一个时间。

第二，内容没变但 modTime 变了，也会触发新 version。比如只 touch 文件，策略内容没变，SDK 仍然可能收到新快照，retry budget 也可能重建。

第三，内容变了但 modTime 没变，也可能被漏掉。比如某些部署工具保留时间戳，或者复制文件时带了原时间。

第四，多 Controller 场景里，不同机器的文件时间和时钟不一定一致。用本地 modTime 做全局版本，很难保证顺序。

更稳的做法是使用显式版本号、配置中心 revision、etcd revision，或者对内容做 hash。生产里我会让每次发布生成一个单调递增的 policy revision，rollback 也生成新 revision，而不是依赖文件时间。

### Q348【深度】YAML reload 成功前后如何保证快照原子切换？

当前 `FileStore.Reload()` 的流程是先在本地变量里完整构建新 map，最后再加锁替换。

它大致是这样：

```go
raw := os.ReadFile(path)
yaml.Unmarshal(raw, &cfg)

policies := make(map[string]*PolicySnapshot)
// 构建所有 service 的 snapshot

s.mu.Lock()
s.modTime = version
s.policies = policies
s.mu.Unlock()
```

这个顺序有一个好处：如果读文件失败、YAML 解析失败、转换失败，函数会直接返回错误，不会污染旧的 `s.policies`。

也就是说，要么旧快照继续服务，要么新快照整体替换。不会出现 user-service 已经换成新配置、order-service 还停在半成品配置这种中间状态。

不过当前实现还可以增强。比如：

- YAML 解析前先做 schema validation。
- 写文件时用临时文件加原子 rename，避免 Controller 读到半写入文件。
- 对每个 service 的 policy 做约束校验。
- reload 失败时记录 metric 和日志，让 dashboard 能看到配置加载失败。

### Q349【深度】PolicyManager 返回 proto.Clone 的原因是什么？

是为了防止外部修改内部快照。

`PolicySnapshot` 里有指针字段，也有 map：

```proto
RetryPolicy retry
map<string, MethodPolicy> methods
```

如果 `policyManager.Snapshot()` 直接把内部指针返回出去，调用方就可能修改它。比如某个 interceptor 拿到 snapshot 后改了 `Methods` map，会影响别的 goroutine 看到的策略。

所以 `Snapshot()` 里做了：

```go
return proto.Clone(m.snapshot).(*aegisv1.PolicySnapshot)
```

`Update()` 也会 clone 一份存进去。

这样每个调用方拿到的是自己的副本，读写不会影响 manager 内部状态，也降低了并发读写 map 的风险。

`FileStore.Get()` 和 `PolicyService.GetPolicy()` 也做 clone，原因一样：策略快照是共享配置，不能把内部对象裸露给外部。

### Q350【深度】policy watcher 断线重连期间，SDK 使用旧策略有什么风险？

风险是策略变更不能及时生效。

比如线上发现 `CreateOrder` 不应该重试，控制面已经改了 YAML，但 SDK 和 Controller 的 WatchPolicy stream 正好断开。SDK 会继续使用旧策略，直到 watcher 重连成功。

类似风险还有：

- retry budget ratio 降低了，但客户端还按旧 ratio 重试。
- per-try timeout 调小了，但客户端还按旧 timeout 等待。
- routing policy 初始配置错了，已经建立的 ClientConn 也不会自动切换 balancer。
- 某个紧急关闭 retry 的策略下发延迟，可能继续制造额外压力。

不过保留旧策略也有好处。策略面短暂不可用时，业务请求不会直接失败。这是一个可用性优先的退化方式。

生产里我会给 policy snapshot 加 staleness 机制。比如超过 5 分钟没有成功 watch，就上报 `policy_stale=1`，必要时进入更保守的本地策略：降低 retry、缩短 timeout、拒绝高风险写方法自动重试。

### Q351【深度】如果 WatchPolicy 收到旧版本快照，是否应该拒绝？

一般应该拒绝，至少要识别出来。

当前 SDK 的 `policyManager.Update()` 不检查 version。它拿到 snapshot 就覆盖：

```go
m.snapshot = proto.Clone(snapshot).(*aegisv1.PolicySnapshot)
```

在单 Controller、单 YAML 文件场景里问题不大，因为 Controller 只会按当前文件状态发送快照。

但多 Controller 或重连场景会复杂。SDK 可能先连到一个新 Controller，收到 version 100；后来重连到旧 Controller，又收到 version 90。如果直接覆盖，客户端就回到了旧策略。

理想做法是：

- 如果 version 单调递增，SDK 拒绝比当前 version 小的快照。
- 如果要回滚，也不要下发旧 version，而是生成一个新的 version，内容回到旧策略。
- 对相同 version、不同内容的快照直接报警，这是严重发布问题。

所以我的回答是：当前代码还没有做版本单调检查；生产环境应该补。回滚不是“发旧版本”，而是“用新版本发布旧内容”。

### Q352【深度】Retry budget 按 policy version 重建会带来什么副作用？

副作用是预算计数会重置。

`dynamicRetrySource` 里有两个 map：

```go
budgets        map[string]*Budget
budgetVersions map[string]int64
```

key 是 method。如果某个 method 的 budget 不存在，或者 snapshot version 变了，就创建新的 budget：

```go
if budget == nil || s.budgetVersions[method] != version {
    budget = retrypkg.NewBudget(cfg)
}
```

好处是策略变化能立即生效。比如把 budget ratio 从 0.15 改成 0.05，新 budget 会用新参数。

坏处是频繁 policy 更新可能绕开预算。比如故障期间每 3 秒更新一次 version，每次都重建 budget，每次都有新的 minBudget。这样重试总量会比预期更高。

解决办法有几种：

- 只有 retry 相关字段变化时才重建 budget。
- 兼容字段变更时迁移已有计数，而不是清零。
- 控制面限制发布频率。
- 将 budget key 从 policy version 改成 retry config hash，routing 或 outlier 变化不影响 retry budget。

当前实现简单清楚，适合 demo；生产里我会避免“任何 policy 变化都重建 retry budget”。

### Q353【深度】routing policy 初始选择在 ClientConn 建立后是否还能动态切换？

当前不能完整动态切换。

SDK 在 Dial 时根据 `options.RoutingPolicy` 生成 gRPC service config：

```go
grpc.WithDefaultServiceConfig(serviceConfig)
```

这个 service config 决定使用 `aegis_adaptive_p2c` 还是 `round_robin`。

`GetPolicy` 在 Dial 前执行，所以初始 routing policy 能影响 ClientConn。`WatchPolicy` 后续收到新 snapshot 后，会更新 policyManager。retry interceptor 每次调用会读取 manager，所以 retry、budget、timeout 能热更新。

但 balancer 类型不是通过 retry interceptor 决定的。当前代码没有在 WatchPolicy 收到新 routing_policy 后重建 ClientConn，也没有使用 gRPC 支持的动态 service config 更新。

所以准确说：

- 初始 routing policy：已生效。
- 运行中 retry/timeout/idempotency：可热更新。
- 运行中切换 balancer：当前未完整实现。

如果要支持运行中切换，可以有两条路：一是 SDK 监听 policy 后重建 ClientConn；二是引入类似 xDS 的动态配置，让 resolver 或 balancer 接收并应用新策略。

### Q354【深度】方法级 retry 与服务级 retry 同时存在时，合并规则如何避免歧义？

当前合并顺序是确定的：

```text
默认 DialOptions
-> 服务级 PolicySnapshot.retry
-> 方法级 MethodPolicy.timeout_millis
-> 方法级 MethodPolicy.retry
-> 方法级 idempotent 兜底
```

服务级 retry 先设置整服务默认行为。比如 user-service 默认最多 2 次尝试，budget ratio 0.15。

方法级 timeout 会先覆盖 per-try timeout。

如果方法级 retry 里有字段，就调用 `applyRetryPolicyToDialOptions` 覆盖服务级 retry。这个覆盖是“有值才覆盖”：`max_attempts > 0` 才改最大尝试次数，`budget_ratio > 0` 才改比例，`window_seconds > 0` 才改窗口。

如果方法级 retry 没有任何字段，并且 `idempotent=false`，SDK 会关闭 retry。

这里有一个 proto3 bool 的边界：`enabled=false` 本身和默认值一样，代码里 `policyRetryHasAnyField` 看不到“用户显式写了 false”。所以如果想显式关闭 retry，YAML 里要像 demo-policy 那样写：

```yaml
retry:
  enabled: false
  max_attempts: 1
```

只写 `enabled: false` 可能不会被识别成一次显式 override。更好的长期方案是把 `enabled` 改成 optional bool 或 enum，比如 `RETRY_UNSPECIFIED / RETRY_ENABLED / RETRY_DISABLED`。

### Q355【深度】策略更新频率很高时，SDK goroutine 和 Controller stream 会承受什么压力？

压力会出现在几个地方。

Controller 侧，每个 WatchPolicy stream 都会定期检查 `ReloadIfChanged()`。如果客户端很多，stream 很多，配置频繁变化，Controller 要不断 stat 文件、解析 YAML、clone snapshot、发送 stream message。

SDK 侧，每个 ClientConn 都有 watcher goroutine。更新频繁时，policyManager 会频繁加锁更新。retry source 看到 version 变化后，还可能频繁重建 budget。

业务侧也会受影响。retry、timeout 这些策略如果几秒一变，调用行为会不稳定。比如一部分请求按旧 timeout，一部分请求按新 timeout；budget 反复重置，实验结果也会变得不好解释。

网络上，很多客户端同时 watch，同一份策略变化会广播给所有客户端。如果没有限速和合并，Controller 会有明显 fan-out 压力。

我会加几个保护：

- policy reload debounce，比如 1 秒内多次文件变化只发布一次。
- version 去重，相同内容不发布。
- 客户端拒绝旧 version。
- 发布频率限制。
- 大规模环境下按服务分片或用配置中心 watch。
- 给 policy watcher 暴露 metrics，比如当前版本、更新次数、失败次数、stale 时间。

### Q356【深度】如何设计 policy validation，防止错误 YAML 让服务不可用？

先做语法校验，再做语义校验。

语法层面：

- YAML 必须能解析。
- 未知字段要报错，而不是静默忽略。Go 里可以用 yaml decoder 的 `KnownFields(true)`。
- service 名和 method 名不能为空。
- method key 最好符合 gRPC full method 格式。

语义层面：

- routing policy 只能是 `adaptive_p2c` 或 `round_robin`。
- `max_attempts >= 1`。
- `budget_ratio` 在合理范围内，比如 0 到 1。
- `min_budget >= 0`。
- `window_seconds > 0`。
- `per_try_timeout_millis > 0`。
- 状态机阈值要有顺序，比如 recovery < degraded < eject。
- ejection duration 不能是负数。
- circuit breaker 上限要大于 0。

安全层面：

- 非幂等方法默认不允许开启 retry，除非显式声明有 idempotency key。
- 禁止一次策略把某个服务所有 endpoint 都不可路由。
- 禁止把 retry budget 配得过大。

发布层面：

- reload 失败保留旧策略。
- dry-run 先评估会影响哪些服务和方法。
- 灰度发布给少量客户端。
- 有自动回滚条件，比如 p99、error rate、retry amplification 超过阈值。

当前项目已经做到“解析失败不替换旧 map”。要往生产走，validation 是下一步必须补的。

## 拓展

### Q357【拓展】动态配置系统通常需要哪些能力：灰度、生效范围、回滚、审计、权限？

一个能上线的动态配置系统，至少要有这些能力。

灰度：不能一改配置就全量推给所有客户端。应该支持按服务、调用方、实例比例、region、环境逐步放量。

生效范围：策略要明确作用于哪些服务、哪些 method、哪些 caller、哪些 tenant。没有 scope 的配置很危险，容易误伤整个系统。

回滚：每次发布都应该能一键回到上一个稳定版本。更准确地说，是用一个新 version 发布旧内容，而不是让客户端回退到旧 version。

审计：要记录谁改了配置、什么时候改、改了什么、为什么改、审批记录是什么。故障复盘时这些信息很关键。

权限：不是所有人都能改所有服务的 retry、timeout 和熔断。比如订单服务的写接口 retry 策略，权限应该更严格。

校验：发布前要验证 schema、字段范围、策略冲突和风险。

观测：要知道每个客户端当前拿到哪个 policy version，更新是否成功，失败多久了。

AegisMesh 当前实现的是最小可用版本：YAML -> PolicyService -> SDK watcher。它已经能说明动态策略闭环，但离平台级配置系统还差灰度、权限、审计和回滚。

### Q358【拓展】xDS LDS/RDS/CDS/EDS 的分层思想能否迁移到 AegisMesh？

可以迁移，而且很适合用来拆清楚控制面边界。

Envoy/xDS 里常见分层是：

- LDS：listener，控制入口监听。
- RDS：route，控制路由规则。
- CDS：cluster，控制上游集群配置。
- EDS：endpoint，控制具体 endpoint 列表。

映射到 AegisMesh，可以这样理解：

- RDS 对应 method route policy，比如 retry、timeout、canary、method-level rule。
- CDS 对应服务级策略，比如 routing policy、circuit breaker、retry budget。
- EDS 对应 RegistryService 返回的 instance list、endpoint state、slow_score、weight。
- LDS 在 SDK mesh 里不明显，因为没有独立 sidecar listener；如果改成 sidecar 模式，就会出现 LDS。

AegisMesh 当前是自定义的 RegistryService、PolicyService、TelemetryService，没有实现 xDS。但分层思想可以借鉴：endpoint 更新不要和 retry policy 混在一起，route policy 不要和服务发现混在一起。

如果未来要接 Envoy 或 gRPC xDS client，可以让 AegisMesh Controller 生成 xDS 资源，保留 slow_score 和状态机作为策略生成逻辑。

### Q359【拓展】配置中心推送和客户端拉取各有什么一致性问题？

客户端拉取简单，但会有延迟。比如每 3 秒拉一次，策略变化最多要等 3 秒才看到。客户端很多时，拉取还会给配置中心带来周期性压力。

推送响应快，但要处理连接状态、重连、消息顺序和版本去重。客户端断线期间错过了更新，重连后必须拿到最新全量快照，而不是只等下一次增量。

AegisMesh 的 `WatchPolicy` 更接近推送，但 Controller 内部仍然是 ticker 检查 YAML 文件变化。它是一个混合模型：

```text
Controller 轮询本地文件
-> WatchPolicy stream 推给 SDK
```

一致性问题主要有：

- 客户端之间收到新策略的时间不同。
- stream 断线期间客户端使用旧策略。
- 多 Controller 场景可能发送不同版本。
- 旧 version 可能覆盖新 version。

解决办法还是 version。每个 snapshot 要有单调版本，客户端只接受更新版本；重连后先拿当前全量快照；控制面要能回答“哪些客户端已经生效到哪个版本”。

### Q360【拓展】如何支持按调用方、租户、region、method 的多维策略？

需要把 policy 从简单的 service map 变成带 match 条件的规则集。

一个可能的结构是：

```yaml
services:
  user-service:
    defaults:
      retry:
        enabled: true
        max_attempts: 2
    rules:
      - match:
          source: checkout-service
          method: /demo.shop.v1.UserService/GetUser
          region: cn-east
        retry:
          budget_ratio: 0.1
          per_try_timeout_millis: 120

      - match:
          tenant_tier: free
          method: /demo.shop.v1.UserService/Search
        rate_limit:
          rps: 50
```

匹配维度可以来自：

- SDK source
- destination service
- gRPC method
- request metadata
- tenant ID
- region / zone
- workload version

但维度不能无限加。维度越多，规则冲突越多，缓存和计算也越复杂。

我会先支持 source + method + region，因为它们和 RPC 治理关系最直接。tenant 维度要看业务是否已经有稳定的身份体系。

### Q361【拓展】如果策略冲突，优先级应该如何定义？

优先级必须固定，否则排查会非常痛苦。

我会用这样的顺序：

```text
安全兜底策略
> 显式方法级策略
> 调用方/租户/region 特定策略
> 服务级策略
> 全局默认策略
```

安全兜底策略最高。比如非幂等写方法默认不重试，这类保护不应该被一个宽泛的服务级 retry 打开。

方法级策略通常比服务级策略更具体。`CreateOrder` 的 retry off 应该覆盖 order-service 的默认 retry。

调用方和租户策略要看业务定义。比如 VIP 租户可能有更高限流额度，但不能绕过幂等性安全规则。

还要定义字段合并规则。有些字段是覆盖，比如 `max_attempts`；有些字段是取更保守值，比如 timeout 可以取更小，retry budget ratio 可以取更小；有些字段不能同时存在，比如两个 routing policy 冲突时必须报错。

好的策略系统还应该给出解释：某个请求最终用了哪几条规则，为什么。这比单纯告诉你“最终 max_attempts=2”有用。

### Q362【拓展】如何做策略变更的 dry-run 和 shadow evaluation？

dry-run 是“评估但不执行”。

比如准备把 `user-service` 的 `eject_threshold` 从 2.5 改成 1.8。dry-run 可以拿过去 30 分钟的 telemetry 回放，看如果用新阈值，会有多少 endpoint 被标记为 EJECTED。

shadow evaluation 是运行时同时算新旧策略，但只执行旧策略。

SDK 或 Controller 可以记录：

```text
actual_decision = old_policy.pick(endpoint_a)
shadow_decision = new_policy.pick(endpoint_b)
```

但真正请求仍然按旧策略走。这样能观察新策略会不会更激进、是否会摘除太多 endpoint、是否会增加 retry。

对 AegisMesh 来说，可以做几个 shadow 指标：

- `shadow_slow_score`
- `shadow_endpoint_state`
- `shadow_retry_allowed`
- `shadow_route_weight`
- `shadow_budget_denied`

然后比较新旧策略下的预期影响。只有 dry-run 和 shadow 都没明显风险，再灰度给少量客户端真实执行。

### Q363【拓展】如何把策略和实验平台结合，自动评估治理效果？

可以把每次策略变更当成一次实验。

实验平台负责分组：

- control：旧策略。
- treatment：新策略。

AegisMesh policy 里可以加 experiment ID 和 variant。SDK 拿到策略后，把 variant 写入 telemetry 和 trace。实验脚本或平台再按 variant 聚合指标。

评估指标可以包括：

- p95/p99 latency
- error rate
- retry amplification
- slow_score
- endpoint state churn
- PROBING 流量占比
- EJECTED 持续时间
- 用户侧成功率

自动化流程可以是：

1. 新策略先 dry-run。
2. 通过后给 1% 客户端。
3. 观察 10 到 30 分钟。
4. 如果 p99、错误率、retry amplification 都没恶化，扩大到 10%。
5. 指标恶化就自动回滚。

这个方向能把项目从“手动实验”推进到“策略闭环评估”。AegisMesh 现在已经有实验脚本、CSV checker、verifier 和 dashboard，后续可以把 policy version 接进这些结果里。

### Q364【拓展】如果 policy 更新导致事故，如何快速回滚并证明影响范围？

先回滚，再复盘。

回滚时不要让客户端回到旧 version，而是发布一个新 version，内容恢复到上一个稳定策略。这样 SDK 的 version 判断不会混乱，WatchPolicy 也能正常推送。

快速动作应该是：

1. 从审计记录里找到最后一个稳定策略。
2. 用新 version 发布旧内容。
3. WatchPolicy 推送给 SDK。
4. 观察 p99、error rate、retry amplification、endpoint state 是否恢复。
5. 如果 watcher 不稳定，重启 Controller 或让 SDK 重建连接。

证明影响范围需要几类证据。

第一，配置审计：哪个 version 在什么时间发布，改了哪些字段。

第二，客户端生效情况：哪些 source、哪些服务、哪些实例收到了这个 version。当前项目还没有完整的 policy version metric，这是可以补的。

第三，业务指标：事故时间段内哪些 method 的 p99、error rate、retry attempts 变坏。

第四，治理指标：slow_score、state、in-flight 是否出现异常变化。

第五，trace 或 verifier：哪些真实请求使用了受影响策略，路由到了哪些 upstream。

当前 AegisMesh 可以用日志、Prometheus、实验 CSV 和 trace JSONL 做一部分证明。生产化后，我会补 `aegis_policy_version{source,service}` 这类指标，以及策略审计日志。
