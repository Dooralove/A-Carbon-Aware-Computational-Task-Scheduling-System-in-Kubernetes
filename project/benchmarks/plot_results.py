#!/usr/bin/env python3
"""Gate 9 plots and scaling fit from the Stage 9 benchmark outputs.

usage: plot_results.py results_latency.json bench_raw.txt OUT_DIR

Writes to OUT_DIR:
  scaling_latency_vs_nodes.png  cycle p50/p95 per profile, plugin PreScore, overhead
  prescore_vs_forecast.png      PreScore at N=20 against forecast length F
  flamegraph_cycle_carbon50.png flame graph of the carbon-aware cycle CPU profile
  scaling_fit.json              least-squares fits (linear and log-log slope)
"""
import json
import math
import re
import sys
from collections import defaultdict

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt  # noqa: E402

PROFILES = ["default-scheduler", "carbon-aware-scheduler", "carbon-only-scheduler"]
LABELS = {
    "default-scheduler": "default (A)",
    "carbon-aware-scheduler": "carbon-aware (C)",
    "carbon-only-scheduler": "carbon-only (B)",
}
COLORS = {
    "default-scheduler": "#4C78A8",
    "carbon-aware-scheduler": "#2CA02C",
    "carbon-only-scheduler": "#F58518",
}


def linfit(xs, ys):
    n = len(xs)
    mx, my = sum(xs) / n, sum(ys) / n
    sxx = sum((x - mx) ** 2 for x in xs)
    sxy = sum((x - mx) * (y - my) for x, y in zip(xs, ys))
    slope = sxy / sxx
    icpt = my - slope * mx
    ss_res = sum((y - (icpt + slope * x)) ** 2 for x, y in zip(xs, ys))
    ss_tot = sum((y - my) ** 2 for y in ys)
    r2 = 1 - ss_res / ss_tot if ss_tot else 1.0
    return slope, icpt, r2


def loglog_slope(xs, ys):
    lx = [math.log(x) for x in xs]
    ly = [math.log(y) for y in ys]
    s, _, r2 = linfit(lx, ly)
    return s, r2


def fit_entry(xs, ys):
    s, i, r2 = linfit(xs, ys)
    k, kr2 = loglog_slope(xs, ys)
    return {
        "x": xs,
        "y_us": ys,
        "linear_slope_us_per_unit": round(s, 4),
        "linear_intercept_us": round(i, 3),
        "linear_r2": round(r2, 5),
        "loglog_exponent": round(k, 4),
        "loglog_r2": round(kr2, 5),
    }


def parse_bench(path):
    """name -> ns/op from go test -bench output."""
    out = {}
    pat = re.compile(r"^(Benchmark\S+?)(?:-\d+)?\s+\d+\s+([\d.]+) ns/op")
    with open(path) as f:
        for line in f:
            m = pat.match(line)
            if m:
                out[m.group(1)] = float(m.group(2))
    return out


def plot_scaling(rep, out_dir, fits):
    cyc = defaultdict(dict)
    for c in rep["scheduling_cycle"]:
        cyc[c["profile"]][c["nodes"]] = c["latency"]
    plug = {p["nodes"]: p for p in rep["plugin_extension_points"]}
    ov = defaultdict(dict)
    for o in rep["overhead_vs_default"]:
        ov[o["profile"]][o["nodes"]] = o
    nodes = sorted(plug)

    fig, axes = plt.subplots(1, 3, figsize=(16, 4.8))
    ax = axes[0]
    for p in PROFILES:
        xs = sorted(cyc[p])
        ax.plot(xs, [cyc[p][n]["p50_us"] for n in xs], "o-", color=COLORS[p], label=f"{LABELS[p]} p50")
        ax.plot(xs, [cyc[p][n]["p95_us"] for n in xs], "s--", color=COLORS[p], alpha=0.6, label=f"{LABELS[p]} p95")
    ax.set_title("Scheduling cycle (UpdateSnapshot + SchedulePod)")
    ax.set_xlabel("nodes")
    ax.set_ylabel("latency, µs")
    ax.grid(alpha=0.3)
    ax.legend(fontsize=8)

    ax = axes[1]
    pre = [plug[n]["prescore"]["mean_us"] for n in nodes]
    s, i, r2 = linfit(nodes, pre)
    ax.plot(nodes, pre, "o", color=COLORS["carbon-aware-scheduler"], label="PreScore mean")
    ax.plot(nodes, [plug[n]["prescore"]["p99_us"] for n in nodes], "^", color="#999999", label="PreScore p99")
    ax.plot([0, max(nodes)], [i, i + s * max(nodes)], "-", color="black", lw=1,
            label=f"fit {s:.2f} µs/node + {i:.1f}, R²={r2:.4f}")
    tot = [plug[n]["plugin_total_mean_us"] for n in nodes]
    ax.plot(nodes, tot, "x-", color="#D62728", label="PreScore + N·Score + Normalize")
    ax.set_title("CarbonAware plugin, standalone")
    ax.set_xlabel("nodes")
    ax.set_ylabel("µs per pod")
    ax.grid(alpha=0.3)
    ax.legend(fontsize=8)

    ax = axes[2]
    p = "carbon-aware-scheduler"
    xs = sorted(ov[p])
    ax.errorbar(xs, [ov[p][n]["mean_delta_us"] / 1e3 for n in xs],
                yerr=[ov[p][n]["mean_delta_ci95_us"] / 1e3 for n in xs], fmt="o-",
                color=COLORS[p], capsize=3, label="mean Δ (95% CI)")
    ax.plot(xs, [ov[p][n]["p99_delta_us"] / 1e3 for n in xs], "s--", color="#555555", label="p99 Δ")
    ax.axhline(5.0, color="#D62728", ls=":", label="budget 5 ms")
    ax.set_yscale("symlog", linthresh=0.1)
    ax.set_title("Overhead carbon-aware − default")
    ax.set_xlabel("nodes")
    ax.set_ylabel("ms per pod")
    ax.grid(alpha=0.3)
    ax.legend(fontsize=8)

    fig.tight_layout()
    fig.savefig(f"{out_dir}/scaling_latency_vs_nodes.png", dpi=150)
    plt.close(fig)

    fits["plugin_prescore_mean_vs_nodes"] = fit_entry(nodes, pre)
    fits["plugin_total_mean_vs_nodes"] = fit_entry(nodes, tot)
    for p in PROFILES:
        xs = sorted(cyc[p])
        fits[f"cycle_mean_{p}"] = fit_entry(xs, [cyc[p][n]["mean_us"] for n in xs])
    xs = sorted(ov["carbon-aware-scheduler"])
    fits["overhead_mean_carbon_aware"] = fit_entry(xs, [max(ov["carbon-aware-scheduler"][n]["mean_delta_us"], 1e-3) for n in xs])


def plot_forecast(bench, out_dir, fits):
    pat = re.compile(r"BenchmarkSchedulePreScoreForecast/nodes=20/intervals=(\d+)")
    pts = sorted((int(pat.match(k).group(1)), v / 1e3) for k, v in bench.items() if pat.match(k))
    if not pts:
        return
    xs, ys = [p[0] for p in pts], [p[1] for p in pts]
    fig, ax = plt.subplots(figsize=(6, 4.2))
    ax.plot(xs, ys, "o-", color=COLORS["carbon-aware-scheduler"], label="after fix (cursor)")
    ax.axhline(5000, color="#D62728", ls=":", label="5 ms budget")
    ax.set_xlabel("forecast intervals F (5 min each), job T̂ = 24 h")
    ax.set_ylabel("PreScore, µs (N = 20)")
    ax.set_title("PreScore vs forecast length")
    ax.grid(alpha=0.3)
    ax.legend(fontsize=8)
    fig.tight_layout()
    fig.savefig(f"{out_dir}/prescore_vs_forecast.png", dpi=150)
    plt.close(fig)
    fx = [x for x in xs if x > 0]
    fy = [y for x, y in zip(xs, ys) if x > 0]
    fits["prescore_vs_forecast_n20"] = fit_entry(fx, fy)


def parse_traces(path):
    """go tool pprof -traces → list of (value_ms, [root..leaf])."""
    stacks = []
    val, frames = None, []
    unit = {"ns": 1e-6, "us": 1e-3, "µs": 1e-3, "ms": 1.0, "s": 1e3}
    with open(path) as f:
        for line in f:
            if line.startswith("-----------+"):
                if val is not None and frames:
                    stacks.append((val, list(reversed(frames))))
                val, frames = None, []
                continue
            m = re.match(r"^\s+([\d.]+)(ns|us|µs|ms|s)\s+(\S.*)$", line)
            if m:
                val = float(m.group(1)) * unit[m.group(2)]
                frames = [m.group(3).strip()]
                continue
            if val is not None and line.strip():
                frames.append(line.strip())
    if val is not None and frames:
        stacks.append((val, list(reversed(frames))))
    return stacks


def short(fn):
    fn = re.sub(r"^.*/", "", fn)
    return fn


def plot_flame(traces_path, out_dir):
    try:
        stacks = parse_traces(traces_path)
    except FileNotFoundError:
        return
    if not stacks:
        return
    tree = {"v": 0.0, "c": {}}
    for v, fr in stacks:
        node = tree
        node["v"] += v
        for f in fr:
            node = node["c"].setdefault(f, {"v": 0.0, "c": {}})
            node["v"] += v
    total = tree["v"]
    rects = []

    def walk(node, x, depth):
        for name, ch in sorted(node["c"].items()):
            w = ch["v"] / total
            if w >= 0.002:
                rects.append((x, depth, w, name, ch["v"]))
                walk(ch, x, depth + 1)
            x += w

    walk(tree, 0.0, 0)
    depth = max(r[1] for r in rects) + 1
    fig, ax = plt.subplots(figsize=(18, max(6, 0.28 * depth)))
    for x, d, w, name, v in rects:
        ours = "carbon-aware-k8s/pkg/" in name or "carbon-aware-k8s/api/" in name
        color = "#2CA02C" if ours else plt.cm.Oranges(0.35 + 0.5 * ((hash(name) % 100) / 100))
        ax.add_patch(plt.Rectangle((x, d), w, 0.95, facecolor=color, edgecolor="white", lw=0.3))
        label = short(name)
        if w * 18 * 72 / 6.5 > len(label) * 0.9:
            ax.text(x + 0.002, d + 0.45, label, va="center", fontsize=6.5, clip_on=True)
        elif w > 0.02:
            ax.text(x + 0.002, d + 0.45, label[: max(3, int(w * 18 * 72 / 6.5 / 0.9) - 1)], va="center", fontsize=6.5, clip_on=True)
    ax.set_xlim(0, 1)
    ax.set_ylim(0, depth)
    ax.set_yticks([])
    ax.set_xlabel(f"share of CPU samples (total {total/1e3:.2f} s); green = carbon-aware-k8s plugin code (pkg/, api/)")
    ax.set_title("CPU flame graph: BenchmarkScheduleCycle carbon-aware-scheduler, 50 nodes")
    fig.tight_layout()
    fig.savefig(f"{out_dir}/flamegraph_cycle_carbon50.png", dpi=130)
    plt.close(fig)


def main():
    rep_path, bench_path, out_dir = sys.argv[1:4]
    with open(rep_path) as f:
        rep = json.load(f)
    bench = parse_bench(bench_path)
    fits = {}
    plot_scaling(rep, out_dir, fits)
    plot_forecast(bench, out_dir, fits)
    plot_flame(f"{out_dir}/cpu_cycle_carbon50.traces", out_dir)
    with open(f"{out_dir}/scaling_fit.json", "w") as f:
        json.dump(fits, f, indent=2)
        f.write("\n")
    for k, v in fits.items():
        print(f"{k}: slope={v['linear_slope_us_per_unit']} R2={v['linear_r2']} loglog={v['loglog_exponent']}")


if __name__ == "__main__":
    main()
