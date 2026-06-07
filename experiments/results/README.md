# Experiment Results

This directory stores generated experiment outputs. The checked-in `*_schema.csv` files define the expected columns only; they are not benchmark results.

Generated files:

- `latency.csv`: throughput and latency windows from HTTP benchmark runs
- `recovery.csv`: time-series recovery samples from Prometheus or Controller health snapshots
- `retry.csv`: retry amplification windows
- `figures/`: plots generated from measured CSV files

Do not fill result rows by hand. Generate them from scripts or a real benchmark run and include the run environment in `docs/evaluation.md`.
