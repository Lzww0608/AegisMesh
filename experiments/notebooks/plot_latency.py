import argparse
import csv
import os
from pathlib import Path


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--results", default="experiments/results")
    parser.add_argument("--out", default="experiments/results/figures")
    args = parser.parse_args()

    results_dir = Path(args.results)
    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)

    latency_rows = read_rows(results_dir / "latency.csv")
    retry_rows = read_rows(results_dir / "retry.csv")

    write_summary(out_dir / "summary.md", latency_rows, retry_rows)
    latency_plot = out_dir / "latency_p99.png"
    try:
        import matplotlib.pyplot as plt
    except ImportError:
        if latency_plot.exists():
            latency_plot.unlink()
        print("matplotlib is not installed; wrote markdown summary only")
        return

    if latency_rows:
        plot_latency(plt, latency_rows, latency_plot)
    elif latency_plot.exists():
        latency_plot.unlink()


def read_rows(path):
    if not path.exists():
        return []
    with path.open(newline="", encoding="utf-8") as f:
        return list(csv.DictReader(f))


def write_summary(path, latency_rows, retry_rows):
    with path.open("w", encoding="utf-8") as f:
        f.write("# AegisMesh Experiment Summary\n\n")
        f.write("Generated from measured CSV files. Empty tables mean no run data was present.\n\n")
        f.write("## Latency Rows\n\n")
        f.write(f"- rows: {len(latency_rows)}\n")
        f.write("## Retry Rows\n\n")
        f.write(f"- rows: {len(retry_rows)}\n")


def plot_latency(plt, rows, path):
    points = [
        (f"{row['experiment']}:{row['variant']}", float(row["latency_p99_ms"]))
        for row in rows
        if row.get("latency_p99_ms")
    ]
    if not points:
        if path.exists():
            path.unlink()
        return
    labels = [label for label, _ in points]
    values = [value for _, value in points]
    plt.figure(figsize=(max(8, len(values) * 1.4), 4))
    plt.bar(labels, values)
    plt.ylabel("p99 latency (ms)")
    plt.xticks(rotation=30, ha="right")
    plt.tight_layout()
    plt.savefig(path)


if __name__ == "__main__":
    main()
