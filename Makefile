SHELL := /usr/bin/env bash

COMPOSE := docker compose
DEMO_COMPOSE := -f docker-compose.demo.yml
OBS_COMPOSE := -f docker-compose.demo.yml -f docker-compose.observability.yml
EXP_COMPOSE := -f docker-compose.demo.yml -f docker-compose.experiments.yml
EXP_OBS_COMPOSE := -f docker-compose.demo.yml -f docker-compose.experiments.yml -f docker-compose.observability.yml

TARGET ?= aegis-user-b
TARGETS ?= aegis-user-a aegis-user-b
DEVICE ?= eth0
DELAY ?= 200ms
JITTER ?= 50ms
LOSS ?= 2
CPUS ?= 0.25
REQUESTS ?= 200
CONCURRENCY ?= 16
REPETITIONS ?= 5
PRE_DURATION ?= 15s
FAULT_DURATION ?= 60s
POST_DURATION ?= 30s
RECOVERY_DURATION ?= 90s
RECOVERY_INTERVAL ?= 1s
FRONTEND_URL ?= http://127.0.0.1:8080/checkout
URL_ADAPTIVE ?= http://127.0.0.1:8083/checkout
TRACE_LOG ?= experiments/traces/frontend-adaptive.jsonl
PROBING_PORT ?= 7002
MAX_PROBE_RATIO ?= 0.10
VARIANT ?= with_absolute_slo
MIN_SCORE ?= 1.0
RESULTS_DIR ?= experiments/results
RUNS_DIR ?= experiments/results/runs
COMBINED_DIR ?= experiments/results/combined
RUN_ID ?= $(shell date +%Y%m%d-%H%M%S)
DSB_CONFIG ?= experiments/deathstarbench/social-network.yaml
DSB_REPO_DIR ?=
DSB_RUN_DIR ?= experiments/results/runs/deathstarbench-social-network-$(RUN_ID)
DSB_STARTUP_TIMEOUT ?= 90s

.PHONY: demo-up demo-down observability-up dashboard experiments-up experiments-down load inject-delay inject-loss inject-cpu reset-faults bench bench-required bench-probe-ratio bench-absolute-slo summarize-probe-slo deathstarbench-plan deathstarbench-run check-deathstarbench-run check-results record-recovery report test test-race microbench microbench-baseline microbench-baseline-full microbench-race microbench-quick microbench-adaptive

demo-up:
	$(COMPOSE) $(DEMO_COMPOSE) up -d --build

demo-down:
	$(COMPOSE) $(OBS_COMPOSE) down --remove-orphans

observability-up:
	$(COMPOSE) $(OBS_COMPOSE) up -d --build

dashboard: observability-up
	@echo "Prometheus: http://127.0.0.1:9090"
	@echo "Grafana:    http://127.0.0.1:3000"

experiments-up:
	$(COMPOSE) $(EXP_COMPOSE) up -d --build

experiments-down:
	$(COMPOSE) $(EXP_OBS_COMPOSE) down --remove-orphans

load:
	REQUESTS=$(REQUESTS) CONCURRENCY=$(CONCURRENCY) FRONTEND_URL=$(FRONTEND_URL) RESULTS_DIR=$(RESULTS_DIR) bash experiments/scripts/run_baseline.sh

inject-delay:
	go run ./cmd/fault-injector --kind delay --container $(TARGET) --device $(DEVICE) --delay $(DELAY) --jitter $(JITTER) --execute

inject-loss:
	go run ./cmd/fault-injector --kind loss --container $(TARGET) --device $(DEVICE) --loss-percent $(LOSS) --execute

inject-cpu:
	go run ./cmd/fault-injector --kind cpu --container $(TARGET) --cpus $(CPUS) --execute

reset-faults:
	TARGET=$(TARGET) DEVICE=$(DEVICE) bash scripts/reset_faults.sh

bench:
	RESULTS_DIR=$(RESULTS_DIR) REQUESTS=$(REQUESTS) CONCURRENCY=$(CONCURRENCY) bash experiments/scripts/run_baseline.sh
	RESULTS_DIR=$(RESULTS_DIR) REQUESTS=$(REQUESTS) CONCURRENCY=$(CONCURRENCY) TARGET=$(TARGET) DELAY=$(DELAY) JITTER=$(JITTER) bash experiments/scripts/run_slow_fault.sh
	RESULTS_DIR=$(RESULTS_DIR) REQUESTS=$(REQUESTS) CONCURRENCY=$(CONCURRENCY) bash experiments/scripts/run_retry_budget.sh

bench-required:
	RESULTS_DIR=$(RESULTS_DIR) REQUESTS=$(REQUESTS) CONCURRENCY=$(CONCURRENCY) TARGET=$(TARGET) DEVICE=$(DEVICE) DELAY=$(DELAY) JITTER=$(JITTER) LOSS=$(LOSS) CPUS=$(CPUS) bash experiments/scripts/run_required_experiments.sh

bench-retry-repeat:
	RUN_ID=$(RUN_ID) RUNS_DIR=$(RUNS_DIR) REQUESTS=$(REQUESTS) CONCURRENCY=$(CONCURRENCY) REPETITIONS=$(REPETITIONS) bash experiments/scripts/run_retry_repetitions.sh

bench-recovery-state:
	RUN_ID=$(RUN_ID) RUNS_DIR=$(RUNS_DIR) REQUESTS=$(REQUESTS) CONCURRENCY=$(CONCURRENCY) TARGET=$(TARGET) DEVICE=$(DEVICE) DELAY=$(DELAY) JITTER=$(JITTER) PRE_DURATION=$(PRE_DURATION) FAULT_DURATION=$(FAULT_DURATION) POST_DURATION=$(POST_DURATION) RECOVERY_DURATION=$(RECOVERY_DURATION) RECOVERY_INTERVAL=$(RECOVERY_INTERVAL) bash experiments/scripts/run_recovery_state_experiment.sh

bench-probe-ratio:
	RUN_ID=$(RUN_ID) RUNS_DIR=$(RUNS_DIR) CONCURRENCY=$(CONCURRENCY) TARGET=$(TARGET) DEVICE=$(DEVICE) DELAY=$(DELAY) JITTER=$(JITTER) PRE_DURATION=$(PRE_DURATION) FAULT_DURATION=$(FAULT_DURATION) POST_DURATION=$(POST_DURATION) RECOVERY_DURATION=$(RECOVERY_DURATION) RECOVERY_INTERVAL=$(RECOVERY_INTERVAL) URL_ADAPTIVE=$(URL_ADAPTIVE) TRACE_LOG=$(TRACE_LOG) PROBING_PORT=$(PROBING_PORT) MAX_PROBE_RATIO=$(MAX_PROBE_RATIO) bash experiments/scripts/run_probe_ratio_experiment.sh

bench-absolute-slo:
	RUN_ID=$(RUN_ID) RUNS_DIR=$(RUNS_DIR) VARIANT=$(VARIANT) CONCURRENCY=$(CONCURRENCY) TARGETS="$(TARGETS)" DEVICE=$(DEVICE) DELAY=$(DELAY) JITTER=$(JITTER) PRE_DURATION=$(PRE_DURATION) FAULT_DURATION=$(FAULT_DURATION) POST_DURATION=$(POST_DURATION) RECOVERY_DURATION=$(RECOVERY_DURATION) RECOVERY_INTERVAL=$(RECOVERY_INTERVAL) URL_ADAPTIVE=$(URL_ADAPTIVE) MIN_SCORE=$(MIN_SCORE) bash experiments/scripts/run_absolute_slo_experiment.sh

summarize-probe-slo:
	python experiments/scripts/summarize_probe_slo.py --runs-dir $(RUNS_DIR) --out experiments/results/probe_slo_summary.md

bench-single-machine:
	RUN_ID=$(RUN_ID) RUNS_DIR=$(RUNS_DIR) REQUESTS=$(REQUESTS) CONCURRENCY=$(CONCURRENCY) TARGET=$(TARGET) DEVICE=$(DEVICE) DELAY=$(DELAY) JITTER=$(JITTER) LOSS=$(LOSS) CPUS=$(CPUS) bash experiments/scripts/run_single_machine_experiments.sh


deathstarbench-plan:
	go run ./cmd/deathstarbench-adapter --config $(DSB_CONFIG)

deathstarbench-run:
	@test -n "$(DSB_REPO_DIR)" || (echo "usage: make deathstarbench-run DSB_REPO_DIR=/path/to/DeathStarBench" && exit 1)
	go run ./cmd/deathstarbench-adapter --config $(DSB_CONFIG) --run --repo-dir "$(DSB_REPO_DIR)" --out "$(DSB_RUN_DIR)" --startup-timeout $(DSB_STARTUP_TIMEOUT)

check-deathstarbench-run:
	go run ./cmd/deathstarbench-adapter --validate-run "$(DSB_RUN_DIR)"

check-results:
	python experiments/scripts/check_results.py --results $(RESULTS_DIR)

merge-results:
	python experiments/scripts/merge_results.py --inputs experiments/results $(RUNS_DIR) --out $(COMBINED_DIR)

record-recovery:
	go run ./cmd/experiment-recorder --out $(RESULTS_DIR)/recovery.csv --duration 30s --interval 1s

report:
	python experiments/notebooks/plot_latency.py --results $(RESULTS_DIR) --out $(RESULTS_DIR)/figures

test:
	go test ./...

test-race:
	go test -race ./...

microbench:
	bash scripts/run_microbench.sh capture

microbench-baseline:
	BENCH_COUNT=10 CAPTURE_JOBS=8 PER_JOB_GOMAXPROCS=4 bash scripts/run_microbench.sh capture main

microbench-baseline-full:
	BENCH_COUNT=10 CAPTURE_JOBS=16 PER_JOB_GOMAXPROCS=2 bash scripts/run_microbench.sh capture main
	BENCH_COUNT=10 CAPTURE_JOBS=16 PER_JOB_GOMAXPROCS=2 bash scripts/run_microbench.sh capture-snapshots main

microbench-quick:
	BENCH_COUNT=1 BENCHTIME=50ms bash scripts/run_microbench.sh capture quick

microbench-race:
	bash scripts/run_microbench.sh race

microbench-adaptive:
	@test -n "$(OLD)" && test -n "$(NEW)" || (echo "usage: make microbench-adaptive OLD=benchmarks/baseline/adaptive_gomax1_main.txt NEW=/tmp/adaptive.txt" && exit 1)
	bash scripts/run_microbench.sh adaptive "$(OLD)" "$(NEW)"
