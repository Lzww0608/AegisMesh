# AegisMesh eBPF Agent

The eBPF agent reads kernel TCP signals and reports them to the Aegis Controller as endpoint telemetry samples. It currently emits:

- `tcp_retransmit` from `tcp_retransmit_skb`
- `connect_error` and `connect_latency` from `tcp_v4_connect`

## Linux Build

Build the BPF object on the target Linux host:

```bash
sudo apt-get install -y clang llvm bpftool make linux-tools-common
make -C agent/ebpf/bpf
```

The Makefile generates `vmlinux.h` from `/sys/kernel/btf/vmlinux` and compiles:

```text
agent/ebpf/bpf/tcp_metrics.bpf.o
```

## Run

Start the Controller first, then run the agent with endpoint mappings that bind observed `ip:port` pairs to Aegis service instances:

```bash
sudo go run ./cmd/agent \
  --controller 127.0.0.1:9000 \
  --object agent/ebpf/bpf/tcp_metrics.bpf.o \
  --endpoint-map "10.0.0.2:7001=user-service/user-a,10.0.0.3:7002=user-service/user-b"
```

For a built binary:

```bash
go build -o bin/aegis-agent ./cmd/agent
sudo ./bin/aegis-agent --controller 127.0.0.1:9000 --endpoint-map "10.0.0.2:7001=user-service/user-a"
```

The agent needs privileges to load BPF programs and attach kprobes. Use root or grant the binary the required capabilities for your kernel policy.

## Validation Checklist

1. Confirm the object builds with `make -C agent/ebpf/bpf`.
2. Start `cmd/controller` and register demo services.
3. Run `cmd/agent` with endpoint mappings matching service container or host IPs.
4. Trigger traffic and network faults.
5. Watch Controller metrics for `aegis_endpoint_slow_score` and `aegis_endpoint_state`.

On non-Linux hosts, `NewCollector` returns `ErrUnsupportedPlatform`; event decoding, aggregation, and telemetry conversion remain covered by Go tests.
