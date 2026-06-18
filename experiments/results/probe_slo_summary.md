# PROBING probe ratio and absolute SLO summary

## PROBING probe ratio

| Run | Probe Ratio | Bound | Pass | Trace Rows | PROBING Rows |
| --- | ---: | ---: | --- | ---: | ---: |
| `probe-ratio` | 0.002177 | 0.1000 | PASS | 257258 | 9 |

## Absolute SLO

| Run | Max Slow Score | Score Pass | State Pass | States |
| --- | ---: | --- | --- | --- |
| `absolute-slo-disabled` | 0.377401 | FAIL | FAIL | HEALTHY |
| `absolute-slo-enabled` | 1.007183 | PASS | PASS | DEGRADED, HEALTHY |

## Notes for reporting

- PROBING probe ratio: report the measured `probe_ratio` and whether it stayed below the configured bound.
- Absolute SLO score: compare `without_absolute_slo` and `with_absolute_slo`; report max slow_score and whether degraded/ejected/probing states appeared only with SLO scoring enabled.
