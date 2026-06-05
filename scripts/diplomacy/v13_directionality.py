#!/usr/bin/env python3
"""V13 within-data directionality audit.

The V13 cross-condition audit established that briefings caused behavior
divergence (Jaccard 0.65 between treatment and control at year 1) and that
agents demonstrably read them (82.6% citation rate in order generation).
What it didn't establish: was the divergence in a USEFUL direction?

This audit measures the trajectory of per-power SC counts in treatment vs
control, year by year, to answer:

  - When did the conditions diverge in outcome (not just behavior)?
  - Which power(s) drove the survival diff?
  - Did treatment's early divergence (more aggressive Y1-2, more unequal)
    actually pay off later (rebalancing Y3-7)?
  - Is the briefing-citation rate correlated with SC gain at a per-(power,
    year) level?

Zero LLM cost. Reads lmvsgame.json phase states and llm_responses.csv
citation flags.
"""

import csv
import json
import re
import sys
from collections import defaultdict
from pathlib import Path

csv.field_size_limit(sys.maxsize)

RESULTS_DIR = Path("/home/justin/Documents/AI_Diplomacy/results")
POWERS = ["AUSTRIA", "ENGLAND", "FRANCE", "GERMANY", "ITALY", "RUSSIA", "TURKEY"]
CITATION_TERMS = [
    "briefing",
    "intelligence",
    "coalition threat",
    "cross-message",
    "consistency",
    "stab risk",
    "bait",
    "deception",
    "crux",
    "alignment",
    "shared ground",
]


def load_phases(lmvsgame_path: Path) -> list[dict]:
    """Return list of {name, year, season, centers} sorted by phase order."""
    with lmvsgame_path.open() as f:
        d = json.load(f)
    out = []
    for p in d["phases"]:
        name = p["name"]
        m = re.match(r"([SFW])(\d{4})([MAR])", name)
        if not m:
            continue
        season_char, year_str, phase_char = m.groups()
        out.append(
            {
                "name": name,
                "year": int(year_str),
                "season": season_char,
                "phase_type": phase_char,
                "centers": p["state"]["centers"],
            }
        )
    return out


def sc_counts(centers: dict) -> dict[str, int]:
    return {p: len(centers.get(p, [])) for p in POWERS}


def end_of_year_sc(phases: list[dict]) -> dict[int, dict[str, int]]:
    """Per year, the SC count at the LAST phase of that year.

    SC totals are computed after winter adjustments, so the W{year}A phase
    is the canonical year-end. If a year has no W phase (game ended), use
    the last available phase for that year."""
    by_year: dict[int, list[dict]] = defaultdict(list)
    for p in phases:
        by_year[p["year"]].append(p)
    out = {}
    for year, ps in sorted(by_year.items()):
        winter = [p for p in ps if p["phase_type"] == "A"]
        end = winter[-1] if winter else ps[-1]
        out[year] = sc_counts(end["centers"])
    return out


def gini(values: list[int]) -> float:
    """Standard Gini coefficient on a list of values."""
    n = len(values)
    if n == 0:
        return 0.0
    s = sorted(values)
    total = sum(s)
    if total == 0:
        return 0.0
    cum = 0
    for i, v in enumerate(s, 1):
        cum += i * v
    return (2 * cum) / (n * total) - (n + 1) / n


def citation_by_year_power(csv_path: Path) -> dict[tuple[int, str], dict]:
    """Per (year, power), count order-gen calls and how many cited briefings."""
    out: dict[tuple[int, str], dict[str, int]] = defaultdict(
        lambda: {"hits": 0, "total": 0}
    )
    with csv_path.open() as f:
        reader = csv.DictReader(f)
        for row in reader:
            if row["response_type"] != "order_generation":
                continue
            phase = row["phase"]
            m = re.match(r"[SFW](\d{4})[MAR]", phase)
            if not m:
                continue
            year = int(m.group(1))
            key = (year, row["power"])
            out[key]["total"] += 1
            text = row["raw_response"].lower()
            if any(t in text for t in CITATION_TERMS):
                out[key]["hits"] += 1
    return dict(out)


def main():
    t_phases = load_phases(RESULTS_DIR / "gemot_v13_live" / "game" / "lmvsgame.json")
    c_phases = load_phases(RESULTS_DIR / "control_v13_live" / "game" / "lmvsgame.json")
    t_sc = end_of_year_sc(t_phases)
    c_sc = end_of_year_sc(c_phases)
    years = sorted(set(t_sc) & set(c_sc))

    t_citations = citation_by_year_power(
        RESULTS_DIR / "gemot_v13_live" / "game" / "llm_responses.csv"
    )

    print("=== Per-power SC trajectory, treatment vs control ===\n")
    print(f"{'power':<8} {'year':>5} {'T':>3} {'C':>3} {'ΔSC':>5} {'T cite':>7}")
    print("-" * 50)
    delta_by_year_power: dict[tuple[int, str], int] = {}
    for power in POWERS:
        for year in years:
            t = t_sc[year][power]
            c = c_sc[year][power]
            delta = t - c
            delta_by_year_power[(year, power)] = delta
            cite = t_citations.get((year, power), {})
            cite_str = f"{cite['hits']}/{cite['total']}" if cite.get("total") else "—"
            print(f"  {power:<6} {year:>5} {t:>3} {c:>3} {delta:>+5} {cite_str:>7}")
        print()

    print("=== Per-year Gini, treatment vs control ===\n")
    print(
        f"{'year':>5}  {'T Gini':>7}  {'C Gini':>7}  {'ΔGini':>7}  ({'T more balanced' if False else ''})"
    )
    print("-" * 50)
    for year in years:
        t_g = gini(list(t_sc[year].values()))
        c_g = gini(list(c_sc[year].values()))
        marker = " ← treatment more balanced" if t_g < c_g else ""
        print(f"{year:>5}  {t_g:>7.3f}  {c_g:>7.3f}  {t_g - c_g:>+7.3f}{marker}")

    print("\n=== Where did treatment outperform control? ===\n")
    by_year_total_delta = defaultdict(int)
    for (year, power), d in delta_by_year_power.items():
        by_year_total_delta[year] += d
    by_power_terminal = {}
    terminal_year = years[-1]
    for power in POWERS:
        by_power_terminal[power] = delta_by_year_power.get((terminal_year, power), 0)

    print("Per-year |sum of treatment SC advantage across all powers|:")
    for year in years:
        s = by_year_total_delta[year]
        bar = "█" * abs(s)
        print(f"  {year}: {s:+3d}  {bar}")
    print()
    print(f"Terminal-year ({terminal_year}) per-power ΔSC (treatment - control):")
    for power in POWERS:
        d = by_power_terminal[power]
        sign = "+" if d > 0 else ""
        marker = (
            "  ← driver of survival diff"
            if d > 0 and c_sc[terminal_year][power] == 0
            else ""
        )
        print(f"  {power:<8} {sign}{d:>+3d}{marker}")

    print("\n=== Citation correlation with SC gain ===\n")
    cited_deltas = []
    uncited_deltas = []
    for (year, power), cite in t_citations.items():
        if not cite["total"]:
            continue
        if (year, power) not in delta_by_year_power:
            continue
        nxt = delta_by_year_power.get((year + 1, power))
        own_sc_delta = (
            t_sc[year + 1][power] - t_sc[year][power] if year + 1 in t_sc else 0
        )
        if cite["hits"] == cite["total"]:
            cited_deltas.append(own_sc_delta)
        elif cite["hits"] == 0:
            uncited_deltas.append(own_sc_delta)
    if cited_deltas:
        mean_cited = sum(cited_deltas) / len(cited_deltas)
        print(
            f"  Mean own-power SC delta when all orders cited briefings:  {mean_cited:+.2f}  (n={len(cited_deltas)})"
        )
    if uncited_deltas:
        mean_uncited = sum(uncited_deltas) / len(uncited_deltas)
        print(
            f"  Mean own-power SC delta when no orders cited briefings:    {mean_uncited:+.2f}  (n={len(uncited_deltas)})"
        )
    else:
        print("  (No fully-uncited years to compare — citation rate is too high.)")

    print("\n=== Verdict guide ===")
    print("Treatment outperformed if: ΔGini negative over Y3-7, and the surviving")
    print("powers in treatment include ones eliminated in control. If ΔGini is")
    print("noisy or treatment underperforms in late years, the divergence was")
    print("not directionally helpful and the V13 7/7-vs-6/7 result is N=1 luck.")


if __name__ == "__main__":
    main()
