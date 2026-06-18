# Runtime Trace Output

The Docker experiment stack writes SDK trace JSONL here when `demo-frontend` starts with `--trace-log`.

The JSONL files are runtime artifacts. Do not hand-write them. A typical check:

```bash
rm -f experiments/traces/frontend-adaptive.jsonl
make experiments-up
curl 'http://127.0.0.1:8083/checkout'
go run ./cmd/verifier --spec experiments/verifier/real-trace-smoke.yaml --traces experiments/traces/frontend-adaptive.jsonl
```
