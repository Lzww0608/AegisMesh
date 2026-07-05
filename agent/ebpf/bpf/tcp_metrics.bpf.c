// SPDX-License-Identifier: GPL-2.0
// AegisMesh TCP telemetry BPF program.

#include "vmlinux.h"
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_endian.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#define AEGIS_EVENT_RETRANSMIT 1
#define AEGIS_EVENT_CONNECT 2
#define AEGIS_TASK_COMM_LEN 16

struct tcp_event {
    __u64 timestamp_ns;
    __u32 pid;
    __u32 type;
    __u16 family;
    __u16 sport;
    __u16 dport;
    __u16 pad;
    __s32 ret;
    __u32 saddr_v4;
    __u32 daddr_v4;
    __u64 connect_latency_ns;
    char comm[AEGIS_TASK_COMM_LEN];
};

struct connect_start {
    __u64 timestamp_ns;
    __u32 pid;
    __u16 family;
    __u16 sport;
    __u16 dport;
    __u16 pad;
    __u32 saddr_v4;
    __u32 daddr_v4;
    char comm[AEGIS_TASK_COMM_LEN];
};

struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 1 << 20);
} events SEC(".maps");

struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 16384);
    __type(key, __u64);
    __type(value, struct connect_start);
} connect_starts SEC(".maps");

// read_family reads the socket address family through CO-RE so layout offsets stay kernel-version safe.
static __always_inline __u16 read_family(struct sock *sk)
{
    return BPF_CORE_READ(sk, __sk_common.skc_family);
}

// read_sport reads the local TCP port from struct sock without dereferencing user memory.
static __always_inline __u16 read_sport(struct sock *sk)
{
    return BPF_CORE_READ(sk, __sk_common.skc_num);
}

// read_dport reads and byte-swaps the remote TCP port into host order for emitted events.
static __always_inline __u16 read_dport(struct sock *sk)
{
    __u16 dport = BPF_CORE_READ(sk, __sk_common.skc_dport);
    return bpf_ntohs(dport);
}

// read_saddr_v4 reads the IPv4 source address captured in the socket common fields.
static __always_inline __u32 read_saddr_v4(struct sock *sk)
{
    return BPF_CORE_READ(sk, __sk_common.skc_rcv_saddr);
}

// read_daddr_v4 reads the IPv4 destination address captured in the socket common fields.
static __always_inline __u32 read_daddr_v4(struct sock *sk)
{
    return BPF_CORE_READ(sk, __sk_common.skc_daddr);
}

// fill_tuple copies the stable five-tuple fields shared by retransmit and connect events.
static __always_inline void fill_tuple(struct tcp_event *event, struct sock *sk)
{
    event->family = read_family(sk);
    event->sport = read_sport(sk);
    event->dport = read_dport(sk);
    event->saddr_v4 = read_saddr_v4(sk);
    event->daddr_v4 = read_daddr_v4(sk);
}

// fill_start_tuple snapshots connect-attempt identity before the kretprobe observes the result.
static __always_inline void fill_start_tuple(struct connect_start *start, struct sock *sk,
                                             struct sockaddr_in *addr)
{
    start->family = read_family(sk);
    start->sport = read_sport(sk);
    start->saddr_v4 = read_saddr_v4(sk);
    start->dport = bpf_ntohs(BPF_CORE_READ(addr, sin_port));
    start->daddr_v4 = BPF_CORE_READ(addr, sin_addr.s_addr);
}

SEC("kprobe/tcp_retransmit_skb")
// aegis_tcp_retransmit emits a ring-buffer event for retransmits and drops silently under backpressure.
int BPF_KPROBE(aegis_tcp_retransmit, struct sock *sk)
{
    struct tcp_event *event;
    __u64 pid_tgid;

    if (!sk) {
        return 0;
    }

    event = bpf_ringbuf_reserve(&events, sizeof(*event), 0);
    if (!event) {
        return 0;
    }

    __builtin_memset(event, 0, sizeof(*event));
    pid_tgid = bpf_get_current_pid_tgid();
    event->timestamp_ns = bpf_ktime_get_ns();
    event->pid = pid_tgid >> 32;
    event->type = AEGIS_EVENT_RETRANSMIT;
    fill_tuple(event, sk);
    bpf_get_current_comm(event->comm, sizeof(event->comm));
    bpf_ringbuf_submit(event, 0);
    return 0;
}

SEC("kprobe/tcp_v4_connect")
// aegis_tcp_v4_connect stores connect-start metadata keyed by pid/tgid until the return probe runs.
int BPF_KPROBE(aegis_tcp_v4_connect, struct sock *sk, struct sockaddr *uaddr)
{
    struct connect_start start = {};
    struct sockaddr_in *addr = (struct sockaddr_in *)uaddr;
    __u64 pid_tgid;

    if (!sk || !addr) {
        return 0;
    }

    pid_tgid = bpf_get_current_pid_tgid();
    start.timestamp_ns = bpf_ktime_get_ns();
    start.pid = pid_tgid >> 32;
    fill_start_tuple(&start, sk, addr);
    bpf_get_current_comm(start.comm, sizeof(start.comm));
    bpf_map_update_elem(&connect_starts, &pid_tgid, &start, BPF_ANY);
    return 0;
}

SEC("kretprobe/tcp_v4_connect")
// aegis_tcp_v4_connect_ret emits latency/error events and always clears the matching start record.
int BPF_KRETPROBE(aegis_tcp_v4_connect_ret, int ret)
{
    __u64 pid_tgid = bpf_get_current_pid_tgid();
    struct connect_start *start;
    struct tcp_event *event;
    __u64 now = bpf_ktime_get_ns();

    start = bpf_map_lookup_elem(&connect_starts, &pid_tgid);
    if (!start) {
        return 0;
    }

    event = bpf_ringbuf_reserve(&events, sizeof(*event), 0);
    if (!event) {
        bpf_map_delete_elem(&connect_starts, &pid_tgid);
        return 0;
    }

    __builtin_memset(event, 0, sizeof(*event));
    event->timestamp_ns = now;
    event->pid = start->pid;
    event->type = AEGIS_EVENT_CONNECT;
    event->family = start->family;
    event->sport = start->sport;
    event->dport = start->dport;
    event->ret = ret;
    event->saddr_v4 = start->saddr_v4;
    event->daddr_v4 = start->daddr_v4;
    event->connect_latency_ns = now - start->timestamp_ns;
    __builtin_memcpy(event->comm, start->comm, sizeof(event->comm));

    bpf_ringbuf_submit(event, 0);
    bpf_map_delete_elem(&connect_starts, &pid_tgid);
    return 0;
}

char LICENSE[] SEC("license") = "GPL";
