import argparse
import csv
import json
import statistics
from collections import defaultdict
from pathlib import Path


# main parses command-line options and runs the script workflow.
def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--results", default="experiments/results")
    parser.add_argument("--matrix", default="experiments/config/experiment_matrix.json")
    parser.add_argument("--allow-partial", action="store_true")
    args = parser.parse_args()

    results = Path(args.results)
    matrix = json.loads(Path(args.matrix).read_text(encoding="utf-8"))

    latency = read_rows(results / "latency.csv")
    retry = read_rows(results / "retry.csv")
    recovery = read_rows(results / "recovery.csv")

    expected = {(s["experiment"], s["variant"]) for s in matrix["scenarios"]}
    measured_latency = {(r["experiment"], r["variant"]) for r in latency}
    measured_retry = {(r["experiment"], r["variant"]) for r in retry}
    measured_recovery = {(r["experiment"], r["variant"]) for r in recovery}

    missing_latency = sorted(expected - measured_latency - measured_retry - measured_recovery)
    missing_retry_pairs = sorted({
        ("retry_budget", "without_budget"),
        ("retry_budget", "with_budget"),
    } - measured_retry)
    missing_recovery_pairs = sorted({
        ("single_instance_delay", "adaptive_p2c"),
        ("cpu_throttle", "slow_score"),
        ("recovery_curve", "adaptive_p2c"),
    } - measured_recovery)

    print("AegisMesh experiment result check")
    print(f"latency rows:  {len(latency)}")
    print(f"retry rows:    {len(retry)}")
    print(f"recovery rows: {len(recovery)}")
    print()

    derive_latency(latency)
    derive_retry(retry)
    derive_recovery(recovery)

    failures = []
    if missing_latency:
        failures.append("missing scenario evidence: " + ", ".join(f"{a}/{b}" for a, b in missing_latency))
    if missing_retry_pairs:
        failures.append("missing retry comparison rows: " + ", ".join(f"{a}/{b}" for a, b in missing_retry_pairs))
    if missing_recovery_pairs:
        failures.append("missing recovery rows: " + ", ".join(f"{a}/{b}" for a, b in missing_recovery_pairs))

    if failures:
        print("Gaps:")
        for failure in failures:
            print(f"- {failure}")
        if not args.allow_partial:
            raise SystemExit(1)
    else:
        print("All required experiment comparisons have evidence rows.")


# read_rows loads CSV rows from disk for the analysis stage.
def read_rows(path):
    if not path.exists():
        return []
    with path.open(newline="", encoding="utf-8") as f:
        return list(csv.DictReader(f))


# derive_latency keeps the derive latency helper near the workflow that consumes its formatted output.
def derive_latency(rows):
    by_key = group_rows(rows)
    print("Latency comparisons:")
    compare_p99(by_key, ("baseline", "no_mesh"), ("baseline", "aegismesh"))
    compare_p99(by_key, ("single_instance_delay", "round_robin"), ("single_instance_delay", "adaptive_p2c"))
    compare_p99(by_key, ("cpu_throttle", "static_threshold"), ("cpu_throttle", "slow_score"))
    compare_p99(by_key, ("packet_loss", "no_ebpf_network_score"), ("packet_loss", "ebpf_network_score"))
    print()


# compare_p99 keeps the compare p99 helper near the workflow that consumes its formatted output.
def compare_p99(by_key, left, right):
    if left not in by_key or right not in by_key:
        print(f"- {left[0]}: missing {left[1]} vs {right[1]}")
        return
    l_values = numeric_values(by_key[left], "latency_p99_ms")
    r_values = numeric_values(by_key[right], "latency_p99_ms")
    if not l_values or not r_values:
        print(f"- {left[0]}: missing numeric p99 values")
        return
    l_val = statistics.median(l_values)
    r_val = statistics.median(r_values)
    if l_val <= 0:
        print(f"- {left[0]}: invalid baseline p99 {l_val}")
        return
    change = (l_val - r_val) / l_val * 100
    print(
        f"- {left[0]}: {left[1]} median p99={l_val:.3f} ms (n={len(l_values)}), "
        f"{right[1]} median p99={r_val:.3f} ms (n={len(r_values)}), delta={change:.2f}%"
    )


# derive_retry keeps the derive retry helper near the workflow that consumes its formatted output.
def derive_retry(rows):
    by_key = group_rows(rows)
    print("Retry comparisons:")
    for key in [("retry_budget", "without_budget"), ("retry_budget", "with_budget")]:
        if key not in by_key:
            print(f"- missing {key[0]}/{key[1]}")
            continue
        originals = numeric_values(by_key[key], "original_requests")
        retry_attempts = numeric_values(by_key[key], "retry_attempts")
        totals = numeric_values(by_key[key], "total_attempts")
        amps = numeric_values(by_key[key], "retry_amplification")
        print(
            f"- {key[1]}: median retries={statistics.median(retry_attempts):.0f}, "
            f"median total={statistics.median(totals):.0f}, "
            f"median amplification={statistics.median(amps):.3f}x, n={len(amps)}"
        )
        if sum(retry_attempts) == 0 and sum(originals) > 0:
            print(f"  warning: {key[1]} did not trigger retries")
    print()


# derive_recovery keeps the derive recovery helper near the workflow that consumes its formatted output.
def derive_recovery(rows):
    print("Recovery coverage:")
    if not rows:
        print("- no recovery rows")
        print()
        return
    states = sorted({r["state"] for r in rows})
    scores = [float(r["slow_score"]) for r in rows if r.get("slow_score")]
    print(f"- states: {', '.join(states)}")
    print(f"- max slow_score: {max(scores):.6f}" if scores else "- no slow_score values")
    if states == ["HEALTHY"]:
        print("- warning: no endpoint state transition was observed")
    print()


# group_rows keeps the group rows helper near the workflow that consumes its formatted output.
def group_rows(rows):
    grouped = defaultdict(list)
    for row in rows:
        grouped[(row["experiment"], row["variant"])].append(row)
    return grouped


# numeric_values keeps the numeric values helper near the workflow that consumes its formatted output.
def numeric_values(rows, field):
    values = []
    for row in rows:
        raw = row.get(field, "")
        if raw == "":
            continue
        values.append(float(raw))
    return values


if __name__ == "__main__":
    main()
