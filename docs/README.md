# AegisMesh Documentation

This directory holds the detailed project material. Keep the root `README.md` short and use these files for the full explanation, reproduction steps, and interview preparation.

## Reading Order

1. [project_report.md](project_report.md) - full project report: problem, design, mechanisms, measured results, reproducibility, and project boundaries.
2. [evaluation.md](evaluation.md) - measurement log: run environment, checked-in result sources, metric definitions, and reporting guardrails.
3. [experiments.md](experiments.md) - reproduction runbook for the Docker benchmark matrix, recovery-state run, PROBING probe-ratio run, and absolute-SLO run.
4. [resume.md](resume.md) - Chinese and English resume bullets, performance evidence, interview talking points, and deeper defense boundaries.
5. [../interview/README.md](../interview/README.md) - systematic interview question bank for project positioning, RPC, routing, slow-score design, retry, telemetry, verifier, eBPF, and production discussion.

## Result Sources

- Main benchmark evidence: [project_report.md](project_report.md) and [evaluation.md](evaluation.md), which record the merged result checker run for `experiments/results/combined`.
- Result checker command: `python experiments/scripts/check_results.py --results experiments/results/combined`.
- Supplemental PROBING and absolute-SLO summary: `experiments/results/probe_slo_summary.md`.
- Reproduction commands and accepted thresholds: [experiments.md](experiments.md).

## What Goes Where

- Put high-level positioning and the strongest checked-in evidence in the root `README.md`.
- Put complete experiment tables, caveats, and reproduction detail in [project_report.md](project_report.md), [evaluation.md](evaluation.md), and [experiments.md](experiments.md).
- Put resume phrasing, interview framing, and boundary statements in [resume.md](resume.md).
- Put long-form question-and-answer material in `../interview/` or `../tech_interview_qna/`.
