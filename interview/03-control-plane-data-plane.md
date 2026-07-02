# 03. 控制面、数据面与架构边界

## 简单

### Q057【简单】AegisMesh 的控制面有哪些接口？

AegisMesh 的控制面主要有三组 gRPC 接口。

第一组是 `RegistryService`，包括 `RegisterInstance`、`Heartbeat`、`ListInstances`。它负责服务注册、TTL 心跳和服务发现。

第二组是 `TelemetryService`，包括 `ReportEndpointStats` 和 `ListEndpointHealth`。SDK 通过它上报 endpoint 级指标，Controller 通过它暴露当前健康状态。

第三组是 `PolicyService`，包括 `GetPolicy` 和 `WatchPolicy`。SDK 启动时先拉取策略快照，之后通过 watch 接收策略更新。

项目里还有 Prometheus `/metrics`，但它更偏观测接口，不是业务治理协议本身。

### Q058【简单】数据面为什么放在 SDK 里，而不是放在独立代理里？

这是一个有意取舍。这个项目的重点是验证 gRPC 客户端侧治理，所以我把数据面放在 Go SDK 里。SDK 能直接接触 gRPC 的 resolver、balancer、interceptor、deadline、status code 和 metadata，做方法级 retry、trace、负载均衡会很自然。

独立代理的好处是语言无关，业务进程不用引 SDK。但代理模式会引入部署复杂度，也更难拿到某些应用层语义，比如具体 gRPC method、幂等策略和业务 trace metadata。

所以 AegisMesh 当前选择 SDK mesh：路径短、实验可控、和 gRPC 扩展点贴得更近。代价是跨语言支持要分别实现 SDK。

### Q059【简单】RegistryService、TelemetryService、PolicyService 的职责怎么划分？

`RegistryService` 负责“有哪些实例可用”。服务实例启动后注册自己的 service、instance id、address 和 metadata，并定期心跳续租。SDK resolver 通过 `ListInstances` 拿到地址列表。

`TelemetryService` 负责“这些实例现在表现怎么样”。SDK 会把请求数、错误数、延迟、in-flight、网络信号等窗口数据上报给 Controller，Controller 再计算 slow_score 和 endpoint 状态。

`PolicyService` 负责“客户端应该按什么策略治理”。比如方法级 timeout、retry、retry budget、outlier threshold、PROBING 参数和 SLO 都属于策略层。

简单说，Registry 管实例，Telemetry 管观测，Policy 管规则。

### Q060【简单】Controller 是否参与每一次业务请求？为什么？

不参与。

一次业务 RPC 的实际路径是客户端 SDK 直接调用后端服务，Controller 不在请求转发链路上。Controller 只参与注册、服务发现、telemetry 上报和策略下发。

这样设计有两个原因。第一，Controller 延迟不会叠加到每个业务请求上。第二，Controller 短暂不可用时，已经解析出来的地址和已有 gRPC 连接还能继续使用，数据面不会立刻中断。

### Q061【简单】SDK 里哪些逻辑会影响请求路径？

会影响请求路径的逻辑主要在 resolver、balancer 和 interceptor。

resolver 决定候选 endpoint 列表，并把 `status`、`slow_score`、`instance_id` 放进 gRPC address attributes。`EJECTED` 和 `DEAD` 会被过滤，`HEALTHY`、`DEGRADED`、`PROBING` 可以进入候选列表。

balancer 使用 adaptive P2C 选择具体 SubConn。它会考虑 in-flight、EWMA 延迟、Controller 下发的状态、slow_score 和 probe ratio。

interceptor 会影响 timeout、retry、retry budget、circuit breaker、telemetry 和 trace metadata。也就是说，SDK 既决定“发给谁”，也决定“失败后是否重试、如何记录”。

### Q062【简单】为什么要把健康状态叠加到 service discovery 的实例列表里？

因为客户端选路时需要同时知道地址和健康信息。单独返回地址列表不够，SDK 还要知道这个 endpoint 是 `HEALTHY`、`DEGRADED`、`PROBING`，以及它的 `slow_score`。

AegisMesh 的做法是在 `ListInstances` 返回时叠加健康状态。Registry 仍然负责基础实例和租约，Controller 的 health manager 负责运行时状态。这样 SDK resolver 一次请求就能拿到完整的路由输入。

这个设计也让状态传播更直接：Controller 计算出 slow_score 后，下次 resolver 刷新就能把状态带到 balancer。

### Q063【简单】控制面宕机后，已经解析到的地址是否还能继续使用？

可以继续使用已有地址和已有连接。

AegisMesh 的 resolver 拉取失败时会向 gRPC 报告错误，但不会把当前地址列表清空。已有 SubConn 仍然可以被 balancer 使用。Policy watch 失败时，SDK 也会继续使用已有策略快照。

边界是冷启动。如果客户端刚启动，Controller 又不可用，那它可能拿不到初始地址和策略。生产系统通常会补本地缓存、DNS fallback 或 bootstrap 配置。

### Q064【简单】项目里的 in-memory registry 和 file-backed registry 分别适合什么场景？

in-memory registry 适合本地开发、单测和最小可运行 demo。它实现简单，重启后状态会丢。

file-backed registry 适合单机实验和项目展示。它把注册信息和租约过期时间写到本地文件，Controller 重启后可以恢复未过期的实例。

但 file-backed registry 不是高可用注册中心。它解决的是“单机 Controller 重启后不完全丢状态”，不是多 Controller 一致性。真正生产环境更适合 etcd、Consul 或 Kubernetes EndpointSlice 这类后端。

### Q065【简单】为什么 endpoint state 不直接存到注册中心的基础状态里？

因为两类状态的来源和生命周期不同。

注册中心的基础状态来自服务实例自身：注册、心跳、TTL 过期。它回答的是“这个实例还在不在”。

endpoint state 来自运行时 telemetry：延迟、错误率、slow_score、状态机窗口。它回答的是“这个实例现在是否适合承接流量”。

把两者分开后，Registry 可以保持简单稳定，health state 可以按窗口更新、衰减和恢复。`ListInstances` 再把两者叠加给 SDK。

### Q066【简单】控制面和数据面之间有哪些数据流？

主要有四条。

服务实例到 Controller：启动时注册，运行中定期心跳。

SDK resolver 到 Controller：周期性调用 `ListInstances`，拿服务实例和健康状态。

SDK telemetry reporter 到 Controller：按窗口上报 endpoint 级调用指标。

SDK policy manager 到 Controller：启动时 `GetPolicy`，运行中 `WatchPolicy` 接收策略修订更新。

业务 RPC 本身不经过 Controller，而是由 SDK 直接调用后端服务。

## 深度

### Q067【深度】如果控制面延迟很高，数据面的路由决策会产生什么滞后？

会出现状态滞后。慢故障已经发生，但 SDK 还在用旧的实例状态；或者 endpoint 已经恢复，SDK 仍然按旧状态降低它的权重。

这个延迟由几部分组成：SDK telemetry 上报间隔、Controller 计算状态的窗口、resolver 刷新间隔、Policy watch 或实例列表传播时间。AegisMesh 里 resolver 默认是周期刷新，telemetry 也是窗口上报，所以它不是毫秒级控制闭环。

缓解方式有两个。第一，SDK 本地 balancer 也维护 EWMA 和 in-flight，可以在 Controller 状态更新前先避开明显变慢的连接。第二，Controller 状态机用了连续窗口和恢复阈值，避免一次抖动就频繁切状态。

### Q068【深度】Controller 里的 health state 是全局权威状态，还是每个客户端的局部观测汇总？

更准确地说，它是 Controller 维护的聚合状态，不是绝对意义上的全局真相。

SDK 客户端把自己看到的 endpoint telemetry 上报给 Controller，Controller 按 service 和 instance 聚合这些样本，再计算 slow_score 和状态。它比单个客户端的本地 EWMA 更全局，但仍然来自客户端观测。

如果所有客户端都在同一个机房、访问路径差不多，这个聚合状态很有参考价值。如果客户端跨机房、跨可用区，或者调用方法差异很大，全局状态可能掩盖局部问题。那时就要引入 source、zone、method 维度。

### Q069【深度】多个 SDK 客户端同时上报 telemetry 时，Controller 如何处理观测偏差？

当前项目的处理方式是按 endpoint 汇总窗口样本，再用 slow_score 和状态机做判断。每条样本会带请求数、错误数、延迟和 endpoint 信息，Controller 以这些数据更新健康状态。

观测偏差仍然存在。比如一个客户端流量很小，它看到的 p99 很容易受偶发请求影响；另一个客户端在不同网络路径上，看到的延迟也可能不一样。

更稳的做法是按 request count 加权，设置最小样本量，并把 source、zone、method 作为可选维度。AegisMesh 当前已经能支撑基本的聚合实验，但生产化时我会把“全局 endpoint 分数”和“客户端本地分数”分开看。

### Q070【深度】如果不同客户端看到同一个 endpoint 的延迟差异很大，应该按客户端维度还是全局维度评分？

要看差异来自哪里。

如果延迟差异来自 endpoint 本身，比如某个实例 CPU 被打满，那么全局评分更合适，因为所有客户端都应该避开它。

如果差异来自网络路径，比如某个可用区到这个实例的链路变差，那按客户端或 zone 维度评分更合理。否则全局评分可能把一个只对部分客户端慢的实例摘掉，影响整体容量。

AegisMesh 当前采用混合思路：Controller 给出全局 health state 和 slow_score，SDK balancer 再叠加本地 EWMA 和 in-flight。这样全局状态负责识别明显坏点，本地状态负责处理客户端自己的观测差异。

### Q071【深度】service discovery 返回地址时过滤 EJECTED/DEAD，会不会让恢复探测失去流量来源？

如果一直过滤，会有这个问题。被摘除的 endpoint 没有任何流量，就无法证明自己已经恢复。

AegisMesh 的状态机把恢复拆成两步。`EJECTED` 期间不进入正常候选列表；到达 ejection duration 后，状态会转到 `PROBING`。resolver 会把 `PROBING` endpoint 放回地址列表，但 balancer 只给它很小比例的探测流量。

这样既能避免慢节点马上吃回正常流量，也能给它恢复验证的机会。这个设计比“直接从 EJECTED 回 HEALTHY”稳一些。

### Q072【深度】为什么 PROBING 状态需要特殊路由，而不能直接回到 HEALTHY？

因为恢复信号需要验证。一个 endpoint 在摘除一段时间后，看起来可能已经正常，但这不等于它能承受完整流量。

如果直接回到 `HEALTHY`，一旦它只是短暂恢复，流量会立刻打回去，p99 可能再次升高，状态也会反复震荡。

`PROBING` 的作用是小流量试探。AegisMesh 通过 probe ratio 限制它的请求占比，成功率和 slow_score 达标后再回到 `HEALTHY`；如果探测失败，就回到 `EJECTED`。

### Q073【深度】控制面状态和客户端本地 EWMA 状态冲突时，谁的权重更高？

我会让控制面状态先决定安全边界，本地 EWMA 再决定具体排序。

比如 Controller 已经把 endpoint 标为 `EJECTED`，resolver 就不应该把它作为正常候选。这个状态代表跨窗口、跨样本的判断，优先级更高。

在 `HEALTHY` 或 `DEGRADED` 的候选集合里，SDK 本地 EWMA 和 in-flight 更适合做细粒度选择。它反映的是这个客户端当前连接上的即时体验。

`PROBING` 比较特殊：它可以进入候选列表，但只能拿到受限探测流量，不能因为本地 EWMA 低就直接吃满流量。

### Q074【深度】如果注册实例的 address 发生漂移，telemetry 的 endpoint 到 instance 解析会出现什么问题？

最大问题是归因错误。

Telemetry 上报里有 endpoint address。如果容器重启后 IP 变了，或者 NAT、端口映射发生变化，Controller 用旧地址解析 instance 时可能找不到实例，也可能把样本归到错误实例。

AegisMesh 里做了地址到 instance 的映射，也有按唯一端口兜底的逻辑，适合 demo 和单机 Docker 场景。但生产环境不能只靠 address。更稳的方式是 SDK 在 telemetry 里带稳定的 instance id、注册 generation 或 lease revision，Controller 用这些字段做主键，address 只作为连接信息。

### Q075【深度】在高并发环境下，Controller 的 health map、registry map、policy map 如何避免锁竞争？

思路是减少共享写锁的范围。

Registry 可以按 service 分片，避免所有注册和查询都抢一把全局锁。health map 也可以按 service 或 endpoint 分片，telemetry 先进入批处理队列，再由 worker 合并窗口数据。

Policy 更适合做 copy-on-write。每次加载 YAML 后生成不可变 snapshot，用修订号和原子指针发布；读路径只读当前 snapshot，不在热路径上解析文件。

还有一个原则：不要在持锁期间做慢操作，比如文件 IO、Prometheus 导出、复杂排序和网络调用。锁只保护内存状态，计算尽量在锁外完成。

### Q076【深度】控制面如果做水平扩展，health state、policy snapshot 和 registry lease 怎么保持一致？

Registry lease 适合放到 etcd、Consul 或 Kubernetes API 这类带 watch 和 TTL 语义的后端。多个 Controller 都从同一个后端读写注册信息。

Policy snapshot 应该是带修订号的。写入时用 revision 或 CAS，SDK watch 时按 revision 判断是否更新。这样多个 Controller 下发的是同一个修订语义。

health state 麻烦一些，因为它是从 telemetry 算出来的动态状态。当前代码已经支持把非过期 endpoint health snapshot 写到 etcd，Controller 启动或 watch 到更新时恢复/合并状态，用于 failover 后保留近期 EJECTED/DEGRADED/PROBING 信号。但它仍是最终一致观测缓存；更强的 active-active 形态还需要按 service 或 endpoint 做一致性哈希/leader ownership，避免多个 Controller 同时推进同一个 endpoint 状态机。

我更倾向于“注册和策略强一致，health 最终一致”。健康分数本来就是近似信号，不值得为每个分数更新付出强一致成本。

## 拓展

### Q077【拓展】Kubernetes 中 EndpointSlice、Service、Pod readiness 与 AegisMesh Registry 的职责如何对应？

Kubernetes `Service` 对应 AegisMesh 的逻辑 service name。它提供稳定入口和服务身份。

`EndpointSlice` 对应可调用 endpoint 列表，里面有 Pod IP、端口和 readiness 信息。AegisMesh 的 Registry 也保存 service、instance id、address 和 metadata。

Pod readiness 更像基础可用性信号，说明这个 Pod 是否应该进入服务发现。AegisMesh 的 endpoint state 是更上层的运行时治理信号，来自真实 RPC telemetry。一个 Pod readiness 可能是 ready，但在 AegisMesh 里仍然被标为 `DEGRADED` 或 `EJECTED`。

如果把项目接到 Kubernetes，Registry 可以从 EndpointSlice watch 得到基础实例列表，再叠加 AegisMesh 自己的 slow_score 和状态机。

### Q078【拓展】Istio/Envoy 的 outlier detection 和 AegisMesh 的 slow_score 有什么相似点和差异？

相似点是目标一致：发现异常 endpoint，降低或移除它的流量，之后再做恢复探测。

差异在信号和落点。Envoy outlier detection 常见信号是连续 5xx、gateway failure、success rate、failure percentage 等，逻辑运行在代理里。AegisMesh 的 slow_score 更偏 fail-slow，融合了 EWMA、p95、相对异常、absolute SLO、错误率、in-flight 和网络信号，逻辑运行在 Controller 和 SDK 里。

Envoy/Istio 是成熟的 sidecar mesh 体系，功能和生态更完整。AegisMesh 是一个针对慢故障治理的 SDK-based 实验系统，优势是能贴近 gRPC 方法、retry policy 和客户端本地观测。

### Q079【拓展】如果使用 etcd/Consul 作为后端 registry，lease 和 watch 机制应如何设计？

可以把每个实例写成一个带 TTL 的 key，比如 `/aegis/registry/{service}/{instance}`，value 里放 address、metadata、revision 和注册时间。

实例启动时创建 lease，心跳时续租。实例异常退出后 lease 过期，key 自动消失。Controller 或 SDK 可以 watch service 前缀，拿到新增、更新和删除事件。

health state 和 policy 不建议混在同一个 key 里。当前 health snapshot 放在独立 `/aegismesh/health/v1/services/{service}/instances/{instance}` 前缀下，带 `UpdatedAt` 和 max-age 过滤；policy 放在 `/aegismesh/policy/v1/services/{service}/snapshot`，使用 revision 做修订控制。

这样注册、健康和策略各自有清晰的生命周期。

### Q080【拓展】控制面配置强一致和最终一致分别适合哪些治理策略？

强一致适合影响安全边界和配置语义的内容，比如租户权限、认证策略、全局禁用开关、策略修订发布、服务注册租约。

最终一致适合观测驱动的动态数据，比如 slow_score、endpoint 权重、EWMA、Prometheus 指标和局部路由偏好。这些数据本来就来自采样，短时间不一致可以接受。

故障摘除介于两者之间。它不一定需要强一致，但需要修订、滞回和最小窗口，避免不同 Controller 或不同 SDK 在同一时间做出完全相反的路由决策。

### Q081【拓展】如何设计多集群、多地域的 RPC 治理控制面？

我会分两层做。

每个集群或地域内部有本地 Controller，负责本地注册、telemetry 聚合、slow_score 和路由状态。业务流量只依赖本地控制面，避免跨地域控制面延迟影响请求。

上层有全局控制面，负责策略分发、服务目录、跨地域 failover 规则和汇总报表。全局策略下发到本地 Controller，本地 Controller 再结合本地 telemetry 做最终路由判断。

跨地域路由要带 locality 信息，比如 region、zone、cluster、network cost。默认走本地，只有本地容量不足或故障时才按策略切到远端。

### Q082【拓展】如果不同服务有不同 SLO，控制面如何管理策略隔离？

策略要按 service、namespace、tenant 和 method 分层。

比如 `user-service/GetUser` 的 p95 SLO 可以是 100ms，`order-service/CreateOrder` 的 SLO 可以是 300ms，而且后者可能禁止自动 retry。Controller 的 PolicyService 应该返回服务级 snapshot，里面包含方法级 timeout、retry、outlier threshold 和 latency SLO。

计算 slow_score 时也要使用对应服务的 SLO。否则一个本来就重的接口会被误判为慢故障，或者一个低延迟接口已经退化却没有触发状态变化。

策略隔离还需要 RBAC 和修订审计。不同团队只能改自己服务的策略，发布记录要能回滚。

### Q083【拓展】Sidecar mesh、SDK mesh、ambient mesh 三种形态的优缺点是什么？

Sidecar mesh 的代表是每个 Pod 旁边放一个代理。优点是语言无关，治理能力集中在代理里；缺点是资源开销和运维复杂度高，请求路径也多一跳。

SDK mesh 把治理逻辑放进客户端库。优点是能拿到应用层上下文，和 gRPC method、deadline、metadata 结合得很好，路径也短；缺点是每种语言都要维护 SDK，升级需要业务应用配合。

Ambient mesh 把部分治理能力下沉到节点或共享代理，减少 sidecar 数量。它能降低部署成本，但获取方法级语义和做细粒度客户端策略会更难。

AegisMesh 当前是 SDK mesh。这个选择适合项目目标，但不代表它是所有场景的最优解。

### Q084【拓展】如果要支持灰度发布和故障治理同时生效，策略合并顺序如何定义？

我会把顺序定义清楚，避免两个策略互相打架。

第一步是服务发现，拿到候选实例。第二步是基础健康过滤，`DEAD` 和 `EJECTED` 不进入正常流量，`PROBING` 只保留探测资格。第三步应用灰度规则，比如按发布变体、用户标签或百分比得到目标权重。第四步把 slow_score、endpoint state 和网络分数叠加到权重上。最后由 SDK 的 adaptive P2C 根据本地 EWMA 和 in-flight 选具体 endpoint。

安全策略应该能压过灰度策略。比如灰度规则允许 10% 流量进 secondary，但 secondary 的某个 endpoint 被 `EJECTED`，那这个 endpoint 的正常流量应该是 0，只允许后续 PROBING。否则灰度发布会把故障治理抵消掉。

所有合并结果都应该带 policy revision，trace 里也要记录 route revision，方便 verifier 复盘实际流量是否符合预期。
