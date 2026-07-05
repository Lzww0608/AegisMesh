import argparse
import time
import urllib.error
import urllib.request


# main parses command-line options and runs the script workflow.
def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--url", required=True)
    parser.add_argument("--timeout", default="60s")
    parser.add_argument("--interval", default="1s")
    args = parser.parse_args()

    deadline = time.monotonic() + parse_duration(args.timeout)
    interval = parse_duration(args.interval)
    last_error = None
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(args.url, timeout=min(interval, 5.0)) as resp:
                resp.read()
                if 200 <= resp.status < 500:
                    return
        except Exception as exc:  # Readiness probes should tolerate transient startup failures.
            last_error = exc
        time.sleep(interval)

    raise SystemExit(f"{args.url} did not become ready within {args.timeout}; last error: {last_error}")


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


if __name__ == "__main__":
    main()
