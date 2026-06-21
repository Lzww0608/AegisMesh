# 19. Protobuf、API 设计与兼容性

## 简单

### Q505【简单】项目有哪些 protobuf contract？

项目里主要有四组 protobuf contract。

第一组是 `aegis.v1.RegistryService`，定义在 `api/proto/aegis/v1/registry.proto`。它负责服务注册和服务发现，包括 `RegisterInstance`、`Heartbeat`、`ListInstances`。SDK resolver 和 demo service 都会依赖它。

第二组是 `aegis.v1.TelemetryService`，定义在 `api/proto/aegis/v1/telemetry.proto`。它负责把 SDK 侧观测数据上报给 Controller，也支持查询 Controller 当前维护的 endpoint health。核心接口是 `ReportEndpointStats` 和 `ListEndpointHealth`。

第三组是 `aegis.v1.PolicyService`，定义在 `api/proto/aegis/v1/policy.proto`。它负责动态策略下发，包括 `GetPolicy` 和 `WatchPolicy`。策略里有 routing policy、retry policy、outlier detection、circuit breaker、method policy。

第四组是 demo 业务 proto，定义在 `api/proto/demo/shop/v1/shop.proto`。里面有 `UserService.GetUser` 和 `OrderService.CreateOrder`，用于构造一个最小微服务业务链路，方便验证 AegisMesh 的治理能力。

面试里可以这样概括：AegisMesh 的 proto 分成控制面 API 和 demo 业务 API。控制面 API 是项目的主体，demo API 是实验载体。

### Q506【简单】ServiceInstance、EndpointStatsSample、PolicySnapshot 分别表达什么？

`ServiceInstance` 表达一个可被发现的服务实例。它包含 `id`、`service`、`address`、`status`、`labels`、`last_seen_unix_millis` 和 `slow_score`。SDK resolver 拿到它后，会把 address 转成 gRPC 地址，同时把 instance_id、status、slow_score 放进地址属性，供 balancer 使用。

`EndpointStatsSample` 表达一个 SDK 在某个窗口内观察到的 endpoint 统计。字段里有 source、service、instance_id、endpoint_address、method、request_count、error_count、timeout_count、retry_count、inflight、latency EWMA、p95、TCP retransmit、connect error、capacity、窗口开始和结束时间。Controller 用这些样本计算 slow_score，并推动 endpoint 状态机。

`PolicySnapshot` 表达某个 service 当前生效的治理策略快照。它包含 service、revision、routing_policy、retry、outlier_detection、circuit_breaker 和 methods。methods 是按 gRPC 方法名索引的 method-level policy，可以覆盖重试、超时、幂等性这类配置。

三个 message 对应三条主线：服务在哪里、运行得怎么样、应该按什么策略治理。

### Q507【简单】为什么 protobuf 字段要考虑向后兼容？

因为 Controller 和 SDK 不一定同时升级。实际部署时可能先升级 Controller，部分业务进程里的 SDK 还是旧代码；也可能某些服务先升级 SDK，Controller 仍然是旧实现。

protobuf 的兼容性规则允许“旧代码忽略新字段，新代码读不到旧字段时用默认值”。这让滚动升级变得可行。但前提是不能随便改字段号、不能把字段类型改成不兼容类型、不能复用删除过的字段号。

比如 `EndpointStatsSample` 后续要加 `tcp_rtt_ms`，最稳的做法是增加一个新字段号。旧 Controller 会忽略它；新 Controller 如果没收到，就按 0 或 unknown 处理。这样系统可以慢慢升级，不需要所有进程同时停机。

### Q508【简单】ListInstances 和 ReportEndpointStats 的请求响应语义是什么？

`ListInstances` 是服务发现查询。SDK resolver 发 `ListInstancesRequest{service: "user-service"}`，Controller 返回这个 service 下仍有效的 `ServiceInstance` 列表。返回结果里会带地址和健康状态，SDK 再决定哪些 endpoint 可以参与路由。

`ReportEndpointStats` 是 telemetry 上报。SDK reporter 把一个时间窗口内的 `EndpointStatsSample` 批量发给 Controller。Controller 接收样本后更新 slow_score 和 endpoint 状态机，然后在 response 里返回一组 `EndpointHealth`。这意味着 telemetry 上报不只是“写日志”，它还参与控制闭环。

简单说，`ListInstances` 是 SDK 问“现在有哪些后端可用”，`ReportEndpointStats` 是 SDK 告诉 Controller“我看到这些后端表现如何”。

### Q509【简单】WatchPolicy 为什么适合 streaming RPC？

策略更新是低频但需要及时感知的事件。用普通 unary polling 也能做，比如 SDK 每 3 秒调一次 `GetPolicy`，但这会带来无意义请求；配置刚变更时，客户端还要等下一轮轮询。

`WatchPolicy` 用 server streaming 更自然。SDK 建立一条流，Controller 先发当前 snapshot，后面策略文件变化时再推新的 snapshot。没有变化时连接保持着，不需要不停请求。

项目里的实现仍然比较朴素：Controller 定时检查策略文件是否变化，如果 revision 变了就往 stream 里发送。它不是 xDS 那种完整的 ACK/NACK 协议，但对当前动态策略需求已经够用。

### Q510【简单】protobuf 生成代码后为什么需要提交或重新生成？

Go 代码不能直接调用 `.proto` 文件，必须先生成对应的 `.pb.go` 和 `_grpc.pb.go`。这些生成代码里包含 message struct、序列化逻辑、service client、service server interface。

如果 `.proto` 改了但生成代码没更新，编译时可能找不到字段或接口；更糟的是，本地编译用旧代码，别人重新生成后行为不同。项目里 proto 是 API contract，改 contract 后应该同步重新生成，并把生成文件一起提交。

另一种做法是在 CI 或 build 脚本里固定 protoc 和插件修订，每次构建自动生成。但这样要保证所有环境的生成工具一致。对这个项目来说，把生成代码提交进仓库更方便复现实验。

### Q511【简单】枚举状态和字符串状态各有什么优缺点？

字符串状态的优点是灵活。现在 `ServiceInstance.status` 和 `EndpointHealth.state` 都是 string，`HEALTHY`、`DEGRADED`、`EJECTED`、`PROBING`、`DEAD` 都可以直接写进去。新增状态时，不需要改 proto，也不会让旧 SDK 编译失败。

缺点是没有类型约束。拼错一个 `DEGRDAED`，protobuf 层不会发现，只能靠业务校验。不同语言 SDK 也可能大小写不一致。

enum 的优点是类型清楚，生成代码会给出固定取值，IDE 和编译器都能帮忙。缺点是演进要更谨慎。新增 enum value 通常兼容，但旧语言运行时怎么处理未知 enum，要看具体实现；如果从 string 迁移到 enum，还会牵涉字段兼容和灰度。

当前项目用 string 是为了快速迭代。生产化时我会新增 enum 字段，而不是直接改原字段，比如保留 `state = 4`，新增 `state_code = 8`，灰度迁移后再逐步减少对 string 的依赖。

### Q512【简单】method policy map 的 key 应该用完整方法名还是短方法名？

应该用完整 gRPC 方法名，比如：

```text
/demo.shop.v1.UserService/GetUser
```

短方法名 `GetUser` 不够安全。不同 service 可能都有 `Get`、`Create`、`List` 这类方法名，只用短名很容易撞。完整方法名包含 package、service 和 method，能精确定位一次 RPC。

AegisMesh 的 retry interceptor、telemetry interceptor 拿到的 `fullMethod` 本来就是这种格式，所以 method policy map 用完整方法名最省事，也最不容易出错。配置文件里虽然稍微长一点，但面试官问幂等性和重试策略时，这个设计更站得住。

### Q513【简单】timestamp 用 unix millis、seconds 还是 google.protobuf.Timestamp 有什么取舍？

unix seconds 简单，但粒度太粗。RPC telemetry、窗口统计和恢复曲线通常要看秒内变化，只用 seconds 不够。

unix millis 是项目当前的选择，比如 `last_seen_unix_millis`、`window_start_unix_millis`、`window_end_unix_millis`。它简单，跨语言好处理，写 CSV 和 JSON 也方便。缺点是语义没有 `Timestamp` 明确，调用方要知道单位是毫秒。

`google.protobuf.Timestamp` 表达力更强，带 seconds 和 nanos，也有标准工具支持。缺点是 message 更重一点，写实验 CSV 或命令行输出时不如 int64 直接。

AegisMesh 当前偏实验和本地可复现，所以用 unix millis 合理。生产 API 如果追求语义严谨，可以在新字段里使用 `google.protobuf.Timestamp`，但要避免直接替换旧字段。

### Q514【简单】API revision primary 代表什么兼容性承诺？

`aegis.v1` 包名里的数字后缀表示这组 API 已经有一个相对稳定的 contract。稳定不等于不能改，而是改的时候要遵守兼容性规则。

对 primary 来说，可以做的改动包括：新增 optional 语义字段、新增 RPC、新增 message、新增 enum value。旧客户端看不懂的新字段会忽略，新客户端读不到旧服务端没发的字段时使用默认值。

不应该做的改动包括：复用字段号、改字段类型、删除字段后让字段号重新给别的含义、改变已有 RPC 的基本语义。比如 `ReportEndpointStats` 原本表示批量上报，如果突然改成只接受单个 sample，那就是破坏兼容。

如果未来确实要大改，比如把状态全部 enum 化、把 service discovery 改成 xDS 风格，应该开一个新的 API 包，让旧包和新包并行一段时间。

## 深度

### Q515【深度】如果要新增 slow_score 组成项，proto 如何扩展才兼容旧 SDK？

最稳的做法是在 `EndpointStatsSample` 里新增字段号，不改旧字段。比如要加入 TCP RTT，可以加：

```proto
double tcp_rtt_seconds = 18;
```

旧 SDK 不会上报这个字段，新 Controller 读到默认值 0。Controller 不能简单把 0 当成真实 RTT，它应该区分“没上报”和“上报值为 0”。proto3 标量字段默认值会丢失 presence 信息，所以如果这个区别很重要，可以用 wrapper type，或者新增一个 `bool has_tcp_rtt = 19`。另一种方式是把网络信号放进 repeated metric 结构，里面有 name、value、unit，但那会牺牲类型清晰度。

旧 Controller 收到新 SDK 发来的新字段，会直接忽略。这样新 SDK 可以先上线，不会打坏旧 Controller。

策略层也要配合。新增 score 组成项后，`OutlierDetectionPolicy` 可能需要新增权重字段，比如 `tcp_rtt_weight`。同样用新字段号，默认值保持旧行为。不能让新字段缺省时突然改变旧集群的 slow_score 结果。

### Q516【深度】如果 ServiceInstance.Status 从 string 改成 enum，会有什么迁移成本？

直接把 `string status = 4` 改成 enum 是不兼容改动。字段号 4 原来按 string 编码，新代码按 varint enum 解码， wire type 都不一样。旧数据、旧 SDK、旧 Controller 之间会出现解析问题。

更安全的迁移方式是新增字段：

```proto
EndpointState status_code = 8;
```

然后走四步：

1. Controller 同时写 `status` 和 `status_code`。
2. 新 SDK 优先读 `status_code`，没有时 fallback 到 `status`。
3. 所有 SDK 升级后，Controller 可以逐步减少对字符串状态的依赖。
4. 原 `status` 字段不能复用，最多标记 deprecated，下一代 secondary 再移除。

迁移成本主要在多语言 SDK 和实验脚本。CSV、Prometheus label、trace JSONL 里很多地方可能都在读字符串状态。enum 更适合内部逻辑，字符串更适合页面显示和兼容旧工具。实际工程里我会让 wire API 同时保留一段时间。

### Q517【深度】PolicySnapshot 中 revision 是 int64，如何保证跨 Controller 单调？

当前文件策略实现里，revision 通常来自策略文件的 `modTime.UnixNano()`。单 Controller、单文件情况下，它基本够用：文件变了，modTime 变，SDK 收到更大的 revision。

跨 Controller 就没这么简单。多副本 Controller 如果各自读本地文件，文件系统时间可能不一致；如果不同机器时钟漂移，modTime 也不能保证全局单调。甚至同一秒内多次写文件，在某些文件系统上 modTime 精度也可能不够。

生产化有几种做法：

1. 把策略存到 etcd、Consul 或数据库，用后端的 revision 作为快照序号。etcd revision 天然单调。
2. 用中心配置服务生成 revision，Controller 只是缓存和转发。
3. revision 改成 `{epoch, counter}` 或 `{config_id, revision}`，避免单纯依赖本机时间。
4. SDK 收到 revision 小于当前值的 snapshot 时拒绝应用，防止回滚污染。

所以 `int64 revision` 这个字段本身没问题，问题在 revision 的来源。当前实现适合单 Controller 实验，分布式部署要换成集中式 revision。

### Q518【深度】Telemetry sample 中 source 由客户端提供，是否可信？

严格说，不可信。`EndpointStatsSample.source` 是 SDK 填的，Controller 默认相信它。实验环境里没问题，因为所有服务都是我们自己的 demo service。但如果放到真实环境，恶意客户端可以伪造 source，给别的服务上报假 telemetry，影响 slow_score 和路由状态。

生产化至少要做几件事：

1. Controller 端通过 mTLS 识别客户端身份，不只看 proto 里的 source 字符串。
2. source 和证书身份、service account 或 workload identity 绑定。比如证书说你是 frontend，你就不能上报自己是 payment。
3. 对 telemetry 做权限校验。哪些调用方可以上报哪些 destination，要有规则。
4. 对异常 telemetry 做限流和 sanity check，比如 request_count 不合理、窗口时间太旧、endpoint 不存在。

当前 source 字段适合做实验分组和观测来源标记，但不能当安全身份。面试时要主动把这个边界说清楚。

### Q519【深度】如果客户端和 Controller proto 不一致，最坏会发生什么？

如果只是新增字段，通常没事。旧端忽略新字段，新端读不到字段时用默认值。

麻烦出现在破坏性变更。比如字段号复用、字段类型改变、RPC 语义改变。最坏情况不是“请求失败”，而是“请求成功但语义错了”。例如字段号 13 原来是 `tcp_retransmit`，后面错误复用成别的含义，旧 Controller 可能把新 SDK 的数据当网络重传数，slow_score 直接被带偏。

还有一种风险是默认值误判。新 SDK 期待 Controller 返回 `PolicySnapshot.revision` 和 retry policy，但旧 Controller 不支持 PolicyService。SDK 如果没有 fallback，就可能启动失败；如果 fallback 太宽，又可能在没有策略的情况下启用不该启用的重试。

所以项目里要做三层保护：

1. protobuf 层遵守兼容规则。
2. SDK 对 `Unimplemented`、`NotFound`、空 policy 有默认策略。
3. CI 加 API 兼容性检查，防止字段号和类型被误改。

### Q520【深度】如何设计错误码，让 SDK 能区分策略缺失和 Controller 故障？

gRPC 状态码要表达清楚“没有配置”和“控制面坏了”的区别。

策略缺失可以用 `NotFound`。比如 SDK 请求 `GetPolicy(service=user-service)`，Controller 没有这个 service 的策略，返回 `codes.NotFound`。SDK 可以选择使用默认策略，或者按配置决定是否启动失败。

接口未实现用 `Unimplemented`。这适合老 Controller 不支持 PolicyService 的情况。SDK 看到它，应该降级到内置默认策略，而不是一直重试。

Controller 暂时不可达、连接断开、进程重启，一般会表现为 `Unavailable`。SDK 应该继续使用最近一次成功拿到的策略，并按 backoff 重连。

策略文件格式错误或字段不合法，用 `InvalidArgument` 或在 Controller reload 时保留旧快照。我的倾向是不要把坏策略推给 SDK。Controller 应该拒绝加载，并暴露错误 metric。

内部异常用 `Internal`，超时用 `DeadlineExceeded`。SDK 不应该把这些错误和“没有策略”混在一起，否则会出现控制面短暂故障就清空策略的情况。

### Q521【深度】WatchPolicy stream 需要 heartbeat 或 keepalive 吗？

需要，尤其是长连接在生产网络里跑的时候。

当前 `WatchPolicy` 在策略变化时发送 snapshot，没有变化时 stream 可能长时间没有业务消息。很多中间层，比如 LB、NAT、防火墙，会清理长时间空闲连接。客户端如果只等 `Recv()`，可能要很久才发现连接已经半死。

有两种做法。

一种是 gRPC keepalive，用 HTTP/2 ping 保活。这不需要改 proto，但要配置客户端和服务端 keepalive 参数。

另一种是在业务协议里发 heartbeat，比如定期发送带相同 revision 的 `PolicySnapshot`，或者新增一个 `WatchPolicyResponse`：

```proto
message WatchPolicyResponse {
  oneof event {
    PolicySnapshot snapshot = 1;
    WatchHeartbeat heartbeat = 2;
  }
}
```

当前 proto 直接 streaming `PolicySnapshot`，没有 oneof response，所以做业务 heartbeat 会比较别扭。短期可以靠 gRPC keepalive 和客户端 backoff 重连；长期如果要做完整 watch 协议，我会改成 response wrapper，支持 snapshot、heartbeat、error detail、ack revision。

### Q522【深度】ReportEndpointStats 批量上报时，部分 sample invalid 应该整体失败还是部分接受？

要看 invalid 的类型。

如果整个请求结构错了，比如没有 samples，或者 source 身份不合法，可以整体失败。因为这类错误说明请求本身不可信。

如果只是批量里的某几条 sample 有问题，比如某个 latency 是负数、窗口时间反了、endpoint address 为空，我更倾向于部分接受。否则一个坏 sample 会让整个窗口的其他正常 telemetry 都丢掉，控制面状态更容易断档。

但当前 `ReportEndpointStatsResponse` 只有 `repeated EndpointHealth endpoints`，没有 per-sample error。它不方便告诉 SDK 哪些 sample 被拒绝。要做部分接受，可以扩展 response：

```proto
message ReportEndpointStatsResponse {
  repeated EndpointHealth endpoints = 1;
  repeated SampleError errors = 2;
}
```

旧 SDK 会忽略 `errors`，新 SDK 可以记录日志或 metrics。服务端也要有 dropped sample counter。对治理系统来说，静默丢弃很危险，至少要在 Controller 侧可观测。

### Q523【深度】服务注册接口是否应该支持 Deregister？没有 Deregister 会有什么影响？

应该支持，但不是必须依赖它。

当前 registry 有 `RegisterInstance`、`Heartbeat`、`ListInstances`，没有 `Deregister`。实例退出后，只要心跳停止，TTL 到期后 `SweepExpired` 会把它清掉。这个设计对异常退出很友好，因为进程崩了也不可能发 deregister。

缺点是正常发布或缩容时会有延迟。实例已经停止接流量，但注册中心要等 TTL 过期，resolver 也要等下一轮 refresh，期间客户端可能还会拿到旧地址。虽然 RPC 会失败并触发重试或健康降级，但用户请求还是会受影响。

更好的做法是加：

```proto
rpc DeregisterInstance(DeregisterInstanceRequest) returns (DeregisterInstanceResponse);
```

实例优雅退出时先标记 draining 或直接 deregister，再停止服务。即使 Deregister 丢了，TTL 仍然兜底。也就是说，Deregister 是加速正常退出，TTL 是处理异常退出，两者不冲突。

### Q524【深度】如果 endpoint address 是 IPv6 或 Unix Domain Socket，proto 字段是否够用？

当前 `address` 和 `endpoint_address` 都是 string，所以表达能力基本够用。IPv4 可以写 `127.0.0.1:7001`，IPv6 可以写 `[::1]:7001`，Unix Domain Socket 可以写 `unix:///tmp/aegis.sock` 或者约定好的 UDS 格式。

问题不在 proto 字段能不能装下，而在解析规范。SDK resolver、fault injector、eBPF endpoint mapping、trace verifier 都可能默认 address 是 `ip:port`。如果直接塞 IPv6，简单的 `strings.Split(address, ":")` 会出错。UDS 更明显，它没有远端 IP 和端口，eBPF TCP telemetry 也对不上。

生产化需要明确 address grammar，比如：

```proto
message EndpointAddress {
  string network = 1; // tcp, tcp6, unix
  string host = 2;
  int32 port = 3;
  string path = 4;
}
```

但这会改变现有 API。折中做法是继续保留 string address，新增结构化字段，让新 SDK 优先读结构化地址，旧 SDK 仍然读 string。

## 拓展

### Q525【拓展】protobuf reserved field number 和 reserved name 为什么重要？

字段删除后，字段号不能随便复用。protobuf 的 wire format 主要靠字段号识别数据，不靠字段名。如果以前 `13` 是 `tcp_retransmit`，后来删掉又把 `13` 给了 `cpu_usage`，旧客户端发来的数据就可能被新服务端解释成完全不同的含义。

`reserved` 就是为了防止这种事故：

```proto
message EndpointStatsSample {
  reserved 20;
  reserved "old_field_name";
}
```

reserved field number 防止编号复用，reserved name 防止同名字段被重新引入。对多人协作的 proto 很有用，因为它把“这个位置不能再用”写进 IDL，protoc 会帮你拦住。

AegisMesh 现在还处在快速迭代环节，字段没有大规模删除。后续如果废弃字段，应该立刻加 reserved，而不是只靠文档提醒。

### Q526【拓展】gRPC API 的兼容性测试应该怎么做？

我会做四类测试。

第一类是 proto breaking change 检查。可以用 Buf 或类似工具，把当前 proto 和 main 分支上的 proto 做比较，禁止字段号复用、类型改变、RPC 删除。这个适合放 CI。

第二类是旧客户端对新 Controller。用旧 proto 生成的 SDK 调新 Controller，跑 Register、Heartbeat、ListInstances、ReportEndpointStats、GetPolicy，确认旧客户端还能工作。

第三类是新客户端对旧 Controller。尤其要测 PolicyService 不存在或字段缺失时，SDK 是否能 fallback 到默认策略，而不是启动失败。

第四类是语义测试。比如新增 policy 字段后，默认值必须保持旧行为；新增 telemetry 字段后，旧 sample 不应该让 slow_score 计算异常。

这里的重点是：protobuf 解析成功不代表 API 兼容。真正要测的是“新旧组件混部时，治理语义是否仍然安全”。

### Q527【拓展】如何生成 OpenAPI/HTTP gateway 以便非 gRPC 客户端调用 Controller？

可以用 grpc-gateway。做法是在 proto 上加 `google.api.http` annotation，比如：

```proto
rpc ListInstances(ListInstancesRequest) returns (ListInstancesResponse) {
  option (google.api.http) = {
    get: "/v1/registry/{service}/instances"
  };
}
```

然后用 `protoc-gen-grpc-gateway` 生成 HTTP reverse proxy，用 `protoc-gen-openapiv2` 或类似工具生成 OpenAPI 文档。这样非 gRPC 客户端可以用 HTTP JSON 调 Controller，运维脚本和 dashboard 也更方便。

但我不会把 SDK 的热路径改成 HTTP gateway。SDK 到 Controller 仍然用 gRPC 更合适，尤其是 `WatchPolicy` 这种 streaming。HTTP gateway 适合人和工具访问，比如查看实例、查看健康状态、手动触发 policy reload。

还要注意鉴权。HTTP API 一旦暴露给浏览器或公网，认证、授权、审计就不能省。

### Q528【拓展】IDL-first 和 code-first API 设计有什么差异？

IDL-first 是先写 `.proto`，把 API contract 定下来，再生成 Go、Java、Python 等语言代码。AegisMesh 采用的就是这种方式。它适合跨语言、跨进程系统，因为所有人都围着同一份 contract 工作。

code-first 是先写服务端代码，再从代码注解或类型里生成 API 描述。它开发快，适合单语言团队或 HTTP CRUD 服务。但一旦要支持多语言 SDK，code-first 很容易把某种语言的类型习惯泄漏到协议里。

治理系统更适合 IDL-first。原因很简单：Controller、Go SDK、未来 Java/Python SDK、实验工具、verifier 都要理解同一套 API。先把 proto 设计清楚，可以减少后面多语言实现时的歧义。

缺点是 IDL-first 前期要想得更细，字段命名、编号、默认值都要谨慎。改错的成本比改内部代码高。

### Q529【拓展】如果策略需要表达复杂匹配条件，protobuf oneof、map、repeated message 如何设计？

简单的 method policy 用 `map<string, MethodPolicy>` 就够了，因为 key 是完整方法名，查找快，配置也直观。

复杂匹配就不适合继续塞 map。比如要按 caller、tenant、region、header、method 前缀、流量比例匹配，就应该用 repeated rule：

```proto
message RoutePolicy {
  repeated PolicyRule rules = 1;
}

message PolicyRule {
  Match match = 1;
  PolicyAction action = 2;
  int32 priority = 3;
}
```

匹配条件内部可以用 `oneof` 表达不同类型：

```proto
message MatchCondition {
  oneof condition {
    MethodMatch method = 1;
    HeaderMatch header = 2;
    CallerMatch caller = 3;
  }
}
```

`map` 适合精确查找，不适合有优先级和顺序的规则。`repeated message` 适合保留顺序和表达 priority。`oneof` 适合互斥结构，避免一个条件里同时填多个含义冲突的字段。

如果做成平台级策略，还要定义冲突合并规则：method 规则优先于 service 默认，caller/tenant 规则优先于全局默认，高 priority 先匹配，最后 fallback 到默认策略。

### Q530【拓展】如何支持多租户身份、RBAC、审计字段的 API 演进？

可以分步骤做，不要一次把所有字段塞进现有 message。

第一步，在请求里增加身份上下文。比如：

```proto
message RequestContext {
  string tenant = 1;
  string caller = 2;
  string request_id = 3;
}
```

然后把它加到 `GetPolicyRequest`、`WatchPolicyRequest`、`ListInstancesRequest` 这类控制面请求里。旧客户端不传时按 default tenant 处理。

第二步，策略和实例增加作用域。`PolicySnapshot` 可以加 tenant、namespace、environment。`ServiceInstance` labels 里短期可以放 tenant，长期最好有结构化字段。

第三步，加审计信息。策略变更不是 SDK 请求直接完成的，但如果未来有 Policy Admin API，就需要 `created_by`、`updated_by`、`reason`、`change_id`、`created_at`、`updated_at`。

RBAC 不建议只靠 proto 字段做。proto 只能承载身份和资源信息，真正授权应该在 Controller 里结合 mTLS、token、service account、角色策略来判断。审计日志也应该落到独立存储，不能只放在 response 里。

### Q531【拓展】如果 Controller 对外暴露公网 API，认证和授权应如何加入 proto？

认证优先放在传输层和 metadata，不建议把密码、token 这类敏感信息直接放进业务 message。

如果是服务到服务通信，我会用 mTLS。客户端证书标识 workload identity，Controller 根据证书里的 SPIFFE ID、service account 或 SAN 判断它是谁。proto 里的 source、service 只能作为业务声明，不能当认证依据。

如果是公网 HTTP/gRPC API，可以用 Authorization metadata，比如 Bearer token、JWT 或 API key。gRPC 里这些会走 metadata：

```text
authorization: Bearer ...
```

proto 里可以增加 `RequestContext`，承载 tenant、request_id、reason 这类非敏感上下文。授权逻辑在 Controller interceptor 里做，先认证身份，再检查这个身份是否能读某个 service 的 registry、上报 telemetry、watch policy。

审计也要放在服务端 interceptor 或 admin handler 里统一记录。不要让客户端自己传 `operator` 就相信它。公网 API 还要配 rate limit、请求大小限制和 TLS。

### Q532【拓展】如何做 API deprecation 和 rolling upgrade？

先说规则：不要直接删字段，不要复用字段号，不要突然改变旧字段语义。

一次安全的 rolling upgrade 通常分四步。

第一步，新增字段或新增 RPC。比如新增 `state_code` enum，同时保留原来的 `state` string。Controller 可以先同时写两个字段。

第二步，升级 SDK，让 SDK 优先读新字段，读不到就 fallback 到旧字段。这个环节新旧 Controller 都能跑。

第三步，等大部分 SDK 升级完成后，Controller 内部逻辑开始依赖新字段，但仍然继续填旧字段，给未升级客户端留时间。

第四步，在文档和 proto 注释里把旧字段标记 deprecated。字段号保留，不复用。真正删除可以等到 v2。

RPC 也是类似。比如要从 `GetPolicy` 迁到更完整的 `FetchConfig`，先新增 RPC，新 SDK 双读或切换，Controller 同时支持两套接口。等客户端全部升级后，再把旧 RPC 标 deprecated。

回滚也要考虑。如果新 Controller 发了新字段，旧 SDK 应该忽略；如果新 SDK 连到旧 Controller，应该能用默认策略继续工作。这就是 API 演进里最重要的检验标准：新旧组件混部期间，系统可以降级，但不能乱解释数据。
