# RPC Fundamentals

本文对应《AegisMesh 技术面试问题库》里“RPC 基础概念（30 题）”这一大类。按要求，这一类问题统一放在同一个 md 文件里；本文件是新文件，所以题号从 1 开始。后续如果继续补 RPC 基础概念，直接沿着当前题号往下接。

写法上按面试口述来组织：先给一段可以直接说的回答，再把容易被追问的边界拆开。内容写作参考官方文档和一手资料，同时结合仓库里已经整理好的 AegisMesh gRPC 设计与面试笔记；文件内不单列参考资料。

## 1. RPC 和 HTTP API 的区别是什么？RPC 一定比 REST 更高效吗？

可以先这样答：

RPC 和 HTTP API 的核心区别在接口抽象。RPC 把远端能力抽象成 service 和 method，调用方像调用本地方法一样调用 `UserService.GetUser(request)`；HTTP API，尤其是按 REST 风格设计的 API，更强调 resource、URI、HTTP method、status code、cache、content negotiation 这些 HTTP 语义，典型形式是 `GET /users/{id}` 或 `POST /orders`。二进制、文本、高性能、低性能这些说法都只是实现层面的结果，不能拿来定义二者。

所以 RPC 更像“动作接口”，HTTP API 更像“资源接口”。RPC 的接口名通常直接表达业务动作，比如 `CreateOrder`、`ReportEndpointStats`、`WatchPolicy`。HTTP API 则更依赖资源建模和方法语义：`GET` 获取资源表示，`POST` 让目标资源按自己的语义处理请求体，`PUT` 替换目标资源状态，`DELETE` 删除资源映射。IETF RFC 9110 里 HTTP 的这些方法、资源、缓存语义是标准化的，很多代理、浏览器、CDN、网关都天然理解它们。

gRPC 这类现代 RPC 框架通常会用 `.proto` 定义服务和消息，用 Protocol Buffers 做默认序列化，用 HTTP/2 做传输。这样带来的好处很直接：接口强类型，客户端和服务端都从 IDL 生成代码；请求体紧凑，解析成本通常比 JSON 低；HTTP/2 可以在一条连接上多路复用；框架还能统一提供 deadline、metadata、status code、streaming、interceptor、resolver、balancer 等能力。对内部服务调用来说，这些能力很实用。

但“RPC 一定比 REST 更高效”这个说法不严谨。效率要看你讨论的是哪一种效率：网络字节数、CPU 解析成本、连接复用、缓存命中、调试效率、跨团队协作成本，还是系统整体吞吐。一个 protobuf unary RPC 的字节数和解析成本可能比 JSON HTTP 低；但一个可缓存的 `GET` 请求如果被 CDN 或网关命中，后端一次都不用访问，这时 HTTP API 的整体效率反而更高。

还有一些场景里，HTTP API 的通用性比 RPC 的紧凑性更重要。公开 API 面向浏览器、第三方开发者、命令行调试和跨组织集成时，HTTP+JSON 更容易被理解和排查，curl、浏览器 DevTools、API Gateway、WAF、CDN、缓存和日志系统支持也更成熟。RPC 的二进制消息和生成代码在内部服务里很舒服，但对外部调用方可能提高接入门槛。

RPC 的高效也不是免费来的。强 schema 要求团队维护 IDL 兼容性，字段编号不能乱改，生成代码要跟随语言版本升级，客户端和服务端要协调发布。gRPC 的 HTTP/2 长连接和多路复用降低建连开销，但也会带来连接级负载均衡、流控、单连接热点和调试复杂度。REST/HTTP API 虽然常见 JSON 开销更大，但中间件可见性强，缓存和代理行为更容易利用。

面试里可以把边界说清楚：RPC 更适合内部高频服务调用、强类型契约、跨语言 SDK、低延迟治理、streaming、统一超时和重试控制；HTTP API 更适合公开接口、资源型 API、浏览器生态、缓存友好读请求、低接入门槛和人工调试。RPC 和 REST 没有谁天然更高级，主要是服务的约束不同。

放到 AegisMesh 上，项目选择 gRPC 是有具体原因的。AegisMesh 需要在客户端 SDK 里拿到 gRPC method、deadline、status code、metadata、attempt、upstream，并接入 resolver、balancer、interceptor、retry budget、telemetry 和 trace。用 gRPC 可以把这些治理点挂在框架已有扩展点上。如果用普通 HTTP JSON，也能做，但要自己统一不同 HTTP client 的行为，成本更高。

所以这题的结论是：RPC 和 HTTP API 的区别主要在接口模型和治理能力，不在“谁天然更快”。RPC 常常在内部服务调用里更紧凑、更强类型、更适合方法级治理；HTTP API 常常在资源语义、缓存、生态兼容和外部可调试性上更好。真正的选择应该看调用场景、数据格式、缓存可能性、客户端生态、治理需求和团队维护成本。

如果面试官继续深挖，可以按这条路线走：先区分方法抽象和资源抽象；再说明 gRPC/protobuf/HTTP/2 为什么常让内部 RPC 更紧凑；接着反驳“RPC 一定更高效”，举 GET 缓存、CDN、公开 API 调试这些反例；最后落到项目，说明 AegisMesh 选择 gRPC 是为了方法级治理和客户端扩展点，而不是因为 REST 一定慢。

## 2. RPC 框架通常由哪些模块组成？

可以先这样答：

一个完整的 RPC 框架，不只是“把请求发到远端”的网络库。它通常要把接口定义、代码生成、序列化、连接管理、服务发现、负载均衡、超时、重试、鉴权、拦截器、可观测性和错误模型都包起来。因为远程调用的问题不在“能不能发出去”，而在跨机器以后，接口契约、网络失败、版本兼容、流量治理和排查成本都会变成框架责任。

第一块是 IDL 和代码生成。IDL 定义 service、method、request、response、字段类型和字段编号。gRPC 默认用 Protocol Buffers 作为 IDL 和消息结构描述方式；Thrift 也有自己的 IDL，并通过代码生成器生成多语言代码。有了 IDL，框架可以生成客户端 stub 和服务端 skeleton，业务代码不需要手写编解码和方法分发。

第二块是客户端 stub。调用方看到的是一个本地对象或本地函数，比如 `client.GetUser(ctx, req)`。stub 负责把本地方法调用转换成远程请求：读取方法名，检查参数类型，序列化请求，把 metadata、deadline、认证信息放进去，再交给底层 transport。stub 的价值是隐藏网络细节，但它也容易让人忘记远程调用不是本地调用，延迟、失败和重试都不能按本地函数理解。

第三块是服务端分发。服务端要注册 service 实现，接收请求后解析出完整方法名，反序列化请求，执行对应 handler，再把响应、错误码、trailing metadata 编回去。成熟框架还会处理并发模型、连接生命周期、流式调用、消息大小限制、压缩和优雅关闭。这里的重点是：业务 handler 只写业务逻辑，框架负责协议边界。

第四块是序列化和反序列化。protobuf、Thrift、Avro、JSON 都可以做这个角色，只是取舍不同。protobuf 紧凑、跨语言、强类型、生成代码成熟，但二进制消息离开 schema 不容易直接解释；JSON 可读、通用、调试方便，但字段类型和兼容性更依赖约定。RPC 框架会把序列化和协议绑定起来，避免每个团队自己约定字段格式。

第五块是传输协议和连接管理。现代 gRPC 常用 HTTP/2，所以有 channel、connection、stream、flow control、header、trailer、多路复用这些概念。连接管理要处理 TLS、keepalive、连接池、连接状态、backoff、max message size、压缩和关闭。这个模块直接影响延迟和负载均衡效果：如果所有请求复用一条连接，而框架只在连接建立时选后端，流量就可能被粘住。

第六块是服务发现和负载均衡。客户端需要把逻辑服务名解析成后端地址列表，这通常由 resolver 或注册中心完成。拿到地址后，balancer 决定每次 RPC 选哪个连接或后端。策略可以是 pick_first、round_robin、P2C、least request、权重、 locality、健康状态或自定义成本函数。AegisMesh 的自定义 resolver 和 adaptive P2C balancer 就属于这一层。

第七块是可靠性治理。远程调用一定会失败，所以框架要提供 deadline、timeout、cancellation、retry、hedging、circuit breaker、rate limit、backpressure、wait-for-ready 等机制。这里不能只看功能列表，还要看语义是否安全。比如重试要区分幂等和非幂等方法，deadline 要能传播到下游，取消不代表服务端已经回滚，breaker 要避免把局部慢故障扩散成全局重试风暴。

第八块是拦截器或 middleware。拦截器适合处理横切逻辑，比如鉴权、日志、metrics、trace、metadata 注入、租户识别、限流、故障注入、灰度标记。它的位置很关键。以 AegisMesh 为例，retry interceptor 和 telemetry interceptor 的顺序会影响指标看到的是逻辑请求还是每次 attempt。框架要让这些横切逻辑可组合，但不能让顺序变成隐蔽的事故源。

第九块是错误模型。HTTP 有状态码，gRPC 有自己的 status code，比如 `UNAVAILABLE`、`DEADLINE_EXCEEDED`、`INVALID_ARGUMENT`。框架要把连接失败、协议错误、服务端业务错误、deadline 超时、取消、限流、认证失败区分清楚。否则调用方只能看到一个泛化的 error，就很难判断能不能重试、要不要降级、该不该报警。

第十块是可观测性和运维能力。RPC 框架通常要暴露请求数、延迟分布、错误码、重试次数、in-flight、连接状态、消息大小、服务名、方法名、peer、deadline、trace id 等信息。还可能提供 health checking、reflection、admin endpoint、debug 日志、channel state 查看。没有这些，RPC 框架越“透明”，故障时越难排查。

放到 AegisMesh 上，可以把这些模块对应到项目里：`.proto` 文件定义 Registry、Telemetry、Policy 和 demo shop 服务；生成的 `*_pb.go`、`*_grpc.pb.go` 提供类型和 stub；`sdk/go/aegisgrpc/resolver.go` 做服务发现；`adaptive_balancer.go` 做 picker；`retry.go` 做重试；`interceptor.go` 和 `trace.go` 做 telemetry 和 trace；Controller 侧的 registry、policy、telemetry service 提供服务端实现。这个拆法比笼统说“RPC 有客户端和服务端”更接近真实工程。

所以这题的结论是：RPC 框架通常由 IDL/代码生成、客户端 stub、服务端 skeleton、序列化、传输协议、连接管理、服务发现、负载均衡、可靠性治理、拦截器、错误模型和可观测性组成。面试时最好按一次调用路径来讲这些模块，而不是背模块名。

如果面试官继续深挖，可以按这条路线走：先讲 IDL 生成 stub/skeleton；再讲请求如何被序列化并通过连接发送；接着讲 resolver/balancer 怎么选后端；再讲 deadline、retry、breaker、metadata、interceptor；最后用 AegisMesh 的 resolver、adaptive balancer、retry interceptor 和 telemetry 做落地例子。

## 3. 一次 RPC 调用从客户端发起到服务端返回，中间经历了哪些步骤？

可以先这样答：

一次 RPC 调用可以拆成两条线：一条是业务看见的“调用一个方法”，另一条是框架内部真正做的“查地址、选连接、编码、传输、解码、执行、回包、记录结果”。面试里不要只说“客户端序列化，服务端反序列化”。那太粗了。真正容易出问题的点在 deadline、metadata、连接选择、重试、服务端拦截器、状态码和调用完成后的反馈。

第一步，业务代码调用客户端 stub。比如 `client.GetUser(ctx, req)`。这个 stub 来自 IDL 生成代码，方法签名里已经约束了请求和响应类型。调用时通常会带一个 context，里面可能有 deadline、trace id、租户信息、认证信息或取消信号。对调用方来说像本地方法，但从这一刻开始，它已经进入远程调用语义。

第二步，客户端拦截器开始工作。拦截器可能注入 trace metadata、认证 token、租户标识、灰度标签，也可能做日志、metrics、限流、重试和故障注入。顺序很重要：重试拦截器在外层时，内层 telemetry 可能记录每一次 attempt；telemetry 在外层时，看到的可能是整个逻辑请求。AegisMesh 里就利用这个顺序记录每次 attempt 的 latency、status、upstream 和 trace。

第三步，框架根据目标服务做 name resolution。调用方通常不会直接写死每个后端 IP，而是写逻辑服务名。resolver 把逻辑目标转换成地址列表，并可能附带服务配置、实例状态、权重或自定义属性。AegisMesh 的 `aegis://controller/service` 目标会让 resolver 去 Controller 查询实例列表，并把 `instance_id`、`status`、`slow_score` 等属性放到地址上。

第四步，负载均衡器选择后端连接。gRPC 的 balancer 会维护一批 SubConn 或类似连接抽象，picker 在每次 RPC 前选择一个可用后端。不同策略差异很大：`pick_first` 可能长期使用第一个可用连接，`round_robin` 按请求轮转，AegisMesh 的 adaptive P2C 会看 in-flight、EWMA 延迟、slow_score 和 circuit breaker 状态。这里决定了请求实际会打到哪个实例。

第五步，客户端把请求编码成协议消息。以 gRPC/protobuf 为例，请求对象会被序列化成 protobuf bytes，再和方法名、metadata、deadline、压缩标记等一起放进 HTTP/2 headers/data frames。HTTP/2 连接可以承载多个并发 stream，所以一次 RPC 通常对应一个 stream，而不是一条新 TCP 连接。TLS、流控、keepalive、消息大小限制也都在这一层影响行为。

第六步，请求经过网络和中间层。中间可能有 sidecar、L7 proxy、service mesh、网关、负载均衡器、防火墙或 mTLS 终止点。它们可能只做转发，也可能解析 HTTP/2/gRPC，按 method、metadata、authority、tenant 或 route policy 做路由。这里常见问题是超时预算被吃掉、metadata 丢失、代理重试放大、连接级负载均衡导致流量不均。

第七步，服务端接收到请求。gRPC 服务端会识别 service/method，解析 metadata、deadline 和消息体。如果请求已过期，服务端可以尽早停止处理；如果 metadata 里有认证或租户信息，服务端拦截器会先做校验。然后框架把 bytes 反序列化成业务请求对象，并调用注册的 handler。

第八步，业务 handler 执行。这里可能访问数据库、缓存、消息队列或继续调用下游 RPC。关键是 handler 要尊重 context cancellation 和 deadline。如果上游已经超时，继续做昂贵计算通常是在浪费资源。gRPC 官方文档也提醒，客户端和服务端对一次 RPC 是否成功的判断可能不一致：服务端可能已经执行并发出了响应，但客户端因为 deadline 已过而认为失败。

第九步，服务端返回响应或错误。成功时，服务端序列化响应消息，并带上 status OK、trailing metadata。失败时，服务端返回对应 status code 和错误信息。对于 streaming RPC，这个过程不是一次请求一次响应，而是多个消息在同一个 stream 里持续读写，最后再用状态码结束整个 RPC。

第十步，客户端接收结果并收尾。客户端解析响应、还原成响应对象，或者把 status code 转成 error。picker 的 Done 回调、telemetry interceptor、trace writer、retry interceptor 会在这里记录耗时、状态、上游地址、attempt，释放 in-flight，更新 EWMA，归还 circuit breaker token。如果错误可重试，且方法幂等、deadline 还有剩余、retry budget 没耗尽，重试拦截器可能发起下一次 attempt。

最后，结果回到业务代码。业务代码看到的是 response 或 error，但框架内部已经发生了很多动作：地址解析、连接选择、序列化、HTTP/2 传输、服务端分发、handler 执行、状态码返回、指标和 trace 记录。一次 RPC 的稳定性取决于整条链路，而不是某一个 `Call()` 函数。

放到 AegisMesh 上，一次业务 RPC 会经过 SDK 的 resolver、adaptive balancer、retry interceptor、telemetry interceptor 和 trace 记录。调用完成后，AegisMesh 会把 attempt、latency、status、upstream 等写入本地 trace 和 telemetry，再由控制面用这些反馈调整 endpoint 的 slow_score 或策略。也就是说，RPC 调用不只是业务通信，还是治理系统采样真实后端状态的入口。

所以这题的结论是：一次 RPC 调用从 stub 开始，经过拦截器、服务发现、负载均衡、连接选择、序列化、协议传输、服务端分发、业务处理、响应编码、状态返回、客户端解码和指标收尾。讲清楚这些步骤，才能继续解释 deadline、重试、metadata、流控、连接复用和负载均衡为什么会影响系统稳定性。

如果面试官继续深挖，可以按这条路线走：先按客户端 stub 到服务端 handler 的主链路讲；再插入 resolver/balancer/interceptor/deadline/metadata；接着讲服务端返回 status 和 trailing metadata；最后用 AegisMesh 说明 Done 回调、telemetry 和 trace 怎么把一次 RPC 变成可观测、可治理的样本。

## 4. IDL 的作用是什么？为什么很多 RPC 框架需要 IDL？

可以先这样答：

IDL，也就是 Interface Definition Language，解决的是“远程调用双方到底在调用什么”的问题。RPC 调用跨进程、跨机器、跨语言，客户端和服务端必须对服务名、方法名、请求类型、响应类型、字段含义、字段编号、错误语义和版本演进有共同约定。IDL 把这些约定写成机器可读的契约，再通过代码生成器生成不同语言的类型和调用代码。

以 gRPC 和 protobuf 为例，`.proto` 文件里可以定义 message 和 service。message 描述请求/响应的数据结构，service 描述有哪些 RPC 方法，每个方法接收什么请求、返回什么响应。gRPC 官方文档里最简单的形式就是 `rpc SayHello(HelloRequest) returns (HelloResponse)`。这看起来只是一行声明，但它会生成客户端 stub、服务端接口、序列化代码和类型访问器。

IDL 的第一个作用是统一契约。没有 IDL 时，客户端可能以为字段叫 `userId`，服务端实际读的是 `user_id`；客户端以为金额单位是元，服务端以为是分；客户端以为某个字段可选，服务端当成必填。这类问题用 JSON 也能靠文档约定解决，但文档很容易滞后。IDL 至少让字段名、类型、方法签名和生成代码保持一致。

第二个作用是跨语言。RPC 很常见的场景是 Go 服务调用 Java 服务，Python 任务调用 Go 控制面，前端网关调用 C++ 后端。IDL 让这些语言从同一份 schema 生成各自语言的类型，而不是每个团队手写一套模型。Protocol Buffers 官方文档也强调，它是语言中立、平台中立、可扩展的结构化数据序列化机制，并能生成多语言绑定。

第三个作用是让二进制协议可解释。protobuf 的线上数据通常不是自描述的，字段在 wire format 里靠字段编号识别。离开 `.proto` 文件，很多 bytes 很难知道对应什么字段和语义。所以 IDL 既是代码生成输入，也是解码、调试、兼容性分析和治理工具的依据。二进制协议越紧凑，越依赖 schema 管理。

第四个作用是支持版本演进。RPC 接口不是一次写完就不变。新增字段、删除字段、枚举扩展、方法拆分、客户端和服务端滚动发布，都要求旧版本和新版本能同时存在一段时间。protobuf 里字段编号一旦使用就不能随便改；删除字段后应该把字段编号放进 `reserved`，避免未来复用导致解码歧义。这就是 IDL 带来的约束，也是它保护系统的方式。

第五个作用是让工具链围绕契约工作。IDL 可以生成文档、mock、测试桩、网关映射、schema diff、兼容性检查、权限策略、方法级 timeout/retry 配置和可观测性标签。很多 RPC 治理能力其实都依赖“方法是可枚举、字段是可描述、版本是可比较的”。如果所有请求都是一个 `POST /call` 加任意 JSON，治理系统很难可靠地区分接口语义。

IDL 不是所有远程调用都必须有。JSON-RPC、动态语言内部调用、简单 HTTP API、某些事件消息系统可以不强制 IDL，也能跑得很好。OpenAPI、JSON Schema、AsyncAPI 这类东西也能承担一部分契约描述工作。很多强类型 RPC 框架把 IDL 放到了核心路径上：没有 IDL，就没有生成代码、强类型 stub、稳定序列化和一致的多语言接口。

IDL 的代价也要说清楚。它会增加 schema 维护成本，要求团队学习兼容性规则，要求 CI 检查 breaking change，还会带来生成代码提交、语言插件版本、包名规范和发布顺序问题。IDL 写得太贴近内部实现，也会把内部结构泄露给所有客户端，导致以后很难改。所以好的 IDL 应该表达稳定业务契约，而不是把数据库表或内部结构原样暴露出去。

放到 AegisMesh 上，Registry、Telemetry、Policy 这些控制面接口很适合用 IDL。SDK、Controller、demo 服务都需要对实例状态、策略字段、指标样本和服务方法有一致理解。`.proto` 文件让这些接口长期可演进，也让 Go 端生成的 `*_pb.go` 和 `*_grpc.pb.go` 成为明确边界。后续如果要支持其他语言 SDK，同一份 IDL 也能继续生成对应语言的客户端。

所以这题的结论是：IDL 的作用是把远程服务接口写成可生成、可检查、可演进的契约。很多 RPC 框架需要 IDL，是因为它们要做跨语言强类型调用、二进制序列化、客户端/服务端代码生成、版本兼容和方法级治理。IDL 带来约束，但这些约束正是大规模服务协作需要的边界。

如果面试官继续深挖，可以按这条路线走：先说 IDL 定义 service/method/message；再讲代码生成和跨语言；接着讲 protobuf 字段编号和 reserved 的兼容性约束；然后说明不是所有 HTTP API 都必须 IDL；最后落到 AegisMesh 的 Registry、Telemetry、Policy proto，解释为什么控制面协议需要稳定 schema。

## 5. 强类型接口对跨语言服务调用有什么优势和约束？

可以先这样答：

强类型接口最大的价值，是把很多运行时问题提前到编译期或生成代码阶段。跨语言服务调用里，最怕的是双方都以为自己理解了接口，结果字段名、字段类型、枚举值、默认值、可空语义、时间单位、错误码含义都不一样。强类型 IDL 和生成代码至少能让方法签名、请求响应结构和基础字段类型保持一致，减少“线上才发现字段写错”的低级事故。

第一个优势是类型安全。客户端调用 `GetUserRequest` 时，IDE 和编译器能知道哪些字段存在，字段大致是什么类型，响应里能取到什么。服务端实现接口时，也必须满足生成代码定义的方法签名。相比手写 JSON map 或字符串拼接，这能减少字段名拼错、类型转换失败、漏填必填信息、响应结构漂移这类问题。

第二个优势是跨语言一致性。不同语言有不同习惯：Go 有 struct，Java 有 class，Python 更动态，TypeScript 有结构类型。IDL 提供一个中间契约，再由工具生成各语言代码。这样 Go 服务、Java 服务、Python 客户端使用的是同一份 schema，而不是各自维护一套相似但不完全相同的模型。Protocol Buffers 官方文档里提到，同一份消息可以被不同支持语言读写，这正是跨语言 RPC 的基础。

第三个优势是接口可发现、可治理。强类型接口让服务有哪些方法、每个方法的请求响应、字段编号、注释和版本变化都可以被工具读出来。这样可以做文档生成、mock server、契约测试、兼容性扫描、权限绑定、方法级限流、方法级 retry、trace 标签和指标维度。AegisMesh 需要按 gRPC method 做重试、telemetry 和策略判断，强类型方法边界会让这些治理动作更稳。

第四个优势是性能和编码效率。强类型 IDL 通常配合二进制序列化和生成代码。protobuf 这类格式不需要在每次请求里重复传字段名，生成代码也避免了大量反射式解析。对内部高频调用来说，CPU、带宽和内存分配都会受益。当然这不是绝对结论，具体还要看消息大小、语言实现、压缩、网络、缓存和业务耗时。

第五个优势是演进有规则。强类型接口不是不能改，而是要求按规则改。新增可选字段通常比较安全；删除字段要保留编号；枚举扩展要考虑旧客户端；字段语义变化比字段类型变化更危险。IDL 让这些变化可以被 review 和 CI 检查。没有 schema 的动态接口也能演进，但更依赖人工约定和线上兼容测试。

约束也很明显。第一类约束是类型系统不完全一致。比如 Java 的 `long`、Go 的 `int64`、JavaScript 的 number 精度，语义不是完全一样；有些语言区分 nullable，有些语言习惯零值；有些语言没有 unsigned；时间、decimal、bytes、map、oneof、enum unknown value 在不同语言里的体验也不同。IDL 能统一 wire format，但不能消除语言模型差异。

第二类约束是发布协调。强类型接口一旦生成到多个客户端里，服务端改字段或改方法就不能只看自己。老客户端可能还在发旧字段，新客户端可能已经读新字段，服务端可能滚动发布到一半。跨语言 SDK 更新更慢，尤其是移动端、第三方客户和离线部署环境。接口设计必须默认“旧代码会活很久”。

第三类约束是灵活性下降。动态 JSON 接口可以临时加字段、临时透传对象、排查时直接看文本；强类型 RPC 往往要改 IDL、生成代码、发布包、升级客户端。这个流程更稳，但不适合频繁试验不稳定的字段。内部系统可以接受这种约束，公开 API 或快速探索型业务未必适合一开始就强绑定。

第四类约束是调试成本。二进制消息没有 JSON 那么直观，抓包后通常要有 schema 才能解释。生成代码也会让调用栈更长，错误可能藏在拦截器、序列化、channel、balancer 或自动重试里。工程上要配好 reflection、日志脱敏、trace、metadata 打印和错误映射，否则强类型接口会在故障时显得不透明。

第五类约束是耦合边界。强类型接口容易让调用方像调用本地函数一样依赖远端细节，甚至把服务端内部模型直接暴露成 proto。这样短期省事，长期会难改。好的跨语言接口应该稳定、粗粒度、语义清楚，把内部实现藏起来。不要把数据库字段、内部枚举、临时实验开关全都变成公共 IDL。

放到 AegisMesh 上，强类型接口对控制面很有价值。Registry 的实例状态、Telemetry 的指标样本、Policy 的重试和熔断配置，都需要 SDK 和 Controller 精确一致。用 protobuf 可以减少字段漂移，也方便未来生成其他语言 SDK。但项目也要守住边界：proto 表达的是治理契约，不应该把 Go 内部结构、临时实验字段或某个实现细节直接暴露成长期接口。

所以这题的结论是：强类型接口让跨语言调用更可检查、更可生成、更可治理，也通常更适合内部 RPC 的性能和兼容性要求；它的约束是类型差异、schema 演进、发布协调、调试难度和接口耦合。面试时不要只说“强类型更安全”，要能说清楚它把灵活性换成了长期协作边界。

如果面试官继续深挖，可以按这条路线走：先讲编译期检查和生成代码；再讲跨语言一致性；接着讲 protobuf 字段编号、默认值、optional、enum 这些兼容性细节；然后讲二进制协议调试和发布协调成本；最后落到 AegisMesh，说明控制面 proto 适合强类型，但 IDL 不能泄露内部实现。

## 6. 序列化协议应该从哪些维度比较？

可以先这样答：

比较序列化协议，不能只问“哪个更快”或“哪个更省空间”。这两个指标重要，但只看它们很容易选错。真实系统里还要看 schema 怎么管理、跨语言是否稳定、版本能不能平滑演进、调试是否方便、大消息怎么处理、工具链是否成熟，以及协议失败时能不能解释清楚。

第一类维度是 schema 和类型系统。Protobuf、Thrift、Avro 这类协议围绕 schema 或 IDL 工作，字段类型、字段编号、服务方法都写在契约里；JSON 和 MessagePack 更接近通用数据结构表达，schema 可以有，但不是格式本身强制要求。schema 强，生成代码和兼容检查更好做；schema 弱，接入快，但长期协作更依赖文档和测试。

第二类维度是线缆格式和性能。文本格式可读性强，抓包、日志、curl、人工排查都舒服；二进制格式通常更紧凑，字段名不用反复传，数值可以用变长编码或固定宽度编码，解析时也少走字符串处理路径。性能还要拆成编码速度、解码速度、数据大小、内存分配、是否需要反射、是否支持 streaming，而不是只看一个 benchmark。

第三类维度是兼容性。协议要支持新增字段、删除字段、旧客户端读新数据、新客户端读旧数据。Protobuf 依赖字段编号和 unknown fields；Avro 明确区分 writer schema 和 reader schema；JSON 常靠忽略未知字段和默认值约定；MessagePack 本身不替你解决 schema 演进。面试里如果只说“新增字段没问题”，还不够，要说清楚旧读新、新读旧分别怎么处理。

第四类维度是跨语言细节。不同语言对整数、浮点数、bytes、null、默认值、map 顺序、enum unknown value 的处理并不一样。JavaScript number 的整数精度、Go 零值、Java boxed type、Python 动态类型、JSON 没有原生 bytes，都可能让同一个字段在不同语言里表现不一致。序列化协议越强类型，越要把这些差异提前写清楚。

第五类维度是工具链和运维。成熟协议应该有代码生成、schema diff、兼容性检查、mock、文档生成、日志解码、抓包工具、网关转码和监控支持。还要看安全边界：最大消息大小、递归深度、字段数量、压缩后膨胀比例、未知字段保留策略、CPU 时间和内存占用。协议是二进制不等于安全，解析器仍然可能被大消息或恶意结构拖垮。

放到 AegisMesh 上，Registry、Telemetry、Policy 这些控制面接口适合 protobuf，因为字段固定、跨组件共享、需要长期演进，而且 SDK 和 Controller 都要用生成代码保持一致。业务请求如果以后出现大文件、大响应或高维数组，就不应该硬塞进一个 protobuf message，而应该考虑流式传输、对象存储引用或专门的数据格式。

所以这题的结论是：比较序列化协议，要从 schema、类型系统、线缆大小、解析成本、跨语言、兼容性、调试体验、工具链、安全边界、大消息处理和运维可见性一起看。没有一个协议在所有维度都赢，选型应该先看调用场景。

如果面试官继续深挖，可以按这条路线走：先说 schema 强弱；再说二进制和文本的取舍；接着讲兼容性和字段演进；然后讲性能、调试和安全边界；最后落到 AegisMesh，说明为什么控制面协议选 protobuf，而大数据传输不能无脑用 protobuf。

## 7. Protobuf、JSON、Thrift、Avro、MessagePack 的设计取向有什么不同？

可以先这样答：

这几个协议解决的问题有重叠，但设计取向不一样。Protobuf 偏强 schema、紧凑二进制、多语言生成代码和内部服务通信；JSON 偏文本、通用、人可读和 Web 生态；Thrift 偏 IDL、RPC 框架和多协议多传输组合；Avro 偏数据文件、数据管道和读写 schema 演进；MessagePack 偏 JSON-like 数据结构的紧凑二进制表达。

Protobuf 的核心是 `.proto`。字段用编号识别，二进制格式里不传字段名，所以消息紧凑，旧代码也能跳过不认识的新字段。它很适合内部 RPC、控制面协议和跨语言 SDK。代价是调试时离不开 schema，字段编号和兼容规则必须严守。AegisMesh 的 `policy.proto` 和 `telemetry.proto` 就是典型用法：字段结构稳定，组件间需要共同理解。

JSON 的核心优势是简单和通用。它是文本格式，可以表示对象、数组、字符串、数字、布尔和 null，几乎所有语言、浏览器、网关、日志系统都能处理。它适合公开 API、配置、人工调试和对接第三方。代价是体积偏大，数字精度和类型表达有限，字段是否必填、枚举是否合法、版本怎么演进都要靠额外 schema 或业务约定。

Thrift 更像一套跨语言服务系统。它有 IDL，可以定义 struct、union、exception、service，也有字段 ID 和 required/optional/default requiredness。它还把协议和传输抽象出来，历史上常用于跨语言 RPC。跟 Protobuf 相比，Thrift 的设计更早把 service、exception、transport 这些放在一套系统里；跟 gRPC 相比，现代云原生生态的使用重心不太一样。

Avro 的味道更偏数据系统。它的 schema 用 JSON 表达，数据可以用紧凑二进制编码，Object Container File 会带 schema，适合日志、离线文件、数据湖、Kafka/Schema Registry 这类读写端版本长期不一致的场景。Avro 的 reader schema 和 writer schema 分离很有价值：读端可以用自己的 schema 去解释写端数据，只要满足 schema resolution 规则。

MessagePack 比较轻。它把 JSON 里常见的数组、map、string、number、bool、nil 变成二进制表示，还支持 binary 和 extension type。它的优点是比 JSON 紧凑，接入心智接近 JSON，不一定需要 IDL。缺点也在这里：没有强 schema 时，兼容性、字段语义、业务类型和代码生成都要靠上层约定。

放到 AegisMesh 上，我会把控制面和 SDK 通信用 Protobuf/gRPC，把运维配置保留 YAML/JSON 这类可读格式，把离线实验结果继续用 CSV/JSONL 这类方便分析的格式。不要为了统一而统一。协议选型应该服务于读写者、生命周期和排查方式。

所以这题的结论是：Protobuf 追求强契约和紧凑 RPC，JSON 追求通用可读，Thrift 追求跨语言 RPC 系统，Avro 追求数据管道和 schema 演进，MessagePack 追求动态数据的紧凑表达。面试时不要说“某个协议最好”，要说清楚它们各自偏向哪类系统。

如果面试官继续深挖，可以按这条路线走：先按 schema 强弱分；再按 RPC、Web API、数据管道、动态结构分；接着讲字段编号、reader/writer schema、required/optional、可读性；最后落到项目里说明不同数据面不需要强行使用同一种协议。

## 8. 为什么 Protobuf 字段编号比字段名更关键？

可以先这样答：

在 Protobuf 的二进制 wire format 里，真正写进消息的是字段编号和 wire type，不是字段名。字段名主要服务于 `.proto` 可读性、生成代码和 JSON/TextFormat 映射；字段编号才是解析二进制数据时识别“这是哪个字段”的依据。所以一个字段上线后，编号基本就成了这个字段的身份，不能随便改，也不能复用。

Protobuf 编码时，每个字段会形成一个 tag。这个 tag 由字段编号和 wire type 组合而成，低 3 位表示 wire type，其余位表示字段编号。解析器看到 tag 后，先知道字段编号，再按 wire type 判断后面的 payload 怎么读。字段名 `user_id` 没有出现在二进制流里。只要编号一样，解析器就会把它当成同一个字段；编号变了，哪怕字段名不变，也等于换了字段。

这也是 Protobuf 比 JSON 紧凑的原因之一。JSON 每条消息都会反复传字段名，Protobuf 只传 tag 和值。常用字段通常会放在较小的字段编号上，因为 1 到 15 的字段编号在 tag 上更省字节。这个优化看起来细，但在高频内部 RPC、指标上报和日志流里很现实。

字段名当然也重要，但重要性不一样。字段名会影响生成代码的 API、JSON 映射、TextFormat、人读文档和调试体验；字段编号影响二进制兼容性。你可以谨慎地改字段名，让二进制消费者继续正常工作，但 JSON/TextFormat 可能受影响；你一旦改字段编号，二进制消费者就会把旧数据当成 unknown field，或者把新数据读错。

复用字段编号是最危险的情况。比如原来的 `1` 是 `user_id`，后来删掉后又把 `1` 给了 `phone_number`。旧数据和新数据在 wire format 上都带着字段 `1`，解析器无法知道这个 `1` 到底是哪一代语义。轻则解析失败，重则数据污染、权限错误、隐私字段串线。官方文档也明确建议删除字段后 reserve 字段编号，防止未来复用。

放到 AegisMesh 上，`telemetry.proto` 里 `request_count = 6`、`error_count = 7`、`timeout_count = 8` 这种编号一旦被 SDK 和 Controller 共同使用，就不能因为“看起来排序更好”而重排。重排字段编号不是整理代码，而是破坏 wire contract。真正需要废弃字段时，应该保留编号，新增一个新字段号承载新语义。

所以这题的结论是：Protobuf 的二进制兼容性建立在字段编号上，字段名主要影响人和生成代码。字段编号不能改、不能复用；删除字段要 reserve；常用字段可以使用较小编号以减少编码体积。面试里把 tag、wire type 和字段编号这层说清楚，就能说明你理解的是 wire format，不只是会写 `.proto`。

如果面试官继续深挖，可以按这条路线走：先说二进制流里没有字段名；再讲 tag 等于字段编号加 wire type；然后讲改名、改号、复用编号的区别；最后用 AegisMesh 的 telemetry 字段说明为什么 proto 编号是长期契约。

## 9. RPC 中的向前兼容和向后兼容分别是什么意思？

可以先这样答：

RPC 里的兼容性，本质是在问“不同版本的客户端和服务端能不能同时在线”。向后兼容通常指新版本代码能读旧版本数据、能服务旧版本调用方；向前兼容通常指旧版本代码遇到新版本数据或新版本调用方时，不会直接崩掉。说得更工程一点：滚动发布期间，old client、new client、old server、new server 会混在一起，接口变更不能要求全网同一秒升级。

以消息格式看，新代码读旧消息是向后兼容。旧消息里没有新字段，新代码要能接受默认值、optional unset、空 repeated、空 map。旧代码读新消息是向前兼容。新消息里多了旧代码不认识的字段，旧代码应该能跳过 unknown field，而不是解析失败。Protobuf 的新增字段安全，主要就靠这个机制。

以 RPC 方法看，兼容性不只关心字段。新增一个方法通常对旧客户端安全，因为旧客户端不会调用它；删除一个方法就会打断旧客户端。给已有方法新增请求字段，如果是 optional 或有默认语义，通常比较安全；如果服务端突然要求老客户端必须传这个字段，那就是破坏兼容。响应里新增字段也要保证旧客户端能忽略。

以服务发布看，先升级服务端还是先升级客户端，要看变更方向。如果你给响应新增字段，通常先上服务端，再让客户端读；如果你给请求新增可选字段，通常先让服务端能接受新字段，再逐步让客户端发送。危险的是新客户端一发布就发送旧服务端无法理解或语义上无法接受的字段。

兼容性还包括语义兼容。字段类型没变，不代表兼容。比如 `status` 仍然是 string，但原来只有 `OK` 和 `FAILED`，后来加了 `PARTIAL_SUCCESS`，旧客户端可能把它当失败或未知。再比如 `timeout_millis` 仍然是 int64，但单位从毫秒改成微秒，wire format 没坏，业务已经坏了。面试里要主动提这个边界。

放到 AegisMesh 上，`PolicySnapshot` 里新增 `methods` 这类字段，对老 SDK 来说应该可以忽略；新 SDK 读老 Controller 返回的策略时，也要能在字段缺失时使用默认策略。`RetryPolicy` 这类字段如果改语义，比如 `budget_ratio` 从 0.1 表示 10% 改成 10 表示 10%，那就算字段类型没变，也会把线上行为打坏。

所以这题的结论是：向后兼容看新代码能否处理旧数据和旧调用；向前兼容看旧代码能否跳过或容忍新数据。RPC 兼容性要同时看 wire format、方法存在性、字段默认值、枚举扩展、业务语义和发布顺序。真正难的不是“新增字段能不能解析”，而是混部期间行为是否还能解释。

如果面试官继续深挖，可以按这条路线走：先给出新读旧、旧读新的定义；再讲 protobuf unknown fields 和默认值；接着讲服务端/客户端发布顺序；最后提醒语义兼容比 wire 兼容更容易被忽略。

## 10. 删除字段、修改字段类型、复用字段编号会带来什么风险？

可以先这样答：

删除字段、修改字段类型、复用字段编号，风险不是一个级别。删除字段可以做，但要保留编号和名字；修改字段类型有些情况 wire-compatible，但语义可能丢数据；复用字段编号最危险，因为它会让同一段 bytes 在不同版本里代表不同含义，解析器无法自动判断哪一代定义才对。

先看删除字段。Protobuf 里删除字段后，旧客户端可能还会发送这个字段，新服务端会把它当 unknown field；新客户端读旧数据时，这个字段就是缺失或默认值。这个方向通常可控。真正要做的是把已删除字段的编号放进 `reserved`，最好连字段名也保留或 reserve，避免以后有人把同一个编号或 JSON/TextFormat 名字拿去定义新语义。

修改字段类型要分情况。把 `int32` 改成 `int64` 这类变化在某些范围内可能还能被解析，但一旦新端写出超出旧类型范围的值，旧端就会截断或读错。把 `string` 改成 `bytes`、把标量改成 message、把普通字段移动到已有 `oneof`，风险更高。很多变更不是“解析不了”，而是“解析出来但业务语义错了”，这种更难排查。

复用字段编号最应该避免。比如旧版本里 `3` 是 `user_id`，新版本里 `3` 变成 `role`。旧数据被新代码读时，可能把用户 ID 当角色；新数据被旧代码读时，可能把角色当用户 ID。因为 wire format 只知道字段编号和 wire type，不知道字段名，也不知道历史语义。官方文档把复用编号的后果说得很重，包括数据污染和隐私泄漏，这不是理论风险。

枚举也要小心。新增枚举值通常比删除枚举值安全，但旧客户端看到 unknown enum 怎么处理，取决于语言和版本。业务代码如果用 `default` 分支把未知值当成功或当失败，都可能出错。状态类字段最好保留 `UNSPECIFIED = 0`，新增值要让旧逻辑走保守路径。

字段语义变化也算破坏兼容。比如 `latency_p95_seconds` 仍然是 double，但新版本改成毫秒；`retry_count` 仍然是 int64，但从“重试次数”改成“总尝试次数”。wire format 完全正常，指标和告警却会全部错。面试时要强调：兼容性不只由 IDL 编译器保证，语义变化也要进入 review。

放到 AegisMesh 上，`EndpointStatsSample` 里已经有 `request_count`、`error_count`、`timeout_count`、`retry_count`、`latency_ewma_seconds` 等字段。以后如果废弃 `tcp_retransmit = 13`，正确做法是保留编号，不复用给别的指标。想新增 `queue_depth`，就用新的字段编号。想改单位，就新加字段，比如 `latency_ewma_millis`，等两端都升级后再清理旧字段。

所以这题的结论是：删除字段要 reserve，修改类型要看 wire 兼容和语义兼容，复用字段编号基本等于埋数据事故。生产里的 proto 演进要靠 schema review、兼容性检查、灰度发布和长期保留废弃编号，而不是靠“看起来字段没人用了”。

如果面试官继续深挖，可以按这条路线走：先说删除字段可控但要 reserve；再讲类型变化可能解析成功但丢数据；接着强调复用编号最危险；最后补充语义变化和单位变化也属于兼容性风险。

## 11. gRPC 为什么基于 HTTP/2？HTTP/2 给 gRPC 带来了哪些能力？

可以先这样答：

gRPC 选择 HTTP/2，不是因为 HTTP/2 这个名字更“新”，而是因为它刚好提供了 RPC 框架需要的一组传输语义：长连接、二进制分帧、多路复用、头部和尾部元数据、流级别控制、流控和取消。RPC 调用看起来像一次函数调用，但在网络上需要表达请求消息、响应消息、调用元数据、最终状态、错误详情、超时取消、流式传输。HTTP/2 的 stream 模型能比较自然地承载这些东西。

如果用 HTTP/1.1 来做，同一条连接上并发请求很难处理。HTTP/1.1 虽然有 keep-alive，但一个连接通常同一时刻处理一个请求，或者依赖已经很少使用的 pipelining，容易出现队头阻塞。想提升并发，就要开很多连接。HTTP/2 把一条 TCP 连接拆成多个独立 stream，不同 RPC 可以在同一连接上并发传输，各自有 stream id、frame 序列和状态机。对 gRPC 来说，这让“一个 channel 上跑很多 RPC”成为常态。

HTTP/2 的 header 和 trailer 对 gRPC 也很关键。请求里的 method、authority、content-type、grpc-timeout、认证信息、trace 信息可以放在 headers 里；服务端最终的 `grpc-status`、`grpc-message`、错误详情可以放在 trailers 里。这样响应 body 可以专注承载业务消息，最终调用结果由 trailers 表达。很多面试回答只说“HTTP/2 支持多路复用”，但 metadata 和 trailers 对 gRPC 的错误模型同样重要。

HTTP/2 的流控让流式 RPC 有了底层背压能力。server streaming、client streaming、bidirectional streaming 都可能一边生产一边消费。如果接收端处理慢，发送端不能无限写入内存；HTTP/2 的 window update 机制可以让写入被阻塞或放慢。gRPC 在此之上再提供语言层面的 send/recv API，应用还要继续做队列、限速和取消处理。

还有一个容易被忽略的点：HTTP/2 解决的是 HTTP/1.x 应用层队头阻塞，不代表完全没有队头阻塞。HTTP/2 通常跑在一条 TCP 连接上，TCP 层丢包仍然会影响这条连接上的所有 stream。HTTP/3/QUIC 后来强调的一个价值，就是把 stream 多路复用下沉到 QUIC，减少 TCP 层队头阻塞。回答时可以顺手提这个边界，说明你没有把 HTTP/2 神化。

放到 AegisMesh 上，SDK 走 gRPC，就能利用 method、metadata、status code、deadline、stream 等统一抽象。比如 `PolicyService.WatchPolicy` 是 server streaming，适合用一个请求建立策略观察流；客户端拦截器可以通过 metadata 写入 trace id、attempt；遥测拦截器可以按 method 和 status 记录指标。这些能力如果都靠裸 TCP 自己设计，协议和 SDK 成本会高很多。

所以这题的结论是：gRPC 基于 HTTP/2，是为了把 RPC 调用映射到成熟的 stream、frame、header、trailer、flow-control 和长连接模型上。HTTP/2 给 gRPC 带来的不只是多路复用，还有元数据传播、状态收尾、流式传输、取消和背压这些 RPC 框架真正需要的基础能力。

如果面试官继续深挖，可以按这条路线走：先讲 HTTP/1.1 的连接并发限制；再讲 HTTP/2 stream 和 frame；接着讲 metadata、trailers、flow control；最后补充 HTTP/2 仍然受 TCP 队头阻塞影响。

## 12. gRPC unary、server streaming、client streaming、bidirectional streaming 分别适合什么场景？

可以先这样答：

gRPC 的四种调用模式，本质是在回答“请求和响应各是一条消息，还是一串消息”。unary 是一个请求对应一个响应；server streaming 是一个请求对应服务端多次响应；client streaming 是客户端多次发送请求，服务端最后返回一个响应；bidirectional streaming 是双方都可以持续发送消息。不要只背定义，最好能说明它们各自适合什么业务形态，以及失败、重试、流控会有什么差异。

unary 最适合传统的查询、创建、更新、删除和一次性计算。比如查用户、创建订单、上报一次状态、获取一次策略快照，都可以用 unary。它的优点是语义简单，deadline、重试、错误码、指标统计都容易处理。大多数 RPC 方法应该先从 unary 开始，只有当业务真的需要连续消息时，再引入 streaming。

server streaming 适合“客户端发起订阅，服务端持续推送结果”的场景。典型例子是 watch 配置、推送策略变更、下载大结果集、日志尾随、任务进度通知。客户端只有一个初始请求，后续由服务端不断返回消息。它的关键问题是连接生命周期、心跳、慢客户端、服务端资源占用和重连后如何从上次位置继续。

client streaming 适合“客户端持续上传一组数据，服务端最终汇总确认”的场景。比如批量上传日志、指标、文件分片、传感器数据，或者把多个小事件聚合成一次处理。它比 unary 批量请求更节省头部开销，也能让服务端边收边处理。风险在于服务端不能无界缓存，客户端也要根据服务端读速率接受背压。

bidirectional streaming 适合双方都需要持续交互的场景，比如实时协商、长连接控制通道、聊天、代理隧道、在线语音、双向同步。它最灵活，也最难写好。因为请求流和响应流不一定一一对应，应用层要定义自己的消息类型、序列号、确认机制、错误恢复和关闭语义。面试里可以说：能用 unary 或单向 streaming 解决的问题，不要一上来就做 bidi。

四种模式对重试也不一样。unary 在幂等前提下最容易自动重试；server streaming 如果已经收到一部分响应，再自动重试可能造成重复数据；client streaming 如果服务端已经处理了前半段输入，客户端重发也会有重复或乱序；bidi streaming 更复杂，通常需要应用层 checkpoint 或消息幂等键。流式 RPC 不是不能恢复，而是恢复逻辑要显式设计。

放到 AegisMesh 上，`UserService.GetUser`、`OrderService.CreateOrder` 这种 demo 方法适合 unary；`PolicyService.WatchPolicy` 适合 server streaming，因为 SDK 需要持续收到 Controller 的策略更新；高频遥测如果以后从周期性 unary 上报演进成连续上传，就可以考虑 client streaming；如果 SDK 和 Controller 要做实时协商、推送控制指令、回传执行结果，才更像 bidi streaming。

所以这题的结论是：unary 用于一次性请求响应，server streaming 用于服务端持续推送，client streaming 用于客户端连续上传，bidirectional streaming 用于双方持续交互。选型时要同时看业务模型、资源占用、流控、取消、重试和恢复语义，不能只看 API 是否“高级”。

如果面试官继续深挖，可以按这条路线走：先定义四种模式；再各举一个真实场景；接着讲流式调用的背压和取消；最后说明流式 RPC 的自动重试比 unary 更难。

## 13. RPC deadline 和 HTTP timeout 有什么区别？

可以先这样答：

RPC deadline 和 HTTP timeout 都和“等多久”有关，但语义层次不同。deadline 更像一次调用的端到端预算，它告诉整个调用链：这件事最晚到什么时候必须结束。HTTP timeout 往往是某个客户端、代理或连接库上的本地等待限制，比如连接超时、读超时、写超时、空闲超时、上游响应超时。前者更偏调用语义，后者更偏传输或组件配置。

gRPC 里常见的是 deadline 或 timeout 转换后的剩余时间。客户端发起调用时设置一个 deadline，服务端可以看到调用还剩多少时间。服务端继续调用下游时，应该把剩余预算传播下去，而不是重新给每个下游一个完整超时。这样才能避免上游已经放弃了，下游还在继续烧 CPU、占连接、占锁。官方文档也强调，gRPC 默认不设置 deadline，所以生产代码通常要显式设置。

HTTP timeout 更分散。一个 HTTP 客户端可能有 dial timeout、TLS handshake timeout、response header timeout、read timeout；反向代理可能有 upstream connect timeout、idle timeout、request timeout；服务端也可能有 header read timeout、write timeout。这些 timeout 当然有用，但它们不一定会自动变成业务调用的剩余预算，也不一定能被下游服务看见。

错误语义也不一样。RPC deadline 到期后，客户端通常看到 `DEADLINE_EXCEEDED` 或上下文超时；服务端如果能感知取消，就应该停止处理。不过这不代表服务端一定没有完成操作。网络慢、响应丢失、客户端 deadline 太短，都可能导致客户端超时，但服务端实际已经把写库或发消息做完了。所以 deadline 和重试、幂等性必须一起设计。

deadline 还可以帮助排队和降级。服务端收到请求时，如果发现剩余时间只剩几毫秒，就不应该再进入一个需要几百毫秒的排队路径，而应该快速失败或走降级。单纯的 HTTP read timeout 通常只能在等待太久之后被动触发，不能很好地指导服务端提前停止无意义工作。

放到 AegisMesh 上，`sdk/go/aegisgrpc/retry.go` 里有 per-try timeout 的概念，它可以限制每次尝试花多久；但整个 RPC 还需要外层 context deadline 控制总预算。否则每次重试都给一个完整超时，整体延迟会被放大。更合理的做法是：外层 deadline 控制用户可接受时间，每次 attempt 在剩余预算内再分配 per-try timeout，并把 trace 和 attempt 信息写入 metadata。

所以这题的结论是：HTTP timeout 是必要的传输保护，RPC deadline 是端到端调用预算。一个成熟 RPC 框架应该把 deadline 和 context 贯穿客户端、服务端、下游调用、重试和取消，而不是只在某个 HTTP 客户端上设置一个等待时间。

如果面试官继续深挖，可以按这条路线走：先区分端到端预算和本地等待限制；再讲 deadline propagation；接着讲超时后服务端可能已经完成；最后把 deadline 和重试幂等、排队降级联系起来。

## 14. 为什么 RPC 框架需要 metadata？metadata 通常承载哪些信息？

可以先这样答：

RPC 框架需要 metadata，是因为一次远程调用除了业务请求体，还有很多“调用上下文”需要随请求一起传递。业务请求体应该描述业务对象，比如用户、订单、策略、指标；metadata 描述这次调用本身，比如谁发起、属于哪个租户、trace id 是什么、deadline 还剩多久、要不要走灰度、客户端版本是多少。把这些信息混进业务 message，会污染接口模型，也会让跨语言中间件很难统一处理。

在 gRPC 里，metadata 通常映射到 HTTP/2 headers 和 trailers。请求 headers 可以带认证 token、trace context、request id、tenant id、locale、client version、routing hint、idempotency key、灰度标签、调用来源等。响应 trailers 可以带最终状态、错误详情、服务端统计、配额信息、负载反馈或者调试标记。metadata 是 side channel，但不是随便塞东西的垃圾桶。

metadata 最常见的用途是认证和授权。比如客户端在 metadata 里带 bearer token、mTLS 之外的应用身份、租户标识，服务端拦截器先做鉴权，再决定是否进入业务 handler。这里要注意信任边界：外部请求传来的 tenant header 不能直接相信，通常需要由网关或认证层校验后改写成内部可信 metadata。

第二类用途是可观测性。trace id、span id、baggage、request id 都适合放在 metadata 中，让调用链上的每个服务都能把日志、指标和 trace 串起来。metadata 比 body 更适合这件事，因为拦截器和代理不需要理解业务 schema，也能完成注入、提取和传播。

第三类用途是路由和治理。灰度发布可以根据 `x-user-cohort`、`x-tenant`、`x-release-channel` 这类 metadata 做流量染色；重试可以把 attempt 写进去；限流可以按租户和方法组合统计；负载均衡可以读取服务端 trailers 里的负载反馈。注意这些字段要有明确规范，避免不同团队随意命名，最后变成不可治理的隐式协议。

metadata 也有边界。它通常有大小限制，官方文档也提醒默认请求头大小可能有限；它不适合放大对象、复杂业务字段、隐私明文或频繁变化的大数组。metadata 还可能出现在日志、代理、调试面板中，所以要做脱敏。二进制 metadata 可以用特定后缀，但也要控制大小和兼容性。

放到 AegisMesh 上，`trace.go` 里已经定义了 `x-aegis-trace-id`、`x-aegis-span-id`、`x-aegis-attempt` 这类 metadata。它们不属于 `GetUserRequest` 或 `CreateOrderRequest` 的业务字段，却对重试、遥测和 trace 很关键。以后如果要做灰度，也可以把租户、用户分组、版本通道作为 metadata，由 SDK 拦截器统一注入，由负载均衡或治理层统一读取。

所以这题的结论是：metadata 是 RPC 调用的上下文通道，适合承载认证、追踪、租户、路由、重试、版本和负载反馈等横切信息。它应该有边界、有命名规范、有大小控制、有安全处理，不能替代业务 payload。

如果面试官继续深挖，可以按这条路线走：先区分业务 body 和调用上下文；再讲 headers/trailers；接着列认证、trace、租户、灰度、幂等键；最后补充大小限制和信任边界。

## 15. RPC 拦截器和中间件可以解决哪些横切关注点？

可以先这样答：

RPC 拦截器和中间件解决的是横切关注点，也就是“每个接口都要做，但不应该散落在每个业务 handler 里”的事情。典型例子包括认证、鉴权、日志、指标、trace、超时、重试、限流、熔断、参数校验、metadata 注入、错误映射、灰度标签、故障注入和审计。它们的价值不是让业务代码少写几行，而是让这些规则在所有方法上保持一致。

客户端拦截器通常负责发起调用前后的治理逻辑。比如给 metadata 注入 trace id 和 tenant id；按 method 查重试策略；为每次 attempt 设置 per-try timeout；记录请求耗时、目标实例和状态码；把连接错误和 RPC 状态归一化成可观测字段。客户端最接近调用策略，因此很适合做重试、负载均衡配合、deadline 处理和指标打点。

服务端拦截器通常负责进入业务 handler 之前和之后的逻辑。比如解析身份、检查权限、限流、校验请求大小、提取 trace context、记录访问日志、把 panic 或异常转成规范状态码、把业务错误映射成响应或 status。这样业务 handler 可以更专注于领域逻辑，而不是每个方法都手写同样的鉴权和打点。

拦截器要区分 unary 和 stream。unary 拦截器围绕一次请求响应执行，比较直观；stream 拦截器要处理长生命周期、多次 send/recv、半关闭、取消和背压。很多框架里 unary 和 stream 拦截器是不同接口，原因就在这里。流式 RPC 的日志和指标也不能只记录一次开始结束，通常还要关心消息数量、持续时间和中途错误。

拦截器顺序很重要。比如客户端上重试拦截器如果包在遥测拦截器外层，遥测可能记录每一次 attempt；如果顺序反过来，遥测可能只看到一次整体调用。认证要在业务处理前，panic recovery 要包住业务 handler，日志可能要包住整个链路。面试里可以说：拦截器不是随便堆，顺序就是语义。

拦截器也不适合做所有事情。底层 TCP 连接建立、TLS 握手、HTTP/2 frame 级控制，通常不在普通 RPC 拦截器里处理。拦截器运行在“每次 RPC 调用”的边界上，适合处理 call-level 逻辑，不适合直接替代 resolver、balancer、transport 或线程模型。把职责边界说清楚，回答会更稳。

放到 AegisMesh 上，`sdk/go/aegisgrpc/dial.go` 会把 retry interceptor 和 telemetry interceptor 链起来；`retry.go` 按策略判断是否重试，并设置 attempt context；`interceptor.go` 记录 method、upstream、status、latency 等遥测。这个设计说明拦截器很适合把 RPC 治理能力放到 SDK 层，而不是要求每个业务服务手写同样逻辑。

所以这题的结论是：RPC 拦截器和中间件适合承载认证、可观测性、重试、超时、限流、错误映射、metadata 传播等横切逻辑。设计时要区分客户端和服务端、unary 和 stream、整体调用和单次 attempt，并且明确拦截器顺序带来的行为差异。

如果面试官继续深挖，可以按这条路线走：先给横切关注点定义；再分客户端和服务端；接着讲 unary/stream 差异；最后用拦截器顺序解释为什么同一组中间件组合不同，结果会不同。

## 16. RPC 客户端连接池和 HTTP 连接池有什么区别？

可以先这样答：

HTTP 连接池和 RPC 客户端连接池都在复用连接，但关注点不完全一样。传统 HTTP 连接池通常按 scheme、host、port、proxy 等维度缓存 TCP/TLS 连接，请求来了就借一条连接，用完归还。RPC 客户端，尤其是 gRPC，更常见的抽象是 channel 或 ClientConn：它代表一个逻辑服务目标，背后可能有 resolver、多个地址、多个 subchannel、负载均衡策略、连接状态和健康信息。

在 HTTP/1.1 里，一个连接同一时间处理请求的能力有限，所以连接池大小直接影响并发。请求分布也常常和“从池里拿到哪条连接”有关。HTTP/2 之后，一条连接可以承载多个并发 stream，连接池不再只是“多开几条连接提高并发”，还要考虑 stream 上限、流控窗口、长连接健康、单连接热点和连接级负载均衡公平性。

RPC 连接池还要处理服务发现。客户端并不总是连接一个固定 host，它可能连接一个服务名，比如 `aegis:///shop.UserService`。resolver 把服务名解析成多个实例地址，负载均衡器在每次 RPC 开始时选择一个 ready subchannel。也就是说，RPC 的连接管理和实例列表、权重、健康状态、灰度策略绑定在一起，而不只是复用 socket。

RPC 长连接还会承载更多状态。一个 gRPC channel 里有连接状态转换，比如 idle、connecting、ready、transient failure、shutdown；subchannel 可能因为连接失败进入退避；resolver 可能推送新地址；balancer 需要重新生成 picker。HTTP 连接池当然也要处理坏连接，但通常不负责完整的服务治理决策。

长连接和流式 RPC 会让连接池更复杂。一个 server streaming 或 bidi streaming 调用可能占用某个 subchannel 很久。即使新请求已经均匀分配，老 stream 仍然留在原连接上，实例下线、权重变化、扩容缩容都不会自动迁移已有 stream。连接池如果只在连接建立时选后端，就容易造成流量长期倾斜。

放到 AegisMesh 上，`resolver.go` 会从 Controller 拉取服务实例，并把实例状态、权重、region、zone 等属性放进地址；`dial.go` 给服务构造 gRPC target；自定义 balancer 再根据策略选择后端。这和普通 HTTP 客户端按 host 建一个连接池不同。AegisMesh 的连接管理天然和服务发现、健康、权重、重试、遥测耦合在一起。

所以这题的结论是：HTTP 连接池主要是复用到某个网络目标的连接，RPC 客户端连接池通常是面向逻辑服务的 channel/subchannel 管理系统。它不仅要复用连接，还要配合 resolver、负载均衡、健康检查、重试、流式调用和连接状态机。

如果面试官继续深挖，可以按这条路线走：先讲 HTTP/1.1 连接复用；再讲 HTTP/2 stream 多路复用；接着讲 gRPC channel、resolver、subchannel、balancer；最后说明长连接和流式 RPC 会让“连接池均衡”变得更难。

## 17. RPC 中如何处理服务端返回的错误码和业务错误？

可以先这样答：

RPC 里要先把错误分成两层：一层是 RPC 调用本身有没有成功完成，另一层是业务动作的结果是什么。RPC 状态码应该描述调用是否成功到达、服务端是否能处理、权限是否允许、deadline 是否超时、服务是否不可用、服务端是否发生内部错误。业务错误描述领域规则，比如库存不足、余额不够、订单状态不允许取消、用户名已存在。两层混在一起，客户端、重试、监控和告警都会变得很混乱。

如果服务端根本没有正常处理请求，应该返回 RPC 错误状态。比如参数格式不合法可以是 `INVALID_ARGUMENT`，没有登录是 `UNAUTHENTICATED`，没有权限是 `PERMISSION_DENIED`，找不到资源可以是 `NOT_FOUND`，并发状态冲突可以是 `ABORTED` 或 `FAILED_PRECONDITION`，服务临时不可用可以是 `UNAVAILABLE`，超过 deadline 是 `DEADLINE_EXCEEDED`，服务端 bug 或依赖异常可能是 `INTERNAL`。这些状态码给框架和基础设施使用，影响重试、告警和 SLO 统计。

如果请求已经被正常处理，只是业务规则不允许成功，处理方式要看接口语义。有些团队会把业务错误放进响应体，比如 `CreateOrderResponse{success=false, code="OUT_OF_STOCK"}`，RPC status 仍然是 OK，表示调用本身成功。也有些场景会用合适的 RPC status 表达方法无法完成，比如资源已存在用 `ALREADY_EXISTS`，前置条件不满足用 `FAILED_PRECONDITION`。关键不在于只能选一种，而是要有一致规范。

更细的业务错误通常不要强行塞进少数几个 RPC 状态码里。RPC 状态码数量有限，它们是跨语言、跨框架的通用错误分类；业务错误码可以更细，比如 `ORDER_ALREADY_PAID`、`COUPON_EXPIRED`、`ACCOUNT_FROZEN`。如果业务需要给前端展示、给客服排查、给调用方做分支逻辑，就应该在响应 message 或 error details 中提供结构化业务信息。

服务端还要避免泄漏内部细节。`INTERNAL` 不能直接把数据库 SQL、栈信息、密钥路径返回给外部；但完全返回“系统错误”也不利于排查。比较好的做法是：对外返回稳定的错误码、可读但不敏感的 message、request id 或 trace id；内部日志记录详细 cause、依赖、实例和栈信息。错误处理要同时考虑安全和可观测性。

放到 AegisMesh 上，SDK 遥测用 `status.Code(err)` 记录 RPC 结果，这适合统计网络、超时、不可用、服务端错误。如果 demo 的 `CreateOrder` 因库存不足失败，把它记成 `UNAVAILABLE` 就会误导重试策略；更合理的是把业务失败表达为业务响应或明确的业务错误详情。反过来，如果 Controller 不可达，却把结果包装成业务 `success=false`，治理层就看不见真实故障。

所以这题的结论是：RPC 状态码用于描述调用层和框架层结果，业务错误用于描述领域规则。处理服务端错误时要先分层，再决定是返回 status、响应体业务码，还是 status 搭配结构化 error details；同时要兼顾重试语义、监控统计和安全脱敏。

如果面试官继续深挖，可以按这条路线走：先分 RPC 调用失败和业务动作失败；再举常见 status code；接着讲业务错误码的位置；最后强调错误映射会影响重试、SLO 和安全。

## 18. RPC 状态码和业务状态码为什么不应该混用？

可以先这样答：

RPC 状态码和业务状态码不应该混用，因为它们服务的对象不同。RPC 状态码服务于框架、客户端 SDK、负载均衡器、重试策略、监控和告警；业务状态码服务于产品逻辑、调用方业务分支、用户提示和领域流程。如果把库存不足、余额不足这类业务结果伪装成 `UNAVAILABLE`，基础设施会以为服务故障，可能触发重试和告警；如果把数据库连接失败包装成业务 `SYSTEM_BUSY` 并返回 OK，基础设施又会以为调用成功。

混用的第一个后果是重试错误。比如 `CreateOrder` 返回 `UNAVAILABLE`，客户端重试拦截器可能自动重试。真实原因如果是“库存不足”，重试只会增加压力；真实原因如果是“订单已经创建但响应超时”，重试还可能造成重复订单。反过来，如果网络故障被包装成业务码，RPC 框架不知道这是可重试的临时故障，也就无法做退避和换实例。

第二个后果是指标和 SLO 失真。服务端把业务失败都返回非 OK，错误率会被业务流量结构影响，比如促销时库存不足变多，监控看起来像服务大面积故障。服务端把系统故障都返回 OK，错误率又会被压低，直到用户投诉才发现接口不可用。SLO 统计应该区分“技术可用性”和“业务成功率”，两者都重要，但不能用一个码混着算。

第三个后果是调用方语义不稳定。RPC 状态码是跨语言、跨框架的通用分类，业务状态码是服务自己定义的领域协议。调用方如果一会儿看 status，一会儿看 response.code，而且两边都可能表达同一件事，就很容易写出互相矛盾的处理逻辑。接口文档也会变得难懂：到底 `NOT_FOUND` 表示资源不存在，还是业务 code 里的 `USER_NOT_FOUND` 表示资源不存在？

比较稳的做法是先定分层规则。调用未被正常处理、基础设施失败、认证失败、权限失败、请求格式非法、服务端内部异常，用 RPC status 表达。调用被正常处理，但业务规则导致不能达成目标，用业务码或结构化业务结果表达。中间地带，比如资源不存在、前置条件不满足，可以选择 RPC status，也可以选择业务码，但团队要统一，不要按个人习惯随意切换。

放到 AegisMesh 上，重试策略通常会按 `Unavailable`、`DeadlineExceeded` 这类 RPC code 做判断。如果业务错误也用这些 code，SDK 会把领域失败当成基础设施失败；如果基础设施失败被业务响应吞掉，遥测里的 `error_count`、`timeout_count`、`connect_error` 就不能反映真实故障。对一个治理系统来说，这会直接影响负载均衡和熔断判断。

所以这题的结论是：RPC 状态码和业务状态码的边界，是可靠性治理和业务语义之间的边界。RPC status 负责调用是否可靠完成，业务 code 负责领域结果；混用会破坏重试、监控、告警、SLO 和客户端分支逻辑。

如果面试官继续深挖，可以按这条路线走：先讲两类状态码面向的消费者不同；再举重试误判和 SLO 失真；接着给出分层规则；最后说明有些边界场景要靠团队规范统一。

## 19. RPC 框架中的重试应该放在哪一层？

可以先这样答：

RPC 重试最适合放在客户端 RPC 框架或 SDK 层，但不是所有重试都只能放这一层。客户端 RPC 层最了解 method、deadline、metadata、status code、负载均衡结果和 attempt 信息，所以它适合做通用的、短周期的、幂等方法重试。业务层适合做带业务补偿和状态机的重试。代理或 service mesh 也能做部分重试，但它通常看不懂业务幂等性和请求语义。真正要避免的是多层都偷偷重试，最后形成重试放大。

客户端 SDK 层重试的优势是信息完整。它可以知道这次调用的总 deadline 还剩多少，是否已经收到响应 headers，错误码是不是可重试，method 是否被标记为 idempotent，本次 attempt 选中了哪个后端，是否应该换一个实例，重试预算还剩多少。它还能把 attempt 写进 metadata 和 trace，方便排查。

代理层重试的优势是对应用无侵入，适合统一治理入口流量，比如网关对部分 GET 请求做一次快速重试，或者 service mesh 对连接失败做短退避重试。但代理层也有盲区：它可能不知道某个 POST 是否幂等，不知道业务已经执行到哪一步，也不一定知道应用层 deadline。代理层重试如果和客户端 SDK 重试叠加，就可能把一次用户请求放大成多次后端请求。

业务层重试适合长周期、强语义的场景。比如支付、发货、审批、消息投递，需要幂等键、状态机、补偿、人工介入和审计。这个层次的重试不能交给 RPC 框架自动完成，因为框架只知道一次远程调用失败了，不知道业务流程是否可以继续、是否需要查单、是否要回滚或发起补偿。

RPC 框架做自动重试时，还要遵守几个约束：只对明确可重试的状态码重试；只对幂等或声明安全的方法重试；设置最大次数、退避和 jitter；受整体 deadline 限制；有重试预算或限流，避免故障时自我放大；一旦响应已经提交，就不能假装没发生过。gRPC 官方重试模型里也强调，重试会创建新的 stream，并且收到响应 header 后调用就进入 committed 状态。

放到 AegisMesh 上，`retry.go` 把重试放在 Go SDK 的 unary interceptor 中，这是一个合理位置。它可以读取 method policy、设置 per-try timeout、记录 attempt、结合 status code 决定是否重试。更复杂的业务重试，比如 `CreateOrder` 已经落库后响应丢失，就不应该只靠这个拦截器解决，而要靠订单号、幂等键或业务查询确认。

所以这题的结论是：通用短周期 RPC 重试放在客户端框架或 SDK 层最合适，业务补偿型重试放在业务层，代理层重试要谨慎使用并避免叠加。重试策略必须和 deadline、幂等性、错误分类、负载均衡和观测性一起设计。

如果面试官继续深挖，可以按这条路线走：先比较客户端 SDK、代理层、业务层；再说明客户端层信息最完整；接着讲多层重试放大；最后落到幂等、deadline、backoff、budget 和 committed 语义。

## 20. RPC 自动重试为什么需要幂等性约束？

可以先这样答：

RPC 自动重试需要幂等性约束，是因为客户端看到失败，并不等于服务端没有执行。网络超时、连接断开、响应丢失、代理重置、deadline 到期，都可能发生在服务端已经完成写库、扣款、发消息之后。客户端如果自动再发一次，请求就变成了至少一次执行。没有幂等性，自动重试可能把一次用户意图变成多次业务动作。

最典型的例子是创建订单。客户端调用 `CreateOrder`，服务端已经创建订单并提交事务，但响应在返回途中丢了。客户端看到 `DEADLINE_EXCEEDED` 或 `UNAVAILABLE` 后自动重试，如果请求没有幂等键，服务端可能再创建一笔订单。对读请求来说，重试通常比较安全；对写请求来说，安全性取决于服务端是否能识别“这是同一个业务请求”。

幂等性不是只看 HTTP method 或 RPC 方法名。`GetUser` 大概率是幂等的，但如果它顺手写访问记录、扣一次查询次数，就不完全是纯读。`CreateOrder` 默认不是幂等的，但如果客户端提供 stable request id，服务端按 request id 做去重，并保证相同请求返回同一结果，它就可以被设计成幂等。面试里要强调：幂等是服务端语义，不是客户端愿望。

RPC 框架只能根据配置或 IDL 标注判断是否允许重试，不能凭空证明业务幂等。比较稳的做法是按 method 配置重试策略：读方法允许重试；写方法默认不自动重试；声明 idempotent 的写方法必须要求幂等键、去重表或唯一约束；非幂等方法只允许在很窄的场景做透明重试，比如请求还没有真正写到 wire 或还没被服务端接收。

错误码也不能单独决定是否重试。`UNAVAILABLE` 通常可重试，但如果方法非幂等，仍然不能自动重试。`DEADLINE_EXCEEDED` 更敏感，因为官方状态码说明里也提到，即使操作已经成功完成，也可能返回 deadline exceeded。这个细节非常适合作为面试深挖点：超时不是失败证明，只是客户端没有在预算内拿到确认。

放到 AegisMesh 上，`MethodPolicy` 里如果有 idempotent 或重试策略标记，就应该让 SDK 只对安全方法做自动重试。`UserService.GetUser` 比较适合重试；`OrderService.CreateOrder` 如果没有业务幂等键，就不应该因为 `Unavailable` 自动重试多次。AegisMesh 的 retry interceptor 可以执行策略，但策略本身必须来自接口语义或服务治理配置。

所以这题的结论是：自动重试把失败调用变成至少一次执行，必须有幂等性、幂等键、去重机制或明确的安全方法语义兜底。否则重试不是提升可靠性，而是在故障时制造重复写、重复扣费、重复消息和状态污染。

如果面试官继续深挖，可以按这条路线走：先说客户端失败不等于服务端未执行；再举创建订单响应丢失；接着区分读幂等、写幂等和幂等键；最后强调错误码、deadline 和重试策略不能脱离业务语义。

## 21. RPC 如何支持超时、取消和上下文传播？

可以先这样答：

RPC 支持超时、取消和上下文传播，核心是把“调用上下文”作为一等概念贯穿客户端、服务端和下游调用。客户端发起 RPC 时带上 context 或 deadline；框架把 deadline 编码到传输层 metadata；服务端 handler 能从 context 里看到取消信号和剩余时间；如果服务端继续调用下游，它应该把同一个调用链的剩余预算、trace 信息和必要 metadata 继续传播下去。

超时是主动设定预算。客户端不能无限等，服务端也不能无限处理。一个 RPC 框架通常会支持整体 deadline、per-try timeout、连接超时、读写超时等不同层次。关键是整体 deadline 要约束整个调用生命周期，而不是每次重试都重新开始计时。否则一次用户请求可能因为多次 attempt 被拖得很长。

取消是调用方表达“不再需要结果”。取消可能来自用户断开连接、上游请求超时、客户端主动 cancel、服务端关闭、网络 I/O 错误。服务端收到取消后，应该尽快停止无意义工作，包括停止排队、停止读写、取消数据库查询、停止下游 RPC、释放锁和缓冲区。取消不是自动回滚，已经提交的事务不会因为 context canceled 自动撤销，这一点要说清楚。

上下文传播包括两类信息。一类是控制语义，比如 deadline、cancel signal、tenant、auth、idempotency key、灰度标签；另一类是可观测性语义，比如 trace id、span id、request id、baggage。传播时要过滤和规范，不是把所有上游 header 原样转发。跨信任边界的字段需要重新校验或重写，敏感字段要避免泄漏。

在服务端实现上，handler 要主动检查 context。短请求可能只在调用数据库或下游 RPC 时自然感知取消；长循环、流式推送、批处理任务则必须在循环里定期检查 `ctx.Done()` 或等价信号。如果服务端不检查，客户端即使取消了，服务端也会继续消耗资源，最后形成“用户已经走了，系统还在忙”的浪费。

流式 RPC 对取消尤其敏感。server streaming 要在客户端断开或 deadline 到期时停止发送；client streaming 要在服务端不再需要输入时通知客户端；bidi streaming 要处理半关闭、双方取消和错误传播。这里如果没有明确关闭协议，很容易出现一边等待对方继续发，另一边已经退出的悬挂连接。

放到 AegisMesh 上，retry interceptor 里的 per-try context 可以限制单次尝试，trace metadata 可以把 attempt 信息传出去。更完整的设计还要保证外层 context deadline 被尊重：重试不能突破总预算，resolver 调用 Controller 也要有短 timeout，流式 `WatchPolicy` 要能在 SDK 关闭或服务实例下线时及时退出。

所以这题的结论是：RPC 的超时、取消和上下文传播要靠 context/deadline/metadata 贯穿全链路。成熟实现不只是客户端不等了，还要让服务端和下游尽快停止工作，并且把 trace、租户、灰度、幂等键等必要上下文用受控方式传播下去。

如果面试官继续深挖，可以按这条路线走：先讲 deadline 预算；再讲 cancel signal；接着讲服务端和下游传播；最后补充取消不是事务回滚，流式 RPC 还要处理半关闭和长期资源占用。

## 22. RPC 如何做流控和背压？

可以先这样答：

RPC 的流控和背压要分两层看：传输层防止发送方把接收方的连接和缓冲区打爆，应用层防止请求处理、队列、线程池、数据库和下游依赖被打爆。HTTP/2 和 gRPC 提供了基础流控，但它们不能替你决定业务什么时候限流、什么时候拒绝、什么时候降级。成熟系统通常两层都要做。

在传输层，HTTP/2 有 stream 级和 connection 级窗口。接收方处理完一部分数据后发送 window update，发送方才可以继续发。gRPC 流式调用会受这个机制影响：如果接收方不读，发送方的写操作最终会阻塞或变慢。这样可以避免一个快生产者无限堆积消息，把慢消费者内存打满。

不过传输层流控只管 bytes，不懂业务成本。一个 1 KB 请求可能触发复杂查询，一个 10 MB 响应可能只是静态对象。应用层还需要做并发限制、最大 in-flight、队列长度限制、令牌桶、租户配额、优先级、负载反馈、server pushback、熔断和降级。背压的目标不是让所有请求都排队等，而是在系统接近饱和时把压力反馈给上游。

流式 RPC 的背压更直接。server streaming 如果客户端读取慢，服务端不能把所有消息都预先生成进内存；应该边生成边发送，发送阻塞时暂停生产。client streaming 如果服务端处理慢，客户端写入也应该被放慢。bidi streaming 更容易写出死锁，比如双方都在等待对方先读，却都在阻塞写。官方文档也提醒，手动流控使用不当可能造成双方互相等待。

排队是背压设计里的关键。队列可以吸收短突发，但队列过长会把延迟放大，并让超时请求继续占资源。一个好的 RPC 框架或治理层通常会配合 deadline：如果请求剩余时间已经不足，就不要再进入长队列；如果后端已经高负载，就快速失败、返回可重试状态或带上重试建议，而不是无界排队。

客户端也要参与背压。比如限制每个服务、每个实例、每个租户的并发数；遇到 `RESOURCE_EXHAUSTED`、pushback metadata 或高延迟时降低发送速率；重试要有 budget，不能在服务端过载时继续放大流量。只做服务端限流，客户端无脑重试，最终会把系统推向雪崩。

放到 AegisMesh 上，`EndpointStatsSample` 里有 `inflight`、`latency_ewma_seconds`、`error_count`、`timeout_count` 等指标，可以作为负载均衡和过载判断的输入。SDK 侧如果结合这些遥测做并发控制、慢实例降权和重试预算，就能把背压从“服务端拒绝”提前到“客户端少发或换后端”。

所以这题的结论是：RPC 流控靠 HTTP/2/gRPC 控制字节流和 stream 读写节奏，背压靠应用层并发、队列、限流、deadline、负载反馈和重试预算控制业务压力。只依赖传输层流控不够，只依赖应用层限流也不够。

如果面试官继续深挖，可以按这条路线走：先区分传输层流控和应用层背压；再讲 HTTP/2 window；接着讲队列、并发和 deadline；最后说明客户端重试也必须纳入背压设计。

## 23. RPC 长连接如何处理连接断开、重连和负载再均衡？

可以先这样答：

RPC 长连接要同时处理三件事：连接断了怎么发现，发现后怎么重连，重连或实例变化后新流量怎么重新分布。gRPC 这类框架通常把连接封装成 channel 和 subchannel 状态机，连接会在 idle、connecting、ready、transient failure、shutdown 等状态之间变化。应用看到的是一次 RPC 成功或失败，框架内部要负责连接生命周期。

连接断开可以通过多种方式发现。最直接的是读写失败、TCP reset、TLS 关闭、HTTP/2 GOAWAY 或 stream reset；也可以靠 keepalive、健康检查、请求失败率和 resolver 更新间接发现。keepalive 不能太激进，否则会给网络设备和服务端制造额外压力；也不能完全没有，否则半开连接可能很久才暴露。

重连要有退避和 jitter。服务端大面积重启或网络抖动时，如果所有客户端同时立刻重连，会形成连接风暴，把刚恢复的服务再次打垮。正确做法是指数退避、随机抖动、最大退避上限、连接状态可观测，并且在服务端下线时尽量用 GOAWAY 或 drain 方式让客户端逐步迁移，而不是直接断所有连接。

负载再均衡主要影响新 RPC。已有的 unary 调用已经快结束，已有的 streaming 调用可能在原连接上持续很久。一般框架不会把一个正在进行的 stream 无缝迁移到另一个后端，因为 stream 内部状态在服务端。实例权重变化、扩容缩容、健康状态变化后，负载均衡器通常重新生成 picker，让新的 RPC 选择新的 subchannel；长流要靠服务端 drain、客户端重连或应用层 checkpoint 慢慢迁移。

连接级负载均衡容易失衡。比如客户端启动时选中了一个后端，然后在这条 HTTP/2 连接上长期跑很多 stream，后端扩容后旧连接不会自动把流量分给新实例。解决思路包括按 RPC 而不是按连接 pick 后端、限制连接寿命、服务端发送 drain 信号、客户端定期刷新连接、长流做重新订阅、LB 策略感知 subchannel 负载。

重连还要和重试分开。连接断开导致当前 RPC 失败，是否重试要看 method 幂等性、deadline、是否已经 committed、是否是 streaming。不能因为连接层自动恢复，就假设业务调用一定安全重放。连接恢复解决“后续还能不能发”，重试解决“这次失败要不要再试一次”，两者边界不同。

放到 AegisMesh 上，resolver 会周期性从 Controller 拉取实例列表，balancer 根据地址和属性选择后端。如果某个实例从 healthy 变成 unhealthy，新 RPC 应该尽快避开它；如果 Controller 推出新实例，SDK 应该逐步把新请求分过去。对于 `WatchPolicy` 这种长流，如果连接断了，客户端需要重新 watch，并处理从哪个版本或快照继续的问题。

所以这题的结论是：RPC 长连接通过连接状态机、keepalive、GOAWAY/drain、退避重连、resolver 更新和负载均衡 picker 来处理断开和再均衡。新调用可以较容易重分布，已有流式调用需要应用层恢复语义，不能指望框架无损迁移。

如果面试官继续深挖，可以按这条路线走：先讲断开检测；再讲退避重连和连接风暴；接着讲新 RPC re-pick 与老 stream 不迁移；最后把连接恢复和请求重试区分开。

## 24. RPC 客户端如何感知服务端实例列表变化？

可以先这样答：

RPC 客户端感知服务端实例变化，通常靠 resolver 或 service discovery 机制。客户端不应该把实例 IP 写死在代码里，而是连接一个逻辑服务名。resolver 负责把服务名解析成一组后端地址，并在地址集合变化时通知负载均衡器。负载均衡器再根据新地址、健康状态、权重和策略更新 picker，让后续 RPC 选择正确实例。

最简单的方式是 DNS。客户端周期性解析服务名，拿到多个 A/AAAA 记录或 SRV 记录。DNS 的好处是通用，成本低；缺点是 TTL、缓存、健康语义和权重表达有限，很多语言运行时或系统 resolver 对 DNS 更新也不够敏感。对强治理系统来说，DNS 常常只是基础能力。

更常见的是注册中心或控制面。服务实例启动时注册地址、版本、region、zone、权重、健康状态；客户端通过 watch 或 poll 获取变化。watch 的实时性好，但要处理断线重连和事件丢失；poll 简单稳定，但有刷新间隔，变化感知会慢一些。无论哪种方式，客户端都要有版本号、TTL、错误处理和缓存策略，不能因为一次控制面失败就清空所有后端。

客户端拿到实例列表后，还要过滤和解释。不是所有注册实例都应该接流量。状态为 starting、draining、unhealthy 的实例要谨慎处理；灰度实例可能只接一部分染色流量；跨地域实例可能作为 fallback；权重为 0 的实例可能只做预热或排空。resolver 负责发现地址，balancer 负责用策略消费地址，两者职责要分开。

实例变化还要做抖动控制。大规模发布时，实例列表会频繁变化，如果客户端每次变化都立即重建所有连接，可能造成连接风暴和流量震荡。更稳的做法是 debounce、随机刷新、渐进加入新实例、连接 drain、保留旧连接一段时间，并让失败恢复路径有 backoff。

放到 AegisMesh 上，`resolver.go` 会定期调用 Controller 的 `ListInstances`，把健康或可探测的实例转成 gRPC resolver 地址，并带上 weight、region、zone、status 等 attributes。这个设计说明客户端不是直接写死地址，而是通过控制面感知服务拓扑。它现在偏 poll 模式，所以要关注刷新周期、Controller 暂时不可用时的旧地址保留，以及实例状态变化到 picker 的传播延迟。

所以这题的结论是：RPC 客户端通过 DNS、注册中心、xDS、控制面 watch/poll 等机制感知实例列表变化，再由 resolver 更新地址集合，由 balancer 更新选择策略。真正难点在健康过滤、权重灰度、缓存容错、更新抖动和连接生命周期，而不只是“拿到一堆 IP”。

如果面试官继续深挖，可以按这条路线走：先讲逻辑服务名和 resolver；再比较 DNS、watch、poll；接着讲健康状态、权重、灰度；最后补充控制面失败和大规模发布时的抖动控制。

## 25. RPC 框架如何支持灰度发布和流量染色？

可以先这样答：

RPC 框架支持灰度发布和流量染色，核心是三件事：请求上要有可识别的标签，控制面要有按标签和比例路由的策略，客户端或代理要能按策略选择正确后端。灰度不是简单把新版本实例加进服务发现列表；如果没有染色和路由规则，流量会随机打到新旧版本，既不好验证，也不好回滚。

流量染色通常通过 metadata 或上下文完成。常见标签包括 tenant、user id hash、实验组、release channel、客户端版本、region、内部测试标记、trace id、请求来源。入口网关或 SDK 可以给请求打标签，下游服务要继续传播必要标签。这里要注意信任边界：外部用户不能随便传一个 header 就进入内部灰度版本，入口层需要校验、清洗和重写。

控制面需要表达灰度规则。比如某个方法 5% 流量到 v2；某个租户固定走 v2；内部员工走 canary；某个 region 不参与灰度；错误率超过阈值自动降权；新实例先 slow start。规则维度通常包括服务、方法、版本、权重、标签匹配、优先级、熔断条件和回滚开关。没有控制面，灰度规则会散落在业务代码里，很难统一治理。

执行层可以在客户端 SDK、网关、sidecar 或服务端路由器里。客户端 SDK 的好处是离负载均衡最近，可以直接选择目标实例，并把 attempt、trace 和路由结果记录下来；网关或 sidecar 的好处是对业务代码透明，适合入口治理。无论放哪一层，都要避免多层同时做灰度决策，导致实际比例和策略配置不一致。

灰度还要考虑粘性。按百分比随机分配容易让同一个用户一会儿走 v1，一会儿走 v2，影响会话和排查。常见做法是按 user id、tenant id 或 stable request key 做一致性 hash，让同一对象稳定落到同一版本。对无状态读请求可以更宽松，对订单、支付、工作流这类有状态业务要更谨慎。

观测性是灰度能否落地的关键。必须能按版本、灰度标签、租户、方法、实例维度查看 QPS、延迟、错误率、重试、超时和业务指标。否则灰度只是“把一部分用户暴露给新版本”，而不是可控实验。回滚也要可验证：策略下发后新请求是否真的不再进入灰度实例，长连接是否需要 drain，都要看得见。

放到 AegisMesh 上，策略快照和 method policy 可以扩展承载按方法的路由规则；resolver 地址里已有状态、权重、region、zone 等属性，适合做实例筛选；metadata 里可以传播 trace、attempt、租户或灰度标签；遥测可以按 upstream 和 status 反馈效果。这样灰度逻辑可以放在治理层，而不是写死在每个业务服务里。

所以这题的结论是：RPC 灰度发布靠 metadata 染色、控制面策略、客户端或代理路由、实例权重和可观测性闭环共同完成。它不是单纯发布 v2 实例，而是要能选中目标流量、稳定路由、监控效果、快速回滚并控制长连接影响。

如果面试官继续深挖，可以按这条路线走：先讲染色标签；再讲控制面规则；接着讲客户端、网关和 sidecar 的执行位置；最后强调粘性、信任边界和按版本观测。

## 26. 为什么 RPC 框架通常需要可观测性内建能力？

可以先这样答：

RPC 框架需要可观测性内建能力，是因为它站在每次远程调用的必经路径上，天然知道很多业务代码难以完整拿到的信息。比如服务名、方法名、调用方、目标实例、状态码、deadline、耗时、消息大小、重试次数、每次 attempt 的后端、连接状态、resolver 错误、负载均衡选择结果。应用只在 handler 里打日志，很难还原这些框架层事实。

最基础的是指标。RPC 框架应该能记录请求数、错误数、延迟分布、in-flight、超时数、重试数、每次 attempt 的结果、客户端和服务端状态码、消息大小、连接错误、解析失败和负载均衡状态。注意平均延迟不够，最好有直方图或分位数；只看最终调用结果也不够，重试前的失败 attempt 也要能看到，否则故障会被“最终成功”掩盖。

第二类是 trace。RPC 框架应该自动注入和提取 trace context，把客户端 span、服务端 span、重试 attempt、下游调用串起来。没有框架支持，团队很容易忘记传播 trace id，或者每个服务使用不同 header 名。内建 trace 能让一次用户请求穿过网关、RPC 服务、数据库、消息队列时保持同一条链路。

第三类是结构化日志和错误信息。RPC 框架可以在统一位置记录 method、peer、authority、tenant、trace id、status、elapsed、deadline remaining、attempt、upstream、error stage。这比业务代码里各写各的日志更容易检索和聚合。日志要脱敏，也要控制高频字段和高基数字段，不能把 user id、完整 token 或大 payload 随便打进去。

可观测性还要服务治理决策。负载均衡器需要后端延迟、错误率、in-flight 来做自适应选择；熔断器需要失败率和并发；重试策略需要知道哪些状态码可恢复；容量规划需要按服务、方法、租户看负载。RPC 框架如果不暴露这些数据，治理层只能盲猜。

内建不等于不可配置。不同业务对指标维度、采样率、日志字段、隐私要求不同。框架应该提供默认安全的观测能力，也允许接入 OpenTelemetry、Prometheus、日志系统和自定义 exporter。特别要防止高基数标签把指标系统打爆，比如把完整 URL、订单号、用户 ID 直接作为 metric label。

放到 AegisMesh 上，`EndpointStatsSample` 把请求数、错误数、超时数、重试数、inflight、延迟 EWMA、连接错误等数据作为治理输入；Go SDK 的 telemetry interceptor 记录 method、upstream、status、latency；trace writer 可以输出 attempt 级信息。这个方向说明 AegisMesh 不只是转发请求，而是把观测数据反哺负载均衡和策略调整。

所以这题的结论是：RPC 框架需要内建可观测性，因为它掌握调用路径上最关键的框架层事实。指标、trace、日志和负载反馈要从 RPC 层统一采集，才能支撑排障、SLO、重试、熔断、负载均衡和容量规划。

如果面试官继续深挖，可以按这条路线走：先讲业务代码拿不到框架层事实；再列 metrics、traces、logs；接着讲 attempt 级观测；最后提醒高基数标签和隐私脱敏。

## 27. RPC trace context 应该放在哪里传播？

可以先这样答：

RPC trace context 应该放在调用 metadata 或协议头里传播，而不是放在业务请求体里。trace context 描述的是这次调用链的观测上下文，不是业务领域对象。放在 metadata 里，客户端拦截器、服务端拦截器、网关、代理和 tracing SDK 都可以统一读取和注入，不需要理解每个 protobuf message 的字段。

在 HTTP/gRPC 体系里，trace context 通常映射到 headers 或 gRPC metadata。W3C Trace Context 定义了 `traceparent` 和 `tracestate` 这样的标准字段，用来传播 trace id、parent span id、采样标记和厂商扩展信息。gRPC 基于 HTTP/2，metadata 能承载这些头部语义。服务端收到后创建 server span，下游调用再把新的 span 上下文传播出去。

trace context 不应该只放在日志里。日志能帮助事后检索，但不能让下游服务自动知道自己属于哪条 trace。也不应该放在 URL query 或业务字段里，因为这样会污染接口、影响缓存、泄漏到不该出现的位置，还会让跨语言拦截器难以统一处理。正确位置是 transport metadata；应用内部可以把解析后的上下文放进语言里的 context 对象。

重试场景下，trace 语义要设计清楚。一次用户请求应该保持同一个 trace id；每次 RPC attempt 可以有自己的 child span 或 attempt 属性。这样排查时能看到整体调用经历了几次尝试、分别打到了哪些后端、哪次失败、哪次成功。如果每次重试都生成完全独立 trace，就很难解释延迟和放大；如果所有 attempt 都挤在一个 span 里，又看不清细节。

异步边界也要传播。RPC 调用链是同步场景，metadata 足够；如果服务端把任务投递到消息队列、后台任务或批处理系统，就需要把 trace context 放进消息 headers 或任务 metadata，而不是依赖线程局部变量。跨进程、跨线程、跨队列传播时，context 的载体会变，但语义应该保持一致。

安全和大小控制也要考虑。trace id 本身通常不敏感，但 baggage 里可能携带租户、实验、业务标签，不能无限扩散。跨信任边界时，要限制可传播字段，避免外部用户伪造 trace 或 baggage 影响内部路由和日志。trace metadata 也不应该变成任意业务标签的收纳箱。

放到 AegisMesh 上，`trace.go` 里用 `x-aegis-trace-id`、`x-aegis-span-id`、`x-aegis-attempt` 这类 metadata 传播调用上下文。这个位置是合理的，因为 retry interceptor 和 telemetry interceptor 都能读取它。未来如果要和 OpenTelemetry 或 W3C Trace Context 对齐，可以在这些内部字段和标准 `traceparent`/`tracestate` 之间做适配。

所以这题的结论是：RPC trace context 应该通过 metadata/header 传播，进入进程后再放到语言级 context 中；业务 body 不应该承载 trace。重试、异步任务、跨信任边界和 baggage 控制，是 trace context 设计里最容易被追问的几个点。

如果面试官继续深挖，可以按这条路线走：先说明 metadata 是正确载体；再讲 W3C Trace Context 和 gRPC metadata；接着讲重试 attempt 的 span 建模；最后补充异步消息和安全边界。

## 28. 如何避免 RPC 框架隐藏真实错误？

可以先这样答：

避免 RPC 框架隐藏真实错误，第一原则是不要把所有失败都包装成一个模糊的 `UNKNOWN`、`INTERNAL` 或“调用失败”。RPC 调用失败发生在很多层：DNS、服务发现、负载均衡、连接、TLS、HTTP/2、deadline、服务端拦截器、业务 handler、下游依赖。框架应该保留错误发生的阶段、原始 cause、RPC status、目标地址、attempt 信息和 trace id，让调用方和运维能判断到底是哪一层出了问题。

错误映射要有边界。服务端业务异常可以映射成稳定的 status code 和 error details；客户端网络错误可以映射成 `UNAVAILABLE` 或本地 transport error；deadline 到期映射成 `DEADLINE_EXCEEDED`；取消映射成 `CANCELLED`。但映射后不能丢掉上下文。比如“连接被拒绝”和“服务端返回 unavailable”都可能表现为 unavailable，但排障方向完全不同。

重试最容易隐藏错误。一次 RPC 最终成功，不代表中间没有失败；一次 RPC 最终失败，也不代表每次 attempt 都失败于同一原因。框架应该记录 attempt history，比如第 1 次 DNS 失败、第 2 次连接超时、第 3 次后端返回 `RESOURCE_EXHAUSTED`。对调用方可以返回简化错误，但日志、trace、metrics 至少要能看到被重试掩盖的失败。

拦截器也可能隐藏错误。比如 recovery interceptor 把 panic 全部转成 OK 响应里的业务失败，监控就看不见服务端 bug；错误处理中间件把所有业务错误都转成 `INTERNAL`，客户端就无法做正确分支；日志拦截器只打印 error message，不打印 status code、method 和 peer，也会增加排查成本。拦截器应该规范错误，而不是抹平差异。

服务端返回错误时要兼顾信息量和安全。外部 API 不应该暴露数据库连接串、SQL、栈信息、内部 IP；内部 RPC 也要避免泄漏密钥和 token。但这不意味着只能返回“系统错误”。可以返回稳定错误码、简短 message、request id、trace id，并在内部日志和 trace 中保留详细 cause。对调用方可见信息和内部诊断信息要分层。

可观测性字段要结构化。比起一段自然语言错误，`stage=connect`、`code=UNAVAILABLE`、`peer=10.0.1.5:50051`、`method=/shop.OrderService/CreateOrder`、`attempt=2`、`deadline_remaining_ms=37` 更容易检索和聚合。框架如果只抛一个字符串，最终会逼业务团队靠正则解析日志。

放到 AegisMesh 上，trace 和 telemetry 已经记录 upstream、status、latency、attempt 这类信息，`EndpointStatsSample` 也有 `connect_error`。这有助于区分后端业务错误、连接错误和重试导致的最终成功。后续如果要更强，可以把 resolver error、picker no-ready-subconn、TLS failure、per-attempt cause 都作为结构化字段输出，而不是只给一个总的 err。

所以这题的结论是：RPC 框架要做错误规范化，但不能抹掉错误来源。保留 stage、cause、status、peer、method、attempt、deadline 和 trace，是避免“调用失败”四个字吞掉真相的关键。

如果面试官继续深挖，可以按这条路线走：先说错误发生层次多；再讲状态码映射不能丢 cause；接着讲重试和拦截器如何隐藏错误；最后给出结构化错误字段和内外部信息分层。

## 29. RPC 调用失败时，如何区分 DNS、连接、TLS、路由、超时、服务端业务错误？

可以先这样答：

区分 RPC 失败原因，要按调用路径分层排查，而不是只看最后一个错误字符串。一次 RPC 从客户端发起到服务端返回，至少经过名称解析、实例选择、连接建立、TLS/ALPN、HTTP/2 stream、服务端拦截器、业务 handler、响应返回这些阶段。每一层失败的症状、指标和处理方式都不同。面试里可以直接给出一个排查顺序：resolver、connect、TLS、route/LB、deadline、server status、business code。

DNS 或服务发现失败，通常表现为服务名无法解析、注册中心不可用、resolver 返回空地址、地址过期、实例列表为空。日志里应该能看到 target、resolver scheme、服务名、lookup error、控制面请求错误。处理方向是检查 DNS、注册中心、Controller、权限、网络连通性和缓存策略。这个阶段还没有真正连到业务后端。

连接失败发生在拿到地址之后。典型错误包括 connection refused、no route to host、i/o timeout、connection reset、目标端口没监听、安全组或防火墙阻断、后端进程重启。排查要看 peer address、端口、subchannel 状态、连接耗时、失败实例分布。如果只有某些实例连接失败，可能是实例健康或网络分区；如果全部失败，可能是客户端网络、服务发现地址错误或上游整体不可用。

TLS 失败发生在连接建立之后、RPC 语义之前。典型原因包括证书过期、证书链不被信任、SAN 不匹配、SNI 错误、客户端证书缺失、mTLS 权限失败、ALPN 协商不到 h2。TLS 错误要保留 handshake error、server name、证书主题、到期时间和信任链信息，但不能把私钥或敏感证书内容打到日志里。

路由或负载均衡失败，通常是 resolver 有地址，但 balancer 没有可用 subchannel，或者策略找不到匹配版本、region、tenant、灰度 subset。症状可能是 no ready backend、picker 返回 transient failure、请求被网关返回 not found 或 unavailable。排查要看路由规则、metadata 标签、实例状态、权重、灰度策略、健康检查和 LB picker 输出。

超时失败要继续细分。连接超时说明连不上；TLS 超时说明握手卡住；response header timeout 说明服务端迟迟没有开始响应；RPC deadline exceeded 说明整体预算用完；流式 idle timeout 说明长期没有消息。只说“timeout”没有排障价值。最好记录 elapsed、deadline、remaining budget、attempt、阶段和是否已经收到 headers。

服务端业务错误和服务端系统错误也要分开。服务端返回 RPC status，比如 `INVALID_ARGUMENT`、`PERMISSION_DENIED`、`RESOURCE_EXHAUSTED`、`INTERNAL`，说明调用已经到达服务端并由服务端给出结果；响应体里的业务 code，比如库存不足、余额不足、状态不允许，说明业务逻辑正常执行但领域结果失败。它们和 DNS、连接、TLS 不是同一类问题。

放到 AegisMesh 上，可以通过 trace 和遥测字段把这些阶段拆开：resolver 从 Controller 拉实例，balancer 选 upstream，interceptor 记录 method、status、latency、attempt，指标里有 connect error、timeout、retry 和 inflight。排查时不要只看最终 status，要把每次 attempt 的 upstream、耗时和失败阶段串起来。

所以这题的结论是：区分 RPC 失败要按链路阶段建模，并把 stage、target、peer、TLS server name、route label、status code、business code、attempt、elapsed 和 trace id 结构化记录下来。没有这些字段，所有问题都会退化成“偶发调用失败”。

如果面试官继续深挖，可以按这条路线走：先画出 RPC 调用阶段；再逐层列 DNS、connect、TLS、route、timeout、server status、business code 的信号；最后强调结构化日志和 per-attempt trace 是排障前提。

## 30. RPC 框架如何处理大请求、大响应和消息压缩？

可以先这样答：

RPC 框架处理大请求、大响应和压缩，要先明确一个边界：RPC 适合传结构化消息，不适合把任意大对象都塞进一次调用。Protobuf 官方文档也提醒它不适合非常大的消息。大消息会带来内存峰值、序列化 CPU、GC 压力、网络拥塞、deadline 超时、重试放大和服务端排队。处理思路通常是限制大小、改成流式、分片、外部对象存储引用和选择性压缩。

第一步是设置消息大小上限。客户端和服务端都应该有 max request size、max response size、max metadata size、max decompressed size。没有上限，大请求可能直接把进程内存打爆；只有压缩后大小限制也不够，因为攻击者可以构造很小的压缩包，解压后变成巨大内容。大小限制要在网关、RPC 框架和业务层都一致，否则错误会出现在不可控的位置。

第二步是判断能不能流式化。大请求如果是文件、日志、指标批量、训练数据、导入数据，可以用 client streaming 分片上传；大响应如果是导出结果、大列表、日志、报表，可以用 server streaming 分页或分块返回。流式化的好处是降低内存峰值，让流控和背压生效；代价是重试、断点续传、顺序、去重和最终一致性要由应用协议定义清楚。

第三步是考虑外部存储引用。很多系统不会通过 RPC 直接传大文件，而是先把对象放到对象存储、文件服务或 blob store，再通过 RPC 传 object key、版本、hash、大小、权限和过期时间。这样 RPC 负责控制面和元数据，大对象走更适合的传输通道。这个模式也方便做断点续传、CDN、校验和权限控制。

消息压缩要看收益和成本。gRPC 支持按 channel、call 或 message 配置压缩，常见压缩对文本、JSON、重复字段收益明显，对已经压缩过的图片、视频、zip、protobuf 小消息收益有限。压缩会消耗 CPU，增加延迟，也可能让小消息变慢。生产上通常要按方法、消息大小、网络成本和 CPU 余量选择，而不是全局无脑打开。

压缩还有安全边界。包含敏感信息的响应如果和攻击者可控输入一起压缩，可能引入压缩侧信道风险。框架也可能允许服务端针对某条消息禁用压缩。另一个兼容性点是压缩算法协商：如果对端不支持某个压缩算法，可能返回类似 `UNIMPLEMENTED` 的错误。客户端和服务端要有明确的可用算法列表和回退策略。

大消息会影响重试。一个 100 MB 请求失败后自动重试，会再次占满带宽和服务端 CPU；如果服务端已经处理了前半段，重试还可能重复写入。大响应超时后重试也可能让后端重复生成昂贵结果。对于大请求大响应，重试要更保守，通常需要幂等键、分片编号、checkpoint、hash 校验和断点续传。

放到 AegisMesh 上，策略、遥测、用户和订单 demo 都应该保持小消息模型。如果未来要传大量日志或指标，优先考虑 client streaming、批量窗口、压缩阈值和 backpressure；如果要传大对象，最好只通过 RPC 传对象引用和校验信息。遥测里也应该记录请求大小、响应大小和压缩开关，否则很难解释延迟和 CPU 抖动。

所以这题的结论是：RPC 框架处理大请求和大响应，靠大小限制、流式传输、分片、外部存储引用、流控背压和谨慎压缩。压缩不是免费优化，大消息也不是单纯把上限调大就完事；它会影响内存、CPU、网络、deadline、重试和安全。

如果面试官继续深挖，可以按这条路线走：先说明 RPC 不适合无限大消息；再讲大小限制和解压后限制；接着讲 streaming、对象存储引用和重试；最后讨论压缩的收益、CPU 成本、兼容性和安全风险。
