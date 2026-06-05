#!/usr/bin/env python3
"""V14 causal-trace audit.

V14 differs from V13:
  - per-season briefings (year_spring + year_fall), not annual
  - no matched control (V14 vs V13/control is cross-cadence, weaker signal)

This audit tests:

  B) Citation rate in V14 treatment rationales — no control needed
  C) Per-season alignment between briefings and orders — no control needed
  A') Year-1-spring V14-treatment vs V13-control orders. Looser comparison
      than V13 audit's matched control (different briefing cadence, same
      model + initial state) but the only cross-condition signal available.

If B and C track V13's numbers, the V14 mechanism is doing the same kind of
work (with more frequent briefings). If they diverge sharply, per-season
cadence does something materially different.
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

ORDER_BLOCK_RE = re.compile(
    r"PARSABLE\s+OUTPUT:?\s*```(?:json)?\s*(\{.*?\})\s*```",
    re.DOTALL | re.IGNORECASE,
)

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


def extract_orders(raw_response: str) -> list[str]:
    m = ORDER_BLOCK_RE.search(raw_response)
    if not m:
        return []
    try:
        parsed = json.loads(m.group(1))
    except json.JSONDecodeError:
        return []
    orders = parsed.get("orders") or parsed.get("final_orders") or []
    return [o.strip() for o in orders if isinstance(o, str)]


def load_orders(csv_path: Path) -> dict[str, dict[str, list[str]]]:
    out: dict[str, dict[str, list[str]]] = defaultdict(dict)
    with csv_path.open() as f:
        reader = csv.DictReader(f)
        for row in reader:
            if row["response_type"] != "order_generation":
                continue
            phase = row["phase"]
            if not (phase.endswith("M") or phase.endswith("A")):
                continue
            orders = extract_orders(row["raw_response"])
            out[row["power"]][phase] = orders
    return out


def count_citations(csv_path: Path) -> dict:
    hits = defaultdict(int)
    total = defaultdict(int)
    with csv_path.open() as f:
        reader = csv.DictReader(f)
        for row in reader:
            key = (row["power"], row["response_type"])
            total[key] += 1
            text = row["raw_response"].lower()
            if any(term in text for term in CITATION_TERMS):
                hits[key] += 1
    return {"hits": dict(hits), "total": dict(total)}


def briefing_codes(path: Path) -> set[str]:
    text = path.read_text()
    codes = set(re.findall(r"\b([A-Z]{3})\b", text))
    skip = {"YES", "ALL", "AND", "FOR", "WAR", "TIE", "PER", "SEC", "ITS", "GMT", "YOU"}
    return codes - skip


def order_destinations(orders: list[str]) -> set[str]:
    dests = set()
    for o in orders:
        toks = re.findall(r"\b([A-Z]{3})\b", o.upper())
        if len(toks) >= 2:
            dests.update(toks[1:])
    return dests


def list_seasons(treatment_dir: Path) -> list[tuple[str, str, list[str]]]:
    """Find (season_dir_name, briefing_dir, phases_in_this_season)."""
    seasons = []
    for d in sorted(treatment_dir.iterdir()):
        if not d.is_dir() or "_" not in d.name:
            continue
        m = re.match(r"year(\d+)_(spring|fall)", d.name)
        if not m:
            continue
        year = int(m.group(1))
        season = m.group(2)
        if season == "spring":
            phases = [f"S{1900 + year}M", f"S{1900 + year}R"]
        else:
            phases = [f"F{1900 + year}M", f"F{1900 + year}R", f"W{1900 + year}A"]
        if (d / "briefings").exists():
            seasons.append((d.name, d / "briefings", phases))
    return seasons


def audit_B(citations: dict) -> dict:
    by_type = defaultdict(lambda: {"hits": 0, "total": 0})
    for (_, rt), h in citations["hits"].items():
        by_type[rt]["hits"] += h
    for (_, rt), t in citations["total"].items():
        by_type[rt]["total"] += t
    return {
        rt: {**v, "rate": v["hits"] / max(v["total"], 1)} for rt, v in by_type.items()
    }


def audit_C_per_season(seasons, orders: dict) -> dict:
    out = {}
    for season_name, briefings_dir, phases in seasons:
        per_power = {}
        for power in POWERS:
            bp = briefings_dir / f"{power.lower()}_briefing.txt"
            if not bp.exists():
                continue
            b_codes = briefing_codes(bp)
            o_codes = set()
            for phase in phases:
                o_codes.update(order_destinations(orders.get(power, {}).get(phase, [])))
            if not o_codes:
                continue
            per_power[power] = {
                "briefing_codes": len(b_codes),
                "order_codes": len(o_codes),
                "overlap": len(o_codes & b_codes),
                "alignment": len(o_codes & b_codes) / len(o_codes),
            }
        if per_power:
            mean = sum(v["alignment"] for v in per_power.values()) / len(per_power)
            out[season_name] = {"per_power": per_power, "mean_alignment": mean}
    return out


def audit_A_prime_year1(v14_orders: dict, v13_control_orders: dict) -> dict:
    """Loose cross-condition comparison: V14 year-1 vs V13-control year-1.
    Same model + initial state, different intervention cadence."""
    year1_phases = ["S1901M", "F1901M", "W1901A"]
    out = {}
    for power in POWERS:
        for phase in year1_phases:
            t = set(v14_orders.get(power, {}).get(phase, []))
            c = set(v13_control_orders.get(power, {}).get(phase, []))
            if not t and not c:
                continue
            jaccard = len(t & c) / max(len(t | c), 1)
            out[(power, phase)] = {
                "v14": sorted(t),
                "v13_control": sorted(c),
                "jaccard": jaccard,
            }
    return out


def main():
    v14_dir = RESULTS_DIR / "gemot_v14_seasonal"
    v14_csv = v14_dir / "game" / "llm_responses.csv"
    v13_control_csv = RESULTS_DIR / "control_v13_live" / "game" / "llm_responses.csv"

    print("Loading orders + citations...")
    v14_orders = load_orders(v14_csv)
    v14_citations = count_citations(v14_csv)
    v13_control_orders = load_orders(v13_control_csv)
    seasons = list_seasons(v14_dir)

    print(
        f"\nV14 has {len(seasons)} seasons with briefings: {[s[0] for s in seasons[:6]]}..."
    )

    print("\n=== V14 Audit B: Briefing-citation rate in treatment rationales ===\n")
    b = audit_B(v14_citations)
    for rt in sorted(b):
        v = b[rt]
        print(f"  {rt:<26} {v['hits']}/{v['total']}  ({v['rate'] * 100:.1f}%)")

    print("\n=== V14 Audit C: Per-season alignment ===\n")
    c = audit_C_per_season(seasons, v14_orders)
    print(f"{'season':<14} {'mean_alignment':>16}  per-power")
    for season in sorted(c):
        v = c[season]
        per_power_str = ", ".join(
            f"{p[:3]}={d['alignment'] * 100:.0f}%"
            for p, d in sorted(v["per_power"].items())
        )
        print(f"  {season:<12} {v['mean_alignment'] * 100:>14.1f}%  {per_power_str}")
    if c:
        overall = sum(v["mean_alignment"] for v in c.values()) / len(c)
        print(f"\n  Overall mean (across seasons): {overall * 100:.1f}%")

    print("\n=== V14 Audit A': Year-1 V14 vs V13-control orders ===")
    print("(Cross-cadence comparison — weaker than matched control.)\n")
    a = audit_A_prime_year1(v14_orders, v13_control_orders)
    n_diff = sum(1 for v in a.values() if v["jaccard"] < 1.0)
    n_total = len(a)
    mean_j = sum(v["jaccard"] for v in a.values()) / max(n_total, 1)
    print(f"  {n_diff}/{n_total} pairs differ; mean Jaccard = {mean_j:.2f}")
    for (power, phase), v in sorted(a.items()):
        if v["jaccard"] < 1.0:
            print(f"  {power} {phase}  jaccard={v['jaccard']:.2f}")

    print("\n=== Verdict guide ===")
    print("If B/C track V13's numbers (order-gen citation ~80%, alignment ~67%),")
    print("V14 mechanism behaves the same as V13 — more frequent briefings,")
    print("same engagement pattern. Divergence = per-season cadence matters.")


if __name__ == "__main__":
    main()
