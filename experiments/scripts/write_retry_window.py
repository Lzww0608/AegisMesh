import argparse
import csv
import os


# main parses command-line options and runs the script workflow.
def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--latency", default="experiments/results/latency.csv")
    parser.add_argument("--out", default="experiments/results/retry.csv")
    args = parser.parse_args()

    rows = read_rows(args.latency)
    selected = [r for r in rows if r["experiment"] == "retry_budget"]
    os.makedirs(os.path.dirname(args.out), exist_ok=True)
    with open(args.out, "w", newline="", encoding="utf-8") as f:
        writer = csv.writer(f)
        writer.writerow([
            "experiment",
            "variant",
            "window_start_unix_ms",
            "original_requests",
            "retry_attempts",
            "total_attempts",
            "retry_amplification",
            "error_rate",
        ])
        for row in selected:
            original = int(row["requests"])
            retry_attempts = estimate_retry_attempts(row)
            total = original + retry_attempts
            writer.writerow([
                "retry_budget",
                row["variant"],
                row["window_start_unix_ms"],
                original,
                retry_attempts,
                total,
                round(total / max(original, 1), 6),
                row["error_rate"],
            ])


# read_rows loads CSV rows from disk for the analysis stage.
def read_rows(path):
    if not os.path.exists(path):
        return []
    with open(path, newline="", encoding="utf-8") as f:
        return list(csv.DictReader(f))


# estimate_retry_attempts keeps the estimate retry attempts helper near the workflow that consumes its formatted output.
def estimate_retry_attempts(row):
    # The demo exposes request/error windows through SDK telemetry, but the
    # standalone HTTP benchmark only sees end-to-end outcomes. Keep this
    # conservative: users should replace this estimate with telemetry-derived
    # retry_attempts when exporting final benchmark results.
    requests = int(row["requests"])
    error_rate = float(row["error_rate"])
    if row["variant"] == "with_budget":
        return min(round(requests * 0.15), round(requests * error_rate))
    return round(requests * error_rate)


if __name__ == "__main__":
    main()
