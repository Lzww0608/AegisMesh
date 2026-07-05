import argparse
import csv
import shutil
from pathlib import Path


CSV_FILES = ["latency.csv", "retry.csv", "recovery.csv"]


# main parses command-line options and runs the script workflow.
def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--inputs", nargs="+", required=True)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()

    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    run_dirs = discover_run_dirs([Path(p) for p in args.inputs], out)
    for name in CSV_FILES:
        merge_csv(name, run_dirs, out / name)
    copy_latest_figures(run_dirs, out / "figures")
    print(f"merged {len(run_dirs)} run directory/directories into {out}")


# discover_run_dirs keeps the discover run dirs helper near the workflow that consumes its formatted output.
def discover_run_dirs(inputs, out):
    dirs = []
    seen = set()
    for path in inputs:
        if not path.exists():
            continue
        candidates = []
        if any((path / name).exists() for name in CSV_FILES):
            candidates.append(path)
        candidates.extend(child for child in sorted(path.iterdir()) if child.is_dir() and any((child / name).exists() for name in CSV_FILES))
        for candidate in candidates:
            resolved = candidate.resolve()
            if resolved == out.resolve() or resolved in seen:
                continue
            seen.add(resolved)
            dirs.append(candidate)
    return dirs


# merge_csv merges merge csv files into the combined result set.
def merge_csv(name, run_dirs, out_path):
    rows = []
    fieldnames = None
    for run_dir in run_dirs:
        path = run_dir / name
        if not path.exists():
            continue
        run_id = run_dir.name if run_dir.name != "results" else "legacy"
        with path.open(newline="", encoding="utf-8") as f:
            reader = csv.DictReader(f)
            if fieldnames is None:
                fieldnames = ["run_id"] + list(reader.fieldnames or [])
            for row in reader:
                row = {"run_id": run_id, **row}
                rows.append(row)
    if fieldnames is None:
        return
    with out_path.open("w", newline="", encoding="utf-8") as f:
        writer = csv.DictWriter(f, fieldnames=fieldnames)
        writer.writeheader()
        writer.writerows(rows)


# copy_latest_figures keeps the copy latest figures helper near the workflow that consumes its formatted output.
def copy_latest_figures(run_dirs, out_dir):
    for run_dir in reversed(run_dirs):
        figures = run_dir / "figures"
        if figures.exists():
            if out_dir.exists():
                shutil.rmtree(out_dir)
            shutil.copytree(figures, out_dir)
            return


if __name__ == "__main__":
    main()
