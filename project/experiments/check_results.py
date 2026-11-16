#!/usr/bin/env python3
"""Integrity check and short summary of the Stage 10 raw CSV files.

Usage (from project/):
    python3 experiments/check_results.py experiments/results/raw_data_matrix.csv \
        experiments/results/raw_data_stress.csv

For every raw_*.csv it checks the runner schema, the required T10.2 columns,
row counts per run, carbon/energy completeness and the weight metadata, then
prints a Markdown table with per-cell means over repetitions (descriptive
only; statistics are Stage 11). Exit code 1 if a check fails.
"""
import os
import sys

import pandas as pd

REQUIRED = ["pod_id", "node", "zone", "duration", "energy_kwh", "carbon_gco2",
            "scheduled_at", "completed_at", "sla_violated"]
CELL = ["experiment", "scenario", "strategy", "w_c", "deadline_factor"]


def check(path):
    ok = True
    df = pd.read_csv(path)
    runs_path = os.path.join(os.path.dirname(path), "runs_" + os.path.basename(path)[len("raw_"):])
    runs = pd.read_csv(runs_path)
    print(f"## {path}\n")
    print(f"- rows: {len(df)}, runs: {df.run_id.nunique()} (runs file: {len(runs)})")
    missing = [c for c in REQUIRED if c not in df.columns]
    if missing:
        print(f"- FAIL: missing columns {missing}")
        ok = False
    per_run = df.groupby("run_id").size()
    if per_run.nunique() != 1:
        print(f"- FAIL: unequal rows per run: {per_run.value_counts().to_dict()}")
        ok = False
    else:
        print(f"- rows per run: {per_run.iloc[0]}")
    if set(df.run_id) != set(runs.run_id):
        print("- FAIL: task and run files disagree on run_ids")
        ok = False
    succ = df[df.status == "succeeded"]
    print(f"- task status: {df.status.value_counts().to_dict()}")
    for col in ["carbon_gco2", "energy_kwh", "duration", "node", "zone"]:
        n = succ[col].isna().sum()
        if n:
            print(f"- FAIL: {n} succeeded tasks without {col}")
            ok = False
    plugin = df[df.strategy != "default"]
    if plugin[["w_c", "w_r", "w_p"]].isna().any().any():
        print("- FAIL: plugin runs without weight metadata")
        ok = False
    if df[df.strategy == "default"][["w_c"]].notna().any().any():
        print("- FAIL: default-scheduler rows carry plugin weights")
        ok = False
    bad = runs[(runs.strategy != "default") & (runs.decisions_delta != runs.tasks)]
    if len(bad):
        print(f"- note: decisions_total delta != tasks in {len(bad)} runs: {bad.run_id.tolist()}")
    fb = runs[(runs.strategy != "default") & (runs.fallback_delta > 0)]
    print(f"- runs with plugin fallback/stale events: {len(fb)}")
    print(f"- host CPU utilization during runs: mean {runs.host_cpu_util_mean.mean():.2f}, max {runs.host_cpu_util_max.max():.2f}")

    runs = runs.copy()
    runs["w_c"] = runs["w_c"].fillna(-1)
    g = runs.groupby(CELL, dropna=False)
    t = g.agg(n=("run_id", "count"),
              carbon_g=("total_carbon_gco2", "mean"),
              carbon_sd=("total_carbon_gco2", "std"),
              energy_kwh=("total_energy_kwh", "mean"),
              turnaround_s=("mean_turnaround_s", "mean"),
              svr=("svr", "mean"),
              sched_p50_ms=("sched_latency_p50_s", lambda s: 1000 * s.mean()),
              attempt_p99_ms=("sched_attempt_p99_s", lambda s: 1000 * s.mean()),
              north=("zone_shares", lambda s: s.map(share("north")).mean()))
    t = t.reset_index()
    t["w_c"] = t["w_c"].map(lambda v: "" if v == -1 else f"{v:g}")
    print()
    print("| " + " | ".join(t.columns) + " |")
    print("|" + "---|" * len(t.columns))
    for _, r in t.iterrows():
        cells = []
        for c in t.columns:
            v = r[c]
            cells.append(f"{v:.4g}" if isinstance(v, float) else str(v))
        print("| " + " | ".join(cells) + " |")
    print()
    return ok


def share(zone):
    def f(s):
        for part in str(s).split(";"):
            z, _, v = part.partition(":")
            if z == zone:
                return float(v)
        return 0.0
    return f


if __name__ == "__main__":
    results = [check(p) for p in sys.argv[1:]]
    sys.exit(0 if all(results) else 1)
