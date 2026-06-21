# 21. 测试、CI 与代码质量

## 简单

### Q561【简单】项目如何验证 go test ./... 通过？

本地最直接的验证方式是：

```bash
go test ./...
```

项目的 `Makefile` 里也有 `test` target，本质上执行的就是 `go test ./...`。所以本地可以用：

```bash
make test
```

CI 里也会跑同样的检查。当前 `.github/workflows/ci.yml` 使用 `actions/setup-go@v5` 安装 Go 1.23.x，然后执行三步：`go test ./...`、`go vet ./...`、`python experiments/scripts/check_results.py --results experiments/results/combined --allow-partial`。前两步验证 Go 代码能编译、单元测试通过、静态检查没有明显问题；第三步验证实验结果目录的 schema 和基础结果检查脚本能跑。

我在面试里会强调一点：`go test ./...` 是代码正确性的底线，不等于实验结论已经复现。benchmark、Docker Compose、eBPF、故障注入这些要分到集成测试或实验环境里跑。

### Q562【简单】哪些模块最适合写单元测试？

最适合写单元测试的是纯逻辑模块，因为它们不依赖 Docker、网络和真实时间。

当前项目里比较适合单测的模块有：

1. `pkg/fault`：slow_score、HealthManager、Endpoint StateMachine。输入 telemetry 和配置，输出分数和状态。
2. `pkg/retry`：retry budget 的窗口、比例、min budget。
3. `pkg/circuitbreaker`：Acquire、release、最大 inflight、重复 release。
4. `pkg/registry`：memory registry、file registry、TTL、heartbeat、snapshot restore。
5. `pkg/telemetry`：EWMA、Recorder、p95、SnapshotAndReset、Prometheus 指标。
6. `pkg/routing`：round robin、adaptive P2C、PROBING ratio、cost 选择。
7. `pkg/verifier`：YAML spec、JSONL trace、route distribution、forbidden edge。
8. `pkg/policy`：YAML policy reload、method policy、idempotent retry。
9. `sdk/go/aegisgrpc`：resolver 地址转换、retry interceptor、telemetry interceptor、policy 应用、adaptive picker。
10. `agent/ebpf`：event parser、endpoint mapping、aggregator、reporter，不强依赖真实内核的部分。

这些模块的共同点是输入输出明确。真正需要容器、tc、eBPF capability 的测试，应该放在集成实验里，不要拖慢普通 CI。

### Q563【简单】StateMachine 的测试用例应该覆盖哪些状态迁移？

StateMachine 至少要覆盖这几条路径。

第一条是 `HEALTHY -> DEGRADED`。slow_score 超过 degraded threshold，但要连续 N 个窗口才降级。项目现有测试里已经验证了前两个慢窗口仍然 HEALTHY，第三个慢窗口才变成 DEGRADED。

第二条是 `HEALTHY/DEGRADED -> EJECTED`。slow_score 持续超过 eject threshold，说明 endpoint 已经不是轻微退化，而是需要被摘除。

第三条是 `EJECTED -> PROBING`。EJECTED 不是永久状态，过了 `EjectionDuration` 后要进入 PROBING，给少量探测流量一个恢复机会。

第四条是 `PROBING -> HEALTHY`。probe 期间 slow_score 低于 recovery threshold，success rate 达到 probe success threshold，说明它可以恢复。

第五条是 `PROBING -> EJECTED`。探测失败时不能直接放回 HEALTHY，要重新隔离。

还应该补一些边界：slow_score 在阈值附近抖动时不应频繁切换；没有流量时是否只靠 tick 推进 EJECTED 到 PROBING；`LastTransitionAt` 和 `EjectedAt` 是否更新正确；所有 endpoint 都被摘除时是否有 fail-open 或 least-bad fallback。

### Q564【简单】Retry Budget 的测试如何模拟时间窗口？

项目的 retry budget 支持注入时间函数。`BudgetConfig` 里可以传 `Now func() time.Time`，测试里定义一个变量 `now`，然后让 `Now` 返回这个变量。

示例思路是：

```go
now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
budget := NewBudget(BudgetConfig{
    BudgetRatio: 0.1,
    MinBudget: 1,
    Window: 10 * time.Second,
    Now: func() time.Time { return now },
})
```

测试窗口重置时，不需要 `time.Sleep(11 * time.Second)`，直接：

```go
now = now.Add(11 * time.Second)
```

这样测试快、稳定，也不会因为 CI 机器慢导致偶发失败。当前 `pkg/retry/budget_test.go` 就是这种写法。

### Q565【简单】Registry TTL 测试如何避免真实 sleep？

registry 也支持注入 `now func() time.Time`。创建 `NewMemoryRegistry` 时传一个 fake clock，注册实例后手动推进 `now`，再调用 `SweepExpired` 或 `List`。

例如注册一个 TTL 5 秒的实例，然后：

```go
now = now.Add(6 * time.Second)
expired := reg.SweepExpired(context.Background())
```

这样可以验证实例过期，不需要真的等 6 秒。Heartbeat 测试也一样：先推进到快过期，调用 `Heartbeat` 延长 lease，再继续推进时间，确认实例还活着。

这种设计比在测试里 sleep 好很多。真实 sleep 会拖慢 CI，也容易因为调度抖动导致测试不稳定。

### Q566【简单】Verifier 的正向和反向测试分别是什么？

正向测试是给一组符合期望的 trace，验证 verifier 通过。比如 spec 要求 `user-service:primary` 75%、`user-service:secondary` 25%，容忍度 1%，trace 里刚好是 3 条 primary、1 条 secondary，并且 retry attempts 没超过上限，也没有 forbidden edge。报告应该 `Passed=true`。

反向测试是故意构造违规 trace，确认 verifier 能报错。比如期望 50/50，但 trace 全部打到 primary；某条 trace 的 retry attempts 超过 `max_retry_attempts`；path 里出现 `frontend->payment-service` 这种 forbidden edge。报告应该失败，并且失败项里能看到 route distribution、retry budget、forbidden edges。

这两类测试都要有。只有正向测试会让 verifier 看起来能跑，但不能证明它真的能抓住错误。当前 `pkg/verifier/spec_test.go` 已经覆盖了通过和失败两种情况。

### Q567【简单】BPF 在非 Linux host 上如何保持测试可运行？

eBPF 的真实加载依赖 Linux 内核和权限，不能要求 Windows 或 macOS 开发机也跑完整 BPF 程序。项目需要把测试分层。

普通单元测试只测不依赖内核的部分，比如 BPF C 源码是否声明了需要的 kprobe/kretprobe 和 map，event parser 是否能解析事件，endpoint mapper 是否能把地址映射到 service/instance，aggregator 是否能把 TCP 信号聚合成 telemetry sample。

真实加载测试要加平台限制。非 Linux host 上，加载路径应该返回 `ErrUnsupportedPlatform` 或被 build tag 排除。这样 `go test ./...` 在普通开发机和 GitHub Actions 上仍然能过。

换句话说，CI 默认验证“代码结构和用户态逻辑没坏”；真实内核行为放到 Linux 实验机或专门的 eBPF integration job。

### Q568【简单】fault injector 为什么适合测试 command builder？

fault injector 的核心风险不是 Go 算法复杂，而是生成的系统命令错了。比如 `tc qdisc add` 参数顺序错、container name 映射错、delay/jitter/loss 组合错，实验结果就会偏掉。

直接在单元测试里执行 `tc` 或 `docker update` 不合适。它需要 root、会改机器网络状态，还可能污染后续测试。更好的做法是把命令构造逻辑抽出来，只测试 command builder：输入 kind、container、device、delay、loss、cpus，输出期望命令。

这样测试快，也不会碰真实环境。真正执行故障注入，只在实验脚本里跑，并且要有 `reset_faults.sh` 做清理。

### Q569【简单】policy reload 测试应该验证哪些字段？

policy reload 测试要覆盖服务级策略和方法级覆盖。

服务级字段包括 `routing_policy`、retry enabled、max attempts、budget ratio、min budget、window seconds、per-try timeout、outlier detection 阈值、circuit breaker max inflight。

方法级字段要重点测 `idempotent`、method timeout 和 method retry override。比如 `GetUser` 可以是幂等且允许重试，`CreateOrder` 应该标成非幂等并限制重试。

还要验证 reload 行为：文件没变时不应该无意义更新 revision；文件变了以后 `ReloadIfChanged` 能加载新 snapshot；新 snapshot 应该原子替换旧 snapshot；坏 YAML 不应该把旧可用策略清掉。

当前 `pkg/policy/store_test.go` 已经验证了 YAML 加载、service policy、method policy 和 retry 字段。后续可以补坏配置和回滚类测试。

### Q570【简单】benchmark 结果和 schema 文件为什么不能混淆？

schema 文件只说明结果应该长什么样，不代表已经跑出了结果。

比如 `experiments/results/latency_schema.csv` 只有表头：experiment、variant、latency_p99_ms、error_rate 等字段。它用于约束实验输出格式，方便脚本检查和合并。真正的 benchmark 结果应该在 `experiments/results/runs/...` 或 `experiments/results/combined` 里，有具体的实验行、时间窗口、请求数、p99、retry amplification、recovery state。

如果把 schema 当成结果，就会在报告里写出没有证据的结论。项目里 `check_results.py` 和 `merge_results.py` 的作用就是把真实 run 目录合并、检查 required comparison 是否都有 evidence rows。

面试里要说清楚：schema 是格式合同，results 是实验证据。

## 深度

### Q571【深度】如何用 fake clock 测试 EjectionDuration 和 retry window？

两者思路一样：把时间作为依赖注入，不要在测试里等真实时间。

retry budget 已经这么做了。`BudgetConfig.Now` 返回 fake clock，测试里推进 `now`，触发窗口重置。这能准确测 `Window=10s` 时第 11 秒是否重新放开预算。

StateMachine 当前的 `Apply` 输入里有 `StateInput.Now`，这本身就是 fake clock。测试 `EjectionDuration` 时，可以先把 endpoint 打到 EJECTED，记录开始时间，然后传入 `now.Add(29*time.Second)`，确认还没 PROBING；再传 `now.Add(31*time.Second)`，确认进入 PROBING。

这比 sleep 更好，因为状态机测试要的是逻辑时间，不是真实时间。真实时间只会让测试变慢，还会引入 CI 调度噪声。

### Q572【深度】adaptive P2C 的随机算法如何做确定性测试？

P2C 会随机选两个候选 endpoint，再按 cost 选更好的那个。测试时如果真的用随机数，结果会飘。所以 adaptive picker 里要让随机源可替换。

项目里 `adaptivePickerBuilder` 支持传入自定义 random，测试用 `sequenceRandom` 固定返回 `[0, 1]`。这样每次都选中同一对候选，再验证 slow_score 或 cost 更低的 SubConn 被选中。

除了固定随机源，还可以直接测试 routing 层的纯函数。给定两个 endpoint：一个 slow_score 0.1，一个 slow_score 3.0；一个 inflight 低，一个 inflight 高；然后断言 cost 低的被选中。

要测 PROBING ratio，也应该用可控随机源。比如设置 probe ratio 为 2%，构造 1000 次 pick，固定随机序列，断言 PROBING endpoint 被选中的次数不超过上界。随机算法不是不能测，重点是把随机性变成可重复输入。

### Q573【深度】telemetry p95 计算如何验证边界样本数？

p95 的边界样本数很容易出错，尤其是 0 个、1 个、2 个、少量样本和刚好跨 percentile 索引的情况。

应该至少测这些 case：

1. 没有 latency 样本时，p95 应该是 0 或空值，不能 panic。
2. 只有 1 个样本时，p95 等于这个样本。
3. 2 个样本时，项目当前实现如果用 nearest-rank，p95 会落到较大值。
4. 样本无序输入时，输出应等价于排序后计算。
5. 有重复值时，p95 不应因为重复而偏移。
6. SnapshotAndReset 后，下一窗口的 p95 不应带上上一窗口样本。

当前 `recorder_test.go` 用 100ms 和 300ms 两个样本验证 p95 为 300ms，已经覆盖了一个小样本边界。后续可以补表驱动测试，把 percentile 函数单独喂多组样本。

高 QPS 下还要测性能，但那属于 benchmark，不应该混到普通单元测试里。

### Q574【深度】Resolver 需要如何 mock gRPC Controller？

resolver 要测两类东西：target 解析和从 Controller 拉实例后的地址转换。

最简单的单元测试是不起 gRPC server，直接测 `parseTarget` 和 `instancesToAddresses`。比如给一个 `aegis://127.0.0.1:9000/user-service`，确认 controller address 和 service 解析正确；给一组 ServiceInstance，确认 HEALTHY、DEGRADED、PROBING 被加入地址列表，EJECTED、DEAD 被过滤，attributes 里带 instance_id、status、slow_score。

如果要测试完整 resolver loop，就起一个 bufconn gRPC server。bufconn 是 gRPC 提供的内存连接，不需要真实端口。测试里实现一个 fake `RegistryServiceServer`，让 `ListInstances` 返回固定实例或返回错误，再观察 resolver 的 `UpdateState` 或 `ReportError`。

还要控制时间。resolver 默认 3 秒 refresh，不适合测试。可以把 refresh interval 注入成很短，或者直接调用 `ResolveNow`。测试要避免真实 sleep 和真实网络。

### Q575【深度】Policy Watch stream 断线重连如何测试？

可以用 fake PolicyService server 模拟三种行为。

第一种，正常发送 initial snapshot，然后保持 stream。SDK watcher 应该把 snapshot 写进 policyManager。

第二种，server 发送一次 snapshot 后主动返回错误，比如 `Unavailable`。SDK watcher 应该等待 backoff，再重新建立 WatchPolicy。测试里可以把 backoff 配成很短，或者把 backoff 抽成可注入 timer。

第三种，server 返回 `Unimplemented` 或 `NotFound`。这不应该触发疯狂重连。老 Controller 没有 PolicyService 时，SDK 应该降级到默认策略。

断线重连测试要避免真实时间。可以把 watcher 的 dialer、backoff、timer 都抽接口，或者在测试里用很小的 interval 加 context timeout。还要检查 goroutine 是否退出：测试结束 cancel context，watcher 不能残留。

当前项目的 watcher 逻辑已经有 backoff 和 context，但要做更细测试，最好继续拆出可注入组件。

### Q576【深度】并发测试如何覆盖 Budget、Breaker、Recorder 的 race condition？

这三个模块都在请求路径上，会被多个 goroutine 同时调用。单元测试要配合 race detector。

Budget 可以开很多 goroutine，同时执行 `RecordOriginal`、`AllowRetry`、`RecordRetry`、`Snapshot`。最后检查 retryRequests 不超过预算上限，窗口切换时没有负数或计数错乱。

Breaker 可以并发调用 `Acquire`，成功的 goroutine 持有 release，再并发释放。要验证最大 inflight 不超过 `MaxInflightPerEndpoint`，重复 release 不会让 inflight 变成负数。

Recorder 可以并发 `Start`、finish、`Observe`、`Snapshot`、`SnapshotAndReset`。重点是 race detector 不报数据竞争，inflight 最终回到 0，request count 不丢太多，SnapshotAndReset 后窗口清空。

命令上用：

```bash
go test -race ./pkg/retry ./pkg/circuitbreaker ./pkg/telemetry ./sdk/go/aegisgrpc
```

并发测试不要只看一次通过。可以加：

```bash
go test -race -count=100 ./pkg/retry
```

这样更容易抓到偶发问题。

### Q577【深度】实验脚本如何防止历史结果污染当前结论？

最重要的是每次实验要有独立 run directory。项目里有 `RUN_ID` 和 `RUNS_DIR`，脚本会把结果写到 `experiments/results/runs/<run-id>-...`。这样不会把新旧 CSV 混在同一个文件里。

第二是合并时要显式输入。`make merge-results` 会把历史 runs 合并到 `experiments/results/combined`，然后 `check_results.py` 检查是否有 required evidence rows。写报告时应该引用 combined 的统计结果，而不是手动挑某一次好看的 run。

第三是脚本要先 reset faults。delay、loss、CPU throttle 如果没清掉，会污染后面的 baseline。实验脚本应该在开始前和结束后都调用 reset，失败时也尽量清理。

第四是结果文件要有 schema。latency、retry、recovery 三类 CSV 的表头固定，分析脚本才能知道哪些列可比。

第五是元数据。run directory 里应该有 run_meta，记录参数、时间、目标容器、延迟、jitter、concurrency。否则几天后很难判断某个结果是怎么跑出来的。

### Q578【深度】CI 中能否跑 Docker Compose benchmark？成本和稳定性如何？

能跑，但不建议放在每次 push 的普通 CI 里全量跑。

Docker Compose benchmark 有几个问题。启动慢，拉镜像和 build 时间长；GitHub hosted runner 性能不稳定，p99 latency 很容易受邻居任务影响；tc、eBPF、CPU throttle 需要权限，普通 runner 不一定支持；实验失败时排查成本比单元测试高很多。

更合理的分层是：普通 CI 跑 `go test ./...`、`go vet ./...`、schema check、轻量脚本 smoke test。夜间任务或手动 workflow 跑 Docker Compose benchmark。真实 eBPF 和 fault injection 放到自建 Linux runner，机器配置固定，权限可控。

如果一定要在 CI 跑 Compose，也只跑最小 smoke：服务能启动，`/checkout` 能返回，verifier 能读真实 trace，`check_results.py --allow-partial` 能过。不要在普通 CI 里要求 p99 必须降低 90%，这种性能断言太容易被环境噪声打掉。

### Q579【深度】如何将大型 benchmark 与快速单元测试分层？

我会分四层。

第一层是快速单元测试。纯 Go 逻辑，不依赖网络和 Docker，几秒到几十秒内跑完。每次提交都跑。

第二层是组件测试。用 bufconn、httptest、临时文件、fake clock、fake Controller 测 SDK 和 Controller 的交互。仍然不启动完整 Docker Compose。

第三层是集成 smoke。启动 demo compose，发少量请求，确认 Controller、registry、SDK、trace、metrics 都能连起来。这个可以在 PR 上按需跑。

第四层是 benchmark 和 chaos。包含慢实例 delay、CPU throttle、retry amplification、recovery curve、probe ratio、absolute SLO、eBPF packet loss。它们耗时长，对环境敏感，适合 nightly、自建 runner 或发布前手动触发。

这样做的好处是反馈快。日常开发大部分错误在第一、二层就能抓到，真正昂贵的实验只用于验证系统效果。

### Q580【深度】如果测试通过但实验失败，你会如何定位是代码问题还是环境问题？

我会先把问题分成“功能没生效”和“性能结果不符合预期”。

如果功能没生效，比如 recovery.csv 没有状态迁移，先看 Controller logs、telemetry rows、health state、trace log。确认 SDK 是否上报了 stats，Controller 是否收到了样本，slow_score 是否超过阈值，resolver 是否拿到了状态，fault 是否真的注入到目标容器。

如果功能生效但性能不符合预期，比如 adaptive P2C 没比 round_robin 好，先看实验环境：负载是否足够、故障是否足够强、目标容器是否正确、连接是否复用、系统 CPU 是否打满、其他实验残留的 tc 是否没清。

然后看可复现性。用同样参数重复跑 3 到 5 次，观察中位数和方差。如果只有一次失败，可能是环境抖动；如果稳定失败，才更像代码或实验设计问题。

最后用小化实验定位。不要一上来跑完整矩阵。先只启动 controller、user-a、user-b、frontend-adaptive，注入 delay，跑 sustained load，看 trace 是否绕开慢实例。把问题缩到最小，才容易判断是代码错还是环境错。

## 拓展

### Q581【拓展】属性测试/property-based testing 能用于哪些模块？

属性测试适合那些有清晰不变量的模块。AegisMesh 里有不少。

Registry 可以测：注册后 List 能看到；TTL 过期后 List 看不到；Heartbeat 只能延长已存在实例；List 返回顺序稳定；返回的 labels 被修改不影响内部状态。

Retry Budget 可以测：retryRequests 永远不超过 `max(minBudget, floor(original*ratio))`；窗口推进后预算重置；ratio 为 0 时只能靠 min budget；并发下计数不为负。

StateMachine 可以测：状态只能在合法集合里；EJECTED 未到时间不能进入 PROBING；PROBING 失败一定回 EJECTED；slow_score 持续低于 recovery threshold 不应被错误 EJECTED。

Verifier 可以测：route distribution 的比例总和接近 1；forbidden edge 出现必然失败；retry attempts 超过上限必然失败。

Policy parser 可以测：随机缺字段时默认值不会 panic；method policy 覆盖 service policy 的规则保持一致。

Go 里可以用 `testing/quick` 或第三方 property testing 库。先从小模块做，不要一开始就对整个系统做随机测试。

### Q582【拓展】模型测试/model checking 如何验证状态机？

Endpoint 状态机很适合模型测试，因为状态数量有限：HEALTHY、DEGRADED、EJECTED、PROBING、DEAD。输入也可以抽象成几类：score 低、score 中、score 高、probe 成功、probe 失败、时间到、时间未到。

可以先写一个简化模型，枚举所有可能输入序列，比如长度 5 或 10 的事件序列。每一步都检查不变量：

1. EJECTED 未过 ejection duration 不能直接 HEALTHY。
2. PROBING 成功才能 HEALTHY。
3. PROBING 失败回 EJECTED。
4. 连续窗口不足时不能 DEGRADED。
5. 不存在未知状态。

模型测试不一定要用很重的工具。Go 里可以写枚举测试或 property-based test。更严格的话，可以用 TLA+ 建模状态机，验证没有死锁状态、没有无法恢复的路径。

对 AegisMesh 来说，最值得验证的是 hysteresis 和 PROBING：既不能抖动，也不能永远恢复不了。

### Q583【拓展】Golden file 测试适合 verifier report 和 policy rendering 吗？

适合，但要控制好边界。

Verifier report 很适合 golden file。给定固定 spec 和固定 trace，输出的 report 应该稳定。测试可以把 report JSON 或 Markdown 和 `testdata/*.golden` 对比。一旦报告格式变化，diff 很直观。

Policy rendering 也适合。比如 YAML policy 解析后再渲染成 PolicySnapshot JSON，和 golden 对比，能防止字段丢失、默认值变化、method override 规则被改坏。

但 golden file 不适合带时间戳、随机 ID、绝对路径、map 随机顺序的输出。要么先规范化，要么在 golden 里只比稳定字段。否则每次测试都在因为无关变化更新 golden，时间久了没人信这个测试。

我会把 verifier report、policy snapshot、experiment summary 这类人读产物放 golden file；把性能数字和真实时间窗口排除掉。

### Q584【拓展】如何用 fuzzing 测试 YAML parser、JSONL trace parser、proto handler？

Fuzzing 适合找 panic、越界、死循环和异常输入处理问题。

YAML parser 可以 fuzz `policy.NewFileStore` 背后的解析逻辑。输入随机 YAML，要求结果只有两种：成功返回合法 snapshot，或者返回可解释 error。不能 panic，也不能生成明显非法策略，比如负 timeout、retry attempts 失控。

JSONL trace parser 可以 fuzz 每一行。随机 JSON、半截 JSON、超长字段、缺字段、extra fields，都应该被安全处理。Verifier 可以跳过坏行或返回错误，但不能崩。

proto handler 可以 fuzz message 字节流，调用 `proto.Unmarshal` 后喂给 validation。比如 `EndpointStatsSample` 的 request_count 负数、window_end 小于 window_start、latency NaN，都应该被拒绝或规范化。

Go 原生 fuzz 可以这样组织：

```go
func FuzzParseTraceRecord(f *testing.F) {
    f.Add([]byte(`{"trace_id":"1","route":"user-service:primary"}`))
    f.Fuzz(func(t *testing.T, data []byte) {
        _, _ = ParseTraceRecord(data)
    })
}
```

一开始不要把 fuzz 放进普通 CI 长时间跑。可以短时 smoke，深度 fuzz 放 nightly。

### Q585【拓展】如何将 performance regression test 纳入 CI？

性能回归测试不能和普通单元测试一样写硬阈值。CI 机器波动太大，直接断言 p99 小于某个固定值会很脆。

比较稳的做法是用相对指标和固定环境。

在普通 CI 里，可以跑微基准：

```bash
go test -bench=. -benchmem ./pkg/telemetry ./pkg/routing ./sdk/go/aegisgrpc
```

保存上一次 main 分支的结果，用 `benchstat` 比较。只对明显回归报警，比如 CPU 或 allocs 增加超过 20%。这类适合 recorder、routing cost、retry budget、verifier parser。

端到端 benchmark 更适合自建 runner。机器固定，Docker 环境固定，CPU governor 固定，背景负载低。每次跑多轮，比较中位数和置信区间。CI 只判断“是否明显变差”，不要把论文式性能结论绑定到每个 PR。

性能回归要和代码变更关联。比如改 Recorder 后重点看 allocations 和 p95 计算；改 balancer 后重点看 Pick 延迟和吞吐；改 trace 后重点看 JSONL 写入开销。

### Q586【拓展】如何做 chaos test 的自动判定，而不是人工看图？

自动判定要把实验目标变成机器可检查的指标。

比如 slow-instance delay 实验，判定条件可以是：adaptive P2C 的 median p99 至少比 round_robin 低 50%，错误率不高于某个上限。retry budget 实验，条件是 with_budget 的 retry amplification 小于 1.2x，without_budget 接近 2.0x。probe ratio 实验，条件是 PROBING endpoint 的 trace 比例低于配置上限。absolute SLO 实验，条件是 enabled 修订 slow_score 超过 1.0 且出现 DEGRADED，disabled 修订不出现状态迁移。

项目里已经有 `check_results.py`、`analyze_probe_ratio.py`、`analyze_absolute_slo.py` 这种方向。后续可以把每个实验的 pass/fail 标准写进 JSON spec：

```json
{
  "experiment": "retry_budget",
  "assertions": [
    {"metric": "retry_amplification", "variant": "with_budget", "op": "<=", "value": 1.2}
  ]
}
```

这样 dashboard 图还是保留给人看，但 CI 或实验脚本能自动给出结论。

### Q587【拓展】如何用 pprof、trace、race、benchstat 形成性能分析闭环？

我会按这个流程走。

先用 benchmark 或实验发现问题。比如改了 Recorder 后，端到端 p99 上升，或者 `go test -bench` 里 allocs 增加。

然后用 `pprof` 定位热点。CPU profile 看时间花在哪，heap/allocs profile 看分配来源，mutex/block profile 看锁竞争。Recorder 可能热点在 percentile 排序和 latency slice，balancer 可能热点在 Pick cost 或 Done 回调。

并发问题用 race。跑：

```bash
go test -race ./pkg/... ./sdk/go/...
```

如果 race 报共享 map 或 protobuf snapshot 被并发读写，要先修 correctness，再谈性能。

微基准结果用 benchstat 比较：

```bash
benchstat old.txt new.txt
```

看 ns/op、B/op、allocs/op 是否真的改善。最后再跑端到端实验，确认微优化没有破坏治理效果。

这才是闭环：实验发现问题，pprof 找原因，race 保证并发安全，benchstat 量化改动，端到端实验确认用户侧指标变好。

### Q588【拓展】如果项目要开源，README、examples、CI badge、release process 还缺什么？

开源需要让别人能在陌生机器上快速跑起来，也能判断项目边界。

README 还可以补：架构图、最小 quickstart、Docker Compose 一键启动、常见故障排查、实验结果如何复现、哪些能力是实验级、哪些不建议生产使用。现在项目文档已经比较多，但开源 README 要更像入口页，少放长篇解释，多给可执行命令。

examples 可以分层：最小 gRPC service 接入 SDK；动态 PolicyService 示例；retry budget 示例；verifier 读取真实 trace 示例；eBPF agent Linux 示例。每个 example 都应该能独立运行。

CI badge 可以显示 Go CI 状态、Go toolchain、license、coverage。CI 里可以补 `go vet`、`staticcheck`、race smoke、proto generation check、Docker compose smoke。不要把长 benchmark 放进默认 badge，否则容易不稳定。

release process 要定义修订号、CHANGELOG、tag、生成二进制和容器镜像、SBOM、checksum。proto API 变更要写 breaking change 说明。eBPF agent 的内核发行要求也要写清楚。

License、CONTRIBUTING、CODE_OF_CONDUCT、SECURITY.md 也要补。尤其 SECURITY.md，要说明发现安全问题怎么报告，因为项目涉及控制面、eBPF 和故障注入。
