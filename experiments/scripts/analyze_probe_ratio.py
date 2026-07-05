import argparse
import csv
import json
import os
from collections import Counter


# main parses command-line options and runs the script workflow.
def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--recovery", required=True)
    parser.add_argument("--trace", required=True)
    parser.add_argument("--out", required=True)
    parser.add_argument("--probing-port", default="7002")
    parser.add_argument("--destination", default="user-service")
    parser.add_argument("--window-padding-ms", type=int, default=1000)
    parser.add_argument("--max-probe-ratio", type=float, default=0.10)
    args = parser.parse_args()

    probing_rows = read_probing_rows(args.recovery, args.probing_port)
    if not probing_rows:
        raise SystemExit(f"no PROBING rows found in {args.recovery} for port {args.probing_port}")

    start_ms = min(row["timestamp_unix_ms"] for row in probing_rows) - args.window_padding_ms
    end_ms = max(row["timestamp_unix_ms"] for row in probing_rows) + args.window_padding_ms
    trace_rows = read_trace_rows(args.trace, args.destination, start_ms, end_ms)
    if not trace_rows:
        raise SystemExit(
            f"no trace rows found for destination={args.destination} between {start_ms} and {end_ms}; "
            "increase POST_DURATION or check frontend-adaptive --trace-log"
        )

    total = len(trace_rows)
    probing = [row for row in trace_rows if endpoint_port(row.get("upstream", "")) == args.probing_port]
    by_upstream = Counter(row.get("upstream", "unknown") for row in trace_rows)
    ratio = len(probing) / max(total, 1)

    summary = {
        "recovery_path": args.recovery,
        "trace_path": args.trace,
        "probing_port": args.probing_port,
        "destination": args.destination,
        "probing_window_start_unix_ms": start_ms,
        "probing_window_end_unix_ms": end_ms,
        "probing_state_rows": len(probing_rows),
        "trace_rows_in_window": total,
        "probing_trace_rows": len(probing),
        "probe_ratio": round(ratio, 6),
        "max_expected_probe_ratio": args.max_probe_ratio,
        "within_expected_bound": ratio <= args.max_probe_ratio,
        "trace_rows_by_upstream": dict(by_upstream),
        "conclusion": conclusion(ratio, args.max_probe_ratio),
    }
    os.makedirs(os.path.dirname(args.out), exist_ok=True)
    with open(args.out, "w", encoding="utf-8") as f:
        json.dump(summary, f, indent=2, sort_keys=True)
        f.write("\n")

    print(json.dumps(summary, indent=2, sort_keys=True))
    if ratio > args.max_probe_ratio:
        raise SystemExit(
            f"probe ratio {ratio:.4f} exceeds expected bound {args.max_probe_ratio:.4f}; "
            "PROBING endpoint may be receiving normal traffic"
        )


# read_probing_rows loads probing rows rows from disk for the analysis stage.
def read_probing_rows(path, probing_port):
    rows = []
    with open(path, newline="", encoding="utf-8") as f:
        reader = csv.DictReader(f)
        for row in reader:
            if row.get("state") != "PROBING":
                continue
            if endpoint_port(row.get("endpoint", "")) != probing_port:
                continue
            rows.append({
                "timestamp_unix_ms": int(row["timestamp_unix_ms"]),
                "endpoint": row.get("endpoint", ""),
                "slow_score": parse_float(row.get("slow_score", "")),
            })
    return rows


# read_trace_rows loads trace rows rows from disk for the analysis stage.
def read_trace_rows(path, destination, start_ms, end_ms):
    rows = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            if not line.strip():
                continue
            row = json.loads(line)
            if destination and row.get("destination") != destination:
                continue
            timestamp = int(row.get("timestamp_unix_ms") or 0)
            if start_ms <= timestamp <= end_ms:
                rows.append(row)
    return rows


# endpoint_port keeps the endpoint port helper near the workflow that consumes its formatted output.
def endpoint_port(address):
    if not address or ":" not in address:
        return ""
    return address.rsplit(":", 1)[-1]


# parse_float converts float values from user input or result files.
def parse_float(raw):
    try:
        return float(raw)
    except (TypeError, ValueError):
        return 0.0


# conclusion keeps the conclusion helper near the workflow that consumes its formatted output.
def conclusion(ratio, expected):
    if ratio <= expected:
        return "PASS: PROBING endpoint traffic stayed within the configured probe-ratio bound."
    return "FAIL: PROBING endpoint received more than the expected probe-ratio bound."


if __name__ == "__main__":
    main()
