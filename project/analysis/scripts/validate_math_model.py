#!/usr/bin/env python3
"""Numerical validation of docs/ssot/02_math_model_ssot.md (T2.2).

Pure functions implement the SSOT formulas without changing them. The script
reproduces oracles §14, the G-* guards, weight/CI sensitivity, volatility,
horizon length, SLA cutoff, and the 5-minute discrete-integral cost.

Example:
    python analysis/scripts/validate_math_model.py \\
        --output-dir docs/research/figures/math_model_validation --seed 42
"""

from __future__ import annotations

import argparse
import json
import math
import sys
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import matplotlib.pyplot as plt
import numpy as np
from matplotlib.ticker import MaxNLocator

ETA_PER_HOUR: float = 1.0e-3
ETA_PER_SECOND: float = 1.0 / 3.6e6
EPS_CF: float = 1.0e-6
EPS_CI: float = 1.0e-6
EPS_W: float = 1.0e-9
S_C_NEUTRAL: float = 0.5
SCORE_NEUTRAL: float = 50.0
CI_MAX_VALID: float = 2000.0
P_IDLE_DEFAULT: float = 40.0
P_MAX_DEFAULT: float = 100.0

# Okabe–Ito (colour-blind safe); not the matplotlib default cycle (PY-11).
PALETTE: tuple[str, ...] = (
    "#0072B2",
    "#E69F00",
    "#009E73",
    "#CC79A7",
    "#D55E00",
    "#56B4E9",
)


class ConfigError(Exception):
    """Fail-closed configuration (G-03, G-18)."""


class SLAPreconditionFailed(Exception):
    """Fail-closed workload SLA (G-12)."""


@dataclass(frozen=True)
class NodeSpec:
    name: str
    r_req_millicores: float
    r_alloc_millicores: float
    p_idle_w: float = P_IDLE_DEFAULT
    p_max_w: float = P_MAX_DEFAULT
    zone: str | None = None
    ci_now_gco2e_per_kwh: float | None = None
    has_zone: bool = True


def clip(x: float, lo: float, hi: float) -> float:
    """SSOT clip(x, a, b)."""
    return min(hi, max(lo, x))


def rnd_half_away_from_zero(x: float) -> int:
    """Nearest integer, ties away from zero (2.21)."""
    if x >= 0.0:
        return int(math.floor(x + 0.5))
    return int(math.ceil(x - 0.5))


def validate_power_profile(p_idle_w: float, p_max_w: float) -> None:
    if not math.isfinite(p_idle_w) or not math.isfinite(p_max_w):
        raise ConfigError("P_idle/P_max must be finite")
    if p_idle_w < 0.0 or p_max_w < p_idle_w:
        raise ConfigError("domain (2.2a) violated")


def validate_weights(w_c: float, w_r: float, w_p: float) -> None:
    if min(w_c, w_r, w_p) < 0.0:
        raise ConfigError("negative weight")
    if abs(w_c + w_r + w_p - 1.0) > EPS_W:
        raise ConfigError("weights not on simplex (2.20)")


def is_ci_valid(ci: float) -> bool:
    return math.isfinite(ci) and 0.0 <= ci <= CI_MAX_VALID


def node_power_w(u: float, p_idle_w: float, p_max_w: float) -> float:
    """(2.2)."""
    return p_idle_w + (p_max_w - p_idle_w) * clip(u, 0.0, 1.0)


def utilization_pair(
    r_req: float, r_pod: float, r_alloc: float
) -> tuple[float, float]:
    """(2.3) with G-02."""
    if r_alloc <= 0.0:
        return 1.0, 1.0
    u_minus = clip(r_req / r_alloc, 0.0, 1.0)
    u_plus = clip((r_req + r_pod) / r_alloc, 0.0, 1.0)
    return u_minus, u_plus


def p_pod_w(node: NodeSpec, r_pod: float) -> tuple[float, float, float]:
    """Return (P_pod, u_minus, u_plus) via (2.4)."""
    validate_power_profile(node.p_idle_w, node.p_max_w)
    u_minus, u_plus = utilization_pair(
        node.r_req_millicores, r_pod, node.r_alloc_millicores
    )
    power = node_power_w(u_plus, node.p_idle_w, node.p_max_w) - node_power_w(
        u_minus, node.p_idle_w, node.p_max_w
    )
    return max(power, 0.0), u_minus, u_plus


def lookup_ci(grid: list[tuple[float, float]], t: float) -> float | None:
    """Piecewise-constant CI on left-closed intervals (2.7)."""
    chosen: float | None = None
    for t_start, value in grid:
        if t_start <= t:
            chosen = value
        else:
            break
    return chosen


def expected_cf_gco2e(
    power_w: float,
    t_hat_s: float,
    grid: list[tuple[float, float]],
    t_now_s: float = 0.0,
) -> tuple[float, int]:
    """(2.10s) with G-11, G-15, G-16, G-17."""
    if t_hat_s <= 0.0:
        return 0.0, 0
    t_end = t_now_s + t_hat_s
    points: set[float] = {t_now_s, t_end}
    for t_start, _ci in grid:
        if t_now_s < t_start < t_end:
            points.add(t_start)
    tau = sorted(points)
    total = 0.0
    pieces = 0
    for left, right in zip(tau[:-1], tau[1:]):
        dt_s = right - left
        if dt_s <= 0.0:
            continue
        ci = lookup_ci(grid, left)
        if ci is None or not is_ci_valid(ci):
            continue
        total += ETA_PER_SECOND * power_w * dt_s * ci
        pieces += 1
    if pieces == 0:
        ci = lookup_ci(grid, t_now_s)
        if ci is not None and is_ci_valid(ci):
            total = ETA_PER_SECOND * power_w * t_hat_s * ci
            pieces = 1
    return max(total, 0.0), pieces


def carbon_scores(
    cf_gco2e: np.ndarray,
    in_minmax: np.ndarray,
) -> np.ndarray:
    """(2.15) with G-04, G-05, G-09. Neutral is 0.5, never 1.0 on degeneracy."""
    s_c = np.full(cf_gco2e.shape, S_C_NEUTRAL, dtype=np.float64)
    if not np.any(in_minmax):
        return s_c
    subset = cf_gco2e[in_minmax]
    cf_min = float(np.min(subset))
    cf_max = float(np.max(subset))
    spread = cf_max - cf_min
    if spread > EPS_CF:
        s_c[in_minmax] = (cf_max - cf_gco2e[in_minmax]) / spread
    else:
        s_c[in_minmax] = S_C_NEUTRAL
    np.clip(s_c, 0.0, 1.0, out=s_c)
    return s_c


def performance_score(t_hat_s: float, t_deadline_s: float) -> float:
    """(2.18); caller must already have applied hard SLA."""
    if math.isinf(t_deadline_s):
        return 1.0
    if t_deadline_s == 0.0 and t_hat_s == 0.0:
        return 1.0
    if t_deadline_s > 0.0 and t_hat_s <= t_deadline_s:
        return clip((t_deadline_s - t_hat_s) / t_deadline_s, 0.0, 1.0)
    raise SLAPreconditionFailed("soft S_P called for ineligible node")


def sla_holds(t_hat_s: float, t_deadline_s: float) -> bool:
    if math.isinf(t_deadline_s):
        return True
    return t_hat_s <= t_deadline_s


def strategy_b_scores(ci_now: np.ndarray, valid: np.ndarray) -> np.ndarray:
    """(2.23)."""
    s_b = np.full(ci_now.shape, S_C_NEUTRAL, dtype=np.float64)
    if not np.any(valid):
        return s_b
    subset = ci_now[valid]
    ci_min = float(np.min(subset))
    ci_max = float(np.max(subset))
    spread = ci_max - ci_min
    if spread > EPS_CI:
        s_b[valid] = (ci_max - ci_now[valid]) / spread
    return s_b


@dataclass
class NodeScore:
    name: str
    cf_gco2e: float
    s_c: float
    s_r: float
    s_p: float
    score: float
    s_fwk: int
    k_pieces: int
    p_pod_w: float
    u_plus: float
    eligible: bool


def score_nodes(
    nodes: list[NodeSpec],
    r_pod: float,
    t_hat_s: float,
    t_deadline_s: float,
    weights: tuple[float, float, float],
    ci_grids: dict[str, list[tuple[float, float]]],
    t_now_s: float = 0.0,
    *,
    stale: bool = False,
    lambda_res: float = 1.0,
) -> list[NodeScore]:
    """PreScore-equivalent batch. Score phase would be O(1) lookup of `.score`."""
    w_c, w_r, w_p = weights
    validate_weights(w_c, w_r, w_p)
    if stale:
        return [
            NodeScore(
                name=n.name,
                cf_gco2e=float("nan"),
                s_c=S_C_NEUTRAL,
                s_r=S_C_NEUTRAL,
                s_p=S_C_NEUTRAL,
                score=SCORE_NEUTRAL,
                s_fwk=int(SCORE_NEUTRAL),
                k_pieces=0,
                p_pod_w=float("nan"),
                u_plus=float("nan"),
                eligible=True,
            )
            for n in nodes
        ]
    if not sla_holds(t_hat_s, t_deadline_s):
        raise SLAPreconditionFailed("SLA precondition failed")

    powers: list[float] = []
    u_plus_list: list[float] = []
    cfs: list[float] = []
    ks: list[int] = []
    in_minmax: list[bool] = []
    ci_now_list: list[float] = []
    ci_valid_for_b: list[bool] = []

    for node in nodes:
        p_hat, _u_minus, u_plus = p_pod_w(node, r_pod)
        powers.append(p_hat)
        u_plus_list.append(u_plus)
        zone_ok = bool(node.has_zone and node.zone)
        if zone_ok:
            grid = ci_grids.get(node.zone or "", [])
            cf, k = expected_cf_gco2e(p_hat, t_hat_s, grid, t_now_s)
            ci_now = lookup_ci(grid, t_now_s)
            ok_now = ci_now is not None and is_ci_valid(ci_now)
            cfs.append(cf)
            ks.append(k)
            in_minmax.append(k > 0 or t_hat_s == 0.0)
            ci_now_list.append(ci_now if ok_now else float("nan"))
            ci_valid_for_b.append(ok_now)
            if not in_minmax[-1]:
                in_minmax[-1] = False
        else:
            cfs.append(float("nan"))
            ks.append(0)
            in_minmax.append(False)
            ci_now_list.append(float("nan"))
            ci_valid_for_b.append(False)

    cf_arr = np.array(cfs, dtype=np.float64)
    mm = np.array(in_minmax, dtype=bool)
    # G-17 already applied in integrator; NaN CF must not enter minmax.
    mm = mm & np.isfinite(cf_arr)
    s_c = carbon_scores(np.nan_to_num(cf_arr, nan=0.0), mm)
    s_p = performance_score(t_hat_s, t_deadline_s)
    results: list[NodeScore] = []
    for i, node in enumerate(nodes):
        s_r = 1.0 - clip(lambda_res * u_plus_list[i], 0.0, 1.0)
        score = 100.0 * (w_c * float(s_c[i]) + w_r * s_r + w_p * s_p)
        score = clip(score, 0.0, 100.0)
        if not math.isfinite(score):
            score = SCORE_NEUTRAL
        results.append(
            NodeScore(
                name=node.name,
                cf_gco2e=float(cf_arr[i]) if math.isfinite(cfs[i]) else float("nan"),
                s_c=float(s_c[i]),
                s_r=s_r,
                s_p=s_p,
                score=score,
                s_fwk=int(clip(float(rnd_half_away_from_zero(score)), 0.0, 100.0)),
                k_pieces=ks[i],
                p_pod_w=powers[i],
                u_plus=u_plus_list[i],
                eligible=True,
            )
        )
    return results


def check(
    name: str, cond: bool, detail: str, rows: list[dict[str, Any]]
) -> None:
    rows.append({"id": name, "pass": bool(cond), "detail": detail})
    if not cond:
        print(f"FAIL {name}: {detail}", file=sys.stderr)


def constant_grid(ci: float, t0: float = 0.0) -> list[tuple[float, float]]:
    return [(t0, ci)]


def save_fig(fig: plt.Figure, output_dir: Path, stem: str) -> dict[str, str]:
    png = output_dir / f"{stem}.png"
    pdf = output_dir / f"{stem}.pdf"
    fig.savefig(png, bbox_inches="tight")
    fig.savefig(pdf, bbox_inches="tight")
    plt.close(fig)
    return {"png": str(png.as_posix()), "pdf": str(pdf.as_posix())}


def run_oracles(rows: list[dict[str, Any]]) -> dict[str, Any]:
    out: dict[str, Any] = {}
    # 14.1
    p_n = node_power_w(0.5, 40.0, 100.0)
    node = NodeSpec("n", 500.0, 1000.0)
    p_hat, u_m, u_p = p_pod_w(node, 300.0)
    check("14.1_Pn", math.isclose(p_n, 70.0, abs_tol=1e-12), f"P_n={p_n}", rows)
    check(
        "14.1_Ppod",
        math.isclose(p_hat, 18.0, abs_tol=1e-12) and math.isclose(u_m, 0.5) and math.isclose(u_p, 0.8),
        f"P_pod={p_hat} u-={u_m} u+={u_p}",
        rows,
    )
    p_dyn_1 = (100.0 - 40.0) * (300.0 / 1000.0)
    p_dyn_2 = (100.0 - 40.0) * (200.0 / 1000.0)
    check(
        "14.1_attr",
        math.isclose(p_dyn_1, 18.0) and math.isclose(p_dyn_2, 12.0) and math.isclose(p_dyn_1 + p_dyn_2, 30.0),
        f"dyn={p_dyn_1}+{p_dyn_2}",
        rows,
    )
    out["p_n_w"] = p_n
    out["p_pod_w"] = p_hat

    # 14.2 hours vs seconds
    cf_h = 18.0 * 2.0 * 100.0 * ETA_PER_HOUR
    cf_s, k_s = expected_cf_gco2e(18.0, 7200.0, constant_grid(100.0))
    e_kwh = 18.0 * 2.0 * ETA_PER_HOUR
    check("14.2_hours", math.isclose(cf_h, 3.6, abs_tol=1e-12), f"cf_h={cf_h}", rows)
    check("14.2_seconds", math.isclose(cf_s, 3.6, abs_tol=1e-12), f"cf_s={cf_s} K={k_s}", rows)
    check("14.2_identity", math.isclose(cf_h, cf_s, rel_tol=0.0, abs_tol=1e-12), "hour!=second", rows)
    check("14.2_energy", math.isclose(e_kwh, 0.036, abs_tol=1e-12), f"E={e_kwh}", rows)
    cf_node = 70.0 * 2.0 * 100.0 * ETA_PER_HOUR
    cf_idle = 40.0 * 2.0 * 100.0 * ETA_PER_HOUR
    check(
        "14.2_cluster_not_pod",
        math.isclose(cf_node, 14.0) and math.isclose(cf_idle, 8.0) and cf_s < cf_node,
        f"CF_n={cf_node} idle={cf_idle} pod={cf_s}",
        rows,
    )
    out["cf_hours_gco2e"] = cf_h
    out["cf_seconds_gco2e"] = cf_s
    out["eta_hour_times_3600"] = ETA_PER_HOUR / 3600.0
    out["eta_per_second"] = ETA_PER_SECOND
    check(
        "2.1_eta_identity",
        math.isclose(ETA_PER_HOUR / 3600.0, ETA_PER_SECOND, rel_tol=0.0, abs_tol=1e-18),
        f"{ETA_PER_HOUR / 3600.0} vs {ETA_PER_SECOND}",
        rows,
    )

    # 14.3
    nodes = [
        NodeSpec("n1", 200.0, 1000.0, zone="north", ci_now_gco2e_per_kwh=100.0),
        NodeSpec("n2", 400.0, 1000.0, zone="west", ci_now_gco2e_per_kwh=400.0),
    ]
    grids = {"north": constant_grid(100.0), "west": constant_grid(400.0)}
    scored = score_nodes(
        nodes, 300.0, 7200.0, 14400.0, (0.6, 0.2, 0.2), grids
    )
    check("14.3_cf", math.isclose(scored[0].cf_gco2e, 3.6) and math.isclose(scored[1].cf_gco2e, 14.4), str([(s.cf_gco2e) for s in scored]), rows)
    check("14.3_sc", math.isclose(scored[0].s_c, 1.0) and math.isclose(scored[1].s_c, 0.0), str([s.s_c for s in scored]), rows)
    check("14.3_sr", math.isclose(scored[0].s_r, 0.5) and math.isclose(scored[1].s_r, 0.3), str([s.s_r for s in scored]), rows)
    check("14.3_sp", math.isclose(scored[0].s_p, 0.5) and math.isclose(scored[1].s_p, 0.5), str([s.s_p for s in scored]), rows)
    check("14.3_score", math.isclose(scored[0].score, 80.0) and math.isclose(scored[1].score, 16.0), str([s.score for s in scored]), rows)
    out["oracle_14_3"] = [{"name": s.name, "score": s.score, "s_c": s.s_c, "cf": s.cf_gco2e} for s in scored]

    # 14.4 degeneracy — S_C must be 0.5, not 1.0
    grids_eq = {"north": constant_grid(100.0), "west": constant_grid(100.0)}
    deg = score_nodes(nodes, 300.0, 7200.0, 14400.0, (0.6, 0.2, 0.2), grids_eq)
    check(
        "14.4_sc_neutral",
        math.isclose(deg[0].s_c, 0.5) and math.isclose(deg[1].s_c, 0.5),
        f"S_C={[s.s_c for s in deg]} (canon is 0.5, not 1.0)",
        rows,
    )
    check(
        "14.4_scores",
        math.isclose(deg[0].score, 50.0) and math.isclose(deg[1].score, 46.0),
        str([s.score for s in deg]),
        rows,
    )
    check("14.4_no_div0", all(math.isfinite(s.score) for s in deg), "non-finite Score", rows)
    out["oracle_14_4"] = [{"name": s.name, "score": s.score, "s_c": s.s_c} for s in deg]

    # 14.5 clip
    over = NodeSpec("over", 900.0, 1000.0)
    p_over, u_m_o, u_p_o = p_pod_w(over, 300.0)
    check(
        "14.5_clip",
        math.isclose(u_p_o, 1.0) and math.isclose(u_m_o, 0.9) and math.isclose(p_over, 6.0),
        f"u+={u_p_o} u-={u_m_o} P={p_over}",
        rows,
    )

    # 14.6 SLA
    sla_fail = False
    try:
        score_nodes(nodes, 300.0, 1800.0, 1200.0, (0.6, 0.2, 0.2), grids)
    except SLAPreconditionFailed:
        sla_fail = True
    check("14.6_fail_closed", sla_fail, "expected SLAPreconditionFailed", rows)
    ok_sla = score_nodes(nodes, 300.0, 1800.0, 3600.0, (0.6, 0.2, 0.2), grids)
    check("14.6_eligible_sp", math.isclose(ok_sla[0].s_p, 0.5), f"S_P={ok_sla[0].s_p}", rows)
    # Score=0 is not the SLA mechanism
    check("14.6_not_score_zero", sla_fail, "SLA must raise, not return Score=0", rows)

    # 14.7 forecast vs persistence
    two = [(0.0, 100.0), (3600.0, 400.0)]
    cf_fc, k_fc = expected_cf_gco2e(18.0, 7200.0, two)
    cf_ps, _k = expected_cf_gco2e(18.0, 7200.0, constant_grid(100.0))
    check("14.7_forecast", math.isclose(cf_fc, 9.0, abs_tol=1e-12), f"cf={cf_fc} K={k_fc}", rows)
    check("14.7_persistence", math.isclose(cf_ps, 3.6, abs_tol=1e-12), f"cf={cf_ps}", rows)
    check("14.7_B_vs_C_identifiable", not math.isclose(cf_fc, cf_ps), "forecast collapsed to persistence", rows)
    out["cf_forecast_gco2e"] = cf_fc
    out["cf_persistence_gco2e"] = cf_ps
    return out


def run_guards(rows: list[dict[str, Any]]) -> None:
    nodes = [
        NodeSpec("n1", 200.0, 1000.0, zone="north"),
        NodeSpec("n2", 400.0, 1000.0, zone="west"),
    ]
    grids = {"north": constant_grid(100.0), "west": constant_grid(400.0)}

    # G-01 already in 14.5; also u<0
    u_neg = node_power_w(-0.3, 40.0, 100.0)
    check("G-01_neg", math.isclose(u_neg, 40.0), f"P={u_neg}", rows)

    # G-02
    dead = NodeSpec("dead", 100.0, 0.0)
    _p, um, up = p_pod_w(dead, 100.0)
    check("G-02_alloc0", math.isclose(um, 1.0) and math.isclose(up, 1.0), f"u-={um} u+={up}", rows)

    # G-03
    g03 = False
    try:
        validate_power_profile(40.0, 10.0)
    except ConfigError:
        g03 = True
    check("G-03", g03, "expected ConfigError", rows)

    # G-04 / G-05
    one = score_nodes([nodes[0]], 300.0, 7200.0, 14400.0, (0.6, 0.2, 0.2), grids)
    check("G-05_single", math.isclose(one[0].s_c, 0.5), f"S_C={one[0].s_c}", rows)

    # G-08 stale: total Score equal, even if U differs
    stale = score_nodes(nodes, 300.0, 7200.0, 14400.0, (0.6, 0.2, 0.2), grids, stale=True)
    check(
        "G-08_total",
        all(math.isclose(s.score, 50.0) for s in stale) and stale[0].score == stale[1].score,
        str([s.score for s in stale]),
        rows,
    )

    # G-09 missing zone
    mixed = [
        NodeSpec("n1", 200.0, 1000.0, zone="north"),
        NodeSpec("n2", 100.0, 1000.0, has_zone=False, zone=None),
    ]
    scored = score_nodes(mixed, 300.0, 7200.0, 14400.0, (0.6, 0.2, 0.2), grids)
    check("G-09_sc", math.isclose(scored[1].s_c, 0.5), f"S_C={scored[1].s_c}", rows)
    check("G-09_sr_kept", scored[1].s_r != scored[0].s_r, "S_R should still discriminate", rows)

    # G-10 invalid CI treated as out of minmax
    bad_grid = {"north": constant_grid(-5.0), "west": constant_grid(400.0)}
    scored_bad = score_nodes(nodes, 300.0, 7200.0, 14400.0, (0.6, 0.2, 0.2), bad_grid)
    check("G-10_invalid", math.isclose(scored_bad[0].s_c, 0.5), f"S_C n1={scored_bad[0].s_c}", rows)

    # G-11 zero runtime
    zrt = score_nodes(nodes, 300.0, 0.0, 14400.0, (0.6, 0.2, 0.2), grids)
    check(
        "G-11_zero_T",
        math.isclose(zrt[0].cf_gco2e, 0.0) and math.isclose(zrt[1].cf_gco2e, 0.0) and math.isclose(zrt[0].s_c, 0.5),
        str([(s.cf_gco2e, s.s_c) for s in zrt]),
        rows,
    )

    # G-14 0/0 slack
    sp = performance_score(0.0, 0.0)
    check("G-14_zero_deadline", math.isclose(sp, 1.0), f"S_P={sp}", rows)

    # G-18
    g18 = False
    try:
        validate_weights(0.5, 0.5, 0.5)
    except ConfigError:
        g18 = True
    check("G-18", g18, "expected ConfigError on weights", rows)

    # G-21 NaN score path: invalid everything → minmax empty → S_C 0.5, finite Score
    nan_nodes = [
        NodeSpec("a", 200.0, 1000.0, has_zone=False),
        NodeSpec("b", 400.0, 1000.0, has_zone=False),
    ]
    nan_scored = score_nodes(nan_nodes, 300.0, 7200.0, 14400.0, (0.6, 0.2, 0.2), {})
    check(
        "G-21_finite",
        all(math.isfinite(s.score) for s in nan_scored),
        str([s.score for s in nan_scored]),
        rows,
    )


def tradeoff_nodes() -> tuple[list[NodeSpec], dict[str, list[tuple[float, float]]]]:
    """S_C favours n1, S_R favours n2 — used for weight sensitivity."""
    nodes = [
        NodeSpec("clean_busy", 700.0, 1000.0, zone="north"),
        NodeSpec("dirty_empty", 0.0, 1000.0, zone="west"),
    ]
    grids = {"north": constant_grid(50.0), "west": constant_grid(400.0)}
    return nodes, grids


def run_sensitivity(
    rows: list[dict[str, Any]], rng: np.random.Generator
) -> dict[str, Any]:
    nodes, grids = tradeoff_nodes()
    r_pod = 200.0
    t_hat = 7200.0
    t_d = 14400.0
    base = score_nodes(nodes, r_pod, t_hat, t_d, (0.6, 0.2, 0.2), grids)
    # Analytic flip: ΔS_C=1, ΔS_R=0.1-0.8=-0.7, ΔS_P=0 → w_C = 0.7 w_R
    ds_c = base[0].s_c - base[1].s_c
    ds_r = base[0].s_r - base[1].s_r
    check("sens_conflict", ds_c > 0.0 and ds_r < 0.0, f"dSc={ds_c} dSr={ds_r}", rows)

    w_c_grid = np.linspace(0.0, 1.0, 51)
    scores_n1: list[float] = []
    scores_n2: list[float] = []
    winners: list[str] = []
    for w_c in w_c_grid:
        rest = 1.0 - float(w_c)
        w_r = rest / 2.0
        w_p = rest / 2.0
        scored = score_nodes(nodes, r_pod, t_hat, t_d, (float(w_c), w_r, w_p), grids)
        scores_n1.append(scored[0].score)
        scores_n2.append(scored[1].score)
        if math.isclose(scored[0].score, scored[1].score, abs_tol=1e-9):
            winners.append("tie")
        elif scored[0].score > scored[1].score:
            winners.append("clean_busy")
        else:
            winners.append("dirty_empty")

    # Simplex heatmap
    n_bin = 41
    wc_vals = np.linspace(0.0, 1.0, n_bin)
    wr_vals = np.linspace(0.0, 1.0, n_bin)
    argmax = np.full((n_bin, n_bin), np.nan)
    margin = np.full((n_bin, n_bin), np.nan)
    for i, w_c in enumerate(wc_vals):
        for j, w_r in enumerate(wr_vals):
            w_p = 1.0 - float(w_c) - float(w_r)
            if w_p < -1e-12:
                continue
            w_p = max(w_p, 0.0)
            total = float(w_c) + float(w_r) + w_p
            scored = score_nodes(
                nodes, r_pod, t_hat, t_d, (float(w_c) / total, float(w_r) / total, w_p / total), grids
            )
            argmax[j, i] = 0.0 if scored[0].score >= scored[1].score else 1.0
            margin[j, i] = scored[0].score - scored[1].score

    # CI multiplicative noise
    sigmas = [0.0, 0.05, 0.10, 0.20, 0.35, 0.50]
    n_mc = 1000
    flip_rate: list[float] = []
    mean_margin: list[float] = []
    quiet = np.seterr(divide="raise", invalid="raise")
    try:
        base_winner = 0 if base[0].score >= base[1].score else 1
        for sigma in sigmas:
            flips = 0
            margins = []
            for _ in range(n_mc):
                ci_n = float(np.clip(50.0 * (1.0 + sigma * rng.normal()), 0.0, CI_MAX_VALID))
                ci_w = float(np.clip(400.0 * (1.0 + sigma * rng.normal()), 0.0, CI_MAX_VALID))
                g = {"north": constant_grid(ci_n), "west": constant_grid(ci_w)}
                sc = score_nodes(nodes, r_pod, t_hat, t_d, (0.6, 0.2, 0.2), g)
                winner = 0 if sc[0].score >= sc[1].score else 1
                if winner != base_winner:
                    flips += 1
                margins.append(sc[0].score - sc[1].score)
            flip_rate.append(flips / n_mc)
            mean_margin.append(float(np.mean(margins)))
    finally:
        np.seterr(**quiet)

    # Rank preservation vs uniform CI scale (all zones * (1+e)): S_C invariant
    scaled = score_nodes(
        nodes,
        r_pod,
        t_hat,
        t_d,
        (0.6, 0.2, 0.2),
        {"north": constant_grid(50.0 * 1.25), "west": constant_grid(400.0 * 1.25)},
    )
    check(
        "sens_uniform_scale_invariant_sc",
        math.isclose(scaled[0].s_c, base[0].s_c) and math.isclose(scaled[1].s_c, base[1].s_c),
        f"{scaled[0].s_c},{scaled[1].s_c} vs {base[0].s_c},{base[1].s_c}",
        rows,
    )

    return {
        "base": [
            {
                "name": s.name,
                "s_c": s.s_c,
                "s_r": s.s_r,
                "s_p": s.s_p,
                "score": s.score,
                "cf_gco2e": s.cf_gco2e,
                "u_plus": s.u_plus,
                "p_pod_w": s.p_pod_w,
            }
            for s in base
        ],
        "ds_c": ds_c,
        "ds_r": ds_r,
        "flip_line_wc_over_wr": -ds_r / ds_c if ds_c != 0 else None,
        "w_c_grid": w_c_grid.tolist(),
        "score_n1": scores_n1,
        "score_n2": scores_n2,
        "winners_equal_split": winners,
        "argmax": argmax.tolist(),
        "margin": margin.tolist(),
        "wc_vals": wc_vals.tolist(),
        "wr_vals": wr_vals.tolist(),
        "noise_sigma": sigmas,
        "noise_flip_rate": flip_rate,
        "noise_mean_margin": mean_margin,
        "n_mc": n_mc,
    }


def run_volatility() -> dict[str, Any]:
    """CI 10 → 800 → 10 gCO2e/kWh on north; west constant 200."""
    dt = 300.0
    north: list[tuple[float, float]] = []
    west: list[tuple[float, float]] = []
    t = 0.0
    while t < 10800.0 + 1e-9:
        if t < 3600.0:
            ci_n = 10.0
        elif t < 7200.0:
            ci_n = 800.0
        else:
            ci_n = 10.0
        north.append((t, ci_n))
        west.append((t, 200.0))
        t += dt
    nodes = [
        NodeSpec("north_volatile", 200.0, 1000.0, zone="north"),
        NodeSpec("west_stable", 200.0, 1000.0, zone="west"),
    ]
    grids = {"north": north, "west": west}
    horizons_s = [30.0, 60.0, 300.0, 1800.0, 3600.0, 5400.0, 7200.0, 9000.0, 10800.0]
    series: list[dict[str, Any]] = []
    for t_hat in horizons_s:
        sc = score_nodes(nodes, 300.0, t_hat, math.inf, (0.6, 0.2, 0.2), grids)
        s_b = strategy_b_scores(
            np.array([10.0, 200.0]), np.array([True, True])
        )
        series.append(
            {
                "t_hat_s": t_hat,
                "k_north": sc[0].k_pieces,
                "k_west": sc[1].k_pieces,
                "cf_north": sc[0].cf_gco2e,
                "cf_west": sc[1].cf_gco2e,
                "score_north": sc[0].score,
                "score_west": sc[1].score,
                "winner_C": sc[0].name if sc[0].score >= sc[1].score else sc[1].name,
                "s_b_north": float(s_b[0]),
                "s_b_west": float(s_b[1]),
                "winner_B": "north_volatile" if s_b[0] >= s_b[1] else "west_stable",
            }
        )
    return {"dt_s": dt, "series": series}


def run_complexity(rng: np.random.Generator) -> dict[str, Any]:
    dt = 300.0
    horizons_s = np.array(
        [30.0, 60.0, 300.0, 600.0, 1800.0, 3600.0, 7200.0, 14400.0, 28800.0, 86400.0]
    )
    k_bound = 1.0 + np.ceil(horizons_s / dt)
    measured_k: list[int] = []
    times_us: list[float] = []
    grid = [(float(i * dt), 100.0 + (i % 3) * 10.0) for i in range(0, 300)]
    for t_hat in horizons_s:
        _cf, k = expected_cf_gco2e(18.0, float(t_hat), grid)
        measured_k.append(k)
        reps = 2000
        t0 = time.perf_counter()
        for _ in range(reps):
            expected_cf_gco2e(18.0, float(t_hat), grid)
        elapsed = (time.perf_counter() - t0) / reps
        times_us.append(elapsed * 1.0e6)
    # Score-phase model: dict lookup, independent of K
    table = {f"n{i}": float(i) for i in range(64)}
    t0 = time.perf_counter()
    acc = 0.0
    for _ in range(20000):
        acc += table["n7"]
    lookup_ns = (time.perf_counter() - t0) / 20000 * 1.0e9
    _ = acc
    return {
        "dt_s": dt,
        "horizons_s": horizons_s.tolist(),
        "k_bound": k_bound.tolist(),
        "k_measured": measured_k,
        "pre_score_integral_us": times_us,
        "score_lookup_ns": lookup_ns,
        "seed_note": f"rng used elsewhere; lookup acc={acc:.1f} rng={rng.random():.6f}",
    }


def make_plots(
    output_dir: Path,
    sensitivity: dict[str, Any],
    volatility: dict[str, Any],
    complexity: dict[str, Any],
    degeneracy_delta: dict[str, Any],
) -> dict[str, dict[str, str]]:
    plt.rcParams.update(
        {
            "axes.prop_cycle": plt.cycler(color=list(PALETTE)),
            "axes.grid": True,
            "grid.alpha": 0.35,
            "font.size": 10,
            "figure.facecolor": "white",
        }
    )
    figs: dict[str, dict[str, str]] = {}

    fig, ax = plt.subplots(figsize=(7.2, 4.2))
    wc = np.array(sensitivity["w_c_grid"])
    ax.plot(wc, sensitivity["score_n1"], label="clean_busy Score", lw=2)
    ax.plot(wc, sensitivity["score_n2"], label="dirty_empty Score", lw=2)
    ax.set_xlabel(r"$w_C$ (remainder split equally $w_R=w_P=(1-w_C)/2$)")
    ax.set_ylabel("Score (0–100)")
    ax.set_title("Weight sensitivity: Score vs carbon weight")
    ax.set_xlim(0, 1)
    ax.set_ylim(0, 100)
    ax.legend()
    fig.text(
        0.01,
        0.01,
        "Source: validate_math_model.py · tradeoff cluster · seed=42",
        fontsize=8,
        color="0.4",
    )
    figs["score_vs_wc"] = save_fig(fig, output_dir, "score_vs_wc")

    fig, ax = plt.subplots(figsize=(6.4, 5.4))
    margin = np.array(sensitivity["margin"], dtype=float)
    im = ax.imshow(
        margin,
        origin="lower",
        extent=(0, 1, 0, 1),
        aspect="auto",
        cmap="coolwarm",
        vmin=-40,
        vmax=40,
    )
    # feasible simplex w_C + w_R <= 1
    ax.plot([0, 1], [1, 0], color="0.2", lw=1.0, ls="--", label=r"$w_C+w_R=1$")
    cbar = fig.colorbar(im, ax=ax)
    cbar.set_label(r"Score(clean_busy) − Score(dirty_empty)")
    ax.set_xlabel(r"$w_C$")
    ax.set_ylabel(r"$w_R$")
    ax.set_title("Score margin on the weight simplex (w_P = 1 − w_C − w_R)")
    ax.legend(loc="upper right")
    fig.text(0.01, 0.01, "Source: validate_math_model.py · above diagonal is infeasible", fontsize=8, color="0.4")
    figs["weight_simplex_margin"] = save_fig(fig, output_dir, "weight_simplex_margin")

    fig, ax = plt.subplots(figsize=(7.2, 4.2))
    ax.plot(sensitivity["noise_sigma"], sensitivity["noise_flip_rate"], marker="o", lw=2)
    ax.set_xlabel(r"Multiplicative forecast noise $\sigma$ (independent per zone)")
    ax.set_ylabel("Ranking flip rate")
    ax.set_title(rf"CI noise sensitivity (N={sensitivity['n_mc']} Monte Carlo draws)")
    ax.set_ylim(0, 1)
    fig.text(0.01, 0.01, "Source: validate_math_model.py · CI' = clip(CI·(1+σZ), 0, 2000) · Z~N(0,1)", fontsize=8, color="0.4")
    figs["ci_noise_flip_rate"] = save_fig(fig, output_dir, "ci_noise_flip_rate")

    ser = volatility["series"]
    t_h = [row["t_hat_s"] / 3600.0 for row in ser]
    fig, ax = plt.subplots(figsize=(7.2, 4.2))
    ax.plot(t_h, [row["cf_north"] for row in ser], marker="o", label=r"north $\widehat{CF}$ (volatile 10→800→10)")
    ax.plot(t_h, [row["cf_west"] for row in ser], marker="s", label=r"west $\widehat{CF}$ (stable 200)")
    ax.set_xlabel(r"Horizon $\hat T$ (h)")
    ax.set_ylabel(r"$\widehat{CF}$ (gCO2e)")
    ax.set_title("Volatility: expected footprint vs runtime horizon (Δt = 5 min)")
    ax.legend()
    fig.text(0.01, 0.01, "Source: validate_math_model.py · grid 300 s · identical P_pod", fontsize=8, color="0.4")
    figs["volatility_cf_vs_horizon"] = save_fig(fig, output_dir, "volatility_cf_vs_horizon")

    fig, ax = plt.subplots(figsize=(7.2, 4.2))
    ax.plot(t_h, [row["score_north"] for row in ser], marker="o", label="north Score")
    ax.plot(t_h, [row["score_west"] for row in ser], marker="s", label="west Score")
    ax.set_xlabel(r"Horizon $\hat T$ (h)")
    ax.set_ylabel("Score (0–100)")
    ax.set_title("Volatility: plugin Score vs horizon (w = 0.6/0.2/0.2)")
    ax.set_ylim(0, 100)
    ax.legend()
    fig.text(0.01, 0.01, "Strategy B at t=0 always prefers north (CI=10 vs 200)", fontsize=8, color="0.4")
    figs["volatility_score_vs_horizon"] = save_fig(fig, output_dir, "volatility_score_vs_horizon")

    fig, ax = plt.subplots(figsize=(7.2, 4.2))
    h_h = np.array(complexity["horizons_s"]) / 3600.0
    ax.plot(h_h, complexity["k_bound"], ls="--", label=r"bound $1+\lceil\hat T/\Delta t\rceil$")
    ax.plot(h_h, complexity["k_measured"], marker="o", label="measured K")
    ax.set_xlabel(r"Horizon $\hat T$ (h)")
    ax.set_ylabel("Number of CI pieces K")
    ax.set_title(r"Discrete integral size at $\Delta t = 5$ min")
    ax.yaxis.set_major_locator(MaxNLocator(integer=True))
    ax.legend()
    fig.text(0.01, 0.01, "Source: validate_math_model.py · Score-phase lookup is O(1) in K", fontsize=8, color="0.4")
    figs["complexity_k_vs_T"] = save_fig(fig, output_dir, "complexity_k_vs_T")

    fig, ax = plt.subplots(figsize=(7.2, 4.2))
    dlt = np.array(degeneracy_delta["delta_gco2e"])
    ax.plot(dlt, degeneracy_delta["s_c_best"], marker="o", label=r"$S_C$ of lower-CF node")
    ax.plot(dlt, degeneracy_delta["s_c_worst"], marker="s", label=r"$S_C$ of higher-CF node")
    ax.axvline(EPS_CF, color=PALETTE[4], ls="--", label=rf"$\varepsilon_{{CF}}={EPS_CF:g}$ gCO2e")
    ax.set_xscale("log")
    ax.set_xlabel(r"$CF_{\max}-CF_{\min}$ (gCO2e)")
    ax.set_ylabel(r"$S_C$")
    ax.set_title("Degeneracy guard: $S_C$ collapses to 0.5 at $\\varepsilon_{CF}$")
    ax.set_ylim(-0.05, 1.05)
    ax.legend()
    fig.text(0.01, 0.01, "Source: validate_math_model.py · no division by zero; jump is intentional", fontsize=8, color="0.4")
    figs["degeneracy_sc"] = save_fig(fig, output_dir, "degeneracy_sc")

    return figs


def degeneracy_curve() -> dict[str, Any]:
    deltas = np.logspace(-9, 1, 21)
    s_best: list[float] = []
    s_worst: list[float] = []
    for d in deltas:
        cf = np.array([1.0, 1.0 + float(d)])
        sc = carbon_scores(cf, np.array([True, True]))
        s_best.append(float(sc[0]))
        s_worst.append(float(sc[1]))
    return {
        "delta_gco2e": deltas.tolist(),
        "s_c_best": s_best,
        "s_c_worst": s_worst,
        "eps_cf": EPS_CF,
        "neutral": S_C_NEUTRAL,
    }


def short_long_and_sla(rows: list[dict[str, Any]]) -> dict[str, Any]:
    nodes = [
        NodeSpec("n1", 200.0, 1000.0, zone="north"),
        NodeSpec("n2", 200.0, 1000.0, zone="west"),
    ]
    grids = {"north": constant_grid(100.0), "west": constant_grid(400.0)}
    short = score_nodes(nodes, 300.0, 30.0, math.inf, (0.6, 0.2, 0.2), grids)
    long = score_nodes(nodes, 300.0, 9000.0, math.inf, (0.6, 0.2, 0.2), grids)
    check("short_K", short[0].k_pieces == 1, f"K={short[0].k_pieces}", rows)
    check("short_finite", all(math.isfinite(s.score) for s in short), "non-finite short", rows)
    check("long_finite", all(math.isfinite(s.score) for s in long), "non-finite long", rows)
    # ranking identical under persistence + equal P
    check(
        "short_long_same_winner",
        (short[0].score >= short[1].score) == (long[0].score >= long[1].score),
        "winner changed under persistence",
        rows,
    )
    eps = 1.0e-6
    t_d = 1800.0
    fail_eps = False
    try:
        score_nodes(nodes, 300.0, t_d + eps, t_d, (0.6, 0.2, 0.2), grids)
    except SLAPreconditionFailed:
        fail_eps = True
    eq = score_nodes(nodes, 300.0, t_d, t_d, (0.6, 0.2, 0.2), grids)
    check("sla_equal_ok", eq[0].eligible and math.isclose(eq[0].s_p, 0.0), f"S_P={eq[0].s_p}", rows)
    check("sla_plus_eps_cut", fail_eps, "T_hat = T_deadline+eps must hard-cut", rows)
    return {
        "short_30s": [{"name": s.name, "cf": s.cf_gco2e, "K": s.k_pieces, "score": s.score} for s in short],
        "long_2_5h": [{"name": s.name, "cf": s.cf_gco2e, "K": s.k_pieces, "score": s.score} for s in long],
        "sla_equal_sp": eq[0].s_p,
        "sla_eps_cut": fail_eps,
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Validate carbon-aware math model (T2.2)")
    parser.add_argument(
        "--output-dir",
        type=Path,
        required=True,
        help="Directory for PNG/PDF figures and results.json",
    )
    parser.add_argument("--seed", type=int, default=42, help="RNG seed (printed and stored)")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    output_dir: Path = args.output_dir
    output_dir.mkdir(parents=True, exist_ok=True)
    seed = int(args.seed)
    rng = np.random.default_rng(seed)
    print(f"seed={seed}")

    rows: list[dict[str, Any]] = []
    np.seterr(divide="raise", invalid="raise")
    oracles = run_oracles(rows)
    run_guards(rows)
    horizon = short_long_and_sla(rows)
    sensitivity = run_sensitivity(rows, rng)
    volatility = run_volatility()
    # B vs C disagreement on volatility
    disagree = [row for row in volatility["series"] if row["winner_C"] != row["winner_B"]]
    check("vol_B_ne_C", len(disagree) > 0, f"disagree_rows={len(disagree)}", rows)
    complexity = run_complexity(rng)
    k_ok = all(
        mk <= bd + 1e-9
        for mk, bd in zip(complexity["k_measured"], complexity["k_bound"])
    )
    check("complexity_K_bound", k_ok, str(list(zip(complexity["k_measured"], complexity["k_bound"]))), rows)
    deg = degeneracy_curve()
    # at delta << eps, both 0.5; at delta >> eps, 1 and 0
    check("deg_small", math.isclose(deg["s_c_best"][0], 0.5) and math.isclose(deg["s_c_worst"][0], 0.5), "small delta", rows)
    check("deg_large", math.isclose(deg["s_c_best"][-1], 1.0) and math.isclose(deg["s_c_worst"][-1], 0.0), "large delta", rows)

    np.seterr(divide="warn", invalid="warn")
    figures = make_plots(output_dir, sensitivity, volatility, complexity, deg)

    failed = [r for r in rows if not r["pass"]]
    payload = {
        "seed": seed,
        "python": sys.version,
        "numpy": np.__version__,
        "verdict": "PASS" if not failed else "FAIL",
        "n_checks": len(rows),
        "n_failed": len(failed),
        "checks": rows,
        "oracles": oracles,
        "sensitivity": {
            k: v
            for k, v in sensitivity.items()
            if k not in {"argmax", "margin"}
        },
        "sensitivity_flip_line": sensitivity["flip_line_wc_over_wr"],
        "volatility": volatility,
        "complexity": complexity,
        "horizon_sla": horizon,
        "degeneracy": {"eps_cf": EPS_CF, "neutral_s_c": S_C_NEUTRAL},
        "figures": figures,
        "notes": {
            "s_c_on_cf_max_eq_cf_min": S_C_NEUTRAL,
            "s_c_is_not_1_on_degeneracy": True,
            "eta_s": ETA_PER_SECOND,
            "eta_h": ETA_PER_HOUR,
        },
    }
    result_path = output_dir / "results.json"
    result_path.write_text(json.dumps(payload, indent=2), encoding="utf-8")
    # compact edge matrix CSV
    csv_path = output_dir / "edge_cases.csv"
    csv_lines = ["id,pass,detail"]
    for r in rows:
        detail = r["detail"].replace(",", ";")
        csv_lines.append(f"{r['id']},{int(r['pass'])},{detail}")
    csv_path.write_text("\n".join(csv_lines) + "\n", encoding="utf-8")
    print(f"wrote {result_path}")
    print(f"verdict={payload['verdict']} checks={len(rows)} failed={len(failed)}")
    return 0 if not failed else 1


if __name__ == "__main__":
    raise SystemExit(main())
