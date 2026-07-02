# 20. 安全、可靠性与生产化

## 简单

### Q533【简单】项目当前有哪些明显的生产化缺口？

当前 AegisMesh 已经补上了控制面安全和 registry HA 的基础工程形态，但仍不能直接宣称是完整生产级 service mesh。

第一，安全面已经从“完全 plaintext/insecure”推进到可配置的 Controller TLS/mTLS、service-scoped bearer token RBAC、静态 mTLS 证书主体 RBAC 映射、默认拒绝无 TLS/auth 启动，以及 SDK/agent/recorder/demo registration 的控制面安全配置。生产部署应使用 TLS/mTLS、`--auth-tokens-file`/`--auth-tokens-env` 或 `--auth-cert-principals-file`，本地 demo 才显式使用 `--insecure-dev`。

第二，HA 面已经有 etcd registry backend、多 controller 共享 service lease、CAS heartbeat、watch 更新、SDK 控制面多地址 ordered failover。file/file-v2 registry 仍然只是单 controller 本地重启恢复，不是 HA 后端。

第三，仍未完全生产化的是强一致 health ownership 和策略治理面。当前 runbook 已支持通过 etcd 共享 registry、policy snapshot 和非过期 health snapshot，避免多个 controller 读取不同本地 policy file，也能在 failover 后恢复近期的 EJECTED/DEGRADED/PROBING 状态。但 health 仍是 telemetry 驱动的最终一致观测缓存，不是 leader 协调的强一致状态机。策略写入、审批、回滚和审计也还没有做成完整管理平面。

第四，RBAC 已经从纯方法级角色授权推进到 token 和静态 mTLS 证书主体都可绑定 service scope：`sdk:user-service=...` 这类 principal 只能访问对应 service 的 registry、policy 和 telemetry 请求。但它还不是完整 IAM：instance/source 级归属、动态租户策略、审计流、自动轮换和动态 service owner 映射仍未完成。

所以面试里更准确的说法是：项目已经具备生产化控制面的基础安全、service 级授权、etcd-backed registry/policy/health snapshot 共享形态，但完整生产级还需要强一致 health ownership、instance/source 级授权、审计和发布治理。
### Q534【简单】Controller 和 SDK 之间是否启用了 TLS？

现在代码已经支持。Controller 通过 `--tls-cert-file`、`--tls-key-file`、`--tls-ca-file` 和 `--tls-require-client-cert` 启用 TLS/mTLS；SDK 通过 `DialOptions.TransportCredentials` 连接业务 upstream，通过 `DialOptions.ControllerSecurity` 或 `AEGIS_CONTROLLER_*` 环境变量连接控制面。agent、experiment-recorder 和 demo registration 也复用同一套 `security.ClientConfigFromEnv("AEGIS_CONTROLLER")`。

生产模式下 Controller 默认要求 TLS+auth；`--insecure-dev` 会绕过启动时的 TLS/auth 要求，只应作为本地 demo 或测试模式使用。带 bearer token 的 plaintext 连接还需要额外的 `--auth-allow-insecure`，避免误把 token 发到明文链路。

mTLS 不只用于传输加密和客户端证书校验，现在也可以通过 `--auth-cert-principals-file` 把 URI SAN、DNS SAN 或无 SAN 证书的 CN 静态映射成 RBAC principal。token 仍然是权威来源：请求里有 token 时按 token 授权，无效 token 不会回退到证书。下一步生产化应接动态 SPIFFE/SPIRE 或 IAM，把证书主体和 service/namespace owner 自动关联起来。
### Q535【简单】服务注册是否有认证？

现在 RegistryService 可以通过 Controller bearer token RBAC 保护。`registry` role 可以 `RegisterInstance`、`Heartbeat`、`ListInstances` 和 `WatchInstances`；`reader` 只能读；`sdk` 只能读 registry、读 policy 并上报 telemetry，不能注册实例。token 还可以写成 `role:service=token` 或 `role:service-a+service-b=token`，把权限限制在具体 service 上。Controller 启用 token 后，所有 gRPC unary/stream 方法都会经过同一套拦截器。

这个能力解决了“任何人能连上 Controller 就能注册实例”的最低安全边界，并且 scoped token 或证书 principal 已经能按 `ServiceInstance.service`、policy service、telemetry sample service 做精确 service 名称校验。它还不是完整资源级授权：`instance_id`、telemetry `source`、动态 service owner、token/证书轮换和审计仍需要补齐；生产级下一步应把 principal 映射到真实 service owner，并扩展到实例和来源维度。
### Q536【简单】Policy YAML 是否有权限控制和审计？

当前没有完整策略写入权限控制和审计。PolicyService 可以从本地 YAML 文件加载，也可以从 etcd 读取共享 protobuf `PolicySnapshot`，并通过 `GetPolicy` 和 `WatchPolicy` 提供给 SDK。谁能写入文件或 etcd key，主要仍取决于外部部署流程和 etcd 权限配置。

这对实验够用，但生产里策略属于高风险配置。比如把 `CreateOrder` 错误标成可重试，可能造成重复订单；把 outlier 阈值设得太低，可能让大量 endpoint 被摘除；把 retry budget 放太宽，故障时会放大下游压力。

生产化需要策略平台：谁改的、改了什么、为什么改、影响哪些服务、是否经过审批、什么时候生效、如何回滚，都要有记录。Policy YAML 可以继续作为底层格式，但不应该靠手工改文件直接上线。

### Q537【简单】Prometheus metrics endpoint 是否可能泄露内部信息？

可能。Controller 和 frontend 都暴露了 `/metrics`。指标里可能包含 service 名、method 名、upstream address、endpoint state、slow_score、错误率、inflight、retry 相关信息。这些信息对排障很有用，但也能暴露系统拓扑和故障状态。

本地默认绑定 `127.0.0.1` 风险较小；Docker 或生产环境如果把 metrics 端口暴露到不可信网络，就有问题。

生产里通常会把 metrics endpoint 放在内网，配合 NetworkPolicy、安全组或 sidecar auth proxy 限制访问。对外只暴露 Grafana 或经过权限控制的观测平台。高敏 label 也要谨慎，比如不要把用户 ID、租户 ID 或完整内部地址随便打进 metrics。

### Q538【简单】file-backed registry 在多副本 Controller 下是否安全？

不安全。file-backed registry 是单机重启恢复方案，不是多副本一致性方案。

它把内存里的注册记录写到一个 JSON 快照文件，写入时用临时文件加 `os.Rename` 做原子替换。单个 Controller 进程里，这能避免半截文件。多个 Controller 同时写同一个文件时就不行了：没有分布式锁，没有修订冲突检测，也没有 watch 机制。最后谁覆盖谁，取决于写入时序。

多副本 Controller 应该用 etcd、Consul、数据库或其他共享存储。服务注册信息、lease、policy revision、health state 至少要有明确的一致性模型。file-backed registry 只能说“重启后不至于立刻丢光实例”，不能说“支持 HA 控制面”。

### Q539【简单】eBPF agent 需要高权限，这带来什么安全风险？

eBPF agent 要加载内核 BPF 程序，通常需要 root 或 `CAP_BPF`、`CAP_PERFMON`、`CAP_NET_ADMIN` 这类 capability。权限一高，风险就不只是 AegisMesh 自己的进程了，而是会接触到宿主机内核和网络观测面。

风险包括：错误 BPF 程序影响系统性能；agent 被攻破后读取敏感网络元数据；过宽的 capability 被用于修改网络配置；ringbuf 事件里带出的地址和错误信息泄露内部拓扑。

所以生产部署要最小权限。能不用 root 就不用 root，容器要限制 capability，文件系统尽量只读，BPF object 固定来源，镜像签名，运行节点做 allowlist。eBPF 是很有价值的增强信号，但它的部署权限不能随便放开。

### Q540【简单】故障注入工具为什么不能随便在生产执行？

故障注入工具本质上是在主动破坏系统。项目里的 fault injector 可以模拟 delay、jitter、packet loss、CPU throttle、应用层慢调用。这些对实验很有用，但生产里如果没有边界，很容易把真实用户请求打坏。

比如对错误容器注入 800ms delay，可能让整个调用链 p99 飙升；packet loss 会触发重试，进一步放大下游压力；CPU throttle 可能让服务健康检查也开始失败。

生产混沌实验需要安全闸门：目标 allowlist、影响范围限制、审批、dry-run、自动回滚、abort condition、实验窗口、监控联动。项目里的 fault injector 默认偏实验用途，不能直接当生产混沌平台用。

### Q541【简单】如果 Controller 重启，哪些状态会恢复，哪些状态会丢失？

要看 registry backend。

如果用 memory registry，Controller 重启后服务注册信息会丢失。实例要重新 Register 或 Heartbeat，resolver 才能重新拿到地址。

如果用 file-backed registry，Controller 会从 JSON 快照恢复还没过期的实例记录，包括 instance 基本信息和 `expires_at`。这能让 Controller 重启后保留一部分服务发现数据。

如果启用了 `--health-state-backend etcd`，`HealthManager` 里的 slow_score、consecutive windows、EJECTED/PROBING 转换时间这些会以带 `UpdatedAt` 的 endpoint snapshot 写入 etcd。Controller 重启或 failover 后会加载非过期 snapshot，并继续 EJECTED/PROBING 的时间语义。没有启用该 backend 时，这些状态仍是内存态，会从新 telemetry 重新积累。Prometheus 指标本地内存也会重置，不过外部 Prometheus 已经抓走的历史还在 Prometheus 里。

Policy YAML 会重新从文件加载，所以策略可以恢复。SDK 侧的本地 EWMA、retry budget、trace writer 状态属于业务进程，不跟 Controller 重启直接绑定。

### Q542【简单】项目如何处理 Controller 单点故障？

当前项目没有真正的 HA Controller。Controller 不在每次业务 RPC 的请求路径上，所以它短时间不可用时，已经建立的业务 gRPC 连接还可以继续跑；SDK balancer 也还能用本地 SubConn 和本地 EWMA 做选择。

但控制面能力会受影响。resolver 拉不到新实例，telemetry 上报失败，Policy Watch 断开，health state 不再更新，新服务注册也无法完成。也就是说，业务流量不一定立刻中断，但服务发现、策略更新和慢故障治理会逐渐变钝。

当前降级方式主要靠 SDK 使用已有地址、本地状态和默认策略。生产化要做多副本 Controller、共享 registry/policy 存储、客户端多 Controller endpoint、失败重试和本地缓存过期策略。

## 深度

### Q543【深度】如果要把 Controller 做成 HA，需要一致性存储还是 leader election？

两者通常都需要，但负责的问题不同。

一致性存储解决状态共享。Registry lease、policy snapshot、health state、修订号、审计记录，都不能只放在某个 Controller 进程内。多副本 Controller 要读写同一份可信状态，比较自然的后端是 etcd、Consul 或数据库。etcd 还能提供 lease、watch、revision，这和服务发现、Policy Watch 很契合。

leader election 解决“谁来做主动动作”。比如定期 sweep expired instances、把 EJECTED 推到 PROBING、处理某些全局状态迁移。如果每个 Controller 副本都各自 tick，同一个 endpoint 可能被重复推进状态，或者指标重复写入。可以让一个 leader 负责这些主动任务，其他副本只处理读请求和转发写请求。

有些设计也可以不用单 leader，而是把状态迁移做成 CAS 写入：每个副本都可以尝试更新，但必须基于修订号比较，写失败就重试或放弃。这会更复杂。

当前代码已经先落了 etcd-backed registry、policy 和 health snapshot 共享；health snapshot 用 UpdatedAt 做较新覆盖，并用 max-age 控制 staleness。下一步如果要继续生产化，我会再加 leader election 或 endpoint ownership，让同一个 endpoint 的 health 状态机只由一个 owner 推进。这样工程边界清楚，面试里也好解释。

### Q544【深度】Health state 是否应该持久化？持久化后会不会恢复旧故障状态造成误判？

Health state 可以持久化，但不能像 registry 那样简单恢复。

registry 里的实例 lease 有明确过期时间，恢复时只要看 `expires_at`。health state 更微妙。比如某个 endpoint 在 Controller 崩溃前是 EJECTED，重启后如果直接恢复 EJECTED，可能会继续隔离一个已经恢复的实例；如果完全不恢复，又可能在故障仍然存在时短时间把流量打回慢实例。

当前比较稳的做法已经落到 runbook 里的 health snapshot backend：持久化带 `UpdatedAt` 和 max-age 的 health state。恢复时按状态区别处理：

1. `HEALTHY` 可以恢复，但 slow_score 要快速用新 telemetry 覆盖。
2. `DEGRADED` 可以短期恢复，过期后回到 HEALTHY 或 UNKNOWN。
3. `EJECTED` 不建议无限恢复，应该带最大保留时间，到期后进入 PROBING，而不是直接 HEALTHY。
4. `PROBING` 可以恢复为 PROBING，但 probe 窗口要重新计算。

我更倾向把 health state 看成“带保质期的控制面缓存”，而不是永久事实。真实状态要靠新 telemetry 重新确认。

### Q545【深度】SDK 默认 insecure credentials 在生产环境必须如何替换？

生产里必须把 `insecure.NewCredentials()` 替换成 TLS 或 mTLS transport credentials。

SDK 侧可以在 `DialOptions` 里增加字段：

```go
TransportCredentials credentials.TransportCredentials
```

如果用户提供，就用它；没有提供时本地 demo 可以继续用 insecure，但生产构建或生产配置应该禁止 insecure。Controller 侧也要从普通 `grpc.NewServer()` 改成带 credentials 的 server：

```go
grpc.NewServer(grpc.Creds(creds))
```

mTLS 时还要校验客户端证书。Controller 不能只确认“连接加密了”，还要知道“这个客户端是谁”。证书身份可以映射到 service owner、namespace、tenant 或 workload identity。Registry、Telemetry、PolicyService 的权限都应该基于这个身份判断。

如果接 SPIFFE/SPIRE，SDK 和 Controller 不需要手工管理证书文件，可以通过 workload API 获取 SVID，证书轮换也更自然。

### Q546【深度】服务实例恶意上报 telemetry 会怎样影响路由？如何防止？

恶意 telemetry 会直接影响 slow_score 和状态机。比如一个客户端伪造某个正常 endpoint 的高 latency、高 error、高 retransmit，Controller 可能把它打成 DEGRADED 或 EJECTED，正常流量被转走。反过来，它也可以伪造低延迟、低错误，把一个慢 endpoint 包装成健康。

这类问题很危险，因为治理系统本身会放大观测数据的影响。观测错了，路由就会错。

防护要分几层：

1. 身份认证。Telemetry 上报方必须用 mTLS 或 token 证明自己是谁。
2. 授权。某个 source 只能上报它实际调用过的 destination，不能替别的服务随便上报。
3. 端点校验。上报的 endpoint 必须存在于 registry，并且 service/instance/address 能对上。
4. 数据校验。request_count、latency、window 时间范围要合理；太老或太未来的窗口丢弃。
5. 多源聚合。不要让单个客户端的一小批样本立刻决定全局状态，可以按 source 加权，要求最小样本量，或者使用中位数类聚合。
6. 审计和告警。某个 source 突然上报异常 telemetry，要能查到。

当前项目适合可信实验环境。生产里不能把客户端上报当成天然可信事实。

### Q547【深度】恶意客户端注册大量假实例会造成什么问题？

问题会很直接。

第一，resolver 会拿到大量无效地址，客户端连接失败率上升，业务请求出现 `UNAVAILABLE` 或 timeout。

第二，内存和 CPU 被消耗。Registry map 变大，ListInstances 返回变慢，SDK resolver 更新地址列表也变重。Prometheus label 可能因为大量假 endpoint 产生高基数时序。

第三，负载均衡被污染。adaptive P2C 会在候选集合里看到假实例，即使请求失败后状态机会慢慢降权，前期仍然会伤害用户请求。

第四，真实实例可能被稀释。如果假实例很多，流量会被打散到不存在的地址上，表现像大面积下游故障。

防护方式包括：注册认证、每个 service 的注册权限、instance ID 命名规则、最大实例数限制、注册速率限制、地址合法性校验、TTL 上限、异常注册告警。生产里还可以把注册来源绑定到 Kubernetes Pod、ServiceAccount 或 workload identity，避免客户端随意声明地址。

### Q548【深度】policy file 被错误修改后，如何回滚和限制 blast radius？

首先要阻止坏配置直接全量生效。PolicyService 现在支持文件 reload 和 etcd snapshot watch，但策略写入仍应在进入文件或 etcd 前做 validation。比如 retry attempts 不能过大，非幂等方法默认不能开启 retry，eject threshold 不能低于 degraded threshold，probe ratio 不能超过上限。

然后是限制影响范围。策略应该有 service、namespace、tenant、caller、method 等 scope。一次变更只影响目标 scope，不应该默认全局生效。高风险字段，比如 retry、outlier ejection、circuit breaker，可以要求灰度比例或 allowlist。

回滚要靠修订。每个 `PolicySnapshot` 有 revision，但当前 revision 主要来自文件 modTime。生产里应该有配置修订历史，支持一键回滚到上一个已验证修订。SDK 收到旧修订要有规则：普通旧修订可以作为回滚事件接受，但不能被乱序 watch 消息误应用。可以用 `rollback_to_revision` 或新的 epoch 表达。

最后要有观测联动。策略发布后自动看 p99、error rate、retry amplification、endpoint state churn。如果超过 abort condition，自动回滚。这比事后人工查 YAML 快得多。

### Q549【深度】如果治理策略 bug 导致大量 endpoint EJECTED，如何设计紧急开关？

紧急开关要能绕过有问题的治理策略，优先恢复服务可用性。

我会设计几个层次。

第一层是全局 fail-open。Controller 或 SDK 提供开关：忽略 EJECTED 状态，把所有非 DEAD 实例重新放回候选集。这样可能牺牲尾延迟，但能避免没有可用 endpoint。

第二层是按 service 关闭 outlier ejection。保留 telemetry 和 metrics，但状态机不再推进 EJECTED，adaptive P2C 只按本地 inflight/EWMA 做温和选择。

第三层是冻结 policy revision。SDK 继续使用上一个稳定修订，拒绝新的有问题 snapshot。

第四层是最小健康实例数或最大摘除比例。比如一个 service 至少保留 2 个实例，最多摘除 30%。这能防止阈值 bug 一次摘光。

第五层是快速回滚和审计。紧急开关要有操作记录，不能变成长期隐藏状态。事故结束后要知道谁打开、什么时候打开、影响哪些服务、什么时候关闭。

### Q550【深度】如何在多团队环境中划分 service owner 和 policy owner？

我会把 service owner 和 policy owner 分开，但让它们有明确协作关系。

service owner 负责服务本身的 SLO、容量、依赖、实例标签和方法幂等性声明。比如 `CreateOrder` 能不能重试，必须由业务 owner 确认，平台团队不能凭空判断。

policy owner 负责治理策略模板和安全边界。比如 retry budget 的最大值、outlier detection 阈值范围、probe ratio 上限、哪些字段允许业务团队自助修改。平台团队可以给默认策略，但不能让每个团队随便把 retry 开到 5 次。

实际落地可以这样：

1. 每个 service 有 owner metadata。
2. Policy YAML 或配置平台里有 owner、reviewer、change reason。
3. 低风险字段允许 service owner 自助改，比如 timeout。
4. 高风险字段需要平台 owner review，比如全局 retry、ejection 阈值。
5. 所有变更都进审计日志，能追到人和修订。

这样既不会把平台团队卡成所有配置的审批瓶颈，也不会让业务团队随意改出连锁故障。

### Q551【深度】Controller 内存状态增长如何做 TTL、压缩和清理？

Controller 里会增长的状态主要有几类：registry instances、health map、policy cache、telemetry 聚合状态、Prometheus time series。每类清理方式不一样。

Registry 已经有 TTL lease 和 `SweepExpired`。实例不 heartbeat 后，到期清掉。这是最基本的清理。

Health map 也应该有 TTL。某个 endpoint 从 registry 消失后，它对应的 health state 不应该永久留在内存里。可以记录 last_updated，超过一段时间且 registry 已无此实例，就删除 health entry。

Telemetry 聚合状态要按窗口压缩。Controller 不应该长期保存每个 sample 的原始数据，只保存当前窗口、最近 N 个窗口或者导出到外部时序库。需要长期分析时，把数据交给 Prometheus、ClickHouse 或日志系统。

Policy cache 可以按 service 维度保留当前修订和少量历史修订。完整审计历史应该存外部配置系统或数据库，不适合一直放内存。

Prometheus label 要控制基数。如果 endpoint address、instance ID 高频变化，time series 会 churn。生产里要通过 instance 生命周期、label 规范和保留策略控制。

### Q552【深度】如何做 rolling upgrade，保证新旧 SDK 与 Controller 兼容？

原则是先扩展，再切换，最后废弃。

比如要给 PolicyService 加新字段，先升级 Controller，让它能返回新字段，同时旧字段继续保留。旧 SDK 会忽略新字段。然后升级 SDK，让它优先读新字段，读不到就用旧逻辑。等 SDK 覆盖率足够高，再把策略实际切到新字段上。

如果要升级 Controller 到多副本，先让新 Controller 兼容旧 SDK 的所有 RPC，再逐步把 SDK 的 controller 地址切到负载均衡入口。不要同时改协议、改地址、改策略语义。

SDK 侧要有 fallback：PolicyService 不存在时用默认策略；WatchPolicy 断线时用上一次 snapshot；ListInstances 失败时保留旧 resolver state 一段时间；telemetry 上报失败不能影响业务请求。

CI 要覆盖新旧组件混部。至少测旧 SDK + 新 Controller、新 SDK + 旧 Controller、策略字段缺失、Controller 暂时不可达。只跑同一套代码的单测不够。

## 拓展

### Q553【拓展】mTLS、SPIFFE/SPIRE 如何用于服务身份认证？

mTLS 解决两件事：通信加密和双向身份认证。客户端验证自己连的是 Controller，Controller 也验证客户端是谁。和普通 TLS 只验证服务端不同，mTLS 能让 Controller 基于客户端证书做授权。

SPIFFE 是一套 workload identity 规范，常见身份格式像：

```text
spiffe://example.org/ns/default/sa/frontend
```

SPIRE 是 SPIFFE 的实现，可以给 workload 自动签发和轮换 SVID 证书。AegisMesh 接入后，SDK 连接 Controller 时带上自己的 SVID，Controller 从证书里解析出 workload identity。

这样服务注册就能从“客户端说自己是 user-service”变成“证书证明它属于 user-service 或某个 service account”。Telemetry 上报、Policy Watch、Registry List 都可以按 identity 做授权。

对面试来说，这个回答要落到项目上：当前 AegisMesh 已支持文件证书形式的 TLS/mTLS、token RBAC 和静态证书主体 RBAC 映射；生产化改造时，SPIFFE/SPIRE 可以替换手工证书管理，并把 source/service 字段从自声明变成可校验身份。

### Q554【拓展】零信任网络中 RPC 治理组件应承担哪些安全职责？

零信任的前提是网络位置不可信。不能因为请求来自内网，就默认它可信。

AegisMesh 这类 RPC 治理组件至少要承担这些职责：

1. 身份认证。每个 SDK、agent、Controller 都要有可验证身份。
2. 授权。某个 workload 能注册哪些 service，能 watch 哪些 policy，能上报哪些 telemetry，要有规则。
3. 传输加密。控制面 gRPC 和 metrics/admin API 都不能明文裸奔。
4. 策略完整性。PolicySnapshot 要有修订、来源、审计和回滚能力。
5. 最小权限。eBPF agent、fault injector、Controller admin API 都要限制权限和目标范围。
6. 可观测审计。谁改策略、谁注册实例、谁上报异常 telemetry，要能查。

但它不应该替代业务鉴权。比如用户是否能下单，这是业务服务自己的权限逻辑。AegisMesh 负责治理平面的身份、安全边界和流量保护，不负责所有业务安全。

### Q555【拓展】多租户控制面如何隔离指标、策略和注册数据？

多租户隔离要从数据模型开始做，而不是后面靠命名约定补。

Registry 里每个 instance 应该有 tenant、namespace 或 environment 字段。`ListInstances` 必须带租户上下文，Controller 只能返回调用方有权看到的实例。

Policy 也要按租户隔离。同名 service 在不同租户下可能有完全不同的 timeout、retry、SLO。`PolicySnapshot` 应该有 tenant scope，WatchPolicy 也要校验调用方身份。

Telemetry 更敏感。一个租户不能看到另一个租户的 endpoint latency、错误率和拓扑。上报时要校验 source 和 destination 是否属于允许的租户关系。导出 Prometheus 时，tenant label 会带来高基数和权限问题，通常要接入多租户指标系统，比如 Mimir、Thanos receive 或按租户分 Prometheus。

审计日志也要隔离。平台管理员可以跨租户查看，租户管理员只能看自己范围内的策略和事件。

### Q556【拓展】如何给 eBPF agent 最小权限部署？

先从能力拆分开始。eBPF agent 需要加载 BPF 程序、读取 ringbuf、可能需要网络命名空间信息。不要直接给 `privileged: true`，能给具体 capability 就给具体 capability。

在新内核上，可以尝试使用 `CAP_BPF`、`CAP_PERFMON`，必要时才加 `CAP_NET_ADMIN`。如果需要读取内核符号或 tracefs，要把挂载点只读挂进去。容器 root filesystem 设成 read-only，禁止写宿主机敏感路径。

Kubernetes 部署时用 DaemonSet，但要限制 node selector 和 toleration，不要默认跑满所有节点。ServiceAccount 权限只给需要的资源。镜像要固定 digest，BPF object 随镜像发布，避免运行时从不可信位置加载。

agent 自身也要做输出限制。只上报必要的 endpoint 网络指标，不采集 payload。日志里不要打印过多连接细节。Controller 端对 agent telemetry 也要认证，不能因为它是 agent 就默认可信。

### Q557【拓展】如何做审计日志，记录谁修改了治理策略？

审计日志要记录能复盘一次策略变更的最小事实。

至少包括：操作者身份、租户或 namespace、service、变更前 revision、变更后 revision、变更摘要、完整 diff 或 diff hash、变更原因、审批人、提交时间、生效时间、客户端确认情况、回滚目标修订。

如果策略来自 GitOps，审计可以绑定 commit、PR、reviewer 和 CI 结果。如果策略来自配置平台，平台本身要生成 change_id，并把 change_id 写进 `PolicySnapshot` 或 metadata。SDK 上报 telemetry 或 trace 时带 route revision，事故复盘时就能知道某个请求命中了哪个策略修订。

审计日志不能只写本地文件。生产里应该写入不可随意修改的存储，比如数据库、对象存储、审计系统或 SIEM。查询权限也要控制，避免审计日志本身泄露服务拓扑。

### Q558【拓展】如何设计灾备：Controller 全部不可用时 SDK 的降级策略是什么？

Controller 全部不可用时，SDK 的目标是让业务流量尽量继续跑，但不要无限期相信旧状态。

我会分几层降级。

第一，resolver 保留最后一次成功解析的地址列表，并记录过期时间。短时间内 Controller 不可用，继续使用旧地址。

第二，policy watcher 断开后继续使用最后一个已确认的 PolicySnapshot。如果没有 snapshot，就用内置安全默认值：保守 retry、有限 timeout、非幂等方法不重试。

第三，telemetry 上报失败不阻塞业务请求。可以丢弃或本地短暂缓冲，但不能因为 Controller 不可用拖慢业务。

第四，health state 过期后逐渐衰减。不能永久相信旧的 EJECTED，也不能立刻忘掉刚发生的慢故障。可以设置 TTL：短期保留，过期后进入 UNKNOWN/PROBING 或回到本地 EWMA 决策。

第五，支持多个 Controller 地址和指数退避重连，避免所有 SDK 同时打爆恢复中的 Controller。

降级不是“永远可用”。如果 Controller 长时间不可用，新实例发现、策略更新和慢故障治理都会变差。SDK 应该暴露 metrics，让运维知道自己处在 degraded control-plane mode。

### Q559【拓展】如何对治理系统本身做 SLO？

治理系统也要有自己的 SLO，否则它出问题时会被误认为业务服务出问题。

Controller 可以定义这些 SLI：

1. `RegisterInstance`、`Heartbeat`、`ListInstances` 的成功率和 p99 延迟。
2. `ReportEndpointStats` 的成功率、处理延迟、样本丢弃率。
3. `WatchPolicy` 连接数、断线率、策略推送延迟。
4. registry 中实例过期清理延迟。
5. policy reload 成功率和失败次数。
6. health state 计算延迟和状态 churn。

SDK 侧也要有 SLI：resolver 最近成功时间、当前地址列表年龄、telemetry report 失败次数、policy revision age、retry budget 使用率、治理拦截器开销。

可以给出 SLO 示例：99.9% 的 `ListInstances` 在 100ms 内返回；Policy 变更 5 秒内被 99% SDK 收到；telemetry 上报成功率 99%；Controller 不可用时 SDK 能在 10 分钟内使用缓存地址继续服务。

这些指标要和业务指标分开看。治理系统的 SLO 失败，通常意味着 fail-slow 治理会变弱，不一定意味着业务立刻不可用。

### Q560【拓展】如果治理系统误判导致事故，事后如何定位和回滚？

我会按时间线复盘。

先确定事故窗口：什么时候 p99、error rate、retry amplification 或 endpoint state 开始异常。然后查当时的 PolicySnapshot revision、Controller 日志、SDK trace、Prometheus 指标和 verifier 报告。

重点看几类证据：

1. 哪些 endpoint 被标成 DEGRADED、EJECTED、PROBING。
2. slow_score 的组成项是什么，latency、error、timeout、network signal 哪个拉高了分数。
3. 是否有新 policy 生效，比如阈值、retry、timeout、probe ratio 被改过。
4. telemetry 是否来自少数异常 source，是否有伪造或偏差。
5. SDK resolver 是否拿到了过期地址，balancer 是否按预期降权。

回滚顺序要快：先打开 fail-open 或关闭 outlier ejection，恢复业务流量；再回滚 policy revision；必要时重启 Controller 清空错误 health state；最后再做根因分析。

事后要补防线。比如加最小健康实例数、最大摘除比例、策略 dry-run、灰度发布、telemetry sanity check、state transition audit。治理系统的误判本质上也是生产事故，不能只看业务服务日志，要把控制面决策本身记录下来。
