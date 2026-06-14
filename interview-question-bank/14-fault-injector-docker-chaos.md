# 14. Fault Injector、Docker 与故障注入

## 简单

### Q365【简单】项目支持哪些故障注入类型？

项目里有两类故障注入。

第一类是 `cmd/fault-injector` 提供的 Docker / tc 级别故障，主要用于实验脚本：

- `delay`：用 `tc netem delay` 给目标容器的网卡注入固定延迟。
- `delay + jitter`：在固定延迟上叠加抖动，比如 `200ms 50ms`。
- `loss`：用 `tc netem loss` 注入丢包率。
- `cpu`：用 `docker update --cpus` 限制目标容器 CPU。
- `reset`：删除目标网卡上的 `tc qdisc`，用于清理网络故障。

第二类是 demo 服务自己的应用级故障。`cmd/demo-user` 和 `cmd/demo-order` 支持：

- `--slow-probability`
- `--slow-duration`
- `--error-probability`

这类故障不改网络，也不改容器资源，而是在业务 handler 里按概率 sleep 或直接返回错误。retry 实验里的 `retry-user-service` 就是一个专门用于制造失败请求的服务，实验环境里把它配置成一直返回错误，方便观察重试放大。

面试时我会强调一点：项目没有把所有混沌工程能力都做完，它实现的是一组可复现实验所需的故障模型，覆盖网络慢、网络丢包、CPU 资源不足、应用慢调用和应用错误。

### Q366【简单】tc netem 可以模拟哪些网络故障？

`tc netem` 是 Linux traffic control 里的网络仿真模块。它能模拟很多链路层面的异常，比如：

- 延迟；
- 延迟抖动；
- 丢包；
- 包重复；
- 包乱序；
- 包损坏；
- 带宽受限。

AegisMesh 当前只封装了实验里真正用到的两类：`delay` 和 `loss`。延迟用于模拟 fail-slow，因为服务还活着，连接也能通，但响应明显变慢。丢包用于观察 TCP 重传、gRPC 尾延迟和 eBPF 网络信号。

没有封装重复包、乱序和损坏，不是因为做不了，而是项目的主线是慢故障治理。先把最能影响 p95 / p99、slow_score 和 retry 行为的场景跑清楚，价值更高。

### Q367【简单】CPU throttle 和应用级 slow-probability 有什么区别？

CPU throttle 是资源层面的故障。项目里通过：

```bash
docker update --cpus 0.25 aegis-user-b
```

把目标容器限制到较低 CPU 配额。它的结果通常是请求排队、业务处理变慢、inflight 升高，但服务不一定报错。TCP 连接、健康检查、服务注册可能都还是正常的。

`slow-probability` 是应用层故障。比如：

```bash
go run ./cmd/demo-user \
  --slow-probability 1 \
  --slow-duration 250ms
```

它表示每个请求进入 handler 后按概率 sleep。这个故障更可控，适合做功能验证，因为慢调用持续时间和概率都由参数决定。

两者的区别在于故障来源不同。CPU throttle 更接近真实机器资源竞争，副作用也更多，可能影响日志、心跳、metrics 暴露。应用级 sleep 更干净，适合验证 slow_score、timeout、retry 这些逻辑是否按预期工作。

### Q368【简单】为什么 fault-injector 默认只打印命令，不直接执行？

因为故障注入会改运行环境。`tc qdisc add` 会影响目标容器的网络，`docker update --cpus` 会改变容器资源限制。如果命令写错目标容器，实验结果会被污染，严重时会影响正在跑的其他服务。

所以 `cmd/fault-injector` 默认是 dry-run，只打印即将执行的命令，例如：

```bash
docker exec aegis-user-b tc qdisc add dev eth0 root netem delay 200ms 50ms
```

确认没问题后，加 `--execute` 才真正执行。Makefile 里的 `make inject-delay`、`make inject-loss`、`make inject-cpu` 会带上 `--execute`，因为这些命令本来就是给实验使用的。

这个设计有两个好处。第一，手动实验时可以先检查命令。第二，CI 里可以测试命令生成逻辑，而不要求 CI runner 一定有 Docker、tc 和特权网络权限。

### Q369【简单】make inject-delay 和 reset-faults 的作用是什么？

`make inject-delay` 用来给目标容器注入网络延迟。默认目标是 `aegis-user-b`，默认网卡是 `eth0`，默认延迟和抖动来自 Makefile 里的变量。常见用法是：

```bash
make inject-delay TARGET=aegis-user-b DELAY=200ms JITTER=50ms
```

它最终会调用：

```bash
go run ./cmd/fault-injector \
  --kind delay \
  --container aegis-user-b \
  --device eth0 \
  --delay 200ms \
  --jitter 50ms \
  --execute
```

`make reset-faults` 用来清理故障：

```bash
make reset-faults TARGET=aegis-user-b
```

脚本会做两件事：删除目标容器上的 `tc qdisc`，再用 `docker update --cpus 0` 清掉 CPU 限制。每次实验结束后都应该跑它。否则下一组实验会继承上一组故障，结果就很难解释。

### Q370【简单】Docker container name 在故障注入中起什么作用？

故障注入命令需要知道目标容器。项目的 `fault-injector` 不直接用服务名，而是用 Docker container name，例如：

- `aegis-user-a`
- `aegis-user-b`
- `aegis-order-a`
- `aegis-retry-user`

网络故障走的是：

```bash
docker exec <container> tc ...
```

CPU 故障走的是：

```bash
docker update --cpus <value> <container>
```

所以 container name 决定故障打到哪个实例上。这个点在实验里很容易出错。比如 Compose service 叫 `user-b`，但容器名叫 `aegis-user-b`。如果用错名字，要么命令失败，要么打到错误目标上。

我一般会先跑：

```bash
docker compose -f docker-compose.demo.yml -f docker-compose.experiments.yml ps
```

确认容器名，再注入故障。

### Q371【简单】packet loss、delay、jitter 分别会影响哪些 RPC 指标？

`delay` 最直接影响 RPC latency。p95、p99、EWMA latency 会升高，慢实例上的 inflight 也可能升高。如果 timeout 较短，还会出现 `DEADLINE_EXCEEDED`。

`jitter` 影响尾延迟。固定 200ms delay 会让请求整体变慢，但分布相对稳定。加入 50ms 或 150ms jitter 后，p99 会更明显，因为请求延迟开始波动。

`packet loss` 的表现更复杂。TCP 会重传，所以应用层不一定马上看到错误，但 p99 会变大，TCP retransmit 指标会上升。严重时可能出现连接失败、`UNAVAILABLE` 或 deadline 超时。

从 AegisMesh 的角度看：

- delay 和 jitter 主要推高 latency score；
- loss 会推高 latency，也会给 eBPF network_score 提供信号；
- 如果请求超时或失败，ErrorCount 和 TimeoutCount 也会进入 slow_score。

### Q372【简单】为什么需要专门的 always-unavailable retry service？

retry 实验要回答的问题是：有 retry budget 和没有 retry budget，下游压力差多少。

如果直接拿正常业务服务做实验，失败可能来自多种原因：慢调用、网络抖动、路由变化、服务端偶发错误。这样很难把重试放大单独拎出来分析。

所以实验环境里加了 `retry-user-service`。它本质上还是 demo-user 服务，但启动参数里把错误概率设成 1，让它稳定返回失败。`frontend-retry-unbudgeted` 和 `frontend-retry-budgeted` 都去调用这个服务，然后通过 metrics 统计原始请求数、重试次数和总下游调用次数。

这个设计很朴素，但实验口径干净。之前的结果里，无预算时 retry amplification 是 2.000x，有预算后降到 1.150x。这说明 retry budget 确实限制了失败场景下的额外流量。

### Q373【简单】故障注入如何帮助验证 slow_score？

slow_score 不是靠单个指标判断实例好坏，而是把 latency、error、timeout、inflight 和网络信号合成一个慢故障分数。故障注入正好可以分别刺激这些信号。

比如：

- 给 `aegis-user-b` 注入 200ms delay，可以观察它的 p95 / p99 和 relative latency score 是否上升。
- 给 `aegis-user-b` 做 CPU throttle，可以观察 latency 和 inflight 是否上升，slow_score 是否比静态阈值更稳。
- 注入 packet loss，可以看 eBPF TCP retransmit 信号是否进入 network_score。
- 同时给两个 user 实例注入延迟，可以验证 absolute SLO score 是否能发现“所有实例都慢”的场景。

如果没有故障注入，只靠随机压测，很难证明 slow_score 对不同故障类型都有效。实验必须有可控输入，slow_score 的变化才有解释空间。

### Q374【简单】如何确认故障确实被注入到了目标实例？

我会从四层确认。

第一，看命令是否执行成功。网络故障可以查：

```bash
docker exec aegis-user-b tc qdisc show dev eth0
```

如果看到 `netem delay` 或 `netem loss`，说明 qdisc 已经挂上去了。CPU throttle 可以用：

```bash
docker inspect aegis-user-b
docker stats aegis-user-b
```

看 CPU 限制和运行时 CPU 使用情况。

第二，看目标实例指标。比如只给 `aegis-user-b` 注入 delay，那么 `user-b` 的 p99、slow_score、route_weight 应该明显变化，`user-a` 不应该出现同样幅度的变化。

第三，看 trace。SDK trace log 里会记录 upstream。故障窗口内，慢实例上的请求延迟应该更高；如果它进入 PROBING，流量比例应该受 probe ratio 限制。

第四，实验结束后重置故障，再看指标是否回落。只看注入时的异常还不够，能恢复到接近 baseline，才说明实验环境没有残留污染。

## 深度

### Q375【深度】tc qdisc 注入延迟时会影响入方向还是出方向？这对实验解释有什么影响？

项目现在的命令是：

```bash
docker exec aegis-user-b \
  tc qdisc add dev eth0 root netem delay 200ms 50ms
```

这里挂的是 `eth0` 的 root qdisc。按 Linux tc 的语义，它主要影响这个网卡的出方向流量，也就是从目标容器发出去的包。

如果目标是服务端容器，比如 `aegis-user-b`，客户端发来的请求进入容器时不一定被这个 qdisc 延迟，但服务端返回响应时会经过容器的出方向，所以客户端看到的 RPC latency 仍然会上升。对 unary RPC 来说，这已经足够模拟“调用这个实例变慢”。

这个方向性会影响实验解释。它不是严格模拟“双向链路都慢”，更像是目标实例到调用方方向变慢。如果要模拟入方向故障，需要更复杂的 ingress qdisc、ifb 设备，或者在调用方容器的出方向注入故障。

所以我在报告里不会把它说成完整网络链路仿真，而会说它是基于 Docker 容器网卡 egress 的延迟注入，用来稳定放大 RPC 可见延迟。

### Q376【深度】在 Docker bridge 网络中做 host 侧 tc 注入有哪些可见性限制？

项目当前主要是在容器内部执行 `tc`，因为 `docker-compose.demo.yml` 给 demo 容器加了 `NET_ADMIN`。这样目标很明确：进入 `aegis-user-b`，改它的 `eth0`。

如果改成 host 侧 tc，问题会多一些。Docker bridge 网络里，每个容器对应 host 上一端 veth，另一端在容器里叫 `eth0`。host 侧看到的是一堆 veth 名字，不一定直观对应容器名。容器重建后 veth 名也会变。

还有方向问题。host veth 的出方向和容器 eth0 的出方向不是同一个视角。你以为给服务端出方向加了 delay，实际上可能影响的是发往服务端的流量。NAT、bridge 转发和本机回环也会让抓包结果更难解释。

对这个项目来说，容器内注入更适合教学和复现实验：目标清楚、脚本短、失败容易排查。代价是它依赖容器有 `NET_ADMIN`，也不等价于生产机器上的真实网络故障。

### Q377【深度】CPU throttle 为什么可能表现为 latency 增加而不是 error 增加？

CPU throttle 不会直接让服务返回错误。它只是限制容器能用多少 CPU。服务仍然能接连接、读请求、执行 handler，只是处理速度变慢。

在 gRPC 里，这种故障常见表现是：

- 请求排队时间变长；
- handler 执行时间变长；
- inflight 请求数上升；
- p95 / p99 latency 上升；
- 如果客户端 deadline 足够长，请求最后仍然成功。

只有当排队时间超过 deadline，或者服务端线程、goroutine、连接资源被拖到不可用，错误率才会明显上升。

这正是 fail-slow 的麻烦之处。传统健康检查可能只看进程是否存活、端口是否可连接、心跳是否正常。CPU 被限制时，这些检查都可能通过，但用户请求已经很慢。AegisMesh 用 telemetry 和 slow_score 来补这个盲区。

### Q378【深度】应用级 sleep 和网络 delay 在 eBPF 指标上有什么差异？

应用级 sleep 是业务代码主动等一段时间。TCP 连接本身没有问题，内核也不会因为 handler sleep 就产生 retransmit。你会看到 RPC latency 上升，但 eBPF TCP retransmit、connect error 这类网络指标不应该明显变化。

网络 delay 不一样。它发生在包发送路径上。RPC latency 也会上升，但这次慢来自网络路径，而不是业务 handler。如果是 packet loss，还可能看到 TCP retransmit 增加。

CPU throttle 又是第三种情况。它也会让 RPC latency 上升，但网络层不一定有异常。它更可能伴随进程调度变慢、请求排队和 inflight 升高。

所以 eBPF 的价值不只是“再加一个分数”。它能帮助区分慢在哪里：应用层慢、资源层慢、还是网络层慢。这个区分对定位故障比单纯看 p99 更有用。

### Q379【深度】如果故障注入影响了两个实例，relative scoring 会如何表现？

如果两个实例一起变慢，relative scoring 的效果会变差。

原因很简单。relative score 依赖同一个 service 内部的对比，比如 median 和 MAD。只有 `user-b` 慢、`user-a` 正常时，`user-b` 很容易被识别出来。但如果 `user-a` 和 `user-b` 都慢，服务内部的中位数也变慢了，大家看起来差不多，relative outlier 就不敏感。

这就是项目后来补 absolute SLO score 的原因。absolute SLO 不问“你比别人慢多少”，而是问“你有没有超过这个服务自己的 SLO”。比如 p95 超过设定阈值，slow_score 就会被拉高。

项目的 absolute SLO 实验也验证了这个点。关闭 absolute SLO 时，两实例同时变慢，max slow_score 只有 0.377，状态仍然是 HEALTHY。开启后 max slow_score 到 1.007，状态机出现 DEGRADED。这个结果说明：relative score 适合找单点慢实例，absolute SLO 负责兜住整体退化。

### Q380【深度】如何避免故障注入脚本污染后续实验结果？

最重要的是每组实验都要有清理动作。项目里有 `scripts/reset_faults.sh`，会删除目标网卡上的 qdisc，并清掉 CPU 限制：

```bash
go run ./cmd/fault-injector --kind reset --container "$TARGET" --device "$DEVICE" --execute || true
docker update --cpus 0 "$TARGET"
```

实验脚本也尽量把 reset 放进流程里。比如 absolute SLO 实验会对多个目标注入故障，脚本用 `trap` 保证退出时清理目标容器。

手动实验时我会遵守几个习惯：

- 实验前查一次 `tc qdisc show dev eth0`，确认没有旧 netem。
- 实验后跑 `make reset-faults TARGET=...`。
- CPU throttle 后确认 `docker update --cpus 0` 已执行。
- 每个实验写到独立 run 目录，避免 CSV 混在一起。
- 如果出现异常结果，重启 compose，再跑一组短 baseline。

故障注入最怕“上一组实验没清干净”。清理不是收尾动作，而是实验设计的一部分。

### Q381【深度】为什么 packet-loss eBPF comparison 需要确认 agent 正在运行？

packet-loss 对比要证明的是：加入 eBPF network_score 后，治理系统能利用网络层信号做更好的判断。

如果 eBPF agent 没跑，或者 endpoint mapping 没配对，Controller 收不到 TCP retransmit、connect error、connect latency 这些信号。此时所谓 `with ebpf` 实际上和 `without ebpf` 差不多，实验结论就站不住。

所以 packet-loss 实验前要确认几件事：

- eBPF agent 进程正在运行；
- ringbuf 能读到事件；
- agent 能把 remote address 映射到 service / instance；
- Controller 指标里能看到 network_score 或 TCP 事件相关变化；
- 实验脚本没有跳过等待 eBPF 数据的阶段。

在单机 Docker bridge 环境里，packet loss 的收益可能不会特别大。之前实验里 p99 从 27.539ms 降到 26.456ms，改善约 3.93%，这就是一个比较克制的结论。它能说明网络信号接入了闭环，但不应该被包装成大幅性能提升。

### Q382【深度】如果单机实验中容器调度竞争严重，结果如何归因？

单机实验最大的风险是干扰源太多。Controller、frontend、user、order、Prometheus、Grafana、eBPF agent 都在一台机器上跑时，CPU、内存、Docker bridge、磁盘 IO 都会共享。某个容器慢，可能是注入故障造成的，也可能是整机负载太高。

所以结果归因要谨慎。我会用几个方法降低误判：

- 每组实验前跑 no-fault baseline。
- 同一场景重复多次，看 median，而不是只挑一次结果。
- 对比同一时间窗口内目标实例和非目标实例的指标。
- 用较低但稳定的并发，避免把整机压满。
- 用 `docker stats` 或系统监控确认不是全局 CPU 打满。
- 实验报告里明确写“单机 Docker 环境模拟”，不写成多节点生产测试。

如果单机上两个实例都同时变慢，那更像整机资源竞争，不应该直接归因到 adaptive P2C 或 slow_score。这个时候 absolute SLO 实验仍然有价值，但解释口径要换成“整体退化检测”，而不是“单慢实例隔离”。

### Q383【深度】故障注入参数 DELAY、JITTER、LOSS、CPUS 如何选取才有说服力？

参数要满足两个条件：能明显超过背景噪声，又不能把服务直接打死。

比如单实例 delay 实验用 `DELAY=200ms`，这是为了制造肉眼可见的尾延迟差异。round-robin 会持续把一半请求打到慢实例，p99 会明显升高；adaptive P2C 会逐步避开慢实例。

状态迁移实验用过更重的参数，比如 `DELAY=800ms`、`JITTER=150ms`、`FAULT_DURATION=120s`。这是因为状态机有连续窗口、摘除时间和探测恢复逻辑，故障太轻或持续太短，不一定能走到 DEGRADED、EJECTED、PROBING。

packet loss 用 `LOSS=2` 这类较温和的值更合适。太低看不出网络信号，太高会变成 fail-stop 或连接不可用，反而偏离慢故障主题。

CPU throttle 用 `CPUS=0.25` 是为了让服务仍然能处理请求，但处理能力明显下降。如果设得过低，所有请求都超时，实验就会变成错误恢复测试。

一个好的参数不是越狠越好，而是能让系统进入要验证的状态。要测 p99 改善，就让 p99 拉开差距；要测状态机，就让 slow_score 稳定超过阈值；要测 retry budget，就让失败足够稳定。

### Q384【深度】如何设计实验避免 warm-up、connection reuse 和 cache 对结果的干扰？

我会把实验分成预热、故障、恢复三个阶段。

预热阶段让 gRPC 连接、resolver、balancer、Controller telemetry 都跑起来。否则刚开始的连接建立、服务发现和缓存初始化会污染 latency。

故障阶段只改一个变量。比如测 adaptive P2C，就固定并发、请求入口、目标服务，只在 `aegis-user-b` 上注入 delay。round-robin 和 adaptive P2C 使用不同 frontend 端口，但请求脚本和并发参数保持一致。

恢复阶段先 reset fault，再继续压测一段时间，观察 slow_score、route_weight 和 endpoint state 是否回落。没有恢复阶段，只能证明系统会降权，不能证明它能回到正常状态。

connection reuse 也要注意。gRPC 会复用 HTTP/2 长连接，但负载均衡 Pick 仍然发生在 RPC 级别。P2C 的选择不是每次新建 TCP 连接，而是每次 RPC 在可用 SubConn 里 pick。实验报告里要讲清这一点。

cache 对 demo 项目影响不大，因为业务逻辑很轻，但我仍然会丢掉最开始几秒或第一组窗口，防止启动抖动进入结果。真正做严谨评测时，还应该固定请求 mix、重复实验、用 median 或置信区间报告结果。

## 拓展

### Q385【拓展】Chaos engineering 和传统测试有什么区别？

传统测试通常验证“代码在预期输入下是否正确”。比如单元测试检查函数输出，集成测试检查服务调用是否成功。

Chaos engineering 更关心“系统在非理想运行环境下是否还能保持可接受的行为”。它会主动引入延迟、丢包、资源不足、依赖失败，然后观察系统是否能降级、限流、恢复，是否会把局部故障放大成全局故障。

AegisMesh 的故障注入更接近混沌实验里的小型实验台。它不是生产级混沌平台，但有基本要素：

- 有明确假设，比如“adaptive P2C 会降低慢实例 p99”；
- 有受控故障，比如只给 `aegis-user-b` 加 delay；
- 有观测指标，比如 p99、slow_score、state、retry amplification；
- 有恢复和清理脚本；
- 有结果检查脚本。

这比单纯“写个 demo 然后手动点一下”更接近系统实验。

### Q386【拓展】生产环境做故障注入需要哪些安全闸门？

生产故障注入首先要限制爆炸半径。不能随便对核心服务全量注入故障。常见做法是只选 canary 实例、小流量租户、低峰窗口或影子流量。

还需要几类安全闸门：

- 目标 allowlist：只有允许实验的服务和实例能被注入。
- 权限控制：谁能创建实验、谁能审批、谁能停止。
- 自动 abort condition：比如 p99 超过阈值、错误率超过阈值、成功率低于阈值就自动停止。
- 自动 rollback：停止实验后立即清理 tc、恢复 CPU quota、恢复路由策略。
- 审计日志：记录谁在什么时间对哪个实例做了什么注入。
- 观测前置检查：metrics、trace、日志不可用时不允许开始实验。

生产混沌实验最怕“实验本身变成事故”。所以它必须比普通测试多一层操作安全设计。

### Q387【拓展】LitmusChaos、Chaos Mesh、Gremlin 这类工具和项目内置 fault injector 有什么差别？

LitmusChaos、Chaos Mesh、Gremlin 是完整的混沌工程平台。它们通常支持 Kubernetes CRD、实验编排、权限、Dashboard、调度、回滚、安全策略和多种故障类型。

AegisMesh 的 fault injector 更轻。它只是一个 Go CLI，负责生成和执行 Docker / tc 命令。它的目标不是替代这些平台，而是服务项目自己的 benchmark：

- 在本地 Docker Compose 环境快速注入 delay、loss、CPU throttle；
- 让实验脚本能一键复现；
- 让 CI 至少能测试命令生成逻辑；
- 把故障输入和 slow_score、routing、retry budget 的输出关联起来。

如果项目上 Kubernetes，内置 injector 可以继续保留做本地实验；生产或大规模测试更适合接 Chaos Mesh 这类平台，然后把实验结果写回 AegisMesh 的观测和报告链路。

### Q388【拓展】如何对数据库、消息队列、DNS、证书过期进行 RPC 依赖故障注入？

这类故障要在依赖边界上做，不一定都适合用 `tc`。

数据库可以注入连接失败、慢查询、锁等待、主从切换、只读模式、连接池耗尽。RPC 层看到的可能是下游服务慢，也可能是业务错误率上升。

消息队列可以注入 broker 不可用、ack 延迟、消费堆积、重复消息、消息乱序。它和 RPC 最大的区别是异步语义更强，重试可能发生在 consumer、producer 或 broker 层。

DNS 可以注入解析超时、NXDOMAIN、错误 IP、TTL 过长或过短。对 RPC 系统来说，DNS 故障可能表现为新连接失败，但已有长连接还能继续工作。

证书过期可以通过 mTLS 证书失效、CA 轮换失败、服务端证书和域名不匹配来模拟。gRPC 里常见表现是连接建立失败，而不是业务 handler 返回错误。

如果把这些接入 AegisMesh，我会先把它们映射成统一的观测结果：connect error、UNAVAILABLE、DEADLINE_EXCEEDED、业务错误、latency 上升，再看 retry、breaker、state machine 如何处理。

### Q389【拓展】如何用 fault injection 验证 cascading failure 防护？

cascading failure 的核心是局部故障被重试、排队和资源争抢放大，最后拖垮上游和旁路服务。

实验可以这样设计：

1. 让 `retry-user-service` 稳定失败，制造下游错误。
2. 对比 `frontend-retry-unbudgeted` 和 `frontend-retry-budgeted`。
3. 记录原始请求数、重试次数、总下游调用次数、error rate、frontend p99。
4. 再叠加 circuit breaker 或 CPU throttle，观察 inflight 是否被限制住。

如果没有 retry budget，1000 个原始请求、最大 2 次尝试，很容易变成 2000 次下游调用。这个就是重试放大。加入 budget 后，之前实验里总调用中位数降到 1150，amplification 从 2.000x 降到 1.150x。

再看 adaptive routing。如果一个 endpoint 变慢，流量应该逐步转移到健康实例，而不是继续把请求打进慢实例排队。这个组合能说明系统不是只做了“失败后重试”，而是在限制放大、隔离慢点、控制恢复。

### Q390【拓展】如果要做持续混沌实验，如何设定 abort condition？

abort condition 要和用户影响、系统安全、实验目标绑定。常见条件包括：

- frontend p99 超过某个上限，比如超过 baseline 的 3 倍；
- error rate 超过 5% 或 10%；
- retry amplification 超过预算，比如大于 1.3x；
- 所有 endpoint 都进入 EJECTED；
- PROBING 流量超过配置上限；
- Controller 或 registry 不可用；
- CPU、内存、连接数超过机器安全阈值；
- 业务成功率低于预设值。

触发 abort 后，系统应该立刻做三件事：停止新故障注入、调用 reset 脚本、保留实验现场数据。不要只清理不留证据，否则事后没法判断是系统问题、注入参数问题，还是实验环境问题。

AegisMesh 现在的脚本更偏离线实验，abort 还没有做成完整平台能力。如果继续演进，我会把 abort condition 写进实验 spec，并让 runner 根据 Prometheus 查询结果自动中止。

### Q391【拓展】如何把故障注入结果纳入 CI/CD 质量门禁？

我会分两层做。

第一层放在普通 CI：跑不需要特权权限的测试。比如：

- `pkg/faultinjector` 的命令生成单元测试；
- Makefile 和实验脚本存在性检查；
- verifier、policy、registry、balancer 的 Go test；
- dry-run fault-injector，确认生成的命令符合预期。

这些测试稳定、快，不依赖 runner 有 `NET_ADMIN`。

第二层放在 nightly 或专门的 privileged runner：启动 Docker Compose，真正执行 tc、CPU throttle、压测和结果检查。这个阶段可以跑：

- slow instance delay；
- retry budget；
- recovery state；
- PROBING probe ratio；
- absolute SLO；
- packet loss + eBPF。

质量门禁不应该要求每次 PR 都跑完整混沌实验，成本太高，也容易因为机器噪声变 flaky。更合理的方式是：PR 保证逻辑不坏，nightly 监控治理效果是否退化。失败时保留 CSV、summary JSON 和日志。

### Q392【拓展】怎样证明故障注入覆盖了真实生产故障模型？

不能只说“我注入了 delay 和 loss，所以覆盖生产故障”。这个说法太粗。

更好的做法是列一张故障模型矩阵，把生产中常见问题映射到实验输入和观测信号：

- 单实例网络延迟：`tc delay`，观察 p99、EWMA、route_weight。
- 网络丢包：`tc loss`，观察 retransmit、network_score、p99。
- CPU 资源不足：`docker update --cpus`，观察 latency、inflight、slow_score。
- 应用慢调用：`slow-probability + slow-duration`，观察 handler 级慢请求。
- 应用错误：`error-probability` 或 `retry-user-service`，观察 retry amplification。
- 全实例变慢：对多个实例同时 delay，验证 absolute SLO。
- 恢复过程：reset fault 后观察 DEGRADED / EJECTED / PROBING / HEALTHY。

然后诚实写出没有覆盖的部分，比如 DNS 故障、数据库锁等待、多节点网络分区、证书过期、跨 AZ 故障、真实 Kubernetes CNI 行为。项目当前实验覆盖的是 RPC 慢故障治理的主干场景，不等于覆盖所有生产事故。

这种说法更稳。它既能说明实验设计有体系，也不会把本地 Docker 实验夸成生产级混沌平台。
