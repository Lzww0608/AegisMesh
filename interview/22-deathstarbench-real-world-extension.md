# 22. DeathStarBench、真实场景与系统扩展

## 简单

### Q589【简单】DeathStarBench 是用来验证什么类型问题的？

DeathStarBench 主要用来验证真实微服务拓扑里的性能和可靠性问题。它不是一个简单的单服务压测工具，而是一组更接近线上系统的微服务应用，比如 Social Network、Hotel Reservation 这类场景。

它适合验证几类问题：

1. 多服务调用链下的尾延迟。
2. 下游慢故障如何影响上游和入口请求。
3. 服务之间的 fan-out、依赖链、缓存、存储访问带来的放大效应。
4. 不同请求类型混合时，负载均衡和重试策略是否仍然有效。
5. 观测系统能否把端到端 latency 和 per-service latency 对齐起来。

对 AegisMesh 来说，DeathStarBench 的价值是把项目从“自研 shop demo 能跑”推进到“更复杂服务图也能解释治理效果”。不过当前仓库还没完成真实 DeathStarBench 测评，只提供了集成计划生成器。

### Q590【简单】项目里的 DeathStarBench adapter 目前输出什么？

当前 adapter 读取 `experiments/deathstarbench/social-network.yaml`，输出一个 JSON 格式的 integration plan。

这个 plan 里主要有四类信息：

1. `compose_command`：比如 `docker compose -f socialNetwork/docker-compose.yml up -d`，告诉你外部 DeathStarBench repo 里应该启动哪个 compose 文件。
2. `workload_command`：比如 `wrk2 -t4 -c64 -d60s http://localhost:8080`，告诉你入口压测命令怎么跑。
3. `environment`：包含 `AEGIS_CONTROLLER` 和 `AEGIS_SERVICE_MAP`。前者是 AegisMesh Controller 地址，后者把 DeathStarBench 服务名映射到 AegisMesh 的 service 名和端口。
4. `service_names`：列出配置中涉及的 DeathStarBench 服务名，方便脚本或人检查映射是否齐全。

命令是：

```bash
go run ./cmd/deathstarbench-adapter --config experiments/deathstarbench/social-network.yaml
```

它不会 clone DeathStarBench，不会修改外部服务代码，也不会自动跑 benchmark。它现在是 plan generator，不是完整 benchmark runner。

### Q591【简单】为什么 Social Network workload 比 shop demo 更有说服力？

shop demo 是我们自己写的最小微服务系统，主要用于证明 AegisMesh 的功能闭环：注册、发现、telemetry、slow_score、状态机、adaptive P2C、retry budget、trace verifier。它可控，适合开发和调试。

Social Network workload 更有说服力，是因为服务图更复杂，请求类型更多，依赖关系也更接近真实系统。一次入口请求可能经过用户服务、社交图服务、帖子存储、缓存、数据库等多个组件。某个下游慢了，不一定只影响一个简单 RPC，而可能通过 fan-out 和排队影响整条链路。

这类 workload 更容易暴露 demo 看不到的问题，比如：上游慢到底是自身慢还是下游拖慢；重试在多跳链路里是否放大；adaptive routing 会不会把瓶颈转移到另一个服务；method-level policy 是否够细。

所以 Social Network 不是为了替代 shop demo，而是下一层验证。shop demo 说明机制能跑，Social Network 才更能说明机制在复杂拓扑里有没有价值。

### Q592【简单】integration plan generator 和真正接入 benchmark 有什么区别？

integration plan generator 只是把接入所需的信息整理出来。它告诉你用哪个 compose 文件、入口 URL 是什么、wrk2 怎么跑、哪些 DeathStarBench 服务映射到 AegisMesh service 名。

真正接入 benchmark 要做的事多得多：

1. 拉取或准备外部 DeathStarBench repo。
2. 修改或包装服务启动方式，让服务注册到 AegisMesh Controller。
3. 让客户端调用走 AegisMesh SDK、sidecar 或其他治理路径。
4. 接入 telemetry、trace、metrics。
5. 写 fault injection 脚本，能对指定服务注入 delay、CPU、packet loss。
6. 跑 workload，采集端到端 p99、per-service latency、retry amplification、recovery curve。
7. 生成报告，并和 baseline 对比。

当前项目只有第一步的准备能力。它能降低后续接入成本，但不能代表已经完成 DeathStarBench 实验。

### Q593【简单】服务映射在 benchmark 集成中起什么作用？

服务映射负责把 DeathStarBench 自己的服务名转换成 AegisMesh 能理解的服务名和端口。

当前配置里有类似这样的映射：

```yaml
services:
  nginx-thrift:
    aegis_name: frontend
    port: 8080
  user-service:
    aegis_name: user-service
    port: 9090
  social-graph-service:
    aegis_name: social-graph-service
    port: 9090
```

它的作用有三点。

第一，注册发现需要知道 service 名。DeathStarBench 里的 `social-graph-service` 要对应到 AegisMesh registry 里的同名或规范化 service。

第二，telemetry 需要归属。SDK 上报 `EndpointStatsSample` 时，要知道目标是哪个 service 和 instance。

第三，策略要能匹配。Policy YAML 里的 retry、timeout、outlier detection 都是按 service 和 method 组织的。没有服务映射，策略很难套到外部 benchmark 上。

简单说，服务映射是 AegisMesh 和 DeathStarBench 服务图之间的翻译层。

### Q594【简单】真实 benchmark 中哪些服务最适合作为慢故障注入目标？

最适合的目标通常是“被入口请求频繁调用、又不是唯一入口”的中间服务或存储访问服务。

在 Social Network 这类应用里，候选目标可以是：

1. user-service：很多请求会查用户信息，慢了会影响入口 latency。
2. social-graph-service：涉及好友关系、关注关系，常出现在 fan-out 路径里。
3. post-storage-service：偏存储访问，适合模拟后端 I/O 慢。
4. compose-post 或 user-timeline 这类处在请求路径中间的服务，如果实际部署里有这些组件，也适合注入。

不建议一开始就对入口 nginx 注入，因为入口慢会让整个系统都慢，难以验证 per-endpoint routing。也不建议一开始就对数据库或缓存做强故障，因为那可能让所有上游都一起慢，relative slow_score 很难区分实例级异常。

比较好的实验是：同一个服务保留两个或多个实例，只对其中一个实例注入 200ms 到 800ms delay，看 adaptive P2C 能否把流量转走。

### Q595【简单】为什么不能只用一个 demo service 证明 service mesh 治理能力？

一个 demo service 能证明功能，但不能证明复杂系统里的治理效果。

服务治理真正难的地方在多实例、多服务、多调用路径。比如慢故障可能只发生在某个实例上，也可能由下游慢导致；一次入口请求可能调用多个下游；不同方法的幂等性不同；重试可能在多层同时发生；某个局部优化可能让另一个服务压力变大。

单个 demo service 的好处是可控，适合开发算法和做最小实验。缺点是它隐藏了真实拓扑里的很多问题。比如只要一个 frontend 调一个 user-service，判断谁慢很容易；到了 Social Network，入口 p99 变差可能来自用户服务、社交图、存储、缓存、负载生成器，甚至容器调度。

所以项目叙述应该分两层：shop demo 用于可复现实验和功能验证，DeathStarBench 用于后续真实拓扑验证。不能把前者的结果直接夸大成后者的结论。

### Q596【简单】如果接入外部 benchmark，最先要打通哪些脚本？

我会先打通五类脚本。

第一是环境准备脚本。包括 clone DeathStarBench、检查 Docker/compose、确认端口和依赖。

第二是启动脚本。比如 `scripts/run_deathstarbench_social.sh`，负责启动 AegisMesh Controller、启动 DeathStarBench Social Network、注入必要环境变量。

第三是注册或接入脚本。要让 DeathStarBench 服务注册到 AegisMesh，或者让调用流量经过 AegisMesh SDK/sidecar。这是最难的一步。

第四是 workload 脚本。用 wrk2、wrk、k6 或 DeathStarBench 自带 workload 生成稳定负载，输出端到端 latency。

第五是实验脚本。包括 inject-delay、inject-cpu、inject-loss、reset-faults、collect-results、merge-results、check-results。没有清理脚本，实验很容易互相污染。

当前 adapter 只提供 plan。真正开始接入时，第一步应该把 plan 变成可执行的启动和 workload 脚本。

### Q597【简单】benchmark 中如何采集端到端 latency 和 per-service latency？

端到端 latency 通常由负载生成器采集。比如 wrk2 会输出请求延迟分布，包含 p50、p95、p99、吞吐和错误。它回答的是用户视角：入口请求慢不慢。

per-service latency 要靠服务内部或 RPC 层 telemetry。AegisMesh SDK 可以记录 destination、method、upstream、latency EWMA、p95、错误、timeout、inflight，再上报给 Controller。Prometheus 也可以抓 SDK 侧指标。

如果接入 tracing，还可以从 trace 里看到每条请求经过哪些服务、每一跳耗时多少。这个信息能把端到端 p99 分解到具体服务。

真实 benchmark 最好三类数据一起收：

1. 负载生成器的端到端 latency。
2. AegisMesh SDK 的 per-endpoint RPC latency。
3. tracing 或日志里的 per-service span latency。

只有端到端 latency，不知道慢在哪；只有 per-service latency，不知道用户有没有变好。两边要对齐。

### Q598【简单】如何把 AegisMesh policy 应用到 DeathStarBench 服务图？

先要把 DeathStarBench 服务名映射到 AegisMesh service 名。比如 `user-service`、`social-graph-service`、`post-storage-service`。然后为每个 service 写 Policy YAML。

策略可以分几层。

服务级策略包括 routing policy、retry budget、outlier detection、circuit breaker。例如对 `user-service` 使用 adaptive P2C，设置 absolute SLO 和 slow_score 阈值。

方法级策略要按请求语义配置。读请求通常更适合重试，比如查询用户、查询社交图。写请求要谨慎，比如发帖、更新用户状态、写 timeline，这类需要禁用重试或使用 idempotency key。

最后要让 SDK 真的拿到策略。SDK Dial 时先 `GetPolicy`，随后通过 `WatchPolicy` 接收更新。DeathStarBench 服务接入 AegisMesh 后，就能按这些策略路由和上报 telemetry。

难点是 DeathStarBench 很多服务可能不是 Go/gRPC。那就需要 sidecar、代理层或改造服务 client，不一定能直接复用当前 Go SDK。

## 深度

### Q599【深度】DeathStarBench 服务图更复杂后，slow_score 是否需要考虑调用链上下文？

需要。当前 slow_score 主要按 service/instance 聚合 endpoint telemetry，适合识别“同一个服务里某个实例比其他实例慢”。在简单 demo 里这足够用。

复杂服务图里，单纯实例级分数可能不够。比如 frontend 调 user-service 慢，可能是 user-service 自己慢，也可能是 user-service 调下游 social-graph 慢。此时只看 frontend 到 user-service 的 RPC latency，容易把 user-service 实例当成根因。

调用链上下文能帮助区分两种情况。比如 trace 显示 user-service 自己处理只花 5ms，但下游 social-graph 花 300ms，那应该把主要嫌疑放到 social-graph。AegisMesh 可以把 trace span latency、upstream/downstream 关系、method 信息放进分析里。

更细的设计是按 service + method + caller 维度评分。frontend 调 user-service/GetUser 慢，不一定代表所有 caller 调 user-service 都慢。这样能减少误伤，但会带来更高维度和更多样本量要求。

所以答案是：基础 slow_score 可以继续作为 endpoint 健康信号，但接入 DeathStarBench 后，应加入调用链上下文，至少在报告和 verifier 里能解释慢故障传播路径。

### Q600【深度】如果上游慢是下游慢导致的，AegisMesh 如何避免把上游实例误判为慢？

首先要承认：只看客户端观察到的 RPC latency，很难完全避免误判。上游响应慢，客户端确实看到它慢，但根因可能在更下游。

减少误判可以从几层做。

第一，按 method 和 downstream 分解 telemetry。上游只有某个方法慢，且这个方法正好调用慢下游，就不要把整个上游实例都打成慢。

第二，引入 trace。通过 span 可以看到每一跳耗时。如果上游 span 里大部分时间都在等待下游，状态判断应更偏向下游服务。

第三，比较多个 caller。如果所有 caller 调某个下游都慢，说明下游更可能是根因；如果只有一个 caller 看到慢，可能是 caller 自身、网络路径或调用模式问题。

第四，状态机可以区分“本地慢”和“依赖慢”。比如 EndpointHealth 增加 reason：application_cpu、downstream_wait、network_retransmit、unknown。路由时可以先对根因下游降载，而不是立刻摘除上游。

当前 AegisMesh 还没有完整的根因分析，只能通过 telemetry、eBPF 网络信号和 trace verifier 做辅助。接入复杂 benchmark 后，这是很值得扩展的一块。

### Q601【深度】多跳 RPC 下 retry budget 应该按每一跳还是全链路控制？

只按每一跳控制不够。每一跳都有自己的 retry budget，能保护单个客户端和单个下游之间的放大，但多跳链路里会出现乘法效应。

举个例子，入口请求调用 A，A 调 B，B 调 C。每一层都最多重试 2 次，最坏情况下底层 C 可能看到远超入口请求数的尝试。即使每一层“局部看起来合理”，全链路仍然可能被重试放大。

更好的设计是两层预算。

第一层是 per-hop retry budget。它控制某个调用方对某个下游的额外请求，防止局部 retry storm。

第二层是 end-to-end retry context。入口请求带一个 retry budget token 或 attempt metadata，沿调用链传下去。下游再重试时要消耗同一个全链路预算。这样能控制总放大。

当前 AegisMesh 的 retry budget 更偏 per-ClientConn/per-method。它已经能在单跳实验里把 amplification 从 2.0x 降到约 1.15x，但接入 DeathStarBench 后，应该考虑全链路 retry context，否则多跳场景里结论会打折。

### Q602【深度】真实 workload 中请求类型混合，方法级 policy 是否足够？

方法级 policy 是必要的，但不一定足够。

它能解决最基本的问题：读请求和写请求不能用同一套重试策略。比如查询用户信息可以重试，创建订单、发帖、扣库存这类写请求不能随便重试。AegisMesh 当前的 method policy 已经能表达 idempotent、timeout 和 retry override。

真实 workload 还会有更多维度。比如同一个方法在不同 caller 下成本不同；同一个查询方法对 VIP 用户和普通用户有不同 SLO；同一个 endpoint 在不同 region 的 latency 基线不同；某些请求携带大 payload，耗时天然更长。

所以方法级 policy 是第一层。更完整的策略可能要加入 caller、tenant、region、header、request class、payload size 等匹配条件。proto 上可以从 `map<string, MethodPolicy>` 演进到 repeated rules，并定义 priority。

面试里可以说：当前 method-level policy 解决了重试幂等性这个最容易被追问的问题；真实 workload 下，还需要多维策略匹配。

### Q603【深度】如何区分服务端业务瓶颈、网络瓶颈、客户端负载生成瓶颈？

要同时看三类信号。

服务端业务瓶颈通常表现为服务内部处理时间增加、CPU 饱和、队列长度上升、GC 或锁竞争变多。RPC latency 高，但网络层 retransmit 不一定高。

网络瓶颈通常表现为 TCP retransmit、connect error、connect latency、RTT 增加，可能伴随 packet loss。应用服务 CPU 未必高。AegisMesh 的 eBPF agent 当前采集 TCP retransmit、connect error、connect latency 这类信号，就是为了把网络慢和应用慢分开。

客户端负载生成瓶颈则表现为 generator 自身 CPU 打满、连接数不足、请求发不出去、吞吐上不去但服务端压力不高。wrk2 或 k6 所在机器的 CPU、网络、错误日志都要看。

真实 benchmark 里要把这些信号时间对齐。比如 p99 在 12:01:10 变高，同一秒 eBPF retransmit 也变高，那更像网络；如果服务端 CPU 在同一窗口满了，更像业务处理瓶颈；如果只有负载机 CPU 满，而服务端没压力，那就是压测端问题。

### Q604【深度】如果某个下游被 adaptive P2C 降载，上游 throughput 变化如何解释？

要分情况看。

如果慢实例被降载后，上游 throughput 上升，通常说明请求不再排队等慢 endpoint，整体完成速度变快。这是理想情况。

如果 p99 降了但 throughput 没明显变，说明治理主要减少了尾部请求，对平均吞吐影响不大。这个也合理，很多慢故障治理本来就是为了尾延迟。

如果 throughput 下降，可能有几种原因。第一，健康实例容量不足，流量转走后它们被压满。第二，circuit breaker 或 state machine 太激进，候选 endpoint 变少。第三，上游因为下游 EJECTED 或 retry budget 限制，开始快速失败。第四，负载生成器或其他依赖成了瓶颈。

所以不能只看 throughput，也不能只看 p99。要一起看 route weight、slow_score、endpoint state、inflight、error rate、retry amplification。adaptive P2C 的目标不是盲目提高吞吐，而是在慢故障下把流量从坏实例转到相对好的实例，并控制尾延迟和错误放大。

### Q605【深度】复杂拓扑中 verifier 的 forbidden edge 和 route distribution 是否足够表达策略？

不够，但它们是一个好的起点。

route distribution 能检查流量比例，比如 canary 90/10 是否达标，PROBING endpoint 是否只拿到少量流量。forbidden edge 能检查不该出现的调用边，比如 frontend 不能直接调 payment-service。

复杂拓扑里还需要更多表达能力。比如一次请求路径不是线性的，而是 DAG；某个服务会 fan-out 到多个下游；异步消息没有同步 RPC span；策略可能要求“如果 route 到 secondary，就不能再调用 primary 的 downstream”；retry attempts 要按全链路聚合，而不是单 trace 字段。

所以 verifier 可以扩展成 trace policy DSL。检查对象可以从最终 route 扩到 path pattern、service DAG、span attributes、policy revision、attempt metadata、tenant/caller 维度。

当前 AegisMesh verifier 已经能检查 route distribution、retry attempts、forbidden edges，也支持真实 SDK JSONL trace。接入 DeathStarBench 后，它需要从“线性路径校验”扩展到“服务图约束校验”。

### Q606【深度】如何在 benchmark 中引入可重复的 CPU、network、application fault？

可重复的故障要满足三点：目标明确、参数固定、清理可靠。

CPU fault 可以用 Docker 的 CPU 限制，比如 `docker update --cpus 0.25 <container>`，或者在容器里跑受控 CPU burner。参数要记录到 run_meta：目标容器、CPU 配额、开始时间、持续时间。

Network fault 可以用 `tc netem` 注入 delay、jitter、loss。比如只对某个容器的 veth 或容器内网卡注入 200ms delay 和 50ms jitter。难点是 Docker bridge 网络的方向和设备映射，要确认故障打在目标路径上。

Application fault 可以在服务里加可控 sleep、错误率、慢概率。比如 10% 请求 sleep 500ms。这类故障最稳定，也最容易解释，但它偏应用层，不会触发 eBPF 网络信号。

每个实验开始前要 reset，结束后也要 reset。脚本失败时也要尽量清理。否则下一组 baseline 会被上一组故障污染。

### Q607【深度】DeathStarBench 接入后，哪些当前 Demo 结论可能不再成立？

有几类结论可能变弱。

第一，adaptive P2C 在 single_instance_delay 下 p99 降幅很大，这在复杂拓扑里不一定还能达到同样比例。因为入口 p99 可能由多个下游共同决定，绕开一个慢实例只能解决一部分问题。

第二，slow_score 的相对异常检测在实例数量少或所有实例都慢时会变弱。项目已经加了 absolute SLO score，但真实 workload 的每个方法 SLO 不同，还需要更细配置。

第三，retry budget 的单跳 amplification 结果不等于全链路 amplification。DeathStarBench 多跳链路里，各层重试叠加会更复杂。

第四，eBPF network score 的提升可能更难解释。真实拓扑里网络、应用、存储、缓存混在一起，packet loss 对入口 p99 的影响不一定直接。

第五，单机 Docker Compose 结果不能直接代表多节点环境。跨节点网络、真实负载均衡、Kubernetes 调度、服务发现延迟都会改变收敛时间。

这不是坏事。真实 benchmark 的意义就是把 demo 里过于干净的假设打破，让项目结论更可信。

### Q608【深度】如何用 benchmark 结果证明 AegisMesh 对真实微服务拓扑有价值？

要证明价值，不能只给一张 p99 下降图。需要一组对照。

第一组是 no-fault overhead。没有故障时，AegisMesh 相比原生调用或 baseline mesh 的延迟和吞吐开销是多少。这个开销必须可接受。

第二组是 slow-instance fault。对一个下游实例注入 delay，比较 round_robin、static threshold、adaptive P2C + slow_score 的 p99、错误率、route weight。

第三组是 multi-hop retry amplification。比较无预算、per-hop budget、全链路 budget。看底层服务收到的尝试数有没有被控制住。

第四组是 recovery curve。故障注入、故障持续、故障解除后，记录 slow_score、endpoint state、route weight、p99 随时间变化。要能说出收敛时间。

第五组是 ablation。关闭 slow_score、关闭 adaptive P2C、关闭 eBPF network score、关闭 absolute SLO，分别看结果变化。这样才能说明不是某个偶然因素导致结果好。

最后要给统计。每组重复多次，报告 median、p95 或置信区间。不要只挑一次最好结果。

## 拓展

### Q609【拓展】微服务 benchmark 与真实生产系统的差距主要在哪里？

差距主要在工作负载、数据、部署和组织流程。

工作负载上，benchmark 的请求分布通常是固定脚本，真实生产有突发流量、长尾用户、节假日、灰度发布、爬虫、批任务。请求参数也更复杂。

数据上，benchmark 数据集规模有限，缓存命中率、热点 key、数据库索引、租户隔离都不一定接近生产。很多尾延迟来自数据分布，而不是服务代码本身。

部署上，单机 Docker Compose 和真实 Kubernetes 多节点差距很大。真实环境有跨机网络、节点噪声、资源抢占、滚动发布、HPA、sidecar、service discovery 延迟。

组织流程上，生产系统有权限、审计、变更窗口、值班、回滚、SLO、告警。benchmark 通常只看技术路径，不覆盖这些约束。

所以 benchmark 结果能证明方向和机制，但不能直接宣称生产收益。比较稳的说法是：“在可复现 benchmark 中验证了 fail-slow 治理效果，生产落地还需要结合真实流量和运维体系做灰度验证。”

### Q610【拓展】如何将 tracing、profiling、kernel telemetry 结合定位跨服务尾延迟？

三类信号各看一层。

Tracing 看请求路径。它告诉你一次慢请求经过了哪些服务、每个 span 耗时多少、在哪一跳开始变慢。

Profiling 看进程内部。它告诉你某个服务慢的时候，CPU 花在哪、锁在哪里等、GC 是否明显、内存分配是否异常。

Kernel telemetry 看网络和内核层。eBPF 能看到 TCP retransmit、connect error、connect latency、RTT 等应用层不容易直接看到的信号。

定位时先从 tracing 找慢 span。比如入口 p99 慢，trace 显示 social-graph span 耗时 400ms。然后看 social-graph 的 profiling，如果 CPU 或锁很高，偏业务瓶颈；如果 profiling 正常，但 eBPF retransmit 和 connect latency 高，偏网络瓶颈；如果两者都正常，可能是下游存储或负载生成器问题。

AegisMesh 当前已经有 SDK telemetry、真实 trace JSONL verifier、eBPF TCP telemetry。下一步是把这些信号按 trace_id、endpoint、时间窗口对齐，形成更完整的尾延迟定位链路。

### Q611【拓展】如果服务使用异步消息而非 RPC，AegisMesh 的治理思想如何迁移？

异步消息没有同步 RPC 的“请求等待响应”模型，所以不能直接套 gRPC balancer 和 per-try retry。

但治理思想可以迁移。

服务发现和路由可以变成 topic/partition/consumer group 的选择。慢故障不再是某个 RPC endpoint 慢，而是某个 consumer lag 增长、处理耗时变长、重试队列堆积。

slow_score 可以换成消息系统指标：消费延迟、lag、失败率、DLQ 数量、重投次数、处理耗时 p95、broker 网络错误。状态机仍然可以用 HEALTHY、DEGRADED、EJECTED、PROBING，但 EJECTED 的含义可能是暂停给某个 consumer 分配 partition，或者降低它的拉取速率。

retry budget 也要换语义。消息重试通常有延迟队列、死信队列、最大投递次数，不能像 RPC 一样立即重试。幂等性仍然重要，甚至更重要，因为消息至少一次投递很常见。

所以迁移方向是：保留 telemetry + scoring + state machine + budget 的思想，替换传输层实现和指标语义。

### Q612【拓展】如何做全链路 admission control，避免局部优化导致全局恶化？

局部优化可能出问题。比如某个下游实例被降载，流量转到其他实例，结果把健康实例也打满；某个上游为了降低自己错误率快速失败，导致入口可用性下降。

全链路 admission control 要从入口和下游两端一起做。

入口侧要限制进入系统的总请求量。可以按 service、method、tenant、priority 设 token bucket 或 concurrency limit。系统过载时先保护高优先级请求。

下游侧要暴露负载信号，比如 queue length、CPU、inflight、error rate。上游 routing 不能只看自己的 latency，也要看下游是否已经接近容量上限。

链路级要传递预算。比如 deadline、retry budget、priority、request cost 从入口一路传下去。下游知道这个请求剩多少时间和预算，没必要处理已经超时的低优先级请求。

控制面要有全局约束，比如最大摘除比例、最小健康实例数、全链路 retry cap。这样 adaptive P2C 的局部决策不会把全局系统推向更差状态。

### Q613【拓展】如果接入 Kubernetes，DeathStarBench 的部署、service discovery、fault injection 怎么改？

部署上，DeathStarBench 服务要从 docker-compose 变成 Kubernetes manifests 或 Helm chart。每个服务是 Deployment，入口是 Service 或 Ingress，配置走 ConfigMap 和 Secret。

服务发现上，可以有两种路线。一种是 AegisMesh Registry 继续独立存在，服务启动时向 Controller 注册。另一种是 Controller watch Kubernetes EndpointSlice，把 Pod IP、Service、labels 同步成 AegisMesh instances。后者更像云原生做法，少改业务服务。

fault injection 上，不能再直接对 Docker container name 操作。网络故障可以用 Chaos Mesh、tc sidecar、Cilium/iptables，或者在节点上定位 Pod veth。CPU throttle 可以改 Pod resource limit 或使用 chaos 工具。应用层 fault 可以通过环境变量、配置或 service wrapper 控制。

观测上，Prometheus scrape 要通过 ServiceMonitor 或 PodMonitor，trace 可以接 OpenTelemetry Collector，eBPF agent 用 DaemonSet 部署。

策略上，Policy YAML 可以变成 CRD，比如 `AegisPolicy`。Controller watch CRD，生成 PolicySnapshot 下发给 SDK。这样和 Kubernetes 的权限、审计、GitOps 更容易接上。

### Q614【拓展】如何比较 AegisMesh 与 Istio/Envoy 在同一 benchmark 下的性能？

要做公平对比，变量要控制住。

第一，使用同一套 benchmark、同一套数据、同一组 workload 参数、同一台机器或同一组节点。不能 AegisMesh 用轻负载，Istio 用重负载。

第二，定义清楚对比模式。比如 no mesh、AegisMesh SDK、Istio/Envoy sidecar。三组都跑 no-fault、slow-instance delay、packet loss、retry storm、recovery。

第三，策略要尽量等价。Istio/Envoy 配 outlier detection、retry、least request；AegisMesh 配 slow_score、adaptive P2C、retry budget。两边阈值不可能完全一样，但目标要一致。

第四，采集指标要一致。端到端 p99、吞吐、错误率、CPU、内存、sidecar/SDK 开销、收敛时间、retry amplification 都要记录。

第五，报告要诚实。Istio/Envoy 是成熟系统，功能面和生产能力远超个人项目；AegisMesh 的差异点应该放在 fail-slow scoring、SDK 内部自适应路由、实验可解释性上，而不是声称全面超过。

### Q615【拓展】如何设计论文式 evaluation，让面试官相信结论不是 cherry-pick？

要把实验设计写清楚，而不是只放结果。

首先定义研究问题。比如：AegisMesh 能否降低慢实例故障下的 p99？retry budget 能否控制放大？absolute SLO 能否发现所有实例同时变慢？

然后定义 baseline。至少包括 no mesh、round_robin、static threshold、without retry budget、without eBPF network score。没有 baseline，结果没有解释力。

再定义 workload 和 fault。请求数、并发、持续时间、目标服务、delay、jitter、loss、CPU throttle 都要写清楚。每个实验只改一个主要变量。

结果要重复多次，报告 median、p95 或置信区间。不要只报最好一次。异常结果也要解释，不能删掉。

还要做 ablation。关闭某个模块后效果下降，才能说明这个模块有贡献。比如关闭 slow_score 后 adaptive P2C 是否变差，关闭 retry budget 后 amplification 是否上升。

最后写限制。单机 Docker Compose、负载生成器、容器网络、DeathStarBench 是否真实接入，这些都要说清楚。面试官反而更信这种有边界的报告。

### Q616【拓展】如果 benchmark 显示 no-fault overhead 明显，如何优化？

先定位 overhead 来自哪里。AegisMesh 的 no-fault overhead 可能来自 SDK interceptor、telemetry recorder、Prometheus metrics、trace JSONL、resolver refresh、adaptive balancer Pick、policy watcher。

可以按层优化。

第一，关掉不必要的热路径工作。生产默认不应该 100% 写 JSONL trace，可以采样。Prometheus label 要控制，metrics 更新尽量轻。

第二，优化 Recorder。当前 p95 用窗口内 latency slice 排序，低 QPS 没问题，高 QPS 可以换 histogram、DDSketch 或 HDR Histogram。计数用 atomic 或 sharding，减少全局锁。

第三，优化 balancer。Pick 里只做常数级计算，endpoint stats 尽量缓存，address attributes 解析不要重复做重活。PROBING 和 slow_score 权重可以提前转换成 effective weight。

第四，控制后台频率。resolver refresh、telemetry report、policy watch 不要过于频繁。状态变化可以用 watch/push，减少无意义 polling。

第五，做 profile。用 pprof 和 benchstat 看 ns/op、allocs/op、mutex 等待，不要靠猜。优化后再跑 no-fault benchmark，确认 p99 和吞吐真的改善。

如果优化后仍有 overhead，就要在报告里诚实写出来：AegisMesh 用少量 no-fault 开销换慢故障场景下的尾延迟收益。这个 tradeoff 是否值得，要看业务对 p99 和故障恢复的要求。
