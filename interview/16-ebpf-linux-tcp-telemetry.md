# 16. eBPF、Linux TCP Telemetry 与内核观测

## 简单

### Q421【简单】项目 eBPF agent 当前采集哪些 TCP 信号？

当前 eBPF agent 采集三类 TCP 信号：

- `tcp_retransmit`：来自 `tcp_retransmit_skb`，表示 TCP 发生重传。
- `connect_error`：来自 `tcp_v4_connect` 的返回值，如果返回值非 0，就记一次连接失败。
- `connect_latency`：同样来自 `tcp_v4_connect`，在入口记录开始时间，在返回时计算连接耗时。

BPF 程序在 `agent/ebpf/bpf/tcp_metrics.bpf.c`，用户态 collector 在 `agent/ebpf/collector_linux.go`。

这里有个实现边界要讲清楚：agent 会解码并聚合 `connect_latency`，但当前 `api/proto/aegis/v1/telemetry.proto` 里还没有 `connect_latency` 字段，`NetworkSamplesToTelemetrySamples` 也没有把它上报给 Controller。Controller 当前实际参与 slow_score 的网络信号是 `tcp_retransmit` 和 `connect_error`。

所以面试里可以这样说：项目的 eBPF 路径已经采集 retransmit、connect error 和 connect latency，其中 retransmit/connect error 已经进入 Controller telemetry 和 slow_score，connect latency 还停留在 agent 侧，后续可以扩展到 proto。

### Q422【简单】tcp_retransmit_skb 能反映什么网络问题？

`tcp_retransmit_skb` 发生在 TCP 需要重传数据包时。它通常说明某个包没有按时被对端确认。

常见原因有：

- 网络丢包；
- 链路拥塞；
- 队列过长；
- 对端接收慢；
- 中间设备丢包或限速；
- Docker bridge、虚拟网卡或主机调度导致的局部网络异常。

它不是一个“根因指标”。看到 retransmit，只能说明 TCP 层出现了重传，不能直接断言一定是物理网络坏了。比如对端 CPU 很忙，ack 延迟，也可能间接导致重传。

在 AegisMesh 里，这个信号的用途是给 slow_score 增加网络侧证据。也就是说，如果一个 endpoint 的 p99 上升，同时 TCP retransmit 也上升，系统可以更有把握认为它不是单纯应用 handler 慢，而可能和网络路径有关。

### Q423【简单】tcp_v4_connect kprobe/kretprobe 分别采集什么？

项目对 `tcp_v4_connect` 挂了两个探针。

kprobe 挂在函数入口。它做的事是记录连接开始时的信息：

- 当前时间 `timestamp_ns`；
- 当前进程 pid；
- 源地址、源端口；
- 目标地址、目标端口；
- 进程名 `comm`。

这些信息会存进 `connect_starts` hash map，key 是 `pid_tgid`。

kretprobe 挂在函数返回。它读取返回值 `ret`，再从 `connect_starts` 找到入口时保存的数据，计算：

```text
connect_latency = return_time - start_time
```

如果 `ret != 0`，就认为这次 connect 失败，记一次 `connect_error`。

这个设计能把“一次 connect 的入口”和“同一次 connect 的返回”配对起来。没有这个配对，就只能知道 connect 结束了，但不知道它花了多久、连的是哪个远端。

### Q424【简单】ringbuf map 的作用是什么？

ringbuf 是 eBPF 程序把事件传给用户态 agent 的通道。

内核里的 BPF 程序不能直接调用 Go 代码，也不能做复杂 IO。它只能把事件写到 BPF map 里。项目里定义了：

```c
struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 1 << 20);
} events SEC(".maps");
```

每次出现 TCP retransmit 或 connect 返回事件，BPF 程序会：

```c
event = bpf_ringbuf_reserve(&events, sizeof(*event), 0);
...
bpf_ringbuf_submit(event, 0);
```

用户态 Go collector 用 `ringbuf.NewReader(eventsMap)` 读这些事件，再调用 `DecodeRawTCPEvent` 解码成 `TCPEvent`。

ringbuf 的好处是开销低，适合把小事件从内核送到用户态。代价是它不是无限队列。压力太大时 `bpf_ringbuf_reserve` 会失败，当前代码会直接丢掉这次事件。

### Q425【简单】connect_starts hash map 为什么需要保存开始时间？

因为 connect latency 不是单点事件能算出来的。

`tcp_v4_connect` 入口时可以拿到开始时间和目标地址，但这时函数还没执行完，不知道结果。返回时可以拿到返回值 `ret`，但返回探针里不方便再完整拿到入口参数。

所以项目用 `connect_starts` 做中间状态：

```c
key:   pid_tgid
value: connect_start { timestamp_ns, pid, addr, port, comm }
```

入口 probe 写入 start，返回 probe 读出 start，然后计算耗时：

```c
event->connect_latency_ns = now - start->timestamp_ns;
```

最后删除 map 里的记录。

这个模式很常见：kprobe 记录上下文，kretprobe 补返回值，两边合起来还原一次函数调用。

### Q426【简单】agent 为什么需要 root 或 capabilities？

因为它要做几件普通用户不能做的事：

- 加载 eBPF object；
- 创建 BPF map；
- 把 BPF 程序 attach 到 kernel kprobe/kretprobe；
- 读取内核事件；
- 在某些内核上还要调整 memlock limit。

不同内核发行和发行版策略不完全一样。新内核可能允许用 `CAP_BPF`、`CAP_PERFMON` 等能力拆分权限；老内核很多场景仍然需要 `CAP_SYS_ADMIN`。本项目 README 里给的运行方式是 `sudo go run ./cmd/agent ...`，这是本地实验最直接的方式。

生产部署时不建议简单粗暴地给所有权限。更好的方式是按内核发行给最小 capabilities，限制容器镜像、命名空间、可挂载路径和可访问的 Controller 地址。

### Q427【简单】为什么非 Linux host 返回 ErrUnsupportedPlatform？

eBPF agent 依赖 Linux 内核能力。Windows 和 macOS 上没有项目需要的 kprobe、BPF map、ringbuf 和 BTF 环境。

所以代码分了两份：

- `collector_linux.go`：真正加载 eBPF object，attach kprobe，读取 ringbuf。
- `collector_other.go`：非 Linux 下返回 `ErrUnsupportedPlatform`。

这样做有两个好处。

第一，项目可以在 Windows/macOS 上编译和跑大部分 Go 单元测试，不会因为没有 Linux eBPF 环境直接崩掉。

第二，错误语义清楚。用户在非 Linux host 上启动 agent，会得到“eBPF collector is only available on Linux”，而不是一堆底层系统调用失败。

### Q428【简单】endpoint-map 的作用是什么？

`endpoint-map` 把 eBPF 看到的 `ip:port` 映射成 AegisMesh 的服务实例。

命令示例：

```bash
sudo go run ./cmd/agent \
  --controller 127.0.0.1:9000 \
  --object agent/ebpf/bpf/tcp_metrics.bpf.o \
  --endpoint-map "10.0.0.2:7001=user-service/user-a,10.0.0.3:7002=user-service/user-b"
```

左边是网络事件里的 remote address，右边是：

```text
service/instance
```

agent 聚合事件时会查这个 map。如果 `10.0.0.2:7001` 发生 3 次 retransmit，它就知道这是 `user-service/user-a` 的网络信号。

如果没有映射，agent 只能保留 remote address。当前 Controller telemetry 又要求 sample 里有 service 和 instance id，所以 unmapped 事件很难进入有效 slow_score。也就是说，`endpoint-map` 不是装饰项，它决定 eBPF 信号能不能归属到正确 endpoint。

### Q429【简单】connect_error、connect_latency 如何转换成 Controller telemetry？

当前转换路径是：

```text
BPF raw event
-> TCPEvent
-> Aggregator NetworkSample
-> EndpointStatsSample
-> Controller TelemetryService
-> fault.EndpointSample
-> slow_score
```

`connect_error` 的转换已经打通。BPF 返回 probe 发现 `ret != 0`，用户态解码后把 `TCPEvent.ConnectErrors` 设为 1，聚合后写入：

```go
EndpointStatsSample.ConnectError
```

Controller 再转成：

```go
fault.EndpointSample.ConnectError
```

slow_score 里把 `TCPRetransmit + ConnectError` 合在一起作为 network events。

`connect_latency` 的情况不一样。它会进入 `TCPEvent.ConnectLatency`，也会被 Aggregator 累加到 `NetworkSample.ConnectLatency`。但当前 proto 没有 `connect_latency` 字段，`NetworkSamplesToTelemetrySamples` 没有上报它。因此 Controller 现在不会用 connect latency 算 slow_score。

如果后续要补完整，需要在 `EndpointStatsSample` 加字段，比如 `connect_latency_seconds` 或 `connect_latency_p95_seconds`，再在 Controller 和 score calculator 里接入。

### Q430【简单】eBPF 路径在项目中是核心路径还是增强信号？

它是增强信号，不是核心路径。

AegisMesh 的核心治理闭环是：

```text
SDK telemetry -> Controller slow_score -> endpoint state -> resolver/balancer routing
```

即使没有 eBPF，系统仍然可以用 RPC latency、error、timeout、inflight 做慢故障检测和 adaptive P2C。

eBPF 的作用是补网络层视角。RPC latency 只能告诉你“请求慢了”，但不能直接告诉你慢在应用、CPU、网络，还是连接建立。TCP retransmit 和 connect error 能给 Controller 一个额外证据。

所以我不会把它说成系统不可缺少的主链路。更准确的说法是：AegisMesh 先用 SDK telemetry 完成治理闭环，再用 eBPF TCP telemetry 增强网络慢故障识别。

## 深度

### Q431【深度】kprobe 和 tracepoint 各有什么稳定性取舍？

kprobe 的优点是灵活。只要内核里有这个函数，就可以挂上去。项目现在用的就是：

- `kprobe/tcp_retransmit_skb`
- `kprobe/tcp_v4_connect`
- `kretprobe/tcp_v4_connect`

它能拿到函数参数，适合做 connect entry/return 这种配对。

缺点是稳定性差一些。内核函数名、参数、调用路径可能随修订变化。某些发行版也可能禁用或限制 kprobe。

tracepoint 更稳定。它是内核暴露出来的事件接口，字段相对固定，修订兼容性通常比 kprobe 好。比如 TCP retransmit 有相关 tracepoint 可以用，生产系统更偏向 tracepoint。

但 tracepoint 的缺点是只能拿它暴露的字段，灵活性不如 kprobe。你想观察某个没有 tracepoint 的函数，还是得用 kprobe。

项目当前选择 kprobe，是因为实现直接、实验清晰。生产化时，我会优先把能换成 tracepoint 的地方换掉，保留 kprobe 处理 tracepoint 覆盖不到的细节。

### Q432【深度】BPF CO-RE 依赖 vmlinux.h 解决了什么问题？

不同 Linux 内核发行里，很多结构体字段布局会变。比如 `struct sock` 的字段偏移，不同内核构建可能不一样。

如果 BPF 程序直接按固定 offset 读字段，在一台机器上能跑，换一台内核发行就可能读错。

CO-RE 的思路是 Compile Once, Run Everywhere。BPF 程序通过 BTF 类型信息知道目标内核里结构体字段的位置，再在加载时做重定位。项目的 BPF 代码使用：

```c
#include "vmlinux.h"
#include <bpf/bpf_core_read.h>

BPF_CORE_READ(sk, __sk_common.skc_daddr)
```

`vmlinux.h` 由：

```bash
bpftool btf dump file /sys/kernel/btf/vmlinux format c > vmlinux.h
```

生成。它包含当前内核的类型定义。

CO-RE 主要解决结构体字段偏移问题，不保证所有内核函数都存在。比如某个内核没有 `tcp_v4_connect` 这个符号，或者 kprobe 权限被限制，程序仍然会 attach 失败。

### Q433【深度】为什么 connect_start 用 pid_tgid 作为 key？这种 key 会有哪些并发边界？

`pid_tgid` 是当前进程和线程的组合值。项目用它作为 key，是为了把同一线程里 `tcp_v4_connect` 的入口和返回配对起来。

入口时：

```c
pid_tgid = bpf_get_current_pid_tgid();
bpf_map_update_elem(&connect_starts, &pid_tgid, &start, BPF_ANY);
```

返回时：

```c
pid_tgid = bpf_get_current_pid_tgid();
start = bpf_map_lookup_elem(&connect_starts, &pid_tgid);
```

这个选择很自然，因为函数入口和返回一般在同一个线程上下文里执行。多线程并发 connect 时，不同线程有不同 tid，key 不会互相覆盖。

边界也存在。

如果同一个线程发生嵌套 connect，后一次入口可能覆盖前一次 start。正常 TCP connect 路径里这种情况很少，但理论上要考虑。

如果入口记录了 start，但返回 probe 没执行或 agent 中途卸载，map 里可能留下旧记录。当前 map 有 `max_entries=16384`，长期异常情况下需要清理策略。

还有 PID/TID 复用问题。正常情况下一次 connect 生命周期很短，复用风险很低。但如果 start 没删掉，后续线程复用相同 id，就可能误配。

更稳的 key 可以把 `pid_tgid` 和 `struct sock *` 指针组合起来，减少覆盖风险。

### Q434【深度】tcp retransmit 事件如何归属到具体 service instance？

BPF 事件里有 TCP 四元组。项目当前主要用 remote address 做归属：

```text
remote ip:port -> service/instance
```

用户通过 `--endpoint-map` 提供映射：

```text
10.0.0.2:7001=user-service/user-a
```

Aggregator 收到 `TCPEvent.RemoteAddr="10.0.0.2:7001"` 后，就能把 retransmit 累加到 `user-service/user-a`。

这个归属方式在“agent 运行在客户端侧，remote 是下游服务地址”时比较好理解。比如 frontend 连接 user-service，重传事件的 daddr/dport 指向 user-service，映射关系就成立。

如果 agent 运行在服务端侧，情况会反过来。服务端 socket 发送响应时，remote 可能是客户端地址，不是服务端自己的地址。那就不能简单把 remote address 当成 service instance。

所以 eBPF 归属要看部署位置。单机实验可以手工映射；生产里最好从 Controller registry、Kubernetes EndpointSlice 或连接方向信息里自动推断，不能长期靠手填。

### Q435【深度】在 NAT、Docker bridge、service mesh sidecar 场景下，ip:port 映射会遇到什么问题？

NAT 会改地址或端口。eBPF 看到的地址可能是 NAT 前，也可能是 NAT 后，取决于 hook 点。你在 registry 里登记的是 `user-b:7002`，但内核事件里看到的可能是 `172.18.0.5:7002`。

Docker bridge 也有类似问题。容器名、容器 IP、宿主机端口映射是几套标识。Compose 服务叫 `user-b`，容器名叫 `aegis-user-b`，容器 IP 可能每次重建都变。手工 endpoint-map 很容易过期。

sidecar mesh 会再加一层复杂度。应用连接的 remote 可能是本机 sidecar，比如 `127.0.0.1:15001`，真正的下游地址在 sidecar 内部转发后才出现。agent 如果只在应用进程视角看连接，可能把所有流量都归到 sidecar，而不是业务服务。

解决办法一般有几种：

- 从 Controller registry 自动拉取 address 到 instance 的映射；
- 在 Kubernetes 里读 Pod IP、EndpointSlice、Service、namespace、labels；
- 结合 cgroup id、network namespace、pid 到 pod 的映射；
- 对 sidecar 场景接入代理的 access log 或 xDS metadata；
- 在 trace 里保留 SDK 看到的 upstream，和 eBPF 看到的 socket 地址做关联。

当前项目还停留在手工 `endpoint-map`，适合本地实验。生产化要把 mapping 自动化，否则 eBPF 信号的可信度会受影响。

### Q436【深度】ringbuf reserve 失败时丢事件，对 slow_score 有什么影响？

当前 BPF 代码里，如果：

```c
bpf_ringbuf_reserve(...)
```

返回 nil，就直接 `return 0`。也就是说，这个事件会被丢掉。

这会带来 false negative。网络确实有 retransmit 或 connect error，但 agent 没把事件送到用户态，Controller 看到的 network events 比实际少，slow_score 的 network component 会偏低。

通常不会带来 false positive，因为丢事件只会少报，不会凭空多报。

对 AegisMesh 来说影响相对可控。默认 `RetransmitWeight=0.10`，网络信号只是 slow_score 的一部分。latency、error、inflight 还在 SDK telemetry 里，所以 eBPF 丢少量事件不会让系统完全失明。

但如果网络信号是某次实验的主证据，丢事件就会影响结论。生产化时应该给 agent 加 dropped event 计数，比如：

- ringbuf reserve 失败计数；
- Go channel 满导致丢事件计数；
- decode 失败计数；
- 上报失败计数。

没有这些自监控指标，eBPF 实验只能做谨慎结论。

### Q437【深度】eBPF 采集 connect_latency 与应用层 RPC latency 的语义差别是什么？

connect latency 是 TCP 连接建立耗时。它只覆盖连接建立环节，主要反映 SYN/SYN-ACK/ACK 这段路径的耗时和失败情况。

RPC latency 是一次业务调用从发出到收到响应的耗时。它包含更多东西：

- 连接复用；
- HTTP/2 stream 排队；
- gRPC 编解码；
- 服务端排队；
- handler 执行；
- 网络往返；
- 客户端 deadline 和 retry。

gRPC 通常会复用长连接。连接建好后，后面几千个 RPC 可能都不会触发新的 `tcp_v4_connect`。这时 connect latency 对 steady-state p99 的解释力就很有限。

所以 connect latency 更适合发现连接建立慢、服务不可达、网络路径异常、冷启动连接失败。应用层 RPC latency 更适合衡量用户请求体验。

如果一个服务 handler sleep 200ms，RPC latency 会明显上升，但 connect latency 可能完全没变化。这也是为什么 AegisMesh 不能只靠 eBPF，必须保留 SDK 侧 telemetry。

### Q438【深度】如果 packet loss 实验结果提升很小，你如何解释 eBPF 价值？

我会直接承认结果小，不会硬说它提升很大。

当前单机实验里，packet loss 对比结果是：

```text
no_ebpf_network_score p99 = 27.539 ms
ebpf_network_score    p99 = 26.456 ms
改善约 3.93%
```

这个提升很小。原因也合理：实验跑在单机 Docker bridge 上，网络路径短，gRPC 长连接会隐藏一部分 connect 信号，packet loss 参数也没有打到非常严重。再加上 eBPF 网络权重默认只有 0.10，它本来就不是主导项。

eBPF 的价值不只看这一个 p99 数字。它更像诊断信号：

- 能证明网络事件进入了 Controller telemetry；
- 能把网络异常和应用慢调用区分开；
- 在多节点、真实丢包、跨机房链路里会更有解释价值；
- 能帮助 slow_score 在网络故障场景下更早获得证据。

所以报告里应该写克制结论：当前单机 packet-loss 实验只显示小幅改善，说明链路打通；更强的性能主张需要多节点或更真实的网络故障环境。

### Q439【深度】网络层信号权重过高会造成哪些误判？

如果 network signal 权重太高，系统可能把短暂网络噪声误判成 endpoint 慢故障。

几个典型风险：

- 某个客户端本机网络抖动，导致它看到很多 retransmit，但服务端其实健康。
- Docker bridge 或 sidecar 转发导致 remote address 映射错，网络事件归到了错误实例。
- 低 QPS 场景下，一两个 retransmit 被放大成很高的 network rate。
- 连接建立失败来自客户端资源耗尽，却被算到下游 endpoint。
- 多个服务共享同一条网络路径，一个链路问题让多个 endpoint 同时被降权。

这会导致 healthy endpoint 被错误降权甚至 EJECTED。

项目里默认 `RetransmitWeight=0.10`，就是比较保守的选择。网络信号可以影响 slow_score，但不能压过 latency、error、inflight 这些 RPC 侧信号。

生产里还可以加几层保护：

- 设置最小样本数；
- 对 network events 做时间平滑；
- 区分客户端维度和 endpoint 维度；
- 只在 latency 同时异常时提高网络信号权重；
- 对低 QPS 服务降低网络信号敏感度。

### Q440【深度】如何避免 eBPF agent 对系统性能造成显著开销？

核心原则是：内核里少做事，用户态再聚合。

当前项目的 BPF 程序比较轻：

- 只挂三个 hook；
- 事件结构固定；
- 不做字符串拼接；
- 不做复杂循环；
- 只读 TCP tuple、pid、comm 和时间；
- 用 ringbuf 传事件；
- ringbuf reserve 失败时直接丢弃，不阻塞内核路径。

用户态也做了缓冲。collector 把事件放进 channel，Reporter 每隔 5 秒聚合上报一次，不是每个事件都打一次 gRPC。

生产里还可以继续控制开销：

- 只在需要的节点或 namespace 开 agent；
- 按 cgroup、端口或进程名过滤；
- 给 ringbuf 和 map 设置合理大小；
- 对高频事件采样；
- 暴露 agent 自身 CPU、内存、丢事件指标；
- 避免采集 stack trace 这种重操作；
- 使用 tracepoint 替代不稳定或高成本 kprobe。

eBPF 很强，但不能把它当成免费观测。越靠近内核热路径，越要控制代码复杂度。

## 拓展

### Q441【拓展】kprobe、kretprobe、uprobes、tracepoints、XDP、tc BPF 分别适合什么场景？

这几类 hook 位置不同。

`kprobe` 挂内核函数入口，适合观察内核函数参数。项目用它看 `tcp_retransmit_skb` 和 `tcp_v4_connect`。

`kretprobe` 挂内核函数返回，适合拿返回值。项目用它看 `tcp_v4_connect` 的 ret，并计算 connect latency。

`uprobe` 挂用户态函数，适合观察 libc、OpenSSL、JVM、Go runtime 或业务二进制里的函数。比如想看 TLS 明文或某个库函数调用，可以考虑 uprobe，但维护成本更高。

`tracepoint` 是内核预定义事件，稳定性通常比 kprobe 好，适合生产观测。缺点是只能拿 tracepoint 暴露的字段。

`XDP` 工作在网卡收包很早的位置，适合高性能包过滤、DDoS 防护、L4 负载均衡。它离应用很远，不适合直接看 RPC method。

`tc BPF` 挂在流量入方向或出方向 qdisc，适合包分类、限速、重定向和网络策略。Cilium 这类系统会大量使用 tc / XDP 位置做 datapath。

AegisMesh 现在用 kprobe/kretprobe，是因为目标是 TCP telemetry，不是包转发或流量拦截。

### Q442【拓展】Cilium/Hubble 如何利用 eBPF 做网络可观测性？

Cilium 用 eBPF 接管或增强 Kubernetes 网络 datapath。它能在内核里观察包、连接、策略判定、service 负载均衡和网络身份。

Hubble 是 Cilium 的观测层。它把 eBPF 采集到的 flow 信息整理出来，能看到：

- 哪个 pod 调哪个 pod；
- L3/L4 连接；
- DNS 信息；
- HTTP/gRPC 层信息，取决于配置和可见性；
- network policy allow/drop；
- 跨 namespace、service、workload 的流量图。

AegisMesh 的 eBPF agent 要小很多。它不是 datapath，也不做网络策略，只采 TCP retransmit/connect 这类信号，然后把它们送进 slow_score。

可以把两者关系理解成：Cilium/Hubble 是完整的 Kubernetes 网络可观测平台；AegisMesh 当前 eBPF 是 RPC 治理系统里的网络健康补充信号。

### Q443【拓展】eBPF verifier 会检查什么？为什么 BPF 程序受限？

eBPF 程序会跑在内核里。如果它死循环、乱读内存、写坏内核对象，后果比普通用户态程序严重得多。

所以加载前，内核 eBPF verifier 会做静态检查，常见内容包括：

- 程序是否能终止；
- 循环是否有界；
- 指针访问是否合法；
- 读写内存是否越界；
- stack 使用是否在限制内；
- map key/value 类型是否匹配；
- helper 函数调用是否合法；
- 只有初始化过的数据才能读；
- 不允许任意调用内核函数。

这些限制会让 BPF 程序写起来比普通 C 程序麻烦，但换来的是内核安全。项目里的 BPF 程序结构很简单，固定大小 event、固定 map、少量字段读取，也是为了降低 verifier 拒绝和运行开销。

### Q444【拓展】如果要支持 IPv6，BPF struct 和 user-space parser 需要怎么改？

当前实现基本是 IPv4 路径。它 hook 的是 `tcp_v4_connect`，event 里只有：

```c
__u32 saddr_v4;
__u32 daddr_v4;
```

用户态 `endpointString` 也只把 32 位地址转成 IPv4。

支持 IPv6 要改几层。

BPF 侧要增加 IPv6 地址字段，比如：

```c
__u8 saddr_v6[16];
__u8 daddr_v6[16];
```

还要 hook `tcp_v6_connect`，并从 `sock_common` 里读取 v6 地址。event 里要保留 `family`，让用户态知道这是 AF_INET 还是 AF_INET6。

Go 解码侧要把 raw struct 对齐改掉，`endpointString` 要支持 IPv6，并输出标准格式：

```text
[2001:db8::1]:7001
```

`endpoint-map` parser 也要支持这种带方括号的地址。测试要补 IPv4、IPv6、空地址、非法端口几类。

Controller 侧最好不要假设 `host:port` 里只有一个冒号，所有解析都用 `net.SplitHostPort`。

### Q445【拓展】如何采集 TCP RTT、重传、拥塞窗口等更丰富信号？

可以从 `struct tcp_sock` 里读更多字段。比如：

- `srtt_us`：平滑 RTT；
- `mdev_us`：RTT 抖动；
- `snd_cwnd`：拥塞窗口；
- `ssthresh`：慢启动阈值；
- `retrans_out`：当前未恢复的重传包；
- `lost_out`：估计丢失包；
- `rto`：重传超时；
- `bytes_acked`、`bytes_received`：吞吐相关信息。

实现方式有几种。

一种是在已有 retransmit 或 connect 事件里，通过 `tcp_sk(sk)` 读取 tcp_sock 字段，一起发到 ringbuf。

另一种是挂 tracepoint，比如 TCP probe 类事件，拿内核已经暴露的 RTT/cwnd 信息。

还可以在用户态通过 `getsockopt(TCP_INFO)` 获取连接状态，但这需要拿到 socket fd，对通用 agent 不一定方便。

工程上要小心两个问题。第一，字段越多，内核发行兼容越难。第二，事件越频繁，开销越高。更合理的做法是先采 retransmit/connect error，再按需要增加 RTT 和 cwnd，同时给每个字段明确用途。

### Q446【拓展】TLS 加密后，eBPF 还能看到哪些网络层信息，看不到哪些应用层信息？

TLS 加密不会影响内核看到 TCP 层信息。

eBPF 仍然能看到：

- 源 IP、目标 IP；
- 源端口、目标端口；
- connect 成功或失败；
- connect latency；
- retransmit；
- RTT、cwnd 这类 TCP 状态；
- 进程 pid、comm；
- 字节数和连接生命周期。

但它看不到加密后的应用层内容：

- gRPC method；
- HTTP path；
- request body；
- response status 的业务含义；
- protobuf payload；
- 用户 ID、订单 ID 等业务字段。

除非使用 uprobe 挂到 TLS 库或应用 runtime，在加密前后读取明文，但这会带来很高的侵入性和安全风险。

所以 AegisMesh 的设计是分层的：SDK 负责 RPC 语义，比如 method、status、attempt、upstream；eBPF 负责 TCP 语义，比如 retransmit 和 connect error。两边合起来，比单靠任意一边更完整。

### Q447【拓展】内核发行差异会如何影响 eBPF 程序兼容性？

影响主要在几处。

第一，内核函数是否存在。项目 hook `tcp_v4_connect` 和 `tcp_retransmit_skb`。如果某个内核发行函数名变了、被内联了、不可 kprobe，attach 就会失败。

第二，结构体字段布局不同。CO-RE 和 BTF 能缓解这个问题，但前提是目标机器有可用 BTF，字段也还存在。

第三，权限模型不同。新内核把 BPF 权限拆得更细，老内核可能还需要 `CAP_SYS_ADMIN`。容器环境里还会受 seccomp、AppArmor、SELinux 限制。

第四，helper 和 map 类型支持不同。ringbuf 是比较新的能力，太老的内核可能不支持。

第五，发行版补丁也会影响行为。即使修订号相同，云厂商内核可能改过配置。

所以 eBPF 程序要有兼容性测试。项目现在建议在目标 Linux host 上用：

```bash
make -C agent/ebpf/bpf
```

生成 `vmlinux.h` 并编译 object。真正生产化时，还需要列出最低内核发行、BTF 要求、权限要求，并在 CI 或预发节点上做 attach smoke test。

### Q448【拓展】如果要在 Kubernetes 中部署 agent，DaemonSet、权限、命名空间怎么设计？

我会把 eBPF agent 做成 DaemonSet，每个节点跑一个 pod。因为 TCP 事件发生在节点内核里，节点级 agent 更自然。

DaemonSet 需要的配置大概包括：

- `hostPID: true`，方便把 pid/cgroup 映射到容器或 pod；
- 挂载 `/sys/kernel/btf`、`/sys/fs/bpf`，必要时挂载 debugfs/tracefs；
- securityContext 配置 `privileged: true`，或者按内核发行配置 `CAP_BPF`、`CAP_PERFMON`、`CAP_NET_ADMIN`、`CAP_SYS_ADMIN`；
- nodeSelector / tolerations，控制在哪些节点运行；
- resource requests/limits，避免 agent 抢业务资源。

权限上，Kubernetes RBAC 至少要能读：

- Pods；
- Nodes；
- Services；
- EndpointSlices；
- Namespaces。

这些对象用来建立映射：

```text
pod IP:container port -> namespace/service/pod/instance
```

命名空间上，不建议让所有租户直接看到所有 eBPF 数据。agent 可以运行在 `observability` 或 `kube-system`，但上报给 Controller 时要带 namespace、service、pod labels。查询侧再按租户做隔离。

如果集群里有 sidecar，还要考虑应用容器和 sidecar 容器的流量归属。可以结合 cgroup id、pod sandbox、container id 和 SDK trace 里的 upstream 来做关联。

项目当前没有做到 Kubernetes DaemonSet 级部署。现环节是本地 Linux agent + endpoint-map。扩展到 K8s 的关键不是再多采几个 TCP 字段，而是把地址、pod、service、tenant 的映射做准确。
