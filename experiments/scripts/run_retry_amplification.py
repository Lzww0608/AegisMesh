import argparse
import csv
import os
import re
import subprocess
import sys
import urllib.request


REQUESTS_METRIC = "aegis_rpc_requests_total"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--without-url", required=True)
    parser.add_argument("--with-url", required=True)
    parser.add_argument("--without-metrics", default="http://127.0.0.1:8084/metrics")
    parser.add_argument("--with-metrics", default="http://127.0.0.1:8086/metrics")
    parser.add_argument("--requests", type=int, default=1000)
    parser.add_argument("--concurrency", type=int, default=32)
    parser.add_argument("--latency-out", default="experiments/results/latency.csv")
    parser.add_argument("--retry-out", default="experiments/results/retry.csv")
    args = parser.parse_args()

    run_variant(
        experiment="retry_budget",
        variant="without_budget",
        url=args.without_url,
        metrics_url=args.without_metrics,
        requests=args.requests,
        concurrency=args.concurrency,
        latency_out=args.latency_out,
        retry_out=args.retry_out,
    )
    run_variant(
        experiment="retry_budget",
        variant="with_budget",
        url=args.with_url,
        metrics_url=args.with_metrics,
        requests=args.requests,
        concurrency=args.concurrency,
        latency_out=args.latency_out,
        retry_out=args.retry_out,
    )


def run_variant(experiment, variant, url, metrics_url, requests, concurrency, latency_out, retry_out):
    before = scrape_attempts(metrics_url)
    subprocess.run(
        [
            sys.executable,
            "experiments/scripts/run_http_benchmark.py",
            "--url",
            url,
            "--requests",
            str(requests),
            "--concurrency",
            str(concurrency),
            "--experiment",
            experiment,
            "--variant",
            variant,
            "--latency-out",
            latency_out,
        ],
        check=True,
    )
    after = scrape_attempts(metrics_url)
    attempts = max(after - before, 0)
    latest = latest_latency_row(latency_out, experiment, variant)
    write_retry_row(
        retry_out,
        {
            "experiment": experiment,
            "variant": variant,
            "window_start_unix_ms": latest.get("window_start_unix_ms", ""),
            "original_requests": str(requests),
            "retry_attempts": str(max(attempts - requests, 0)),
            "total_attempts": str(attempts),
            "retry_amplification": f"{attempts / max(requests, 1):.6f}",
            "error_rate": latest.get("error_rate", ""),
        },
    )


def scrape_attempts(metrics_url):
    text = urllib.request.urlopen(metrics_url, timeout=5).read().decode("utf-8")
    total = 0.0
    for line in text.splitlines():
        if not line.startswith(REQUESTS_METRIC):
            continue
        if 'destination="retry-user-service"' not in line:
            continue
        match = re.search(r"}\s+([0-9.eE+-]+)$", line)
        if match:
            total += float(match.group(1))
    return int(total)


def latest_latency_row(path, experiment, variant):
    if not os.path.exists(path):
        return {}
    with open(path, newline="", encoding="utf-8") as f:
        rows = [
            row
            for row in csv.DictReader(f)
            if row.get("experiment") == experiment and row.get("variant") == variant
        ]
    return rows[-1] if rows else {}


def write_retry_row(path, row):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    exists = os.path.exists(path)
    with open(path, "a", newline="", encoding="utf-8") as f:
        fieldnames = [
            "experiment",
            "variant",
            "window_start_unix_ms",
            "original_requests",
            "retry_attempts",
            "total_attempts",
            "retry_amplification",
            "error_rate",
        ]
        writer = csv.DictWriter(f, fieldnames=fieldnames)
        if not exists:
            writer.writeheader()
        writer.writerow(row)


if __name__ == "__main__":
    main()
