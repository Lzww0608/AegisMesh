import argparse
import concurrent.futures
import csv
import http.client
import math
import os
import threading
import time
import urllib.error
import urllib.request


# main parses command-line options and runs the script workflow.
def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--url", required=True)
    parser.add_argument("--duration", default="30s")
    parser.add_argument("--concurrency", type=int, default=32)
    parser.add_argument("--experiment", required=True)
    parser.add_argument("--variant", required=True)
    parser.add_argument("--latency-out", required=True)
    args = parser.parse_args()

    duration = parse_duration(args.duration)
    deadline = time.monotonic() + duration
    results = []
    lock = threading.Lock()
    started_ms = int(time.time() * 1000)

    # worker keeps the worker helper near the workflow that consumes its formatted output.
    def worker():
        local = []
        while time.monotonic() < deadline:
            local.append(request_once(args.url))
        with lock:
            results.extend(local)

    with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as pool:
        futures = [pool.submit(worker) for _ in range(args.concurrency)]
        for future in futures:
            future.result()

    ended_ms = int(time.time() * 1000)
    write_latency_row(args.latency_out, args.experiment, args.variant, started_ms, ended_ms, results)


# parse_duration converts duration values from user input or result files.
def parse_duration(raw):
    raw = str(raw).strip()
    if raw.endswith("ms"):
        return float(raw[:-2]) / 1000.0
    if raw.endswith("s"):
        return float(raw[:-1])
    if raw.endswith("m"):
        return float(raw[:-1]) * 60.0
    return float(raw)


# request_once keeps the request once helper near the workflow that consumes its formatted output.
def request_once(url):
    started = time.perf_counter()
    try:
        with urllib.request.urlopen(url, timeout=5) as resp:
            ok = 200 <= resp.status < 500
            resp.read()
    except (urllib.error.URLError, TimeoutError, ConnectionError, OSError, http.client.HTTPException):
        ok = False
    return (time.perf_counter() - started) * 1000.0, ok


# write_latency_row writes write latency row output for downstream analysis.
def write_latency_row(path, experiment, variant, started_ms, ended_ms, results):
    latencies = sorted(latency for latency, ok in results if ok)
    failures = len(results) - len(latencies)
    duration_seconds = max((ended_ms - started_ms) / 1000.0, 0.001)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    exists = os.path.exists(path)
    with open(path, "a", newline="", encoding="utf-8") as f:
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
        writer.writerow([
            experiment,
            variant,
            started_ms,
            ended_ms,
            len(results),
            round(len(results) / duration_seconds, 3),
            percentile(latencies, 50),
            percentile(latencies, 95),
            percentile(latencies, 99),
            round(failures / max(len(results), 1), 6),
        ])


# percentile keeps the percentile helper near the workflow that consumes its formatted output.
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
