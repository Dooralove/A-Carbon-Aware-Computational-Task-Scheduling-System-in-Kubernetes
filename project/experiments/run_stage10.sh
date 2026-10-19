#!/usr/bin/env bash
# Stage 10 experiment series (T10.3 + T10.4) on the kind stand. Run from
# project/ after the stand is up (experiments/README.md, "Стенд"):
#   bash experiments/run_stage10.sh            # everything
#   bash experiments/run_stage10.sh matrix     # T10.3 only
#   bash experiments/run_stage10.sh stress     # T10.4 only
# The runner appends and skips finished run_ids, so an interrupted series
# continues where it stopped when the same command is run again.
set -euo pipefail
RUNNER=${RUNNER:-go run ./experiments/runner}
RES=experiments/results
what=${1:-all}

if [[ $what == all || $what == matrix ]]; then
  # T10.3: 3 strategies x 2 scenarios x 5 repetitions x 50 tasks.
  $RUNNER --experiment=matrix --strategy=default,carbon-only,proposed \
    --scenario=stable,dynamic --repetitions=5 --out=$RES/raw_data_matrix.csv
fi

if [[ $what == all || $what == stress ]]; then
  # T10.4.3: volatile scenario, 3 strategies, 5 repetitions (deadline 1.5 T̂).
  $RUNNER --experiment=volatile --strategy=default,carbon-only,proposed \
    --scenario=volatile --repetitions=5 --out=$RES/raw_data_stress.csv
  # T10.4.1: w_C sweep on the volatile scenario, 3 repetitions.
  $RUNNER --experiment=weights --strategy=proposed --scenario=volatile \
    --carbon-weight=0,0.2,0.4,0.6,0.8,1.0 --repetitions=3 --out=$RES/raw_data_stress.csv
  # T10.4.2: SLA tightness 1.1 T̂ and 2.0 T̂ (1.5 T̂ is the volatile series,
  # same seeds for repetitions 1-3), 3 strategies, 3 repetitions.
  $RUNNER --experiment=sla --strategy=default,carbon-only,proposed --scenario=volatile \
    --deadline-factor=1.1,2.0 --repetitions=3 --out=$RES/raw_data_stress.csv
fi
