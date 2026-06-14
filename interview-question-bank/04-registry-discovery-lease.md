# 04. Registry、服务发现与租约

## 简单

### Q085【简单】Registry 中 Instance 包含哪些字段？

当前代码里的 `registry.Instance` 有这些字段：`ID`、`Service`、`Address`、`Status`、`Labels`、`LastSeen`。

`ID` 是实例 ID，比如 `user-a`。`Service` 是逻辑服务名，比如 `user-service`。`Address` 是真实访问地址，比如 `127.0.0.1:7001`。`Status` 表示实例状态，默认是 `HEALTHY`。`Labels` 放版本、zone 这类扩展信息。`LastSeen` 是最近一次注册或心跳时间。

还有一个重要字段不在 `Instance` 里，而是在 registry 内部 record 里：`expiresAt`。它表示租约过期时间，用来判断实例是否还活着。file-backed registry 持久化时也会把它保存成 `expires_at`。

### Q086【简单】Register 和 Heartbeat 的区别是什么？

`Register` 是实例首次加入注册中心，或者重新声明自己的完整信息。它需要带 `ID`、`Service`、`Address`、`Status`、`Labels` 和 TTL。

`Heartbeat` 是续租。它只需要 service、instance id 和 TTL，表示“我这个实例还活着”。当前实现里，Heartbeat 会更新 `LastSeen` 和 `expiresAt`，但不会更新 address 或 labels。

所以如果实例地址、版本、labels 变了，应该重新 Register；如果只是保持租约，就 Heartbeat。

### Q087【简单】TTL lease 解决什么问题？

TTL lease 解决的是实例异常退出后的清理问题。

如果服务进程正常关闭，它可以主动注销。但真实系统里更常见的是进程崩溃、机器断电、网络断开，实例根本来不及通知注册中心。TTL lease 的思路是：实例必须定期 Heartbeat 续租；超过过期时间还没续上，就认为它不再可用。

这样 Registry 不需要完全相信实例主动下线，过期实例会自动从服务发现结果里消失。

### Q088【简单】SweepExpired 的作用是什么？

`SweepExpired` 会扫描 registry 里的记录，把 `expiresAt` 已经过期的实例删掉，并返回删除数量。

它的作用是清理内存状态，避免过期实例一直占着 map。file-backed registry 里，如果清掉了过期实例，还会重新 persist 一次，把快照文件也更新掉。

不过即使没有立刻 Sweep，`List` 也会过滤过期实例。也就是说，Sweep 是清理存储，List 是保护服务发现结果。

### Q089【简单】为什么 List 需要过滤过期实例？

因为不能假设后台清理一定准时执行。

如果只靠 `SweepExpired`，那么清理任务延迟、Controller 短暂卡顿、测试里没有启动 sweep loop，都可能让过期实例继续出现在服务发现里。`List` 每次返回前再按当前时间过滤一遍，可以保证 SDK 不会拿到已经过期的实例。

这个设计很实用：清理可以晚一点，但服务发现结果不能把明显过期的地址返回给客户端。

### Q090【简单】实例状态 HEALTHY、DEGRADED、EJECTED、PROBING、DEAD 分别表示什么？

`HEALTHY` 表示实例正常，可以承接普通流量。

`DEGRADED` 表示实例还没彻底摘除，但已经变慢或出现异常。SDK 可以继续使用它，不过 adaptive P2C 会提高它的成本，减少流量。

`EJECTED` 表示实例被状态机摘除，不进入正常候选列表。

`PROBING` 表示实例从摘除状态进入恢复探测。resolver 会把它放回地址列表，但 balancer 只给少量探测流量。

`DEAD` 表示实例不可用或不该再被路由。当前 resolver 会过滤 `EJECTED` 和 `DEAD`，保留 `HEALTHY`、`DEGRADED`、`PROBING`。

### Q091【简单】file-backed registry 比 in-memory registry 多解决了什么问题？

in-memory registry 的问题是 Controller 一重启，注册信息就没了。

file-backed registry 会把实例快照写到本地 JSON 文件。Controller 重启后，它会加载快照，把未过期的实例恢复到内存里。这样单机实验和项目演示时，不会因为 Controller 重启就丢掉所有注册信息。

但它不是高可用注册中心。它只能解决本地持久化，不解决多 Controller 一致性，也不等价于 etcd 或 Consul。

### Q092【简单】为什么持久化快照要保存 expires_at？

因为重启恢复时要知道这个实例的租约是否还有效。

如果只保存 instance 信息，不保存 `expires_at`，Controller 重启后就不知道这个实例是不是早就过期了。最坏情况是把已经死掉的实例重新放进服务发现结果。

当前 file registry 加载快照时会检查 `expires_at`。只有过期时间晚于当前时间的记录才会恢复，过期记录会被跳过。

### Q093【简单】Registry 的 List 为什么按 instance ID 排序？

主要是为了结果稳定。

Go map 的遍历顺序是不固定的。如果 `List` 直接返回 map 遍历结果，同样一批实例每次顺序都可能不同，单测、日志和 resolver 更新都会更难判断。

按 instance ID 排序后，同样的输入会得到同样的输出。这不代表负载均衡按 ID 顺序选实例，只是让服务发现结果更可预期。

### Q094【简单】服务发现失败时 SDK 应该怎样表现？

SDK 不应该因为一次服务发现失败就清空已有地址。

AegisMesh 当前 resolver 调 `ListInstances` 失败时会 `ReportError`，但不会把地址列表更新成空。已有 SubConn 和最后一次解析到的地址还能继续使用。这样 Controller 短暂不可用时，业务请求不一定立刻中断。

边界是冷启动。如果 SDK 第一次启动时就连不上 Controller，它拿不到初始地址，就没有可用后端。生产系统通常会加本地缓存、DNS fallback 或 bootstrap 地址。

## 深度

### Q095【深度】TTL 过短或过长会分别带来什么问题？

TTL 过短会让系统太敏感。实例只是短暂 GC、网络抖动、CPU 忙，Heartbeat 晚了一点，就可能被误删。客户端看到地址消失，又要重建连接，服务发现会抖。

TTL 过长会让失效实例残留太久。实例已经崩了，但 registry 还觉得它活着，SDK 继续拿到这个地址，业务请求会遇到连接失败或超时。

所以 TTL 要和 Heartbeat 周期配合。常见做法是 TTL 至少是 Heartbeat 间隔的几倍，再加一点 jitter 和容忍窗口。AegisMesh 默认 lease 是 30 秒，本地实验可以调短，但生产环境不能太激进。

### Q096【深度】Heartbeat 成功但业务 RPC 全部慢，Registry 能否发现？为什么需要 telemetry？

Registry 发现不了。

Heartbeat 只能说明实例进程还在、能和 Controller 通信、租约还能续上。它看不到真实业务 RPC 的延迟、错误率、in-flight 和网络重传。

慢故障恰恰经常是这种情况：Heartbeat 很快，业务方法很慢。比如实例 CPU 被打满、下游数据库慢、锁竞争严重，健康接口和心跳都可能正常。

所以 AegisMesh 把 Registry 和 Telemetry 分开。Registry 管“实例是否还在”，Telemetry 管“业务调用表现怎么样”。slow_score 和 endpoint 状态要靠 telemetry 计算，不能只靠心跳。

### Q097【深度】file registry 每次 Register/Heartbeat 都 persist，有什么性能和一致性取舍？

好处是简单，而且重启恢复更可靠。每次注册或心跳后都写快照，Controller 崩溃时丢的数据比较少。

代价是写放大。Heartbeat 如果很频繁、实例很多，每次都 marshal 整个快照并写文件，IO 和 CPU 开销会变高。当前实现适合单机实验和小规模演示，不适合几万实例高频心跳。

改进方向很明确：批量 flush、定时刷盘、只写变更日志、或者直接换 etcd/Consul 这类带 lease 的后端。面试里我会说明，这个 file registry 是为了补 Controller 重启恢复，不是最终生产存储。

### Q098【深度】os.Rename 原子替换快照能避免哪些故障场景？

它主要避免“写到一半把正式快照写坏”。

当前实现先写 `registry.json.tmp`，写完后再 `os.Rename(tmp, path)` 替换正式文件。这样如果进程在写 tmp 文件时崩溃，旧的正式快照还在；不会留下一个写了一半的 `registry.json` 给下次启动解析。

不过这不是完整的崩溃一致性方案。严格来说，还要考虑 `fsync` 文件和目录，否则机器掉电时仍可能有文件系统层面的风险。项目当前的做法对本地实验足够，但生产级持久化会更谨慎。

### Q099【深度】如果 Controller 在写 tmp 文件后崩溃，恢复逻辑应该怎样处理？

当前恢复逻辑只读取正式路径，比如 `registry.json`，不会读取 `registry.json.tmp`。所以如果 Controller 在写 tmp 文件后、rename 前崩溃，下次启动会使用旧快照，tmp 文件会被忽略。

这个行为是合理的。tmp 文件代表“未提交”的写入，不能拿它当权威状态。

更完整的实现可以在启动时清理残留 tmp 文件，或者检查 tmp 的 mtime 后直接删除。不要尝试合并 tmp 和正式快照，除非有明确的 WAL 格式和提交标记。

### Q100【深度】如果两个实例使用相同 instance ID 注册，会发生什么？如何改进？

当前实现里，同一个 service 下的实例存在 map 里，key 是 instance ID。如果两个实例用同一个 ID 注册，后注册的记录会覆盖前一个记录。

这在 demo 里简单，但生产里有风险。两个真实实例如果误用了同一个 ID，registry 会把它们当成同一个实例，telemetry、health state、resolver 地址都可能混在一起。

改进方式是让 instance ID 真正唯一，比如用 workload identity、Pod UID、启动生成的 UUID，或者 service + address + generation 组合。注册时也可以拒绝“同 ID 但身份不同”的覆盖，要求先注销或带上 lease revision 做 CAS。

### Q101【深度】如果同一个实例 IP 变了但 ID 不变，注册中心和 resolver 会如何收敛？

如果实例重新调用 `Register`，当前 registry 会用同一个 ID 覆盖旧记录，新的 address 会进入内存状态。resolver 下一次 `ListInstances` 刷新时会拿到新地址，然后更新 gRPC 地址列表。

旧地址会从 resolver state 里消失。gRPC balancer 后续会基于新地址建连接，旧 SubConn 会逐步不再被使用。

有一个细节：Heartbeat 不会更新 address。如果只是 Heartbeat，注册中心仍然保留旧地址。所以 IP 变了以后必须重新 Register，不能只续租。

生产里还会加 generation 或 lease revision，避免旧实例迟到的 Heartbeat 把新实例状态续上。

### Q102【深度】Registry 与 health manager 分离会带来哪些好处和复杂性？

好处是职责清楚。Registry 只处理注册、租约和地址；health manager 处理 telemetry、slow_score 和状态机。这样心跳存活和业务慢故障不会混在一起。

它也让状态更新更灵活。Registry 的租约可能几十秒更新一次，health state 可以按 telemetry 窗口更快变化。`ListInstances` 时再把 health state 叠加到实例列表上，SDK 一次拿到地址和健康状态。

复杂性在于一致性。Registry 里有实例，health manager 里可能还没有对应状态；实例过期后，health map 里的旧状态也要清理或忽略；address 漂移时，telemetry 到 instance 的映射也要小心。项目里通过 service + instance ID 关联两边状态，但生产环境还需要 generation 和过期清理。

### Q103【深度】如果需要支持 instance 权重、zone、version、metadata，Registry schema 应如何扩展？

当前 `Labels map[string]string` 已经可以放 `zone`、`version` 这类信息，适合快速扩展。

如果这些字段会进入核心路由逻辑，我会把它们提升成一等字段，比如 `Weight`、`Zone`、`Version`、`Metadata`、`RegisteredAt`、`Revision`。`Weight` 用于基础流量权重，`Zone` 用于 locality-aware routing，`Version` 用于 canary，`Revision` 用于防止旧心跳覆盖新注册。

schema 设计上要注意兼容性。proto 可以新增字段，但不要复用旧字段号；registry 内部可以继续保留 labels，作为不影响核心路由的扩展字段。

### Q104【深度】如果服务规模从几十个实例扩展到几万个实例，List 全量返回是否还能接受？

不能一直靠全量 List。

几十个实例时，全量返回简单可靠。到几万个实例后，每个 SDK 每几秒拉一次完整列表，会带来明显的网络、序列化和 Controller CPU 开销。客户端也会频繁处理大地址列表。

改法通常是增量化。可以加 `WatchInstances`，用 version/revision 推送变更；也可以按 service 分片、分页、long-poll，或者像 Kubernetes EndpointSlice 一样把大列表拆成多个 slice。客户端侧要保留缓存，只处理 delta，而不是每次重建完整连接池。

## 拓展

### Q105【拓展】服务发现的 pull、push、long-poll、watch 四种模式怎么比较？

pull 最简单。客户端定时拉取实例列表，Controller 压力可控，但状态传播有刷新间隔。AegisMesh 当前 resolver 就是这种模式。

push 是服务端主动推更新，延迟低，但 Controller 要维护客户端连接和订阅关系。

long-poll 介于两者之间。客户端发请求，如果没有变化，服务端先挂住一段时间；有变化就返回。它比普通轮询省一些空请求，也比 streaming 简单。

watch 通常是长连接流式订阅，配合 revision/version 使用。它适合实例变化频繁、规模较大的系统，但要处理断线重连、补漏、backpressure 和版本一致性。

### Q106【拓展】Consul 的 TTL check、Eureka 的 lease、Kubernetes readiness probe 有什么异同？

它们都在解决“服务发现里哪些实例应该出现”的问题，但信号来源不同。

Consul TTL check 要求服务或 agent 定期上报健康状态，超过 TTL 就认为 check 不健康。Eureka lease 也是客户端定期续约，服务端根据租约判断实例是否还活着。Kubernetes readiness probe 是 kubelet 对 Pod 做探测，readiness 失败后这个 Pod 会从 Service endpoints 里移除。

AegisMesh 的 TTL lease 更像 Eureka lease 或 Consul TTL check。它判断实例是否还在。readiness 更偏 Kubernetes 原生生命周期。AegisMesh 的 slow_score 则是另一层，它看真实 RPC 表现，不等同于 readiness 或 lease。

### Q107【拓展】CAP 视角下服务注册发现系统应该优先 CP 还是 AP？

服务发现通常更偏 AP，至少数据面要偏可用。

原因很现实：注册中心短暂不可达时，客户端最好还能用最后一次地址继续发请求。如果为了强一致，在分区时直接拒绝所有服务发现，业务面会更容易被控制面拖垮。

但不是所有东西都该 AP。服务身份、权限、策略发布、全局禁用开关更适合 CP 或至少强版本控制。我的设计倾向是：registry 查询和健康权重最终一致，安全和策略写入更重视一致性。

### Q108【拓展】DNS-based service discovery 和 registry RPC discovery 的优缺点是什么？

DNS 的优点是通用、简单、语言无关，几乎所有客户端都支持。缺点是表达能力弱，主要返回地址，不适合带 instance id、slow_score、状态、zone、版本和方法级策略。DNS 缓存和 TTL 也会让变化传播不够精细。

registry RPC discovery 的优点是信息丰富。AegisMesh 的 `ListInstances` 可以返回 address、status、slow_score、labels，SDK 可以直接用于 adaptive routing。缺点是客户端要接入 SDK 或协议，Controller 也变成一个需要维护的控制面组件。

所以 DNS 适合基础寻址，registry RPC 更适合细粒度 RPC 治理。

### Q109【拓展】如果服务发现结果发生抖动，客户端负载均衡如何避免频繁重建连接？

第一，不要因为一次拉取失败就清空地址。AegisMesh 现在就是保留旧地址，只上报 resolver error。

第二，对地址变化做去抖。比如连续几次都看不到某个实例，才真正移除；新实例先预热，再进入完整流量。

第三，保持地址和 instance ID 稳定。只要 address 没变，gRPC 的 SubConn 可以复用，不需要重建连接。状态变化可以放在 attributes 里更新，让 balancer 调整权重，而不是每次都换地址。

还可以加 connection draining、最小保留时间、resolver update debounce 和 jitter，避免大量客户端同一时间重连。

### Q110【拓展】如何设计 registry 的鉴权，防止恶意实例注册到关键服务？

注册请求必须有身份。常见做法是 mTLS，加上 SPIFFE ID、Kubernetes ServiceAccount 或内部证书身份。Controller 根据身份判断它能不能注册到某个 service。

权限要按 service 或 namespace 做。比如只有 `user-service` 的 workload 身份才能注册 `user-service` 实例，不能随便注册到 `payment-service`。还要校验 address 是否属于调用方所在的 Pod、节点或授权网段，避免冒充别人的地址。

审计也要有。Register、Heartbeat 失败、重复 ID、跨服务注册、策略变更都要记录。真正生产系统还会给注册接口限流，防止恶意实例刷爆 registry。

### Q111【拓展】如果多个版本实例同时存在，registry 和 routing policy 如何配合支持 canary？

Registry 负责描述实例事实，比如每个实例的 `version=v1/v2`、zone、address 和 instance ID。它不应该自己决定“v2 走多少流量”。

routing policy 负责流量规则，比如 v2 只接 5% 请求，或者只让某些用户、header、tenant 进入 v2。SDK 或 sidecar 在拿到实例列表后，先按 policy 过滤和分组，再做负载均衡。

故障治理要能压过 canary。比如 v2 的某个 endpoint 被 `EJECTED`，即使 canary 规则允许 v2 接 5% 流量，这个坏 endpoint 也不应该接正常流量。最多进入 PROBING。

### Q112【拓展】如何在 service discovery 中表达 locality-aware routing？

Registry 需要带 locality 信息，比如 `region`、`zone`、`cluster`、`node`，可以先放在 labels 里，规模大了再提升成一等字段。

Policy 决定如何使用这些信息。常见规则是优先同 zone，其次同 region，最后才跨 region。也可以给不同 locality 配权重，比如本 zone 80%，同 region 20%，远端只做故障切换。

SDK 侧需要知道自己的 locality。resolver 返回 endpoint locality，balancer 用本地 locality 计算成本。这样服务发现只提供事实，路由策略决定偏好，负载均衡执行最终选择。
