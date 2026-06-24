# Kubernetes, Containers, and Cloud Native Fundamentals

本文对应《AegisMesh 技术面试问题库》里“Kubernetes、容器与云原生问题 / Kubernetes 和容器基础”这一大类。这个主题覆盖容器运行时、Kubernetes 工作负载、服务发现、网络、Service Mesh、探针、资源管理、扩缩容和访问控制；现有 `tech_interview_qna` 里没有一个专门承接这个大类的文件，所以本文件从题号 1 开始。

写法按面试口述组织：先给可以直接回答的结论，再把实现机制、工程边界和常见追问拆开。内容依据官方文档、规范和一手资料校准，但按要求不在文件内单列参考章节。后续如果继续补 Kubernetes、容器与云原生基础问题，直接沿着当前题号往下接。

## 1. 容器和虚拟机的区别是什么？

可以先这样答：虚拟机虚拟的是一整套硬件环境，容器隔离的是操作系统里的进程运行环境。虚拟机通常通过 Hypervisor 给每个实例提供虚拟 CPU、内存、磁盘和网卡，里面运行一个完整 Guest OS 和自己的内核；容器运行在宿主机内核之上，靠 namespace 做视图隔离，靠 cgroup 做资源限制和统计，再配合镜像、文件系统层、能力集、seccomp、AppArmor 或 SELinux 形成一个相对独立的进程环境。

所以二者的第一层差异是隔离边界。虚拟机的边界在虚拟硬件和 Guest Kernel 上，一个虚拟机内核出问题通常不会直接变成宿主机内核问题；容器共享宿主机内核，隔离更轻，启动更快，密度更高，但内核攻击面也更贴近宿主机。容器不是“轻量虚拟机”，它本质上还是宿主机上的进程，只是这个进程看到的 PID、网络、挂载点、主机名、用户、IPC 等视图被隔离了。

第二层差异是启动和交付方式。虚拟机镜像通常包含完整 OS，体积大，启动要经过系统引导、服务启动等流程。容器镜像更多打包应用、依赖库、运行时和少量用户态文件，不需要启动一个新内核，通常可以在秒级甚至更短时间内拉起。这也是容器适合弹性扩缩容、滚动发布和 CI/CD 的原因。

第三层差异是资源开销。虚拟机每个实例都有 Guest OS，内存、磁盘和补丁维护成本更高；容器共享宿主机内核和很多只读镜像层，同一台机器上可以放更多实例。但“容器更省资源”不是说它没有成本。容器仍然会消耗 CPU、内存、网络、磁盘 IO，镜像拉取、日志、Sidecar、探针和安全代理也会增加开销。

第四层差异是兼容性。虚拟机可以运行不同内核和不同操作系统，比如 Linux 宿主机上跑 Windows VM；Linux 容器必须依赖 Linux 内核语义，Windows 容器也依赖 Windows 内核语义。容器可以打包用户态依赖，但不能把一个完全不同的内核塞进去运行。面试里如果说“容器把操作系统也打包了”，这句话要改成“容器镜像打包了用户态运行环境，不打包宿主机内核”。

工程上，两者不是谁替代谁。虚拟机适合强隔离、多租户边界、异构 OS、传统系统迁移和云主机交付；容器适合应用标准化交付、快速部署、弹性扩缩容、微服务和平台调度。很多生产环境会把二者叠在一起：底层是云厂商 VM 或裸金属，上层跑 Kubernetes 和容器。这样既利用 VM 的资源边界，也利用容器的交付和调度效率。

## 2. Docker 镜像分层的原理是什么？

可以先这样答：Docker 镜像不是一个完整的大压缩包，而是一组只读文件系统层加上一份镜像配置。每一层通常对应构建过程中的一次文件系统变更，比如 `FROM` 带来的基础层、`RUN apt-get ...` 产生的新文件、`COPY` 放进去的应用代码。运行容器时，运行时会把这些只读层叠加起来，再在最上面加一个可写容器层。应用看到的是一个统一文件系统，底层实际由多层组成。

分层的核心价值是复用和缓存。同一个基础镜像层可以被多个镜像共享，同一个镜像层也可以被多个容器共享。构建镜像时，如果前面的 Dockerfile 指令和输入文件没有变化，构建器可以复用缓存层，不必重新执行所有步骤。拉取镜像时，节点上已有的层也不用重复下载。生产集群里大量 Pod 使用相同基础镜像时，这个复用非常明显。

读写语义通常靠 union filesystem 或 overlay 类机制实现。只读层保持不变，容器运行时写入文件会落到最上面的可写层。修改一个来自下层的文件时，会发生 copy-on-write：先把文件复制到可写层，再修改可写层里的副本。删除下层文件时，也常用 whiteout 这类标记让上层“遮住”下层文件，而不是把下层真的删掉。

这带来几个面试里容易追问的点。第一，镜像层是不可变的，所以已经写入旧层的大文件，即使后续 `RUN rm -rf` 删除了，镜像历史层里仍然可能存在，最终镜像不一定变小。第二，Dockerfile 指令顺序会影响缓存命中，通常把变化少的依赖安装放前面，把变化频繁的业务代码放后面。第三，多阶段构建可以在 builder 阶段留下编译器、依赖缓存和中间产物，只把最终二进制或静态资源复制到 runtime 阶段，从而减少最终镜像体积。

镜像分层也不是越多越好。层数过多会增加元数据和构建复杂度，层里包含包管理器缓存、临时文件、密钥或构建上下文垃圾，也会扩大镜像和安全扫描面。成熟做法是使用明确的基础镜像、固定版本、`.dockerignore`、多阶段构建、构建期密钥机制和镜像扫描，而不是只追求“能跑”。

面试里可以收束成一句：Docker 镜像分层把应用文件系统拆成可复用、可缓存、内容寻址的只读差异层；容器运行时在其上增加可写层。这个设计让构建、分发和启动更高效，但也要求我们理解缓存、copy-on-write、层泄漏和镜像体积控制。

## 3. namespace 和 cgroup 分别解决什么问题？

可以先这样答：namespace 解决“看见什么”的问题，cgroup 解决“能用多少”的问题。Linux namespace 把进程看到的系统资源视图隔离开，比如 PID namespace 让容器内进程看到自己的进程树，network namespace 让它拥有独立网卡、路由表和端口空间，mount namespace 让它看到独立挂载点，UTS namespace 隔离主机名，IPC namespace 隔离进程间通信对象，user namespace 可以把容器内 root 映射成宿主机上的非特权用户。cgroup 则用来限制和统计资源，例如 CPU、内存、pids、IO 等。

namespace 本身不限制资源。一个进程可以在自己的 PID namespace 里只看到几个进程，但如果没有 cgroup 限制，它仍然可能吃满宿主机 CPU 或内存。反过来，cgroup 本身也不提供视图隔离。一个进程可以被限制最多用 512Mi 内存，但如果没有 namespace，它仍然可能看到宿主机的进程、网络或挂载结构。容器运行时通常把两者一起用，才形成“像一个独立环境，又不会无限消耗宿主机资源”的效果。

容器还不只依赖 namespace 和 cgroup。安全边界还会用 Linux capabilities 降低 root 权限，用 seccomp 限制系统调用，用 AppArmor 或 SELinux 做强制访问控制，用只读根文件系统、drop capabilities、非 root 用户和 user namespace 减少逃逸风险。面试里如果只说“容器就是 namespace + cgroup”，作为入门可以，但生产安全上还不够。

放到 Kubernetes 里，Pod 和容器的资源请求、限制最终会落到运行时和内核的 cgroup 机制上。CPU request 会影响调度和 CPU share，CPU limit 常映射到 CFS quota；内存 limit 会成为内存上限，超过后可能触发 OOM。网络 namespace 则解释了为什么一个 Pod 内的多个容器共享同一个 Pod IP 和端口空间，而不同 Pod 默认有不同网络命名空间。

一个清晰的例子是：容器里运行 `ps` 只能看到自己的进程，这是 PID namespace 的效果；容器里只能使用某段 CPU 和内存，这是 cgroup 的效果；容器里 `localhost` 指向自己的网络栈，这是 network namespace 的效果；容器里 root 不等于宿主机完全 root，则可能来自 user namespace、capabilities 和安全策略。把这些拆开讲，面试官会更容易判断你是真的理解容器隔离，而不是只背概念。

## 4. Kubernetes 的核心组件有哪些？

可以先这样答：Kubernetes 组件可以分成控制面和节点面。控制面负责保存期望状态、做调度和协调；节点面负责在具体机器上运行 Pod，并把状态汇报回来。典型控制面组件包括 kube-apiserver、etcd、kube-scheduler、kube-controller-manager，以及在云环境中常见的 cloud-controller-manager。节点侧通常包括 kubelet、容器运行时、kube-proxy 和 CNI 网络插件。CoreDNS 常作为集群 DNS 插件部署，虽然不属于最小控制面进程，但几乎是生产集群的基础能力。

kube-apiserver 是所有控制操作的入口。kubectl、控制器、调度器、节点 kubelet、Admission Webhook 和各种 Operator 都通过 API Server 读写 Kubernetes API 对象。它负责认证、鉴权、准入控制、对象校验、版本转换和持久化入口。Kubernetes 的声明式模型本质上就是大量组件围绕 API Server 里的对象状态协作。

etcd 是一致性键值存储，保存 Kubernetes 的集群状态，例如 Pod、Deployment、Service、Secret、ConfigMap、Lease 等 API 对象。etcd 的健康直接影响控制面可用性。生产环境要重视 etcd 的备份、延迟、磁盘 IO、成员数量和证书管理，因为 API Server 最终要把状态落到 etcd。

kube-scheduler 负责给还没有绑定节点的 Pod 选择 Node。它会根据资源请求、节点标签、污点和容忍、亲和性、拓扑约束、端口、卷绑定、优先级等条件过滤和打分。调度器只做“把 Pod 绑定到某个节点”这一步，真正拉镜像、创建容器、挂载卷和执行探针的是节点上的 kubelet。

kube-controller-manager 运行一组控制器，例如 Deployment、ReplicaSet、Job、Node、EndpointSlice、ServiceAccount 等控制器。控制器的核心逻辑是 reconcile：观察当前状态和期望状态的差异，然后不断把当前状态推向期望状态。Deployment 想要 3 个副本，实际只有 2 个，相关控制器就会创建或驱动创建新的 Pod。

节点侧的 kubelet 负责接收分配到本节点的 PodSpec，调用容器运行时通过 CRI 创建和管理容器，执行健康探针，汇报 Pod 和 Node 状态。容器运行时可以是 containerd、CRI-O 等。kube-proxy 负责 Service 的虚拟 IP 到后端 endpoint 的转发规则，CNI 插件负责 Pod 网络，CoreDNS 负责集群内服务名解析。

如果面试官让你讲一次创建 Deployment 的链路，可以这样串起来：用户把 Deployment 提交给 API Server，状态写入 etcd；Deployment controller 创建 ReplicaSet，ReplicaSet controller 创建 Pod；scheduler 给 Pod 选择 Node；目标节点 kubelet 看到自己负责的 Pod，调用容器运行时拉镜像并创建容器，CNI 配网，kubelet 持续上报状态；Service 和 EndpointSlice controller 维护服务发现数据，CoreDNS 和 kube-proxy 让流量能找到后端。

## 5. Pod 为什么是 Kubernetes 的最小调度单位？

可以先这样答：因为 Kubernetes 调度的是一组必须共同运行、共同放置、共享部分运行环境的容器，而不是单个容器。Pod 里的容器共享同一个网络命名空间，通常共享同一个 Pod IP 和端口空间，也可以共享卷、部分生命周期和本地通信路径。调度器把 Pod 绑定到某个 Node，是为了保证这些容器一定在同一台机器上运行。

如果 Kubernetes 以容器为最小调度单位，很多常见模式会变得困难。比如业务容器旁边有日志采集 sidecar、代理 sidecar、配置刷新 sidecar，或者 init container 先准备文件再启动主容器。这些容器必须共享本地卷、localhost 和生命周期顺序。如果它们被调度到不同节点，localhost 通信、共享文件和启动依赖都失效。

Pod 也是资源声明和调度决策的边界。Pod 里每个容器可以声明 requests 和 limits，调度器会把整个 Pod 的资源需求合起来判断某个 Node 是否能容纳它。Pod 还携带 nodeSelector、affinity、tolerations、priority、topology spread、serviceAccount、securityContext、volumes 等调度和运行约束。调度一个 Pod，比调度一堆互相依赖的容器更清晰。

Pod 不是说一个 Pod 里应该塞很多业务服务。通常一个 Pod 放一个主业务容器，再加少量强耦合辅助容器。两个可以独立扩缩容、独立发布、独立失败恢复的业务服务，不应该放在同一个 Pod 里。否则它们的副本数、生命周期、资源隔离和故障半径都会绑在一起。

Pod 生命周期也解释了为什么它是调度单位而不是长期身份。Pod 被删除重建后，名字、UID、IP、所在节点都可能变化。Deployment 通过 ReplicaSet 维持副本数，StatefulSet 通过稳定身份和 PVC 解决有状态需求，Service 通过选择器和 EndpointSlice 给 Pod 提供稳定访问入口。面试里要把 Pod 理解成“调度和运行的原子单元”，而不是“应用实例的永久身份”。

## 6. Deployment、ReplicaSet、StatefulSet、DaemonSet、Job 分别适合什么场景？

可以先这样答：Deployment 适合无状态服务的声明式发布和回滚；ReplicaSet 负责维持一组 Pod 副本数量，通常由 Deployment 自动管理；StatefulSet 适合需要稳定网络身份、稳定存储和有序管理的有状态服务；DaemonSet 适合每个节点或部分节点都要运行一个副本的节点级组件；Job 适合一次性或有限次数完成的任务。

Deployment 是最常用的工作负载控制器。你声明镜像、环境变量、副本数、滚动更新策略和 selector，它会创建 ReplicaSet，再由 ReplicaSet 创建 Pod。Deployment 的价值不只是“拉起 N 个 Pod”，还包括滚动发布、暂停发布、回滚、进度观察和版本历史。Web 服务、API 服务、无状态 worker、网关层都很适合 Deployment。

ReplicaSet 更像底层副本控制器。它保证匹配 selector 的 Pod 数量达到期望值，但不直接提供高级发布语义。实际使用中很少手写 ReplicaSet，因为 Deployment 会为每个版本创建对应 ReplicaSet，并在滚动更新时调整新旧 ReplicaSet 的副本数。面试里可以说：ReplicaSet 是 Deployment 的执行层，不是日常发布入口。

StatefulSet 解决的是“副本有身份”的问题。它会给 Pod 稳定的序号和 DNS 名称，例如 `mysql-0`、`mysql-1`，并能配合 volumeClaimTemplates 给每个副本绑定稳定 PVC。它还支持有序创建、更新和删除。数据库、ZooKeeper、Kafka、etcd、需要稳定主从身份或分片身份的系统，通常比无状态 Deployment 更适合 StatefulSet。但 StatefulSet 不会自动解决数据复制、一致性和故障切换逻辑，这些仍然要靠应用或 Operator。

DaemonSet 适合节点级 agent。比如日志采集器、监控采集器、CNI 组件、存储插件、节点安全代理、kube-proxy 这类组件，经常需要每台节点都跑一个，或者每台带特定 label 的节点跑一个。节点加入集群时 DaemonSet 自动补 Pod，节点移除时对应 Pod 消失。

Job 适合有明确完成条件的任务，例如数据库迁移、离线批处理、一次性数据修复、镜像预热、CI 任务。Job 关心成功完成次数、失败重试、并行度和超时，而不是长期运行。周期性任务则通常用 CronJob，它会按时间创建 Job。

选择时可以按问题问自己：服务是否长期运行？是否无状态？副本是否需要稳定身份和独立存储？是否每个节点都必须有一个？是否跑完就退出？这个判断比背控制器名字更重要。

## 7. Service、Ingress、Gateway API 的区别是什么？

可以先这样答：Service 解决的是集群内服务发现和负载均衡入口，Ingress 解决的是 HTTP/HTTPS 入口路由，Gateway API 是比 Ingress 更明确、更可扩展、角色边界更清楚的流量入口 API。它们不是同一层东西，不能简单说谁替代谁。

Service 给一组 Pod 提供稳定访问入口。Pod 会重建、IP 会变化，Service 通过 selector 和 EndpointSlice 把逻辑服务名映射到后端 endpoint。ClusterIP Service 提供集群内虚拟 IP，DNS 名称也围绕 Service 工作。Service 主要表达 L3/L4 层的“访问这个服务的某个端口”，不擅长表达复杂 HTTP path、header、灰度、证书、多租户路由这类 L7 语义。

Ingress 是 Kubernetes 早期提供的 HTTP 入口资源。它通常表达 host、path 到后端 Service 的规则，实际转发由某个 Ingress Controller 实现，比如 Nginx Ingress、Traefik、HAProxy、云厂商控制器等。Ingress 本身比较薄，很多高级能力需要 annotation 扩展，不同控制器 annotation 差异很大，长期维护时容易出现可移植性和职责边界问题。

Gateway API 把入口流量拆成更清晰的对象：GatewayClass 表示一类网关实现，Gateway 表示具体监听入口，HTTPRoute、TCPRoute、TLSRoute 等 Route 资源表达路由规则。它的设计更适合平台团队和应用团队分工：平台团队管理 GatewayClass、Gateway、证书和入口基础设施，应用团队提交 Route 把自己的服务挂到网关上。它也比 Ingress 更自然地支持多协议、跨 namespace 引用策略、路由绑定和扩展能力。

一个典型链路是：外部用户访问云负载均衡器或网关地址，Gateway 或 Ingress Controller 接收流量，按 host/path/header 等规则路由到某个 Kubernetes Service，Service 再把流量转给后端 Pod。Service 是后端稳定入口，Ingress/Gateway API 是外部流量或南北向流量的 L7 入口管理。

面试里要避免两个误区。第一，不要把 Service 说成 API Gateway，它不理解业务路由和鉴权策略。第二，不要把 Gateway API 说成一定替代所有 Ingress；实际迁移取决于集群版本、控制器支持、团队职责和现有生态。更准确的说法是：Gateway API 是 Kubernetes 网络入口 API 的新一代方向，尤其适合复杂和多团队场景。

## 8. ClusterIP、NodePort、LoadBalancer、Headless Service 有什么区别？

可以先这样答：ClusterIP 是集群内虚拟 IP；NodePort 是在每个节点上打开一个端口，把外部流量转给 Service；LoadBalancer 是请求云厂商或外部控制器创建负载均衡器；Headless Service 不分配 ClusterIP，主要用于直接暴露后端 endpoint 或配合有状态服务做稳定发现。

ClusterIP 是默认类型。它给 Service 分配一个只在集群内可访问的虚拟 IP，集群内 Pod 可以通过 `service-name.namespace.svc` 或 ClusterIP 访问服务。kube-proxy 或替代数据面负责把访问 ClusterIP 的流量转发到后端 Pod。大多数内部服务都用 ClusterIP。

NodePort 会在每个节点上分配或使用一个固定端口。访问 `任意节点IP:NodePort` 时，流量会进入对应 Service，再转给后端 Pod。它简单直接，常用于测试、裸机场景或作为 LoadBalancer 的底层机制。但生产环境直接暴露 NodePort 要注意节点 IP 暴露、防火墙、端口范围、源地址保留和负载均衡策略。

LoadBalancer 在云环境里最常见。你创建 `type: LoadBalancer` 的 Service 后，云控制器或负载均衡控制器会创建外部负载均衡器，把公网或内网 LB 地址接到 Service 上。它适合把 TCP/UDP 服务暴露到集群外，但成本、健康检查、源地址保留、跨可用区流量和云厂商特性都要看具体实现。

Headless Service 设置 `clusterIP: None`，不提供一个统一虚拟 IP。DNS 查询时通常返回后端 Pod IP 或与 StatefulSet 结合返回稳定 Pod DNS。它适合客户端自己做负载均衡、服务发现系统需要拿到完整后端列表，或者 StatefulSet 需要 `pod-ordinal.service` 这种稳定域名。Headless Service 不等于没有服务治理，它只是把“统一 VIP 转发”换成了“暴露后端集合”。

选择时可以这样判断：只给集群内服务调用，用 ClusterIP；临时从外部访问或裸机简单暴露，用 NodePort；需要云负载均衡入口，用 LoadBalancer；需要直接感知每个后端或配合 StatefulSet，用 Headless Service。复杂 HTTP 入口通常不要直接靠这些 Service 类型完成，而是上面再接 Ingress、Gateway API 或 Service Mesh Gateway。

## 9. kube-proxy 的 iptables 模式和 IPVS 模式有什么区别？

可以先这样答：iptables 模式主要通过生成大量 netfilter 规则做 Service VIP 到后端 Pod 的 DNAT；IPVS 模式使用 Linux 内核里的 IP Virtual Server 作为四层负载均衡器，把 Service 建成 virtual service，把 Pod 建成 real server。两者都消费 Service 和 EndpointSlice 信息，也都依赖 Linux 网络栈，但转发模型、调度能力和大规模下的维护成本不同。

iptables 模式的优点是依赖少、行为成熟、几乎所有 Linux 节点都能用。kube-proxy 会为 Service 和 endpoint 生成规则，流量命中 ClusterIP、NodePort 等规则后被 DNAT 到某个后端。它的问题是规则规模会随着 Service 和 endpoint 增长而变大，规则更新和匹配成本在大集群里会更明显。iptables 规则本身也不是一个专门的负载均衡抽象，调度算法比较有限。

IPVS 模式把 Service 映射成内核 IPVS virtual server，把 endpoint 映射成 real server。IPVS 原本就是四层负载均衡功能，支持 round-robin、least connection、源地址哈希等调度算法，查找和转发模型更接近负载均衡器。大规模 Service 和 endpoint 场景下，IPVS 往往比大量 iptables 规则更适合表达“一个 VIP 后面挂一组后端”。

但 IPVS 不是自动更好。它要求节点内核加载 IPVS 相关模块，仍然要和 iptables、ipset、conntrack、CNI、NodePort、externalTrafficPolicy 等机制配合。某些场景的排障也会更复杂，因为你要同时看 `ipvsadm`、iptables、路由、conntrack 和 kube-proxy 日志。另一方面，Kubernetes 网络数据面也在演进，部分环境会使用 nftables、eBPF 或云厂商自己的 Service 实现，不一定只在 iptables 和 IPVS 里二选一。

面试里可以把边界讲清楚：iptables 模式是用通用包过滤/转发表达 Service 转发，简单通用；IPVS 模式是用内核四层负载均衡表达 Service 转发，调度能力和大规模表现更强。真正的性能还取决于 Service 数量、endpoint churn、conntrack 压力、CNI 实现、内核版本和运维可观测性。

## 10. Endpoint 和 EndpointSlice 的区别是什么？

可以先这样答：传统 Endpoints API 用一个对象保存某个 Service 背后的所有后端地址；EndpointSlice 把后端地址分片成多个对象，并且能表达更多状态和拓扑信息。EndpointSlice 是 Kubernetes 服务发现规模化后的更合适模型，传统 Endpoints 更像早期兼容接口。

旧 Endpoints 对象的问题是“大对象”。一个 Service 如果有几千个后端，所有地址都塞进同一个 Endpoints 对象，会让 API Server、watch、控制器和客户端都承受更大的序列化、传输和更新成本。任何一个 endpoint 变化，都可能导致整个大对象被更新。更严重的是，旧 Endpoints 对象在超大后端数量下存在截断风险，依赖它的组件可能看不到完整后端集合。

EndpointSlice 把一个 Service 的 endpoint 分成多个 slice，默认每个 slice 大约承载一部分 endpoint。某个 Pod 上下线时，通常只需要更新相关 slice，而不是重写一个巨大的对象。这样 watch 扇出和对象大小都更可控，也更适合大规模集群。

EndpointSlice 还表达了更多语义。它有地址类型，可以区分 IPv4、IPv6、FQDN；可以携带 topology 信息；endpoint 条件里有 ready、serving、terminating 等状态，用来区分常规可接新流量、正在终止但可能仍在服务已有连接等情况。这对优雅下线、拓扑感知路由和多网络场景都很重要。

如果面试官问为什么还会看到 Endpoints，可以回答：生态里仍有一些旧组件或兼容逻辑会读 Endpoints，但新实现应优先理解 EndpointSlice。Kubernetes 的 Service 发现链路已经围绕 EndpointSlice 扩展，特别是大规模服务、双栈、终止状态和拓扑能力。把 EndpointSlice 说成“Endpoints 的新名字”不准确，它更重要的是分片、扩展性和状态表达能力。

## 11. CoreDNS 在 Kubernetes 中负责什么？

可以先这样答：CoreDNS 负责集群内 DNS 解析，最常见的是把 Service 名称解析成 Service 的 ClusterIP，或者在 Headless Service 场景下解析成后端 endpoint。Pod 访问 `service.namespace.svc.cluster.local` 这类名字时，通常就是 CoreDNS 根据 Kubernetes API 里的 Service 和 EndpointSlice 信息返回结果。

CoreDNS 运行在集群里，一般以 Deployment 形式部署，并通过 kube-dns 这个 Service 暴露给集群内 Pod。每个 Pod 的 `/etc/resolv.conf` 会指向集群 DNS 服务地址，并带上搜索域。这样应用可以用短名字访问同 namespace 的 Service，也可以用完整 DNS 名称跨 namespace 访问。

CoreDNS 的 Kubernetes 插件会 watch Service、Pod、EndpointSlice 等对象，生成对应 DNS 记录。普通 ClusterIP Service 通常返回 ClusterIP；Headless Service 返回 endpoint 地址；命名端口还可以通过 SRV 记录表达。对于集群外域名，CoreDNS 通常会转发到上游 DNS。

CoreDNS 不是负载均衡器本身。普通 ClusterIP Service 返回的是虚拟 IP，后续转发由 kube-proxy、eBPF 数据面或其他 Service 实现完成。Headless Service 返回多个后端地址时，客户端可能自己做选择，但 DNS 轮询也不是完整的服务治理。超时、重试、熔断、连接池、灰度和 mTLS 仍然要由客户端、网关或 Service Mesh 处理。

生产排障里，CoreDNS 常见问题包括：Pod DNS 配置错误、CoreDNS 副本不足、上游 DNS 慢、某些插件配置错误、NodeLocal DNSCache 异常、Service 或 EndpointSlice 不存在、namespace 写错、搜索域导致解析路径和预期不同。面试里讲 CoreDNS 时，最好把它定位成“服务发现名称解析层”，不要夸大成“负责 Kubernetes 全部网络”。

## 12. CNI 负责什么？常见 CNI 有哪些？

可以先这样答：CNI 负责给容器或 Pod 配网络。更具体一点，容器运行时创建出网络命名空间后，会调用 CNI 插件把网卡、IP、路由、DNS 相关配置和必要的宿主机侧网络连接设置好；Pod 删除时，再调用插件清理网络资源。在 Kubernetes 里，CNI 让每个 Pod 拿到可通信的 Pod IP，并让 Pod 之间按集群网络模型互通。

CNI 的重点是接口规范，而不是某一个具体网络方案。规范定义了运行时如何调用插件、传什么参数、插件如何返回结果、ADD/DEL/CHECK 等操作语义。具体如何实现 Pod 网络，由插件决定。插件可以用 bridge、host-local IPAM、overlay、BGP 路由、eBPF、云厂商 ENI、隧道或直接路由等方式。

常见 CNI 包括 Calico、Cilium、Flannel、Weave Net、Antrea、kube-router、Canal，以及云厂商提供的 VPC CNI、Azure CNI、GKE Dataplane 等。Flannel 常见于简单 overlay 网络；Calico 常见于 BGP 路由和 NetworkPolicy；Cilium 依赖 eBPF，除了网络连通还提供策略、可观测性和服务负载均衡能力；云厂商 CNI 通常把 Pod IP 接入 VPC 网络。

CNI 不等于 kube-proxy，也不等于 Service Mesh。CNI 主要处理 Pod 网络连接、IP 分配和网络策略底座；kube-proxy 或替代数据面处理 Service VIP 到 endpoint 的转发；Service Mesh 处理 L7 流量治理、mTLS、重试、熔断和可观测性。当然，一些现代 CNI 会覆盖更多能力，比如 eBPF Service 实现或 L7 policy，但概念上仍要分层。

排障时可以沿着这条线查：Pod 是否拿到 IP，宿主机上 veth 或网络设备是否存在，路由表是否正确，跨节点是否能通，NetworkPolicy 是否拦截，MTU 是否被 overlay 影响，DNS 是否另有问题。把 CNI 说成“负责所有网络问题”太粗，准确说法是：CNI 负责把 Pod 接入集群网络，后续服务发现、负载均衡和应用层治理由其他组件继续完成。

## 13. 容器网络中的 veth、bridge、overlay、VXLAN 分别是什么？

可以先这样答：veth 是连接两个网络命名空间的一对虚拟网卡，bridge 是二层交换设备，overlay 是在现有三层网络上再构造一层虚拟网络，VXLAN 是常见的 overlay 封装协议。它们经常一起出现在容器跨节点通信链路里。

veth pair 可以理解成一根虚拟网线的两端。一端放进 Pod 的 network namespace，成为 Pod 里看到的 `eth0`；另一端留在宿主机 namespace，接到 bridge、路由、eBPF 程序或其他数据面上。Pod 发出的包先从自己的 veth 出来，再进入宿主机网络栈。

bridge 是 Linux 里的二层交换机。多个 veth 宿主机端接到同一个 bridge 后，同一节点上的容器可以像接在同一个二层交换机上一样通信。Docker 默认 bridge 网络就是这个思路。Kubernetes CNI 也可以使用 bridge，但跨节点通信还要解决不同宿主机之间的路由或封装问题。

overlay 网络解决的是“底层网络不认识 Pod 网段也要让 Pod 跨节点互通”。它会把 Pod 到 Pod 的原始包封装进宿主机到宿主机的外层包里，在底层网络只看到节点 IP 之间通信。到达目标节点后再解封装，把内层包交给目标 Pod。这样底层交换机和路由器不需要知道每个 Pod IP 的位置。

VXLAN 是 overlay 的一种常见实现。它把二层帧封装到 UDP 包里，外层用宿主机 IP 做传输，内层保留虚拟网络语义，并通过 VNI 区分虚拟网络。很多容器网络方案使用 VXLAN 或类似隧道技术。它的代价是封装会增加额外头部，影响 MTU；排障时也要同时看内层 Pod IP 和外层 Node IP。

不是所有 CNI 都走 bridge + VXLAN。有的用 BGP 或直接路由，把 Pod 网段发布给网络设备或节点；有的用云厂商 VPC 原生 IP；有的用 eBPF 在内核更早路径处理转发和策略。面试里把这些概念串起来即可：veth 把 Pod 接到宿主机，bridge 或路由处理节点内转发，overlay/VXLAN 处理底层网络不感知 Pod 网段时的跨节点通信。

## 14. Service Mesh 解决什么问题？

可以先这样答：Service Mesh 解决的是服务到服务通信中的通用治理问题，包括服务发现、负载均衡、mTLS、身份认证、授权、超时、重试、熔断、流量切分、灰度、故障注入、指标、日志和分布式追踪。它把这些能力从业务代码里抽出来，放到数据面代理和控制面配置里统一管理。

没有 Service Mesh 时，这些能力通常散落在各语言 SDK、业务框架、网关、客户端库和应用代码里。一个公司如果有 Go、Java、Python、Node.js 多套服务，要保证每种语言的重试、超时、熔断、指标标签、证书轮换和路由规则一致，成本很高。Service Mesh 的思路是让流量经过统一代理，由控制面下发策略，业务服务尽量少改代码。

Service Mesh 的典型价值在东西向流量，也就是服务之间的调用。入口网关解决用户进集群的问题，Mesh 更关注服务 A 调服务 B 时如何安全、可观测、可治理。比如把 5% 流量切到 v2，给某个服务启用 mTLS，对某个路径设置超时和重试，按服务身份做授权，统一采集请求延迟和错误码。

它也有明显代价。第一，链路多了一层代理或数据面，带来资源开销和尾延迟。第二，控制面、证书、注入、xDS 配置、流量劫持和代理版本都成了新的运维对象。第三，排障从“看应用日志”变成“同时看应用、代理、控制面、证书、路由、策略和底层网络”。第四，错误配置传播很快，坏的路由或授权规则可能影响大量服务。

所以 Service Mesh 不是微服务的必选项。服务规模小、语言栈统一、SDK 治理已经成熟时，上 Mesh 可能得不偿失；多语言、多团队、多集群、强安全和强可观测需求明显时，Mesh 的价值更大。面试里可以这样总结：Service Mesh 把服务间通信治理平台化，但它不是业务逻辑平台，也不能替代良好的超时、幂等、容量规划和故障隔离设计。

## 15. sidecar 模式的优缺点是什么？

可以先这样答：sidecar 模式的优点是把辅助能力和业务容器放在同一个 Pod 里，共享网络和生命周期，业务代码少改甚至不改就能获得代理、日志、配置、证书或安全能力。缺点是每个 Pod 都多一个或多个进程，资源开销、启动顺序、终止顺序、升级和排障都会变复杂。

Service Mesh 里的 sidecar 代理是最典型例子。业务容器发出的流量先被本地 Envoy 接管，入站流量也先经过 Envoy。这样代理可以使用 localhost 或 Pod 网络拦截流量，拿到服务身份、证书、路由配置、重试策略和指标采集能力。业务进程不用每种语言都实现同一套治理逻辑。

优点第一是语言无关。Go、Java、Python、Node.js 服务都可以通过同一个 sidecar 获得一致流量治理。第二是部署耦合清晰。sidecar 跟业务 Pod 同生共死，可以访问同一组本地卷、同一个网络命名空间和相同的 Pod 元数据。第三是渐进接入。很多能力可以通过注入 sidecar 实现，不必重构业务服务。

缺点也要讲实。每个 Pod 一个代理，会放大 CPU、内存和连接数成本。服务数和副本数越多，这个成本越明显。流量多一跳本地代理，也会增加延迟和故障点。代理配置错误、证书过期、xDS 下发异常、iptables 重定向问题，都可能让业务容器本身没问题但流量失败。

生命周期是另一个坑。主容器启动前，sidecar 是否已经准备好？Pod 终止时，业务容器还在处理请求，sidecar 是否已经退出？日志 sidecar 或代理 sidecar 如果不正确处理 readiness、preStop、drain 和 SIGTERM，就会造成启动时请求失败或下线时连接被切断。Kubernetes 对 sidecar 容器语义也在持续完善，但工程上仍然要显式设计顺序和探针。

还有升级成本。sidecar 版本通常跟每个工作负载 Pod 绑定，升级代理意味着大量 Pod 滚动。代理安全漏洞、配置 schema 变化、控制面兼容性和注入模板都要管理。也正因为这些代价，Istio 这类项目才会发展 ambient mode、ztunnel、waypoint 等模式，尝试减少每个 Pod 都注入完整 L7 sidecar 的成本。

所以这题的结论是：sidecar 适合把强耦合的辅助能力贴近业务进程，尤其适合代理、日志、证书和配置类能力；它的问题是资源、延迟、生命周期和排障复杂度。面试里不要只说“解耦”，要能讲出每 Pod 成本和上下线顺序。

## 16. Envoy 在 Service Mesh 中通常承担什么角色？

可以先这样答：Envoy 通常承担 Service Mesh 的数据面代理角色。它部署在业务 Pod 旁边作为 sidecar，或者部署成 ingress gateway、egress gateway、waypoint proxy 等网关形式，负责真正处理进出服务的流量。控制面负责生成配置，Envoy 负责按配置转发、加密、限流、重试、熔断、观测和执行策略。

Envoy 的核心模型可以按 listener、filter chain、route、cluster、endpoint 理解。下游连接进入 listener，经过网络过滤器或 HTTP connection manager，路由规则决定请求发往哪个 upstream cluster，cluster 再根据负载均衡和健康状态选择 endpoint。这个模型让 Envoy 同时能处理 L4 TCP 代理和 L7 HTTP/gRPC 代理。

在 Mesh 中，Envoy 经常负责 mTLS。它可以为服务间连接建立双向 TLS，验证对端服务身份，执行基于身份的授权策略。它也能处理 HTTP/2、gRPC、WebSocket、TCP 等流量，在 L7 场景下根据 host、path、header、method 做路由，在 L4 场景下做 TCP 转发和连接级治理。

可靠性能力也在 Envoy 里落地。比如连接池、主动健康检查、异常实例摘除、重试、超时、熔断、限流、故障注入、流量镜像和按百分比切流。这些能力如果放在每个业务语言 SDK 里，很难保持一致；放在 Envoy 数据面里，控制面可以统一下发策略。

可观测性也是 Envoy 的重要角色。它能输出请求量、延迟、响应码、重试次数、上游连接状态、TLS 信息、访问日志和 tracing header。Istio 等 Mesh 会基于 Envoy 指标构建服务拓扑、错误率、P99 延迟和调用关系。很多时候排查服务间调用问题，第一手证据就在 Envoy 的 access log、stats、clusters 和 config dump 里。

但 Envoy 不是 Service Mesh 的全部。它不负责保存全局期望状态，不负责把 Kubernetes Service、DestinationRule、VirtualService、AuthorizationPolicy 等对象翻译成配置，也不负责证书根信任和策略管理。这些属于控制面。面试里可以收束成一句：Envoy 是执行流量治理的数据面，xDS/Istio 控制面是生成和分发治理意图的控制面。

## 17. Istio 的控制面和数据面分别是什么？

可以先这样答：Istio 的数据面由处理业务流量的代理组成，sidecar 模式下主要是随业务 Pod 部署的 Envoy，以及 ingress/egress gateway 里的 Envoy；控制面主要是 istiod，它负责服务发现、配置转换、xDS 下发、证书和安全相关控制逻辑。数据面转发流量，控制面管理数据面配置。

sidecar 模式下，每个接入 Mesh 的工作负载旁边都有 Envoy。入站和出站流量经过 Envoy，Envoy 根据控制面下发的 listener、route、cluster、endpoint、secret 等配置执行路由、mTLS、授权、重试、熔断和指标采集。业务容器不直接感知很多治理细节，但流量路径已经被代理接管。

istiod 是控制面的核心。它从 Kubernetes API、Istio CRD 和服务发现数据中读取信息，把 VirtualService、DestinationRule、Gateway、PeerAuthentication、AuthorizationPolicy 等配置转换成 Envoy 能理解的 xDS 资源，再推送给数据面。它还承担证书签发和身份相关职责，让工作负载可以建立 mTLS 信任。

控制面不应该在每个业务请求的数据路径上。正常情况下，一个服务调用另一个服务，请求经过本地 Envoy、网络和对端 Envoy，不会每次都问 istiod。istiod 挂掉时，已经拿到配置的数据面通常可以继续按最后有效配置转发一段时间；但新配置、证书轮换、服务发现更新和新 Pod 接入会受影响。

Istio 现在也有 ambient mode。ambient 不再给每个 Pod 注入完整 sidecar，而是用 ztunnel 提供 L4 安全通道，用 waypoint 处理需要 L7 策略的流量。面试中如果题目没有特别强调 ambient，可以先按经典 sidecar 模式回答，再补一句 Istio 数据面形态正在扩展，但控制面/数据面的边界仍然是：控制面生成策略和配置，数据面执行流量处理。

容易混淆的一点是：Istio 控制面不是 Kubernetes 控制面。Kubernetes 控制面负责整个集群 API、调度和对象状态；Istio 控制面是建立在 Kubernetes 之上的服务网格控制面，主要管理 Mesh 流量、安全和观测配置。二者协作，但职责不同。

## 18. Kubernetes 的 readinessProbe、livenessProbe、startupProbe 有什么区别？

可以先这样答：readinessProbe 判断容器是否可以接收业务流量；livenessProbe 判断容器是否已经卡死到需要重启；startupProbe 判断慢启动应用是否已经完成启动，并在成功前延后 liveness/readiness 的其他探测。三者都叫探针，但失败后的动作不同。

readinessProbe 失败时，Pod 通常不会被重启，而是被标记为 not ready。对应 Service 的 endpoint 会被更新，常规流量不应该再打到这个 Pod。它适合表达“进程还活着，但暂时不能服务新请求”，比如依赖未就绪、连接池未预热、缓存加载中、队列积压过高、正在优雅下线。

livenessProbe 失败时，kubelet 会按容器重启策略重启容器。它适合检测进程内部死锁、事件循环卡死、无法自恢复的状态。livenessProbe 不适合检测短暂下游故障。比如数据库暂时不可用，如果 liveness 失败导致所有应用容器被重启，可能把一个外部依赖抖动放大成集群内重启风暴。

startupProbe 解决慢启动应用的问题。有些应用启动时要加载大模型、预热缓存、跑迁移或建立大量连接，启动过程可能超过 livenessProbe 的正常阈值。如果没有 startupProbe，livenessProbe 会在应用还没启动完时把它杀掉，形成循环重启。配置 startupProbe 后，在它成功之前，liveness 和 readiness 的普通失败不会过早杀容器。

探针形式可以是 HTTP、TCP、gRPC 或 exec。HTTP 探针适合应用暴露 `/healthz`、`/readyz`；TCP 只能说明端口能建立连接，不代表业务可用；exec 能做进程内检查，但开销和权限要控制；gRPC 探针适合 gRPC 服务。探针要轻量、稳定、语义清楚，不要把昂贵 SQL、远程依赖全量检查放进高频探针里。

面试里可以这样总结：startupProbe 保护慢启动，readinessProbe 控制接流量，livenessProbe 触发自愈重启。大多数线上事故不是因为没有探针，而是探针语义写错，把“暂时不能接新流量”误写成“必须重启进程”。

## 19. 为什么 readinessProbe 比 livenessProbe 更适合控制流量进入？

可以先这样答：因为 readinessProbe 的失败动作是从服务发现和负载均衡候选里摘掉 Pod，而 livenessProbe 的失败动作是重启容器。控制流量进入需要的是“暂时不要把新请求打给我”，不是“把我杀掉重启”。

readinessProbe 直接影响 Pod 的 Ready 条件和 EndpointSlice 里的 ready 状态。一个 Pod readiness 失败后，Service 后端列表会更新，kube-proxy、Ingress、Gateway、Service Mesh 或其他数据面就不应继续把常规新流量转给它。这个语义适合发布预热、依赖未完成、缓存未加载、队列过载、限流保护和优雅下线。

livenessProbe 更像最后兜底。它用来处理应用已经坏到无法自恢复的情况，比如进程死锁、主循环挂死、健康端口永久无响应。重启是很重的动作，会断开连接、丢失内存状态、触发冷启动，还可能让刚恢复的服务再次被流量打爆。如果把下游数据库慢、缓存 miss、临时过载都写进 liveness，容器会不断重启，系统更不稳定。

一个常见场景是发布新版本。新 Pod 刚启动时，进程已经能响应 HTTP，但依赖连接池、JIT、缓存和配置还没准备好。正确做法是 readiness 先失败，等预热完成后再 ready。用 liveness 控制这个过程会导致新 Pod 被 kubelet 重启，发布永远无法稳定完成。

另一个场景是优雅下线。应用收到终止信号或准备下线时，应先让 readiness 失败，让控制面把它从常规流量里摘掉，然后继续处理已有连接。此时进程是健康的，不应该被 liveness 杀掉。真正超过 termination grace period 还不退出时，才由 kubelet 强制终止。

所以这题的结论是：readiness 是流量开关，liveness 是重启开关。入口流量控制要尽量用 readiness、drain、连接关闭和负载均衡权重来做；liveness 只用于进程级不可恢复故障，不能拿来表达业务容量和依赖健康的每一次波动。

## 20. Pod 优雅终止流程是什么？

可以先这样答：Pod 删除时，API Server 会给 Pod 设置 deletionTimestamp 和 grace period；控制面和 EndpointSlice 会把它标记为正在终止，常规新流量应逐步停止进入；节点上的 kubelet 执行 preStop hook，然后向容器发送 SIGTERM；应用在宽限期内停止接新请求、处理已有请求并退出；如果超时仍未退出，kubelet 发送 SIGKILL 强制结束。

第一步是删除意图被记录下来。用户执行 `kubectl delete pod`、Deployment 滚动更新、节点 drain 或控制器缩容时，Pod 不会立刻从世界上消失，而是进入 terminating 状态。这个状态会被 API 对象反映出来，其他控制器和数据面可以观察到。

第二步是服务发现侧停止常规新流量。对于匹配 Service 的 Pod，EndpointSlice 会反映 endpoint 的终止状态，ready 通常不再表示它可以接新请求。不同数据面传播这个变化需要时间，所以应用不能假设 readiness 失败后一瞬间就没有流量。生产里常配合 preStop、drain、sleep、连接关闭、Envoy drain 或网关摘除等待，让上游有时间更新。

第三步是节点本地终止。kubelet 如果看到容器有 preStop hook，会先执行它，但 preStop 的耗时也算在 termination grace period 里。然后 kubelet 通常向容器主进程发送 SIGTERM。应用要正确处理 SIGTERM：停止监听或返回不 ready，拒绝新任务，等待正在处理的请求完成，提交 offset 或 flush 日志，关闭连接池，再退出。

第四步是强制兜底。如果容器在 grace period 内没有退出，kubelet 会发送 SIGKILL。SIGKILL 不能被应用捕获，意味着清理逻辑不会再执行。所以 grace period 要根据真实请求最长耗时、消息处理时长、连接 drain 时间设置，不能随便设成 1 秒，也不能无限大导致发布卡住。

有 sidecar 时还要考虑退出顺序。比如 Envoy 先退出，业务容器还在处理请求，连接可能被切断；日志 sidecar 先退出，最后日志可能丢。成熟做法是让业务先停止接流量，再等代理 drain，最后退出辅助容器；或者使用平台提供的 sidecar 生命周期语义和网格 drain 配置。

面试里可以把优雅终止概括成：先从服务发现摘除，再本地通知进程，再等待已有工作完成，最后超时强杀。真正难点不在 Kubernetes 会不会发 SIGTERM，而在应用、代理、Service、EndpointSlice、Ingress/Gateway 和外部 LB 的传播延迟是否都被算进去了。

## 21. requests 和 limits 的区别是什么？

可以先这样答：requests 是调度和资源保障的依据，limits 是运行时资源上限。调度器看 requests 判断某个 Pod 能不能放到某个节点；节点运行时通过 cgroup 等机制尽量让容器不超过 limits。requests 影响“能不能被调度”和“资源竞争时至少该有多少权重”，limits 影响“最多能用多少”。

CPU request 表示容器希望获得的 CPU 份额。调度器会把同一个 Pod 内各容器 CPU request 加总，和节点可分配 CPU 比较。运行时资源竞争时，CPU request 通常会影响 CPU share，request 高的容器在争抢 CPU 时权重更高。CPU 是可压缩资源，CPU 不够时容器通常变慢，而不是立刻被杀。

CPU limit 表示 CPU 使用上限。容器达到 CPU quota 后，内核会在当前周期内限制它继续运行，产生 throttling。CPU limit 能防止某个容器长期吃满节点，但对延迟敏感服务，如果 limit 设得过低，可能导致请求在短时间突发时被节流，P99 延迟变差。

内存 request 用于调度和 QoS 分类，内存 limit 则是硬上限。内存不是像 CPU 那样容易压缩的资源。容器超过内存 limit 时，可能被内核 OOM kill。节点内存压力大时，Kubernetes 也会根据 QoS、实际使用和驱逐策略选择 Pod 驱逐。没有内存 limit 的容器可能使用节点剩余内存，但也可能在节点压力下影响其他工作负载。

requests 和 limits 还决定 QoS Class。requests 和 limits 都设置且相等，通常是 Guaranteed；设置了部分 request 或 limit 是 Burstable；都不设置是 BestEffort。QoS 会影响节点资源压力下谁更容易被驱逐。生产服务通常至少要设置 requests，否则调度器无法做容量判断，Cluster Autoscaler 也很难判断需要多少节点。

面试里可以这样讲边界：request 不是“实际预留一块永远不用也不能被别人碰的资源”，而是调度承诺和竞争权重；limit 不是“推荐值”，而是运行时上限。CPU limit 过低会节流，内存 limit 过低会 OOM。资源配置的目标不是写一个漂亮 YAML，而是让调度、容量、性能和故障隔离都能成立。

## 22. CPU limit 和 CPU throttling 有什么关系？

可以先这样答：CPU limit 通常会被运行时映射成 cgroup 的 CPU quota；容器在一个调度周期内用完 quota 后，即使节点上还有空闲 CPU，也可能被 throttling，直到下一个周期再继续运行。也就是说，CPU throttling 是 CPU limit 在内核调度层生效的一种表现。

在 cgroup v1 里，常见机制是 CFS quota 和 CFS period。比如 period 是 100ms，limit 是 1 core，容器在这个 100ms 周期里总共能运行约 100ms CPU 时间；如果 limit 是 0.5 core，就是约 50ms。用完后，剩下的周期时间会被限流。cgroup v2 里机制名称不同，但思想仍然是用 `cpu.max` 之类的配置表达最大 CPU 时间。

throttling 对吞吐型任务未必总是灾难，但对延迟敏感服务很容易放大尾延迟。一个服务平均只用 0.3 core，但偶尔在 20ms 内需要突发 CPU 处理请求。如果 CPU limit 很低，它可能在短时间内用完 quota，后续请求只能等下个周期。监控上 CPU 使用率看起来不高，P99 却很差，这类问题经常和 throttling 有关。

CPU request 不会直接造成 throttling。request 更多影响调度和竞争权重。没有 CPU limit 的容器在节点有空闲 CPU 时可以突发使用更多 CPU；有 limit 的容器即使节点空闲，也会受 quota 约束。很多延迟敏感服务会谨慎设置 CPU limit，或者只设置 request，不设置 limit，让服务在节点有余量时能突发。但这要配合集群多租户隔离策略，不能一概而论。

排查 CPU throttling 要看容器级指标，比如 throttled periods、throttled seconds、CPU usage、run queue、P99 latency 和 GC 指标。只看平均 CPU 使用率会误判。解决手段包括提高 CPU limit、取消不必要的 limit、增加副本、优化热点代码、减少同步阻塞、给 JVM/Go runtime 配合容器资源做调优，或者把延迟敏感服务和批处理任务隔离到不同节点池。

面试里可以收束成一句：CPU limit 给容器设了天花板，CPU throttling 是撞到天花板后的内核限流。它能保护节点公平性，但设置不当会让服务在明明有空闲 CPU 的机器上仍然被限速。

## 23. HPA、VPA、Cluster Autoscaler 分别解决什么问题？

可以先这样答：HPA 调整 Pod 副本数，VPA 调整单个 Pod 的资源请求和限制，Cluster Autoscaler 调整节点数量。HPA 解决“实例数够不够”，VPA 解决“每个实例资源配得准不准”，Cluster Autoscaler 解决“集群节点容量够不够”。

HPA 是水平扩缩容。它根据 CPU、内存或自定义指标，把 Deployment、ReplicaSet、StatefulSet 等工作负载的副本数调大或调小。比如 CPU 利用率高，就增加 Pod 副本；流量下降，就减少副本。HPA 适合无状态或可水平扩展的服务。它要求应用能承受多副本并发处理，且上游负载均衡能把流量分散出去。

VPA 是垂直扩缩容。它观察 Pod 历史资源使用，给出或自动应用新的 CPU/memory requests，有时也维护 request/limit 比例。它适合资源配置不准、服务负载相对稳定、希望减少人工调参的场景。VPA 的限制是：很多情况下调整资源需要重建 Pod；对强实时业务，要谨慎自动驱逐；如果和 HPA 都基于 CPU 使用率控制同一个工作负载，可能互相打架，一个改副本数，一个改 request，指标分母也会变化。

Cluster Autoscaler 关注节点。它发现有 Pod 因资源不足无法调度时，会尝试让云厂商或节点组增加节点；发现某些节点长期不需要，且上面的 Pod 可以安全迁移时，会缩容节点。它不直接看业务 QPS，也不直接给 Deployment 改副本数，而是围绕调度结果和节点利用率工作。

三者经常串联。流量升高时，HPA 先增加 Pod；如果新 Pod 因节点资源不足 Pending，Cluster Autoscaler 再增加节点；节点起来后，Pod 被调度成功。VPA 则帮助 Pod 的 requests 更接近真实需求，让调度和 CA 判断更准。反过来，如果 requests 乱写，HPA 和 CA 都可能做出糟糕决策：request 太低导致节点过载，request 太高导致集群扩容过度。

面试里可以用一句话区分：HPA 横向加减 Pod，VPA 纵向调 Pod 资源，CA 横向加减 Node。真正落地时要关注指标延迟、冷启动、预热、PDB、节点供应时间、Pod 反亲和、requests 准确性和控制器之间是否互相干扰。

## 24. ConfigMap 和 Secret 有什么区别？

可以先这样答：ConfigMap 用来保存非敏感配置，Secret 用来保存敏感信息。它们都可以以环境变量、命令行参数或卷文件形式注入 Pod，但安全语义不同。Secret 不是“自动绝对安全”，它只是 Kubernetes 给敏感数据提供的专门 API 对象和访问控制入口。

ConfigMap 适合保存普通配置，例如配置文件、开关、URL、日志级别、模板、非敏感业务参数。它把配置从镜像里拆出来，让同一个镜像可以在不同环境使用不同配置。ConfigMap 的内容通常不应该包含密码、私钥、token、证书私钥这类敏感数据。

Secret 适合保存密码、token、TLS 证书、镜像仓库凭据、ServiceAccount token 等敏感数据。Secret 的 data 字段通常是 base64 编码，但 base64 不是加密。真正的安全依赖 RBAC 最小权限、etcd 加密静态数据、传输加密、审计、Secret 不落日志、限制谁能读取和挂载、以及外部密钥管理系统。面试里要明确说：Secret 比 ConfigMap 更适合敏感数据，但默认 base64 不能当作加密。

两者更新语义也要注意。以卷方式挂载的 ConfigMap/Secret 更新后，kubelet 会在一定延迟后更新挂载内容；以环境变量注入的值不会随着对象更新自动改变，通常需要重启 Pod。很多应用还需要自己监听文件变化或通过滚动发布加载新配置。不可变 ConfigMap/Secret 可以减少误改和 watch 压力，但变更时需要创建新对象或重新发布。

工程上，ConfigMap 和 Secret 都不适合塞很大的配置或频繁高频变更的数据。它们是 Kubernetes API 对象，不是配置中心数据库。大规模动态配置、灰度规则、强审计密钥轮换，往往需要专门配置中心、External Secrets、CSI Secret Store、Vault、云 KMS 或 Operator 配合。

面试里可以这样总结：ConfigMap 管普通配置，Secret 管敏感配置；二者注入方式相似，但安全边界不同。Secret 要配合 RBAC、etcd 加密、密钥轮换和外部密钥系统使用，不能因为字段叫 Secret 就以为已经完成安全治理。

## 25. RBAC 在 Kubernetes 中如何工作？

可以先这样答：RBAC 用“谁对什么资源能做什么动作”来控制 Kubernetes API 访问。谁是 Subject，包括 User、Group 和 ServiceAccount；能做什么由 Role 或 ClusterRole 定义；把 Subject 和权限绑定起来的是 RoleBinding 或 ClusterRoleBinding。请求进入 API Server 后，先认证身份，再通过 RBAC 等授权器判断是否允许。

Role 是 namespace 级权限，只在某个 namespace 内生效。比如允许某个 ServiceAccount 在 `prod` namespace 里读取 Pod 和 ConfigMap。ClusterRole 是集群级权限，可以表达集群资源权限，比如 Node、PersistentVolume、Namespace，也可以被 RoleBinding 绑定到某个 namespace 内，用来复用一套命名空间内权限模板。

RoleBinding 把 Role 或 ClusterRole 绑定给 Subject，作用范围是某个 namespace。ClusterRoleBinding 把 ClusterRole 绑定给 Subject，作用范围是整个集群。很多权限事故来自把本来只该给某个 namespace 的权限用 ClusterRoleBinding 绑到了全局，或者给了 `cluster-admin` 这种过大的内置角色。

权限规则由 apiGroups、resources、verbs 等字段组成。verbs 包括 get、list、watch、create、update、patch、delete 等。资源可以是 pods、deployments、secrets，也可以带子资源，比如 pods/log、pods/exec、deployments/scale。RBAC 还可以控制 nonResourceURLs，例如 `/healthz` 这类非资源路径。理解子资源很重要，因为能 get pod 不等于能 exec 进容器，能看 Deployment 不等于能扩缩容。

Kubernetes RBAC 通常是默认拒绝，没有显式 deny。一个请求只要被任意一条授权规则允许就通过；没有匹配 allow 就拒绝。它不像某些 IAM 系统那样用 deny 覆盖 allow。最小权限设计要从这个特点出发，少给通配符，少给 `*` verbs 和 `*` resources，尤其谨慎授予 secrets、pods/exec、serviceaccounts/token、roles、rolebindings 这些高风险权限。

ServiceAccount 是工作负载访问 API 的常见身份。Pod 里挂载的 token 代表某个 ServiceAccount，控制器、Operator、CI/CD 机器人和业务服务都可能用它访问 API。给 ServiceAccount 授权时要按工作负载职责绑定最小 Role，而不是让所有 Pod 使用 default ServiceAccount，也不要把 default 绑定成管理员。

排查 RBAC 可以用 `kubectl auth can-i`，也可以看 API Server 审计日志、RoleBinding、ClusterRoleBinding 和实际使用的 ServiceAccount。面试里可以这样收束：RBAC 的核心是 Subject、Role/ClusterRole、RoleBinding/ClusterRoleBinding 四件事；它保护的是 Kubernetes API，不直接保护容器里的业务接口；权限要按 namespace、资源、verb 和子资源精确拆分。
## 26. Pod 调度在云原生系统中解决什么问题？

可以先这样答：Pod 调度解决的是“一个 Pod 应该落到哪台 Node 上运行”的问题。Kubernetes 不是把容器随机塞进集群，而是要根据资源、约束、亲和性、污点容忍、拓扑、存储、端口、优先级和集群当前状态，给每个还没有绑定节点的 Pod 选择一个可运行的位置。

这个问题在云原生系统里很基础。Pod 本身是最小调度单位，Deployment、StatefulSet、DaemonSet、Job 最终都会落成 Pod。只要 Pod 没有被调度成功，它就不会真正启动容器，也不会进入 Service 的可用后端，不会产生正常业务流量。调度是从“声明了工作负载”到“节点上真的跑起来”的关键一步。

调度首先解决容量匹配。Pod 声明 CPU、内存、临时存储、扩展资源和卷需求后，调度器要避免把它放到无法承载的节点上。否则节点会过载，kubelet 后面只能靠驱逐、OOM 或资源抢占兜底。调度阶段做得越准，运行时故障越少。

调度还解决故障域和拓扑问题。比如副本不要都落在同一个节点、同一个机架或同一个可用区；延迟敏感服务要靠近某些数据或 GPU 节点；系统组件要跑在带特定标签的节点池上。亲和性、反亲和性、拓扑分布约束和污点容忍，本质上都是把这些工程意图写进调度决策。

面试里不要把 Pod 调度说成“kubelet 启动 Pod”。调度器只负责绑定目标节点；真正拉镜像、创建 sandbox、挂载卷、调用 CNI、启动容器的是目标节点上的 kubelet 和容器运行时。一个清楚的边界是：调度器决定放哪，kubelet 负责跑起来。

## 27. Pod 调度的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：Pod 创建后先写入 API Server 和 etcd，如果 Pod 还没有 `spec.nodeName`，kube-scheduler 会 watch 到这个未绑定 Pod。调度器经过过滤、打分、选择节点、绑定几个阶段，最终通过 API Server 把 Pod 绑定到某个 Node。目标节点上的 kubelet 看到分配给自己的 Pod 后，才开始创建运行环境。

过滤阶段先排除明显不能运行的节点。资源不足、节点不可调度、污点不被容忍、nodeSelector 或 nodeAffinity 不匹配、端口冲突、卷无法挂载、拓扑约束不满足，都可能让节点被过滤掉。这个阶段的目标不是找最优，而是找“可行”。

打分阶段在可行节点里排序。调度器会考虑资源利用率、镜像本地性、Pod 亲和/反亲和、拓扑分散、节点负载等因素，算出每个候选节点的分数。分数最高的节点被选中。如果多个节点同分，调度器会按内部策略选一个。选中后进入绑定流程，Pod 的节点归属被写回 API 对象。

涉及的组件要分清：API Server 是所有对象读写入口；etcd 保存 Pod、Node、PV/PVC 等状态；kube-scheduler 做调度决策；kube-controller-manager 里的各种控制器负责创建 Pod 或维护状态；kubelet 在节点上执行 PodSpec；容器运行时、CSI 和 CNI 分别处理容器、存储和网络；准入控制器、资源配额、LimitRange、调度扩展插件也可能改变或影响调度结果。

调度失败时，Pod 通常会停在 Pending，事件里能看到类似 Insufficient cpu、node affinity mismatch、untolerated taint、volume node affinity conflict。排障不要只看 Pod 日志，因为容器还没启动时根本没有业务日志。先看 `kubectl describe pod` 的 Events，再看调度器日志、Node 标签污点、PVC 绑定状态和资源请求。

## 28. Pod 调度配置错误时会导致哪些线上问题？

可以先这样答：调度配置错了，最直接的线上现象是 Pod Pending、扩容不生效、发布卡住，或者 Pod 虽然启动了但集中落在少数节点上，把节点打满。它不像业务代码 bug 那样总有明确堆栈，很多时候表现为“资源明明还有，为什么就是调不上去”。

资源请求写错是最常见问题。requests 过高会让调度器认为节点放不下，HPA 扩容出来的新 Pod 长期 Pending，Cluster Autoscaler 可能被迫加很多节点；requests 过低会让太多 Pod 被塞到同一批节点，运行时 CPU throttling、内存争抢、OOM、驱逐和尾延迟一起出现。limit 写得离真实突发太近，也会让服务在高峰期被内核限速。

亲和性、反亲和性和拓扑分布约束写错，会制造另一类故障。约束过硬时，Pod 无法调度；约束过松时，副本可能落在同一节点或同一可用区，一个节点故障就带走大量副本。`requiredDuringScheduling` 和 `preferredDuringScheduling` 的区别要谨慎使用，前者是硬条件，后者是偏好。

污点和容忍配置也容易出事故。系统节点、GPU 节点、专用节点池通常会打 taint，如果业务 Pod 没有 toleration，就永远调不上去；反过来，如果所有业务都容忍了专用节点的 taint，可能把普通服务调到昂贵或敏感节点上。节点标签被误删、调度器配置漂移、节点 cordon 后忘记恢复，也会让容量突然少一块。

存储相关错误更隐蔽。PVC 使用的 StorageClass、PV node affinity、可用区和 Pod 调度位置必须一致。状态服务如果卷在一个 zone，Pod 被调到另一个 zone，就会卡在调度或挂载阶段。排查时要把 Pod、PVC、PV、StorageClass、Node zone 标签放在一起看。

## 29. Pod 调度如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Pod 调度决定实例落点，实例落点又决定 Service 后端分布、EndpointSlice 内容、HPA 扩容效果和观测指标的空间分布。调度不是负载均衡器本身，但它决定负载均衡器背后有哪些实例、在哪些节点和可用区。

对负载均衡来说，副本如果均匀分布在节点和可用区，Service、Ingress、Gateway、Service Mesh 的后端池就更健康。副本如果集中在少数节点，kube-proxy 或网关即使按后端均衡，也会把大量流量压到同一故障域。开启 topology-aware routing、本地流量策略或跨可用区成本优化时，调度分布更关键，因为本地 endpoint 数量会直接影响流量走向。

对服务发现来说，Pod 被调度并通过 readiness 后，EndpointSlice controller 才会把它加入对应 Service 的 endpoint 集合。Pod Pending 时不会成为后端；Pod 调到错误节点、网络不可达或 readiness 长期失败，也不会稳定进入可用后端。Headless Service 场景更明显，DNS 直接返回 Pod IP，调度位置和 Pod 网络状态会被客户端直接感知。

对弹性伸缩来说，HPA 只负责增加 Pod 副本，调度器负责把这些副本放下去。如果 requests 写错、节点资源不足、亲和性过硬或 PVC 绑定受限，HPA 的扩容结果就是一堆 Pending Pod。Cluster Autoscaler 也依赖调度失败信号判断是否需要加节点，所以调度约束会影响集群级扩容。

对可观测性来说，调度维度必须进指标和日志。只看服务级 QPS、错误率和 P99 不够，还要按 node、zone、pod、workload、scheduler、reason 拆开看。很多线上问题只有按节点或可用区聚合才看得出来，比如某个节点池上的 Pod 全部 DNS 慢、某个 zone 的 endpoint 都不 ready、某类实例只被调到高延迟节点。

## 30. Deployment 滚动更新在云原生系统中解决什么问题？

可以先这样答：Deployment 滚动更新解决的是“不中断或尽量少中断地把无状态服务从旧版本切到新版本”的问题。它不要求一次性杀掉所有旧 Pod 再拉起新 Pod，而是通过 ReplicaSet 渐进增加新副本、减少旧副本，让服务在发布过程中仍然维持可用容量。

云原生系统里，实例通常很多，发布频率也高。直接全量重启会带来明显风险：容量瞬间归零、连接被切断、缓存全部冷启动、错误版本无法及时止损。滚动更新把风险拆成小步，每次只让一部分实例进入新版本，配合 readiness、maxSurge、maxUnavailable 和回滚能力，把发布变成可观察、可暂停、可回退的过程。

Deployment 主要面向无状态工作负载。它管理的是一组可替换 Pod，Pod 名称、IP 和所在节点都不应该被业务当成长期身份。旧 Pod 和新 Pod 在发布窗口里可能同时存在，Service 通过 selector 和 EndpointSlice 接收 ready 的后端。只要应用本身兼容多版本并行，滚动更新就能比较平滑。

它不能替代灰度发布系统。Deployment 原生滚动更新按副本替换，不理解用户、租户、Header、地域、错误预算和指标门禁。真正的金丝雀、蓝绿、自动指标分析，通常还要 Argo Rollouts、Flagger、Service Mesh、Ingress/Gateway 或发布平台配合。Deployment 提供基础版本替换能力，不负责完整发布治理。

面试里可以收束成一句：Deployment 滚动更新解决无状态服务的渐进替换和回滚问题，关键是保持可用副本、等待新 Pod ready、逐步缩掉旧 ReplicaSet。它的前提是应用能水平扩展，并能承受短时间多版本共存。

## 31. Deployment 滚动更新的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：用户修改 Deployment 的 Pod template 后，Deployment controller 会创建新的 ReplicaSet，并按 rollingUpdate 策略逐步扩新 ReplicaSet、缩旧 ReplicaSet。ReplicaSet controller 负责创建具体 Pod，scheduler 负责给新 Pod 选节点，kubelet 负责在节点上运行它们。

核心参数是 `maxSurge` 和 `maxUnavailable`。`maxSurge` 决定滚动过程中最多可以额外多出多少 Pod，给新版本预热和补容量；`maxUnavailable` 决定最多允许多少期望副本不可用。比如 replicas 是 10，maxSurge 是 2，maxUnavailable 是 1，发布时总 Pod 数可能临时到 12，但可用副本不能低于 9。

readinessProbe 是滚动更新能否平滑的关键。新 Pod 只有 ready 后才会进入 Service 的可用后端，Deployment controller 也会据此继续推进。如果 readiness 写得太宽，新 Pod 还没准备好就接流量；写得太严，新版本迟迟不能推进，发布卡住。`minReadySeconds` 可以要求 Pod ready 后保持一段时间才算 available。

涉及组件包括 API Server、etcd、Deployment controller、ReplicaSet controller、kube-scheduler、kubelet、容器运行时、Service/EndpointSlice controller，以及负责入口流量的 kube-proxy、Ingress、Gateway 或 Mesh。Deployment 控制的是工作负载副本，Service 和数据面负责把流量导到 ready 后端。

回滚本质上也是一次模板切换。Deployment 保留历史 ReplicaSet，发现新版本异常时可以把模板回到旧版本，再由控制器按同样逻辑扩旧缩新。这里要注意：回滚只能恢复 Kubernetes 对象和镜像版本，不能自动回滚数据库 schema、外部配置、消息格式或状态迁移。

## 32. Deployment 滚动更新配置错误时会导致哪些线上问题？

可以先这样答：滚动更新配置错了，常见问题是发布卡住、容量不足、错误版本快速放大、回滚无效，或者下线时仍有请求打到正在退出的 Pod。看起来是“发布系统不稳定”，根因往往是 Deployment 策略、探针和应用兼容性没有对齐。

`maxUnavailable` 过大，会在发布时一次性拿掉太多旧副本，业务容量突然下降。`maxSurge` 过大，会短时间创建太多新 Pod，节点 CPU、内存、镜像拉取、CNI 和存储都被打爆。`maxSurge` 过小又可能让发布很慢，尤其在新 Pod 需要较长预热时更明显。

readiness 错误会直接影响流量。新 Pod 启动进程后马上 ready，但缓存、连接池、配置、JIT 或依赖还没准备好，就会出现发布初期 5xx 和 P99 抖动。反过来，readiness 检查了过多外部依赖，一次数据库抖动会让所有 Pod 同时 not ready，Service 后端池被清空。

应用多版本不兼容是另一类坑。滚动更新天然会让旧版本和新版本共存。如果新版本写入了旧版本无法读的数据格式，或者 API 字段、消息 schema、数据库迁移没有前后兼容，哪怕 Kubernetes 层发布完全正常，业务也会出错。Deployment 只保证 Pod 替换顺序，不保证业务协议兼容。

终止配置也常被忽略。terminationGracePeriod 太短、preStop 没有 drain、网关摘除延迟没算进去，会让旧 Pod 在仍有连接时被 SIGKILL。长连接、gRPC stream、WebSocket、消息消费者尤其容易受影响。发布事故里很多 502/499/connection reset 都发生在这个窗口。

## 33. Deployment 滚动更新如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Deployment 滚动更新会持续改变 Service 后端集合。新 Pod ready 后加入 EndpointSlice，旧 Pod terminating 或 not ready 后退出可用后端。负载均衡、服务发现、伸缩和监控看到的，都是这个动态变化过程。

对负载均衡来说，滚动更新期间后端池里可能同时有 v1 和 v2。普通 Service 不理解版本语义，只按 ready endpoint 转发。Ingress、Gateway 或 Mesh 如果要做按版本流量切分，需要额外标签、子集、路由规则或灰度控制器。否则 Deployment 只是按副本比例间接影响流量比例，不保证精确 1%、5% 这种灰度比例。

对服务发现来说，EndpointSlice 更新有传播延迟。Pod readiness 失败后，从 API 对象变化到 kube-proxy、CoreDNS、Ingress controller、Envoy 或客户端 watcher 都需要时间。应用下线不能假设“我把 readiness 置 false 就立刻没有请求”。成熟做法是 readiness 先失败，再等待一段 drain，再退出进程。

对弹性伸缩来说，滚动更新会和 HPA 叠加。Deployment 官方文档里也强调滚动更新期间可能同时存在多个 ReplicaSet，扩缩容时控制器会在活跃 ReplicaSet 之间做比例分配，降低风险。工程上要关注指标延迟：新版本刚上线时 CPU、延迟或错误率可能不同，HPA 如果立刻响应，可能把发布波动放大。

对可观测性来说，必须把版本维度加进指标、日志和 trace。只看 Deployment 总体错误率，会把新旧版本混在一起。发布排障应该按 ReplicaSet、pod-template-hash、image tag、zone、node、route、status code 拆分，配合 rollout 事件看时间线。这样才能判断是新版本业务错误、调度容量不足、镜像拉取慢，还是网关摘除延迟。

## 34. StatefulSet 在云原生系统中解决什么问题？

可以先这样答：StatefulSet 解决的是“有状态应用需要稳定身份、稳定存储和有序管理”的问题。Deployment 管理的 Pod 是可替换副本，StatefulSet 管理的 Pod 有固定序号、稳定网络身份和通常一一对应的持久卷，更适合数据库、消息队列、协调服务、主从/分片类系统。

StatefulSet 的关键不是“能挂 PVC”，Deployment 也能挂卷。关键在于身份稳定。`web-0`、`web-1` 这类 Pod 名称会保留，Pod 重建后仍然使用同一个序号；配合 Headless Service，还能形成稳定 DNS 名称；配合 volumeClaimTemplates，每个 Pod 拿到自己的 PVC。这样应用可以把副本身份和数据目录绑定起来。

有序性也很重要。默认情况下，StatefulSet 创建、更新、删除 Pod 都有顺序约束。很多有状态系统需要先启动 0 号节点，再启动后续节点；缩容时先删高序号；滚动更新时按逆序逐个处理。这些顺序不是为了形式，而是为了避免 quorum、leader、复制链路或数据恢复被同时打断。

StatefulSet 不是数据库高可用的魔法。它不会自动做数据复制、主从切换、备份恢复、数据一致性校验，也不会理解业务里的 leader election。它只提供 Kubernetes 层的身份、存储和编排顺序。真正的数据安全还要靠应用自身协议、Operator、备份、PDB、反亲和、拓扑规划和恢复演练。

面试里可以这样说：Deployment 适合可替换的无状态副本，StatefulSet 适合不能随便换身份的副本。只要业务把某个实例的名字、网络身份或磁盘当成长期事实，就要认真考虑 StatefulSet 或专门 Operator。

## 35. StatefulSet 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：StatefulSet controller 根据 StatefulSet 规格创建一组带序号的 Pod，并为每个 Pod 按 volumeClaimTemplates 创建对应 PVC。它通常需要一个 Headless Service 提供稳定网络身份。每个 Pod 的名称、DNS、PVC 绑定关系都围绕序号展开。

创建时，StatefulSet 会生成类似 `app-0`、`app-1`、`app-2` 的 Pod。默认 `OrderedReady` 策略下，前一个 Pod ready 后才继续创建下一个。删除或缩容时通常从最高序号开始处理。滚动更新时，也会按受控顺序替换 Pod，避免一口气动掉多个有状态副本。

存储链路涉及 PVC、PV、StorageClass、CSI 和 kubelet。volumeClaimTemplates 为每个序号生成独立 PVC，例如 `data-app-0`。Pod 重建后会重新挂载同一个 PVC，而不是随机拿一个新盘。调度器还要考虑卷的拓扑约束，尤其是云盘绑定可用区时，Pod 必须调度到能挂载该卷的节点或区域。

网络身份依赖 Headless Service 和 DNS。Headless Service 不分配 ClusterIP，CoreDNS 可以为 StatefulSet Pod 生成稳定的 DNS 名称，客户端或集群成员可以通过这些名称发现特定副本。普通 Service 仍然可以用于访问整组 Pod，但如果应用要定位具体副本，Headless Service 更合适。

涉及组件包括 API Server、etcd、StatefulSet controller、PVC/PV 控制器、CSI provisioner、kube-scheduler、kubelet、CoreDNS、EndpointSlice controller，以及应用自己的集群协议。排障时要同时看 Pod、PVC、PV、StorageClass、Headless Service、EndpointSlice 和 DNS，而不是只盯 StatefulSet 对象。

## 36. StatefulSet 配置错误时会导致哪些线上问题？

可以先这样答：StatefulSet 配置错了，线上后果通常比无状态服务更重。它可能导致数据目录错挂、Pod 长期 Pending、集群成员身份混乱、滚动更新卡住、quorum 丢失，甚至数据损坏。因为这里的 Pod 不再是完全可替换的。

最危险的是存储配置错误。volumeClaimTemplates 改错、StorageClass 选错、访问模式不匹配、可用区和节点调度不一致，都会让 Pod 无法挂盘或挂到不符合预期的存储。更严重的是人为删除 PVC、复用错误 PV、把多写不安全的卷给多个副本用，可能直接破坏数据。

Headless Service 缺失或 selector 错误，会让稳定 DNS 身份失效。StatefulSet Pod 还在，但其他成员解析不到 `pod-name.service.namespace.svc`，集群发现失败。服务发现错误在数据库和消息队列里常表现为节点无法加入集群、复制链路断开、客户端连接到错误成员。

更新策略写错会影响可用性。一次性删除多个副本、PDB 过松、反亲和缺失，会让多个有状态副本落在同一故障域或同时重启。对 etcd、ZooKeeper、Kafka、数据库主从这类系统，少数几个副本同时不可用就可能丢 quorum 或触发主从切换风暴。

还有一个常见误区：把不适合 StatefulSet 的应用硬塞进去。StatefulSet 提供身份和存储，不提供应用层一致性。如果应用没有正确处理崩溃恢复、重复启动、旧数据升级、leader 选举和副本重加入，那么 Kubernetes 能把 Pod 拉起来，也不能保证数据层真的健康。
## 37. StatefulSet 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：StatefulSet 会让“实例身份”进入流量和观测模型。Deployment 里的副本通常只看整体可用性，StatefulSet 里的每个序号都可能有不同角色、数据分片、复制延迟和存储状态，所以负载均衡、服务发现、伸缩和监控都不能只按一组无差别 Pod 处理。

对负载均衡来说，普通 Service 可以把流量打到整组 ready Pod，但这只适合所有副本都能等价接请求的场景。很多有状态系统需要区分 leader/follower、primary/replica、分片编号或读写角色，这时要靠应用协议、独立 Service、Operator、标签或客户端路由来做选择。Kubernetes 的 Service 不理解数据库主从语义。

对服务发现来说，Headless Service 很常见。客户端可以解析到每个 Pod 的稳定 DNS 名称，直接连接特定副本。这对成员发现、复制配置、分片路由很有用，但也意味着客户端要承担更多选择逻辑。DNS 返回地址不等于“这个副本适合处理你的写请求”。

对弹性伸缩来说，StatefulSet 扩缩容比 Deployment 更谨慎。扩容新副本可能需要数据复制、分片迁移、日志追赶和预热；缩容旧副本可能要先迁移数据或从集群里优雅移除成员。HPA 虽然可以作用于某些 StatefulSet，但状态服务是否适合自动水平扩容，取决于应用能不能安全处理成员变化。

对可观测性来说，要按 ordinal、PVC、PV、zone、role、replication lag、leader 状态、磁盘 IO、恢复进度拆指标。只看 StatefulSet 总体 ready 副本数不够。一个 `app-2` 的 PVC IO 打满，可能只影响一个分片；一个 leader 频繁漂移，可能导致全局写延迟抖动。观测要贴近状态模型。

## 38. DaemonSet 在云原生系统中解决什么问题？

可以先这样答：DaemonSet 解决的是“在每个符合条件的节点上都运行一份节点本地组件”的问题。它适合日志采集、监控 agent、CNI/CSI 辅助进程、节点安全 agent、存储守护进程、GPU 插件、NodeLocal DNSCache 这类必须贴近节点工作的组件。

普通 Deployment 关心副本总数，比如全局跑 3 个副本；DaemonSet 关心节点覆盖率，比如每台 eligible Node 都要有 1 个 Pod。节点加入集群时，DaemonSet controller 会给新节点补 Pod；节点删除时，对应 Pod 会被清理。这让节点级基础设施可以跟随集群规模自动变化。

DaemonSet 的价值在于本地性。日志采集器要读取节点上的容器日志目录，监控 agent 要采集节点指标，CNI agent 要处理本节点网络规则，NodeLocal DNSCache 要在节点上提供本地 DNS 缓存。这些组件如果只跑少数几个副本，就无法覆盖所有节点路径。

它也适合做按节点池差异化部署。通过 nodeSelector、nodeAffinity、tolerations，可以让某个 DaemonSet 只跑在 GPU 节点、边缘节点、Linux 节点或打了特定标签的节点上。一个集群里可以有多个 DaemonSet 分别覆盖不同硬件或网络环境。

面试里要强调：DaemonSet 管的是节点本地基础能力，不适合普通业务水平扩容。业务服务需要按 QPS 加副本，通常用 Deployment；每个节点都必须有一份的系统或平台组件，才是 DaemonSet 的典型场景。

## 39. DaemonSet 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：DaemonSet controller watch Node 和 DaemonSet 对象，计算哪些节点符合条件，然后为每个目标节点创建一个 Pod。现代 Kubernetes 中，这些 Pod 通常会带有节点亲和约束，由默认调度器完成绑定；节点上的 kubelet 再按普通 Pod 流程启动容器。

DaemonSet 是否覆盖某个节点，取决于 selector、nodeSelector、nodeAffinity、tolerations、节点是否可调度以及一些系统条件。很多节点会带 taint，例如 control-plane、专用节点、GPU 节点。如果 DaemonSet 没有对应 toleration，就不会落上去；如果 toleration 过宽，又可能跑到不该覆盖的节点。

更新时，DaemonSet 也支持滚动更新。控制器会按 maxUnavailable 等策略逐步替换节点上的 DaemonSet Pod，避免所有节点本地 agent 同时重启。对 CNI、日志、监控这类基础组件，更新窗口要谨慎，因为 agent 暂停可能影响整台节点上的多个业务 Pod。

涉及组件包括 API Server、etcd、DaemonSet controller、kube-scheduler、kubelet、容器运行时、CNI、节点标签污点，以及可能的准入控制和 PDB。对某些系统 DaemonSet，还会涉及 hostNetwork、hostPID、hostPath、privileged、capabilities 等宿主机权限。

排障时先看三个问题：目标节点是否符合 selector 和 affinity；DaemonSet Pod 是否被 taint 卡住；节点上的 kubelet 是否能拉镜像、挂载 hostPath、启动特权容器。很多 DaemonSet 异常不是控制器没创建 Pod，而是节点权限、镜像、CNI 或安全策略让 Pod 起不来。

## 40. DaemonSet 配置错误时会导致哪些线上问题？

可以先这样答：DaemonSet 配置错误会影响整个节点池，甚至影响整集群。因为它通常承载日志、监控、网络、存储或安全能力，一旦出错，不是一个业务副本坏了，而是每台节点上的基础能力同时异常。

覆盖范围配置错最常见。selector、nodeAffinity 或 toleration 太窄，会导致部分节点没有日志采集、没有监控、没有 DNS 缓存或没有网络 agent；配置太宽，又会把 agent 跑到控制平面节点、GPU 专用节点、Windows 节点或不兼容节点上。结果可能是某些节点完全不可观测，或者关键节点被多余进程干扰。

资源配置错也危险。DaemonSet 每个节点一份，如果 CPU/memory requests 低估，节点上 agent 竞争不过业务，日志丢失、指标断点、DNS 慢、CNI 规则更新延迟都会出现；requests 高估，则每台节点都被预留一块资源，集群可调度容量明显下降。副本数越多，单个配置错误放大得越厉害。

权限配置错会带来安全和稳定性问题。很多 DaemonSet 需要 hostPath、hostNetwork、privileged 或内核能力。如果权限不够，agent 起不来或功能缺失；权限给得太大，漏洞影响面就扩大到宿主机。日志采集器挂错 hostPath、CNI agent 写错规则、存储插件误操作设备，都会造成节点级事故。

滚动更新策略也要小心。一次更新太多节点上的网络或 DNS agent，可能让大批业务 Pod 同时解析失败、网络抖动或指标中断。系统 DaemonSet 更新前要看 maxUnavailable、节点分批、健康检查和回滚路径，不能把它当普通无状态业务发布。

## 41. DaemonSet 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：DaemonSet 通常不直接承载业务流量，但它强烈影响业务流量能否被正确转发、发现、伸缩和观测。它像节点侧底座，出了问题时，上层 Service、Ingress、HPA 和监控系统都会跟着表现异常。

对负载均衡来说，CNI、kube-proxy 替代组件、eBPF agent、NodeLocal DNSCache、Ingress node agent 等常以 DaemonSet 运行。某个节点上的这些组件异常，Service 转发、NetworkPolicy、DNS、本地代理或节点入口流量就可能出问题。负载均衡器看到的是某些后端节点不稳定，根因却在节点 DaemonSet。

对服务发现来说，NodeLocal DNSCache 或节点本地代理如果是 DaemonSet，覆盖率直接影响 Pod 解析体验。某些节点没有跑对应 Pod，或者本地 DNSCache 配置不一致，同一个 Service 在不同节点上就可能有不同解析延迟和错误率。

对弹性伸缩来说，每新增一个节点，DaemonSet 都会消耗固定资源。Cluster Autoscaler 加节点后，系统 DaemonSet 先占掉一部分 CPU 和内存，真正留给业务 Pod 的可分配容量要扣除这部分。反过来，如果 DaemonSet Pod 没有及时在新节点 ready，节点可能暂时不适合承载业务。

对可观测性来说，DaemonSet 往往就是观测链路本身。日志 agent 没跑，业务日志就断；metrics agent 卡住，节点看起来像没有数据；安全 agent 覆盖缺口会形成审计盲区。监控 DaemonSet 时要看 desired/current/ready 数、按节点覆盖率、重启次数、资源使用、队列积压和采集延迟，而不是只看 DaemonSet 对象是 Active。

## 42. Service 在云原生系统中解决什么问题？

可以先这样答：Service 解决的是“Pod 会变化，但访问入口要稳定”的问题。Pod 的 IP、名称和所在节点都可能随着重建、扩缩容、滚动更新而变化，Service 给一组 Pod 提供稳定的虚拟访问入口和服务发现名称，让调用方不用直接追踪每个 Pod。

Service 的核心是 selector 和后端 endpoint 集合。一个 Service 通过标签选择一组 Pod，控制面把这些 Pod 的地址写入 EndpointSlice。客户端访问 Service 名称或 ClusterIP 时，集群 DNS、kube-proxy、eBPF 数据面或其他实现把流量导向实际 Pod。

它解决了三个基础问题。第一是命名稳定，`my-svc.my-ns.svc.cluster.local` 这类名字可以长期存在；第二是后端动态变化，扩容、缩容、发布时 endpoint 自动更新；第三是基础负载分发，客户端不用自己保存完整 Pod 列表，也不用知道每个 Pod 的生命周期。

Service 有多种类型。ClusterIP 面向集群内访问；NodePort 把服务暴露到每个节点端口；LoadBalancer 通过云厂商或外部控制器创建负载均衡器；ExternalName 返回外部 DNS CNAME；Headless Service 不分配 ClusterIP，而是让客户端看到后端地址集合。不同类型解决不同入口问题。

面试里要把 Service 和 Ingress/Gateway 分开。Service 是服务抽象和 L4 级稳定入口，主要解决一组 Pod 的发现和转发；Ingress/Gateway 更靠近集群入口和 L7 路由，按 Host、Path、Header 等规则把外部或网关流量转给 Service。

## 43. Service 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：用户创建 Service 后，API Server 保存对象；EndpointSlice controller 根据 Service selector 找到匹配且 ready 的 Pod，维护 EndpointSlice；CoreDNS 为 Service 提供 DNS 记录；每个节点上的 kube-proxy 或替代数据面 watch Service 和 EndpointSlice，并下发本地转发规则。

普通 ClusterIP Service 会分配一个虚拟 IP。Pod 访问这个 ClusterIP 时，节点上的数据面规则把目标地址转换或转发到某个后端 Pod IP。iptables 模式会写 netfilter 规则，IPVS 模式会配置内核 IPVS virtual server，nftables 或 eBPF 实现则用自己的数据结构表达同样的 Service 到 endpoint 映射。

DNS 是服务发现入口。普通 Service 的 A/AAAA 记录通常解析到 ClusterIP；Headless Service 则解析到后端 Pod IP 集合。CoreDNS 的 kubernetes 插件会 watch Kubernetes API 中的 Service、EndpointSlice 等对象，按 Kubernetes DNS 规范生成答案。

Service 不一定必须有 selector。没有 selector 的 Service 可以手工关联 EndpointSlice，用来把 Kubernetes 内部的稳定名字指向外部数据库、旧系统或跨集群后端。但这时 Kubernetes 不会自动替你发现 Pod，EndpointSlice 的正确性要由人或控制器维护。

涉及组件包括 API Server、etcd、Service controller、EndpointSlice controller、CoreDNS、kube-proxy 或 eBPF 数据面、CNI 网络、kubelet readiness 状态，以及云厂商负载均衡控制器。排障时要沿着 Service、selector、Pod labels、EndpointSlice、DNS、节点转发规则和网络连通性逐层看。

## 44. Service 配置错误时会导致哪些线上问题？

可以先这样答：Service 配置错误最常见的现象是服务名能解析但访问失败、后端为空、流量打到错误 Pod、端口不通、发布后新版本没流量，或者旧版本仍然接请求。它处在服务发现和负载转发中间，一点小错会影响很多调用方。

selector 错误是第一类问题。selector 太窄，EndpointSlice 为空，客户端访问 ClusterIP 得到连接失败或超时；selector 太宽，会把不属于该服务的 Pod 加进后端，流量打到错误应用。发布时如果模板 label 和 Service selector 不匹配，新 Pod ready 了也不会有流量。

端口配置也容易错。Service 的 port、targetPort、protocol、命名端口必须和容器实际监听一致。targetPort 写错时，Service 有 endpoint，但转发到 Pod 后端口不通；协议写错时，UDP/TCP 行为完全不同；命名端口在多容器或多版本滚动时如果不一致，会让部分 endpoint 异常。

类型配置错会影响入口。ClusterIP 只能集群内访问；NodePort 暴露节点端口但要考虑安全组、防火墙和源地址策略；LoadBalancer 依赖云控制器和健康检查；ExternalName 只是 DNS 层 CNAME，不创建代理转发。把这些类型混用，容易产生“对象创建成功但外部访问不通”的假象。

还有数据面传播和连接状态问题。EndpointSlice 更新后，kube-proxy、Ingress、Gateway、Mesh 或客户端连接池都要同步。旧连接可能继续打到旧 Pod，conntrack 也可能保留旧路径。排障时要看新连接和旧连接的差别，不要只用一次 curl 就下结论。

## 45. Service 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Service 是 Kubernetes 服务发现和基础负载均衡的核心抽象。它把动态 Pod 集合包装成稳定名字和稳定入口；HPA、Deployment 滚动更新、Gateway、Ingress、Mesh 都会通过它或它的 EndpointSlice 间接受影响。

对负载均衡来说，Service 定义后端集合，kube-proxy 或替代数据面负责把访问分散到 endpoint。普通 Service 的负载分发通常是连接级或数据面实现级，不等于应用层智能路由。长连接、HTTP/2/gRPC 多路复用、session affinity、externalTrafficPolicy、topology hints 都会影响实际流量比例。

对服务发现来说，Service 名称比 Pod IP 稳定得多。客户端查 Service DNS，拿到 ClusterIP 或后端地址，再由数据面完成转发。这样 Pod 扩缩容、重建、滚动更新时，调用方不需要改配置。但这也要求 labels、readiness 和 EndpointSlice 正确，否则稳定名字会指向错误或空的后端。

对弹性伸缩来说，Service 是新增副本接入流量的门口。HPA 扩出新 Pod 后，只有 Pod ready 且被 Service selector 选中，EndpointSlice 更新后，流量才会进入新副本。Service selector 不匹配或 readiness 过严，会让扩容看起来成功，实际吞吐没有增加。

对可观测性来说，要同时看 Service 维度和 endpoint 维度。Service 级错误率能告诉你入口是否异常，endpoint 维度才能告诉你是不是某个 Pod、节点、zone 或版本在坏。还要观察 EndpointSlice 数量、后端数量、ready 状态、kube-proxy 同步时延、DNS 查询错误和网关到 Service 的 upstream 指标。

## 46. Headless Service 在云原生系统中解决什么问题？

可以先这样答：Headless Service 解决的是“我不想要一个统一 ClusterIP，而是想让客户端直接看到后端实例地址”的问题。它通过 `clusterIP: None` 关闭虚拟 IP，DNS 查询普通会返回匹配 Pod 的 IP 集合，常用于 StatefulSet、成员发现、客户端自定义负载均衡和需要直连具体实例的系统。

普通 Service 把一组 Pod 藏在 ClusterIP 后面，客户端只知道访问这个虚拟 IP。Headless Service 则把后端暴露给客户端，让客户端或应用协议自己选择连接哪个实例。对数据库副本、Kafka broker、ZooKeeper/etcd 成员、分片服务来说，知道每个具体成员是谁往往比拿到一个统一 VIP 更有用。

它也解决稳定网络身份问题。StatefulSet 配合 Headless Service 后，每个 Pod 可以有稳定 DNS 名称，例如 `pod-0.service.namespace.svc.cluster.local`。Pod 重建后 IP 可能变，但名称和序号仍然有意义，应用可以围绕这个身份做集群配置。

Headless Service 不是“没有服务发现”。它只是不用 ClusterIP 和 kube-proxy 做统一转发，仍然依赖 Service selector、EndpointSlice 和 CoreDNS。客户端查 DNS 拿到后端地址后，后续负载均衡、重试、连接池、健康判断就更偏向客户端或应用协议。

面试里要讲清边界：Headless Service 适合需要感知后端实例的系统，不适合把普通 Web 服务直接暴露给所有客户端后让客户端随便选。客户端能力不足时，普通 Service 或网关代理会更稳。

## 47. Headless Service 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：Headless Service 的 `clusterIP` 是 `None`，API Server 不给它分配 ClusterIP，kube-proxy 也不需要为它创建普通 VIP 转发规则。Service selector 仍然选择 Pod，EndpointSlice controller 仍然维护后端地址，CoreDNS 根据这些后端返回 DNS 记录。

对普通无头 Service，DNS A/AAAA 记录会返回被 Service 选中的 Pod IP 集合，而不是返回 ClusterIP。对 StatefulSet 场景，Headless Service 还配合 Pod hostname、subdomain 和 StatefulSet 序号，形成每个 Pod 的稳定 DNS 名称。SRV 记录也可以表达命名端口。

如果 Headless Service 没有 selector，也可以手工创建 EndpointSlice，把服务名指向外部地址或一组非 Pod 后端。这个模式适合迁移期或跨系统集成，但责任从 Kubernetes 自动控制器转到维护 EndpointSlice 的人或控制器身上。

涉及组件包括 Service、EndpointSlice controller、CoreDNS、StatefulSet controller、Pod readiness、DNS 缓存和客户端连接池。注意 kube-proxy 不再帮你在 ClusterIP 后面做统一选择，客户端拿到多地址后怎么连、多久刷新、失败后怎么剔除，都变成应用侧问题。

排障时要从 DNS 和 EndpointSlice 看起。确认 Service 是否 `clusterIP: None`，selector 是否选中 Pod，EndpointSlice 里 endpoint 是否 ready，CoreDNS 是否返回预期地址，客户端是否缓存了旧答案。很多 Headless 问题不是 Pod 没跑，而是 DNS 名称、namespace、subdomain 或客户端缓存不对。

## 48. Headless Service 配置错误时会导致哪些线上问题？

可以先这样答：Headless Service 配置错误会让客户端拿不到成员、拿到错误成员，或者拿到地址后无法正确负载均衡。对有状态系统来说，这可能表现为集群节点无法发现彼此、复制失败、分片访问错乱、客户端长时间连旧实例。

selector 错误仍然是高频问题。selector 选不到 Pod，DNS 返回空；selector 选到错误 Pod，客户端把非目标实例当成成员。StatefulSet 场景里，如果 serviceName、Headless Service 名称、Pod subdomain 不一致，稳定 DNS 名称也会失效。

`publishNotReadyAddresses` 要谨慎。某些有状态系统需要在 Pod ready 前就能被其他成员发现，用它是合理的；但如果普通客户端也消费这些地址，就可能把流量打到还没初始化完成的实例。这里要区分“成员发现需要看到未 ready 节点”和“业务请求可以打过去”。

客户端 DNS 缓存和连接池会放大问题。Headless Service 返回的是后端地址集合，客户端如果长时间缓存旧地址，Pod 重建后还会继续连接旧 IP；如果只取 DNS 响应里的第一个地址，流量会倾斜；如果不处理连接失败和刷新，就会把后端变化暴露成业务错误。

端口和协议配置也不能忽略。SRV 记录依赖命名端口，端口名写错会让服务发现结果缺失；Pod 网络策略、CNI、跨节点路由异常时，DNS 看起来正确但直连失败。Headless Service 把更多路径交给客户端，排障时必须把 DNS 结果、客户端选择逻辑和实际 TCP/UDP 连通性放在一起看。

## 49. Headless Service 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Headless Service 把一部分负载均衡责任从 Kubernetes 数据面移到客户端或应用协议。它增强了服务发现的实例可见性，但也要求客户端正确处理多地址、健康、缓存、重试和连接池。

对负载均衡来说，普通 Service 通常由 kube-proxy、eBPF 或网关数据面选择后端；Headless Service 返回多个 Pod IP 后，客户端自己选。客户端如果实现了 round-robin、least-load、分片路由或 broker 元数据协议，效果很好；如果只是取第一个地址，流量可能严重倾斜。

对服务发现来说，Headless Service 更透明。调用方能看到具体实例，StatefulSet 能获得稳定成员名。这对 Kafka、ZooKeeper、数据库副本、分片服务很有价值。但透明也意味着暴露了 Pod 生命周期，客户端必须接受地址会变、成员会增减、DNS 有 TTL。

对弹性伸缩来说，扩容新 Pod 后，DNS 会出现更多地址，但客户端是否真的把流量打过去，取决于刷新频率、连接池、分片再均衡和应用协议。缩容时也一样，DNS 不再返回某个地址，不代表所有客户端马上断开旧连接。

对可观测性来说，Headless Service 需要更细的 endpoint 观测。要看每个 Pod 的连接数、请求量、错误率、DNS 返回分布、客户端选择分布、缓存 TTL、旧地址访问量。只看 Service 级别指标，很容易掩盖某个成员完全没流量或某个成员被打爆的问题。
## 50. Ingress 在云原生系统中解决什么问题？

可以先这样答：Ingress 解决的是“把集群外部的 HTTP/HTTPS 流量按域名和路径转发到集群内 Service”的问题。它给入口 HTTP 路由提供一个 Kubernetes API 对象，让应用团队不用直接操作外部负载均衡器、Nginx 或云厂商控制台。

Ingress 常见能力包括对外暴露 URL、基于 host/path 做路由、TLS 终止、虚拟主机、基础负载分发和入口层配置。它比 Service 更懂 HTTP 入口语义，但比 API Gateway 或 Service Mesh 通常更轻。它的核心目标是把外部请求导入集群内的 Service。

Ingress 本身只是资源声明，真正执行流量处理的是 Ingress Controller。官方文档也提醒，只创建 Ingress 对象没有效果，必须有 controller 去 watch 这些对象并配置数据面。这个 controller 可以是 Nginx Ingress Controller、HAProxy、Traefik、Envoy、云厂商 ALB/GCLB 控制器等。

Ingress 不适合所有协议。标准 Ingress 主要面向 HTTP 和 HTTPS，不暴露任意 TCP/UDP 端口。暴露非 HTTP 服务通常要用 Service type=LoadBalancer、NodePort、专门网关、云 LB 或 Gateway API 的相应 Route 类型。

面试里可以这样收束：Ingress 是 Kubernetes 早期入口 L7 路由标准，解决外部 HTTP(S) 到内部 Service 的声明式映射；它不是控制器本身，也不是完整 API 管理平台。具体能力和限制，很大程度取决于选用的 Ingress Controller。

## 51. Ingress 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：用户创建 Ingress、Service、Secret 等对象后，Ingress Controller watch API Server，把 Ingress 规则翻译成自己数据面的配置。外部流量先到负载均衡器或入口代理，再由 controller 配置好的数据面按 host、path、TLS 和 backend Service 转发到集群内后端。

Ingress 对象里通常包含 rules、backend、tls。rules 按 host 和 path 匹配请求，backend 指向 Service 和端口，tls 引用保存证书的 Secret。Controller 读取这些对象后，配置 Nginx、Envoy、HAProxy、云 LB 或其他入口组件，让真实流量按规则走。

Service 仍然在链路中。Ingress 后端不是直接写一堆 Pod IP，而是指向 Service。Service 再通过 EndpointSlice 和数据面连接到 Pod。这样 Pod 扩缩容、滚动更新时，Ingress 配置不需要跟着每个 Pod 改，只要 Service 后端集合正确更新。

涉及组件包括 API Server、etcd、Ingress 资源、IngressClass、Ingress Controller、Service、EndpointSlice、Secret、DNS、外部 LB、防火墙/安全组、CoreDNS 或外部权威 DNS，以及具体代理数据面。TLS 证书还可能由 cert-manager 或云证书管理系统维护。

排障时要按入口路径看：公网 DNS 是否指到 LB，LB 是否健康，Ingress Controller 是否拿到规则，证书 Secret 是否正确，IngressClass 是否匹配，Service backend 是否存在，EndpointSlice 是否有 ready endpoint，Pod 是否真的能处理请求。只看 Ingress YAML 通常不够。

## 52. Ingress 配置错误时会导致哪些线上问题？

可以先这样答：Ingress 配置错误会表现为 404、503、502、TLS 握手失败、路由到错误服务、某些路径不生效、证书不匹配，或者对象看起来创建成功但外部完全访问不到。它横跨 Kubernetes API、入口控制器、外部 DNS/LB 和后端 Service，排障面比较宽。

IngressClass 错误很常见。集群里可能有多个 Ingress Controller，如果 Ingress 没有指定正确 class，或者 controller 只 watch 某个 class，对象就不会被期望的控制器处理。用户会看到 Ingress 存在，但数据面没有任何对应配置。

host/path 规则错误会导致流量走错。pathType、前缀匹配、精确匹配、rewrite 注解、默认后端、大小写和尾斜杠，都可能造成请求进入错误 backend。不同 Ingress Controller 对注解和边界行为也不完全一样，所以同一份 YAML 迁移 controller 后可能表现不同。

TLS 错误很直接。Secret 名称、namespace、证书链、私钥、SAN、SNI、默认证书、证书热加载任何一处不对，都可能导致浏览器报证书错误或客户端握手失败。证书更新后 controller 是否 reload，也要看具体实现和日志。

后端配置错误会变成 502/503。Service 名称或端口写错、EndpointSlice 为空、Pod readiness 失败、NetworkPolicy 拦截、上游协议不匹配、超时太短，都会让入口层报 upstream 错误。不要把所有 502 都归因于 Ingress；很多 502 是 Ingress 到 Service 后端这段路径的问题。

## 53. Ingress 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Ingress 影响的是南北向 HTTP(S) 流量入口。它把外部请求按域名、路径和 TLS 配置导向 Service，所以它处在外部负载均衡和集群内服务发现之间，会影响用户流量怎么进入、怎么分到后端，以及入口层能看到哪些指标。

对负载均衡来说，Ingress Controller 通常会和云 LB 或节点入口配合。外部 LB 先把流量分到 Ingress Controller 实例，Controller 再按规则转到 Service 后端。Controller 副本数、节点分布、连接池、upstream keepalive、超时、重试和限流配置，都会影响最终负载分布。

对服务发现来说，Ingress 通过 Service 间接发现 Pod。Deployment 发布、HPA 扩容、Pod readiness 变化，最终都会反映到 Service/EndpointSlice，再被 Ingress Controller 或数据面使用。这个传播链路如果延迟或失配，外部入口就可能继续打到旧后端或看不到新后端。

对弹性伸缩来说，Ingress 层本身也要扩容。只扩业务 Pod，不扩 Ingress Controller，入口代理可能先成为瓶颈。反过来，业务 HPA 的指标如果来自 Ingress 请求量或延迟，要注意采集延迟、重试放大和入口限流，否则扩容判断会偏。

对可观测性来说，Ingress 是最重要的用户入口观测点之一。要看请求量、状态码、TLS 错误、路由命中、上游连接失败、upstream latency、response time、请求大小、客户端 IP、X-Forwarded-For、trace header 和 controller reload 状态。入口指标要能关联到 backend Service、namespace、Ingress 名称和版本，否则排障会断层。

## 54. Gateway API 在云原生系统中解决什么问题？

可以先这样答：Gateway API 解决的是“用更清晰、可扩展、按角色拆分的方式管理 Kubernetes 服务网络入口和高级路由”的问题。它可以看作 Ingress 的后继方向，但不是简单把 Ingress 改个名字，而是把 GatewayClass、Gateway、HTTPRoute、GRPCRoute 等对象拆开，表达基础设施、网关实例和应用路由之间的关系。

Ingress 的模型比较简单，很多高级能力要靠 controller 私有注解实现，比如 Header 匹配、权重分流、跨 namespace 绑定、gRPC 路由、TLS 策略等。注解多了以后，可移植性差，职责也混在一个对象里。Gateway API 的目标就是把这些常见入口和路由能力做成标准 API。

它强调角色分工。基础设施提供者或平台团队管理 GatewayClass 和具体 Gateway，应用团队管理 HTTPRoute/GRPCRoute，把自己的服务挂到允许的 listener 上。这个模型更适合多团队、多租户、多集群和云厂商/自建网关混合的环境。

Gateway API 也更协议感知。HTTPRoute、GRPCRoute 等 Route 类型可以表达更细的路由条件和后端权重。Gateway 则描述真实流量处理基础设施，例如云负载均衡器、集群内代理或边缘网关。具体执行仍然依赖 Gateway controller 和数据面实现。

面试里可以这样说：Gateway API 是更现代的 Kubernetes 服务网络 API，解决 Ingress 表达能力弱、注解碎片化、角色边界不清的问题。它提供声明式模型，但不替代具体网关实现；没有 controller 和 CRD，实现不了真实流量处理。

## 55. Gateway API 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：Gateway API 通过 CRD 定义一组资源。GatewayClass 指定由哪个 controller 实现一类网关；Gateway 表示一个具体流量入口和 listener；HTTPRoute、GRPCRoute 等 Route 把请求匹配规则和后端 Service 绑定到 Gateway listener。Gateway controller watch 这些对象并配置真实数据面。

请求路径可以这样理解：外部流量到达 Gateway 对应的地址或负载均衡器；Gateway 的 listener 接收指定协议、端口和 hostname；Route 根据 host、path、header、method、gRPC service/method 或权重规则选择 backendRef；backendRef 通常指向 Kubernetes Service；Service 再通过 EndpointSlice 找到 Pod。

Gateway API 里有双向绑定和权限边界。Gateway 可以限制哪些 namespace 的 Route 能挂上来，Route 也要声明自己绑定哪个 Gateway。这比传统 Ingress 更适合平台团队和应用团队协作：平台控制入口和安全边界，应用控制自己的路由规则。

涉及组件包括 API Server、etcd、Gateway API CRD、GatewayClass、Gateway、Route、ReferenceGrant、Gateway controller、Service、EndpointSlice、Secret、证书控制器、外部 LB 或代理数据面。不同实现可能是 Envoy Gateway、Contour、Istio、Kong、云厂商 Gateway controller 等。

要注意，Gateway API 规格不是 Kubernetes 核心二进制天然就会执行的能力。官方文档明确说它是 add-on API，规格由 CRD 提供，并由多种实现支持。创建资源后有没有效果，取决于 CRD 是否安装、controller 是否运行、GatewayClass 是否匹配以及实现是否支持对应特性。

## 56. Gateway API 配置错误时会导致哪些线上问题？

可以先这样答：Gateway API 配置错误通常表现为 Gateway 没有地址、Route 没有被绑定、请求 404/503、证书失败、跨 namespace 引用无效、权重分流不符合预期，或者某些实现不支持你写的字段。它的对象更多，状态条件也更丰富，所以要靠 status 排障。

GatewayClass 或 controllerName 错了，controller 不会接管资源。Gateway 可能一直 Pending 或没有 programmed 状态。Gateway 本身 listener 配错，比如协议、端口、hostname、TLS mode、证书引用错误，会导致入口根本无法接流量或 TLS 失败。

Route 绑定错误也很常见。parentRefs 指错 Gateway，namespace 策略不允许 attach，hostnames 不匹配，sectionName 写错 listener，Route status 会显示未被接受或未解析。跨 namespace 引用 Service 或 Secret 时，如果缺少 ReferenceGrant，符合安全模型的实现会拒绝引用。

后端配置错误会进入 503/502。backendRef Service 不存在、端口错误、EndpointSlice 为空、权重写错、协议和后端不匹配、健康检查失败，都会让请求到不了业务。Gateway API 可以表达权重，但不保证后端已经健康；健康判断仍然要靠 Service readiness、数据面或实现自己的健康检查。

实现差异也要看。Gateway API 有 conformance 和支持级别，不同 controller 对某些扩展字段、过滤器、Route 类型支持程度不同。面试里可以提醒：看 YAML 不够，要看 `Accepted`、`Programmed`、`ResolvedRefs` 这类 status condition，以及 controller 日志和数据面配置。

## 57. Gateway API 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Gateway API 把入口负载均衡和 L7 路由从 controller 私有注解提升成更标准的资源模型。它不会改变 Pod 被 Service/EndpointSlice 发现的基本链路，但会让入口流量如何进入、如何按规则分流、如何归属到团队和路由对象变得更清楚。

对负载均衡来说，Gateway 表示入口基础设施，Route 表示请求如何分到后端 Service。权重分流、Header 匹配、路径匹配、gRPC 方法路由等可以更直接地表达。具体负载均衡算法、连接池、健康检查和超时仍然取决于 controller 和数据面。

对服务发现来说，Gateway API 的 backend 通常仍然指向 Service。Service selector、EndpointSlice、Pod readiness 依旧决定最终后端集合。Gateway API 解决的是入口和路由表达，不负责替代 Service 发现，也不负责给每个 Pod 做身份管理。

对弹性伸缩来说，Gateway 层和业务层都要考虑。业务 HPA 扩容后，Route 指向的 Service 后端增加；Gateway Controller 或数据面也要有足够副本和容量承接入口流量。多租户环境下，还要避免某个 Route 的流量把共享 Gateway 打满。

对可观测性来说，Gateway API 提供了更好的资源维度。指标和日志可以按 GatewayClass、Gateway、listener、Route、backendRef、namespace、hostname 拆分。排障时能更清楚地回答“是入口基础设施坏了，还是某条应用路由坏了，还是后端 Service 没有 endpoint”。

## 58. EndpointSlice 在云原生系统中解决什么问题？

可以先这样答：EndpointSlice 解决的是“如何可扩展地表达 Service 后端地址集合”的问题。早期 Endpoints 对象把一个 Service 的所有后端塞进一个对象，后端很多时对象巨大、更新昂贵，还缺少双栈、拓扑和终止状态等信息。EndpointSlice 把后端切成多个 slice，让服务发现更适合大规模集群。

Service 只是稳定抽象，真正后端地址要落到 Pod IP 和端口。EndpointSlice 就是这层数据模型。它记录每个 endpoint 的地址、端口、协议、ready/serving/terminating 条件、nodeName、zone 等信息。kube-proxy、Ingress、Gateway、Mesh、客户端控制器都可以基于它构建后端列表。

它的价值首先是规模。默认情况下，一个 EndpointSlice 通常承载一部分 endpoint，后端变化时只需要更新相关 slice，而不是重写一个巨大的 Endpoints 对象。官方文档也建议客户端使用 EndpointSlice API，而不是旧 Endpoints API。

第二个价值是语义更完整。rolling update 和优雅终止时，endpoint 可能 terminating 但仍在 serving；拓扑感知路由需要知道 endpoint 所在 zone；双栈需要表达 IPv4/IPv6 地址族。这些信息都不是简单 IP 列表能干净表达的。

面试里可以这样总结：Service 是用户面对的服务抽象，EndpointSlice 是控制面传递给数据面和发现组件的后端清单。它越正确，Service、DNS、网关和负载均衡越稳定。

## 59. EndpointSlice 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：EndpointSlice controller watch Service 和 Pod。当 Service 有 selector 时，控制器找到匹配 Pod，根据 Pod IP、端口、readiness、terminating 状态和拓扑信息生成或更新 EndpointSlice。每个 EndpointSlice 通过 `kubernetes.io/service-name` 标签和 ownerReference 关联到 Service。

后端变化时，控制器尽量只更新必要的 slice。新增 Pod、删除 Pod、readiness 变化、端口变化、Pod 终止，都会让对应 EndpointSlice 变化。官方文档里也提到，控制面会优先减少需要发送到每个节点的更新次数，而不是追求 slice 永远填得最满。

消费者很多。kube-proxy watch EndpointSlice 后更新节点转发规则；CoreDNS 用它回答 Service DNS，尤其是 Headless Service；Ingress/Gateway controller 可以用它构建 upstream；Service Mesh 或自研控制器也可能消费或管理自己的 EndpointSlice。

EndpointSlice 还有管理者标签。默认 controller 管理的对象会标出 managed-by，其他系统如果要创建自己的 EndpointSlice，也应该使用不同管理者标识，避免互相覆盖。没有 selector 的 Service 通常就需要手工或外部控制器维护 EndpointSlice。

涉及组件包括 API Server、etcd、Service、Pod、EndpointSlice controller、kube-proxy、CoreDNS、Ingress/Gateway/Mesh controller、kubelet readiness 状态和应用终止流程。排障时要看 Service selector 是否正确、EndpointSlice 是否存在、endpoint 条件是否 ready，以及消费者是否及时同步。

## 60. EndpointSlice 配置错误时会导致哪些线上问题？

可以先这样答：EndpointSlice 错误会直接污染后端清单。线上表现可能是 Service 后端为空、流量打到错误 IP、DNS 返回旧地址、部分节点转发规则不一致、滚动更新时仍打到 terminating Pod，或者 Headless Service 客户端看到重复/缺失成员。

对有 selector 的 Service，常见根因在 selector、Pod label、readiness 和端口命名。EndpointSlice controller 本身只是按对象状态生成后端；如果 selector 选错，它会忠实地产生错误清单。端口名不一致、containerPort 和 Service targetPort 对不上，也会让 endpoint 端口不是调用方想要的端口。

手工维护 EndpointSlice 风险更高。selectorless Service、外部后端或迁移场景里，EndpointSlice 可能由人或外部控制器创建。IP 写错、端口写错、managed-by 冲突、service-name 标签缺失、addressType 不匹配，都会让 Service 指向错误后端。

状态条件错误会影响优雅下线。ready、serving、terminating 的语义如果被消费者忽略或处理不一致，滚动更新时可能过早摘除仍在 drain 的后端，或者继续给已经不该接新流量的 Pod 发请求。长连接服务尤其容易在这个窗口里出错。

还有规模和同步问题。EndpointSlice 数量多、churn 高、kube-proxy 同步慢、watch 堵塞，会让某些节点短时间使用旧后端列表。排查时要按节点看 kube-proxy 或数据面状态，确认问题是控制面对象错，还是消费者同步慢。

## 61. EndpointSlice 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：EndpointSlice 是负载均衡和服务发现的后端事实来源之一。Service、CoreDNS、kube-proxy、Ingress、Gateway 和 Mesh 最终都需要知道“有哪些 endpoint 可以接流量”，这个信息通常就来自 EndpointSlice。

对负载均衡来说，EndpointSlice 决定候选后端集合。endpoint ready 状态、terminating 状态、zone 信息和端口都会影响数据面选择。拓扑感知、本地流量策略、优雅终止、滚动更新都依赖它表达准确状态。

对服务发现来说，普通 Service DNS 解析到 ClusterIP，后端变化主要由数据面消化；Headless Service DNS 直接依赖 EndpointSlice 返回后端地址。EndpointSlice 更新慢或错误，Headless 客户端会立刻感知到错误地址集合。

对弹性伸缩来说，HPA 扩出来的新 Pod 只有进入 ready endpoint，才真正增加服务处理能力。Pod 数量增加但 EndpointSlice 没更新、readiness 没通过、selector 不匹配，都意味着扩容没有进入流量路径。缩容时也一样，EndpointSlice 是后端退出流量池的重要信号。

对可观测性来说，要把 EndpointSlice 当成排障对象。可以观察每个 Service 的 endpoint 数、ready/serving/terminating 分布、slice 数量、更新频率、消费者同步时延、kube-proxy sync 时长和 DNS 响应。很多“负载均衡不均”问题，源头其实是 endpoint 集合和拓扑分布就不均。
## 62. CoreDNS 在云原生系统中解决什么问题？

可以先这样答：CoreDNS 在 Kubernetes 里主要解决集群内 DNS 服务发现问题。业务 Pod 不应该把其他 Pod 的 IP 写死，而是通过 Service 名称访问。CoreDNS 根据 Kubernetes API 里的 Service、EndpointSlice、Pod 等对象，为集群内客户端返回符合 Kubernetes DNS 规范的解析结果。

最常见场景是 Service 名称解析。应用访问 `redis.default.svc.cluster.local`，CoreDNS 对普通 ClusterIP Service 返回 Service 的 ClusterIP；对 Headless Service 返回后端 Pod IP 集合。这样应用可以用名字访问服务，不需要关心 Pod 扩缩容和重建后的地址变化。

CoreDNS 也承担集群内外 DNS 转发的边界。集群内部的 Service 域名由 kubernetes 插件处理，外部域名通常通过 forward 插件转给上游 DNS，例如云 VPC resolver、企业 DNS 或公网递归解析器。它既是 Kubernetes 服务发现的一部分，也是 Pod 访问外部域名时的关键路径。

它不是业务负载均衡器。普通 Service 返回 ClusterIP 后，后续转发由 kube-proxy、eBPF 或其他数据面完成；Headless Service 返回多个地址后，客户端自己选择。CoreDNS 能影响解析结果、延迟和缓存，但不会替应用做重试、熔断、连接池和 L7 路由。

面试里可以这样说：CoreDNS 是集群 DNS 和名称发现层，负责把 Kubernetes 对象转换成 DNS 答案；它的健康直接影响服务发现、启动依赖、配置拉取和外部访问。很多“服务连不上”的问题，第一步都要区分是 DNS 解析失败，还是解析后连接失败。

## 63. CoreDNS 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：CoreDNS 以 Deployment 形式运行在集群内，通常通过 `kube-dns` Service 暴露给 Pod。kubelet 会为 Pod 生成 `/etc/resolv.conf`，把 nameserver 指向集群 DNS，并写入 search domain 和 ndots 等配置。应用发起 DNS 查询后，请求会到 CoreDNS，再由相应插件处理。

CoreDNS 的 kubernetes 插件会连接 Kubernetes API，watch Service、EndpointSlice、Pod、Namespace 等对象，按照 Kubernetes DNS-Based Service Discovery 规范生成记录。普通 Service 返回 ClusterIP，Headless Service 返回 endpoint 地址，命名端口可以生成 SRV 记录。

CoreDNS 是插件式的。常见插件包括 kubernetes、forward、cache、loop、reload、health、ready、prometheus、errors、log 等。cache 插件减少重复查询，forward 插件把非集群域名发给上游 DNS，prometheus 插件暴露请求量、rcode、延迟等指标。Corefile 配置决定这些插件如何串起来。

涉及组件包括 CoreDNS Pod、kube-dns Service、kubelet DNS 配置、API Server、Service、EndpointSlice、Pod readiness、NodeLocal DNSCache、上游 DNS、网络策略和 kube-proxy/Service 数据面。CoreDNS 自己也是 Kubernetes 工作负载，所以它的调度、资源、探针和 Service 都会影响解析链路。

排障时要分层：Pod 的 `/etc/resolv.conf` 是否正确，CoreDNS Pod 是否 ready，Corefile 是否正确，CoreDNS 到 API Server 是否可用，上游 DNS 是否慢，cache 命中率和 SERVFAIL 是否异常，NodeLocal DNSCache 是否拦截或转发正确。节点上直接 `nslookup` 的结果，不一定等于 Pod 内真实解析路径。

## 64. CoreDNS 配置错误时会导致哪些线上问题？

可以先这样答：CoreDNS 配置错误会让大量业务在“发请求之前”就失败。常见现象包括 Service 名称解析失败、偶发 DNS timeout、SERVFAIL、NXDOMAIN、Pod 启动卡住、外部域名无法解析、同一个服务在不同节点解析结果不一致。

Corefile 写错是直接风险。kubernetes 插件域名配置不对，集群域名解析会失败；forward 上游写错，外部域名解析失败；loop 插件发现转发环路，CoreDNS 可能拒绝启动；cache 配置不合理，会让旧答案残留太久或缓存命中率很低。一次 Corefile 变更影响的是所有走集群 DNS 的 Pod。

容量不足也常见。CoreDNS 副本太少、CPU request 太低、节点上 DNS QPS 太高、ndots 导致短名查询被放大、应用每次请求都重新解析，都会让 DNS 延迟上升。业务日志里通常只看到 `lookup xxx: i/o timeout`，真正瓶颈在 CoreDNS、NodeLocal DNSCache 或上游 resolver。

Service 和 EndpointSlice 异常会被误认为 DNS 错误。CoreDNS 按 Kubernetes API 返回结果，如果 Service 不存在、namespace 写错、selector 选不到 endpoint，解析结果就会和调用方预期不同。排查时要同时看 DNS 答案和 Kubernetes 对象，而不是只重启 CoreDNS。

DNS 缓存也会制造发布误判。Headless Service、ExternalName、外部域名、负缓存、应用运行时缓存、JVM DNS cache、NodeLocal DNSCache 都可能让客户端继续使用旧地址。更改 Service 或外部记录后，要考虑 TTL 和连接池，而不是期待所有客户端瞬间切换。

## 65. CoreDNS 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：CoreDNS 直接影响服务发现，间接影响负载均衡和弹性伸缩的最终效果。它不负责转发业务包，但如果名字解析慢、错或不稳定，后面的 Service、Ingress、Gateway、应用连接池都还没机会发挥作用。

对负载均衡来说，普通 ClusterIP Service 的 DNS 只返回 ClusterIP，真正后端选择由数据面完成；Headless Service 的 DNS 返回多个 Pod IP，客户端选择策略会直接影响负载分布。CoreDNS 返回顺序、缓存、TTL、客户端解析库行为，都会影响 Headless 场景的实际流量。

对服务发现来说，CoreDNS 是默认入口。Service 名、短名、namespace 搜索域、集群域名、SRV 记录都靠它。ndots 和 search domain 让短名字很好用，但也可能放大查询次数。生产里要鼓励关键路径使用明确 FQDN 或合理配置解析策略，减少无意义查询。

对弹性伸缩来说，新 Pod ready 后 EndpointSlice 更新，CoreDNS 才能在 Headless 场景返回新地址；普通 Service 则更多依赖数据面更新。CoreDNS 容量不足时，大规模扩容会带来 DNS 查询洪峰，新实例启动、配置拉取、依赖连接都可能被 DNS 拖慢。

对可观测性来说，CoreDNS 指标很有价值。要看请求量、rcode、SERVFAIL/NXDOMAIN、延迟分布、cache hit/miss、forward upstream 延迟、每个 zone 的 QPS、NodeLocal DNSCache 状态。很多微服务链路的首段延迟其实是 DNS，而不是后端服务慢。

## 66. kube-proxy iptables 在云原生系统中解决什么问题？

可以先这样答：kube-proxy 的 iptables 模式解决的是“在每台 Linux 节点上把 Service 虚拟 IP/NodePort 流量转发到后端 Pod”的问题。Service 给用户稳定入口，iptables 模式把这个入口落成内核 netfilter 规则，让访问 ClusterIP、NodePort 或 LoadBalancer 后端节点端口的流量能被 DNAT 到 endpoint。

Kubernetes 的 Pod IP 会变化，Service ClusterIP 却要稳定。iptables 模式下，每个节点上的 kube-proxy watch Service 和 EndpointSlice，然后写入一组 iptables 规则。客户端访问 Service IP 时，内核规则命中，选择一个后端 endpoint，把目标地址改成 Pod IP 和端口。

它解决的是四层转发，不是应用路由。iptables 模式不理解 HTTP path、Header、gRPC method，也不负责 TLS、鉴权、重试和熔断。它只是在节点本地把 Service 虚拟地址和后端地址连接起来，让 Service 抽象能在包转发层生效。

它也让每个节点都能独立处理 Service 流量。Pod 在某个节点访问任意 Service，不需要绕到中心代理；节点本地内核规则就能做转发。这个模型简单、通用、可靠，曾经也是 Kubernetes Linux 节点上最常见的 Service 实现方式。

面试里可以这样说：Service 是 API 抽象，kube-proxy iptables 是其中一种 Linux 数据面实现。它把 Service/EndpointSlice 状态翻译成 netfilter 规则，负责 VIP 到 Pod endpoint 的转发。

## 67. kube-proxy iptables 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：kube-proxy 运行在每个节点上，watch API Server 里的 Service 和 EndpointSlice。当对象变化时，它计算本节点需要的 iptables 规则，并通过内核 netfilter API 更新规则。流量进入节点网络栈后，在相应链上匹配 Service IP、端口和协议，再跳转到后端 endpoint 规则。

规则通常分几层。先匹配 ClusterIP、NodePort 或 LoadBalancer 相关入口，再进入每个 Service 的规则链，最后根据会话亲和或随机概率选择 endpoint。选中后，iptables 使用 DNAT 把目标地址改写成 Pod IP 和 targetPort。返回路径则依赖连接跟踪、路由、CNI 和可能的 SNAT/MASQUERADE 规则。

涉及组件包括 API Server、Service、EndpointSlice、kube-proxy、iptables/netfilter、conntrack、CNI、Pod 网络、NodePort 防火墙规则、kubelet readiness 和云 LB 健康检查。Service 的 externalTrafficPolicy、sessionAffinity、internalTrafficPolicy、NodePort、LoadBalancer 都会影响具体规则。

iptables 模式的性能重点在规则规模和同步。每个 Service 和 endpoint 都会带来规则，集群很大、endpoint churn 高时，规则更新和匹配成本会变明显。新版本 kube-proxy 已经减少了不必要的全量同步，但大规模集群仍要看 `sync_proxy_rules_duration_seconds`、iptables 规则数量和 CPU softirq。

排障时常用链路是：确认 Service 和 EndpointSlice 正确，再看节点上 kube-proxy 是否健康，iptables 规则是否存在，conntrack 是否异常，CNI 路由是否通，NodePort 是否被防火墙拦截。Service 能解析不代表转发规则一定正确。

## 68. kube-proxy iptables 配置错误时会导致哪些线上问题？

可以先这样答：iptables 模式配置错误会表现为 ClusterIP 访问失败、NodePort 不通、部分节点能访问部分节点不能、连接被 reset、流量打不到新 Pod、旧 Pod 下线后仍有连接残留，或者 kube-proxy CPU 飙高导致规则同步延迟。

kube-proxy 没运行或权限不够，节点上就不会有正确规则。它需要较高网络权限来修改内核规则。如果 DaemonSet 覆盖不全、节点上组件崩溃、版本配置不一致，就会出现“同一个 Service 在某些节点正常，在某些节点失败”。

Service 和 EndpointSlice 变化太频繁时，iptables 同步可能落后。发布、扩缩容或大规模 Pod 重建后，某些节点短时间还用旧规则。新 Pod ready 了但没流量，或旧 Pod terminating 了仍被打到，都可能与同步延迟、conntrack 或消费者状态有关。

conntrack 也是常见坑。Service DNAT 依赖连接跟踪，conntrack 表满、旧连接状态异常、长连接遇到 endpoint 变化，都可能导致偶发连接失败。UDP 服务更要小心超时和状态清理，因为问题不会像 TCP 那样明显。

NodePort 和外部流量策略配置错，会影响真实客户端 IP 和节点可达性。`externalTrafficPolicy: Local` 只把流量转到本地 endpoint，没有本地 endpoint 的节点不该接这类流量；防火墙、安全组、节点删除时的健康检查也会影响外部 LB 是否把流量打到节点。

## 69. kube-proxy iptables 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：iptables 模式是 Service 负载均衡的数据面之一。Service 和 EndpointSlice 负责发现“有哪些后端”，kube-proxy iptables 负责在每个节点把访问转给这些后端。它不会改变服务发现对象，但会决定节点上真实包怎么走。

对负载均衡来说，iptables 模式默认在 endpoint 之间做随机选择或按 session affinity 保持会话。它是连接/包转发层的选择，不理解应用请求。如果客户端使用长连接、HTTP/2 或 gRPC，多次应用请求可能复用同一条连接，实际业务流量不会像短连接那样均匀。

对服务发现来说，iptables 模式消费 EndpointSlice，而不是提供 DNS。普通 Service 的 DNS 返回 ClusterIP 后，iptables 才接手转发；Headless Service 直接返回 Pod IP，通常不会走 ClusterIP 规则。理解这点能避免把 DNS 问题和 kube-proxy 问题混在一起。

对弹性伸缩来说，HPA 新增 Pod 后，EndpointSlice 更新，再由 kube-proxy 同步规则，流量才会进新 Pod。扩容速度不仅取决于 Pod 启动，也取决于 endpoint 传播和节点规则同步。大规模扩容时，要看 kube-proxy 是否成为更新瓶颈。

对可观测性来说，要看 kube-proxy 健康、规则同步耗时、iptables 规则规模、conntrack 使用率、NodePort 健康检查、每个 endpoint 请求分布和节点网络错误。iptables 本身不是高级观测系统，所以通常要结合 eBPF、conntrack、proxy metrics、CNI 指标和应用侧指标判断真实路径。

## 70. kube-proxy IPVS 在云原生系统中解决什么问题？

可以先这样答：kube-proxy IPVS 模式同样解决 Service 虚拟 IP 到后端 Pod 的四层转发问题，只是它用 Linux 内核 IPVS 这种负载均衡机制表达 Service，而不是主要靠大量 iptables 规则表达每个转发选择。它曾经的目标是改善大规模 Service 场景下的同步性能和转发吞吐。

IPVS 原本就是 Linux Virtual Server 的内核四层负载均衡能力。kube-proxy 在 IPVS 模式下把 Service 配成 virtual server，把 endpoint 配成 real server，再结合 iptables/ipset/conntrack 等机制处理捕获、SNAT、NodePort 和兼容逻辑。它比纯 iptables 更像传统 L4 负载均衡器的数据结构。

不过要按当前事实回答：Kubernetes 官方文档已经把 IPVS proxy mode 标为 v1.35 deprecated。原因不是它不能转发，而是内核 IPVS API 和 Kubernetes Service API 的所有边界语义并不完全匹配，某些 Service edge case 难以正确实现。新集群选型时不能再简单说“IPVS 一定更先进”。

它仍然值得理解，因为很多存量集群还在用 IPVS，面试和排障里也常见。看到 `ipvsadm`、kube-ipvs0、virtual server、real server、scheduler、ipset、iptables 配合时，要能判断这是 kube-proxy IPVS 数据面，而不是业务自己的 LVS。

面试里可以这样收束：IPVS 模式是 kube-proxy 的一种 Linux Service 数据面实现，曾用于提高规则同步和转发能力；但它已经被官方标记为弃用，理解它有助于维护存量集群，新的网络数据面选型要同时考虑 nftables、eBPF 和云厂商实现。

## 71. kube-proxy IPVS 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：IPVS 模式下，kube-proxy watch Service 和 EndpointSlice，把每个 Service 转换成内核 IPVS virtual server，把每个后端 endpoint 转换成 real server。流量命中 Service IP 和端口后，IPVS 根据调度算法选择 real server，再把包转发到对应 Pod。

IPVS 需要和其他内核机制配合。iptables 或 ipset 常用于把流量导入 IPVS 路径、处理 NodePort、masquerade、externalTrafficPolicy 等逻辑；conntrack 仍然会参与连接状态；CNI 和路由负责 Pod IP 可达。也就是说，IPVS 模式不是“完全不用 iptables”，只是后端选择和虚拟服务表达主要交给 IPVS。

IPVS 支持多种调度算法，例如 round-robin、least connection、source hashing 等。kube-proxy 配置可以选择 scheduler。但 Kubernetes Service 的语义不只是选后端，还包括 topology、sessionAffinity、external/internal traffic policy、终止 endpoint 处理、NodePort 交互等，所以实际行为仍受 kube-proxy 适配层约束。

涉及组件包括 API Server、Service、EndpointSlice、kube-proxy、IPVS 内核模块、ipset、iptables、conntrack、CNI、kubelet readiness、节点内核版本和监控指标。节点必须加载相应 IPVS 模块，否则 kube-proxy 会失败或退化，具体表现取决于版本和配置。

排障时除了看 Service/EndpointSlice，还要看 `ipvsadm -Ln`、`ipset list`、iptables 相关链、kube-proxy 日志、内核模块、conntrack 和路由。只看 Kubernetes 对象正常，不代表 IPVS 表已经正确同步到节点。

## 72. kube-proxy IPVS 配置错误时会导致哪些线上问题？

可以先这样答：IPVS 配置错误会导致 Service 转发失败、部分节点没有 virtual server、后端 real server 不完整、NodePort 异常、源地址策略不符合预期，或者升级后某些 Service 语义和预期不一致。它多了一层 IPVS 表，排障比普通 iptables 更需要节点侧证据。

内核模块和权限是第一类问题。节点没有加载 ip_vs、ip_vs_rr、ip_vs_wrr、ip_vs_sh、nf_conntrack 等相关模块，kube-proxy 可能无法正确创建规则。托管节点或自定义内核裁剪过度时，这类问题更常见。

同步不一致是第二类问题。API 里的 EndpointSlice 已经变化，但某个节点的 IPVS real server 还没更新，流量就会继续打到旧后端或看不到新后端。kube-proxy 重启、watch 中断、规则清理失败、节点上手工改规则，都可能让节点状态和 API 状态漂移。

Service 语义边界要特别小心。官方弃用 IPVS 的重要背景就是它很难完整覆盖 Kubernetes Service API 的所有边界条件。某些拓扑、流量策略、终止 endpoint、NodePort 和防火墙交互，在 IPVS 模式下可能比 iptables/nftables/eBPF 更难保持一致。存量集群升级前要读对应版本变更说明并做回归。

观测配置不全会让故障拖很久。很多团队只看 Pod 和 Service，没看节点 IPVS 表。结果对象正常、DNS 正常，但节点数据面坏了。IPVS 集群要把 kube-proxy 指标、IPVS 连接统计、real server 数、conntrack、节点内核日志都纳入排障工具箱。

## 73. kube-proxy IPVS 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：IPVS 模式影响的是 Service 的节点数据面负载均衡。它消费 Service 和 EndpointSlice 的发现结果，把这些结果编程到 IPVS 表里。它不负责 DNS，也不改变 Service API 的用户模型，但会影响后端选择算法、节点同步成本和排障方式。

对负载均衡来说，IPVS 的调度算法比 iptables 随机规则更接近传统 L4 LB，可以选择 rr、lc、sh 等算法。实际业务分布仍然会受长连接、HTTP/2/gRPC 复用、sessionAffinity、externalTrafficPolicy、本地 endpoint 数量和客户端行为影响。算法好看不等于业务请求一定均匀。

对服务发现来说，IPVS 仍然依赖 EndpointSlice。EndpointSlice 错了，IPVS 表也会跟着错；Headless Service 直接暴露后端地址，通常不走 Service VIP 的 IPVS 转发。不要把 CoreDNS 返回问题说成 IPVS 问题，也不要把 IPVS real server 缺失说成 DNS 问题。

对弹性伸缩来说，新 Pod ready 后要经过 EndpointSlice 更新和 kube-proxy 同步，才会进入 IPVS real server 列表。大规模扩缩容时，IPVS 曾经的优势在于表结构更适合大量后端，但当前选型还要考虑它已弃用，以及 nftables/eBPF/云数据面的成熟度。

对可观测性来说，IPVS 要多看一组节点侧指标。除了 Service、EndpointSlice、kube-proxy sync、conntrack，还要看 virtual server、real server、active/inactive connection、scheduler、内核模块和 kube-ipvs0。维护存量 IPVS 集群时，最实用的排障原则是同时验证 API 期望状态和节点 IPVS 实际状态。
## 74. CNI 在云原生系统中解决什么问题？

可以先这样答：CNI 解决的是“容器或 Pod 怎么接入集群网络”的标准化问题。Kubernetes 不把具体网络方案写死在核心组件里，而是通过 CNI 插件把 Pod 网络交给可替换的数据面实现。这样 Kubernetes 只要求每个 Pod 有自己的 IP、Pod 之间按网络模型互通、Service 能基于这些后端地址工作，至于底层用 bridge、VXLAN、BGP、云 VPC 网卡、eBPF 还是别的方案，由 CNI 插件和运维环境决定。

CNI 首先解决接口边界。容器运行时在节点上创建 Pod sandbox 和 network namespace 后，需要有人把网卡放进去、分配 IP、配置路由、设置宿主机侧 veth、桥、隧道、路由或策略规则。没有统一接口时，每个运行时和每个网络插件都要互相适配。CNI 规范把调用方式、输入参数、返回结果和生命周期动作标准化，运行时只需要按规范调用插件，插件只需要按规范处理 `ADD`、`DEL`、`CHECK`、`STATUS`、`VERSION` 等操作。

它还解决 Kubernetes 网络模型的落地问题。Kubernetes 的 Service、EndpointSlice、NetworkPolicy、Ingress、Gateway、Service Mesh 都默认 Pod 网络已经可达。Service 能不能把流量转到 Pod，EndpointSlice 里的 Pod IP 有没有意义，readiness 通过后新 Pod 能不能接流量，都依赖 CNI 先把 Pod 放进一个可通信的网络。CNI 不是高层服务发现，但它是后面所有服务抽象的地基。

CNI 的另一个价值是让集群网络可以适配不同基础设施。裸金属集群可能选择 BGP 或三层路由；跨子网、云上或无法改底层路由的环境可能选择 VXLAN overlay；云厂商托管集群可能把 Pod IP 分配到 VPC 原生网段；高性能或可观测性要求强的环境可能选 eBPF 数据面。Kubernetes 不强迫统一方案，CNI 让这些实现都能接到同一个 Pod 生命周期里。

面试里要把边界说清楚：CNI 主要负责 Pod 网络接入、IPAM、路由/封装和部分策略执行；kube-proxy 或替代数据面负责 Service VIP 到后端 endpoint 的转发；CoreDNS 负责名称解析；Ingress/Gateway 负责入口路由；Service Mesh 负责 L7 治理。现代 CNI 可能扩展到 Service 负载均衡、NetworkPolicy、可观测性甚至加密，但那是具体产品能力，不是 CNI 规范本身的全部含义。

## 75. CNI 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：CNI 的基本链路是 kubelet 调用容器运行时，容器运行时创建 Pod sandbox 后读取节点上的 CNI 配置，并执行 CNI 插件给这个 sandbox 配网。插件通常会创建 veth、把一端放进 Pod network namespace、给 Pod 分配 IP、配置默认路由和 DNS 相关信息，再在宿主机侧写入桥、路由、隧道、eBPF map、iptables/nftables 或云网卡配置。Pod 删除时，运行时再调用插件清理资源。

从 Kubernetes 组件看，kubelet 是节点上的入口，但 kubelet 不再负责管理 CNI 插件目录和网络插件参数。较新的 Kubernetes 里，CNI 插件由容器运行时按自己的配置加载，典型运行时是 containerd 或 CRI-O。kubelet 通过 CRI 请求运行时创建 Pod sandbox，运行时按 CNI 配置调用插件。插件执行成功后，Pod 才真正拥有可用网络。

CNI 配置通常放在节点的 CNI 配置目录里，指向一个或多个插件。很多实现会用插件链：主插件负责连接 Pod 网络，IPAM 插件负责分配 IP，portmap 插件处理 hostPort，bandwidth 插件处理带宽限制，loopback 插件保证 Pod 内 `lo` 可用。CNI 规范强调的是“运行时如何调用插件”，不是“插件内部必须怎么转发包”。

具体实现差异很大。Flannel 可能用每节点 PodCIDR 加 VXLAN 后端；Calico 可能用 BGP、VXLAN、IP-in-IP、Felix 规则和策略；Cilium 可能用 eBPF 程序和 BPF map；云厂商 CNI 可能调用云 API 绑定 ENI 或分配 VPC IP。它们都通过 CNI 接入 Pod 生命周期，但节点上的设备、路由、策略和观测方式完全不同。

涉及的 Kubernetes 对象和组件包括：Node 的 PodCIDR、Pod sandbox、kubelet、容器运行时、CNI 配置和二进制插件、Service/EndpointSlice、NetworkPolicy、kube-proxy 或替代数据面、CoreDNS 以及云控制器或云 CNI。排查时不能只看 `kubectl get pod`。Pod 显示 Running 但网络不通时，要到节点上看 CNI agent、运行时事件、Pod netns、veth、路由表、隧道设备、BPF/iptables/nftables 规则和 IPAM 状态。

## 76. CNI 配置错误时会导致哪些线上问题？

可以先这样答：CNI 配置错误最直接的后果是 Pod 无法创建或创建后网络不可用。常见现象包括 Pod 长期停在 `ContainerCreating`，事件里出现 `FailedCreatePodSandBox`、`cni plugin not initialized`、IPAM 分配失败；或者 Pod 虽然 Running，但 Pod 到 Pod、Pod 到 Service、Pod 到 DNS、跨节点访问都异常。

IPAM 配错是高频问题。PodCIDR 与节点网段、Service CIDR、VPC 网段或对端集群网段重叠，会导致路由冲突和诡异回包；IP 池太小会让新 Pod 无法分配地址；IP 回收失败会制造“明明删了 Pod 但地址还被占用”的问题；双栈配置不一致会让 IPv4/IPv6 某一侧部分可用。IPAM 问题通常在扩容、节点重建、集群迁移后暴露。

MTU 配错会让问题更隐蔽。VXLAN、Geneve、IP-in-IP 等封装会增加额外头部，Pod 里仍按 1500 发大包时，底层链路可能需要分片或直接丢包。业务表现可能是小请求正常，大响应、TLS 握手、gRPC 流、镜像拉取、数据库查询偶发超时。很多团队会误判成应用慢，其实是 overlay MTU 和路径 MTU 不匹配。

路由和封装配置错误会表现为节点局部故障。比如同节点 Pod 互通，跨节点不通；同可用区正常，跨可用区不通；新节点上的 Pod 不通，老节点正常。原因可能是 PodCIDR 未分配、隧道端点没建立、BGP 邻居不通、云安全组没放行 UDP 4789、内核模块缺失、反向路径过滤或防火墙拦截。CNI agent 状态和节点路由表比应用日志更有价值。

NetworkPolicy 或策略引擎配置错误会造成“连接被静默丢弃”。默认拒绝策略没放行 DNS、健康检查、指标采集、Webhook、数据库或消息队列，就会让应用启动慢、readiness 不通过、HPA 指标缺失、日志采集断流。策略问题的难点是 Service DNS 可能正常，但真正连接被 CNI 数据面拦掉。

升级和混用也有风险。节点上残留旧 CNI 配置、多个 CNI 配置文件顺序错误、containerd 指向的 CNI bin 版本与配置版本不兼容、主机上手工改过 CNI 管理的规则，都可能导致节点之间行为不一致。线上遇到“只有某些节点的 Pod 不通”，要优先怀疑节点本地 CNI 状态，而不是只查业务代码。

## 77. CNI 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：CNI 不直接定义 Service 或 DNS，但它决定 Pod 网络是否可达、Pod IP 是否稳定地进入数据路径、策略是否允许连接、节点转发是否高效。所以它会从底层影响负载均衡、服务发现、扩缩容和观测结果。很多高层组件看起来坏了，根因其实在 CNI。

对负载均衡来说，Service 数据面最终要把包送到 Pod IP。kube-proxy、Cilium eBPF、云 LB 或 Service Mesh 选中了后端，如果 CNI 没有正确配置路由、隧道、策略或回程路径，这个后端就只是 EndpointSlice 里的一行地址。CNI 还会影响源地址保留、SNAT、externalTrafficPolicy、本地后端优先、跨节点转发路径和连接跟踪压力。

对服务发现来说，CoreDNS 和 EndpointSlice 只能告诉客户端“应该访问谁”。Pod IP 能不能互通、Headless Service 返回的地址能不能到达、StatefulSet 的每个 Pod DNS 是否真正可连，都依赖 CNI。尤其是 Headless Service，客户端直接拿 Pod IP 连接，CNI 故障会比普通 ClusterIP 场景更直观地暴露出来。

对弹性伸缩来说，HPA 扩出来的新 Pod 要先被调度，再由 CNI 分配 IP 和接入网络，然后 readiness 通过，最后进入 EndpointSlice。CNI 变慢会拉长扩容生效时间；IP 池耗尽会让扩容卡在创建阶段；NetworkPolicy 没同步会让新 Pod ready 失败或接不到依赖；节点级 CNI agent 崩溃会让一批新 Pod 同时不可用。

对可观测性来说，CNI 决定你能看到哪些网络事实。传统 overlay 插件主要看节点路由、隧道设备、iptables/nftables、conntrack 和接口计数；Calico 可看 Felix、BGP、策略命中；Cilium 可通过 Hubble、BPF map 和 flow log 看到 L3/L4 甚至部分 L7 事件。一个成熟的排障视角是：先确认服务发现对象正确，再确认 CNI 数据面把这些对象变成了可达路径。

## 78. Calico 在云原生系统中解决什么问题？

可以先这样答：Calico 解决的是 Kubernetes 集群里 Pod 网络、网络策略和网络可观测性的落地问题。它既可以作为 CNI 给 Pod 提供三层连通，也可以作为 NetworkPolicy 执行引擎做东西向访问控制，还能在一些模式下承担路由发布、overlay 封装、服务负载均衡加速和流量观测能力。

Calico 的核心思路偏三层网络。它不要求一定用二层大广播域去承载 Pod，而是把 Pod 网段当成可路由地址，通过节点路由、BGP、VXLAN、IP-in-IP 或 Felix 编程的路由让不同节点上的 Pod 互通。能让底层网络感知 Pod 网段时，可以少用封装，减少包头开销；底层网络不能感知 Pod 网段时，再用 overlay。

它也常被用来做 NetworkPolicy。Kubernetes 原生 NetworkPolicy 只是 API 语义，真正执行要靠网络插件。Calico 支持 Kubernetes NetworkPolicy，也提供自己的 Calico NetworkPolicy 和 GlobalNetworkPolicy，用于更细的选择器、全局策略、主机端点和分层策略。生产里常见做法是先用 namespace 级默认拒绝，再逐步放行业务必需流量。

Calico 的价值不只是“能通”。在多租户、微服务和零信任场景里，真正需要的是：默认不要横向乱通，业务之间按标签、命名空间、服务边界和端口显式授权；节点、Pod、服务和外部网络之间的路由关系可解释；出了问题能从 Felix、BGP、iptables/nftables/eBPF 或流日志里找到证据。

面试里可以这样收束：Calico 是一个以路由和策略见长的 Kubernetes 网络方案。它既能解决 Pod 网络接入，也能解决网络隔离和策略执行，但具体行为取决于它运行在 BGP、VXLAN、IP-in-IP、eBPF 还是云集成模式下。不要只把它背成“一个 CNI 插件”。

## 79. Calico 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：Calico 通常以节点 DaemonSet 和控制面组件的方式运行。节点上的 Calico agent 负责根据 Kubernetes API 和 Calico 自己的配置，把 Pod 地址、路由、策略和隧道状态编程到节点网络栈里。Pod 创建时，Calico CNI 插件给 Pod 分配 IP、创建接口并接入节点网络；随后 Felix、BGP 组件或 overlay 机制负责跨节点可达和策略执行。

Calico 的关键组件包括 CNI 插件、Felix、BIRD 或替代的路由组件、Typha、calico-kube-controllers、IPAM 和数据存储。CNI 插件处理 Pod 生命周期里的 ADD/DEL；Felix 运行在每个节点上，负责写入路由、策略规则、接口配置和 endpoint 状态；BIRD 常用于 BGP 路由发布；Typha 在大集群里减少每个节点直接 watch API Server 的压力；kube-controllers 负责同步 Kubernetes 资源和 Calico 资源状态。

路由模式要分开讲。无封装模式下，Calico 通过三层路由把 Pod 网段发布给其他节点或网络设备，性能简单直接，但要求底层网络能路由 Pod CIDR。VXLAN 或 IP-in-IP 模式下，Calico 会把跨节点 Pod 流量封装起来，让底层网络只需要能路由节点 IP。Cross-subnet 模式则只在跨子网时封装，减少不必要的封装开销。

策略执行通常由 Felix 根据 Kubernetes NetworkPolicy、Calico NetworkPolicy、GlobalNetworkPolicy 等对象生成节点本地数据面规则。不同版本和配置可能使用 iptables、nftables 或 eBPF。策略选择基于 Pod label、namespace label、IP block、端口、协议等信息。这里要注意：Kubernetes API 接受 NetworkPolicy 不代表策略一定生效，必须网络插件支持并正确执行。

涉及的 Kubernetes 组件包括 API Server、Node、Pod、Namespace、NetworkPolicy、Service/EndpointSlice、kubelet、containerd/CRI-O、kube-proxy 或替代数据面，以及底层云网络/BGP peer。排障时要同时看 Kubernetes 对象和 Calico 自己的状态，例如 IPPool、BGPConfiguration、FelixConfiguration、节点路由、BGP 邻居、策略命中和 `calico-node` 日志。

## 80. Calico 配置错误时会导致哪些线上问题？

可以先这样答：Calico 配置错误会同时影响连通性、安全边界和节点稳定性。它不是只会让 Pod 起不来；更常见的是某些节点、某些命名空间、某些跨子网路径或某些策略命中的连接不通。线上表现可能是 DNS 超时、Service 偶发失败、跨节点访问失败、BGP 路由丢失、NetworkPolicy 误拦、节点 CPU 飙高或连接跟踪压力异常。

IPPool 和封装模式配错很常见。Pod 网段与 VPC、Service CIDR、节点网段、对端集群网段重叠，会出现路由冲突；VXLAN/IP-in-IP 没按底层网络能力配置，会导致跨子网不通；MTU 没扣除封装头，会出现大包丢失；不同节点使用不一致的 IPPool 或封装模式，会让故障呈现为“只有一部分 Pod 不通”。

BGP 模式下，BGP 邻居、ASN、路由反射器、ToR 配置和网络设备过滤都会变成业务可用性的依赖。BGP session 掉线后，Pod 路由可能还在某些节点本地正常，但跨节点或外部到 Pod 不通。路由收敛慢也会影响滚动发布和扩容后新 Pod 的可达性。面试里要敢说：BGP 模式性能和可解释性好，但对网络团队协作要求更高。

策略错误会直接制造生产事故。默认拒绝策略如果没有放行 CoreDNS、健康检查、指标采集、Webhook、API Server、镜像仓库、数据库或消息队列，应用会表现为启动失败、readiness 失败、HPA 指标缺失或调用链断开。Calico GlobalNetworkPolicy 作用范围更大，误写一个全局 deny 可能影响多个 namespace 甚至节点流量，所以发布策略要分阶段验证。

组件配置和容量也会出问题。Typha 不足会让大集群 watch 压力变高；Felix CPU 过高会让策略和路由下发变慢；calico-node 权限或内核模块不足会导致节点状态不一致；iptables/nftables/eBPF 后端切换不完整会留下旧规则。遇到 Calico 问题，不能只看 Pod 事件，要把 IPAM、路由、策略、节点 agent 和底层网络放在同一张图里排查。
## 81. Calico 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Calico 影响的是服务流量能不能在节点和 Pod 之间正确到达、策略是否允许、路由是否收敛、封装是否高效。它不替代 CoreDNS，也不一定替代 kube-proxy，但它决定 Service 后端 Pod IP 的真实可达性和安全边界。Calico 配置得好，Service、Ingress、Gateway、HPA 扩容后的后端才有稳定网络基础。

对负载均衡来说，Calico 决定后端 Pod 是否能被访问。Service 数据面选中一个 Pod 后，如果 Calico 路由缺失、VXLAN 隧道不通、BGP 未收敛或策略拒绝，负载均衡就会表现为某些后端失败。Calico eBPF 模式还可能接管部分 Service 负载均衡路径，这时排障对象会从 kube-proxy 规则转向 Calico 的 eBPF 程序、map 和 Felix 状态。

对服务发现来说，Calico 不负责生成 DNS 记录，但会影响 DNS 查询和查询后的连接。默认拒绝 egress 没放行 CoreDNS，会让服务发现直接失败；Headless Service 返回 Pod IP 后，Calico 路由和策略决定这些地址能不能连；跨 namespace 调用时，策略选择器和 namespace label 也会影响真实可达性。

对弹性伸缩来说，新 Pod 进入服务池之前必须完成 CNI ADD、IP 分配、路由/隧道编程和策略同步。Calico IPPool 耗尽会阻塞扩容；Felix 下发慢会让新 Pod ready 之后仍短暂不可达；BGP 收敛慢会让外部或跨节点流量暂时打不到新后端。缩容时，旧 Pod 的路由和策略清理也会影响连接收尾和资源回收。

对可观测性来说，Calico 提供了比普通 CNI 更丰富的网络排障入口。可以观察 IPAM 分配、BGP 邻居、路由、Felix 指标、策略命中、丢包、节点间隧道和流日志。很多“负载不均”并不是上层算法问题，而是后端分布、跨区路由、策略或节点局部网络状态导致某些后端实际不可用。

## 82. Cilium 在云原生系统中解决什么问题？

可以先这样答：Cilium 解决的是 Kubernetes 网络、安全和可观测性的数据面现代化问题。它用 eBPF 在 Linux 内核里实现 Pod 连通、NetworkPolicy、Service 负载均衡、可观测性、部分 L7 策略和多集群能力，目标是减少传统 iptables 大规则集、sidecar 重路径或单纯黑盒网络带来的性能和排障问题。

Cilium 的基础能力仍然是 CNI：给 Pod 分配网络身份，把 Pod 接入集群网络，让 Pod 到 Pod、Pod 到 Service、Pod 到外部网络按预期工作。它的差异在于大量逻辑通过 eBPF 程序和 BPF map 执行，包在 veth、tc、socket 或 XDP 等路径上就能被处理，不必总是绕到用户态代理或依赖大量 netfilter 规则。

安全上，Cilium 强调基于 identity 的策略。传统策略往往看 IP 和端口，但 Pod IP 会变。Cilium 会把 Kubernetes label、namespace 等信息转成安全身份，再在数据面按身份、L3/L4/L7 条件执行策略。这样策略能随着 Pod 重建和扩容移动，而不是绑定在一批短暂 IP 上。

可观测性上，Cilium 生态里的 Hubble 能从 eBPF 采集流量信息，回答“谁在和谁通信、连接为什么失败、是 DNS、TCP 还是 HTTP 问题”等问题。对微服务系统来说，这比只看应用日志或节点抓包更接近真实调用链。

面试里要注意边界：Cilium 不是 eBPF 的同义词，也不是所有场景都自动比传统方案好。它是一个使用 eBPF 的网络系统。它能替代 kube-proxy、做服务负载均衡和策略，但前提是内核版本、启用参数、云网络、安全组、BPF map 容量和运维工具都配套。

## 83. Cilium 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：Cilium 以 CNI 插件、每节点 Cilium agent、operator 和一组 eBPF 程序协同工作。Pod 创建时，CNI 插件把 Pod 接入网络；Cilium agent watch Kubernetes API，获取 Pod、Endpoint、Service、EndpointSlice、NetworkPolicy 等状态；然后把身份、策略、服务后端和路由信息写入 BPF map，并把 eBPF 程序挂到节点和 Pod 的网络路径上执行。

Cilium 的数据面通常围绕几个对象运转：endpoint 表示本节点上的受管工作负载，identity 表示由标签计算出的安全身份，policy 表示允许或拒绝的连接，service map 表示 Service 前端和后端映射，conntrack/NAT map 表示连接和地址转换状态。包经过数据面时，eBPF 程序根据这些 map 决定放行、拒绝、转发、负载均衡、NAT 或记录事件。

如果启用 kube-proxy replacement，Cilium 会用 eBPF 实现 ClusterIP、NodePort、LoadBalancer 等 Service 转发能力。Cilium 文档里明确把“无 kube-proxy 的 Kubernetes”作为一种模式；这时 kube-proxy 不再是 Service 数据面的主执行者，Service 选择和 NAT 主要由 Cilium BPF 程序完成。是否启用、启到什么程度，要看集群配置。

策略方面，Cilium 支持 Kubernetes NetworkPolicy，也支持 CiliumNetworkPolicy 和 CiliumClusterwideNetworkPolicy。L3/L4 策略主要在内核路径执行；L7 策略通常会涉及代理组件，因为 HTTP、Kafka、DNS 等协议语义需要解析。Hubble 则从 Cilium agent 和 eBPF 事件里拿 flow 信息，提供 CLI、Relay、UI 和指标。

涉及的组件包括 API Server、Pod、Node、Service、EndpointSlice、NetworkPolicy、Cilium agent、Cilium operator、CNI 插件、BPF 程序和 map、Hubble、kube-proxy 或其替代关系、CoreDNS、云 LB/安全组以及底层 Linux 内核能力。排障时要同时看 Kubernetes 期望状态和节点上的 BPF 实际状态，否则容易出现“对象都对，但数据面没同步”的误判。

## 84. Cilium 配置错误时会导致哪些线上问题？

可以先这样答：Cilium 配置错误会表现为 Pod 网络不通、Service 转发异常、NetworkPolicy 误拦、DNS/HTTP 观测缺失、节点间行为不一致，严重时还会让 kube-proxy replacement、NodePort、LoadBalancer 或 egress gateway 的路径出问题。因为 Cilium 数据面在内核里，很多故障在应用日志里只剩 timeout、reset 或 connection refused。

内核和 BPF 能力不匹配是第一类问题。内核版本太旧、BPF 功能缺失、挂载点或权限不正确、BPF map 容量不足，都可能导致 agent 启动失败、程序加载失败或运行中丢状态。托管 Kubernetes 节点镜像、内核参数和安全加固策略也可能限制 BPF 使用。升级内核或切换节点池后，必须验证 Cilium agent 和数据面能力。

kube-proxy replacement 配错会直接影响 Service。比如 kube-proxy 没跑，但 Cilium 的替代模式没完全启用；NodePort 设备选择不对；DSR/SNAT 模式与云 LB、安全组或回程路由不匹配；Maglev、session affinity、externalTrafficPolicy、source ranges 等配置理解错误。现象可能是 ClusterIP 正常但 NodePort 不通，或者内网访问正常、外部访问失败。

策略配置错误会让问题更像业务故障。Cilium identity 依赖标签和命名空间元数据，标签选择器写错会误放行或误拒绝；L7 策略如果没有考虑探针、DNS、TLS、HTTP/2 或 gRPC，会出现部分方法失败；默认 deny 没放行 CoreDNS、API Server、metrics 或日志采集，会让基础设施流量被挡住。

观测配置不完整会拖慢排障。Hubble 没启、Relay 证书错、flow buffer 太小、Prometheus 指标没采集，遇到网络事故时就只能回到传统抓包和节点日志。Cilium 的好处是可观测性很强，但前提是提前启用并把关键指标纳入告警，例如 agent 状态、endpoint regeneration、policy verdict、drop reason、BPF map pressure、service translation 和 DNS 错误。

## 85. Cilium 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Cilium 对这四类能力的影响比传统 CNI 更直接，因为它可能同时承担 Pod 网络、NetworkPolicy、Service 负载均衡和观测数据采集。启用 kube-proxy replacement 后，Cilium 不只是“让 Pod 能通”，还会成为 Service 流量选择和转发的数据面。

对负载均衡来说，Cilium 可以在 eBPF 路径里处理 ClusterIP、NodePort、LoadBalancer、session affinity、Maglev、一致性哈希、DSR/SNAT 等能力。好处是减少 iptables 大规则集和部分连接跟踪压力，路径更短；代价是排障要看 BPF map、Cilium service list、agent 日志和 Hubble flow，而不是只看 `iptables-save` 或 kube-proxy 指标。

对服务发现来说，CoreDNS 仍然负责名字解析，EndpointSlice 仍然表达后端集合，但 Cilium 会消费这些状态，把 Service 前端和后端写到 BPF map。Headless Service 返回 Pod IP 时，Cilium 的路由和策略决定这些 Pod IP 是否可达；普通 Service 返回 ClusterIP 时，Cilium 的 Service LB 决定包最终到哪个后端。

对弹性伸缩来说，新 Pod 上线会触发 endpoint identity、策略和 Service 后端同步。Cilium endpoint regeneration 过慢、BPF map 压力大、agent watch 延迟或策略计算慢，都可能让 Pod 已经 ready 但数据面还没完全接好。缩容时，terminating 后端是否及时从 Service map 移除，也会影响长连接和新连接。

对可观测性来说，Cilium 和 Hubble 的价值很大。它能把连接、丢包、策略裁决、DNS、HTTP、Kafka 等信息与 Kubernetes identity 关联起来，直接看到哪个 namespace、哪个 workload、哪个身份之间通信失败。面试里可以强调：Cilium 把“网络是不是通”变成“哪条流、被哪个规则、在哪一层处理”的问题，但这要求团队会看 Cilium 自己的指标和工具。

## 86. Flannel 在云原生系统中解决什么问题？

可以先这样答：Flannel 解决的是 Kubernetes Pod 跨节点三层连通问题。它的定位比较克制：给每个节点分配一段 Pod 子网，然后通过 VXLAN、host-gw 或云厂商后端等机制，把不同节点上的 Pod 子网连起来。它让 Kubernetes 的“每个 Pod 有唯一、可路由 IP”模型在简单集群里快速落地。

Flannel 的价值在于简单。很多集群只需要 Pod 之间能通，不需要复杂策略、BGP、多集群、L7 可观测或 eBPF Service 替代。Flannel 用较少组件完成基础网络，适合学习环境、小中型集群、K3s 这类发行版默认网络，或者运维团队希望先降低网络系统复杂度的场景。

它不主打 NetworkPolicy。Flannel 项目自己的说明也把网络策略交给其他项目，例如 Calico。也就是说，如果集群需要 namespace 间隔离、默认拒绝、零信任东西向访问控制，只装 Flannel 通常不够，需要叠加能执行 NetworkPolicy 的方案，或者选择内置策略能力的 CNI。

Flannel 常见后端里，VXLAN 最通用。底层网络只需要节点之间可达，并放行 VXLAN 使用的 UDP 端口；Pod 网段不需要被物理网络直接感知。host-gw 模式则不封装，直接在节点间写路由，性能更直接，但要求节点之间在同一个二层网络或底层网络能正确路由下一跳。

面试里可以这样定位：Flannel 是一个偏基础、偏连通性的 CNI。它把 Pod 网络打通，但不试图承包所有 Kubernetes 网络治理能力。生产里选不选它，要看是否接受缺少内建策略、观测和高级负载均衡能力，以及底层网络和 MTU 是否能稳定支撑。

## 87. Flannel 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：Flannel 在每个节点上运行 `flanneld`，从一个预先配置的大 Pod 网段里为每个节点申请一个子网租约，并把这些子网租约和节点地址记录到 Kubernetes API 或 etcd。节点上的 Pod 从本节点子网里拿 IP；跨节点访问时，Flannel 根据目标 Pod IP 所属的节点子网，把流量转发到对应节点。

在 Kubernetes 里，Flannel 通常使用 kube subnet manager，也就是通过 Kubernetes API 存储和读取网络配置、节点子网、节点公网/内网地址等信息。节点上的 Flannel CNI 插件负责把 Pod 接入本机网络；`flanneld` 负责维护跨节点转发所需的设备、路由或隧道状态。容器运行时创建 Pod sandbox 时，通过 CNI 调用把 Pod 网卡接好。

VXLAN 后端下，Flannel 会创建类似 `flannel.1` 的 VXLAN 设备。Pod 发往远端 PodCIDR 的流量先到宿主机，再被封装成外层 UDP/VXLAN 包发往目标节点，目标节点解封装后送到目标 Pod。底层网络只看到节点 IP 到节点 IP 的 UDP 流量，不需要知道 Pod IP 路由。

host-gw 后端下，Flannel 不封装，而是在节点路由表里写入“某个 Pod 子网经由某个节点 IP”的路由。包直接按三层路由走，性能和 MTU 更简单，但要求节点之间网络拓扑支持这种下一跳可达。云环境或跨子网环境不一定适合 host-gw。

涉及组件包括 kubelet、容器运行时、CNI 插件、`flanneld` DaemonSet、Node PodCIDR、Kubernetes API 或 etcd、宿主机路由表、VXLAN 设备、bridge/veth、CoreDNS、Service/kube-proxy 和底层防火墙。Flannel 负责 Pod 网络可达，Service VIP 转发仍通常由 kube-proxy 或其他 Service 数据面完成。
## 88. Flannel 配置错误时会导致哪些线上问题？

可以先这样答：Flannel 配错时，最常见的是跨节点 Pod 网络不通。因为它的职责比较集中，所以故障也常围绕 PodCIDR、子网租约、VXLAN/host-gw 后端、MTU、节点地址和防火墙展开。现象可能是同节点 Pod 正常，跨节点 Pod 超时；某些节点上的 Pod 全部不通；新节点加入后只有新 Pod 不通；Service DNS 正常但连接后端失败。

PodCIDR 和 Flannel 网络配置不一致是第一类问题。kube-controller-manager 分给节点的 PodCIDR、Flannel 配置里的 `Network`、实际 CNI IPAM 使用的网段必须对齐。网段不一致会导致 Pod 拿到的地址不属于 Flannel 预期子网，远端节点不知道怎么回包。自定义 PodCIDR 时，如果只改了 kubeadm 或集群参数，没有改 Flannel manifest，也会出现这种问题。

VXLAN 后端常见问题是端口、防火墙和 MTU。VXLAN 通常需要节点间 UDP 4789 可达，安全组、主机防火墙或云防火墙拦截后，跨节点流量会直接失败。MTU 没扣除 VXLAN 头时，小包能通，大包、TLS、HTTP/2、gRPC 或数据库响应失败。这个问题容易被误判成应用层超时。

host-gw 后端常见问题是底层网络不满足假设。host-gw 需要节点之间能把彼此当下一跳直接路由，跨二层、跨 VPC、跨复杂云路由时不一定成立。路由表看起来有目标 PodCIDR，但下一跳不可达，最终表现为跨节点不通。host-gw 性能好，但对网络拓扑要求比 VXLAN 更硬。

节点地址选择也会踩坑。多网卡节点、NAT 节点、内外网 IP 混用、云上节点地址变化，可能让 Flannel 把错误的节点 IP 写入后端数据。远端节点把 VXLAN 包发到不可达地址，业务只看到连接超时。排查时要看 Node 地址、Flannel lease、`flannel.1`、路由表和实际抓包，而不是只看 Pod IP。

还有一个边界要讲清楚：Flannel 不执行 Kubernetes NetworkPolicy。团队如果以为创建 NetworkPolicy 后流量就会被限制，实际上可能完全没有效果。这不是 Flannel “坏了”，而是能力边界不匹配。需要策略时要叠加策略引擎或换用支持策略的 CNI。

## 89. Flannel 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Flannel 主要影响 Pod IP 的跨节点可达性。它不生成 DNS，不做高级 Service 负载均衡，也不提供复杂策略，但 Service、EndpointSlice、CoreDNS、HPA 这些上层能力都要建立在 Pod 网络可达之上。所以 Flannel 出问题时，上层通常表现为“服务发现得到的地址连不上”或“扩出来的 Pod 没有真正形成容量”。

对负载均衡来说，kube-proxy 或其他 Service 数据面选中某个后端 Pod 后，包必须经 Flannel 的跨节点网络到达目标节点。Flannel VXLAN 不通时，Service 会表现为部分请求失败，尤其是后端分布在多个节点时。长连接场景下，如果连接恰好落到故障节点上的后端，错误会持续一段时间。

对服务发现来说，普通 Service DNS 返回 ClusterIP，后续转发仍依赖 Pod 网络；Headless Service 返回 Pod IP，更直接依赖 Flannel。StatefulSet 使用 Headless Service 暴露 `pod-ordinal.service` 时，如果 Flannel 跨节点路由错误，客户端拿到正确 DNS 也连不上。

对弹性伸缩来说，HPA 扩容出的 Pod 被调度到新节点后，需要 Flannel 给该节点分配子网并让其他节点知道这个子网。如果新节点 Flannel 没起来、PodCIDR 未分配、子网 lease 异常或 VXLAN 设备没建好，新 Pod 就算 ready，也无法承接跨节点流量。扩容效果会低于副本数显示的结果。

对可观测性来说，Flannel 本身提供的语义比较少，更多要靠节点网络证据。要看 `flanneld` 日志、subnet lease、路由表、VXLAN 设备、接口 MTU、UDP 4789 抓包、节点间连通性、kube-proxy 指标和应用侧错误。相比 Cilium 或 Calico，Flannel 的排障更偏 Linux 网络路径，而不是策略对象或身份模型。

## 90. VXLAN 在云原生系统中解决什么问题？

可以先这样答：VXLAN 解决的是“底层三层网络不感知 Pod/虚拟网络地址时，如何仍然让这些地址跨节点通信”的 overlay 问题。它把内层二层帧或虚拟网络流量封装到外层 UDP/IP 包里，让底层网络只需要能路由节点 IP。Kubernetes CNI 里常用 VXLAN 来承载跨节点 Pod 流量，尤其是在无法修改物理网络路由、不能跑 BGP 或跨云/跨子网环境里。

VXLAN 的背景来自数据中心虚拟化和多租户。传统 VLAN 只有 12 位 ID，最多 4094 个 VLAN，面对大规模租户和虚拟网络隔离不够用；物理网络也未必愿意学习大量虚拟机或 Pod 的 MAC/IP。VXLAN 用 24 位 VNI 标识虚拟网络段，可以在共享物理网络上承载更多隔离域。

在 Kubernetes 里，VXLAN 的意义通常不是为了“拉伸二层给 Pod 用”，而是为了让 PodCIDR 在底层网络不可见时仍可跨节点互通。Flannel、Calico、Cilium 等都可以使用 VXLAN 或类似隧道模式。节点之间只要外层 IP 可达并放行对应 UDP 端口，内层 Pod 地址就可以通过封装跨过去。

它也降低了网络团队的接入门槛。没有 VXLAN 时，想让每个节点上的 PodCIDR 被全网路由，可能要改 ToR、云路由表、BGP、VPC 路由或专线配置。VXLAN 把复杂度收进节点上的 CNI 数据面，底层网络只处理节点 IP 之间的普通三层流量。

代价是额外封装和排障复杂度。VXLAN 增加包头，降低有效 MTU；隧道端点、FDB、ARP/邻居、UDP 端口、防火墙、checksum offload 都可能影响结果。面试里不能只说“VXLAN 能跨主机”，还要补一句：它用封装换部署灵活性，性能、MTU 和可观测性要一起管理。

## 91. VXLAN 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：VXLAN 通过 VTEP 在源节点把内层报文封装成外层 UDP/IP 包，在目标节点解封装后再交给目标 Pod 网络。VTEP 可以理解为隧道端点，负责发起和终止 VXLAN 隧道；VNI 用来标识 overlay 网络段。对底层网络来说，它只看到节点 IP 之间的 UDP 流量；对 Pod 来说，它像是在一个可达的虚拟网络里通信。

一次跨节点 Pod 访问可以这样串：Pod A 发包给 Pod B 的 IP，源节点路由判断目标 PodCIDR 在另一个节点，于是把包交给 VXLAN 设备；源 VTEP 在外层加上源节点 IP、目标节点 IP、UDP 端口和 VXLAN 头；底层网络按节点 IP 路由；目标节点收到后解封装，恢复内层包，再按本机路由送进目标 Pod 的 veth 或网络路径。

在 Kubernetes 里，VXLAN 本身不是 Kubernetes API 对象，而是 CNI 数据面的实现方式。涉及组件包括 kubelet、容器运行时、CNI 插件、CNI agent、Node PodCIDR、Pod、Service/EndpointSlice、kube-proxy 或替代 Service 数据面、宿主机 VXLAN 设备、路由表、防火墙和底层网络。Kubernetes 只关心 Pod 网络满足模型，VXLAN 怎么建由插件实现。

不同 CNI 对 VXLAN 的控制方式不同。Flannel 用 subnet lease 知道每个节点的 PodCIDR 和 VTEP 地址；Calico 可以在 VXLAN IPPool 下由 Felix 写路由；Cilium 可以用 eBPF 和隧道模式维护 endpoint 到节点的映射。看起来都叫 VXLAN，但具体路由表、FDB、BPF map 和观测命令并不一样。

排障时要沿着内外两层看。内层看 Pod IP、PodCIDR、Service 后端、NetworkPolicy；外层看节点 IP 可达、UDP 端口、防火墙、安全组、VXLAN 设备、MTU 和隧道端点映射。只在 Pod 里 `curl` 不通，无法直接证明是应用、Service 还是 VXLAN。

## 92. VXLAN 配置错误时会导致哪些线上问题？

可以先这样答：VXLAN 配置错误通常会造成跨节点通信失败或大包偶发失败。同节点 Pod 正常、跨节点 Pod 不通，是最典型的信号。进一步看，可能是所有跨节点都不通，也可能只有跨可用区、跨子网、新节点或某些节点不通。

端口和防火墙是第一类问题。VXLAN 需要节点间对应 UDP 端口可达，常见是 4789，但具体实现可配置。云安全组、主机防火墙、网络 ACL、企业防火墙拦住后，隧道包到不了目标节点。业务侧只会看到连接超时，目标 Pod 根本收不到内层包。

MTU 是第二类问题。VXLAN 增加外层 IP/UDP/VXLAN 头，Pod 内 MTU 如果仍按底层链路最大值配置，就可能在封装后超过路径 MTU。表现很隐蔽：ping 小包通，HTTP 小响应通，大响应、TLS 握手、gRPC stream、数据库结果集、镜像拉取失败。正确做法是让 CNI 按封装开销下调 Pod MTU，并验证云网络是否丢弃需要分片的包。

VTEP 映射和节点地址错误会造成节点局部故障。多网卡、NAT、节点地址变化、跨网络域部署时，CNI 可能选择了错误的源地址或目标地址。源节点把外层包发到错误 IP，或者目标节点解封装后回程走错路径，都会造成非对称故障。

封装模式与底层网络策略也可能冲突。某些安全设备不允许 UDP 封装流量，某些云网络对跨区域 UDP、有状态防火墙、源地址检查或反向路径过滤有要求。开启 VXLAN 不是绕过所有网络限制，而是把 Pod 流量包在节点间 UDP 流量里；外层路径仍然要被网络设备允许。

可观测性不足会让 VXLAN 问题拖很久。需要在源 Pod、源节点 VXLAN 设备、物理口、目标节点物理口、目标 VXLAN 设备、目标 Pod 多点抓包。只在 Pod 内抓包，可能看到包发出却不知道封装后是否出节点；只在物理口抓包，又看不到内层 Pod 语义。

## 93. VXLAN 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：VXLAN 影响的是跨节点数据路径。Service、DNS 和 HPA 的控制面可能都正常，但只要后端 Pod 分布跨节点，VXLAN 就会影响真实请求是否能到、延迟多高、MTU 是否安全、故障是否好查。

对负载均衡来说，VXLAN 增加了一段隧道路径。Service 选择远端节点上的 Pod 后，包会被封装发往目标节点。跨节点比例越高，VXLAN 路径越重要。节点本地后端、拓扑感知路由、externalTrafficPolicy、本地优先等策略都会改变有多少流量经过 VXLAN。延迟、CPU、封装开销和 MTU 都会影响尾延迟。

对服务发现来说，VXLAN 不生成服务名，但会决定服务名解析后的地址是否可达。普通 Service 解析到 ClusterIP 后，后端可能在远端节点；Headless Service 直接返回 Pod IP，客户端可能直接访问远端 Pod。DNS 正确不等于 VXLAN 正确，二者要分层排查。

对弹性伸缩来说，新 Pod 被调度到新节点后，其他节点必须知道该节点 PodCIDR 或 endpoint 到 VTEP 的映射。CNI agent 同步慢、VXLAN 设备没建好、路由没下发或安全组没放行，会让新 Pod 短期无法承接跨节点流量。HPA 看见副本变多，不代表网络容量马上变多。

对可观测性来说，VXLAN 会让路径多一层。你要同时观察内层 Pod 流量和外层节点隧道流量，指标也要分开：Pod 连接错误、Service 后端分布、节点 VXLAN 设备丢包、UDP 封装流量、接口 MTU、CPU softirq、隧道端点映射。一个成熟的告警不应只看应用 5xx，还要能发现节点间隧道丢包和大包黑洞。

## 94. NetworkPolicy 在云原生系统中解决什么问题？

可以先这样答：NetworkPolicy 解决的是 Kubernetes 集群内 Pod 之间、Pod 到外部网络之间的三层/四层访问控制问题。默认情况下，没有策略约束的 Pod 往往可以互相访问，这对多租户、微服务和生产安全都太宽。NetworkPolicy 让应用团队用 label、namespace、IP 段、端口和协议声明哪些连接允许建立，其他连接由网络插件拒绝。

它解决的不是身份认证，也不是应用层鉴权。NetworkPolicy 工作在 IP、端口和协议层，主要管连接能不能建立。HTTP path、JWT、用户角色、业务权限仍然要由网关、服务网格或应用自己处理。把 NetworkPolicy 当成唯一安全边界，会漏掉应用层访问控制。

它的价值在横向隔离。比如数据库只允许同 namespace 的后端服务访问，支付服务只允许网关和订单服务访问，普通业务 Pod 不允许访问 kube-system 里的基础组件，默认拒绝 egress 后只放行 DNS、消息队列、数据库和外部 API。这样一个 Pod 被入侵后，攻击者不能随意扫描和访问整个集群。

NetworkPolicy 也把安全策略纳入声明式管理。策略与 Deployment、Service 一样是 Kubernetes 对象，可以走 GitOps、审计、评审和回滚。它使用 label 选择 Pod，能随着 Pod 扩缩容和重建自动生效，不需要手工维护 IP 白名单。

面试里必须补上前提：NetworkPolicy 是 API 语义，真正执行依赖支持它的网络插件。创建了 NetworkPolicy 但 CNI 不支持策略，资源会被 API Server 接受，却不会产生实际拦截效果。生产里要确认 Calico、Cilium、Antrea、kube-router 等策略引擎是否启用并覆盖全部节点。
## 95. NetworkPolicy 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：NetworkPolicy 本身是 Kubernetes API 对象，API Server 负责保存策略，真正执行由 CNI 或网络策略控制器完成。策略控制器 watch Pod、Namespace、NetworkPolicy 等对象，把标签选择器解析成具体端点集合，再在节点数据面写入规则，例如 iptables/nftables、eBPF、OVS flow 或其他实现。包经过节点网络路径时，数据面按这些规则决定放行或拒绝。

NetworkPolicy 的选择逻辑以 Pod 为中心。`podSelector` 选中策略适用的目标 Pod；`policyTypes` 指明作用于 Ingress、Egress 或两者；`ingress.from` 和 `egress.to` 可以使用 `podSelector`、`namespaceSelector`、`ipBlock`；`ports` 限制协议和端口。空的 `podSelector: {}` 表示选中 namespace 内所有 Pod，这常用于默认拒绝策略。

Kubernetes 的隔离语义要讲准：默认没有策略时，Pod 对 ingress 和 egress 都是非隔离的；一旦某个方向上有策略选中该 Pod，该方向就进入隔离状态，只允许策略显式放行的连接。多个策略不会互相覆盖，而是放行集合相加。一次 Pod 到 Pod 的连接要同时满足源 Pod 的 egress 策略和目标 Pod 的 ingress 策略，任一侧不允许，连接就不成立。

涉及的组件包括 API Server、etcd、NetworkPolicy 对象、Pod 和 Namespace label、CNI/策略控制器、节点数据面、kubelet readiness、CoreDNS、Service、Ingress/Gateway、云安全组和底层网络。Service 这里容易误解：NetworkPolicy 最终作用于 Pod 连接，不是作用于 Service 对象本身。访问 Service IP 后经过 DNAT，策略执行在 DNAT 前还是后，取决于插件和路径，尤其是 `ipBlock` 与 Service/NAT 混用时要谨慎。

排障时要先看策略是否被正确选中。`namespaceSelector` 和 `podSelector` 的 YAML 缩进差一层，语义就可能从“某 namespace 内某类 Pod”变成“某 namespace 所有 Pod 或本 namespace 某类 Pod”。再看 CNI 是否支持策略、策略控制器是否同步、节点规则是否下发、DNS 和健康检查是否被允许。很多 NetworkPolicy 事故不是规则太少，而是选择器实际含义和作者想的不一样。

## 96. NetworkPolicy 配置错误时会导致哪些线上问题？

可以先这样答：NetworkPolicy 配置错误会让业务连接被误拦或误放。误拦时，业务看到 DNS 超时、连接超时、readiness 失败、数据库连不上、消息队列消费中断、指标采集失败；误放时，安全边界失效，Pod 之间仍然可以横向访问。最危险的是以为策略已经生效，实际 CNI 不支持或没有覆盖节点。

默认拒绝策略最容易出事故。创建 `podSelector: {}` 加 `policyTypes: [Ingress, Egress]` 后，如果没有补充允许规则，DNS、NTP、镜像仓库、API Server、metrics、日志、Webhook、数据库、缓存和消息队列都会被挡。Kubernetes 官方文档也特别提醒：默认拒绝 egress 会挡 DNS，必须额外放行集群 DNS。

选择器错误会制造大范围影响。namespace label 漏打、Pod label 改名、Helm 升级改变 label、`matchLabels` 与 `matchExpressions` 写错，都会让策略选不中目标或选中太多目标。因为策略是 additive，写一个 allow-all ingress 或 allow-all egress 后，其他策略不能再把这些连接拒掉。很多团队误以为后面的 deny 能覆盖前面的 allow，但 Kubernetes NetworkPolicy 没有规则顺序和显式 deny 语义。

`ipBlock` 与 Service/NAT 混用也容易误判。外部流量经过 LoadBalancer、NodePort、Ingress 或 Service DNAT/SNAT 后，策略看到的源 IP 可能是客户端、节点、LB 或 Pod，取决于插件、云厂商和流量路径。用 `ipBlock` 保护外部访问时，要实际抓包和看策略日志，不能只凭 YAML 推断。

还有一类问题是长连接和策略变更的过渡期。策略更新后，已有连接是否立即断开，取决于网络插件和连接跟踪状态。某些连接会继续存在，某些会被新策略阻断。发布策略时要考虑连接池、gRPC、HTTP/2、数据库长连接和重试风暴，最好先用观测或审计模式验证影响，再强制执行。

## 97. NetworkPolicy 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：NetworkPolicy 不负责负载均衡和服务发现，但会决定被发现的后端是否真的允许连接。Service、CoreDNS、EndpointSlice 都可能返回正确结果，最后连接仍然被策略拒绝。所以它对四类能力的影响通常表现为“控制面看着正常，数据面被安全规则挡住”。

对负载均衡来说，Service 选中了某个 Pod 还不够。源 Pod 的 egress、目标 Pod 的 ingress、节点本地策略和可能的 hostNetwork/NodePort 路径都要允许连接。如果策略只放行某些 namespace 或 label，负载均衡器打到标签不完整的新 Pod 时可能失败。滚动发布中，新版本 label 改了但策略没跟上，是很典型的事故。

对服务发现来说，策略要放行 DNS。很多系统启动时先解析服务名，再连接依赖；一条默认拒绝 egress 如果没放行 CoreDNS，业务根本走不到后端连接阶段。Headless Service 场景下，客户端直接连接多个 Pod IP，NetworkPolicy 更容易暴露为“部分后端可连、部分后端不可连”。

对弹性伸缩来说，新 Pod 的 label、namespace 和端口必须被策略覆盖。HPA 扩容后，如果新 Pod 因 label 改动或策略选择器没匹配而接不到依赖，它可能 readiness 失败，或者接到流量后无法访问下游。缩容时，策略本身不会优雅 drain，但会影响 terminating Pod 是否还能完成外部回调、提交 offset 或 flush 指标。

对可观测性来说，NetworkPolicy 应该带来“为什么被拒绝”的证据。Calico、Cilium 等实现通常有策略命中、drop reason、flow log 或 Hubble/日志。没有这些观测时，应用只看到 timeout，平台侧很难区分是 DNS、路由、Service、策略还是后端。生产里要把策略变更、拒绝计数、DNS 错误和关键依赖连接失败放在同一个排障视图里。

## 98. PodDisruptionBudget 在云原生系统中解决什么问题？

可以先这样答：PodDisruptionBudget，简称 PDB，解决的是“自愿中断不要一次打掉太多副本”的问题。集群升级、节点维护、`kubectl drain`、节点缩容、某些自动化运维动作都可能需要驱逐 Pod。PDB 让应用 owner 声明：这组 Pod 至少要保留多少可用，或者最多允许多少不可用，从而避免维护动作把高可用应用同时驱散到不可服务。

PDB 保护的是自愿中断，不是所有中断。节点宕机、内核崩溃、硬件故障、网络分区、资源压力驱逐这类非自愿中断，PDB 阻止不了。它也不会限制 Deployment 自己的滚动更新行为，滚动更新的可用性主要由 Deployment 的 `maxUnavailable`、`maxSurge`、readiness 和 `minReadySeconds` 控制。

PDB 的典型场景是有副本的服务和有 quorum 的有状态系统。Web 前端可能要求 90% 副本可用；ZooKeeper、etcd、Consul 这类系统要确保一次维护不会低于 quorum；消息队列或数据库副本集要避免多个关键副本同时被驱逐。没有 PDB，节点维护工具可能按节点维度驱逐 Pod，应用层看到的就是容量突然下降。

它也是应用团队和平台团队之间的契约。应用团队知道自己能承受几个副本不可用，就写 PDB；平台团队执行维护时使用 Eviction API，尊重 PDB。如果平台直接删除 Pod 或工作负载对象，PDB 不能保护。这个边界在面试里要讲清楚。

一句话收束：PDB 不是让 Pod 永远不挂，而是让“可控的驱逐”按应用可用性预算进行。它把运维动作从“想删就删”变成“先问这个应用还能不能承受”。

## 99. PodDisruptionBudget 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：PDB 通过 label selector 选中一组 Pod，并用 `minAvailable` 或 `maxUnavailable` 表达驱逐预算。控制面会根据这些 Pod 的 owner、期望副本数和当前健康状态计算 `disruptionsAllowed`。当 `kubectl drain`、Cluster Autoscaler 或其他工具通过 Eviction API 请求驱逐 Pod 时，API Server 会检查相关 PDB；预算不足时，驱逐请求会被拒绝或延后重试。

PDB 有几个关键字段：`.spec.selector` 选择 Pod；`.spec.minAvailable` 表示驱逐后至少还要多少 Pod 可用；`.spec.maxUnavailable` 表示驱逐后最多允许多少 Pod 不可用。两者只能选一个。百分比会按规则向上取整，所以 `maxUnavailable: 30%` 在单副本场景可能允许 1 个 Pod 被驱逐，实际就是 100% 不可用，这个细节很容易被忽略。

PDB 判断“期望副本数”时，会沿着 Pod 的 ownerReferences 找到 Deployment、ReplicaSet、StatefulSet 等工作负载，并读取它们的 scale 信息。对于支持 scale subresource 的自定义控制器，也可以配合 PDB。没有清晰 owner 或 selector 过宽时，PDB 的行为会更难预测。

涉及组件包括 PDB API 对象、API Server、Eviction API、kube-controller-manager 中的相关控制逻辑、Deployment/StatefulSet/ReplicaSet、Pod readiness、`kubectl drain`、Cluster Autoscaler、VPA updater、节点维护工具和调度器。kubelet 最终仍负责 Pod 终止，PDB 负责的是驱逐请求能不能被允许。

还要注意健康状态。PDB 不是简单数 Pod 个数，而是看可用 Pod。readiness 失败的 Pod 会影响预算；非自愿中断虽然不被 PDB 阻止，但会占用预算，使后续自愿驱逐被拒绝。比如一个 5 副本服务已经坏了 1 个，PDB 要求至少 4 个可用，那么再 drain 一个 Pod 就可能被挡住。

## 100. PodDisruptionBudget 配置错误时会导致哪些线上问题？

可以先这样答：PDB 配错有两个方向的事故：太宽会保护不住应用，太严会卡住平台运维。太宽时，节点维护或缩容可能一次驱逐太多副本，造成容量不足、quorum 丢失或服务中断；太严时，`kubectl drain`、节点升级、Cluster Autoscaler 缩容、VPA 驱逐都会长时间卡住。

单副本服务的 PDB 很容易误用。给单副本设置 `maxUnavailable: 1` 等于允许这个唯一副本被驱逐；设置 `maxUnavailable: 30%` 因为向上取整，也可能允许 1 个不可用。想避免单副本被自动驱逐，通常要用 `maxUnavailable: 0` 或 `minAvailable: 1`，但这也意味着维护动作必须有人协调，否则节点 drain 会卡。

selector 错误风险很高。PDB selector 选不中 Pod，等于没有保护；选中太多 Pod，会把多个应用绑到同一个预算里，导致无关应用互相影响；Helm 或 Deployment label 改了，PDB 还用旧 label，会在升级后失效。policy/v1 中空 selector 会匹配 namespace 内所有 Pod，这个细节如果写错，影响范围会很大。

PDB 过严会让集群无法维护。节点升级、内核补丁、节点池缩容都需要驱逐 Pod。如果多个关键应用都设置 `maxUnavailable: 0`，而又没有多副本、反亲和和容量冗余，平台团队会发现节点 drain 永远过不去。长期看，这会让集群积累安全补丁和版本债务。

PDB 也可能掩盖应用健康问题。某个 Pod readiness 长期失败，预算被占用，维护动作被拒绝；表面是 PDB 卡住，根因可能是应用不可用、探针过严、资源不足或依赖失败。排查时要看 PDB status、`disruptionsAllowed`、被选中的 Pod、owner 副本数和每个 Pod 的 Ready 状态。

## 101. PodDisruptionBudget 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：PDB 不直接处理流量，但它影响维护、缩容和驱逐期间还剩多少可用 Pod。可用副本数决定 Service 后端池大小，也决定负载均衡器能不能在变更期间维持容量。它是高可用设计里连接“应用容量”和“平台运维动作”的对象。

对负载均衡来说，PDB 能避免节点维护一次拿掉过多后端。Service、Ingress、Gateway 或 Mesh 看到的 endpoint 数量下降得慢一些，流量有机会重新分配。如果 PDB 太宽，维护动作可能让后端数骤降，剩余 Pod 被打满，尾延迟和错误率上升；如果 PDB 太严，平台无法完成节点维护，长期风险转移到基础设施层。

对服务发现来说，PDB 本身不修改 DNS 或 EndpointSlice，但驱逐被允许后，Pod 会进入终止流程，EndpointSlice 和 readiness 状态会随之变化。PDB 控制的是“同一时间能有多少这种变化发生”。对 Headless Service 和有状态系统尤其重要，因为客户端可能直接感知每个副本的上下线。

对弹性伸缩来说，PDB 会影响 Cluster Autoscaler 缩容和 VPA 更新。缩容节点时，如果节点上的 Pod 受 PDB 保护且预算不足，节点不能被安全清空；VPA Recreate 模式要驱逐 Pod 更新 requests，也会受 PDB 约束。HPA 扩容通常不受 PDB 阻挡，但缩容后的副本数会改变 PDB 预算，需要一起评估。

对可观测性来说，要监控 PDB 的 `currentHealthy`、`desiredHealthy`、`disruptionsAllowed`、被拒绝的 eviction、drain 卡住时长和相关 Pod readiness。线上维护窗口里，如果只看节点升级进度，不看 PDB，就很难解释为什么某些节点一直无法清空。一个合理的面试回答应该把 PDB 放在发布、维护、自动扩缩容和应用 SLO 之间讲。
## 102. readinessProbe 在云原生系统中解决什么问题？

可以先这样答：readinessProbe 解决的是“这个容器现在能不能接新流量”的问题。进程活着不等于能服务请求。应用可能还在加载配置、预热缓存、等待依赖、建立连接池、执行启动迁移，或者运行中临时过载、下游不可用、准备优雅下线。readinessProbe 把这些状态表达给 Kubernetes，让 Service 和其他数据面不要把常规新流量打到未就绪的 Pod。

它的核心价值是保护流量入口。readiness 失败时，容器不会因为这个失败被 kubelet 重启，而是 Pod 的 Ready 条件变为 false。对于匹配 Service 的 Pod，EndpointSlice 中的 ready 状态会随之变化，kube-proxy、Ingress、Gateway、Service Mesh 或其他数据面就不应该继续把普通新连接分给它。

readinessProbe 很适合表达“我还活着，但暂时别给我请求”。比如 Java 应用启动后还在 JIT 或连接池预热，Go 服务已经监听端口但缓存没加载完，消费者需要先拉取配置和注册分区，网关要等路由表下发，或者应用收到 SIGTERM 后希望先从负载均衡里摘掉再处理存量连接。这些场景都不应该靠 liveness 重启解决。

它也是滚动发布能否平滑的关键。Deployment 的新 Pod 只有 ready 后才算可用，旧 Pod 才能按策略继续下线。readiness 写得太宽，新版本没准备好就接流量；写得太严，发布会卡住。`minReadySeconds`、启动预热、依赖健康、队列水位和应用自身过载保护，都要和 readiness 一起设计。

面试里可以用一句话区分：readinessProbe 是流量开关，不是进程保活开关。它解决服务发现和负载均衡候选集的问题，避免把请求打给暂时不能服务的实例。

## 103. readinessProbe 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：kubelet 在节点上按配置周期执行 readinessProbe，探测方式可以是 HTTP、TCP、exec 或 gRPC。探测成功时，容器被认为 ready；失败达到阈值后，Pod Ready 条件会变为 false。控制面和 EndpointSlice controller 会把这个状态反映到 endpoint 上，Service 数据面据此更新后端可用集合。

readinessProbe 的配置包括 `initialDelaySeconds`、`periodSeconds`、`timeoutSeconds`、`successThreshold`、`failureThreshold` 以及具体 handler。HTTP 探针要求状态码在成功范围内；TCP 探针只验证端口能建立连接；exec 探针在容器里执行命令；gRPC 探针走 gRPC health checking。不同方式语义不同，不能只选“最容易配”的。

它和 EndpointSlice 的关系很关键。Pod 对象上的 Ready 条件变化后，匹配 Service selector 的 endpoint ready 状态也会变化。普通 ClusterIP Service 通常只把 ready endpoint 作为常规流量后端；Headless Service、Service Mesh、Ingress/Gateway controller 也会消费这些信号。terminating endpoint 还可能有 `serving`、`terminating` 等更细状态，用来支持优雅下线。

涉及组件包括 kubelet、容器运行时、Pod status、API Server、EndpointSlice controller、Service、kube-proxy 或替代数据面、Ingress/Gateway/Service Mesh controller、Deployment/StatefulSet 控制器以及应用自身的健康端点。readiness 的探测结果从节点本地出发，最后影响集群服务发现和流量转发。

排障时要看三层证据。第一层是 kubelet 事件和 Pod condition，确认探针为什么失败；第二层是 EndpointSlice，确认 Pod 是否进入 ready endpoint；第三层是实际数据面，确认 kube-proxy、Ingress、Mesh 是否已经同步。只看应用健康接口返回 200，不代表 Kubernetes 已经把它放入服务池。

## 104. readinessProbe 配置错误时会导致哪些线上问题？

可以先这样答：readinessProbe 配错会直接影响流量进入。写得太宽，未准备好的 Pod 会接请求，造成启动期 5xx、超时、缓存 miss、连接池打满；写得太严，Pod 长期 not ready，Service 后端不足，Deployment 发布卡住，HPA 扩容出来的 Pod 也不能形成有效容量。

最常见错误是把“进程端口能连”当成“业务就绪”。TCP 探针只证明端口打开，不证明依赖、配置、缓存、路由表、数据库连接或线程池可用。很多应用一启动就监听端口，但真正能处理请求要几十秒。如果 readiness 只探 TCP，滚动更新时新 Pod 会过早接流量。

另一个错误是把下游短暂故障全部写进 readiness。比如数据库慢 1 秒、缓存偶发超时，就让所有 Pod readiness 失败，会把整个服务从负载均衡里摘光，造成自我放大。readiness 要表达“我是否应该接新请求”，但也要避免被短抖动触发。可以用连续失败阈值、内部熔断、过载水位和降级状态，而不是把每个依赖瞬时错误都暴露给 kubelet。

超时和周期配置也会制造事故。`timeoutSeconds` 太短会让高峰期健康检查误报失败；`periodSeconds` 太频繁会给应用和依赖增加额外压力；`failureThreshold` 太低会在短暂 GC、CPU 抢占、I/O 抖动时摘流；`successThreshold` 太高又会让恢复后迟迟不接流量。探针本身也是流量，要给它预算。

还有一个常见问题是没有为下线设计 readiness。Pod 收到终止信号后，如果应用仍然保持 ready，EndpointSlice 和上游数据面可能继续给它新请求；等 kubelet 最后 SIGKILL 时，请求就被切断。优雅终止通常要让 readiness 先失败，等待摘流传播，再开始关闭监听和连接池。

## 105. readinessProbe 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：readinessProbe 是连接应用内部状态和 Kubernetes 流量系统的主要信号。它不转发流量，但决定 Pod 是否进入 Service 后端集合。负载均衡、服务发现、滚动发布和扩缩容能否按预期工作，都要依赖 readiness 的语义准确。

对负载均衡来说，ready Pod 才应该接普通新流量。readiness 失败会把 Pod 从可用后端里摘掉，让 Service、Ingress、Gateway 或 Mesh 重新分配流量。readiness 写错时，负载均衡器本身可能没问题，但候选后端集合是错的，表现为有的后端过早接流量，有的后端永远不接流量。

对服务发现来说，EndpointSlice 会反映 readiness。普通 Service 的 DNS 仍然返回 ClusterIP，但真正后端集合受 ready 状态影响；Headless Service 和一些客户端发现机制会更直接地感知 ready endpoint 的变化。对 StatefulSet 来说，单个副本 ready 与否会影响客户端是否应该连接这个具体成员。

对弹性伸缩来说，HPA 增加副本只是第一步，新 Pod 只有 ready 后才形成服务容量。readiness 太慢会让扩容滞后，readiness 太宽会让未预热实例被流量压垮。HPA 的 CPU 初始化窗口也会考虑 Pod readiness，启动期 readiness 设计不当会影响指标采样和扩缩容判断。

对可观测性来说，readiness 是排障的关键维度。要监控 Ready Pod 数、not ready 原因、探针失败率、EndpointSlice ready/serving/terminating 分布、发布期间 available 副本数和摘流传播时间。很多发布事故不是“新版本挂了”，而是 readiness 没有准确表达新版本何时能接流量。

## 106. livenessProbe 在云原生系统中解决什么问题？

可以先这样答：livenessProbe 解决的是“容器已经坏到需要重启”的问题。有些进程不会退出，但内部已经死锁、事件循环卡住、关键线程停止、健康端口永久无响应，靠 Deployment 副本数无法自动恢复。livenessProbe 给 kubelet 一个判断依据：这个容器还活着但不可恢复时，把它杀掉并按重启策略拉起来。

它的定位是最后兜底，而不是流量治理。liveness 失败后，kubelet 会重启容器，这会断开连接、丢失内存状态、触发冷启动，也可能造成流量重新分布。对暂时不能接新流量、依赖短暂不可用、队列积压、正在下线这类情况，通常应该用 readiness、熔断或降级，而不是 liveness。

livenessProbe 适合检测明确的进程级不可恢复状态。比如主事件循环无法响应、线程池彻底死锁、内部健康检查发现关键后台任务永久停止、应用已经进入无法继续处理请求的状态。它不适合把数据库、缓存、第三方 API 的瞬时健康纳入重启条件，因为重启本服务不能修好外部依赖。

这个探针的意义在云原生系统里很直接：容器不是 VM，不应该靠人工登录重启进程。kubelet 能通过 liveness 自动恢复单实例故障，Deployment/ReplicaSet 再通过副本数维持整体可用。但这个自愈能力只有在探针语义正确时才有价值。

一句话收束：readiness 是“先别给我流量”，liveness 是“我已经坏了，重启我”。把两者写反，是生产里很常见也很危险的错误。

## 107. livenessProbe 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：kubelet 按周期对容器执行 livenessProbe。探测方式同样可以是 HTTP、TCP、exec 或 gRPC。连续失败达到 `failureThreshold` 后，kubelet 会认为容器不健康，停止该容器并按 Pod 的 `restartPolicy` 重启。重启发生在同一个 Pod 内，Pod IP 通常不变，但容器进程和连接都会重新开始。

livenessProbe 的配置字段与 readiness 类似，包括初始延迟、周期、超时、成功/失败阈值和 handler。HTTP 探针常探 `/healthz`，exec 探针可以执行本地命令，TCP 探针只证明端口可连，gRPC 探针适合实现了标准 health checking 的服务。探针本身由 kubelet 发起，所以要考虑节点到容器网络路径、容器 CPU 饥饿和探针 handler 的资源消耗。

涉及组件包括 kubelet、容器运行时、Pod restartPolicy、容器进程、Pod status、Events、Deployment/ReplicaSet 控制器和监控系统。liveness 重启通常不会创建新 Pod 对象，但会增加 container restart count，并在事件里记录 probe failed、killing、started 等信息。Deployment 只看到 Pod 还在，但容器已经重启过。

liveness 与 startupProbe 有明确关系。如果配置了 startupProbe，在 startupProbe 成功前，liveness 和 readiness 的常规探测不会过早杀死慢启动应用。慢启动应用如果只配 liveness，可能还没启动完就被重启，进入 CrashLoopBackOff。startupProbe 是保护启动期的工具，liveness 是启动后兜底。

排障时要看容器重启次数、kubelet 事件、探针失败原因、应用日志、节点资源和探针耗时。不要只看到 `Liveness probe failed` 就加大阈值；要先判断是应用真的死锁，还是探针路径、超时、CPU throttling、GC 暂停或下游依赖导致误杀。

## 108. livenessProbe 配置错误时会导致哪些线上问题？

可以先这样答：livenessProbe 配错会把小故障放大成重启风暴。最典型的是把外部依赖健康写进 liveness：数据库慢、缓存抖动、DNS 短暂失败，所有 Pod 同时 liveness 失败并重启。重启后冷启动、连接重建、缓存丢失又进一步压垮依赖，系统进入恶性循环。

慢启动应用只配 liveness 也很危险。大模型加载、JVM 预热、迁移任务、索引加载、缓存构建都可能超过普通 liveness 初始延迟。kubelet 会在应用没启动完时杀掉它，Pod 进入 CrashLoopBackOff。正确做法通常是加 startupProbe，给启动期足够窗口，启动完成后再交给 liveness 兜底。

探针太敏感会造成误杀。`timeoutSeconds` 太短、`failureThreshold` 太低、`periodSeconds` 太频繁，在 CPU throttling、GC、磁盘抖动、节点压力或短暂网络延迟下都会误判。被误杀的容器会断开长连接、丢失内存缓存和本地队列，用户看到的不是短暂慢，而是明显 5xx 或连接中断。

探针太宽也有问题。如果 liveness 只检查进程是否存在或端口是否打开，死锁线程、事件循环卡死、内部 worker 全挂都可能检查通过。Kubernetes 以为容器活着，实际上请求已经不处理。这类问题会让负载均衡继续把流量打到坏实例，直到人工发现。

还有一种问题是探针 handler 设计不当。健康接口如果要访问多个下游、加锁、查大表或打印大量日志，本身就可能成为负载。exec 探针如果启动昂贵命令，频繁执行会消耗容器资源；探针失败还可能触发更多重启。liveness 的 handler 应该轻量、稳定、能反映本进程不可恢复状态，而不是做一次完整业务巡检。
## 109. livenessProbe 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：livenessProbe 通过重启容器间接影响流量系统。它不直接修改 Service 后端，但容器重启会让应用短暂不可用，readiness 通常会失败，EndpointSlice 后端状态也会变化。配置正确时，它能把坏实例拉回健康；配置错误时，它会制造抖动、重启风暴和容量下降。

对负载均衡来说，liveness 重启会中断这个 Pod 上已有连接。短连接服务可能只是短暂失败，长连接、WebSocket、gRPC stream、数据库连接池和消息消费者会更明显。负载均衡器可能继续把流量分给重启中的 Pod，直到 readiness 把它摘掉，所以 liveness 和 readiness 要配合，而不是只配一个 liveness。

对服务发现来说，liveness 本身不是服务发现信号，但重启过程会影响 Pod Ready 条件和 endpoint 状态。普通 Service 最终会从 ready endpoint 中移除不健康实例；Headless Service 客户端如果直接持有 Pod IP，可能在容器重启期间继续打到同一个地址，收到 reset 或超时。

对弹性伸缩来说，liveness 误杀会污染指标。容器频繁重启会带来冷启动 CPU、缓存 miss、连接重建和请求失败；HPA 看到 CPU 或自定义指标异常，可能进一步扩容。扩出来的新 Pod 如果同样被错误 liveness 杀掉，就会形成“扩容但容量不增加”的状态。

对可观测性来说，liveness 的核心指标是 restart count、probe failure rate、失败原因、重启前后的应用错误、CrashLoopBackOff、节点资源压力和探针耗时。排障时要把重启时间线和错误率、readiness、下游依赖延迟对齐。否则很容易把重启风暴当成业务自身不稳定，而不是探针策略错误。

## 110. startupProbe 在云原生系统中解决什么问题？

可以先这样答：startupProbe 解决的是“慢启动应用在启动完成前不要被 liveness 误杀”的问题。很多应用启动过程很长：加载大模型、初始化索引、预热缓存、跑迁移、建立连接池、加载证书或同步配置。它们不是死锁，也不是坏了，只是启动时间超过普通 liveness 的容忍窗口。startupProbe 给启动期单独设置一套更宽的探测预算。

没有 startupProbe 时，团队常常把 liveness 的 `initialDelaySeconds` 调得很大。这样虽然能避免启动期误杀，但也让应用启动后真正死锁时很久才被发现。startupProbe 的好处是把两个阶段分开：启动没完成前，按 startupProbe 的宽窗口等待；启动成功一次后，liveness 接管，用更快的节奏发现运行期死锁。

它也能让 readiness 更清晰。startupProbe 不是“能接流量”的信号，readiness 才是。一个应用可能已经完成基本启动，但还没预热好，不应该接新流量；也可能 startupProbe 通过后，readiness 还要继续等待缓存、依赖或路由准备。把 startup、readiness、liveness 三者拆开，发布和排障会清楚很多。

典型使用场景包括 JVM 或 Spring 应用、AI 推理服务、数据库或搜索组件、需要加载大量本地数据的服务、启动时做 schema 检查的服务。不是所有应用都需要 startupProbe，启动很快且 liveness 初始延迟足够的服务可以不配。但一旦出现启动期 CrashLoopBackOff，就要优先检查是否缺 startupProbe。

一句话收束：startupProbe 保护启动期，readiness 控制接流量，liveness 负责运行期自愈。三者不是重复配置，而是分别描述生命周期里的不同问题。

## 111. startupProbe 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：kubelet 在容器启动后先执行 startupProbe。只要 startupProbe 还没有成功，普通 liveness 和 readiness 的失败不会按常规方式过早影响容器；startupProbe 成功一次后，它的任务结束，liveness 和 readiness 按各自配置继续执行。如果 startupProbe 在允许窗口内一直失败，kubelet 会杀掉容器，并按重启策略重新启动。

startupProbe 的探测方式与其他探针一致，可以是 HTTP、TCP、exec 或 gRPC。它同样使用 `periodSeconds`、`timeoutSeconds`、`failureThreshold` 等字段决定总启动容忍时间。比如 `periodSeconds: 10`、`failureThreshold: 30`，大致给应用 300 秒启动窗口。这个窗口要按真实启动 P99、镜像拉取后初始化、依赖波动和节点资源压力估算。

涉及组件包括 kubelet、容器运行时、Pod lifecycle、livenessProbe、readinessProbe、Pod events、Deployment/StatefulSet 控制器和应用健康端点。startupProbe 的结果主要由 kubelet本地使用，但它间接影响 Pod 是否进入 Running 后稳定状态、是否进入 CrashLoopBackOff，以及发布是否推进。

startupProbe 的 handler 要能代表“启动是否完成到可以进入运行期检测”。它不一定等于业务完全 ready。比如服务进程启动完成、关键线程已运行、HTTP server 已可响应，可以让 startupProbe 成功；缓存预热、连接池填充、流量开关仍可以交给 readiness。这样运行期死锁能被 liveness 快速发现，接流量时机仍由 readiness 控制。

排障时要看事件顺序。先确认容器是否因为 startupProbe 失败被 kill，再看启动日志、探针耗时、初始化步骤和节点资源。startupProbe 配置太宽会掩盖启动卡死，配置太窄又会回到启动期误杀。它不是把问题拖久，而是给慢启动一个合理、可观测的上限。

## 112. startupProbe 配置错误时会导致哪些线上问题？

可以先这样答：startupProbe 配错会让应用在启动阶段要么被过早杀掉，要么卡死太久才被发现。过窄时，慢启动应用进入 CrashLoopBackOff；过宽时，真正启动失败的容器长期占着 Pod，不触发及时恢复，也拖慢发布和扩容。

过窄配置最常见。`failureThreshold * periodSeconds` 小于真实启动时间，或者 `timeoutSeconds` 小于健康端点响应时间，kubelet 会在应用还没完成初始化时杀掉它。节点冷启动、镜像首次运行、缓存为空、外部配置服务慢，都可能让平时够用的窗口在高峰期不够。

过宽也有成本。应用启动脚本卡住、死锁、等待永远不可用的依赖，如果 startupProbe 给了十几分钟甚至更久，Deployment 发布会一直等待新 Pod，HPA 扩容也不能形成容量。用户看到的是发布卡住或扩容无效，平台侧看到的是 Pod 一直不稳定但又不快速失败。

handler 语义写错会造成阶段混乱。如果 startupProbe 依赖所有下游都正常，短暂下游故障会杀掉正在启动的应用；如果 startupProbe 只检查进程存在，应用内部其实没初始化完就让 liveness 接管，随后可能被 liveness 误杀。startupProbe 应该回答“进程是否已完成启动流程”，不要承担所有业务就绪判断。

还要注意与 readiness/liveness 的组合。只配 startupProbe 不配 readiness，启动成功后 Pod 可能马上接流量；只配 startupProbe 不配合理 liveness，运行期死锁仍难发现。三者的时间窗口要按应用生命周期设计，而不是复制一组通用模板。

## 113. startupProbe 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：startupProbe 主要影响新 Pod 从创建到稳定运行的时间线。它不直接控制 Service 后端，但它决定容器能否越过启动期、进入 readiness 和 liveness 的常规阶段。发布、扩容和故障恢复里，新实例能不能顺利起稳，很大程度取决于 startupProbe 的配置。

对负载均衡来说，startupProbe 间接防止未启动完成的实例被 liveness 杀掉后反复重启。它让新 Pod 有机会走到 readiness 阶段，再由 readiness 决定是否接流量。没有 startupProbe 时，慢启动服务可能一直重启，负载均衡后端看似有副本，实际没有稳定容量。

对服务发现来说，startupProbe 成功前通常不应把 Pod 当成可用后端。最终是否进入 EndpointSlice ready 集合仍由 readiness 决定，但 startupProbe 影响这个过程能否完成。Headless Service 客户端如果直接感知 Pod 地址，也要注意新 Pod 可能已经有 IP，但启动探针未通过，不应该被当成可用成员。

对弹性伸缩来说，startupProbe 会影响扩容生效时间。HPA 扩出新 Pod 后，Pod 要经过镜像启动、startupProbe、readiness、EndpointSlice 更新，才真正增加容量。如果 startupProbe 窗口过短，扩容 Pod 死循环；过长，扩容响应慢。对冷启动重的服务，HPA 参数、预热策略和 startupProbe 要一起调。

对可观测性来说，要监控启动耗时分布、startupProbe 失败次数、启动阶段日志、CrashLoopBackOff、ready 延迟和扩容到可服务的端到端时间。很多团队只看 Pod 创建时间，忽略“从创建到 ready”的启动预算，结果 HPA 和发布窗口都按错了。

## 114. preStop hook 在云原生系统中解决什么问题？

可以先这样答：preStop hook 解决的是“容器被终止前，应用需要最后一次执行收尾逻辑”的问题。Pod 被删除、驱逐、节点维护、探针失败导致重启或其他管理事件触发终止时，kubelet 会在发送 TERM 信号前调用 preStop。应用可以利用这个窗口做摘流、通知、停止接新任务、flush 状态、提交 offset 或给外部负载均衡传播留时间。

它最常用于优雅下线。很多服务收到 SIGTERM 后还可能被上游短时间继续打流量，因为 EndpointSlice、Ingress、Gateway、Service Mesh、云 LB 同步都需要时间。preStop 可以执行一个轻量动作，例如调用本地接口把应用置为 not ready，通知 sidecar drain，或者 sleep 几秒等待控制面摘流传播。注意，sleep 不是优雅关闭本身，只是给传播留时间。

preStop 也适合有状态收尾。消费者可以提交 offset，批处理进程可以写 checkpoint，日志代理可以 flush buffer，网关可以停止接受新连接，数据库代理可以拒绝新事务。前提是逻辑要短、幂等、可失败，不应依赖一个可能已经不可靠的复杂外部系统。

面试里要强调一个边界：preStop 的时间算在 `terminationGracePeriodSeconds` 里。它不是额外赠送的时间。如果 preStop 执行 25 秒，grace period 只有 30 秒，应用主进程收到 TERM 后只剩很短时间退出，超时就会被 SIGKILL。preStop 要和应用 shutdown timeout 一起设计。

一句话总结：preStop 是终止流程里的前置钩子，用来协调摘流和收尾，不是替代应用自己处理 SIGTERM 的机制。

## 115. preStop hook 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：当 Pod 或容器进入终止流程时，kubelet 会在容器还运行时调用 preStop hook。hook 可以是 exec、HTTP 或 sleep。preStop 必须完成后，kubelet 才会向容器发送 TERM 信号；但 Pod 的 termination grace period 在 preStop 开始前就已经倒计时。如果 grace period 到期，kubelet 会强制结束容器。

preStop 由 kubelet 在节点本地执行。exec handler 在容器的 cgroup 和 namespace 内运行，消耗的资源算到容器上；HTTP 和 sleep 由 kubelet 触发。hook 没有参数，失败时会产生事件，但不会保证复杂重试。Kubernetes 对 hook 的交付语义接近 at least once，极少数 kubelet 重启等场景可能重复执行，所以 handler 必须幂等。

它涉及 API Server、Pod deletionTimestamp、kubelet、容器运行时、preStop handler、terminationGracePeriodSeconds、EndpointSlice、Service 数据面、Ingress/Gateway/Mesh 和应用进程。控制面记录 Pod 正在终止，EndpointSlice 会反映 endpoint terminating 状态；节点侧 kubelet 执行 preStop 和发送信号；应用负责真正停止接新请求并处理存量工作。

一个典型链路是：用户或控制器删除 Pod，API Server 写入 deletionTimestamp 和 grace period；EndpointSlice 开始把该 Pod 从常规 ready 后端里移出或标记 terminating；kubelet 看到终止状态，执行 preStop；preStop 调用本地 drain 或短暂等待；然后 kubelet 发送 TERM；应用完成 shutdown；超时仍未退出则 SIGKILL。

排障时要看 preStop 是否执行、执行多久、是否失败、grace period 是否足够、应用是否处理 TERM、EndpointSlice 摘流是否已传播、上游负载均衡是否还有旧连接。很多下线事故不是没有 preStop，而是 preStop 花光了 grace period，主进程没有时间优雅退出。
## 116. preStop hook 配置错误时会导致哪些线上问题？

可以先这样答：preStop 配错会让下线过程变得更差，而不是更优雅。常见问题包括 Pod 长时间 Terminating、主进程来不及处理 SIGTERM、请求被强制切断、消费者重复消费或丢 offset、日志和指标没 flush、节点 drain 卡住，以及滚动发布时间被拉得很长。

最常见错误是 preStop 太慢。很多人把 `sleep 30`、外部 API 调用、复杂脚本都放进 preStop，却没有同步增加 `terminationGracePeriodSeconds`。由于 grace period 从 preStop 前就开始倒计时，preStop 花掉大部分时间后，主进程收到 TERM 时已经没时间关闭连接、提交状态或退出，最后还是被 SIGKILL。

第二类错误是 preStop 不幂等或依赖过多外部系统。hook 可能因为 kubelet 重启等边界情况重复执行，也可能执行失败。脚本如果重复注销实例、重复提交 offset、重复删除锁，可能造成状态错乱；如果依赖一个正在抖动的控制面或外部服务，终止流程会被拖慢。preStop 应该短、确定、可重复执行。

第三类错误是把 preStop 当成唯一摘流机制。只 sleep 不让 readiness 失败，或者只调用本地 drain 但上游网关不知道，都会让新请求继续进来。优雅下线需要 readiness、EndpointSlice、Ingress/Gateway、Service Mesh、应用监听关闭和连接池策略一起配合。

还有一种问题是 preStop 对多容器 Pod 的顺序理解错误。Pod 里有 sidecar 时，业务容器、代理容器、日志容器的关闭顺序会影响连接是否能 drain、日志是否丢失、指标是否上报。preStop 写在错误容器上，或者代理先退出、业务还在处理请求，都会制造短暂但明显的失败。

## 117. preStop hook 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：preStop 通过终止流程影响流量下线质量。它不直接修改 Service 或 DNS，但它给控制面和数据面摘流传播留时间，也给应用处理存量请求留时间。配置得好，滚动更新和节点维护时错误率低；配置得不好，下线窗口会出现连接重置、请求中断和重复处理。

对负载均衡来说，preStop 常用于 drain。Pod 进入 Terminating 后，EndpointSlice 会更新，但 kube-proxy、Ingress、Gateway、Mesh、云 LB 和客户端连接池同步都需要时间。preStop 可以让应用先停止接新请求或等待一小段时间，减少新请求落到即将退出实例的概率。长连接场景尤其需要这个缓冲。

对服务发现来说，preStop 影响的是“被发现的实例何时真正停止服务”。Service 后端集合变化不是瞬时传到所有消费者，Headless Service 客户端还可能缓存 Pod IP。preStop 不能强迫客户端立刻忘记旧地址，但可以让旧实例在一段时间内继续处理已建立连接或返回明确的关闭信号。

对弹性伸缩来说，缩容和节点缩容会触发 Pod 终止。preStop 过长会降低缩容速度，让节点 drain、Cluster Autoscaler 缩容或 Deployment rollout 变慢；preStop 太短又会增加请求失败。服务需要在成本、缩容速度和下线可靠性之间取一个明确的值，而不是随便 sleep。

对可观测性来说，要监控 preStop 执行耗时、失败事件、Terminating 持续时间、TERM 到退出时间、SIGKILL 次数、下线期间 5xx/reset、EndpointSlice terminating 数和上游 still-sending 流量。一个很实用的指标是：Pod 进入 Terminating 后，是否仍有新请求进入。如果有，就说明摘流链路还没对齐。

## 118. graceful termination 在云原生系统中解决什么问题？

可以先这样答：graceful termination 解决的是“Pod 被删除或替换时，不要粗暴中断正在处理的工作”的问题。Kubernetes 的工作负载会频繁滚动更新、扩缩容、节点维护和故障迁移。如果每次终止都直接 SIGKILL，用户请求会被切断，消息可能重复或丢失，日志和指标可能来不及落盘，有状态组件还可能留下脏状态。

优雅终止的核心不是“晚一点杀进程”，而是一套协作：先让服务停止接新流量，再等待负载均衡和服务发现传播，再让应用处理已有请求、提交状态、关闭连接，最后在宽限期内退出。如果超过宽限期仍未退出，Kubernetes 仍然会强制杀掉容器，保证删除最终完成。

它对微服务尤其重要。HTTP 短请求需要尽量避免 5xx，gRPC 和 WebSocket 需要处理长连接，消费者需要提交 offset，批处理需要 checkpoint，网关和代理需要 drain，数据库客户端需要关闭连接池。不同应用的优雅终止时间不一样，不能用统一模板套所有服务。

优雅终止还影响发布速度。宽限期太短，发布会出现连接重置；宽限期太长，滚动更新和节点维护会很慢，旧版本占用资源时间过长。合理做法是用实际请求耗时、最长任务时间、连接 drain 时间和上游同步延迟来设定，而不是凭感觉写 30 秒。

一句话总结：graceful termination 是把“实例从服务池退出”变成一个可控流程，目标是在保证最终删除的同时，把对用户请求和系统状态的破坏降到最低。

## 119. graceful termination 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：当 Pod 被删除时，API Server 会给 Pod 写入 deletionTimestamp 和 grace period。控制面开始把该 Pod 视为正在终止；对于 Service 后端，EndpointSlice 会把它从常规 ready 后端中移出或标记 terminating。节点上的 kubelet 看到终止状态后，执行 preStop hook，然后向容器发送 TERM 信号。应用在 `terminationGracePeriodSeconds` 内完成收尾；超时后 kubelet 发送 SIGKILL。

这条链路有控制面和节点面两条线。控制面负责记录删除意图、更新 Pod 状态、更新 EndpointSlice、让控制器不再把 terminating Pod 当作正常副本。节点面负责执行 hook、发信号、等待退出、清理 sandbox 和卷。两条线并行推进，所以应用不能假设“收到 TERM 时已经没有任何新流量”。

涉及组件包括 API Server、etcd、kube-controller-manager、EndpointSlice controller、Service、kube-proxy 或替代数据面、Ingress/Gateway/Service Mesh、kubelet、容器运行时、preStop、Pod `terminationGracePeriodSeconds`、应用进程、PDB 和 Eviction API。节点维护时，`kubectl drain` 或自动化工具通常通过 Eviction API 发起，PDB 先决定能不能驱逐，然后才进入终止流程。

应用侧必须主动配合。收到 TERM 后应该停止接受新请求或把 readiness 置为 false，关闭监听或从队列暂停拉取，等待已处理请求完成，提交 offset/checkpoint，flush 日志和指标，关闭连接池，再退出。只依赖 Kubernetes 发信号，不写应用 shutdown 逻辑，不能叫真正的优雅终止。

对 sidecar 要特别小心。代理 sidecar、日志 sidecar、服务网格 sidecar 的退出顺序会影响业务容器是否还能发出最后的请求、日志是否能刷出、连接是否能 drain。新版本 Kubernetes 对 sidecar 生命周期有更明确语义，但工程上仍要验证自己的代理和业务容器关闭顺序。

## 120. graceful termination 配置错误时会导致哪些线上问题？

可以先这样答：graceful termination 配错会在滚动更新、缩容、节点维护时集中暴露。常见现象是发布期间 5xx 升高、连接 reset、gRPC stream 中断、消息重复消费、任务执行一半被杀、日志丢失、指标缺口、Pod 长时间 Terminating 或节点 drain 卡住。

`terminationGracePeriodSeconds` 太短是第一类问题。应用最长请求 20 秒，grace period 只有 10 秒，必然有请求被 SIGKILL 切断。消费者批处理、数据库事务、文件上传、AI 推理、报表任务都可能超过默认时间。设置时要看真实 P95/P99 和最长任务，而不是默认值。

grace period 太长也有代价。缩容慢、节点维护慢、Deployment rollout 慢，旧版本占用资源时间长。高流量服务如果每次下线都等几分钟，会拖慢发布窗口；节点池缩容时，Cluster Autoscaler 也可能因为 Pod 长时间退出而回收不了节点。优雅不是无限等待。

摘流顺序错误也很常见。应用先关闭进程，再让 readiness 失败，上游还会继续打新请求；preStop sleep 了，但应用仍然 ready；Ingress 或 Mesh drain 时间比 Pod grace period 长；云 LB 健康检查周期太长，节点已经杀进程，LB 还认为后端健康。优雅终止要按最慢的传播路径设计。

应用不处理 TERM 是根本问题。很多程序在本地 Ctrl+C 能停，不代表容器里能正确处理 SIGTERM。PID 1 信号转发、shell wrapper、进程管理器、sidecar、语言运行时都可能吞掉信号。排查时要看容器主进程、信号处理、退出码和 kubelet 事件，而不是只调大 grace period。

## 121. graceful termination 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：graceful termination 是负载均衡后端退出的控制面和应用面协议。它让一个 Pod 从“可接新流量”变成“只处理存量工作”，再变成“安全退出”。如果这套流程设计不好，负载均衡和服务发现仍会把请求送到正在消失的实例。

对负载均衡来说，优雅终止决定下线窗口里的错误率。Service、Ingress、Gateway、Mesh、云 LB 都需要时间从后端池中移除旧 Pod。应用要在这段时间停止接新请求或返回合理关闭信号，同时保留处理存量连接的能力。长连接服务要额外考虑 GOAWAY、连接 drain、客户端重连和连接池刷新。

对服务发现来说，EndpointSlice 的 terminating/serving/ready 状态比单纯 ready 更有表达力。普通客户端可能只看到 Service ClusterIP，不感知后端变化；Headless 客户端或自定义发现客户端会直接看到 Pod IP 变化。优雅终止要考虑客户端缓存、DNS TTL、连接池和发现刷新周期。

对弹性伸缩来说，缩容本质上就是批量终止 Pod。HPA 缩容、Deployment 缩副本、Cluster Autoscaler 节点缩容、VPA 驱逐都会触发终止流程。终止慢会拖慢资源回收，终止粗暴会制造错误。PDB、readiness、preStop、grace period、应用 shutdown 和上游 drain 时间要一起校准。

对可观测性来说，终止流程需要单独监控。要看 Pod deletionTimestamp 到退出的耗时、Terminating Pod 数、SIGTERM 到进程退出时间、SIGKILL 次数、EndpointSlice terminating 数、下线期间新请求数、长连接关闭原因和发布期间错误率。没有这些指标，团队只能在用户报错后猜是哪一段没跟上。

## 122. HPA 在云原生系统中解决什么问题？

可以先这样答：HPA，Horizontal Pod Autoscaler，解决的是“工作负载副本数要随负载自动增减”的问题。流量、队列、CPU 或自定义业务指标上升时，HPA 增加 Pod 副本分摊压力；负载下降时，HPA 减少副本节省资源。它处理的是横向扩缩容，也就是加减实例数，而不是给单个 Pod 调更多 CPU 或内存。

HPA 的价值在弹性和自动化。没有 HPA，团队要按峰值长期保留副本，成本高；或者人工扩容，响应慢。HPA 让服务可以根据平均 CPU、内存、自定义指标、外部指标等动态调整规模。无状态 API、worker、消费组、网关层都常用 HPA。

它不是容量规划的替代品。HPA 需要指标采集、Pod 启动时间、readiness、负载均衡、节点容量和应用水平扩展能力配合。如果应用有全局锁、单分区瓶颈、长连接粘性、冷启动很慢或下游容量固定，单纯加 Pod 不一定提升吞吐。

HPA 也不直接增加节点。HPA 增加 Pod 副本后，如果节点资源不足，Pod 会 Pending；这时要靠 Cluster Autoscaler 或节点自动扩容补容量。HPA 只改工作负载的 scale 子资源，节点层容量是另一个控制器的职责。

面试里可以这样收束：HPA 解决实例数量的自动调节，前提是指标可靠、应用能水平扩展、调度和节点容量跟得上、readiness 能准确表达新副本何时可用。
## 123. HPA 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：HPA 由一个 Kubernetes API 对象和控制器组成。HPA 对象声明要扩缩哪个目标、最小/最大副本数、使用哪些指标和目标值；控制器周期性读取指标，计算期望副本数，然后通过目标工作负载的 `scale` 子资源修改副本数。目标通常是 Deployment、ReplicaSet、StatefulSet 或其他支持 scale 子资源的对象。

HPA 的基本算法是按当前指标和目标指标的比例计算副本数。直观公式是：期望副本数约等于当前副本数乘以当前指标值和目标指标值的比值，再向上取整。比如目标 CPU 利用率是 50%，当前平均 100%，副本数大致翻倍。实际控制器还会处理容忍区间、缺失指标、not ready Pod、多指标取最大建议、副本上下限和缩容稳定窗口。

指标来源很关键。CPU、内存这类资源指标通常来自 Metrics Server 提供的 `metrics.k8s.io`；自定义指标和外部指标需要通过 Kubernetes API aggregation 注册对应 API。HPA 本身不采集指标，它只是查询指标 API。如果 metrics-server 不可用、指标延迟大、adapter 错误或 RBAC 不对，HPA 就无法正确决策。

Pod readiness 也会影响 HPA。控制器在计算 CPU 指标时会保守处理刚启动、not ready 或缺指标的 Pod，避免启动期指标把扩缩容放大。比如新 Pod 还没 ready，HPA 不应立刻把它当成稳定承载能力。readiness 和启动探针配置不当，会影响 HPA 对真实负载的判断。

涉及组件包括 HPA 对象、horizontal pod autoscaler controller、API Server、目标工作负载的 scale subresource、Metrics Server、自定义/外部指标 adapter、Deployment/StatefulSet 控制器、scheduler、kubelet/cAdvisor、readinessProbe、Cluster Autoscaler 和 Service 数据面。HPA 只负责把副本数调到目标值，Pod 能否调度、启动、ready、接流量，是后续组件的职责。

## 124. HPA 配置错误时会导致哪些线上问题？

可以先这样答：HPA 配错会导致扩容不及时、缩容过快、反复抖动、成本失控或根本不扩容。线上表现可能是高峰期 Pod 数不涨、CPU 打满、请求排队、尾延迟上升；也可能是流量下降后副本数迟迟不降，或者在阈值附近来回扩缩造成发布和连接不稳定。

指标选错是第一类问题。CPU 对 I/O 型服务不敏感，队列型 worker 更应该看队列长度、消费延迟或每 Pod backlog；长连接网关可能 CPU 不高但连接数爆了；缓存服务可能内存和命中率更重要。只用平均 CPU 容易把真实瓶颈藏起来。自定义指标要明确单位、窗口、聚合方式和每 Pod/全局含义。

resources requests 写错会影响 CPU 利用率。HPA 的 CPU utilization 通常基于 CPU request 计算，request 太低会让利用率看起来很高，过度扩容；request 太高会让利用率看起来很低，不扩容。没有设置 request 的容器还会让某些资源指标不可用。HPA 和资源请求不是两套独立配置。

上下限和行为策略也容易出问题。`maxReplicas` 太低，高峰期扩不上去；`minReplicas` 太低，低谷缩到冷启动风险很高；scaleUp 太慢，突发流量打爆；scaleDown 太快，刚恢复就缩掉容量。缩容稳定窗口、扩容速率、冷启动时间、readiness 和负载均衡传播都要一起考虑。

指标系统故障会让 HPA 失明。metrics-server 掉了、自定义指标 adapter 延迟、Prometheus 查询过慢、指标缺失或权限错误，都会让 HPA 跳过缩容或无法扩容。生产里要把 HPA condition、指标 API 可用性、desired/current replicas 和 scaling events 放进告警，而不是只看 Pod 数。

## 125. HPA 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：HPA 直接影响弹性伸缩，间接影响负载均衡和服务发现。它通过调整副本数改变 Service 后端数量；Pod 创建、调度、CNI 配网、readiness 通过、EndpointSlice 更新后，新容量才真正进入负载均衡。HPA 决策快，不代表业务容量立刻增加。

对负载均衡来说，HPA 增加后端数量可以分摊连接和请求，但效果取决于负载均衡粒度。短连接 HTTP 通常更容易均匀分散；HTTP/2、gRPC、WebSocket 和长连接会粘在已有连接上，新 Pod 加入后不一定马上分到请求。需要连接 drain、客户端重连、网关策略或请求级负载均衡配合。

对服务发现来说，HPA 改变的是后端集合。新 Pod ready 后，EndpointSlice 里新增 endpoint；缩容时，endpoint 进入 terminating 并退出服务池。CoreDNS 对普通 ClusterIP Service 通常仍返回同一个 ClusterIP，真正后端变化由 EndpointSlice 和数据面处理；Headless Service 客户端可能直接看到 Pod 地址变化。

对弹性伸缩来说，HPA 只是多控制器链路里的一个环节。HPA 扩 Pod，scheduler 找节点，Cluster Autoscaler 补节点，CNI 配网络，readiness 进服务池，PDB 和 graceful termination 控制缩容下线。任一环慢了，端到端弹性都会慢。面试里要把“副本数变化”和“服务容量变化”分开。

对可观测性来说，要同时看 HPA desired/current replicas、指标值、目标值、condition、扩缩容事件、Pod Pending、ready 延迟、EndpointSlice 后端数、节点容量和业务 SLO。只看 HPA 把副本数调上去了，不足以证明扩容成功；最终要看每 Pod 负载下降、错误率下降和尾延迟恢复。

## 126. VPA 在云原生系统中解决什么问题？

可以先这样答：VPA，Vertical Pod Autoscaler，解决的是 Pod 资源请求和限制配置不准的问题。很多服务的 CPU、内存 request 是靠经验写的：写低了，调度器低估资源需求，节点容易过载、OOM、CPU throttling；写高了，集群资源被浪费，Pod 调度困难，节点扩容过度。VPA 根据历史和当前资源使用给出或应用更合适的 requests/limits。

VPA 处理的是纵向扩缩容，也就是调整单个 Pod 的资源规格，不是加减副本。它适合资源画像相对稳定、人工调参成本高、希望做 rightsizing 的工作负载。对数据库、缓存、后台任务、内部服务、低波动服务，VPA 的建议很有价值；对流量大幅波动且可以水平扩展的无状态服务，HPA 往往更直接。

VPA 的价值首先在调度准确性。调度器按 requests 判断节点能否容纳 Pod，requests 太低会造成超卖，太高会造成碎片和浪费。VPA 让 requests 更接近真实需求，使调度、节点容量规划和 Cluster Autoscaler 判断更准。

它也能降低 OOM 和 throttling 风险。内存 request/limit 太低，应用容易 OOMKilled；CPU request 太低，负载高时竞争更激烈；limit 过紧会出现 CPU throttling。VPA 可以根据历史峰值、OOM 事件和资源使用趋势给出更合理的上下界。当然，是否自动应用，要看业务能否接受重建或原地调整。

要注意当前 Kubernetes 文档里的边界：VPA 是 CRD 和控制器体系，不像 HPA 那样属于核心 API，通常需要单独安装。它不是所有集群开箱即用，也不是一开就自动安全。生产里常先用推荐模式观察，再逐步决定哪些 workload 允许自动更新。

## 127. VPA 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：VPA 通过 VerticalPodAutoscaler CRD、Recommender、Updater 和 Admission Controller 协同工作。Recommender 分析目标 Pod 的 CPU、内存历史使用、当前使用和 OOM 等事件，生成 target、lower bound、upper bound 等建议；Updater 根据策略决定是否更新已有 Pod；Admission Controller 在新 Pod 创建时把推荐的 requests/limits 注入进去。

VPA 对象用 `targetRef` 指向 Deployment、StatefulSet 等目标工作负载，并可以配置 updateMode。`Off` 模式只生成建议，不自动修改；`Initial` 只在 Pod 创建时设置资源；`Recreate` 会通过驱逐让工作负载重建 Pod；`InPlaceOrRecreate` 优先尝试原地调整，不行再重建；`InPlace` 在满足版本和 feature gate 的条件下只尝试原地调整。`Auto` 模式已经被废弃，不应作为新配置首选。

VPA 依赖指标来源，通常需要 Metrics Server 和 `metrics.k8s.io`。Recommender 会根据目标工作负载的 selector 找到相关 Pod，分析 CPU、内存使用和历史模式。建议值写入 VPA status。Admission Controller 是 mutating webhook，拦截 Pod 创建请求，给被 VPA 管理的容器设置推荐资源。

Updater 是影响可用性的关键。传统模式下，很多资源调整需要驱逐 Pod，再由 Deployment/StatefulSet 创建新 Pod。Updater 会考虑 update policy、阈值、PDB 和当前状态，避免过度打扰业务。支持原地调整时，可以减少重启，但仍要看 Kubernetes 版本、feature gate、节点可用容量和资源类型限制。

涉及组件包括 VPA CRD、VPA Recommender、VPA Updater、VPA Admission Controller、API Server、Metrics Server、目标工作负载的 selector、Pod resources、LimitRange、ResourceQuota、PDB、scheduler、Cluster Autoscaler 和 kubelet。VPA 修改的是资源请求/限制，后续调度和节点容量仍由 Kubernetes 其他组件处理。

## 128. VPA 配置错误时会导致哪些线上问题？

可以先这样答：VPA 配错会造成资源反复调整、Pod 被频繁驱逐、请求过高导致调度困难、请求过低导致 OOM 或 throttling，也可能和 HPA、PDB、LimitRange、ResourceQuota 互相冲突。VPA 本意是 rightsizing，但错误配置会变成新的扰动源。

最常见问题是对不适合的工作负载自动应用。低延迟在线服务如果不能接受 Pod 重建，却把 VPA 设置为 Recreate，Updater 可能在业务高峰驱逐 Pod；StatefulSet 或有本地状态的服务如果没有 PDB 和优雅终止，自动驱逐会造成可用性风险。生产里常先用 `Off` 看建议，再决定是否 `Initial` 或自动更新。

和 HPA 冲突也很典型。HPA 如果按 CPU utilization 扩缩容，而 VPA 同时调整 CPU request，HPA 的分母会变化。VPA 提高 request 后，CPU 利用率看起来下降，HPA 可能缩容；VPA 降低 request 后，利用率看起来上升，HPA 可能扩容。通常不建议让 HPA 和 VPA 同时基于 CPU 控制同一个 workload，除非明确分工，例如 HPA 用外部 QPS/队列指标，VPA 调 requests。

资源边界配置不当会带来调度问题。`minAllowed` 太高，Pod request 被抬得很大，节点放不下，Cluster Autoscaler 被迫扩很多节点；`maxAllowed` 太低，VPA 无法给出足够资源，应用继续 OOM；`controlledValues: RequestsAndLimits` 会同时调整 limits，可能改变 throttling 或 OOM 行为；LimitRange 和 ResourceQuota 还可能截断或拒绝推荐值。

指标质量差会让建议失真。metrics-server 缺失、采样窗口太短、启动期尖峰、批任务周期性波动、内存泄漏、异常流量都会影响推荐。VPA 看到的历史不一定代表未来。对有明显昼夜波动或事件驱动的服务，要结合业务周期和压测结果审查建议。

## 129. VPA 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：VPA 主要影响单个 Pod 的资源规格和调度质量，间接影响服务容量、扩缩容策略和节点资源利用率。它不直接修改 Service 后端数量，也不生成服务发现记录，但资源 requests 的变化会影响 Pod 能否被调度、能承载多少负载、是否触发 HPA 或 Cluster Autoscaler 的后续动作。

对负载均衡来说，VPA 调大资源后，单个 Pod 可能能承受更多并发，尾延迟和 OOM 风险下降；调小资源后，资源利用率提高，但每个后端的承载能力可能下降。如果 Service 仍按连接或请求平均分配流量，VPA 造成的后端能力差异要小心。滚动应用新资源规格时，旧 Pod 和新 Pod 可能短时间规格不同。

对服务发现来说，VPA 的影响来自 Pod 重建或原地调整。Recreate 模式会驱逐 Pod，新 Pod 创建后才进入 EndpointSlice；InPlace 模式减少重建，但资源变化仍可能影响 readiness 和性能。Headless Service、有状态服务和长连接客户端会更明显感受到 Pod 重建或短暂不可用。

对弹性伸缩来说，VPA 和 HPA、Cluster Autoscaler 强相关。VPA 调整 requests 后，scheduler 和 Cluster Autoscaler 会按新 requests 计算容量；HPA 的 CPU utilization 也会受 request 变化影响。合理的组合是：VPA 做资源基线校准，HPA 用更贴近业务吞吐的指标做横向扩缩容，CA 根据调度压力补节点。

对可观测性来说，VPA 要看推荐值、实际 requests/limits、updateMode、驱逐次数、OOM、CPU throttling、Pod Pending、节点利用率和 HPA 行为变化。不要只看 VPA status 里的 target recommendation；还要看应用 SLO 是否改善、资源浪费是否下降、是否引入新的重启或调度失败。

面试里可以这样结尾：HPA 解决“要几个 Pod”，VPA 解决“每个 Pod 给多少资源”。二者都叫 autoscaling，但控制变量不同，和 Service、调度、PDB、readiness、Cluster Autoscaler 的耦合方式也不同。

## 130. Cluster Autoscaler 在云原生系统中解决什么问题？

可以先这样答：Cluster Autoscaler 解决的是“Pod 已经需要运行，但集群没有足够节点容量”的问题。HPA 只能增加 Pod 副本，Deployment 只能声明期望副本，scheduler 只能在现有节点里找位置；如果节点资源不够，新 Pod 会停在 Pending。Cluster Autoscaler 负责把这种调度压力转化成节点池扩容请求，让云厂商或底层基础设施补节点。

它关心的是节点层面的弹性，而不是单个业务的 CPU 使用率。它会观察因为资源不足、节点选择、亲和性、污点容忍、卷拓扑等原因而无法调度的 Pod，然后判断增加某个 node group 的节点是否能让这些 Pod 变成可调度。这个边界很重要：Cluster Autoscaler 不是按 QPS 扩容，也不是直接看业务延迟扩容；它主要响应 Kubernetes 调度结果。

它还解决成本问题。低峰期如果某些节点长期利用率低，并且节点上的重要 Pod 可以被安全迁移到其他节点，Cluster Autoscaler 可以缩小节点池，释放底层 VM 或实例。这样集群不需要长期为峰值保留全部节点。

面试里可以把它和 HPA、VPA 分开说：HPA 回答“需要几个 Pod”，VPA 回答“每个 Pod 要多少资源”，Cluster Autoscaler 回答“集群要多少节点”。三者一起构成端到端弹性，但触发条件、控制对象和失败模式完全不同。

## 131. Cluster Autoscaler 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：Cluster Autoscaler 是一个独立控制器，周期性观察 API Server 里的 Pod、Node 和 node group 状态。它发现 unschedulable Pod 后，会模拟调度器判断这些 Pod 放到哪个节点池的新节点上能成功调度；如果某个节点池扩容后能容纳这些 Pod，并且没有超过最小/最大节点数、配额、可用区、实例类型等约束，它就调用 cloud provider 或节点组接口扩容。

扩容链路通常是：业务流量上升，HPA 增加 Deployment 副本；scheduler 发现新 Pod 放不下，把 Pod 标记为不可调度；Cluster Autoscaler 看到这些 Pending Pod，模拟不同 node group 的新节点模板；选中合适的 node group 后，向云厂商扩容节点池；新 VM 启动，kubelet 注册 Node；CNI、DaemonSet、节点初始化完成后，scheduler 再把 Pending Pod 调度上去。

缩容链路更谨慎。Cluster Autoscaler 会找长期不需要的节点，判断节点上的 Pod 是否能迁移到其他节点。PDB 太严格、Pod 没有控制器托管、本地存储、特殊 nodeSelector、hostPort、亲和性、反亲和性、不可迁移的系统 Pod，都可能阻止节点缩容。确认可以迁移后，它会 drain 节点，驱逐 Pod，再让底层节点组删除实例。

涉及组件包括 API Server、scheduler、Node、Pod、Deployment/ReplicaSet/StatefulSet、HPA、VPA、PDB、PriorityClass、taints/tolerations、nodeSelector、node affinity、TopologySpreadConstraints、DaemonSet、kubelet、cloud-controller-manager、cloud provider/node group API、CNI 和 Metrics/Events。它和 scheduler 的关系尤其要讲清楚：Cluster Autoscaler 不替代 scheduler，而是利用调度失败信号和调度约束来决定是否补节点。

## 132. Cluster Autoscaler 配置错误时会导致哪些线上问题？

可以先这样答：Cluster Autoscaler 配错会让集群在高峰期扩不出节点，或者低峰期缩不掉节点。前者表现为大量 Pod Pending、发布卡住、HPA desired replicas 已经上升但 current replicas 跟不上；后者表现为节点长期空闲、成本上升，甚至节点池在错误策略下反复扩缩。

最常见的问题是 node group 配置和 Pod 调度约束不匹配。比如 Pod 要求特定 GPU、可用区、标签、污点容忍或存储拓扑，但 Cluster Autoscaler 管理的节点池没有这些能力，扩容也没用。还有一种情况是节点池 max size 太小或云厂商配额不足，CA 已经发出扩容意图，但底层资源无法创建。

requests 配错会直接误导 CA。Pod request 太低，CA 认为现有节点能放下，实际上运行后 CPU 争抢、内存压力或 OOM 很严重；request 太高，CA 认为需要大节点或更多节点，导致过度扩容和资源碎片。DaemonSet request 也要考虑，因为新节点上会先跑系统 DaemonSet，真实可用容量不是节点标称容量。

缩容配置也容易伤业务。PDB 没配或太松，CA 可能在缩容时驱逐太多副本；PDB 太紧，又会让节点永远下不去。带本地状态、长连接、缓存预热、分片任务的 Pod 如果没有标注和下线策略，缩容会带来连接中断、缓存击穿、任务重跑或可用区容量失衡。生产里要把 scale-up failure、scale-down blocked、cloud quota、unremovable node、Pod Pending reason 都纳入告警。

## 133. Cluster Autoscaler 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Cluster Autoscaler 不直接处理请求负载均衡和服务发现，但它决定集群是否能提供足够节点让 Pod 运行。HPA 把副本数调上去只是第一步；如果没有节点，新增 Pod 只能 Pending，Service 后端不会增加，EndpointSlice 也不会出现新的 ready endpoint。

对负载均衡来说，CA 的影响体现在容量补齐的时间。新节点从云厂商创建、启动 OS、加入集群、拉镜像、配置 CNI、跑 DaemonSet、Pod ready，到 Service 真正转发流量，中间可能有几十秒到数分钟。短连接服务会比较快受益；长连接、gRPC、WebSocket 还需要连接重平衡或客户端重连。

对服务发现来说，CA 只是在更底层补节点。节点可用后，scheduler 才能把 Pod 放上去；Pod 通过 readiness 后，EndpointSlice controller 才会把它加入服务后端。Headless Service、StatefulSet、跨可用区拓扑感知路由都会把节点扩容位置暴露得更明显，因为 endpoint 的节点和区域分布会变化。

对弹性伸缩来说，CA 是 HPA/VPA 之后的节点层闭环。HPA 触发 Pod 增加，CA 负责给 Pending Pod 找节点；VPA 调整 requests，会改变 CA 对节点容量的判断。对可观测性来说，不能只看节点数，要联动看 Pending Pod、unschedulable reason、CA events、node group min/max、云配额、节点启动耗时、DaemonSet 占用、EndpointSlice 后端数和业务 SLO 恢复时间。


## 134. requests/limits 在云原生系统中解决什么问题？

可以先这样答：requests/limits 解决的是容器资源声明、调度和隔离问题。`requests` 告诉 Kubernetes 这个容器至少需要多少 CPU、内存等资源，scheduler 用它判断 Pod 能不能放到某个节点；`limits` 告诉运行时这个容器最多能用多少资源，kubelet 和容器运行时会把它转成 cgroup 约束。

requests 主要用于容量规划和调度。没有 request，调度器无法知道 Pod 的资源需求，只能按较弱的约束放置；request 太低，节点看起来能塞很多 Pod，但运行时会互相争抢；request 太高，Pod 会 Pending，节点资源碎片变多，Cluster Autoscaler 可能扩出更多节点。

limits 主要用于运行时保护。CPU limit 会限制容器可用 CPU 时间，超过后通常被 throttling；memory limit 则更硬，超过 cgroup 内存上限可能触发 OOM kill。CPU 和内存的语义不同，面试里最好明确：CPU 是可压缩资源，内存一般不可压缩，内存超限更容易直接导致进程被杀。

requests/limits 还影响 QoS。Kubernetes 会根据容器是否设置 CPU/内存 request 和 limit，把 Pod 归为 Guaranteed、Burstable 或 BestEffort。节点资源压力下，QoS 会影响驱逐优先级。生产配置里，资源声明不是“成本参数”，它同时决定调度、隔离、扩缩容和故障表现。

## 135. requests/limits 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：requests/limits 写在 Pod spec 的 container resources 里。Pod 创建后，API Server 保存声明；scheduler 读取每个容器的 requests，把同一个 Pod 内所有容器的资源请求加总，再结合节点 allocatable、已有 Pod request、node affinity、taints/tolerations、拓扑约束等条件做调度。调度成功后，kubelet 在目标节点上启动容器，并把 limits 交给容器运行时转成 cgroup 参数。

CPU request 是调度依据，也会影响 CPU shares/weight。节点 CPU 有空闲时，容器可以突发使用更多 CPU；如果多个容器竞争 CPU，request 越高通常获得的相对份额越高。CPU limit 则通过 CFS quota 或 cgroup v2 对应机制限制一个周期内可使用的 CPU 时间，超过就被 throttle。

内存 request 参与调度，也影响节点压力下的驱逐排序。内存 limit 会成为容器 cgroup 的上限。容器内进程申请内存超过限制，内核会在该 cgroup 内选择进程杀掉；Kubernetes 随后把容器状态报告为 OOMKilled，并按 restartPolicy 决定是否重启。

涉及组件包括 API Server、scheduler、kubelet、container runtime、Linux cgroups、LimitRange、ResourceQuota、QoS class、Node allocatable、Metrics Server、HPA、VPA、Cluster Autoscaler 和 eviction manager。LimitRange 可以给 namespace 内容器补默认 request/limit 或限制范围，ResourceQuota 可以限制整个 namespace 的资源总量。它们会让资源配置从单个 Pod 问题变成命名空间级治理问题。

## 136. requests/limits 配置错误时会导致哪些线上问题？

可以先这样答：requests/limits 配错会导致四类问题：调度不准、运行时受限、扩缩容误判、成本失控。线上可能看到 Pod 长期 Pending、节点过载、CPU throttling、OOMKilled、HPA 异常扩缩容、Cluster Autoscaler 频繁扩节点或缩不下来。

request 太低时，调度器会高估节点可承载的 Pod 数。结果是 Pod 都能调上去，但运行时 CPU 争抢、内存压力和磁盘/网络争用加重，业务表现为尾延迟上升、超时增多、重启变多。HPA 如果用 CPU utilization，request 太低还会让利用率看起来偏高，触发过度扩容。

request 太高时，Pod 可能因为没有节点满足需求而 Pending，即使实际使用并不高。节点资源碎片会变严重，Cluster Autoscaler 可能扩出更大或更多节点。对批任务和大内存服务来说，过高 request 还会拖慢排队时间，影响发布和任务吞吐。

limit 太低时，CPU 服务会被 throttling，内存服务会 OOMKilled。limit 太高或不设 limit，又可能让单个容器在异常时吃掉大量节点资源，影响同节点其他 Pod。生产上常见的坏味道是“所有服务套同一份默认 limit”，看似规范，实际把不同服务的资源曲线抹平了。

## 137. requests/limits 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：requests/limits 不直接改 Service 或 DNS，但它们决定 Pod 能否调度、能稳定承载多少流量、会不会被内核限速或杀掉。一个后端 Pod 即使已经进入 EndpointSlice，如果 CPU 被 throttle 或内存接近 OOM，它对负载均衡来说也是一个质量很差的后端。

对负载均衡来说，资源配置决定单 Pod 承载能力。CPU limit 太低会让请求处理时间变长，负载均衡仍然把流量打过来，结果是排队和尾延迟上升；内存 limit 太低会让 Pod 重启，连接被断开。后端数量相同，不代表后端能力相同。

对服务发现来说，requests/limits 主要通过 readiness 间接影响。资源不足导致应用启动慢、GC 频繁、依赖探测超时，readiness 可能反复失败，EndpointSlice 里的 ready 后端数会抖动。Headless Service 场景下，客户端直连 Pod IP，对单个 Pod 抖动更敏感。

对弹性伸缩来说，CPU request 是 HPA CPU utilization 的分母，Pod request 是 scheduler 和 Cluster Autoscaler 的容量依据。VPA 又会根据使用情况调整这些值。对可观测性来说，要同时看 request、limit、actual usage、CPU throttled time、OOMKilled、node pressure、Pod Pending、HPA desired/current、CA 扩容事件和业务延迟。只看 CPU 使用率不足以判断资源配置是否合理。


## 138. CPU throttling 在云原生系统中解决什么问题？

可以先这样答：CPU throttling 本身不是业务功能，它是 CPU limit 的执行结果，用来防止某个容器长期占用超过上限的 CPU 时间。云原生系统里多个 Pod 共享同一台节点，如果没有 CPU 上限，异常流量、死循环或 GC 峰值可能把同节点其他服务拖垮。CPU throttling 用隔离换稳定性。

它解决的是“可压缩资源如何公平分配”的问题。CPU 不像内存那样一超就必须杀进程，CPU 可以排队等待。容器超过 CPU quota 后，内核让它暂时停一停，下个周期再运行。这样节点不会被单个容器打满，但被 throttle 的应用会变慢。

面试里要把 throttling 和 CPU 高使用率区分开。CPU 使用率高说明应用真的在用 CPU；throttling 高说明应用想用更多 CPU，但被 limit 限住了。一个服务 CPU utilization 看起来只有 60%，仍然可能因为短周期突刺被频繁 throttle，表现为 P99 延迟上升。

它还暴露了资源配置和应用模型之间的矛盾。对低延迟服务，CPU limit 太紧会把请求排队放大；对批任务，throttling 可能只是任务跑得慢一点；对 JVM、Go runtime、Node.js 这类运行时，CPU 被限会影响 GC、线程调度或事件循环，症状不一定只表现为 CPU 指标。

## 139. CPU throttling 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：Kubernetes 里 CPU limit 会被 kubelet 和容器运行时转成 Linux cgroup 的 CPU 配额。容器在一个调度周期内用完允许的 CPU 时间后，内核会暂停它的运行，直到下一个周期重新获得 quota。CPU request 影响调度和竞争权重，CPU limit 才是触发 throttling 的主要配置。

在 cgroup v1 里，常见实现是 CFS quota 和 CFS period：period 是统计周期，quota 是周期内允许使用的 CPU 时间。比如 500m CPU 大致表示每个周期可用半个 CPU 的时间。cgroup v2 的接口不同，但思路类似，都是限制某个 cgroup 在时间窗口内可获得的 CPU。

Kubernetes 组件链路是：API Server 保存 Pod resources；scheduler 用 CPU request 调度；kubelet 在节点上为容器创建资源配置；container runtime 调用 runc/containerd 等设置 cgroup；Linux 内核执行调度和限速；cAdvisor/kubelet 暴露 throttling 指标；Metrics Server 或 Prometheus 采集后供 HPA、告警和排障使用。

要注意，CPU throttling 不是 kube-proxy、Service 或 Ingress 的行为。它发生在节点内核的容器资源隔离层。应用层看到的是响应变慢、线程调度延迟、GC 变慢、请求队列变长；Kubernetes 控制面看到的是容器还活着，除非 readiness/liveness 受到影响。

## 140. CPU throttling 配置错误时会导致哪些线上问题？

可以先这样答：CPU limit 配得太低，会让服务在高峰期被频繁限速，表现为平均 CPU 不一定高，但 P95/P99 延迟明显上升、超时增多、readiness 探测失败、HPA 判断滞后。很多线上问题看起来像网络慢、数据库慢，最后发现是容器被 CPU quota 卡住。

低延迟在线服务受影响最明显。请求线程、事件循环、GC、TLS 加解密、序列化、日志压缩都会争 CPU。被 throttle 后，队列等待时间会叠加到业务延迟上；如果 livenessProbe 也受到影响，Pod 可能被误杀，重启后又继续冷启动，形成抖动。

CPU limit 配得太高或不设 limit，也有风险。异常服务可能把节点 CPU 打满，导致同节点 Pod 抢不到 CPU。BestEffort 或 Burstable Pod 在节点压力下表现会更不可控。生产里常见做法不是盲目取消 limit，而是根据服务类型决定：低延迟服务可以更谨慎地设置 CPU limit，或者只用 request 管理调度和权重；多租户环境则可能必须保留 limit。

HPA 也会被误导。HPA 常看 CPU utilization，而 throttling 可能让 CPU 使用被限制在 limit 附近，吞吐上不去，指标却不一定按预期增长。更糟糕的是，request 太低会让 HPA 过度扩容，limit 太低又让每个 Pod 都跑不动。排障时要同时看 usage、request、limit 和 throttled seconds。

## 141. CPU throttling 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：CPU throttling 会把一个已经 ready 的 Pod 变成慢后端。Service、Ingress 或网关仍然会把流量转发给它，但它处理请求的速度下降，导致连接占用更久、队列更长、尾延迟更高。负载均衡只知道后端存在，不一定知道后端被 CPU 限速。

对服务发现来说，影响通常是间接的。如果 throttling 导致 readinessProbe 超时，Pod 会从 EndpointSlice 的 ready 后端里移除；如果只是业务慢但探针仍成功，服务发现层不会变化，流量继续进入慢 Pod。这个差别很重要，很多慢请求不会自动触发摘流。

对弹性伸缩来说，CPU throttling 会干扰 HPA 和容量判断。HPA 看到 CPU utilization 高可能扩容，但新 Pod 也带着同样低的 CPU limit，扩出来仍然慢；Cluster Autoscaler 可能补节点，但如果瓶颈是单 Pod limit，不是节点不足，补节点作用有限。VPA 可以通过调高 CPU request/limit 缓解，但也要考虑是否会改变调度和成本。

对可观测性来说，必须把 CPU throttling 指标放到服务 SLO 旁边看。常用信号包括 `container_cpu_cfs_throttled_seconds_total`、throttled periods、CPU usage、run queue、GC pause、request queue length、P99 latency、readiness failures 和 HPA events。只看 CPU 使用率低，可能会误判为服务还有余量；实际上它只是被限住了。


## 142. memory limit 在云原生系统中解决什么问题？

可以先这样答：memory limit 解决的是容器内存隔离问题。一个节点上会跑很多 Pod，如果某个容器因为内存泄漏、缓存无上限、突发请求或错误配置无限吃内存，就可能把整个节点拖入内存压力。memory limit 给容器设置上限，限制故障扩散范围。

内存和 CPU 的性质不同。CPU 超了可以排队，内存超了通常不能简单等待，因为应用已经申请了更多物理内存或页缓存。容器超过 memory limit 后，内核会在该 cgroup 内触发 OOM 处理，进程可能被杀。Kubernetes 随后把容器状态展示为 OOMKilled。

memory limit 还用于多租户和成本治理。没有上限的服务在低峰期可能看似正常，高峰期或异常路径下突然吃掉大量内存，影响同节点其他 Pod。设置合适的 limit 可以让异常容器尽早失败，而不是让节点级故障扩大。

但 limit 不是越低越好。很多应用需要内存缓存、堆外内存、文件页缓存或启动期峰值。limit 太紧会让应用频繁 OOM，或者因为 GC 压力增大导致延迟上升。生产里要结合内存工作集、峰值、GC、缓存策略和压测结果来定，而不是只按平均使用量定。

## 143. memory limit 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：Pod spec 里的 memory limit 由 API Server 保存，scheduler 不直接按 limit 调度，而主要按 memory request 调度。Pod 被调度到节点后，kubelet 调用容器运行时创建容器，并把内存限制写入 Linux cgroup。容器内进程的匿名内存、部分页缓存、堆外内存等会计入 cgroup 内存使用。

当容器内存使用接近或超过 cgroup 上限时，内核会尝试回收；如果回收后仍无法满足分配，就在该 cgroup 内触发 OOM kill。被杀的通常是容器里的主进程或某个高分进程。kubelet 观察到容器退出，记录 reason 为 OOMKilled，并根据 restartPolicy 和控制器逻辑重启或重建 Pod。

memory request 和 limit 共同影响 QoS。所有容器都设置相等的 CPU/内存 request 和 limit 时，Pod 可能是 Guaranteed；部分设置时通常是 Burstable；都不设则是 BestEffort。节点发生内存压力时，kubelet eviction manager 会根据阈值、Pod 使用量超过 request 的情况、QoS 等因素驱逐 Pod。

涉及组件包括 API Server、scheduler、kubelet、container runtime、Linux cgroups、OOM killer、QoS、eviction manager、LimitRange、ResourceQuota、VPA、Metrics Server、Prometheus/cAdvisor 和应用运行时。Java 堆、Go heap、堆外内存、内存映射文件和缓存策略都会影响实际表现，不能只看 Kubernetes YAML。

## 144. memory limit 配置错误时会导致哪些线上问题？

可以先这样答：memory limit 配错最直接的问题是 OOMKilled 和重启循环。limit 太低时，应用启动期、流量峰值、GC 前堆增长、批量查询、缓存预热都可能触发 OOM。线上表现为 Pod 重启、连接中断、请求失败、任务重跑、发布后新版本起不来。

有些问题更隐蔽。应用接近 memory limit 时，运行时可能更频繁 GC，CPU 消耗上升，延迟变差；页缓存不足会让磁盘读取变慢；堆外内存没有算进应用自己的 heap 监控，Kubernetes 却会把它算进容器内存。于是业务监控看 heap 正常，容器却 OOM。

limit 太高或不设也会出问题。一个异常 Pod 可以吃掉大量节点内存，引发 node memory pressure，kubelet 开始驱逐其他 Pod。对多租户集群来说，这会把单服务问题变成节点级故障。没有 request 还会让调度器低估内存需求，把太多 Pod 放到同一节点。

LimitRange 和 ResourceQuota 也可能制造意外。namespace 默认 limit 过小，开发者没显式设置时所有 Pod 都带着不合适的上限；ResourceQuota 限制太严，发布时 Pod 创建被拒绝；VPA 推荐值被 maxAllowed 或 LimitRange 截断，应用继续 OOM。排查时要同时看 Pod spec、namespace 策略和实际运行时内存。

## 145. memory limit 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：memory limit 会影响 Pod 是否稳定存在、能否保持 ready，以及是否能承受流量峰值。它不直接改 Service 规则，但 OOMKilled 会让后端进程退出，连接断开，EndpointSlice 里的 ready 状态随后变化。对用户来说，这通常表现为间歇性 5xx、超时或连接重置。

对负载均衡来说，内存不足的 Pod 可能先变慢，再重启。重启期间负载均衡应该通过 readiness 把它摘掉；如果 readiness 配得太宽松，流量会继续进入一个已经内存紧张、GC 频繁的后端。长连接服务、缓存服务和大响应服务尤其容易受到影响。

对服务发现来说，Pod OOM 后会退出 ready 后端集合，新 Pod 重启成功后再加入。Headless Service 场景中，客户端可能直接连接到具体 Pod，重启会更明显。StatefulSet 如果因为内存 limit 太低反复重启，服务发现记录还在，但对应实例实际不可用或不稳定。

对弹性伸缩来说，HPA 默认不看内存，除非配置资源指标或自定义指标。VPA 更适合根据历史使用推荐内存 request/limit；Cluster Autoscaler 只会按 request 和 Pending 结果补节点，不会因为某个 Pod OOM 自动加大内存。对可观测性来说，要看 working set、RSS、cache、OOMKilled 次数、restart count、GC、node memory pressure、eviction event、readiness 变化和业务错误率。


## 146. OOMKilled 在云原生系统中解决什么问题？

可以先这样答：OOMKilled 不是一种主动治理能力，而是 Kubernetes 对“容器因为内存不足被内核杀掉”这一结果的状态表达。它帮助运维和控制器识别容器退出原因：不是应用正常退出，不是探针杀死，不是镜像错误，而是内存使用超过了容器或节点能承受的范围。

它解决的是故障归因问题。没有 OOMKilled 这个状态，线上只能看到容器重启，很难区分是代码 panic、进程崩溃、健康检查失败还是内存上限被打穿。Kubernetes 把 container status、last state、reason、exit code、restart count 和事件暴露出来，让排障能沿着内存方向查。

OOMKilled 也保护节点。容器级 OOM 通常把问题限制在对应 cgroup 内；如果没有 limit 或节点整体内存压力过大，可能演变为节点级 OOM 或 kubelet 驱逐其他 Pod。对多租户集群来说，及时终止异常内存消费者比让整台节点失控更可接受。

面试里要注意措辞：OOMKilled 不“防止”内存问题，它只是内存问题已经发生后的处置和状态。真正的预防手段是合理的 request/limit、内存泄漏修复、缓存上限、VPA 建议、压测、GC 参数、探针和优雅重启策略。

## 147. OOMKilled 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：容器运行在 Linux cgroup 中，memory limit 会限制该 cgroup 的内存使用。进程申请内存时，如果 cgroup 内存超过限制且无法回收，内核 OOM killer 会选择进程杀掉。容器主进程退出后，容器运行时把退出状态报告给 kubelet；kubelet 更新 Pod status，`lastState.terminated.reason` 通常显示为 `OOMKilled`，exit code 常见为 137。

如果 Pod 的 restartPolicy 允许重启，kubelet 会重启容器；Deployment、ReplicaSet、StatefulSet 等控制器会继续维护期望状态。于是线上常看到容器反复 OOMKilled、restart count 上升，但 Pod 对象还在。CrashLoopBackOff 可能随后出现，它表示 kubelet 对连续重启做退避，不等于根因就是应用崩溃。

还要区分容器级 OOM、节点内存压力驱逐和系统级 OOM。容器超过自己的 memory limit，通常是 OOMKilled；节点内存压力下，kubelet eviction manager 可能按 eviction threshold 驱逐 Pod，reason 可能是 Evicted；极端情况下系统 OOM 会影响节点稳定性，表现更复杂。排障不能只看一个字段。

涉及组件包括 Linux kernel OOM killer、cgroups、container runtime、kubelet、API Server、Pod status、restartPolicy、Deployment/StatefulSet 控制器、QoS、eviction manager、metrics/cAdvisor、events、日志系统和告警系统。应用运行时也很关键，例如 JVM 最大堆、Go GC、native memory、线程栈和 mmap 都可能贡献到容器内存。

## 148. OOMKilled 配置错误时会导致哪些线上问题？

可以先这样答：配置错误会把 OOMKilled 变成反复发生的线上故障。最典型的是 memory limit 太低，服务在启动、预热、批量请求或流量峰值时被杀；控制器马上重启它，重启后又走同样路径，形成 CrashLoopBackOff 或周期性重启。

内存 request 太低也会造成问题。即使 limit 足够，scheduler 也可能把太多内存型 Pod 放在同一节点，节点整体内存压力升高，kubelet 开始驱逐 Pod。这个时候你看到的不一定是 OOMKilled，可能是 Evicted，但根子仍然是资源声明不准。

探针配置会放大影响。应用刚 OOM 重启，缓存还没热，readiness 如果过早放流量，流量又把内存打满；liveness 如果过于激进，会在 GC 或加载大模型/大配置时误杀进程。OOMKilled 后的恢复链路要和 startupProbe、readinessProbe、preStop、graceful termination 一起设计。

还有一类问题来自对内存来源的误判。只看应用 heap，不看 RSS、堆外内存、页缓存、连接缓冲区、线程栈、sidecar、日志 buffer，就会低估实际容器内存。服务网格 sidecar、日志采集 sidecar、TLS、压缩和大响应都可能让 Pod 的总内存高于主应用看到的数字。

## 149. OOMKilled 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：OOMKilled 会直接让容器退出，导致正在处理的请求失败、连接断开、Pod 暂时不可用。Service 和 Ingress 不会提前知道某个 Pod 要 OOM；只有 kubelet 更新状态、readiness 失败或容器重启后，EndpointSlice 和负载均衡才会反映后端变化。

对负载均衡来说，反复 OOM 的 Pod 会造成容量抖动。后端数量一会儿增加、一会儿减少，长连接被切断，短连接出现 5xx 或超时。如果副本数不多，一个 Pod OOM 就可能让剩余 Pod 承担更多流量，进一步推高内存，形成级联故障。

对弹性伸缩来说，OOMKilled 不一定自动触发扩容。HPA 如果只看 CPU，不会因为内存 OOM 直接加副本；Cluster Autoscaler 只看调度压力，不会加大单 Pod 内存；VPA 可以推荐更合适的内存 request/limit，但是否自动应用取决于策略。内存型瓶颈要配置内存指标或业务指标，否则扩缩容闭环会缺一环。

对可观测性来说，OOMKilled 是强故障信号。需要联动看 restart count、last termination reason、exit code、container memory working set、RSS、OOM events、node memory pressure、GC 日志、应用错误率、EndpointSlice 后端数和发布版本。排查时最好按时间线串起来：内存上升、探针变化、OOM、重启、服务发现摘除、流量重分配、SLO 变化。


## 150. ConfigMap 在云原生系统中解决什么问题？

可以先这样答：ConfigMap 解决的是配置和镜像解耦的问题。应用镜像应该尽量保持不可变，同一份镜像可以部署到开发、测试、生产等环境；环境差异、开关、地址、日志级别、非敏感配置放在 ConfigMap 里，由 Kubernetes 在运行时注入 Pod。

它适合存放非敏感、体量不大的键值配置。比如配置文件片段、功能开关、服务端口、日志级别、白名单、业务参数。敏感信息不应该放 ConfigMap，应该用 Secret 或外部密钥系统。ConfigMap 默认不是配置中心，它只是 Kubernetes API 里的配置对象。

ConfigMap 也让发布流程更清晰。镜像发布和配置变更可以分开管理，回滚时能区分是代码问题还是配置问题。对多环境部署来说，Deployment 模板稳定，ConfigMap 内容按环境替换，可以降低“重新构镜像只为了改配置”的成本。

不过 ConfigMap 不等于动态配置系统。应用是否能感知配置更新，取决于注入方式和应用自身是否 reload。通过环境变量注入的配置，Pod 不重启不会变；通过 volume 挂载的配置，kubelet 会周期性更新文件，但应用也要重新读取。面试里要避免把 ConfigMap 说成自动热更新。

## 151. ConfigMap 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：ConfigMap 是 Kubernetes API 对象，存储在 API Server 后端的 etcd 中。Pod 可以通过三种常见方式使用它：作为环境变量、作为命令行参数的来源、或者作为 volume 挂载成文件。kubelet 在节点上负责把 Pod 引用的 ConfigMap 内容投射到容器运行环境里。

环境变量方式最简单，但只在容器启动时生效。ConfigMap 后续更新，已经启动的容器环境变量不会自动改变。volume 挂载方式更适合配置文件，kubelet 会把 ConfigMap 内容写入投射卷，并在检测到对象变化后更新文件内容。更新不是强实时的，受 kubelet 同步周期和缓存策略影响。

ConfigMap 可以设置 immutable。对不需要变更的配置，把它设成不可变可以减少 kubelet watch 压力，也避免误改线上配置。但 immutable 一旦打开，就不能原地修改，只能创建新 ConfigMap 并更新引用。这种模式更接近“配置版本化”。

涉及组件包括 API Server、etcd、ConfigMap 对象、Pod spec、Deployment/StatefulSet 模板、kubelet、projected volume、environment variables、kubectl/apply、Helm/Kustomize、RBAC 和 admission policy。真正让应用生效的还包括应用 reload 逻辑、配置校验、滚动发布策略和回滚策略。

## 152. ConfigMap 配置错误时会导致哪些线上问题？

可以先这样答：ConfigMap 配错会造成启动失败、配置不生效、不同副本配置不一致、错误路由、功能开关误开、日志级别异常或依赖地址错误。它看起来只是配置对象，线上影响却可能和代码 bug 一样大。

引用错误很常见。Pod 引用了不存在的 ConfigMap 或 key，某些情况下容器会启动失败；如果配置文件路径挂错，应用会读到默认配置；如果 optional 设置不当，缺配置可能被悄悄忽略。发布后服务行为变了，但 Pod 状态看起来正常，这类问题最难排。

更新语义也容易误解。环境变量注入不会热更新，更新 ConfigMap 后旧 Pod 仍用旧值；volume 文件更新后，应用如果不 reload 也不会生效。多个副本滚动期间可能一部分用旧配置，一部分用新配置。对网关、限流、灰度、连接池、依赖地址这类配置，短暂不一致也可能造成线上异常。

把敏感信息放进 ConfigMap 是安全问题。ConfigMap 是明文非敏感配置对象，很多有读取 namespace 权限的人或组件都可能看到。还有体量问题：大配置、频繁变更配置、二进制大文件都不适合塞进 ConfigMap。配置要有校验、版本、回滚和审计，不能只靠手工改 YAML。

## 153. ConfigMap 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：ConfigMap 不直接实现负载均衡或服务发现，但它常常保存影响这些行为的参数。比如网关路由、客户端超时、服务地址、重试次数、限流阈值、采样率、日志级别和指标开关。配置错了，控制面对象没变，数据面行为却会变。

对负载均衡来说，ConfigMap 可能决定应用连接池大小、负载均衡策略、超时、重试和熔断。错误的超时和重试会放大下游故障；错误的后端地址会让流量打到旧服务或错误环境；错误的限流开关会让高峰期没有保护。Ingress Controller 和某些组件也会通过 ConfigMap 管理控制器参数。

对服务发现来说，应用如果绕过 Kubernetes Service，直接从 ConfigMap 读取地址列表，就要自己处理地址更新、健康检查和摘除。更推荐把服务发现交给 Service、DNS 或注册中心，ConfigMap 只放必要的策略参数。否则配置更新滞后会让客户端继续访问已经下线的地址。

对弹性伸缩和可观测性来说，ConfigMap 可能影响 HPA 使用的指标名称、Prometheus 抓取配置、日志级别、trace 采样率、worker 并发数和队列消费参数。改错后可能导致指标消失、告警失明、Pod 负载突然变化。排障时要把 ConfigMap 版本、Pod 重启时间、应用 reload 日志和业务指标变化放在同一条时间线上。


## 154. Secret 在云原生系统中解决什么问题？

可以先这样答：Secret 解决的是敏感配置的分发问题。应用运行需要数据库密码、Token、TLS 证书、镜像仓库凭据、OAuth client secret 等信息，如果把这些内容写进镜像、ConfigMap 或代码仓库，泄漏面太大。Secret 提供了 Kubernetes 原生的敏感数据对象，让 Pod 在运行时获取这些数据。

Secret 的价值在于把敏感数据和应用镜像、Deployment 模板解耦。镜像可以复用，环境差异通过 Secret 注入；权限可以通过 RBAC 控制；密钥可以单独轮转。它不是万能保险箱，但比把密码写进镜像或普通配置文件更容易治理。

要注意，Kubernetes Secret 默认数据是 base64 编码，不等于加密。真正的静态加密要配置 etcd encryption at rest；传输安全依赖 API Server TLS；访问控制依赖 RBAC、ServiceAccount 和审计。生产环境如果密钥要求更高，通常会接外部 KMS、Vault、云厂商 Secrets Manager 或 CSI Secret Store。

Secret 也支持不同类型，比如 Opaque、TLS、docker config、service-account-token 等。类型可以让 API Server 做部分格式校验，也方便控制器识别用途。面试里不要只说“Secret 存密码”，还要讲它在镜像拉取、TLS 证书、服务账号令牌和外部密钥集成里的角色。

## 155. Secret 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：Secret 是 API Server 管理的对象，数据存储在 etcd 中。Pod 可以通过环境变量、volume 挂载、imagePullSecrets 或 projected volume 使用 Secret。kubelet 会在节点上获取 Pod 引用的 Secret，并把内容以文件或环境变量的形式提供给容器。

环境变量注入和 ConfigMap 类似，只在容器启动时生效。volume 挂载方式会把 Secret 内容投射成文件，适合证书、私钥、配置片段。kubelet 通常把 Secret volume 放在内存型存储中，减少落盘风险，但节点上的特权进程和有权限的用户仍可能读取，所以节点安全也很重要。

Secret 的访问由 RBAC 控制。能 `get/list/watch secrets` 的主体风险很高，因为 Secret 读权限基本等同于拿到明文敏感数据。ServiceAccount token 相关 Secret 在新版本 Kubernetes 中也发生过演进，推荐使用 TokenRequest API 和 projected service account token，而不是长期静态 token。

涉及组件包括 API Server、etcd、Secret 对象、RBAC、ServiceAccount、kubelet、container runtime、projected volume、image pull、CSI Secret Store、encryption provider、审计日志和外部 KMS。密钥生命周期还涉及创建、分发、轮转、吊销、重启加载和泄漏响应，这些不是 Kubernetes 自动替你完成的。

## 156. Secret 配置错误时会导致哪些线上问题？

可以先这样答：Secret 配错会导致应用启动失败、认证失败、TLS 握手失败、镜像拉取失败、外部依赖不可访问，严重时还会造成密钥泄漏。Secret 错误通常不是“慢慢降级”，而是很快表现为 CrashLoopBackOff、ImagePullBackOff、401/403、证书错误或数据库连接失败。

引用错误最常见。Secret 名字或 key 写错，Pod 可能启动失败；证书和私钥不匹配，Ingress 或应用 TLS 会失败；imagePullSecrets 配错，节点拉不到私有镜像。通过环境变量注入时，Secret 更新后旧 Pod 不会自动拿到新值，这会让轮转不完整。

权限过宽是另一类严重问题。给业务 ServiceAccount 绑定了读取整个 namespace Secret 的权限，或者把 Secret list/watch 权限给了过多控制器，一旦应用被攻破，攻击者就能横向拿到更多凭据。Secret 的 RBAC 要按最小权限设计，能只挂载单个 Secret 就不要给 API 读取所有 Secret 的能力。

轮转不当也会出事故。新旧密码没有重叠期，应用重启顺序不对，证书到期前没有告警，外部系统和 Kubernetes Secret 不一致，都可能导致大面积不可用。密钥轮转要设计成流程：先写入新密钥，应用兼容双密钥，再切换依赖端，最后删除旧密钥。

## 157. Secret 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Secret 不直接做负载均衡和服务发现，但它影响很多组件能否启动、认证和建立连接。一个后端 Pod 如果数据库密码错、TLS 证书错或 token 失效，即使已经被调度，也可能无法 ready，或者 ready 后处理请求失败。

对负载均衡来说，Secret 常影响 TLS 终止、mTLS、后端认证和镜像拉取。Ingress 证书 Secret 错，会导致入口流量握手失败；服务网格证书或根证书错，会导致东西向调用失败；私有镜像仓库凭据错，会让新 Pod 起不来，扩容时没有新后端加入。

对服务发现来说，Secret 影响的是“发现后能否访问”。Service DNS 能解析，EndpointSlice 也有后端，但客户端凭据错仍然连不上。ServiceAccount token 失效或权限不足时，控制器、Operator、指标采集器可能无法访问 API Server，进而影响服务注册、CRD reconcile 或监控数据采集。

对弹性伸缩和可观测性来说，Secret 错误会让 HPA/CA 的效果失真。HPA 扩出新 Pod，但 imagePullSecret 错导致 ImagePullBackOff；指标 adapter 的 token 错导致 HPA 拿不到指标；Prometheus 抓取凭据错导致监控空洞。排障时要看 Secret 版本、证书有效期、Pod events、认证错误、TLS 错误、image pull 错误和 RBAC deny 日志。


## 158. ServiceAccount 在云原生系统中解决什么问题？

可以先这样答：ServiceAccount 解决的是 Pod 内进程访问 Kubernetes API 或其他集群服务时的身份问题。人用 User 或外部身份登录集群，应用和控制器则需要一个非人的身份。ServiceAccount 就是 Kubernetes 给工作负载使用的账号。

它让权限可以按工作负载隔离。比如业务 Pod 只需要读某个 ConfigMap，不应该有列出所有 Secret 的权限；Operator 需要 watch 自己的 CRD，但不应该能删除所有 namespace；metrics adapter 需要访问指标 API。每类工作负载绑定不同 ServiceAccount，再通过 RBAC 授权。

ServiceAccount 还解决 token 分发问题。Pod 启动后，可以通过 projected volume 获取短期、可轮转的 service account token，用这个 token 向 API Server 认证。现代 Kubernetes 推荐这种短生命周期 token，而不是长期静态 Secret token。

面试里可以一句话收束：ServiceAccount 是“谁在访问集群”的身份，RBAC 是“这个身份能做什么”的授权。把两者混在一起讲，会让权限模型不清楚。

## 159. ServiceAccount 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：ServiceAccount 是 namespace 级资源。Pod spec 可以通过 `serviceAccountName` 指定使用哪个 ServiceAccount；如果不指定，通常使用该 namespace 的 default ServiceAccount。Pod 创建后，ServiceAccount admission 相关逻辑会把 token、CA 证书和 namespace 信息以 projected volume 形式挂载到容器中，供 in-cluster client 使用。

应用访问 API Server 时，把 token 放在 Authorization header 中。API Server 的认证链会验证 token，得到类似 `system:serviceaccount:<namespace>:<name>` 的身份；随后授权模块，例如 RBAC，判断这个身份是否允许对目标资源执行 get、list、watch、create、update、delete 等动作。

ServiceAccount token 的实现经历过变化。现在更推荐 TokenRequest API 生成有过期时间、绑定到 Pod 的 token，并通过 projected volume 自动轮转。老式长期 token Secret 风险更高，泄漏后可长期使用，生产环境应尽量避免依赖长期静态 token。

涉及组件包括 ServiceAccount 对象、Pod spec、API Server authentication、TokenRequest API、projected volume、kubelet、RBAC Role/ClusterRole/RoleBinding/ClusterRoleBinding、admission controller、audit log 和客户端库。外部云身份集成时，还会涉及 workload identity、OIDC、IAM role 等机制。

## 160. ServiceAccount 配置错误时会导致哪些线上问题？

可以先这样答：ServiceAccount 配错会导致两类问题：权限不足导致组件不可用，权限过大导致安全风险。前者表现为控制器 watch 失败、Operator 无法 reconcile、应用读取 ConfigMap/Secret 失败、指标采集失败；后者表现为一个被攻破的 Pod 可以读取过多资源，甚至修改集群状态。

最常见的是用了 default ServiceAccount。很多团队忘记为工作负载单独创建账号，所有 Pod 共用默认身份。默认身份如果绑定了过宽权限，风险会扩散；如果没有权限，应用又会出现 `forbidden` 错误。每个需要访问 API 的组件都应该有明确的 ServiceAccount 和最小权限绑定。

token 挂载也要谨慎。并不是所有 Pod 都需要访问 Kubernetes API。对普通业务服务，如果不需要 API 访问，可以关闭 automountServiceAccountToken，减少 token 泄漏面。相反，控制器、Operator、指标组件如果 token 没挂载或 audience 不匹配，会出现认证失败。

跨 namespace 权限很容易配错。RoleBinding 只在某个 namespace 内授权；ClusterRoleBinding 会把权限授给整个集群范围。把一个 namespace 内应用绑定到 cluster-admin，是很危险的捷径。排障时要看 ServiceAccount、binding、实际请求的 resource、verb、namespace 和 API group。

## 161. ServiceAccount 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：ServiceAccount 不直接处理流量，但很多控制面和观测组件靠它访问 API Server。身份或权限错了，Service、EndpointSlice、HPA、metrics adapter、Operator、Ingress Controller 等组件可能无法读取或更新所需资源，间接影响负载均衡、服务发现和弹性伸缩。

对负载均衡来说，Ingress Controller、Gateway Controller、Service Mesh controller 需要 watch Service、EndpointSlice、Secret、Gateway、Ingress 等资源。如果它们的 ServiceAccount 权限不足，配置变更不会下发到数据面；权限过大则增加入口控制面的攻击面。

对服务发现来说，服务注册本身由 Kubernetes 控制器维护，但外部 DNS、服务网格、注册中心同步器、Operator 常常需要读取 Pod/Service/EndpointSlice。ServiceAccount 错会让这些组件看不到变化，造成 DNS 记录、xDS 配置或外部注册信息滞后。

对弹性伸缩和可观测性来说，HPA controller、metrics adapter、Prometheus、日志采集器、VPA、Cluster Autoscaler 都需要合适权限。权限不足会导致指标缺失、扩缩容失败、事件不可见；权限过宽会把观测系统变成高价值攻击入口。可观测性里要把 API Server audit、RBAC deny、controller error log 和业务 SLO 放在一起看。


## 162. RBAC 在云原生系统中解决什么问题？

可以先这样答：RBAC，Role-Based Access Control，解决的是 Kubernetes API 的授权问题。认证只回答“你是谁”，RBAC 回答“你能对哪些资源做哪些动作”。在多团队、多组件、多命名空间的集群里，没有 RBAC，任何身份都可能读取 Secret、修改 Deployment、删除 Pod 或创建高权限对象。

RBAC 把权限拆成资源、动作和作用域。资源可以是 Pod、Service、Secret、ConfigMap、CRD 等；动作包括 get、list、watch、create、update、patch、delete 等；作用域可以是 namespace 内，也可以是集群级。这样可以按最小权限给人、ServiceAccount 或组授权。

它也让控制器生态可控。Ingress Controller、Operator、metrics adapter、CI/CD agent 都需要访问 API Server，但每个组件需要的资源范围不同。通过 RBAC，可以让某个 Operator 只管理自己的 CRD，让某个业务 Pod 只读自己的 ConfigMap，而不是给它们 cluster-admin。

面试里要强调 RBAC 是允许式模型。没有匹配的允许规则，请求就会被拒绝。RBAC 本身不表达复杂条件，例如“只能在工作时间更新”或“镜像必须来自某仓库”，这类策略通常交给 Admission Controller 或外部策略引擎。

## 163. RBAC 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：请求到达 API Server 后，先经过认证得到 user、groups 或 ServiceAccount 身份，再进入授权阶段。RBAC authorizer 会查找与这个主体匹配的 RoleBinding 或 ClusterRoleBinding，找到绑定的 Role 或 ClusterRole，判断其中的 rules 是否允许该请求的 verb、apiGroup、resource、resourceName 和 namespace。

Role 是 namespace 级权限集合，只能授权本 namespace 内资源；ClusterRole 是集群级权限集合，既可以描述集群级资源权限，也可以被 RoleBinding 绑定到某个 namespace 内复用。RoleBinding 在 namespace 内生效，ClusterRoleBinding 在集群范围生效。这个组合经常是面试重点。

RBAC rule 通常包括 apiGroups、resources、verbs，也可以限制 resourceNames。非资源 URL 也可以用 ClusterRole 授权，例如访问 `/metrics` 或 `/healthz`。`list/watch secrets`、`create pods/exec`、`impersonate`、`bind/escalate` 这类权限风险很高，不能只看资源名字普通就放行。

涉及组件包括 API Server、authentication、authorization chain、RBAC authorizer、Role、ClusterRole、RoleBinding、ClusterRoleBinding、User/Group、ServiceAccount、Audit、kubectl auth can-i、aggregation rule 和 admission policy。RBAC 授权通过后，请求还可能被 admission 拦截，所以“RBAC 允许”不代表最终一定成功。

## 164. RBAC 配置错误时会导致哪些线上问题？

可以先这样答：RBAC 配错要么让组件不能工作，要么让权限面过大。权限不足时，控制器日志里会出现 forbidden，Operator 不 reconcile，HPA 拿不到指标，Ingress Controller 看不到 Secret，CI/CD 发布失败。权限过大时，一个普通 Pod 被攻破可能读取 Secret、修改工作负载甚至接管集群。

命名空间作用域经常出错。开发者以为 RoleBinding 绑定了 ClusterRole 就是全局权限，其实 RoleBinding 只在所在 namespace 生效；也有人为了省事直接用 ClusterRoleBinding，结果把权限放大到全集群。排查时要看 binding 类型和所在 namespace。

verb 配置也容易漏。控制器通常需要 get/list/watch 才能监听资源变化，需要 update/patch 才能写 status 或 finalizer。只给 get 不给 watch，控制器可能启动后无法持续工作；只给 update 不给 patch，某些 client 操作会失败。CRD 控制器还常常需要更新 `status` 子资源和处理 finalizers。

安全侧的典型错误是给了 wildcard。`apiGroups: ['*']`、`resources: ['*']`、`verbs: ['*']` 看起来省事，实际等于未来新增资源也自动授权。Secret、serviceaccounts/token、pods/exec、pods/attach、roles/bind、roles/escalate、impersonate 都要特别审查。RBAC 不是一次配完就结束，需要随组件版本和 API 变化复查。

## 165. RBAC 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：RBAC 通过控制组件能否读取和修改 Kubernetes API 资源，间接影响负载均衡、服务发现、弹性伸缩和可观测性。很多线上“配置没生效”并不是数据面坏了，而是控制器没有权限 watch 或 patch 相关对象。

对负载均衡来说，Ingress Controller、Gateway Controller、Service Mesh control plane 需要读取 Ingress/Gateway/Service/EndpointSlice/Secret，并把结果下发到数据面。RBAC 缺权限时，路由规则、证书或后端变化不会生效；权限过大时，入口控制面一旦被攻破，攻击者能改更多流量入口。

对服务发现来说，控制器和同步组件需要 watch Service、Pod、EndpointSlice。RBAC 不足会让外部 DNS、注册中心同步器、网格控制面拿不到 endpoint 变化，导致服务发现滞后。对 Operator 管理的服务，权限不足还会让自定义资源状态长期不更新。

对弹性伸缩和可观测性来说，HPA、VPA、Cluster Autoscaler、metrics adapter、Prometheus、日志采集器都依赖 RBAC。缺少 metrics API 权限，HPA 无法扩缩容；Prometheus 不能 list endpoints，就抓不到目标；CA 不能读 Pod/Node，就无法判断扩容。排障时直接用对应 ServiceAccount 执行 `kubectl auth can-i` 是很有效的验证方式。


## 166. Admission Controller 在云原生系统中解决什么问题？

可以先这样答：Admission Controller 解决的是 Kubernetes API 写入前的准入控制问题。认证和授权通过后，请求还没写入 etcd，Admission Controller 可以修改对象、拒绝对象或做策略校验。它让集群能在入口处统一执行安全、合规和默认化规则。

它常用于两类事情：mutating 和 validating。Mutating admission 可以给 Pod 注入 sidecar、补默认 labels、设置默认资源、添加 toleration；validating admission 可以拒绝特权容器、禁止 latest 镜像、要求 resource requests、限制 hostPath、校验 Ingress/Gateway/CRD 的规则。一个负责“改”，一个负责“验”。

Admission Controller 解决的是“对象能不能进入集群、进入前是否要被修正”的问题，而不是运行时流量治理。比如它可以拒绝没有 readinessProbe 的 Deployment，但不能保证应用运行时真的健康；可以注入 sidecar，但 sidecar 后续是否稳定运行还要看 kubelet 和数据面。

面试里要把它和 RBAC 分开。RBAC 判断某个主体有没有权限创建 Pod；Admission 判断这个 Pod 的内容是否符合规则。一个用户有 create pod 权限，也可能因为 Pod 使用 privileged 或缺少资源限制而被 admission 拒绝。

## 167. Admission Controller 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：请求进入 API Server 后，通常经历认证、授权、准入控制、持久化等阶段。Admission Controller 只处理写请求，例如 create、update、delete、connect 等，不处理普通 read。准入链中先运行 mutating admission，再运行 validating admission，最后对象才会写入 etcd。

内置 admission controller 是 API Server 进程内的插件，例如 NamespaceLifecycle、LimitRanger、ResourceQuota、ServiceAccount、DefaultStorageClass 等。它们承担很多基础治理能力。Webhook 则通过 MutatingAdmissionWebhook 和 ValidatingAdmissionWebhook 把请求发给外部服务，由外部策略或控制器决定是否修改或拒绝。

Webhook 配置里有很多关键字段：匹配哪些资源和操作、失败策略是 Fail 还是 Ignore、超时时间、namespaceSelector/objectSelector、sideEffects、admissionReviewVersions、reinvocationPolicy 等。Mutating webhook 还要考虑顺序和幂等，因为多个 webhook 可能先后修改同一个对象。

涉及组件包括 API Server、admission chain、内置 admission plugins、MutatingWebhookConfiguration、ValidatingWebhookConfiguration、webhook service、TLS 证书、RBAC、etcd、审计日志、策略引擎和被拦截的 Kubernetes 资源。Admission Controller 是控制面路径的一部分，所以它的可用性和延迟会直接影响资源创建和更新。

## 168. Admission Controller 配置错误时会导致哪些线上问题？

可以先这样答：Admission Controller 配错会让资源创建失败、发布卡住、控制面变慢，甚至让整个集群很多写操作不可用。典型表现是 kubectl apply 超时、Deployment 新 Pod 创建失败、Operator reconcile 失败、证书 webhook 报错、CI/CD 大面积失败。

Webhook 可用性是最大风险之一。如果 failurePolicy 设置为 Fail，而 webhook 服务宕机、证书过期、DNS 失败或网络不通，匹配到的 API 请求都会被拒绝。反过来，如果关键安全策略设置为 Ignore，webhook 掉线时不合规对象又可能进入集群。安全和可用性要按策略重要程度取舍。

Mutating webhook 配错会产生很隐蔽的问题。比如 sidecar 重复注入、资源 request 被改坏、镜像被替换、环境变量覆盖、label/annotation 变化触发控制器异常。多个 webhook 顺序不同，还可能造成最终对象不稳定。Webhook patch 要幂等，策略要尽量可解释。

Validating webhook 过严会阻断正常变更。CRD schema 或策略升级后旧对象不再合法，更新 status 也被拒绝；namespaceSelector 配错导致系统 namespace 被拦；timeout 太长拖慢 API Server；对象选择范围过大导致每次发布都依赖一个外部服务。生产里要监控 admission latency、reject count、webhook error 和证书有效期。

## 169. Admission Controller 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Admission Controller 不直接转发流量，但它决定哪些对象能被创建和以什么形态创建，因此会影响负载均衡、服务发现和扩缩容链路。比如它拒绝没有 label 的 Pod，Service selector 就匹配不到后端；它注入 sidecar，负载路径和指标路径都会变化。

对负载均衡来说，admission 可以强制注入服务网格 sidecar、设置 readinessGate、要求探针、限制 hostNetwork/hostPort、校验 Ingress 或 Gateway 策略。这些规则能提高流量治理一致性，但也可能因为 webhook 故障导致新后端无法创建，扩容和发布都卡住。

对服务发现来说，admission 经常补 label、annotation、ServiceAccount、DNS 策略或 sidecar 配置。补错 label 会让 Service selector 错配；拒绝 EndpointSlice 相关控制器写入会造成服务发现异常；CRD 的 validating webhook 如果拒绝自定义服务资源，Operator 就无法更新真实 Service。

对弹性伸缩和可观测性来说，admission 影响 HPA/VPA/CA 的输入对象。它可以强制设置 resources，让调度和 HPA 更稳定；也可能因为策略过严让 HPA 新建出的 Pod 被拒绝。观测侧要看 API Server admission 指标、webhook 延迟、拒绝原因、审计日志、controller events 和业务发布时间线。很多“扩容失败”根因其实是 admission 拒绝了新 Pod。


## 170. CRD 在云原生系统中解决什么问题？

可以先这样答：CRD，CustomResourceDefinition，解决的是扩展 Kubernetes API 的问题。Kubernetes 内置资源只有 Pod、Service、Deployment、ConfigMap 等，但很多平台能力需要表达更高层对象，比如 Certificate、Gateway、VirtualService、Backup、Database、RedisCluster。CRD 让这些对象像原生资源一样通过 API Server 创建、查询、watch 和声明式管理。

它的核心价值是把平台抽象变成 Kubernetes 对象。用户不需要直接操作一堆 Deployment、Service、Secret 和 Job，而是提交一个自定义资源，例如 `DatabaseCluster`；背后的 controller 根据 spec 去创建和维护底层资源。这就是很多 Operator 和云原生平台的基础。

CRD 还统一了生态工具链。kubectl、RBAC、watch、admission、OpenAPI schema、finalizer、status、ownerReference、GitOps、审计日志都可以围绕自定义资源工作。相比自己做一套 API Server，CRD 的门槛低很多。

但 CRD 只定义 API，不自动实现业务逻辑。创建 CRD 后，API Server 只是能存取这种对象；真正让对象变成现实状态，需要 controller 或 Operator reconcile。面试里要特别说明：CRD 是“声明新资源类型”，Controller 是“让声明生效”。

## 171. CRD 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：CRD 本身是 Kubernetes 资源，提交给 API Server 后，API Server 会注册新的 REST API endpoint，例如 `apis/example.com/v1/namespaces/default/widgets`。之后用户可以创建该 group/version/kind 的自定义资源实例，API Server 会按 CRD 的 schema 做校验并把对象存到 etcd。

CRD 通常包含 group、names、scope、versions、schema、subresources、additional printer columns 等配置。scope 决定资源是 namespace 级还是 cluster 级；versions 决定 API 版本；schema 决定字段校验和裁剪；status 子资源允许 controller 独立更新状态；scale 子资源可以让 HPA 等组件按标准方式扩缩容自定义工作负载。

版本演进是 CRD 的重点。一个 CRD 可以有多个 served version，但通常只有一个 storage version。不同版本之间如果 schema 不兼容，需要 conversion webhook 做转换。否则升级 CRD 时可能出现旧对象读写失败、字段丢失或 controller 无法理解对象。

涉及组件包括 API Server、apiextensions-apiserver、etcd、OpenAPI schema、CRD conversion webhook、admission webhook、RBAC、kubectl、client-go informer、controller-runtime、finalizer、ownerReference、status subresource 和自定义 controller。CRD 把对象放进 Kubernetes API，controller 才负责根据对象做实际动作。

## 172. CRD 配置错误时会导致哪些线上问题？

可以先这样答：CRD 配错会导致自定义资源无法创建、字段被拒绝或被裁剪、版本升级失败、controller reconcile 异常，严重时会影响依赖它的平台组件。很多云原生组件本身就是靠 CRD 工作的，CRD 坏了，上层平台能力也会坏。

schema 错误很常见。字段类型写错、required 设置过严、枚举不兼容、默认值不合理，会让用户对象创建失败；preserveUnknownFields 和结构化 schema 理解不对，可能导致字段被 API Server 裁剪。对象存进去了但字段没了，controller 就会按错误 spec 执行。

版本和 conversion 更危险。升级 CRD 时，如果 storage version、served version、conversion webhook 没设计好，旧对象可能无法转换，新 controller 读不懂旧 spec，回滚也困难。CRD 一旦被多个团队和 GitOps 流水线使用，API 兼容性就像公共接口一样重要。

finalizer 和删除流程也容易出问题。controller 给自定义资源加 finalizer 后，如果 controller 崩了或权限不足，资源删除会卡在 terminating；ownerReference 配错会导致级联删除异常；status 子资源没开，controller 更新状态失败，用户看不到真实进度。CRD 的设计质量会直接影响平台可维护性。

## 173. CRD 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：CRD 通过定义高层资源间接影响负载均衡、服务发现和扩缩容。Gateway API、Service Mesh、证书管理、数据库 Operator、Prometheus Operator、KEDA 等生态里，大量能力都是 CRD 加 controller 实现的。用户改的是 CRD 实例，最终变化会落到 Service、EndpointSlice、Ingress、Gateway、Deployment 或外部系统。

对负载均衡来说，CRD 可能表达路由、流量分配、重试、熔断、TLS、灰度策略。controller watch CRD 后生成或下发真实的数据面配置。如果 CRD schema 或 controller 权限错了，用户看起来提交了策略，实际负载均衡器没有变化。

对服务发现来说，CRD 可以描述服务注册、DNS 记录、网格服务条目、外部服务映射。controller 根据这些资源创建 Service、EndpointSlice、DNSRecord 或 xDS 配置。CRD 状态如果不更新，用户很难知道服务发现是否已生效。

对弹性伸缩和可观测性来说，CRD 可以通过 scale subresource 接入 HPA，也可以由 KEDA 之类组件按外部事件扩缩容；Prometheus Operator 通过 ServiceMonitor/PodMonitor 这类 CRD 管理抓取配置。可观测性要看 CRD spec、status、conditions、events、controller reconcile error、生成的下游资源和数据面实际状态，不能只看自定义资源存在。


## 174. Operator 在云原生系统中解决什么问题？

可以先这样答：Operator 解决的是把有状态应用或复杂平台组件的运维知识自动化的问题。普通 Deployment 只会维持副本数，不能理解数据库备份、主从切换、证书轮转、分片扩容、版本升级、故障恢复这些领域动作。Operator 把这些规则写进控制器，让 Kubernetes 通过声明式 API 管理更复杂的系统。

Operator 通常基于 CRD 工作。用户提交一个自定义资源，比如 `PostgresCluster`、`KafkaCluster`、`Certificate` 或 `Prometheus`，Operator watch 这个资源，根据 spec 创建和维护 StatefulSet、Service、Secret、ConfigMap、PVC、Job、Ingress、Gateway 等对象，并把实际状态写回 status。

它的价值在于把“人工 runbook”变成持续 reconcile。节点故障、Pod 重建、证书过期、备份任务失败、版本不一致时，Operator 可以根据目标状态和当前状态做补偿。对平台团队来说，这比把所有步骤写进 CI/CD 脚本更接近 Kubernetes 的控制器模型。

但 Operator 不是魔法。它只会执行作者编码进去的运维策略，策略错了会稳定地把系统修到错误状态。面试里可以说：Operator 的能力上限取决于 CRD 设计、reconcile 逻辑、权限边界、状态建模和对底层系统的理解。

## 175. Operator 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：Operator 本质上是一个或多个 Kubernetes controller。它通过 informer/watch 监听 CRD 实例和相关下游资源，把事件放入工作队列，然后执行 reconcile：读取期望状态 spec，读取当前集群状态，计算差异，创建、更新或删除资源，最后写回 status 和 conditions。

典型流程是：用户创建自定义资源；API Server 保存对象；Operator 收到 watch 事件；reconcile 创建 StatefulSet、Service、Secret、PVC、Job 等资源；通过 ownerReference 建立归属关系；通过 finalizer 控制删除前清理；通过 status 汇报进度、错误和可用性。失败时，controller 会重试或按退避策略重新入队。

Operator 需要 RBAC 授权。它通常运行在某个 namespace 的 Pod 里，使用自己的 ServiceAccount 访问 API Server。权限必须覆盖它管理的 CRD 和下游资源，但不应无限扩大。多租户集群里，还要考虑 Operator 是 namespace-scoped 还是 cluster-scoped。

涉及组件包括 CRD、自定义资源、API Server、etcd、controller-runtime/client-go、informers、workqueue、reconcile loop、RBAC、ServiceAccount、finalizer、ownerReference、status subresource、events、leader election、Deployment/StatefulSet、Secret、ConfigMap、PVC、Job、Service 和监控系统。Operator 的核心不是创建资源，而是持续把实际状态拉回期望状态。

## 176. Operator 配置错误时会导致哪些线上问题？

可以先这样答：Operator 配错会把控制器错误放大成持续性故障。因为 reconcile 是循环执行的，错误 spec、错误权限、错误模板或错误升级策略可能反复创建、删除、重启或修改资源。线上表现可能是 Pod 反复重建、PVC 绑定异常、Secret 被覆盖、证书轮转失败、数据库主从切换异常。

CRD spec 错误会让 Operator 做错事。比如存储容量、版本号、拓扑、备份保留策略、资源 request、Service 类型写错，Operator 会按这些错误声明执行。和一次性脚本不同，Operator 可能不断把人工临时修复改回 spec 里的错误状态，所以必须从自定义资源源头修。

RBAC 和 scope 配错也常见。权限不足时，Operator 无法创建或更新下游资源，status 里可能一直是 progressing；权限过大时，一旦 Operator 被攻破或代码有 bug，会影响更多 namespace。cluster-scoped Operator 要特别小心 namespaceSelector、watch 范围和租户隔离。

升级和删除是高风险路径。Operator 版本升级可能改变 CRD schema、默认值和 reconcile 行为；finalizer 处理不好会让资源删不掉；ownerReference 配错可能级联删除重要资源；备份恢复 Operator 如果策略错，可能覆盖真实数据。生产使用 Operator 前要看文档中的升级路径、备份方案、兼容矩阵和故障恢复流程。

## 177. Operator 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Operator 通过创建和维护下游 Kubernetes 对象间接影响负载均衡、服务发现、弹性伸缩和可观测性。它可能创建 Service、Headless Service、Ingress、Gateway、Endpoint、StatefulSet、HPA、ServiceMonitor、Secret 和 ConfigMap。用户改一个 CRD，背后可能牵动整条控制链路。

对负载均衡来说，Operator 可能负责暴露服务入口、创建读写分离 Service、维护主从角色 label、更新 Gateway 路由或生成证书。数据库 Operator 如果主节点切换后没有及时更新 Service selector，流量可能继续打到旧主；网关 Operator 如果配置下发失败，路由不会生效。

对服务发现来说，Operator 常维护稳定 DNS、Headless Service、Pod label、EndpointSlice 关联和外部注册。Stateful 应用尤其依赖稳定身份。Operator 改错 label 或 serviceName，客户端可能解析到错误实例，或者 StatefulSet 网络身份不稳定。

对弹性伸缩和可观测性来说，Operator 可以创建 HPA/KEDA ScaledObject，或者自己根据 CRD spec 调整副本、分片、存储和资源请求。Prometheus Operator 这类组件还会通过 CRD 管理监控规则和抓取目标。排障时要看自定义资源 status/conditions、Operator 日志、events、RBAC deny、reconcile duration、workqueue depth、生成的下游资源和真实数据面指标。只看 CRD apply 成功远远不够。


## 178. Helm 在云原生系统中解决什么问题？

可以先这样答：Helm 解决的是 Kubernetes 应用打包、参数化部署和发布版本管理问题。一个真实应用往往不止一个 Deployment，还会有 Service、Ingress 或 Gateway、ConfigMap、Secret、ServiceAccount、RBAC、HPA、PDB、CRD 等一组资源。手工维护这些 YAML 容易漏字段、漏依赖、环境差异也难管理。Helm 把这组资源打包成 Chart，再用 values 管理不同环境的差异。

Helm 的另一个价值是 release 管理。安装、升级、回滚都有 revision 记录，发布失败后可以查看历史版本并回滚。对平台团队来说，Helm Chart 可以沉淀一套标准部署模板；对业务团队来说，只需要改 values，而不是理解每个 Kubernetes 对象的全部细节。

它也解决复用问题。同一个 Chart 可以安装多次，生成不同 release；同一套模板可以按命名空间、镜像 tag、资源规格、Service 类型、Ingress 域名、采样率等参数生成不同清单。相比复制粘贴 YAML，Helm 更适合管理复杂应用的生命周期。

但 Helm 不是运行时控制器。它把 Chart 渲染并提交给 Kubernetes，之后真正让 Pod 调度、Service 转发、HPA 扩缩容、Prometheus 抓取的是 Kubernetes 和对应控制器。Helm 管“声明和发布”，不直接管业务进程是否健康。

## 179. Helm 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：Helm Chart 里包含模板、默认 values、Chart 元数据和可选依赖。执行 `helm install` 或 `helm upgrade` 时，Helm 客户端读取 values，把 Go template 渲染成 Kubernetes manifests，然后通过 kubeconfig 连接 API Server，把这些资源创建或更新到目标 namespace。

Helm 会为每次安装创建一个 release。Helm 3 之后 release 状态默认存储在 Kubernetes Secret 中，也可以配置为 ConfigMap 等存储方式。每次 upgrade 或 rollback 会产生新的 revision，所以可以用 `helm history`、`helm status`、`helm rollback` 追踪和恢复。

Helm 还支持 hooks、依赖、CRD 安装和 `--wait`、`--atomic` 等发布控制。hooks 可以在安装或升级前后执行 Job；CRD 通常需要先安装再创建对应 CR；`--wait` 会等待 Pod ready、PVC bound、Service/Ingress 等达到条件后再认为发布成功。要注意，等待条件覆盖的是 Kubernetes 状态，不等于业务 SLO 一定恢复。

涉及组件包括 Helm CLI、Chart repository 或 OCI registry、values 文件、Kubernetes API Server、Secret/ConfigMap release storage、Deployment、StatefulSet、DaemonSet、Service、Ingress/Gateway、ConfigMap、Secret、RBAC、CRD、Job hooks、admission webhook、scheduler、kubelet 和各类 Operator。Helm 本身不是集群内常驻控制器，但它提交的对象会触发这些控制器工作。

## 180. Helm 配置错误时会导致哪些线上问题？

可以先这样答：Helm 配错会把模板错误快速扩散成一批 Kubernetes 对象错误。常见表现包括发布失败、Pod 起不来、Service selector 匹配不到后端、Ingress 域名或 TLS 配错、HPA 指标错、PDB 太紧导致滚动更新卡住、CRD/CR 顺序错导致资源创建失败。

values 覆盖是高风险点。`--set` 的点号、逗号、数组语法很容易写错；多份 values 文件按顺序合并，后面的覆盖前面的；生产环境误用了测试 values，就可能把镜像 tag、资源规格、数据库地址、Service 类型、采样率或鉴权开关改坏。Chart 越复杂，values schema 和发布前渲染检查越重要。

模板本身也可能有问题。label、selector、namespace、fullname、serviceAccountName、imagePullSecrets、resources、probes 只要一个字段不一致，就会导致服务不可用或发布卡住。CRD 的升级更敏感，Helm 对 CRD 的管理有边界，不适合把 CRD schema 的破坏性变更当普通 Deployment 一样随意回滚。

还有一个容易被忽略的问题是 release 状态和实际集群状态漂移。有人手工改了 Helm 管理的对象，下一次 upgrade 被覆盖；hooks Job 失败但没有被注意；`--atomic` 回滚了部分对象但外部系统已经发生副作用。生产里要结合 `helm diff`、`helm template`、dry-run、chart lint、values schema 和 GitOps 审查来降低风险。

## 181. Helm 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Helm 通过生成 Kubernetes 对象间接影响负载均衡、服务发现、弹性伸缩和可观测性。它不转发请求，也不采集指标，但它创建的 Service、Ingress、Gateway、HPA、ServiceMonitor、PrometheusRule、labels 和 annotations 会决定这些链路是否能工作。

对负载均衡来说，Helm values 常控制 Service 类型、端口、selector、Ingress/Gateway 路由、TLS Secret、mTLS 注入、网关 annotations。selector 写错，Service 没有 endpoint；端口写错，流量打不通；Ingress class 或 GatewayClass 错，入口控制器根本不接管。

对服务发现来说，Helm 生成的 labels 和 names 很关键。很多 Chart 用 `app.kubernetes.io/name`、`instance`、`component` 这些标签连接 Service、Pod、ServiceMonitor 和 NetworkPolicy。一次重命名或 label 覆盖可能让服务发现、监控抓取和网络策略一起失效。

对弹性伸缩和可观测性来说，Helm 可能生成 HPA、VPA、PDB、resources、Prometheus scrape 配置和告警规则。values 里的 request 太低会误导 HPA 和调度；ServiceMonitor label 不匹配会让 Prometheus 抓不到目标；PrometheusRule 写错会造成告警噪声或告警缺失。排障时要看渲染后的 manifest，而不是只看 Chart 源模板。


## 182. Kustomize 在云原生系统中解决什么问题？

可以先这样答：Kustomize 解决的是 Kubernetes YAML 的环境差异管理问题。它不使用模板语法，而是把一组原始 YAML 作为 base，再通过 overlay、patch、namePrefix、namespace、commonLabels、images、configMapGenerator 等机制生成不同环境的最终清单。这样开发、测试、生产可以共享基础配置，又保留差异。

它适合解决“同一套资源，不同环境稍有不同”的问题。比如生产环境副本数更多、镜像 tag 不同、resources 更高、Ingress 域名不同、ConfigMap 内容不同、namespace 不同。Kustomize 避免了把 YAML 复制成三份再手工维护。

和 Helm 相比，Kustomize 更偏声明式叠加，不把 YAML 变成模板语言。它的优点是最终对象仍然是普通 Kubernetes YAML，学习成本低，和 `kubectl apply -k`、GitOps 工具结合直接。缺点是复杂条件逻辑、循环和动态计算能力弱，不适合把过多业务逻辑塞进配置生成阶段。

面试里可以这样定位：Helm 更像应用包管理和发布工具，Kustomize 更像 Kubernetes 原生 YAML 定制工具。二者都生成 manifest，但抽象方式不同。

## 183. Kustomize 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：Kustomize 读取 `kustomization.yaml`，加载 resources 指向的 base 或 YAML 文件，然后按配置执行生成器和转换器。生成器可以创建 ConfigMap、Secret；转换器可以加 namespace、前后缀、labels、annotations、镜像替换、replicas 修改；patch 可以对指定对象做战略合并或 JSON patch。最后输出完整 Kubernetes manifests。

执行方式有两类：本地用 `kustomize build` 输出清单，再交给 `kubectl apply`；或者直接用 `kubectl apply -k`。Kustomize 本身不常驻集群，也不负责 reconcile。真正创建对象、运行 Pod、更新 Service 的仍然是 API Server 和 Kubernetes 控制器。

ConfigMapGenerator 和 SecretGenerator 有一个重要特性：默认会根据内容生成带 hash 后缀的名字。这样配置变更会产生新对象名，Deployment 中引用也会更新，从而触发滚动更新。这个机制很有用，但也容易让人困惑，因为名字不是你在 YAML 里写的静态名字。

涉及组件包括 kustomization.yaml、base、overlay、patches、generators、transformers、kubectl、API Server、Deployment、Service、ConfigMap、Secret、Ingress/Gateway、HPA、RBAC、admission webhook 和 GitOps 控制器。Kustomize 的输出进入集群后，就和手写 YAML 没区别。

## 184. Kustomize 配置错误时会导致哪些线上问题？

可以先这样答：Kustomize 配错会导致最终 manifest 和预期不一致。线上可能看到 Service 没有 endpoint、Deployment selector 不匹配、ConfigMap/Secret 名字变化导致引用失败、patch 没打上、生产环境用了测试镜像、namespace 错误、资源被重复创建或被错误覆盖。

patch target 是常见坑。name、kind、apiVersion、namespace 匹配不准，patch 可能根本没生效；patch 太宽，又可能改到不该改的对象。战略合并 patch 对不同资源类型的合并语义不完全一样，列表字段尤其要小心。最终要看 build 出来的结果，而不是只看 overlay 文件。

labels 和 namePrefix/nameSuffix 更危险。给所有对象加 commonLabels 时，如果误改了 Deployment selector 或 Service selector，Kubernetes 可能拒绝更新，也可能让 Service 匹配不到 Pod。namePrefix 改了资源名，但某些外部引用、RBAC subject、Ingress host、监控抓取选择器没有同步，线上就会出现局部失效。

ConfigMapGenerator/SecretGenerator 的 hash 名字也要理解。配置变更触发滚动更新是好事，但如果某些组件需要固定名字，或者清理策略不当，可能留下大量旧对象。GitOps 场景下还要注意 build 版本一致，否则不同流水线生成的 manifest 有细微差异，造成漂移。

## 185. Kustomize 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Kustomize 通过最终生成的 YAML 影响这些链路。它不直接参与流量转发，但它可以修改 Service、Ingress/Gateway、Deployment labels、HPA、ConfigMap、ServiceMonitor、NetworkPolicy 等对象。最终 manifest 一旦错，数据面和观测面都会跟着错。

对负载均衡来说，overlay 可能改 Service 端口、Service 类型、Ingress class、Gateway listener、annotations 和 selector。一个 selector patch 错误，就会让 Service endpoint 为空；一个端口 patch 错误，就会让流量打到错误 containerPort。

对服务发现来说，Kustomize 经常统一 labels、namespace 和 names。Service、EndpointSlice、ServiceMonitor、NetworkPolicy 都依赖标签和名字关系。看似只是给资源加统一标签，如果碰到 selector 字段，就可能改变服务发现结果。

对弹性伸缩和可观测性来说，overlay 往往改 replicas、resources、HPA 阈值、PrometheusRule、ServiceMonitor selector、日志级别和 trace 采样率。排障时最重要的证据是 `kustomize build` 或 `kubectl kustomize` 的输出，再和集群中的 live object 对比。不要只审查 base 或 overlay 的某一层。


## 186. Prometheus Operator 在云原生系统中解决什么问题？

可以先这样答：Prometheus Operator 解决的是 Kubernetes 上 Prometheus 监控系统的声明式管理问题。没有 Operator 时，Prometheus 的部署、服务发现配置、抓取目标、告警规则、Alertmanager 配置、持久化和升级都要手工维护。Operator 把这些运维动作变成 CRD 和控制器逻辑。

它最典型的价值是让应用团队用 ServiceMonitor、PodMonitor、PrometheusRule 等对象声明“我要被抓取”和“我要告警”，而不是直接改 Prometheus 的主配置文件。平台团队管理 Prometheus 实例，业务团队提交监控声明，职责边界更清楚。

Prometheus Operator 也解决了监控栈自身生命周期。它可以根据 Prometheus CR 创建 StatefulSet、Secret、ConfigMap、Service、RBAC 等对象，管理副本、存储、保留时间、规则文件和 Alertmanager 关联。监控系统也变成 Kubernetes 控制器管理的对象。

面试里要说清楚：Prometheus Operator 不替代 Prometheus 采集和查询引擎。真正抓指标、存 TSDB、执行 PromQL 和发送告警的是 Prometheus/Alertmanager；Operator 负责把 Kubernetes 声明翻译成这些组件需要的配置和工作负载。

## 187. Prometheus Operator 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：Prometheus Operator 安装后会注册一组 CRD，比如 Prometheus、Alertmanager、ServiceMonitor、PodMonitor、Probe、PrometheusRule、ThanosRuler。用户创建这些自定义资源后，Operator watch 它们和相关 Service、Pod、Endpoints/EndpointSlice，根据 selector 生成 Prometheus 配置，并维护 Prometheus StatefulSet。

Prometheus CR 定义 Prometheus 实例本身，例如副本数、版本、存储、保留时间、serviceMonitorSelector、podMonitorSelector、ruleSelector、资源限制。ServiceMonitor 和 PodMonitor 通过 label selector 选择要抓取的 Service 或 Pod，再定义端口、路径、scheme、interval、relabeling 等抓取细节。

Operator 会把生成的 Prometheus 配置写入 Secret，并通过 config reloader 让 Prometheus 加载新配置。PrometheusRule 会被转换成规则文件，Alertmanager CR 则管理 Alertmanager 集群。整个过程仍依赖 Kubernetes API、RBAC、StatefulSet、Secret、ConfigMap、Service 和持久卷。

涉及组件包括 Prometheus Operator Deployment、CRD、API Server、etcd、RBAC、ServiceAccount、Prometheus StatefulSet、Alertmanager、ServiceMonitor、PodMonitor、PrometheusRule、Secret、ConfigMap、PVC、Service、EndpointSlice、admission webhook、config reloader 和 Prometheus 查询/告警组件。

## 188. Prometheus Operator 配置错误时会导致哪些线上问题？

可以先这样答：Prometheus Operator 配错最常见的结果是“以为有监控，实际没抓到”。ServiceMonitor selector 不匹配、namespaceSelector 漏配、端口名写错、scheme/path 写错、RBAC 不足、Prometheus CR 的 selector 为空或过窄，都会让目标没有进入 Prometheus targets。

另一类问题是抓取太多。selector 过宽、interval 太短、relabeling 错误、高基数 label 没控制，会让 Prometheus 样本量暴涨，TSDB 压力、内存和磁盘使用上升，查询变慢，甚至 Prometheus OOMKilled。监控系统本身也需要资源治理。

告警规则也容易出事故。PrometheusRule 语法错误会导致规则加载失败；表达式太重会拖慢规则计算；for 时间和阈值不合理会造成告警风暴或漏告警。Alertmanager 路由、静默和抑制规则错了，告警可能发不到人，或者重复轰炸。

升级和 CRD 兼容性也要小心。Operator、Prometheus 版本、CRD schema、admission webhook、kube-state-metrics 指标名之间可能存在变化。生产里要监控 Operator reconcile error、Prometheus config reload error、target down、sample ingestion、WAL/TSDB、规则评估耗时和告警发送状态。

## 189. Prometheus Operator 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Prometheus Operator 对可观测性是核心组件，对负载均衡、服务发现和弹性伸缩则是间接影响。它通过 ServiceMonitor/PodMonitor 依赖 Kubernetes 服务发现，把 Service、Pod、EndpointSlice 转成 Prometheus 抓取目标；抓到的指标又可能被 HPA、KEDA、告警和容量决策使用。

对服务发现来说，Prometheus Operator 本质上把“谁应该被监控”声明成 Kubernetes 对象。ServiceMonitor selector、namespaceSelector、端口名和标签决定目标是否出现。应用明明暴露了 `/metrics`，但标签不匹配，Prometheus 就看不到它。

对负载均衡来说，Prometheus 通常不在请求路径上，但它会观察 Service、Ingress、Envoy、Istio、Linkerd、Cilium 等组件的指标。缺少这些指标，负载均衡故障就只能靠日志和用户反馈排查。某些黑盒 Probe 还会主动探测入口服务，发现路由或 TLS 问题。

对弹性伸缩来说，Prometheus 指标常通过 adapter 变成 custom/external metrics，供 HPA 或 KEDA 使用。Prometheus Operator 配错导致指标缺失，HPA 就可能无法扩容；高基数或查询过慢又会拖慢指标链路。对可观测性来说，要同时看 targets、rules、alerts、scrape errors、sample rate、query latency、Operator status 和业务 SLO。


## 190. Envoy sidecar 在云原生系统中解决什么问题？

可以先这样答：Envoy sidecar 解决的是把服务间通信能力从业务进程里抽出来的问题。业务应用只处理业务逻辑，Envoy 作为同 Pod 的代理处理连接、负载均衡、重试、超时、熔断、限流、mTLS、指标、访问日志和流量镜像。这样不同语言的服务可以共享同一套网络治理能力。

Sidecar 模式的关键是“就近代理”。每个业务 Pod 旁边都有一个 Envoy，进出流量先经过它。服务治理逻辑不需要每个业务 SDK 都实现一遍，也不需要每次策略变化都重新发版业务代码。控制面下发配置，数据面 Envoy 执行。

它特别适合微服务东西向流量治理。比如服务 A 调服务 B，Envoy 可以根据 endpoints 做负载均衡，根据响应状态做重试，根据 mTLS 证书做身份校验，根据 tracing header 传播链路信息。应用可能只看到 localhost 或原始目标地址，复杂网络逻辑由代理完成。

代价也很明确：每个 Pod 多一个容器，多一段转发路径，多一套配置和证书生命周期。低延迟、高吞吐或资源紧张场景下，Envoy sidecar 的 CPU、内存、连接池和配置规模都要认真评估。

## 191. Envoy sidecar 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：Envoy sidecar 作为 Pod 内的第二个容器运行，和业务容器共享网络命名空间。流量通常通过 iptables、nftables、CNI 插件或透明代理机制重定向到 Envoy 的 inbound/outbound listener。Envoy 根据控制面下发的配置选择 cluster、endpoint、route 和 filter chain。

Envoy 的动态配置通常通过 xDS API 获得。控制面生成 Listener、Route、Cluster、Endpoint、Secret 等配置，Envoy 通过 LDS、RDS、CDS、EDS、SDS 等接口增量更新。服务网格里，Istio、Contour、Consul、Envoy Gateway 等都可能作为 Envoy 控制面的一部分。

在 Kubernetes 中，Envoy sidecar 的注入常由 Mutating Admission Webhook 完成，也可以手工写进 Pod spec。sidecar 需要资源 requests/limits、readiness、liveness、启动顺序、preStop、证书挂载、ConfigMap/Secret、ServiceAccount 和 RBAC 配合。某些实现还会用 initContainer 设置重定向规则。

涉及组件包括 Pod、sidecar container、initContainer、CNI/iptables、Service、EndpointSlice、DNS、xDS control plane、Secret/SDS、mutating webhook、Envoy admin interface、Prometheus metrics、access log、tracing backend、readinessProbe 和应用容器。Envoy 在数据面，控制面负责把 Kubernetes 状态和策略翻译成 Envoy 配置。

## 192. Envoy sidecar 配置错误时会导致哪些线上问题？

可以先这样答：Envoy sidecar 配错会造成流量被拦截后出不去、进不来、路由到错误后端、TLS 握手失败、重试风暴、连接池耗尽、延迟上升或监控指标失真。因为它在请求路径上，错误通常比普通配置错误更直接。

重定向规则错误很常见。iptables 或 CNI 规则把不该代理的流量也劫持了，例如健康检查、metadata server、数据库直连、DNS、Prometheus 抓取；或者遗漏了需要代理的端口，导致一部分流量绕过策略。排查时要看 Pod 内实际监听端口、iptables 规则、Envoy listener 和应用端口。

xDS 配置错误会造成更复杂的问题。cluster 没有 endpoint，Envoy 返回 503；route 匹配顺序错，流量进错版本；timeout 太短导致大量超时；retry 配得太猛放大下游故障；circuit breaker 太紧导致请求被本地拒绝；SDS 证书过期导致 mTLS 失败。

资源配置也会影响稳定性。Envoy CPU limit 太低会增加代理延迟，内存不足会 OOMKilled；连接池、buffer、access log、trace 采样率配置不当会放大开销。sidecar 的生命周期还要和业务容器协调，preStop/drain 不当会在发布或缩容时断连接。

## 193. Envoy sidecar 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Envoy sidecar 直接参与负载均衡和可观测性，间接影响服务发现和弹性伸缩。它可以在客户端侧按 endpoint 做负载均衡、重试、熔断和连接池管理，也可以暴露请求级指标、访问日志和 tracing 信息。

对服务发现来说，Envoy 不一定直接查 Kubernetes API。通常是控制面 watch Service、EndpointSlice、Pod、ServiceEntry 等对象，再通过 EDS 把 endpoint 下发给 Envoy。控制面滞后或 xDS 推送失败时，Envoy 看到的后端集合就会和 Kubernetes 实际状态不一致。

对弹性伸缩来说，sidecar 会改变单 Pod 的资源需求和启动时间。每个 Pod 多一个 Envoy，requests 要算进去；新 Pod ready 前，Envoy 要拿到配置和证书；缩容时，Envoy 要 drain 连接。HPA 如果只看应用容器指标，可能低估 sidecar 消耗；如果看 Pod 总 CPU，又可能被代理开销影响。

对可观测性来说，Envoy 是很强的数据源。它能提供上游/下游请求数、延迟、重试、熔断、连接池、TLS、xDS、健康检查等指标。但也要小心标签基数和采样率。面试里可以这样收尾：Envoy sidecar 把流量治理前移到每个 Pod，这让能力一致，但也让每个 Pod 的网络路径更复杂。


## 194. Istio 在云原生系统中解决什么问题？

可以先这样答：Istio 解决的是服务网格层面的流量治理、安全和可观测性问题。微服务数量多以后，服务之间调用会遇到路由、灰度、重试、超时、熔断、mTLS、身份认证、授权、指标、日志、trace 等问题。Istio 把这些能力从业务代码中抽出来，交给网格控制面和数据面处理。

它的价值在于统一治理。不同语言、不同框架的服务，只要进入网格，就可以用 VirtualService、DestinationRule、PeerAuthentication、AuthorizationPolicy、Gateway 等资源声明策略。业务代码不需要每个语言 SDK 都实现同样的重试、mTLS 和指标逻辑。

Istio 也解决东西向和南北向流量的一致性问题。东西向服务调用通过 sidecar 或 ambient 数据面治理；南北向入口可以通过 Istio Gateway 处理 TLS、路由和策略。平台团队可以把安全、灰度和可观测性做成集群级能力。

边界也要说明：Istio 不是业务注册中心，也不是替代 Kubernetes Service。它通常建立在 Kubernetes 服务发现、Pod、Service、EndpointSlice 和证书身份之上，再提供更细粒度的 L7 策略。

## 195. Istio 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：Istio 由控制面和数据面组成。控制面核心是 istiod，它 watch Kubernetes Service、EndpointSlice、Pod、Istio CRD 和 Secret 等对象，生成 Envoy 需要的 xDS 配置，并负责证书签发和身份相关逻辑。数据面通常是每个 Pod 里的 Envoy sidecar，也可以是 ambient 模式下的节点或命名空间级代理组件。

在 sidecar 模式下，Pod 创建时 mutating webhook 注入 Envoy sidecar 和相关 initContainer 或 CNI 配置。流量通过重定向进入 Envoy，Envoy 根据 istiod 下发的 Listener、Route、Cluster、Endpoint 和 Secret 配置执行路由、mTLS、负载均衡和策略。

Istio 的策略通过 CRD 声明。VirtualService 定义路由、流量分配、重试、超时；DestinationRule 定义子集、负载均衡、连接池、TLS；Gateway 定义入口监听；PeerAuthentication 控制 mTLS；AuthorizationPolicy 控制访问授权；ServiceEntry 可以把外部服务纳入网格模型。

涉及组件包括 istiod、Envoy proxy、mutating admission webhook、Istio CRD、Kubernetes Service、EndpointSlice、Pod labels、ServiceAccount、Secret/SDS、Gateway、Ingress/Gateway API 集成、CNI、Prometheus、tracing backend、RBAC 和 Kubernetes API Server。Istio 控制面把 Kubernetes 状态和网格策略翻译给 Envoy 数据面。

## 196. Istio 配置错误时会导致哪些线上问题？

可以先这样答：Istio 配错会直接影响请求路径。常见问题包括 503、404、路由不到后端、灰度比例错误、mTLS 握手失败、授权误拒、重试风暴、超时过短、连接池耗尽、sidecar 注入失败、新 Pod 无法 ready、入口 Gateway TLS 证书错误。

路由配置错误很常见。VirtualService host、gateway、subset、match 顺序、权重写错，流量可能进错版本或根本匹配不到；DestinationRule subset label 和 Deployment label 不一致，Envoy 会发现没有可用 endpoint；ServiceEntry 配错会让外部依赖访问失败。

安全策略错误也很敏感。PeerAuthentication 开启 STRICT mTLS，但某些工作负载没有 sidecar 或证书，就会调用失败；AuthorizationPolicy 过严会拒绝正常流量，过宽又失去隔离。ServiceAccount 身份、namespace、principal、path、method 匹配条件都要逐项核对。

控制面和 sidecar 生命周期也会出问题。istiod 不可用时，新配置推不出去，新 Pod 可能拿不到配置或证书；sidecar 资源太低会增加延迟；注入 webhook 故障会阻断 Pod 创建或造成无代理 Pod 混入。生产排查要看 Envoy config dump、istioctl proxy-status、xDS 同步状态、mTLS 证书、策略命中和应用日志。

## 197. Istio 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Istio 对负载均衡和可观测性影响很直接，对服务发现和弹性伸缩也有强耦合。它把 Kubernetes Service 后端转换成 Envoy cluster 和 endpoint，再在请求级执行路由、负载均衡、熔断、重试、镜像和流量分配。

对负载均衡来说，Istio 可以做 subset 路由、按 header/cookie/path 分流、权重灰度、故障注入、连接池和 outlier detection。这个能力比普通 Service 的四层负载均衡更细，但配置错了也更容易产生局部流量黑洞。

对服务发现来说，Istio 依赖 Kubernetes Service、EndpointSlice、Pod labels 和 ServiceEntry。服务发现结果不是应用直接看到，而是通过 istiod 下发给 Envoy。Pod ready、EndpointSlice 更新、xDS 推送、Envoy 接收配置之间有传播延迟，发布和扩容时要考虑这段时间。

对弹性伸缩和可观测性来说，每个 sidecar 都消耗资源，HPA/CA 的 request 要算上；新 Pod 要等 sidecar 和证书就绪才真正接流量；Prometheus 可以从 Envoy 抓到请求量、延迟、错误率、mTLS、xDS 等指标。Istio 提供了丰富可观测性，但也会引入高基数标签和更复杂的排障路径。


## 198. Linkerd 在云原生系统中解决什么问题？

可以先这样答：Linkerd 解决的是服务网格里的可靠通信、安全和可观测性问题，但它强调简单、轻量和默认安全。它通过给 Pod 注入 linkerd-proxy sidecar，让服务间调用获得 mTLS、请求级指标、重试、超时、流量拆分等能力，而不要求业务代码改造。

Linkerd 的定位通常比 Istio 更克制。它不追求把所有 L7 网关和复杂策略都放进同一个体系，而是优先解决服务间通信中最常见的问题：调用是否成功、延迟是多少、哪个服务依赖哪个服务、mTLS 是否开启、发布时流量如何切分。

它适合希望快速获得网格可观测性和 mTLS 的团队。注入后，应用之间的 TCP/HTTP/gRPC 流量经过代理，Linkerd 控制面提供身份、策略、指标和扩展能力。对已有 Kubernetes Service 模型的依赖比较强。

边界也要说清楚：Linkerd 不是完整应用平台，也不替代 Kubernetes Service、Ingress、Gateway 或 Prometheus。它是服务间通信层，真正的 Pod 调度、Service 发现、HPA 扩缩容仍由 Kubernetes 和相关控制器完成。

## 199. Linkerd 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：Linkerd 由控制面和数据面组成。数据面是每个被注入 Pod 里的 linkerd-proxy sidecar，通常通过 iptables 或 CNI 重定向流量。控制面包括 identity、destination、proxy-injector、policy、tap 等组件，用来签发身份、发现服务、注入代理、下发策略和提供诊断能力。

Pod 注入可以通过 annotation 和 mutating admission webhook 完成，也可以在 manifest 应用前用 CLI 注入。注入后，业务容器流量进入 linkerd-proxy；proxy 向控制面获取目标服务信息和身份信息，然后对服务间连接执行 mTLS、负载均衡、指标记录和策略检查。

Linkerd 依赖 Kubernetes 的 Service 和 EndpointSlice/Endpoints 做服务发现。destination 服务会根据 Kubernetes 资源返回后端地址和元数据；identity 服务给代理签发短生命周期证书；policy 组件处理授权策略；Prometheus 负责抓取代理和控制面指标。

涉及组件包括 linkerd control plane、linkerd-proxy、mutating webhook、ServiceAccount、Secret、Trust Anchor/issuer 证书、Kubernetes Service、EndpointSlice、Pod labels、Namespace annotations、NetworkPolicy、Prometheus、Grafana、Tap、TrafficSplit 或 Gateway API/SMI 相关扩展。它的核心路径仍是：控制面发现和授权，sidecar 数据面执行。

## 200. Linkerd 配置错误时会导致哪些线上问题？

可以先这样答：Linkerd 配错会导致代理注入失败、mTLS 握手失败、服务发现错误、授权误拒、指标缺失、流量拆分不符合预期，严重时会让 Pod 创建失败或服务间调用中断。因为它在请求路径上，证书和策略问题会很快变成业务错误。

证书和身份是高风险点。trust anchor、issuer 证书过期或不一致，会导致代理之间不能建立 mTLS；ServiceAccount 身份和策略不匹配，会出现授权失败；控制面 identity 不可用时，新代理拿不到证书。生产里要监控证书有效期和 identity 服务状态。

注入和端口配置也常出问题。某些命名空间没开启注入，服务有的 Pod 在网格内、有的在网格外；跳过端口配置错误，导致探针、数据库、管理端口或外部流量被错误代理；proxy 资源 request 太低，会造成延迟和连接失败。升级时还要保证控制面和 proxy 版本兼容。

流量策略配置错会造成灰度事故。TrafficSplit 权重、service 名称、端口和 selector 错误，可能让流量继续打到旧版本或直接没有后端。Linkerd 的工具能给出很多诊断信息，但前提是 Prometheus 和控制面本身工作正常。

## 201. Linkerd 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Linkerd sidecar 会直接处理服务间请求，所以它会影响负载均衡和可观测性。代理可以基于 Kubernetes 服务发现结果做请求级负载均衡，记录成功率、延迟、请求量、TLS 状态，并把这些指标交给 Prometheus。

对服务发现来说，Linkerd 依赖 Kubernetes Service 和 EndpointSlice，再由 destination 控制面把目标地址告诉代理。Pod ready、EndpointSlice 更新、destination 响应和代理连接之间有传播链路。发布时如果 labels 或 Service selector 错，Linkerd 也只能看到错误的后端集合。

对弹性伸缩来说，Linkerd 增加了 sidecar 资源消耗和启动依赖。HPA 如果只看应用容器，可能漏掉 proxy CPU；如果看 Pod 总 CPU，又可能把代理开销算进业务负载。新 Pod 进入服务池前，proxy、identity 和 readiness 都要就绪。缩容时，连接 drain 和代理生命周期也要考虑。

对可观测性来说，Linkerd 的强项是自动产生服务间黄金指标：成功率、RPS、延迟分位、TLS 状态和拓扑。它能帮助快速定位哪个调用边出了问题。不过指标仍然依赖 Prometheus 抓取、标签设计和控制面健康，不能把“网格有指标”误解成“所有业务语义都可观测”。


## 202. Cilium eBPF 在云原生系统中解决什么问题？

可以先这样答：Cilium eBPF 解决的是传统容器网络数据面在性能、可观测性和策略表达上的限制。它利用 Linux eBPF 在内核网络路径中执行程序，实现 Pod 网络、Service 负载均衡、NetworkPolicy、L7 可观测性、透明加密、kube-proxy replacement 等能力。

传统 iptables 规则在大规模 Service 和 endpoint 场景下规则多、排查难、更新成本高。Cilium 用 eBPF map 保存服务、后端、身份和策略等状态，让数据面查表执行，减少长规则链匹配。对高并发、服务数量多的集群，这一点很关键。

Cilium 的另一个核心是身份。它不只按 IP 做策略，还会给 workload 分配 security identity，策略可以按 Kubernetes labels 表达。Pod IP 会变，但身份可以随标签和工作负载语义变化，这让网络策略更贴近 Kubernetes 模型。

要注意，eBPF 不是一个单独产品能力，而是 Linux 内核可编程机制。Cilium 把 eBPF 用在容器网络、安全和可观测性上。最终效果依赖内核版本、CNI 配置、集群网络模式和 Cilium agent 的实现。

## 203. Cilium eBPF 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：Cilium agent 运行在每个节点上，作为 CNI 插件参与 Pod 网络创建，并把 eBPF 程序加载到 Linux 内核的网络挂载点，比如 TC、XDP、socket hooks 等。eBPF 程序通过 BPF maps 查询 Service、endpoint、policy、connection tracking 和身份信息，在内核路径中决定转发、丢弃、NAT 或负载均衡。

Cilium watch Kubernetes API，读取 Pod、Service、EndpointSlice、Node、Namespace、NetworkPolicy、CiliumNetworkPolicy 等资源，把控制面状态转换成 eBPF map 和策略。每个节点上的 agent 维护本节点数据面，operator 处理集群级任务，Hubble 可以消费流量事件并提供可观测性。

在 kube-proxy replacement 模式下，Cilium 可以用 eBPF 实现 Service ClusterIP、NodePort、LoadBalancer、externalTrafficPolicy 等数据面能力，替代 kube-proxy 的 iptables/IPVS 路径。是否启用、支持范围和行为要看 Cilium 配置和内核能力。

涉及组件包括 Cilium CNI、cilium-agent、cilium-operator、Linux eBPF、BPF maps、TC/XDP/socket hooks、Kubernetes API Server、Pod、Service、EndpointSlice、Node、NetworkPolicy、CiliumNetworkPolicy、kube-proxy replacement、Hubble、DNS proxy 和可选 Envoy/L7 proxy。Kubernetes 提供对象语义，Cilium 把这些语义落到内核数据面。

## 204. Cilium eBPF 配置错误时会导致哪些线上问题？

可以先这样答：Cilium eBPF 配错会直接影响 Pod 联通、Service 转发、NetworkPolicy、DNS、NodePort、负载均衡和观测。常见表现是 Pod 之间不通、ClusterIP 访问失败、跨节点流量丢包、DNS 超时、策略误拒、Hubble 看不到流量、节点升级后 eBPF 程序加载失败。

内核和能力不匹配是基础风险。eBPF 特性依赖内核版本和配置，某些云主机镜像、内核安全策略或 cgroup/bpf 挂载不满足要求，会导致 Cilium 启动或加载程序失败。kube-proxy replacement 开启后，如果 Service 行为和底层网络不兼容，影响会覆盖整个集群服务访问。

策略配置也容易产生误判。CiliumNetworkPolicy、Kubernetes NetworkPolicy、DNS policy、L7 policy 混在一起时，如果 selector、namespace、identity 或 DNS 规则写错，流量会被内核路径直接拒绝。应用看到的是连接超时或 reset，不一定能马上意识到是策略问题。

路由和封装配置也要谨慎。VXLAN/Geneve、native routing、BGP、MTU、masquerade、direct routing、externalTrafficPolicy 任何一处和底层网络不匹配，都可能导致跨节点丢包或路径不对称。排查时要看 Cilium status、BPF map、policy verdict、Hubble flow、节点路由和内核日志。

## 205. Cilium eBPF 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：Cilium eBPF 对负载均衡和可观测性影响很直接。它可以在内核数据面实现 Service 负载均衡、NodePort、连接跟踪、DSR、Maglev 等能力；同时通过 Hubble 暴露 L3/L4/L7 流量、策略判定、DNS 和服务调用关系。

对服务发现来说，Cilium 仍依赖 Kubernetes Service、EndpointSlice 和 DNS 作为控制面语义。不同的是，endpoint 变化会被 Cilium agent 转换进 BPF maps，数据面按 map 转发。EndpointSlice 更新、agent 同步、BPF map 更新之间存在传播链路，发布和扩缩容时要观察同步延迟。

对弹性伸缩来说，大规模 Pod 和 Service 变化会增加 agent 同步、BPF map 更新和策略计算压力。HPA 扩容后，新 endpoint 是否快速进入 Service LB，取决于 Kubernetes 控制面、Cilium agent 和内核 map 更新。Cluster Autoscaler 加节点后，新节点上的 Cilium 必须就绪，否则 Pod 网络不可用。

对可观测性来说，Cilium 的优势是接近数据面。Hubble 能看到哪些流量被允许、拒绝、转发到哪个服务，DNS 解析和 L7 请求也可以进入观测链路。缺点是排障门槛更高，要能读懂 identity、BPF map、policy trace、agent log 和内核路径。


## 206. containerd 在云原生系统中解决什么问题？

可以先这样答：containerd 解决的是节点上容器生命周期管理问题。Kubernetes 调度出 Pod 后，真正要在节点上拉镜像、解包镜像、创建容器、启动进程、管理快照、处理日志和清理资源，需要一个容器运行时。containerd 就是常用的高层容器运行时。

它位于 kubelet 和更底层 OCI runtime 之间。kubelet 通过 CRI 调 containerd；containerd 负责镜像、内容存储、snapshotter、容器和任务管理；真正执行容器进程隔离的通常是 runc 这类 OCI runtime。这样 Kubernetes 不需要直接处理每种底层运行时细节。

containerd 也解决了镜像和文件系统管理问题。镜像从 registry 拉下来后会进入 content store，解压成层，再通过 snapshotter 组织成容器 rootfs。overlayfs、stargz、nydus 等 snapshotter 会影响镜像启动速度、磁盘占用和读写性能。

面试里可以这样定位：containerd 是节点运行时核心组件，不是 CNI、不是 kube-proxy、也不是调度器。它不决定 Pod 放在哪个节点，但 Pod 一旦放到这个节点，能不能启动、镜像能不能拉、容器能不能运行，就和它强相关。

## 207. containerd 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：kubelet 通过 CRI gRPC 调用 containerd 的 CRI plugin，请求创建 Pod sandbox、拉取镜像、创建容器、启动容器、停止容器、查询状态和收集日志。containerd 接到请求后，管理镜像内容、快照、容器元数据，并调用 OCI runtime 创建实际进程。

Pod sandbox 通常对应一个共享网络命名空间的基础容器。CNI 插件会为这个 sandbox 配置网络，业务容器加入同一个 Pod 网络命名空间。containerd 同时管理容器 stdout/stderr 日志文件，供 kubelet 和日志采集器读取。

containerd 内部有 content store、metadata store、snapshotter、runtime shim、tasks 等概念。shim 让容器进程和 containerd 主进程解耦，即使 containerd 重启，已有容器也可以继续运行。snapshotter 决定镜像层如何挂载成容器文件系统。

涉及组件包括 kubelet、CRI plugin、containerd daemon、containerd-shim、runc 或其他 OCI runtime、CNI、image registry、snapshotter、overlayfs、Pod sandbox、log files、cgroups、namespaces、kubelet image GC 和 node pressure eviction。Kubernetes 通过 CRI 抽象和 containerd 交互，而不是直接调用 runc。

## 208. containerd 配置错误时会导致哪些线上问题？

可以先这样答：containerd 配错会导致节点级 Pod 启动失败或运行异常。常见表现是 ImagePullBackOff、CreateContainerError、RunContainerError、Pod sandbox 创建失败、日志缺失、镜像 GC 异常、磁盘占满、容器停止不干净或 kubelet 报 runtime not ready。

镜像和 registry 配置最常见。私有 registry 证书、镜像加速、认证、HTTP/HTTPS、镜像平台架构配置错，会导致拉镜像失败。生产发布时新 Pod 都要拉镜像，runtime 配置问题会直接让扩容和滚动更新卡住。

snapshotter 和存储配置也很敏感。overlayfs 不可用、root 路径磁盘太小、镜像层清理失败、inode 耗尽、content store 损坏，都会造成容器启动慢或失败。节点磁盘压力还会触发 kubelet eviction，影响同节点多个 Pod。

运行时和 cgroup 配置不一致也会出问题。systemd cgroup driver、runc 路径、sandbox image、pause 镜像、日志轮转、ulimit、seccomp、AppArmor、SELinux 配置错，都可能表现成容器起不来或权限异常。排查要看 kubelet 日志、containerd 日志、`crictl` 状态、镜像存储和节点事件。

## 209. containerd 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：containerd 不参与负载均衡或服务发现的控制面，但它决定 Pod 能不能在节点上启动并保持运行。Service 只有在 Pod ready 后才会把它作为可用后端；如果 containerd 拉镜像慢、创建容器失败或节点 runtime not ready，负载均衡后端就不会增加。

对弹性伸缩来说，containerd 是 HPA 和 Cluster Autoscaler 之后的落地环节。HPA 增加副本，CA 增加节点，最后仍要靠 containerd 拉镜像、创建 sandbox、启动容器。如果镜像很大、registry 慢、snapshotter 性能差，端到端扩容时间会变长。

对服务发现来说，containerd 的影响是间接的。容器启动失败，Pod 不会 ready，EndpointSlice 不会加入 ready endpoint；容器反复重启，EndpointSlice 后端会抖动。Headless Service 和 StatefulSet 对这种抖动更敏感。

对可观测性来说，要看 kubelet runtime 状态、containerd task 状态、image pull latency、sandbox create latency、container start latency、镜像 GC、磁盘使用、日志文件、runtime errors 和节点事件。只看 Deployment desired/current 不够，节点运行时是 Pod 生命周期的最后一公里。


## 210. CRI 在云原生系统中解决什么问题？

可以先这样答：CRI，Container Runtime Interface，解决的是 kubelet 和容器运行时之间的标准接口问题。Kubernetes 不希望 kubelet 直接绑定 Docker、containerd、CRI-O 或其他运行时的内部实现，所以定义了 CRI，让 kubelet 通过统一 gRPC API 管理 Pod sandbox、容器、镜像和状态。

CRI 的价值是解耦。只要某个运行时实现了 CRI，kubelet 就可以用同一套调用方式拉镜像、创建 sandbox、启动容器、停止容器、查询状态、执行 exec、读取日志。Kubernetes 运行时生态才能从早期 Docker 绑定走向更清晰的接口模型。

它还让问题边界更清楚。调度器决定 Pod 去哪个节点，kubelet 负责节点上 Pod 生命周期，CRI runtime 负责容器运行细节，OCI runtime 负责更底层的进程隔离。面试里把 CRI 讲成“接口层”比讲成“一个组件”更准确。

CRI 不处理网络策略、Service 负载均衡或业务健康。它只负责 kubelet 到 runtime 的容器操作接口。Pod 网络通常还要依赖 CNI，Service 转发依赖 kube-proxy、Cilium 或其他数据面。

## 211. CRI 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：CRI 是 kubelet 调用容器运行时的 gRPC 接口，主要分为 RuntimeService 和 ImageService。RuntimeService 管 Pod sandbox 和容器生命周期，ImageService 管镜像拉取、列举和删除。containerd 的 CRI plugin、CRI-O 等实现会监听 CRI endpoint，供 kubelet 连接。

创建 Pod 时，kubelet 先通过 CRI 创建 Pod sandbox。sandbox 建立后，CNI 为它配置网络命名空间和 IP。然后 kubelet 通过 ImageService 拉取镜像，再通过 RuntimeService 创建并启动业务容器。容器运行期间，kubelet 通过 CRI 查询状态、执行探针相关操作、停止容器并收集退出信息。

CRI 返回的状态会影响 Kubernetes 上层对象。容器退出原因、重启次数、镜像拉取错误、sandbox 错误都会被 kubelet 写回 Pod status 和 events。用户看到的 ImagePullBackOff、CrashLoopBackOff、CreateContainerError 很多都来自 kubelet 和 CRI runtime 的交互结果。

涉及组件包括 kubelet、CRI gRPC endpoint、containerd CRI plugin、CRI-O、Pod sandbox、ImageService、RuntimeService、CNI、OCI runtime、image registry、cgroups、namespaces、Pod status、events、logs 和 `crictl`。`crictl` 之所以常用于排查，就是因为它直接按 CRI 视角看节点运行时。

## 212. CRI 配置错误时会导致哪些线上问题？

可以先这样答：CRI 配错会让 kubelet 认为容器运行时不可用，或者让 Pod 创建链路在 sandbox、镜像、容器启动阶段失败。线上表现包括节点 NotReady、ContainerRuntimeNotReady、Pod 一直 Pending、sandbox 创建失败、镜像拉取失败、容器状态无法更新。

最典型的是 kubelet runtime endpoint 配错。比如 socket 路径不对、containerd CRI plugin 没启、权限不对、版本不兼容，kubelet 就无法和 runtime 通信。节点上已有容器可能还在跑，但 Kubernetes 控制面会认为节点无法正常管理新 Pod。

CRI runtime 和 CNI 的边界也常被误解。CRI 创建 sandbox 后需要 CNI 配网，如果 CNI 配置错误，kubelet 看到的可能是 RunPodSandbox 失败。表面上是 runtime 报错，根因却在 CNI、IPAM、网卡或网络策略。排查时不能只盯一个组件。

镜像服务配置也会影响发布。registry 证书、认证、代理、镜像平台架构、pause image 配错，会导致 ImageService 拉取失败。生产里要把 kubelet、runtime、CNI、registry 和节点磁盘一起看，否则很容易把 CRI 问题误判成应用问题。

## 213. CRI 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：CRI 不直接影响 Service 负载均衡和服务发现规则，但它影响 Pod 是否能创建、启动和报告状态。一个 Pod 如果卡在 RunPodSandbox 或 CreateContainer 阶段，就不会 ready，也不会进入 EndpointSlice 的 ready 后端。

对弹性伸缩来说，CRI 是扩容链路的节点执行接口。HPA 新增副本、CA 新增节点后，kubelet 最终要通过 CRI 创建容器。CRI endpoint 不可用或 runtime 响应慢，扩出来的容量就无法落地。你会看到 Deployment desired 很高，Pod 仍 Pending 或 ContainerCreating。

对可观测性来说，CRI 提供了节点运行时视角。`crictl pods`、`crictl ps`、`crictl images`、`crictl inspect` 能看到 kubelet 之下的 sandbox、容器和镜像状态。kubelet events 只是摘要，runtime 日志和 CRI 状态往往能给出更具体的失败原因。

对负载均衡和服务发现的间接影响主要体现在后端抖动。CRI 错误导致容器反复创建失败或重启，Service 后端数量就会变少或不稳定。发布时如果某批节点 runtime 配置有问题，流量会集中到健康节点上的旧 Pod，造成容量不均。


## 214. OCI runtime 在云原生系统中解决什么问题？

可以先这样答：OCI runtime 解决的是容器进程如何按标准规范被创建和运行的问题。Kubernetes 不直接 `clone` 进程、配置 namespace、设置 cgroup、挂载 rootfs、应用 seccomp 或 capabilities；这些底层动作通常交给 runc、crun、Kata Containers 等 OCI runtime 完成。

OCI runtime 的价值是标准化。Open Container Initiative 定义了 runtime spec，描述容器配置、rootfs、process、mounts、Linux namespaces、cgroups、capabilities、seccomp 等内容。高层运行时 containerd 或 CRI-O 可以生成符合规范的 bundle，再调用 OCI runtime 创建容器进程。

它解决的是“容器最终怎么变成一个受隔离的 Linux 进程”的问题。CRI 是 kubelet 到运行时的接口，containerd 是高层运行时，OCI runtime 是最底层执行者之一。这个分层对排障很重要。

OCI runtime 不负责 Kubernetes Service、Pod 调度、镜像仓库、CNI 网络策略或 HPA 扩缩容。它只负责按配置启动和管理容器进程。业务能不能被访问，还要看上层很多组件。

## 215. OCI runtime 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：containerd 接到 kubelet 通过 CRI 发来的创建容器请求后，会准备镜像 rootfs 和 OCI bundle。bundle 里有 `config.json`，描述容器要运行的进程、环境变量、工作目录、mount、namespace、cgroup、capabilities、seccomp、AppArmor/SELinux 等。然后 containerd-shim 调用 runc 这类 OCI runtime 创建容器。

OCI runtime 会设置 Linux namespaces，例如 pid、mount、uts、ipc、network、user；设置 cgroup 限制 CPU、内存、pids 等资源；挂载 rootfs 和必要文件系统；应用安全配置；最后启动容器内的 init 进程。启动后，shim 负责和容器进程保持关系，收集退出状态。

不同 OCI runtime 有不同取舍。runc 是常见默认选择；crun 更轻量；Kata Containers 会通过轻量虚拟机提供更强隔离。Kubernetes 上层通常不用关心具体实现，但安全、性能和兼容性会受影响。

涉及组件包括 kubelet、CRI runtime、containerd、containerd-shim、OCI runtime、OCI runtime spec、rootfs、overlayfs snapshot、Linux namespaces、cgroups、seccomp、AppArmor、SELinux、capabilities、Pod security、kubelet status 和节点内核。OCI runtime 是容器隔离真正落地的地方。

## 216. OCI runtime 配置错误时会导致哪些线上问题？

可以先这样答：OCI runtime 配错会导致容器无法启动、权限异常、资源限制不生效、安全策略误拒或隔离边界过宽。线上表现通常是 CreateContainerError、RunContainerError、permission denied、operation not permitted、seccomp 拒绝、mount 失败、cgroup 写入失败。

安全配置是常见问题。seccomp profile 禁止了应用需要的系统调用，容器启动后某些功能失败；capabilities 去掉过多，应用不能绑定低端口、设置网络参数或访问设备；AppArmor/SELinux 策略过严，文件访问失败。反过来，privileged、hostPID、hostNetwork、过多 capabilities 会扩大攻击面。

cgroup 和 runtime 版本也会出问题。cgroup v1/v2 配置不一致、systemd cgroup driver 不匹配、runtime 不支持某些配置字段，会让容器创建失败或资源限制异常。升级内核、runc、containerd、Kubernetes 时，这些兼容性要一起验证。

还有一类问题来自底层文件系统和挂载。rootfs 不完整、overlayfs 错误、只读根文件系统配置不当、volume mount 路径冲突，都会在 OCI runtime 创建阶段暴露。排查时要结合 kubelet event、containerd log、runtime log、`runc` 错误和节点内核日志。

## 217. OCI runtime 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：OCI runtime 不直接参与负载均衡或服务发现，但它决定容器进程能否按正确隔离和资源限制运行。容器启动失败，Pod 不会 ready；容器被错误限制，业务变慢或崩溃；安全策略误拒，服务可能只在某些路径出错。

对负载均衡来说，影响体现在后端质量。OCI runtime 创建失败，Service 少一个后端；seccomp 或 mount 配置导致应用部分功能失败，Pod 可能仍 ready，但请求会报错；CPU/内存/pids cgroup 设置异常，会造成延迟、OOM 或进程创建失败。

对弹性伸缩来说，新 Pod 扩出来后要经过 OCI runtime 创建容器进程。runtime 慢、失败或 shim 异常，会拉长扩容时间。节点内某个 runtime 版本有 bug，可能只影响一批节点，导致容量看起来足够但实际可用后端不足。

对可观测性来说，要观察容器启动失败原因、runtime error、seccomp audit、AppArmor/SELinux denial、cgroup throttling/OOM、process exit code、shim 状态和节点内核日志。Kubernetes 事件只是表层，OCI runtime 错误通常要下钻到节点才能看清楚。


## 218. overlayfs 在云原生系统中解决什么问题？

可以先这样答：overlayfs 解决的是容器镜像分层和写时复制文件系统的问题。容器镜像由多层只读 layer 组成，运行容器时需要在这些只读层之上提供一个可写层。overlayfs 把 lowerdir、upperdir、workdir 合成一个 merged 视图，让容器看到像普通文件系统一样的根目录。

它的价值是节省磁盘和加快启动。同一节点上多个容器可以共享相同镜像层，不需要每个容器复制一份完整 rootfs。容器对文件的写入进入自己的 upperdir，其他容器仍然共享只读层。镜像层复用是容器快速分发和运行的基础之一。

overlayfs 也让镜像构建和运行时语义更清晰。Dockerfile 每一层、镜像 pull 后的 layer、containerd snapshotter 管理的 snapshot，最终都和这种分层视图相关。应用看到的是一个文件树，底层实际是多层叠加。

它不解决持久化数据问题。容器可写层通常随容器生命周期消失，数据库、用户上传文件、队列状态等应该放到 PVC、外部存储或专门数据卷里。把重要数据写到容器 rootfs，是很典型的云原生反模式。

## 219. overlayfs 的工作原理是什么？涉及哪些 Kubernetes 组件？

可以先这样答：overlayfs 是 Linux 内核文件系统能力。它把一个或多个 lowerdir 作为只读底层，把 upperdir 作为可写层，把 workdir 作为内部工作目录，然后挂载出 merged 目录。容器进程看到 merged 目录作为 rootfs。

当容器读取文件时，如果 upperdir 有修改后的版本，就读 upperdir；否则从 lowerdir 读取。写入只读层已有文件时，会触发 copy-up，把文件复制到 upperdir 再修改。删除 lowerdir 里的文件时，会通过 whiteout 表示这个文件在合并视图里被删除。

在 Kubernetes 节点上，kubelet 通过 CRI 调用 containerd；containerd 的 overlayfs snapshotter 管理镜像层和容器可写层；OCI runtime 把 snapshotter 准备好的 rootfs 挂载进容器。overlayfs 本身不认识 Pod 或 Deployment，它只是节点内核提供的文件系统机制。

涉及组件包括 Linux kernel overlayfs、containerd snapshotter、image layers、content store、OCI runtime、kubelet、CRI、node filesystem、image GC、ephemeral storage、emptyDir/PVC 对比、日志目录和磁盘监控。容器文件系统问题经常要从 Kubernetes 下钻到节点存储层排查。

## 220. overlayfs 配置错误时会导致哪些线上问题？

可以先这样答：overlayfs 或相关 snapshotter 配置错误会导致镜像解包失败、容器 rootfs 挂载失败、容器启动慢、磁盘占满、inode 耗尽、文件写入异常或节点 runtime 不稳定。线上看到的可能是 CreateContainerError、ContainerCreating 卡住、image pull 很慢、节点 DiskPressure。

底层文件系统兼容性很重要。overlayfs 对内核版本、底层文件系统特性、xattr、d_type、SELinux 等有要求。节点镜像或内核升级后，如果底层不兼容，containerd snapshotter 可能无法正常创建 snapshot。这个问题通常不是应用代码导致的。

copy-up 会影响性能。容器第一次修改大文件时，需要从 lowerdir 复制到 upperdir；大量小文件、频繁写 rootfs、解压大包、写日志到容器层，都会放大 overlayfs 开销。把运行时临时文件、缓存和日志策略设计好，比事后调节点更有效。

磁盘清理也是常见问题。镜像层、旧容器 writable layer、失败的 snapshot、日志文件、临时文件都占用节点磁盘和 inode。image GC 阈值不合理、containerd 清理失败、应用往 rootfs 写大文件，都会触发 DiskPressure 和 Pod 驱逐。

## 221. overlayfs 如何影响负载均衡、服务发现、弹性伸缩或可观测性？

可以先这样答：overlayfs 不直接参与负载均衡和服务发现，但它影响容器启动速度、镜像复用、磁盘压力和运行时稳定性。新 Pod 能否快速启动并 ready，和镜像层解包、snapshot 创建、rootfs 挂载都有关系。

对负载均衡来说，影响主要发生在发布和扩容阶段。overlayfs 或 snapshotter 慢，新 Pod ready 慢，Service 后端增加慢；容器层磁盘满，Pod 可能重启或被驱逐，后端数量下降。业务看到的是容量迟迟补不上，底层原因可能是节点文件系统。

对服务发现来说，Pod 没 ready 就不会进入 EndpointSlice ready 集合。StatefulSet 和 Headless Service 里，如果某个 Pod 因 rootfs 或磁盘问题反复重启，DNS 记录可能还在，但实例不可用。服务发现只告诉你地址，不保证该地址背后的容器文件系统健康。

对弹性伸缩和可观测性来说，overlayfs 影响冷启动时间和节点磁盘容量。HPA/CA 扩容后，如果所有新节点都要拉大镜像并解包大量层，扩容恢复会变慢。需要监控 image pull duration、container start latency、snapshotter error、node filesystem usage、inode、ephemeral storage、DiskPressure、image GC 和容器 writable layer 增长。


## 222. Kubernetes 中一次 Service 访问如何被路由到 Pod？

可以先这样答：一次普通 ClusterIP Service 访问，大致经历“服务名解析到 ClusterIP、节点数据面捕获 Service VIP、选择一个 ready endpoint、把目标地址改写或转发到 Pod IP”这几步。Service 提供稳定入口，EndpointSlice 保存实际后端，kube-proxy、Cilium 或云厂商数据面负责把这个稳定入口接到动态 Pod 上。

从客户端 Pod 看，第一步通常是 DNS。客户端访问 `my-svc.my-ns.svc.cluster.local`，CoreDNS 根据 Service 对象返回 ClusterIP。客户端随后向这个虚拟 IP 和端口发包。ClusterIP 本身不是某个真实网卡地址，它是 Kubernetes 数据面要识别和处理的虚拟服务地址。

包到达客户端所在节点后，转发路径取决于 Service 数据面实现。iptables 模式会命中 kube-proxy 写入的 netfilter 规则，随机或按会话亲和选择后端并 DNAT 到 Pod IP；IPVS 模式会进入内核 IPVS virtual service；Cilium eBPF 模式可能在 socket、TC 或 XDP 等路径上查 BPF map 完成 service lookup 和后端选择。无论哪种实现，都需要消费 Service 和 EndpointSlice 状态。

后端 Pod 可能在同节点，也可能在其他节点。同节点时包可以直接进入目标 Pod 的 veth；跨节点时还要经过 CNI 的路由或封装，比如原生路由、VXLAN、Geneve、BGP 等。回包路径还会受 SNAT、externalTrafficPolicy、sessionAffinity、NetworkPolicy 和 conntrack 影响。排查 Service 访问问题时，要把 DNS、Service、EndpointSlice、节点转发规则、CNI 路由和 Pod readiness 串起来看。

## 223. iptables 模式下 Service 转发规则如何扩展到大规模集群？

可以先这样答：iptables 模式通过在每个节点上维护一组 netfilter 规则来表达 Service 到 endpoint 的转发关系。每个 Service、端口、NodePort、endpoint 都会对应若干链和规则；kube-proxy watch API Server 里的 Service、EndpointSlice 变化，再把期望状态同步成本机 iptables。大规模下的核心问题不是“能不能表达”，而是规则数量、同步耗时、endpoint 变更频率和 conntrack 压力。

iptables 的扩展方式主要靠增量同步、规则链组织和 ipset 等优化。老版本 kube-proxy 更容易全量重写规则，Service 和 endpoint 很多时 CPU 抖动明显；后续版本减少了不必要的全量同步，提升了规则更新效率。即便如此，iptables 仍然是用通用包过滤规则模拟服务负载均衡，规模越大，规则可读性和排障成本越高。

流量匹配也有成本。iptables 模式下，包需要经过一系列规则链，最终命中某个 Service 和后端选择规则。Service 数和 endpoint 数很高时，规则规模会影响同步时间和部分路径的匹配开销。连接跟踪表也会成为瓶颈，尤其是短连接很多、NodePort/LoadBalancer 流量大、SNAT 场景复杂时。

生产排查要看几个信号：kube-proxy 的 sync duration、sync proxy rules 频率、iptables 规则数量、conntrack 使用率、节点 CPU softirq、EndpointSlice churn、API watch 延迟和 Service 数量。不要只说“iptables 不适合大规模”，更准确的说法是：它可以工作，但当 Service 和 endpoint 数量、变更频率和连接规模上来以后，规则维护和排障成本会明显上升。

## 224. IPVS 模式为什么更适合大规模 Service？

可以先这样答：IPVS 模式曾经更适合大规模 Service，是因为它用 Linux 内核里的 IP Virtual Server 表达“一个虚拟服务后面有多个真实后端”，底层以哈希表等结构维护 virtual server 和 real server。相比用大量 iptables 规则表达后端选择，IPVS 的模型更接近四层负载均衡器，规则同步和转发查找在大规模场景下更有优势。

IPVS 支持多种调度算法，例如 round-robin、least connection、source hash、Maglev hash 等。对大量 Service 和 endpoint 来说，这些算法和数据结构比纯 iptables 随机规则更像负载均衡系统。kube-proxy 在 IPVS 模式下 watch Service/EndpointSlice，把 Service 写成 virtual server，把 endpoint 写成 real server。

但面试里要补上当前边界：Kubernetes 官方已经把 IPVS proxy mode 标记为 deprecated。原因不是 IPVS 性能完全不行，而是 IPVS 内核 API 和 Kubernetes Service API 的所有边界语义并不完全匹配，某些 Service edge case 很难正确实现。新集群选型时，要同时考虑 nftables、eBPF 和云厂商原生数据面。

所以更严谨的回答是：在历史上，IPVS 比 iptables 更适合大规模 Service 的同步和转发；在当前版本语境下，它更像存量集群需要理解的模式，而不是新架构的默认推荐。生产里如果还在用 IPVS，要关注内核模块、kube-proxy 配置、Service 功能兼容性、conntrack、NodePort、externalTrafficPolicy 和升级路径。

## 225. Cilium 使用 eBPF 后，Service 转发路径有什么变化？

可以先这样答：Cilium 使用 eBPF 后，Service 转发从“主要依赖 kube-proxy 写 iptables/IPVS 规则”变成“Cilium agent 把 Service 和 endpoint 状态写入 BPF maps，内核中的 eBPF 程序在更靠近包处理的位置完成查表、后端选择、NAT 和策略判断”。如果启用 kube-proxy replacement，节点上可以不再由 kube-proxy 维护 Service 数据面。

路径变化的关键是状态表达方式。Service、EndpointSlice、Node 等 Kubernetes 对象仍然是控制面来源；不同的是，Cilium watch 这些对象后，把 ClusterIP、NodePort、LoadBalancer、backend、rev NAT、affinity、policy 等状态写进 BPF map。包进入节点后，eBPF 程序查 map 决定转发到哪个后端，而不是沿着长 iptables 链找规则。

这带来两个好处。第一，Service 和 endpoint 规模很大时，BPF map 查找和增量更新更适合做数据面；第二，Cilium 可以在同一条路径里结合网络策略、身份、负载均衡和可观测性。Hubble 能看到 flow、drop reason、policy verdict，这比只看 iptables 计数更贴近 Kubernetes 语义。

代价是排障层级更深。你要看的不再只是 iptables-save 或 ipvsadm，还要会看 Cilium status、BPF map、endpoint identity、policy trace、Hubble flow、agent log、内核版本和 CNI 配置。Cilium 没有改变 Service/EndpointSlice 的用户模型，但改变了节点上实现 Service 转发的方式。

## 226. Pod readiness 变为 false 后，流量多久会停止进入？

可以先这样答：没有一个固定秒数。readiness 变为 false 后，kubelet 更新 Pod Ready 条件，API Server 保存状态，EndpointSlice controller 更新 endpoint 的 ready 条件，kube-proxy、Ingress Controller、Gateway、Service Mesh 或云负载均衡再 watch 到变化并更新自己的数据面。这个链路通常很快，但不是同步阻塞的原子操作。

在纯 Kubernetes Service 场景里，关键路径是 kubelet 到 API Server、EndpointSlice 更新、节点数据面同步。kube-proxy 或 Cilium watch 到 EndpointSlice 后，本机规则或 BPF map 才会变化。已有连接不一定马上断开，尤其是 TCP 长连接、HTTP/2、gRPC、WebSocket 这类连接复用场景；readiness 主要阻止新的常规后端选择。

在 Ingress、Gateway、Service Mesh 或云 LB 场景里，传播链路更长。控制器要 watch EndpointSlice 或 Pod 状态，生成 NGINX/Envoy/云 LB 配置，再 reload 或 xDS 下发。不同控制器的同步周期、缓存、连接 drain 行为不同，所以从 readiness false 到入口层停止新流量可能是几百毫秒，也可能是几秒甚至更久。

生产设计不能假设 readiness false 立即生效。优雅下线通常要先让 readiness 失败，再等待一段 drain 时间，随后停止接新请求、处理已有请求，最后退出进程。这个等待时间应该来自实测：看 EndpointSlice ready 状态变化、kube-proxy/Cilium 更新、Ingress/Envoy 后端摘除、客户端错误率和长连接关闭时间。


## 227. Deployment 滚动发布如何避免无可用副本？

可以先这样答：Deployment 依靠 rollingUpdate 策略、readinessProbe、minReadySeconds、PodDisruptionBudget、反亲和和足够资源来避免发布时无可用副本。核心思路是：新 Pod 没有真正 ready 之前，不要过早删掉旧 Pod；旧 Pod 下线前，要保证还有足够副本能接流量。

RollingUpdate 里最关键的是 `maxUnavailable` 和 `maxSurge`。`maxUnavailable` 控制升级过程中最多允许多少期望副本不可用，设为 0 可以要求发布期间不主动减少可用副本；`maxSurge` 控制最多额外创建多少 Pod。对在线服务，常见做法是允许一定 surge，让新副本先起来，再逐步替换旧副本。资源不足时，surge Pod 可能 Pending，这时发布会卡住而不是安全完成。

readinessProbe 是第二道门。Deployment 只看到 Pod ready 后，才会把它计入可用副本。`minReadySeconds` 可以要求 Pod ready 后持续稳定一段时间再算 available，避免刚启动就短暂 ready、随后崩溃的副本被当成可用。对有预热、缓存加载、连接池建立的服务，readiness 不应只检查进程端口是否打开。

还要考虑故障域。多个副本如果都在同一节点或同一可用区，滚动发布叠加节点故障就可能瞬间无可用副本。生产里应结合 topologySpreadConstraints、podAntiAffinity、PDB、合理的 request 和 Cluster Autoscaler。面试里可以这样收束：Deployment 策略保证“替换顺序”，readiness 保证“新副本可服务”，调度约束保证“副本不要集中在同一个风险点”。

## 228. 如何设计 Kubernetes 中的优雅下线，避免请求被中断？

可以先这样答：优雅下线要把摘流、停止接新请求、处理已有请求、释放资源和进程退出分成几个阶段，而不是只在 preStop 里 sleep。一个比较稳妥的顺序是：收到终止信号或下线指令后，先让 readiness 失败；等待 Service、Ingress、Gateway、Service Mesh 和客户端感知摘流；应用停止接受新请求；已有请求在超时时间内完成；最后退出进程。

Kubernetes 删除 Pod 时，会设置 deletionTimestamp，kubelet 执行 preStop hook，然后向容器主进程发送 SIGTERM，等 terminationGracePeriodSeconds 到期后再 SIGKILL。preStop 的执行时间也算在 grace period 里，所以不能把 preStop 写成很长的阻塞脚本。应用必须正确处理 SIGTERM，否则再好的 Kubernetes 配置也没用。

readiness 是摘流入口。下线前应让 readinessProbe 返回失败，让 EndpointSlice ready 变为 false。对长连接和代理路径，还要配合 Envoy/NGINX drain、gRPC graceful stop、HTTP keep-alive 关闭、连接池摘除、消费者停止拉新消息。不同流量入口传播时间不同，drain 时间要通过压测或线上观测确定。

还要配合 PDB 和发布策略。PDB 防止自愿驱逐一次带走太多副本；rollingUpdate 控制替换节奏；preStop 和 terminationGracePeriodSeconds 处理单个 Pod 下线；应用层超时和幂等保护处理已经进入的请求。排查优雅下线失败时，要看 deletionTimestamp、EndpointSlice 状态、Ingress/mesh 后端状态、应用 SIGTERM 日志、未完成请求数和客户端错误时间线。

## 229. CPU throttling 为什么会导致延迟升高？

可以先这样答：CPU throttling 会让容器“想运行但被 cgroup quota 暂停”，所以请求处理时间、队列等待、GC、TLS 加解密、日志编码、序列化和事件循环都会被拉长。对延迟敏感服务来说，问题不只是吞吐下降，而是尾延迟会被放大。

CPU limit 通常会转成 Linux CFS quota。容器在一个周期内用完 CPU 配额后，即使节点上还有空闲 CPU，也可能要等下一个周期才能继续运行。应用线程被暂停时，请求还在排队，连接还占着，超时计时还在走。于是平均 CPU 看起来不一定很高，P99 却明显上升。

运行时会放大这个影响。Go 服务里，goroutine 调度、GC、timer、网络轮询都需要 CPU；JVM 里，GC、JIT、线程池调度也需要 CPU；Node.js 事件循环一旦被限速，所有请求共享同一个延迟池。CPU throttling 往往表现为“偶发慢请求”，而不是持续 100% CPU。

还有一个容易误判的点：HPA 看到 CPU utilization 高可能扩容，但如果每个 Pod 的 CPU limit 都太低，新 Pod 仍然会被限速。扩容能分摊流量，但不能修复单 Pod 被 quota 卡住的问题。排查时要同时看 CPU usage、CPU request、CPU limit、throttled seconds、throttled periods、run queue、GC 和业务延迟。

## 230. 容器内应用如何感知 CPU limit？

可以先这样答：容器内应用通常通过 cgroup 文件、运行时库或语言运行时来感知 CPU limit。Kubernetes 把容器的 CPU request/limit 交给容器运行时，运行时写入 cgroup；应用如果要知道自己能用多少 CPU，需要读取 cgroup v1/v2 的 CPU quota、period、cpuset 或相关接口，而不是只看宿主机总 CPU 数。

在 cgroup v1 中，常见文件是 `cpu.cfs_quota_us` 和 `cpu.cfs_period_us`，quota 除以 period 可以估算可用 CPU 核数；如果 quota 为 -1，表示没有 CFS quota。cgroup v2 中通常看 `cpu.max`，例如 `50000 100000` 大致表示 0.5 核。cpuset 还可能限制进程能跑在哪些 CPU 上，和 quota 不是同一个概念。

现代语言运行时已经开始适配容器限制，但不能盲信。Go 的 GOMAXPROCS、JVM 的容器感知参数、Node.js/libuv 线程池、Python 多进程数，都可能受 CPU 限制影响。如果运行时没有正确感知 cgroup，它可能按宿主机 CPU 数开过多线程，导致频繁争抢和 throttling。

生产里更稳的做法是显式校验。容器启动时打印 CPU quota、period、cpuset、GOMAXPROCS 或 JVM ActiveProcessorCount；指标里同时暴露容器 CPU limit 和应用并发配置；压测时观察 throttling 和延迟曲线。应用感知 CPU limit 的目的不是炫技，而是把 worker 数、连接池、批处理并发、GC 参数调到和容器资源匹配。

## 231. HPA 基于 CPU 指标扩容有哪些滞后和误判？

可以先这样答：HPA 基于 CPU 扩容会有采集滞后、计算窗口滞后、Pod 启动滞后和负载均衡生效滞后。它不是实时控制系统。Metrics Server 采集 kubelet/cAdvisor 指标需要时间，HPA controller 周期性拉取指标，新 Pod 创建、调度、拉镜像、启动、readiness 通过、进入 EndpointSlice 还需要时间。

CPU 指标本身也容易误判。CPU 高可能是有效业务负载，也可能是 GC、日志、压缩、加密、重试风暴、死循环或 sidecar 开销；CPU 低不代表服务健康，I/O 等待、数据库慢、锁竞争、队列阻塞、连接池耗尽时 CPU 可能不高但延迟已经很差。用 CPU 做唯一扩缩容指标，很容易和用户体验脱节。

CPU utilization 的分母是 request。request 太低，利用率被放大，HPA 过度扩容；request 太高，利用率被压低，HPA 扩容不及时。VPA 如果同时调整 CPU request，也会改变 HPA 的分母，造成扩缩容行为变化。没有 CPU request 的容器还可能让对应指标不可用。

还有长连接和冷启动问题。HPA 加了新 Pod，已有 HTTP/2、gRPC、WebSocket 连接不会自动均匀迁移；新 Pod 冷启动、缓存未热、JIT 未完成时承载能力也不等于稳定状态。生产里要给 HPA 配合理的 stabilization window、scaleUp/scaleDown behavior、readiness/startupProbe，并用业务指标补充 CPU。


## 232. 如何为延迟敏感服务设计扩缩容指标？

可以先这样答：延迟敏感服务不要只用平均 CPU 扩缩容，应该围绕“用户请求是否排队、后端是否接近饱和、错误和尾延迟是否恶化”设计指标。常见组合是 QPS/RPS、并发 in-flight、队列长度、队列等待时间、P95/P99 延迟、错误率、每 Pod 连接数、下游依赖耗时和资源饱和度。

指标要选能提前反映压力的信号。P99 延迟已经升高时再扩容，往往已经晚了；队列长度、in-flight、连接池等待、线程池活跃数、事件循环延迟、每 Pod QPS 更适合做提前量。对网关和 RPC 服务，每 Pod 并发和请求排队时间通常比 CPU 更贴近容量。

还要区分服务自身瓶颈和下游瓶颈。下游数据库慢导致本服务 in-flight 增多，如果盲目扩本服务副本，会把更多请求压到数据库上。此时更需要限流、熔断、排队上限和下游保护。扩缩容指标最好和 SLO、依赖指标一起看，不能让 HPA 成为故障放大器。

生产设计可以分层：HPA 用业务吞吐或并发指标做快速扩容，用 CPU/内存作为保护性指标；VPA 校准 request；Cluster Autoscaler 保证节点容量；预扩容或 scheduled scaling 处理已知峰值；scale down 要保守，避免刚恢复就缩掉容量。延迟敏感场景里，稳定比省几台机器更重要。

## 233. Service Mesh 会带来哪些资源开销和故障面？

可以先这样答：Service Mesh 的主要开销来自 sidecar 或节点代理的 CPU、内存、连接池、证书、配置同步和观测数据。每个请求多经过一层代理，就会增加一次转发、过滤器处理、TLS、指标记录和日志处理。单次开销可能不大，但服务数、连接数、QPS、标签基数上来后，成本会很明显。

资源开销包括每个 Pod 多一个代理容器、更多 file descriptors、更多连接、更多内存 buffer、更高 CPU、更多 Prometheus 指标和 trace/log 数据。HPA 和容量规划要把 sidecar request 算进去，否则节点看起来资源够，实际代理已经成为瓶颈。

故障面也增加了。控制面不可用会影响新配置、证书签发或新 Pod 加入；sidecar 注入失败会造成部分服务不在网格；xDS 配置错误会造成 503；mTLS 证书过期会造成握手失败；策略误配会误拒流量；代理资源不足会提升延迟；观测系统高基数会拖垮 Prometheus。

设计上要接受这个现实：Mesh 用复杂度换统一治理。生产里要有网格控制面 SLO、代理资源基线、证书到期告警、配置回滚、分阶段开启、旁路策略、命名空间隔离、关键路径压测和故障演练。不要把 Mesh 当成“加上就自动可靠”的透明层。

## 234. sidecar 注入失败会导致什么问题？

可以先这样答：sidecar 注入失败会导致 Pod 没有进入预期的数据面路径。轻则服务缺少 mTLS、指标、访问日志、重试、限流和策略；重则在强制 mTLS 或 AuthorizationPolicy 场景下，未注入 Pod 无法和网格内服务通信，或者成为安全绕过点。

注入失败有两种常见表现。第一种是 Pod 创建被 webhook 拒绝，Deployment 滚动发布卡住，新副本起不来；第二种是 Pod 创建成功，但没有 sidecar。这取决于 mutating webhook 的 failurePolicy、namespace label、pod annotation、webhook 可用性和选择器配置。

混合状态最危险。同一个 Deployment 里一部分 Pod 有 sidecar，一部分没有，Service 仍然把它们都当后端。网格控制面看到的流量、mTLS 身份、策略命中和指标就会不完整；如果 STRICT mTLS 开启，无 sidecar Pod 可能无法被其他服务访问；如果策略依赖代理执行，无 sidecar Pod 可能绕过治理。

排查时要看 namespace 是否开启注入、Pod annotation、mutating webhook 配置、admission 日志、Pod spec 里是否有 sidecar、initContainer 是否成功、iptables/CNI 重定向是否存在、代理是否 ready、控制面是否接收到该 workload。发布前最好有准入策略强制关键命名空间必须注入成功。

## 235. mTLS 在 Service Mesh 中如何自动证书管理？

可以先这样答：Service Mesh 的 mTLS 自动证书管理通常由控制面负责身份、证书签发、轮转和分发，数据面代理负责在连接建立时使用证书完成双向 TLS。应用不直接管理证书文件，也不需要在业务代码里写 TLS 握手逻辑。

以 Istio 这类架构为例，工作负载身份通常和 Kubernetes ServiceAccount 绑定。代理启动后通过安全通道向控制面申请证书，控制面或 CA 根据工作负载身份签发短生命周期证书，再通过 SDS 或类似机制把证书分发给 Envoy。证书接近过期时，代理自动轮转。

Linkerd 也有类似思想：identity 组件根据 ServiceAccount 身份给代理签发短期证书，代理之间建立 mTLS 连接。不同 Mesh 的实现细节不同，但核心都是把“谁是这个工作负载”和“它能拿到什么证书”绑定到 Kubernetes 身份和控制面信任根上。

生产里要关注 trust anchor、issuer、证书有效期、轮转失败、时钟偏移、控制面可用性、跨集群信任、SPIFFE/SPIRE 集成和策略兼容。mTLS 自动化降低了应用管理证书的成本，但把风险集中到了网格控制面和身份系统。证书到期告警、灰度轮转和故障回滚必须提前准备。

## 236. 多集群服务发现和流量治理如何设计？

可以先这样答：多集群设计要先明确目标：是容灾、就近访问、容量扩展、灰度发布，还是跨地域统一治理。目标不同，服务发现和流量治理的方案不同。不要一开始就追求“任意服务跨任意集群互通”，那会把网络、安全、观测和故障域全部耦合在一起。

服务发现可以分层。集群内仍用 Kubernetes Service 和 DNS；跨集群可以用全局 DNS、云负载均衡、Service Mesh multi-cluster、Gateway API、多集群服务 API 或注册中心同步。关键是明确服务名、集群名、区域、版本、健康状态和权重如何表达，以及状态传播延迟有多大。

流量治理要把入口流量和东西向流量分开。南北向可以通过全局 LB/GSLB/Anycast/Gateway 按地域、健康和权重分流；东西向可以通过 Mesh、Gateway 或显式跨集群客户端策略处理。跨集群调用要考虑 mTLS 信任、身份映射、网络连通、NetworkPolicy、防火墙、重试和超时。跨地域重试尤其危险，容易把局部故障放大全局故障。

生产上还要设计故障隔离。每个集群应能独立运行关键路径；跨集群服务发现失败时，要有本地降级或静态兜底；监控要能按 cluster/region/zone/service/version 分解；发布要支持按集群灰度；数据依赖要明确一致性边界。多集群不是把单集群模型复制几份，而是把故障域和治理域显式建模。


## 237. 如何排查 Kubernetes 中 DNS 解析慢？

可以先这样答：DNS 解析慢要按“应用侧、Pod 网络、CoreDNS、上游 DNS、节点本地缓存、控制面对象”逐层排查。先确认是所有域名慢，还是只有 `*.svc.cluster.local` 慢；是所有 Pod 慢，还是某些节点、某些 namespace、某些服务慢。范围定不清，后面的命令容易乱跑。

应用侧先看解析模式。glibc 的 ndots、search domain、超时和重试会让一个短域名触发多次查询；Java、Go、Node.js 的 DNS 缓存策略也不同。`ndots:5` 场景下，访问外部短域名可能先按多个集群 search suffix 尝试，造成额外延迟。可以在 Pod 内用 `dig +trace`、`time nslookup`、`tcpdump` 或应用日志确认每次请求发了多少 DNS 查询。

CoreDNS 层要看负载和错误。检查 CoreDNS Pod 是否重启、CPU throttling、内存压力、日志里的 timeout、SERVFAIL、plugin error；看 `coredns_dns_request_duration_seconds`、request count、cache hit、forward latency、upstream error。CoreDNS 的副本数、资源 request、HPA、cache 插件、forward 插件和上游 DNS 稳定性都影响延迟。

网络层要看 Pod 到 kube-dns Service 是否通。kube-dns 本身是 Service，仍要经过 Service 数据面和 EndpointSlice。iptables/Cilium/kube-proxy 问题、NetworkPolicy 阻断 UDP/TCP 53、节点 conntrack 满、MTU 问题、CoreDNS endpoint 分布不均，都可能表现成 DNS 慢。生产优化常见手段包括 NodeLocal DNSCache、合理 CoreDNS 副本和资源、减少无谓 search 查询、监控上游 DNS 延迟。

## 238. 如何排查 Pod 到 Pod 网络不通？

可以先这样答：Pod 到 Pod 不通，要先判断是哪种不通：同节点不通、跨节点不通、单向不通、只 TCP 不通、只 DNS 名称不通、只通过 Service 不通，还是直接 Pod IP 不通。不同范围对应完全不同的根因。

第一步看对象状态。确认源 Pod 和目标 Pod 是否 Running/Ready，Pod IP 是否存在，是否在同一节点，目标容器是否监听端口，Service selector 是否匹配，EndpointSlice 是否包含目标后端。很多“网络不通”其实是应用没监听、readiness 未通过或 Service 没 endpoint。

第二步看策略和路由。NetworkPolicy 默认不隔离，但一旦某个方向被策略选中，就只允许显式放行的连接。要同时检查源 Pod egress 和目标 Pod ingress。再看 CNI 状态、节点路由、隧道接口、MTU、BGP/VXLAN/Geneve、iptables/eBPF datapath、conntrack、节点防火墙和云安全组。

第三步用分层工具验证。Pod 内 `curl/nc` 测目标 IP 和端口，`dig` 验证 DNS，节点上抓包看包是否离开源 veth、是否到达目标节点、是否进入目标 Pod。Cilium 场景看 Hubble flow、policy verdict 和 drop reason；Calico 看 Felix/BGP/iptables；Flannel 看 VXLAN 和路由。排障结论要落到“包在哪一层丢了”，而不是停在“网络有问题”。

## 239. 如何排查 Ingress 502/504？

可以先这样答：Ingress 502/504 要先区分含义。502 通常表示入口控制器连后端失败、后端提前断开、协议不匹配或上游返回异常；504 通常表示入口控制器等后端响应超时。不同 Ingress Controller 细节不同，但排查路径可以按“客户端到 Ingress、Ingress 到 Service、Service 到 Pod、Pod 到依赖”拆开。

先看 Ingress 规则是否命中。host、path、IngressClass、TLS Secret、backend Service 名称和端口是否正确；入口控制器是否 watch 到该 Ingress；配置是否 reload 成功。再看 Service 是否有 ready endpoints，端口名、targetPort、selector 是否匹配。Service 没 endpoint 时，Ingress 往往只能返回 502/503 一类错误。

然后看 Ingress 到 Pod 的连接。Pod 是否 ready，应用是否监听 targetPort，协议是否一致。常见问题包括 Ingress 用 HTTP 连后端，但后端要求 HTTPS；gRPC 配置缺失；WebSocket/长连接超时太短；后端 keepalive 或 header 大小限制不匹配；NetworkPolicy 阻断 Ingress Controller 到 Pod。

504 更要看超时链路。入口层 proxy_read_timeout、upstream timeout、应用处理时间、下游数据库/RPC 延迟、连接池等待、Pod CPU throttling、GC、重试风暴都会导致超时。排查证据包括 Ingress Controller access/error log、upstream response time、Service endpoint、Pod 日志、应用 trace、下游依赖指标和发布变更时间线。

## 240. 如何排查容器被 OOMKilled？

可以先这样答：OOMKilled 排查要先确认是容器级 OOM、节点内存压力驱逐，还是应用主动退出。看 Pod 的 `lastState.terminated.reason`、exit code、restart count、events、kubelet 日志和节点 memory pressure。容器级 OOM 通常会看到 reason 为 OOMKilled，exit code 常见 137；Evicted 则是另一条链路。

确认后看资源配置。检查 memory request、memory limit、QoS class、LimitRange、ResourceQuota、VPA 建议、最近是否改过镜像或配置。limit 太低会直接杀容器；request 太低会让节点过度装箱，导致节点压力上升。还要看 sidecar，总 Pod 内存不是只有主应用。

再看应用内存来源。JVM 要看 heap、metaspace、direct buffer、thread stack；Go 要看 heap、goroutine、GC、mmap、cgo；Node.js 要看 V8 heap 和 native memory；还要看文件页缓存、大响应 buffer、日志、压缩、TLS、连接数、批量查询和本地缓存。只看应用 heap 正常，不能证明容器不会 OOM。

最后按时间线关联业务。内存曲线上升发生在发布后、流量高峰、定时任务、配置变更还是依赖异常后？OOM 前是否有 GC 变慢、CPU throttling、请求堆积、readiness 失败？修复手段可能是调高 limit、降低并发、限制缓存、分页查询、修内存泄漏、调整 GC、拆分任务或让 VPA 先给建议。不要只把 limit 调大后就结束排查。

## 241. 如何设计生产集群的故障域和调度约束？

可以先这样答：生产集群的故障域设计要先定义哪些故障必须被隔离：单 Pod、单节点、单机架、单可用区、单节点池、单集群、单地域。然后用 Kubernetes 的调度约束把副本分散到这些故障域里，而不是只追求副本数看起来足够。

最常用的工具是 topologySpreadConstraints、podAntiAffinity、nodeAffinity、nodeSelector、taints/tolerations 和 PodDisruptionBudget。topologySpreadConstraints 可以按 zone、hostname、node pool 等拓扑键控制副本分布；反亲和可以避免同一服务多个副本落在同一节点；污点容忍可以把系统组件、GPU、存储或高优先级业务隔离到专用节点池。

还要把调度约束和容量模型放在一起。约束太松，副本可能集中在一个故障域；约束太严，Pod 会 Pending，发布和扩容失败。比如三副本服务跨三可用区部署，如果某个可用区容量不足，拓扑约束可能阻止新 Pod 调度。Cluster Autoscaler、节点池规格、预留容量和 PDB 都要一起设计。

生产设计还要考虑流量和数据。跨可用区流量有延迟和成本，拓扑感知路由、本地流量策略、存储拓扑、数据库主从、缓存预热都会影响故障域选择。可观测性上要按 zone/nodepool/node/service 展开 SLO、endpoint 分布、Pod Pending、调度失败原因和 PDB blocked。好的调度约束不是写得越多越好，而是能清楚表达“哪些故障发生时，服务仍然有足够容量”。
