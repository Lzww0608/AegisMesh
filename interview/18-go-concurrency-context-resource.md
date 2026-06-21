# 18. Go 并发、上下文与资源管理

## 简单

### Q477【简单】项目中哪些地方用了 mutex？

项目里用锁的地方主要集中在几类共享状态上。

第一类是注册发现和控制面状态。`MemoryRegistry` 用 `sync.RWMutex` 保护服务实例 map，`Register`、`Heartbeat`、`SweepExpired` 需要写锁，`List` 主要是读锁。`FileRegistry` 底层复用 `MemoryRegistry`，加载快照和生成快照时也要拿内存 registry 的锁。`HealthManager` 用 `sync.RWMutex` 保护 endpoint health map，因为 Controller 会同时接收 telemetry、更新状态机、被 resolver 查询健康状态。

第二类是 SDK 里的运行时状态。`Recorder` 用一个 `sync.Mutex` 保护按 destination、method、upstream 聚合的统计行，包括请求数、错误数、inflight、EWMA 和 latency 样本。`retry.Budget` 用 `sync.Mutex` 保护窗口内的 original request 和 retry request 计数。`circuitbreaker.Breaker` 用 `sync.Mutex` 保护每个 endpoint 的 inflight map。adaptive balancer 里每个 endpoint 的 EWMA 用 `sync.Mutex`，inflight 则用 atomic。

第三类是动态配置。SDK 的 `policyManager` 用 `sync.RWMutex` 保存当前 `PolicySnapshot`，文件策略存储 `FileStore` 也用 `sync.RWMutex` 做读写切换。`dynamicRetrySource` 里还有预算对象 map 和修订 map，需要普通 mutex 保护。

还有一些更小的地方，比如 trace JSONL writer 用 mutex 保证多 goroutine 写日志不会交错，Prometheus 默认 collector 和 gRPC resolver/balancer 注册用 `sync.Once`，虽然它不是 mutex，但目的也是保护全局初始化。

### Q478【简单】sync.Once 在 balancer 注册和 release 中分别起什么作用？

这两个场景用 `sync.Once` 的原因不一样。

balancer 注册是全局初始化问题。gRPC 的 balancer builder 是按名字注册到全局 registry 里的，AegisMesh 的 adaptive P2C balancer 只应该注册一次。如果多个 `DialService` 同时创建连接，没有 `sync.Once` 就可能重复注册同名 balancer，轻则行为不可预测，重则直接 panic。`registerBalancerOnce.Do(...)` 把这件事变成进程级的一次性动作。

release 是资源回收问题。`circuitbreaker.Acquire(endpoint)` 成功后会增加这个 endpoint 的 inflight，并返回一个 `release` 函数。这个 release 可能在不同错误路径、Done 回调或者清理逻辑里被调用。用局部的 `sync.Once` 包住 release，可以保证 inflight 只减一次，避免出现负数或者提前释放容量。

项目里还有类似设计：`Recorder.Start` 返回的 finish 函数也用 `sync.Once`。这类模式适合“调用方必须释放，但重复释放不能破坏状态”的场景。

### Q479【简单】context.Context 在 gRPC 调用、resolver、policy watcher 中如何使用？

在业务 gRPC 调用里，`context.Context` 负责传递 deadline、取消信号和 metadata。retry interceptor 会在每次 attempt 上设置 attempt 信息；如果配置了 `PerTryTimeout`，每次尝试都会派生一个带超时的 context。业务方传进来的外层 context 仍然控制整次调用的生命周期。

resolver 里也有 context。自定义 resolver 创建时会拿一个 `context.WithCancel(context.Background())`，`Close()` 时调用 cancel，让后台 refresh goroutine 退出。每次向 Controller 调 `ListInstances` 时，还会派生一个 2 秒 timeout 的 context，避免 Controller 卡住导致 resolver goroutine 被长期挂住。

policy watcher 的 context 用来控制 stream 生命周期。SDK 初始化时先用一个 500ms timeout 拉取 initial policy，然后启动 watcher goroutine。watcher 里 `WatchPolicy` 是一个 server streaming RPC，stream 断了会按 backoff 重连；如果外部 context 被 cancel，watcher 退出，不再重连。

telemetry reporter 也是同样的思路：它用 ticker 周期性上报，一旦 ctx done，就退出循环并关闭到 Controller 的 gRPC 连接。

### Q480【简单】atomic.Int64 为什么适合记录 inflight？

inflight 是一个非常热的计数器。每次 Pick 成功要加一，每次 RPC 完成要减一，P2C 选择 endpoint 时还要读取当前值。如果每次都拿 mutex，负载高的时候会把很短的计数操作变成锁竞争。

`atomic.Int64` 适合这里，是因为 inflight 本身只是一个单独的整数，不需要和其他字段一起保持复杂不变量。加一、减一、读取都可以用 atomic 完成。项目里 `adaptiveEndpointStats` 就是这样设计的：inflight 用 atomic，latency EWMA 因为要读写多个字段，比如 `seen` 和 `ewma`，仍然用 mutex。

还有一个细节：减 inflight 时项目用了 CAS loop，避免重复 Done 或边界情况把值减成负数。这比单纯 `Add(-1)` 更稳一点。

### Q481【简单】goroutine 在 SDK 中有哪些后台任务？

SDK 里主要有三类后台 goroutine。

第一类是 resolver refresh。自定义 resolver 会启动 watch loop，默认每 3 秒向 Controller 拉一次实例列表，把实例地址、状态、slow_score 等信息写进 gRPC resolver state。

第二类是 telemetry reporter。SDK 的 recorder 在请求路径里记录观测数据，reporter goroutine 默认每 5 秒调用 `SnapshotAndReset`，把窗口内 stats 上报给 Controller。

第三类是 policy watcher。启用动态策略后，SDK 会连接 Controller 的 `WatchPolicy` stream，接收策略更新。stream 断开后会按照 backoff 重连。

gRPC 自身还会创建连接管理、HTTP/2 读写等内部 goroutine，但那不属于 AegisMesh 自己管理的业务 goroutine。AegisMesh 需要保证自己启动的 goroutine 都能通过 context 或 Close 退出。

### Q482【简单】defer cancel 的作用是什么？

`defer cancel()` 主要是释放 context 关联的资源，尤其是 timer。比如 `context.WithTimeout` 会创建定时器，如果函数返回时不 cancel，这个定时器要等超时时间到了才释放。短请求里这可能看不出来，高频调用里就会变成资源浪费。

项目里常见的写法是：

```go
ctx, cancel := context.WithTimeout(parent, 2*time.Second)
defer cancel()
```

这种写法适合单次函数调用，比如 resolver 的一次 `ListInstances`、initial policy fetch。retry interceptor 的循环里没有用 defer，而是在每次 invoker 返回后立刻 `cancel()`，这是对的。如果在循环里 defer cancel，所有 timer 都要等整个函数返回才释放，重试次数多时会拖住资源。

### Q483【简单】为什么 map 访问需要锁保护？

Go 的普通 map 不是并发安全的。一个 goroutine 写 map，另一个 goroutine 同时读 map，可能直接触发 `fatal error: concurrent map read and map write`，也可能造成数据竞争。

AegisMesh 里大量状态都放在 map 里，比如 registry 的 `items`、recorder 的 `rows`、health manager 的 health map、retry source 的 budget map、breaker 的 inflight map。这些 map 都可能被多个 goroutine 同时访问：业务请求在记录 telemetry，reporter 在 snapshot；resolver 在拉实例，Controller 在更新健康状态；policy watcher 在更新策略，retry interceptor 在读取策略。

所以项目里有两层保护：内部 map 读写用 mutex 或 RWMutex；返回给外部的 map，比如 labels、policy snapshot，也要 clone，避免调用方拿到内部引用后在锁外修改。

### Q484【简单】为什么 policy snapshot 要 proto.Clone？

protobuf message 在 Go 里本质上是可变对象，里面可能有 slice、map 和嵌套 message。如果 `policyManager.Snapshot()` 直接返回内部指针，调用方就能修改 SDK 当前正在使用的策略。更麻烦的是，watcher goroutine 可能在更新策略，业务请求 goroutine 可能在读取策略，直接共享同一个对象会带来数据竞争。

所以项目里 `policyManager.Update` 保存前会 `proto.Clone(snapshot)`，`Snapshot` 返回时也会 `proto.Clone`。这样内部状态和外部调用方隔离开。代价是多一次拷贝，但 policy 更新和读取的频率远低于每次 RPC 的热路径，这个代价可以接受。

Controller 的 `PolicyService.GetPolicy` 和 `WatchPolicy` 发送 snapshot 前也 clone，理由一样：不要把 store 里的共享对象直接暴露给 gRPC 发送路径。

### Q485【简单】os.ReadFile、os.WriteFile、os.Rename 在 file registry 中如何配合？

file-backed registry 的思路是把内存 registry 定期落成一个 JSON 快照。

启动时用 `os.ReadFile` 读取快照文件，反序列化后恢复还没过期的 instance record。这样 Controller 重启后不会把所有注册信息立刻丢掉。

写快照时不是直接覆盖正式文件，而是先 `os.WriteFile` 写到 `registry.json.tmp` 这类临时文件。临时文件写完整后，再用 `os.Rename(tmp, path)` 替换正式快照。只要 tmp 和正式文件在同一个文件系统上，rename 通常是原子替换。这样可以避免写到一半进程崩溃，留下半截 JSON，把下一次启动也拖坏。

这套机制不是分布式强一致存储，不能替代 etcd，但对单机 Controller 的重启恢复已经够用。

### Q486【简单】sort.Slice 在 registry 和 health list 中为什么有用？

`sort.Slice` 主要是为了让输出顺序稳定。

Go map 遍历顺序是随机的。如果 `ListInstances` 每次都按 map 顺序返回，resolver 看到的地址顺序会抖动，测试结果和 trace 也更难对比。registry 里按 instance ID 排序，health list 里按 service、instance ID 排序，recorder snapshot 里按 destination、upstream、method 排序，都是同一个目的：让结果可预测。

这不直接改变治理算法，但对调试、实验复现、单元测试和文档结果都很有帮助。做系统项目时，这种小的确定性很值钱。

## 深度

### Q487【深度】adaptiveStats sync.Map 按 address 存储，长期运行是否会内存增长？

会有这个风险。当前 adaptive balancer 的 `adaptiveStats` 是包级 `sync.Map`，key 是 endpoint address，比如 `172.18.0.5:7002`。每见到一个新地址，就会创建一个本地 stats 对象，里面记录 inflight 和 latency EWMA。问题是这个 map 目前没有清理逻辑。

在 demo 里地址数量很少，容器也比较稳定，所以这个设计简单有效。但如果放到 Kubernetes 里，Pod 重建、IP 漂移、灰度修订扩缩容都会产生新地址。旧地址不再出现在 resolver 结果里，但它对应的 stats 还留在 `sync.Map` 里。时间长了就会有内存增长。

我会这样改：

1. 让 balancer 在 resolver 更新地址列表时维护活跃地址集合，对不再出现的地址做延迟删除。
2. stats 里记录 lastSeen，每隔一段时间 sweep 超过 TTL 的 entry。
3. key 尽量用 service + instance_id，而不是单纯 IP:port。这样实例重启但 identity 不变时，历史 EWMA 也更容易延续。
4. 给 stats map 加上最大容量，极端情况下优先清理最久未使用的 entry。

所以现在的实现适合实验和单进程 demo，但长期生产运行需要补清理策略。

### Q488【深度】DefaultBreaker 作为包级变量是否会造成跨服务干扰？

有可能。当前 adaptive balancer 里有一个包级 `defaultBreaker`，它按 endpoint 字符串统计 inflight。这样做的好处是简单，同一个进程里所有 ClientConn 都共享一个本地保护器；如果某个 endpoint 已经被打满，其他连接也能看到这个压力。

问题也在这里。它共享得太粗了。

如果两个逻辑服务碰巧使用了相同地址，或者同一个进程里有多个业务客户端对同一个 endpoint 访问模式完全不同，它们会共用一个 breaker 计数。一个高并发调用方可能把另一个低并发调用方也挡掉。测试里也可能出现状态残留，前一个 case 的 inflight 如果因为 bug 没释放，会影响后一个 case。

更稳的设计是把 breaker 放到 balancer builder 或 ClientConn 级别，至少按 service 维度隔离。key 也可以从 address 升级为 `service/address`。如果要共享，也应该是显式配置，比如“同进程同服务共享 breaker”，而不是包级默认行为。

当前实现可以解释为本地保护的 MVP：它能防止单个 endpoint 被过多并发压垮，但隔离粒度还可以再细。

### Q489【深度】policy watcher 每次 watch 都新建 gRPC 连接，有什么资源成本？

`watchPolicyOnce` 每次进入都会 `grpc.DialContext` 到 Controller，stream 结束后 `defer conn.Close()`。正常情况下，`WatchPolicy` 是长连接 stream，一条连接可以维持很久，所以成本不高。只有 Controller 重启、网络抖动、stream 出错时，watcher 才会退出这次 watch，等待 backoff 后重新 dial。

成本主要有几类：TCP 连接和 HTTP/2 连接的建立成本、gRPC 内部 goroutine、文件描述符、TLS 场景下的握手成本。如果 Controller 不可达，大量 SDK 实例又同时重连，就会形成一波连接风暴。项目里默认 backoff 2 秒，能缓和一点，但还不是完整的指数退避。

更好的实现是复用 SDK 到 Controller 的控制面连接。resolver、telemetry reporter、policy watcher 都访问 Controller，可以通过一个控制面 client manager 统一管理连接，内部开多个 RPC。这样连接数少，关闭也好管理。当前写法更清晰，便于把 policy watcher 做成独立模块，但资源效率不是最优。

### Q490【深度】telemetry reporter goroutine 如何保证退出时关闭连接？

SDK 的 `startReporter` 会先 dial Controller，然后启动一个 goroutine。goroutine 里一般是这样的结构：

```go
go func() {
    defer conn.Close()
    reporter.Run(ctx)
}()
```

`reporter.Run(ctx)` 里面是 ticker loop，select 监听 `ctx.Done()`。外部取消 context 后，Run 返回，defer 执行，gRPC 连接关闭。

这说明 reporter 的生命周期依赖外部传进来的 context。如果业务用的是可取消的应用 context，比如服务退出时 cancel，那么 reporter 能正常收尾。如果业务传的是 `context.Background()`，那 reporter 就会跟进程一样长寿，除非进程退出。严格来说，当前 `DialService` 返回的是 `*grpc.ClientConn`，关闭 ClientConn 并不一定能取消 reporter，因为 reporter 用的是外部 ctx。

如果要做得更完整，可以在 SDK 返回一个包装类型，比如 `AegisClientConn`，里面既有 gRPC ClientConn，也有 cancel 函数和 Close 方法。业务调用 Close 时，同时关闭业务连接、resolver、reporter、policy watcher 和 trace writer。

### Q491【深度】Recorder 的锁粒度在高 QPS 下是否会成为瓶颈？

有可能。当前 `Recorder` 用一个全局 mutex 保护所有 stats row。每次请求完成时，要拿锁更新 request count、error count、timeout count、EWMA、latency slice，还会把 latency append 进去。reporter 调 `SnapshotAndReset` 时也要拿同一把锁，并且会复制、排序 latency 样本来算 p95。

在 demo 压测里，这样写没问题。它简单，数据一致性也清楚。但高 QPS 下会有几个压力点：

1. 多个 upstream、method 共享一把锁，热点 endpoint 会拖住其他 endpoint 的记录。
2. 每个 latency 样本都进 slice，窗口内请求越多，占用内存越大。
3. 计算 percentile 要复制并排序样本，CPU 成本会随窗口内样本数量上升。
4. 如果 Prometheus metrics 更新也在锁内做，锁持有时间会变长。

生产化可以改成分片 recorder：按 statsKey hash 到多个 shard，每个 shard 一把锁。latency 可以换成固定 bucket histogram、HDR Histogram 或 DDSketch。这样 p95 不需要保留全部样本，内存上界也更清楚。

### Q492【深度】Budget 的 mutex 是否会限制重试路径性能？

一般不会先成为瓶颈，但在极高 QPS 下会有影响。

retry budget 的操作很短：原始请求进来时 `RecordOriginal`，准备重试时 `AllowRetry`，真正重试前 `RecordRetry`。每次只是检查窗口时间、更新两个计数器、计算允许的 retry 数。mutex 临界区很小。

但如果某个服务方法 QPS 很高，所有请求又共享同一个 method budget，那么这把锁就会被频繁竞争。尤其故障场景下，大量请求同时失败并进入 retry 判断，`AllowRetry` 会变成热点。

可以优化的方向有：

1. 按 method + caller + endpoint 分片，降低单个 budget 热度。
2. 用 atomic 计数配合窗口号，减少锁。
3. 用 token bucket 形式实现预算，后台按 original request 补 token。
4. 在每个 goroutine 或每个 shard 做本地预算，再周期性合并。

当前实现的优点是语义清楚，容易证明 amplification 受控。对这个项目的实验目标来说，优先保证正确性比追求无锁更合适。

### Q493【深度】StateMachine 和 HealthManager 的锁顺序是否可能死锁？

当前设计里死锁风险不高，因为锁层级很简单。

`StateMachine` 本身没有 mutex，它只是一个纯状态转换器。`HealthManager` 有一把 `sync.RWMutex`，`Update` 时先在锁外用 telemetry 计算 slow_score，然后进入写锁，把每个 endpoint 的状态交给 state machine 计算，再更新 health map。`Get` 和 `List` 拿读锁。

这里没有出现“先拿 HealthManager 锁，再拿 StateMachine 锁”的嵌套，也没有反向拿锁的路径，所以不会形成典型的锁顺序死锁。

真正要注意的是未来扩展。比如在持有 HealthManager 写锁时，如果调用 Prometheus、registry、policy store 这类外部组件，而这些组件又反过来查询 HealthManager，就可能出现循环等待。项目现在的做法比较克制：`Update` 返回健康列表后，Controller 再去记录 metrics，这样可以避免在 manager 锁内做太多外部动作。

一句话：当前锁顺序比较干净，但后续加功能时要守住边界，不要在持锁状态下调用复杂外部逻辑。

### Q494【深度】在 Pick 的 Done 回调中更新 EWMA 时，锁竞争会如何影响性能？

`PickResult.Done` 是每次 RPC 完成后都会执行的回调。AegisMesh 在里面做三件事：减少 inflight、把本次 latency 写入本地 EWMA、释放 circuit breaker 的 inflight。inflight 是 atomic，breaker release 会拿 breaker 的 mutex，EWMA 更新会拿 endpoint stats 的 mutex。

如果某个 endpoint 很热，很多请求同时完成，就会在 EWMA 的 mutex 上竞争。不过这个临界区很短，只是读写 `seen` 和 `ewma`，做一次加权计算。一般情况下这点开销能接受。

风险在于 Done 回调处在 gRPC 调用完成路径上，不能做重活。如果在 Done 里排序、上报网络、写文件，尾延迟会被拖高。当前项目把 Done 里的工作控制得比较轻，这个选择是合理的。

进一步优化可以把 EWMA 更新改成分片聚合，或者让 Done 只把样本写入 lock-free queue，由后台 goroutine 批量更新。但那会带来延迟和复杂度。对 adaptive P2C 来说，本地 EWMA 需要比较及时，所以当前的短锁是一个实用折中。

### Q495【深度】如果 context 被 cancel 后 invoker 仍然返回较晚，telemetry 记录会怎样？

telemetry interceptor 是包在 gRPC invoker 外面的。它通常在调用前记录开始时间，等 invoker 返回后再根据 err 记录 status 和 latency。

如果 context 已经 cancel，但 invoker 很晚才返回，那么 recorder 看到的是“从开始到 invoker 返回”的耗时。状态码大概率是 `Canceled` 或 `DeadlineExceeded`，具体取决于 gRPC 返回的 error。这个 latency 可能比 deadline 更长，因为客户端取消只是发出信号，真正返回还要等 gRPC 内部、网络栈或服务端响应路径收尾。

这不是 bug，而是观测语义要说清楚：SDK 记录的是客户端实际等待时间，不是服务端真实执行时间。如果服务端已经处理成功但响应在网络上丢了，客户端仍然可能记录为 timeout。retry 也会基于客户端看到的错误判断。

这也是为什么非幂等方法不能随便重试。客户端看到 timeout，不代表服务端没执行。

### Q496【深度】如何用 Go race detector 验证项目没有数据竞争？

最直接的命令是：

```bash
go test -race ./...
```

在这个项目里，我会先跑两个层次：

```bash
go test -race ./pkg/... ./sdk/go/...
go test -race ./...
```

第一条先覆盖核心库和 SDK，反馈更快。第二条覆盖所有 package。eBPF、Docker 实验、需要 Linux capability 的部分可能不适合在普通本地环境里跑全量 race，这类可以单独放到实验机或者 CI matrix 里。

race detector 能发现的是未同步的并发读写，比如 map 读写没加锁、共享 protobuf message 被多个 goroutine 改。它不能证明没有死锁，也不能证明逻辑上没有资源泄漏。所以我还会配合这些检查：

1. `go test -count=100` 跑并发相关单测，放大偶发问题。
2. `go test -run TestXXX -race -count=100` 针对 resolver、budget、recorder、breaker 做压力测试。
3. 用 `pprof` 的 goroutine profile 看实验结束后 goroutine 是否持续增长。
4. 对 watcher、reporter、resolver 写取消测试，确认 cancel 后能退出。

race detector 是底线检查，不是并发正确性的完整证明。

## 拓展

### Q497【拓展】Go 中 sync.Mutex、sync.RWMutex、sync.Map、atomic 的适用边界是什么？

我一般按共享状态的形态来选。

`sync.Mutex` 适合保护一组需要一起更新的状态。比如 breaker 的 inflight map、retry budget 的窗口计数、recorder 的 stats row。这些状态不是单个数字，通常要先读再改，还要保持不变量，用 mutex 最直接。

`sync.RWMutex` 适合读多写少的场景。registry 的 `List` 远多于 `Register` 和 `Heartbeat` 时，用读写锁能让多个读并发。policy store 也是类似，大部分时候 SDK 在读当前策略，文件 reload 只是偶尔发生。但 RWMutex 不是永远更快，如果写很多或者读临界区很短，普通 Mutex 反而更简单。

`sync.Map` 适合 key 动态变化、读多写少、或者只写一次多次读的场景。adaptive balancer 的 endpoint stats 用它，是因为多个 picker 可能并发获取同一个 address 的 stats，`LoadOrStore` 很方便。但 `sync.Map` 缺点也明显：类型不安全，清理不如普通 map 直观，复杂业务逻辑不好写。

`atomic` 适合单个数值或标志位，比如 inflight 计数、开关状态。它的优势是低开销，但只能处理很简单的不变量。只要多个字段之间需要一致，比如 `ewma` 和 `seen` 一起变化，就不适合硬用 atomic。

在 AegisMesh 里，比较典型的组合是：热路径单计数用 atomic，复杂聚合用 mutex，读多写少状态用 RWMutex，全局动态 stats 用 sync.Map。

### Q498【拓展】如何设计无锁或低锁指标聚合器？

我会先避免追求“完全无锁”，因为指标聚合更需要稳定和可解释。比较实用的是低锁设计。

第一步是分片。按 `destination/method/upstream` 做 hash，把 stats 分到多个 shard。每个 shard 有自己的锁，热点不会把全局 recorder 锁住。Prometheus 采集或 telemetry snapshot 时逐个 shard 取快照。

第二步是把计数和分位数分开。request count、error count、timeout count 可以用 atomic counter。latency 不要把每个样本都 append 到 slice，可以用固定 bucket histogram。每个 bucket 是 atomic counter，窗口结束时读取 buckets 反推 p95。这样内存上界固定，也不需要排序。

第三步是做双缓冲。当前窗口写 active buffer，snapshot 时用一次短锁把 active 和 standby 交换，后台慢慢序列化 standby。这样请求路径只在交换瞬间被挡一下。

第四步是控制分配。Observation 尽量用值类型，labels 和 key 预先规范化。高 QPS 下，频繁分配比锁还容易把 p99 搞差。

如果要再进一步，可以用 per-P 本地缓存或者 ring buffer，把请求路径的数据写到本地 buffer，由后台 goroutine 合并。但这会让指标有一点延迟，也会让代码复杂不少。对 AegisMesh 这种治理系统来说，我会优先做 shard + histogram + double buffer。

### Q499【拓展】Go GC 对高频 telemetry 样本和 latency slice 有什么影响？

当前 recorder 会把窗口内每个请求的 latency 放进 slice，snapshot 时再复制、排序计算 p95。QPS 一高，这个 slice 会变大，扩容和复制都会制造堆分配。窗口结束后 reset，旧数组等 GC 回收。这样 GC 压力会上升，最终可能反过来影响业务请求 p99。

trace JSONL 也有类似问题。每次请求写 trace record，JSON 编码、字符串、metadata 都可能分配。实验里这很好用，因为它能支持 verifier 和报告；生产里如果 100% trace，就要考虑采样和异步写。

GC 对 AegisMesh 的影响有两层：

1. SDK 自身的 GC pause 会进入业务进程，可能影响客户端侧延迟。
2. telemetry 统计如果因为 GC 抖动变慢，控制面看到的 latency 也会带上观测端的噪声。

优化方式包括：用 histogram 替代 latency slice，减少每请求分配；trace 做采样或批量写；复用 buffer 时谨慎使用 `sync.Pool`；通过 `pprof heap` 和 `allocs` 找真实分配热点，而不是靠猜。

### Q500【拓展】如何用 pprof 定位 balancer 或 recorder 的 CPU/内存瓶颈？

我会先在 demo 进程里暴露 `net/http/pprof`，然后用稳定压测复现问题。比如先跑 adaptive P2C 场景，再抓 30 秒 CPU profile：

```bash
go tool pprof http://127.0.0.1:6060/debug/pprof/profile?seconds=30
```

如果怀疑 recorder 内存压力，就抓 heap 和 allocs：

```bash
go tool pprof http://127.0.0.1:6060/debug/pprof/heap
go tool pprof http://127.0.0.1:6060/debug/pprof/allocs
```

如果怀疑锁竞争，要打开 mutex profile：

```go
runtime.SetMutexProfileFraction(1)
runtime.SetBlockProfileRate(1)
```

然后看：

```bash
go tool pprof http://127.0.0.1:6060/debug/pprof/mutex
go tool pprof http://127.0.0.1:6060/debug/pprof/block
```

分析时我会重点看这些函数：recorder 的 `finish`、`applyObservationLocked`、`snapshotLocked`、`percentile`，balancer 的 `Pick`、`Done`、`ObserveLatency`，routing 的 cost 计算。CPU 高不一定是坏事，要结合吞吐和 p99 看。如果 `percentile` 排序占比很高，那就说明该换 histogram。如果 mutex profile 显示 recorder 锁等待很高，就说明要分片。

### Q501【拓展】context cancellation 传播在复杂 RPC 链中有哪些坑？

第一个坑是只在入口用了 context，内部 goroutine 没有接。比如请求结束了，后台还在上报、重连、写日志，这就会泄漏。AegisMesh 的 reporter、resolver、policy watcher 都需要明确接收 context 或 Close 信号。

第二个坑是 retry 会改变时间语义。外层 context 是 overall timeout，每次 attempt 又可能有 per-try timeout。如果 per-try timeout 设置得太长，最后一次重试可能已经没有足够剩余时间；如果设置得太短，正常 p99 请求也会被提前杀掉。

第三个坑是 cancel 不等于服务端没有执行。客户端取消后，服务端可能已经完成了写库、扣库存、创建订单。AegisMesh 里 CreateOrder 这类非幂等方法要通过 policy 禁用重试，或者配 idempotency key。

第四个坑是 metadata 丢失。派生新 context 时如果没保留 gRPC metadata，trace_id、attempt、route revision 这些信息可能断掉。项目里的 retry interceptor 会给每次 attempt 填 metadata，这就是为了避免 trace 混乱。

第五个坑是在循环里 `defer cancel()`。这种写法会拖住 timer，应该每轮调用结束后立刻 cancel。

### Q502【拓展】如何避免 goroutine leak？常见检测方法是什么？

避免 goroutine leak 的原则很朴素：谁启动 goroutine，谁就要定义退出条件。

在 AegisMesh 里，resolver watch loop 要在 `Close()` 后退出；telemetry reporter 要在 context cancel 后退出并关闭 Controller 连接；policy watcher 要在 context cancel 后停止重连；server streaming 的 `WatchPolicy` 要监听 `stream.Context().Done()`。

写代码时我会检查几个点：

1. ticker 是否 `Stop()`，timer 是否释放。
2. 阻塞的 channel send/recv 是否能被 ctx 打断。
3. gRPC stream 断开后是否会退出循环，而不是空转。
4. 连接是否 Close。
5. 错误重试是否有 backoff，避免失败时疯狂创建 goroutine 或连接。

检测方法有几种。单测里可以记录 `runtime.NumGoroutine()`，执行 cancel 后等一小段时间，看数量是否回落。更专业一点可以用 `go.uber.org/goleak`。实验环境里可以抓 goroutine profile：

```bash
curl http://127.0.0.1:6060/debug/pprof/goroutine?debug=2
```

如果看到大量 resolver watch、policy watcher 或 gRPC stream goroutine 残留，就要顺着它们阻塞的位置查。

### Q503【拓展】Go module、依赖锁定和 reproducible build 如何管理？

Go 项目里最基本的是把 `go.mod` 和 `go.sum` 提交进仓库。AegisMesh 的 module 是 `github.com/aegismesh/aegismesh`，Go 工具链写在 `go.mod` 里，gRPC、protobuf、Prometheus、cilium/ebpf 这些依赖也都有明确修订。这样别人 clone 后用同一套 module graph 构建，不会随机器变化。

我会用这些做法保证构建可复现：

1. 每次改依赖后跑 `go mod tidy`，确保 `go.mod` 和 `go.sum` 没有脏依赖。
2. CI 里固定 Go 工具链，比如 1.23.x，而不是用系统默认 Go。
3. Dockerfile 里也固定基础镜像 tag。
4. protobuf 生成文件提交到仓库，避免每个人本地 protoc 插件不同导致生成结果漂移。
5. CI 跑 `go test ./...`、`go vet ./...`，必要时加 `go test -race ./pkg/... ./sdk/go/...`。
6. 对 release build 使用 `-trimpath`，减少本地路径进入产物。

如果公司内网构建，还要固定 `GOPROXY`、私有 module 的 `GONOSUMDB`，甚至可以做 vendor。对 AegisMesh 这种系统项目，依赖可复现很重要，因为 eBPF、gRPC、protobuf 任一依赖漂移，都可能让实验结果变得难解释。

### Q504【拓展】如果项目要支持插件化策略，Go interface 和 generics 如何取舍？

我会优先用 interface，而不是一上来用 generics。

策略插件的本质是运行时替换行为，比如选择不同的 score calculator、routing picker、retry decider、registry backend、telemetry sink。这些都适合定义接口：

```go
type ScoreCalculator interface {
    Compute(samples []EndpointStats) []EndpointScore
}
```

然后通过注册表或配置选择实现。这样业务逻辑只依赖接口，插件可以在编译期接进来，测试也容易替换 fake。

generics 更适合写类型安全的数据结构或通用工具，比如泛型 ring buffer、泛型 LRU、泛型 histogram bucket 封装。它不适合表达“运行时加载一个完全不同的治理策略”。因为 generics 的类型参数在编译期决定，不能替代动态多态。

Go 的 `plugin` 包也可以做动态加载，但它限制多：主要面向 Linux，Go 工具链和依赖锁定信息要严格匹配，部署和排错都麻烦。真正要做平台级策略扩展，我更倾向于三种方式：

1. 编译期插件：用 interface + blank import 注册，稳定简单。
2. 外部策略服务：SDK 把观测数据发出去，策略服务返回权重或状态。
3. WASM 插件：隔离性更好，但工程成本更高。

放到 AegisMesh 当前代码里，最自然的路线是先把 slow_score calculator、state machine config、routing cost function、retry policy 合并逻辑都抽成接口，再用 YAML policy 控制选择哪个实现。generics 可以辅助数据结构，但不是策略插件的主轴。
