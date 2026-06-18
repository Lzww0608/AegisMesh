# Experiment Results

This directory holds experiment outputs. The checked-in `*_schema.csv` files define columns only; they are not benchmark results.

Files produced by the scripts:

- `latency.csv`: throughput and latency windows from HTTP benchmark runs
- `recovery.csv`: time-series recovery samples from Prometheus or Controller health snapshots
- `retry.csv`: retry amplification windows
- `figures/`: summary files and optional plots generated from measured CSV files
- `probe_slo_summary.md`: measured summary for PROBING probe-ratio and absolute-SLO runs
- `runs/*/probe_ratio_summary.json` and `runs/*/absolute_slo_summary.json`: per-run probe-ratio and absolute-SLO summaries

Do not fill result rows by hand. Generate them from scripts or a real benchmark run, then record the run environment in `docs/evaluation.md`.
