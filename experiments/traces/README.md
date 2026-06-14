# Runtime Trace Output

This directory is used by the Docker experiment stack when `demo-frontend` is started with `--trace-log`.

Generated JSONL files are runtime artifacts. Do not hand-write them. A typical check looks like this:

```bash
rm -f experiments/traces/frontend-adaptive.jsonl
make experiments-up
curl 'http://127.0.0.1:8083/checkout'
go run ./cmd/verifier --spec experiments/verifier/real-trace-smoke.yaml --traces experiments/traces/frontend-adaptive.jsonl
```
