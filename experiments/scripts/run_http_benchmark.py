import argparse
import concurrent.futures
import csv
import math
import os
import time
import urllib.error
import urllib.request


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--url", required=True)
    parser.add_argument("--requests", type=int, default=200)
    parser.add_argument("--concurrency", type=int, default=16)
    parser.add_argument("--experiment", required=True)
    parser.add_argument("--variant", required=True)
    parser.add_argument("--latency-out", required=True)
    args = parser.parse_args()

    started = int(time.time() * 1000)
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as pool:
        results = list(pool.map(lambda _: request_once(args.url), range(args.requests)))
    ended = int(time.time() * 1000)

    latencies = sorted(latency for latency, ok in results if ok)
    failures = len(results) - len(latencies)
    duration_seconds = max((ended - started) / 1000.0, 0.001)
    row = [
        args.experiment,
        args.variant,
        started,
        ended,
        len(results),
        round(len(results) / duration_seconds, 3),
        percentile(latencies, 50),
        percentile(latencies, 95),
        percentile(latencies, 99),
        round(failures / max(len(results), 1), 6),
    ]

    os.makedirs(os.path.dirname(args.latency_out), exist_ok=True)
    exists = os.path.exists(args.latency_out)
    with open(args.latency_out, "a", newline="", encoding="utf-8") as f:
        writer = csv.writer(f)
        if not exists:
            writer.writerow([
                "experiment",
                "variant",
                "window_start_unix_ms",
                "window_end_unix_ms",
                "requests",
                "throughput_rps",
                "latency_p50_ms",
                "latency_p95_ms",
                "latency_p99_ms",
                "error_rate",
            ])
        writer.writerow(row)


def request_once(url):
    started = time.perf_counter()
    try:
        with urllib.request.urlopen(url, timeout=5) as resp:
            ok = 200 <= resp.status < 500
            resp.read()
    except (urllib.error.URLError, TimeoutError):
        ok = False
    elapsed_ms = (time.perf_counter() - started) * 1000.0
    return elapsed_ms, ok


def percentile(values, pct):
    if not values:
        return ""
    if len(values) == 1:
        return round(values[0], 3)
    rank = (pct / 100.0) * (len(values) - 1)
    lower = math.floor(rank)
    upper = math.ceil(rank)
    if lower == upper:
        return round(values[int(rank)], 3)
    weight = rank - lower
    return round(values[lower] * (1 - weight) + values[upper] * weight, 3)


if __name__ == "__main__":
    main()
