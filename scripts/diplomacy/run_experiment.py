#!/usr/bin/env python3
"""
run_experiment.py - Run iterative AI Diplomacy experiment with gemot intelligence.

Replaces the T3C pipeline with gemot's deliberation analysis.
Each year: run game -> analyze messages via gemot -> inject briefings -> next year.

Usage:
    # Gemot experiment (with briefings from Year 2+)
    python run_experiment.py --years 7 --name gemot_v1 --seed 2026

    # Control (same seed, no briefings)
    python run_experiment.py --years 7 --name control_v1 --seed 2026 --skip-analysis

Requirements:
    - AI Diplomacy repo at ~/Documents/AI_Diplomacy with venv
    - gemot binary built (go build -o gemot .)
    - GEMOT_API_SECRET and ANTHROPIC_API_KEY env vars (or gemot .env)
    - GEMOT_LIVE_URL env var (default: https://gemot.dev/mcp)
"""

import argparse
import json
import os
import subprocess
from pathlib import Path

AI_DIPLOMACY_DIR = Path.home() / "Documents" / "AI_Diplomacy"
GEMOT_DIR = Path.home() / "Documents" / "gemot"
DEFAULT_BASE_PROMPTS = AI_DIPLOMACY_DIR / "ai_diplomacy" / "prompts_simple"
POWERS = ["austria", "england", "france", "germany", "italy", "russia", "turkey"]


def run_diplomacy_year(
    year: int,
    run_dir: Path,
    max_year: int,
    prompts_dir: Path = None,
    models: str = None,
    seed: int = 42,
):
    """Run AI Diplomacy for one year using --pause_after_year."""
    game_year = 1900 + year
    cmd = [
        "python",
        "lm_game.py",
        "--max_year",
        str(max_year),
        "--pause_after_year",
        str(game_year),
        "--num_negotiation_rounds",
        "2",
        "--run_dir",
        str(run_dir),
        "--seed_base",
        str(seed),
    ]

    if models:
        cmd.extend(["--models", models])
    else:
        cmd.extend(["--models", ",".join(["openai:gpt-4o-mini"] * 7)])

    if prompts_dir:
        cmd.extend(["--prompts_dir", str(prompts_dir)])

    full_cmd = f"source {AI_DIPLOMACY_DIR}/.venv/bin/activate && {' '.join(cmd)}"

    print(f"\n{'=' * 60}")
    print(f"Running AI Diplomacy: Year {year} ({game_year})")
    print(f"{'=' * 60}")

    result = subprocess.run(
        full_cmd,
        shell=True,
        executable="/bin/bash",
        cwd=AI_DIPLOMACY_DIR,
        capture_output=True,
        text=True,
    )

    if result.returncode != 0:
        print(f"STDOUT: {result.stdout[-2000:] if result.stdout else 'None'}")
        print(f"STDERR: {result.stderr[-2000:] if result.stderr else 'None'}")
        raise RuntimeError(f"AI Diplomacy game failed for year {year}")

    print(f"Year {year} completed")
    return run_dir / "lmvsgame.json"


def generate_gemot_briefings(
    game_json: Path,
    year: int,
    output_dir: Path,
    state_file: Path = None,
    experiment_name: str = None,
):
    """Run gemot analysis on game messages and output briefing files.

    Powers are processed in parallel by the Go script. If some fail or the
    process times out, we still use whatever briefings were written to disk.
    Persistent deliberations are tracked via the state file across years.
    """
    output_dir.mkdir(parents=True, exist_ok=True)

    cmd = [
        "go",
        "run",
        "./scripts/diplomacy/",
        "--game",
        str(game_json),
        "--year",
        str(year),
        "--output",
        str(output_dir),
    ]

    if state_file:
        cmd.extend(["--state", str(state_file)])
    if experiment_name:
        cmd.extend(["--experiment", experiment_name])

    # Pass through gemot env vars
    env = os.environ.copy()
    if "GEMOT_LIVE_URL" not in env:
        env["GEMOT_LIVE_URL"] = "https://gemot.dev/mcp"

    print(f"\n  Generating gemot briefings for Year {year}...")

    timed_out = False
    try:
        result = subprocess.run(
            cmd, cwd=GEMOT_DIR, env=env, capture_output=True, text=True, timeout=3600
        )  # 1 hour timeout (powers run in parallel, ~5 min total)
        print(result.stderr)
        if result.returncode != 0:
            print(f"  WARNING: Go script exited with code {result.returncode}")
    except subprocess.TimeoutExpired as e:
        timed_out = True
        print("  WARNING: Briefing generation timed out after 60 min")
        if e.stderr:
            print(e.stderr[-500:] if len(e.stderr) > 500 else e.stderr)

    # Collect whatever briefings were written to disk (partial results are fine)
    briefings = {}
    for power in POWERS:
        bf = output_dir / f"{power}_briefing.txt"
        if bf.exists() and bf.stat().st_size > 100:
            briefings[power] = bf
            print(f"    {power}: {bf.stat().st_size:,} bytes")
        else:
            print(f"    WARNING: {power} briefing missing or empty")

    if not briefings:
        raise RuntimeError("No briefings produced at all")

    status = "timed out" if timed_out else "complete"
    print(f"  Briefing generation {status}: {len(briefings)}/7 powers")
    return briefings


def inject_briefings(
    briefings: dict, base_prompts: Path, output_dir: Path, year: int
) -> Path:
    """Inject briefings into power-specific system prompts."""
    output_dir.mkdir(parents=True, exist_ok=True)

    # Copy all base prompt files
    for src in base_prompts.iterdir():
        if src.is_file():
            (output_dir / src.name).write_text(src.read_text())

    base_system = base_prompts / "system_prompt.txt"
    if not base_system.exists():
        raise FileNotFoundError(f"Base system prompt not found: {base_system}")

    original = base_system.read_text()

    for power in POWERS:
        if power not in briefings:
            print(f"    WARNING: no briefing for {power}, using base prompt")
            continue

        briefing_content = briefings[power].read_text(errors="replace")
        injected = f"""{original}

=== YOUR PRIVATE DIPLOMATIC INTELLIGENCE BRIEFING (Year {year}) ===
The following analysis is based on YOUR diplomatic communications only.
Other powers have their own intelligence based on their own communications.

IMPORTANT: When making strategic decisions, explicitly note when you are using
insights from this briefing. For example:
- "Based on my intelligence showing strong alignment with Russia, I will..."
- "The briefing identifies Germany as a swing power, so I should..."
- "Given the high controversy score on Mediterranean strategies, I will..."

This helps track how diplomatic intelligence influences your strategy.

{briefing_content}
=== END BRIEFING ===
"""
        (output_dir / f"{power}_system_prompt.txt").write_text(injected)
        print(f"    Injected briefing for {power}")

    return output_dir


def run_experiment(
    name: str,
    num_years: int,
    base_prompts: Path = None,
    models: str = None,
    skip_analysis: bool = False,
    seed: int = 42,
):
    """Run the full iterative experiment."""
    experiment_dir = AI_DIPLOMACY_DIR / "results" / name
    experiment_dir.mkdir(parents=True, exist_ok=True)

    if base_prompts is None:
        base_prompts = DEFAULT_BASE_PROMPTS

    print(f"\n{'=' * 60}")
    print(f"Starting Gemot Diplomacy Experiment: {name}")
    print(f"Years: {num_years}, Seed: {seed}")
    print(f"Output: {experiment_dir}")
    print(f"Analysis: {'SKIP (control)' if skip_analysis else 'gemot'}")
    print(f"{'=' * 60}")

    game_dir = experiment_dir / "game"
    current_prompts = None
    max_year = 1900 + num_years
    state_file = experiment_dir / "deliberation_state.json"

    for year_num in range(1, num_years + 1):
        game_year = 1900 + year_num
        year_seed = seed + (game_year - 1901)

        print(f"\n\n{'#' * 60}")
        print(f"# YEAR {year_num} ({game_year}) - seed={year_seed}")
        print(f"{'#' * 60}")

        game_json = run_diplomacy_year(
            year=year_num,
            run_dir=game_dir,
            max_year=max_year,
            prompts_dir=current_prompts,
            models=models,
            seed=year_seed,
        )

        if skip_analysis:
            print("\n  Skipping analysis (control run)")
            continue

        # Generate gemot briefings
        briefings_dir = experiment_dir / f"year{year_num}" / "briefings"
        try:
            briefings = generate_gemot_briefings(
                game_json, year_num, briefings_dir, state_file, name
            )
        except Exception as e:
            print(f"\n  WARNING: Briefing generation failed: {e}")
            print(f"  Continuing without briefings for Year {year_num + 1}")
            continue

        if not briefings:
            print(f"\n  No briefings generated for Year {year_num}")
            continue

        # Inject briefings for next year
        if year_num < num_years:
            prompts_dir = experiment_dir / f"year{year_num}" / "prompts"
            current_prompts = inject_briefings(
                briefings,
                base_prompts,
                prompts_dir,
                year_num,
            )

        print(f"\n  Year {year_num} complete!")

    # Final summary
    print(f"\n\n{'=' * 60}")
    print(f"Experiment Complete: {name}")
    print(f"{'=' * 60}")

    game_json = game_dir / "lmvsgame.json"
    if game_json.exists():
        with open(game_json) as f:
            game_data = json.load(f)

        last_phase = game_data["phases"][-1]
        if "state" in last_phase and "centers" in last_phase["state"]:
            print(f"\nFinal Supply Centers (after {last_phase['name']}):")
            centers = last_phase["state"]["centers"]
            for power, scs in sorted(centers.items(), key=lambda x: -len(x[1])):
                print(f"  {power}: {len(scs)} SCs")

            # Compare with control if it exists
            control_json = (
                AI_DIPLOMACY_DIR / "results" / "control_v2" / "game" / "lmvsgame.json"
            )
            if control_json.exists():
                with open(control_json) as f:
                    control = json.load(f)
                ctrl_last = control["phases"][-1]
                if "state" in ctrl_last and "centers" in ctrl_last["state"]:
                    print("\nControl Comparison:")
                    ctrl_centers = ctrl_last["state"]["centers"]
                    for power in sorted(centers.keys()):
                        gemot_n = len(centers.get(power, []))
                        ctrl_n = len(ctrl_centers.get(power, []))
                        diff = gemot_n - ctrl_n
                        arrow = "+" if diff > 0 else "" if diff == 0 else ""
                        print(
                            f"  {power}: {gemot_n} (control: {ctrl_n}, {arrow}{diff})"
                        )

    print(f"\nResults saved to: {experiment_dir}")


def main():
    parser = argparse.ArgumentParser(
        description="Run iterative AI Diplomacy experiment with gemot intelligence",
    )
    parser.add_argument("--name", "-n", required=True, help="Experiment name")
    parser.add_argument(
        "--years", "-y", type=int, default=7, help="Number of years (default: 7)"
    )
    parser.add_argument("--prompts", default=None, help="Base prompts directory")
    parser.add_argument(
        "--models", default=None, help="Comma-separated model names for 7 powers"
    )
    parser.add_argument(
        "--skip-analysis", action="store_true", help="Skip gemot analysis (control run)"
    )
    parser.add_argument(
        "--seed", type=int, default=2026, help="Random seed (default: 2026)"
    )

    args = parser.parse_args()
    base_prompts = Path(args.prompts) if args.prompts else None

    run_experiment(
        name=args.name,
        num_years=args.years,
        base_prompts=base_prompts,
        models=args.models,
        skip_analysis=args.skip_analysis,
        seed=args.seed,
    )


if __name__ == "__main__":
    main()
