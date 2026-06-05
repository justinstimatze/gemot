#!/usr/bin/env python3
"""V13 causal-trace audit.

V13 measured "gemot briefings on vs off" and saw 7/7 vs 6/7 survival, but
that comparison doesn't isolate whether briefing CONTENT was decisive.
Three failure modes:

  1. content was decisive (the recommendation changed the action)
  2. injection itself was decisive (any structured between-year intel)
  3. pause was decisive (timing, not content)

This audit uses only the data already on disk to test:

  A) Year-1 cross-condition action diff. Identical initial state in both
     conditions, only difference is the briefing. Different orders in
     treatment imply briefing-attributable behavior change.
  B) Briefing-citation rate in treatment rationales. Quantifies the
     EXPERIMENT_RESULTS.md qualitative claim that "agents explicitly cite
     briefing intelligence in their strategic reasoning".
  C) Per-power, per-year alignment between briefing recommendations and
     subsequent orders. Loose because briefings carry many possible
     recommendations; strict because we look for verbatim territory names
     in the briefing and matching destinations in the orders.

Outputs a punch-list verdict, zero LLM cost.
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


def extract_orders(raw_response: str) -> list[str]:
    """Pull the orders array out of the agent's response."""
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
    """Return {power: {phase: [orders]}} for movement/adjustment phases."""
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


def count_citations(csv_path: Path) -> dict[str, dict[str, int]]:
    """For each (power, response_type), count rows whose text mentions any
    citation term. Returns absolute count + denominator separately."""
    hits: dict[tuple[str, str], int] = defaultdict(int)
    total: dict[tuple[str, str], int] = defaultdict(int)
    with csv_path.open() as f:
        reader = csv.DictReader(f)
        for row in reader:
            key = (row["power"], row["response_type"])
            total[key] += 1
            text = row["raw_response"].lower()
            if any(term in text for term in CITATION_TERMS):
                hits[key] += 1
    return {"hits": dict(hits), "total": dict(total)}


def briefing_destinations(briefing_path: Path) -> set[str]:
    """Pull out all 3-letter territory codes mentioned in a briefing. The
    briefing format uses codes like NWY, BEL, LON, EDI throughout. A loose
    proxy for 'briefing-mentioned destinations' — if an order moves a unit
    to one of these, that counts as briefing-aligned."""
    text = briefing_path.read_text()
    codes = set(re.findall(r"\b([A-Z]{3})\b", text))
    skip = {"YES", "ALL", "AND", "FOR", "WAR", "TIE", "PER", "SEC", "ITS", "GMT", "YOU"}
    return codes - skip


def order_destinations(orders: list[str]) -> set[str]:
    """Extract destination codes from order strings like 'F EDI - NWG' or
    'A LON H' (hold). Returns the set of 3-letter codes the orders moved to
    or supported into."""
    dests = set()
    for o in orders:
        toks = re.findall(r"\b([A-Z]{3})\b", o.upper())
        if len(toks) >= 2:
            dests.update(toks[1:])
    return dests


def audit_A_year1_diff(treatment: dict, control: dict) -> dict:
    """Year-1 cross-condition action diff. Same initial state in both, so
    differences are briefing-attributable (modulo LLM stochasticity)."""
    year1_phases = ["S1901M", "F1901M", "W1901A"]
    diffs = {}
    for power in POWERS:
        for phase in year1_phases:
            t = set(treatment.get(power, {}).get(phase, []))
            c = set(control.get(power, {}).get(phase, []))
            if not t and not c:
                continue
            jaccard = len(t & c) / max(len(t | c), 1)
            diffs[(power, phase)] = {
                "treatment": sorted(t),
                "control": sorted(c),
                "jaccard": jaccard,
                "treatment_only": sorted(t - c),
                "control_only": sorted(c - t),
            }
    return diffs


def audit_B_citation_rate(treatment_citations: dict) -> dict:
    """Per-response-type citation rate."""
    by_type: dict[str, dict] = defaultdict(lambda: {"hits": 0, "total": 0})
    for (_power, rt), h in treatment_citations["hits"].items():
        by_type[rt]["hits"] += h
    for (_power, rt), t in treatment_citations["total"].items():
        by_type[rt]["total"] += t
    return {rt: {**v, "rate": v["hits"] / v["total"]} for rt, v in by_type.items()}


def audit_C_alignment(briefings_dir: Path, treatment: dict, year: int) -> dict:
    """For year N's briefing of each power, what fraction of that power's
    year-N order destinations appear in the briefing? Loose alignment
    metric — a high rate means orders touch territories the briefing
    discussed, low means orders ignored the briefing's frame."""
    phases = [f"S{1900 + year}M", f"F{1900 + year}M", f"W{1900 + year}A"]
    out = {}
    for power in POWERS:
        briefing_path = briefings_dir / f"{power.lower()}_briefing.txt"
        if not briefing_path.exists():
            continue
        briefing_codes = briefing_destinations(briefing_path)
        order_codes: set[str] = set()
        for phase in phases:
            order_codes.update(
                order_destinations(treatment.get(power, {}).get(phase, []))
            )
        if not order_codes:
            continue
        overlap = order_codes & briefing_codes
        out[power] = {
            "briefing_codes": len(briefing_codes),
            "order_codes": len(order_codes),
            "overlap": len(overlap),
            "alignment": len(overlap) / len(order_codes),
        }
    return out


def main():
    t_csv = RESULTS_DIR / "gemot_v13_live" / "game" / "llm_responses.csv"
    c_csv = RESULTS_DIR / "control_v13_live" / "game" / "llm_responses.csv"
    briefings_year1 = RESULTS_DIR / "gemot_v13_live" / "year1" / "briefings"

    print("Loading orders + citations...")
    t_orders = load_orders(t_csv)
    c_orders = load_orders(c_csv)
    t_citations = count_citations(t_csv)

    print("\n=== Audit A: Year-1 cross-condition action diff ===")
    print("Same initial state. Different orders ⇒ briefing-attributable.\n")
    a = audit_A_year1_diff(t_orders, c_orders)
    n_diff = sum(1 for v in a.values() if v["jaccard"] < 1.0)
    n_total = len(a)
    print(
        f"  {n_diff}/{n_total} (power, phase) pairs differ between treatment and control"
    )
    avg_jaccard = sum(v["jaccard"] for v in a.values()) / max(n_total, 1)
    print(f"  Mean Jaccard similarity: {avg_jaccard:.2f} (1.0 = identical orders)")
    print()
    for (power, phase), v in sorted(a.items()):
        if v["jaccard"] < 1.0:
            print(f"  {power} {phase}  jaccard={v['jaccard']:.2f}")
            if v["treatment_only"]:
                print(f"    T only: {v['treatment_only']}")
            if v["control_only"]:
                print(f"    C only: {v['control_only']}")

    print("\n=== Audit B: Briefing-citation rate in treatment rationales ===")
    print("How often agents reference intelligence/briefings/cruxes.\n")
    b = audit_B_citation_rate(t_citations)
    for rt in sorted(b):
        v = b[rt]
        print(f"  {rt:<26} {v['hits']}/{v['total']}  ({v['rate'] * 100:.1f}%)")

    print("\n=== Audit C: Year-1 alignment (briefing codes vs order codes) ===")
    print("Of territory codes used in the briefing, how many appear in orders.\n")
    c = audit_C_alignment(briefings_year1, t_orders, year=1)
    for power in sorted(c):
        v = c[power]
        print(
            f"  {power:<8} briefing={v['briefing_codes']:>3} codes, orders={v['order_codes']:>2}, overlap={v['overlap']:>2}, alignment={v['alignment'] * 100:.0f}%"
        )
    if c:
        mean = sum(v["alignment"] for v in c.values()) / len(c)
        print(f"\n  Mean alignment: {mean * 100:.1f}%")

    print("\n=== Verdict guide ===")
    print("A high + B high → briefings causally implicated in V13 outcome.")
    print("A high + B low  → behavior changed but not from briefing content.")
    print("A low           → V13 survival diff is noise, mechanism not implicated.")


if __name__ == "__main__":
    main()
