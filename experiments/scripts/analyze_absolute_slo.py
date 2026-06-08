import argparse
import csv
import json
import os
from collections import Counter, defaultdict


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--recovery", required=True)
    parser.add_argument("--out", required=True)
    parser.add_argument("--min-score", type=float, default=1.0)
    parser.add_argument("--expect-state", action="append", default=["DEGRADED", "EJECTED", "PROBING"])
    args = parser.parse_args()

    rows = read_rows(args.recovery)
    if not rows:
        raise SystemExit(f"no recovery rows found in {args.recovery}")

    states = Counter(row["state"] for row in rows)
    by_endpoint = defaultdict(list)
    for row in rows:
        by_endpoint[row["endpoint"]].append(row)

    endpoints = {}
    max_score = 0.0
    for endpoint, endpoint_rows in by_endpoint.items():
        endpoint_max = max(row["slow_score"] for row in endpoint_rows)
        endpoint_states = sorted({row["state"] for row in endpoint_rows})
        max_score = max(max_score, endpoint_max)
        endpoints[endpoint] = {
            "rows": len(endpoint_rows),
            "max_slow_score": round(endpoint_max, 6),
            "states": endpoint_states,
        }

    observed_expected = sorted(state for state in args.expect_state if states[state] > 0)
    summary = {
        "recovery_path": args.recovery,
        "rows": len(rows),
        "states": dict(states),
        "endpoints": endpoints,
        "max_slow_score": round(max_score, 6),
        "min_expected_score": args.min_score,
        "expected_states_observed": observed_expected,
        "score_pass": max_score >= args.min_score,
        "state_pass": len(observed_expected) > 0,
    }
    summary["conclusion"] = conclusion(summary)

    os.makedirs(os.path.dirname(args.out), exist_ok=True)
    with open(args.out, "w", encoding="utf-8") as f:
        json.dump(summary, f, indent=2, sort_keys=True)
        f.write("\n")

    print(json.dumps(summary, indent=2, sort_keys=True))
    if not summary["score_pass"]:
        raise SystemExit(f"max slow_score {max_score:.6f} is below expected minimum {args.min_score:.6f}")
    if not summary["state_pass"]:
        raise SystemExit(f"no expected degraded/ejected/probing state observed in {args.recovery}")


def read_rows(path):
    rows = []
    with open(path, newline="", encoding="utf-8") as f:
        reader = csv.DictReader(f)
        for row in reader:
            endpoint = row.get("endpoint", "")
            state = row.get("state", "")
            if not endpoint or not state:
                continue
            rows.append({
                "timestamp_unix_ms": int(row["timestamp_unix_ms"]),
                "endpoint": endpoint,
                "state": state,
                "slow_score": parse_float(row.get("slow_score", "")),
            })
    return rows


def parse_float(raw):
    try:
        return float(raw)
    except (TypeError, ValueError):
        return 0.0


def conclusion(summary):
    if summary["score_pass"] and summary["state_pass"]:
        return "PASS: absolute SLO scoring produced elevated slow_score and a health-state reaction."
    if not summary["score_pass"]:
        return "FAIL: slow_score did not rise above the expected threshold."
    return "FAIL: slow_score rose, but no degraded/ejected/probing state was observed."


if __name__ == "__main__":
    main()
