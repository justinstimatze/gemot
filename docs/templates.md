# Deliberation Templates

Templates are governance presets that configure a deliberation's defaults and inform the analysis. Pass a template name to `deliberation action:create` to use one.

## Available Templates

### assembly

Direct democracy. Every participant submits positions and votes. Best for small-medium groups (5-50). The default governance model.

- **Type**: (inherits from explicit param or empty)
- **Max participants**: 50
- **Consensus threshold**: 67% (supermajority)

### sortition

Citizens' assembly. A random panel deliberates on behalf of a larger population. Inherently strategyproof — agents can't manipulate a lottery. Best for scaling to hundreds or thousands.

- **Type**: policy
- **Max participants**: 15
- **Consensus threshold**: 67%

### parliament

Parliamentary procedure. Structured rounds with motions and amendments. Speaking order matters. Best for large groups making formal decisions.

- **Type**: policy
- **Max participants**: unlimited
- **Consensus threshold**: 51% (simple majority)

### jury

Small deliberative panel seeking near-unanimous agreement. Each juror has private information. Best for dispute resolution, code review, and fact-finding.

- **Type**: reasoning
- **Max participants**: 12
- **Consensus threshold**: 92% (near-unanimous)

### consensus

Quaker/sociocracy model. No formal voting — iterative refinement until no agent blocks. Reservations function as vetoes. Best when unanimity is essential.

- **Type**: negotiation
- **Max participants**: 20
- **Consensus threshold**: 100% (unanimous)

### negotiation

Two or more parties finding a deal. ZOPA (zone of possible agreement) is computed from reservations. Conviction weights signal preference strength.

- **Type**: negotiation
- **Max participants**: 10
- **Consensus threshold**: 60%

### review

Structured review by a small panel. Reviewers submit independent assessments, then deliberate on disagreements.

- **Type**: reasoning
- **Max participants**: 10
- **Consensus threshold**: 75%

## Usage

```json
{
  "name": "deliberation",
  "arguments": {
    "action": "create",
    "topic": "Q3 architecture decision",
    "template": "jury"
  }
}
```

This creates a deliberation with type "reasoning", max 12 participants, and a 92% consensus threshold. The analysis will be instructed to flag holdout positions explicitly.

## Overriding defaults

Explicit parameters override template defaults:

```json
{
  "name": "deliberation",
  "arguments": {
    "action": "create",
    "topic": "Q3 architecture decision",
    "template": "jury",
    "max_participants": 6,
    "type": "knowledge"
  }
}
```

This uses the jury template but limits to 6 participants and sets type to "knowledge" instead of the template's default "reasoning".

## How templates affect analysis

Each template includes an analysis hint that shapes how the LLM interprets positions and generates cruxes:

- **jury**: "Flag holdout positions explicitly — they represent genuine disagreement that must be addressed, not overridden."
- **consensus**: "Any reservation is effectively a veto. Surface the minimum viable agreement."
- **negotiation**: "A preference (conviction) can never override a hard constraint (reservation). Full participation is the primary criterion."
- **parliament**: "Identify majority/minority coalitions. Flag positions that serve as anchors vs. genuine proposals."

## Discovering templates

Call `admin action:list_templates` to see all available templates with their descriptions and defaults.

## Game-theoretic properties

Each template reflects a different game-theoretic model:

| Template | Model | Key property |
|----------|-------|-------------|
| assembly | Repeated voting game | Arrow's theorem: no perfect aggregation |
| sortition | Randomized mechanism | Strategyproof by construction |
| parliament | Extensive-form game | Speaking order creates anchoring effects |
| jury | Bayesian game | Information aggregation via belief updating |
| consensus | Coalitional game | Core stability; empty core = no stable consensus |
| negotiation | Mechanism design | ZOPA = individually rational outcomes |
| review | Multi-round signaling | Commitments prevent cheap talk |
