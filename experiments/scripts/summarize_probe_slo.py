import argparse
import json
import os
from pathlib import Path


# main parses command-line options and runs the script workflow.
def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--runs-dir", default="experiments/results/runs")
    parser.add_argument("--out", default="experiments/results/probe_slo_summary.md")
    args = parser.parse_args()

    runs_dir = Path(args.runs_dir)
    probe = load_summaries(runs_dir, "probe_ratio_summary.json")
    slo = load_summaries(runs_dir, "absolute_slo_summary.json")

    lines = [
        "# PROBING Probe Ratio And Absolute SLO Summary",
        "",
        "## PROBING Probe Ratio",
        "",
    ]
    if not probe:
        lines.append("No `probe_ratio_summary.json` files found.")
    else:
        lines.extend([
            "| Run | Probe Ratio | Bound | Pass | Trace Rows | PROBING Rows |",
            "| --- | ---: | ---: | --- | ---: | ---: |",
        ])
        for item in probe:
            data = item["data"]
            lines.append(
                f"| `{item['run']}` | {data.get('probe_ratio', 0):.6f} | "
                f"{data.get('max_expected_probe_ratio', 0):.4f} | "
                f"{yes_no(data.get('within_expected_bound'))} | "
                f"{data.get('trace_rows_in_window', 0)} | "
                f"{data.get('probing_state_rows', 0)} |"
            )

    lines.extend(["", "## Absolute SLO", ""])
    if not slo:
        lines.append("No `absolute_slo_summary.json` files found.")
    else:
        lines.extend([
            "| Run | Max Slow Score | Score Pass | State Pass | States |",
            "| --- | ---: | --- | --- | --- |",
        ])
        for item in slo:
            data = item["data"]
            states = ", ".join(sorted((data.get("states") or {}).keys()))
            lines.append(
                f"| `{item['run']}` | {data.get('max_slow_score', 0):.6f} | "
                f"{yes_no(data.get('score_pass'))} | "
                f"{yes_no(data.get('state_pass'))} | {states} |"
            )

    lines.extend([
        "",
        "## Suggested Conclusion Template",
        "",
        "- PROBING probe ratio: report the measured `probe_ratio` and whether it stayed below the configured bound.",
        "- Absolute SLO score: compare `without_absolute_slo` and `with_absolute_slo`; report max slow_score and whether degraded/ejected/probing states appeared only when SLO scoring was enabled.",
        "",
    ])

    out = Path(args.out)
    os.makedirs(out.parent, exist_ok=True)
    out.write_text("\n".join(lines), encoding="utf-8")
    print(f"wrote {out}")


# load_summaries keeps the load summaries helper near the workflow that consumes its formatted output.
def load_summaries(runs_dir, filename):
    out = []
    if not runs_dir.exists():
        return out
    for path in sorted(runs_dir.glob(f"**/{filename}")):
        with path.open(encoding="utf-8") as f:
            data = json.load(f)
        out.append({"run": path.parent.name, "path": str(path), "data": data})
    return out


# yes_no keeps the yes no helper near the workflow that consumes its formatted output.
def yes_no(value):
    return "PASS" if value else "FAIL"


if __name__ == "__main__":
    main()
