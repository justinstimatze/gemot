package deliberation

import "sort"

// Template defines a governance preset for deliberations.
// Phase 1: sets defaults and informs LLM analysis. No rule enforcement.
type Template struct {
	Name               string         `json:"name"`
	Description        string         `json:"description"`
	DefaultType        string         `json:"default_type"`
	DefaultMaxPart     int            `json:"default_max_participants"`
	SuggestedThreshold float64        `json:"suggested_threshold"`
	AnalysisHint       string         `json:"analysis_hint"`
	DefaultRules       map[string]any `json:"default_rules,omitempty"`
}

var templates = map[string]Template{
	"assembly": {
		Name:               "assembly",
		Description:        "Direct democracy. Every participant submits positions and votes. Best for small-medium groups (5-50). The default governance model.",
		DefaultType:        "",
		DefaultMaxPart:     50,
		SuggestedThreshold: 0.67,
		AnalysisHint:       "Direct democracy: every participant's voice carries equal weight. Seek supermajority consensus. Report minority positions fairly — they may represent important dissent, not just outliers.",
		DefaultRules:       map[string]any{"min_participants": 3},
	},
	"sortition": {
		Name:               "sortition",
		Description:        "Citizens' assembly. A random panel deliberates on behalf of a larger population. Inherently strategyproof — agents can't manipulate a lottery. Best for scaling to hundreds or thousands.",
		DefaultType:        "policy",
		DefaultMaxPart:     15,
		SuggestedThreshold: 0.67,
		AnalysisHint:       "Citizens' assembly by sortition: this panel was randomly selected to represent a larger population. Weight representativeness — are all perspectives in the population reflected? Flag if the panel's composition appears skewed.",
		DefaultRules:       map[string]any{"min_participants": 10},
	},
	"parliament": {
		Name:               "parliament",
		Description:        "Parliamentary procedure. Structured rounds with motions and amendments. Speaking order matters. Best for large groups making formal decisions. Simple majority threshold.",
		DefaultType:        "policy",
		DefaultMaxPart:     0,
		SuggestedThreshold: 0.51,
		AnalysisHint:       "Parliamentary procedure: identify majority and minority coalitions. Amendments modify existing proposals rather than introducing competing ones. Look for log-rolling (vote-trading across issues). Flag positions that serve as anchors vs. genuine proposals.",
		DefaultRules:       map[string]any{"min_participants": 5, "cooling_period_minutes": 60, "position_cost": 5},
	},
	"jury": {
		Name:               "jury",
		Description:        "Small deliberative panel seeking near-unanimous agreement. Each juror has private information. Best for dispute resolution, code review, and fact-finding. High consensus threshold.",
		DefaultType:        "reasoning",
		DefaultMaxPart:     12,
		SuggestedThreshold: 0.92,
		AnalysisHint:       "Jury deliberation: near-unanimity required. Flag any holdout positions explicitly — they represent genuine disagreement that must be addressed, not overridden. Identify what information would change a holdout's mind. The goal is shared understanding, not majority rule.",
		DefaultRules:       map[string]any{"min_participants": 6, "cooling_period_minutes": 15},
	},
	"consensus": {
		Name:               "consensus",
		Description:        "Quaker/sociocracy model. No formal voting — iterative refinement until no agent blocks. Reservations function as vetoes. Best when unanimity is essential and the group is willing to invest time.",
		DefaultType:        "negotiation",
		DefaultMaxPart:     20,
		SuggestedThreshold: 1.0,
		AnalysisHint:       "Consensus process: any reservation is effectively a veto. Do not report majority/minority — instead identify what modifications would remove each blocking concern. The goal is a proposal no one blocks, not one everyone loves. Surface the minimum viable agreement.",
		DefaultRules:       map[string]any{"min_participants": 3, "cooling_period_minutes": 30},
	},
	"negotiation": {
		Name:               "negotiation",
		Description:        "Two or more parties finding a deal. ZOPA (zone of possible agreement) is computed from reservations. Conviction weights signal preference strength. Best for scheduling, resource allocation, contract terms.",
		DefaultType:        "negotiation",
		DefaultMaxPart:     10,
		SuggestedThreshold: 0.60,
		AnalysisHint:       "Negotiation: identify each party's interests (not just positions), reservation values (walk-away points), and the zone of possible agreement. Propose package deals that trade across issues. A preference (conviction) can never override a hard constraint (reservation). Full participation is the primary criterion; individual preferences are secondary.",
		DefaultRules:       map[string]any{"min_participants": 2},
	},
	"review": {
		Name:               "review",
		Description:        "Structured review by a small panel. Reviewers submit independent assessments, then deliberate on disagreements. Best for code review, document review, and quality assessment.",
		DefaultType:        "reasoning",
		DefaultMaxPart:     10,
		SuggestedThreshold: 0.75,
		AnalysisHint:       "Review panel: distinguish blocking concerns (must be addressed before approval) from suggestions (nice-to-have improvements). Identify where reviewers agree the work is sound. Flag security or correctness issues with higher weight than style preferences.",
		DefaultRules:       map[string]any{"min_participants": 2},
	},
	"roberts_rules": {
		Name:               "roberts_rules",
		Description:        "Full parliamentary procedure. Motions require a second before debate. Amendments modify existing motions. Call the question forces a vote. Point of order challenges procedural violations.",
		DefaultType:        "policy",
		DefaultMaxPart:     100,
		SuggestedThreshold: 0.51,
		AnalysisHint:       "Parliamentary procedure: motions require a second to be debatable. Unsupported motions are tabled. Amendments modify the original motion — analyze the amended version. Report the procedural state: which motions are on the floor, which are tabled, which have been decided.",
		DefaultRules: map[string]any{
			"min_participants":       5,
			"cooling_period_minutes": 15,
			"position_cost":          3,
			"require_second":         true,
			"allow_amendments":       true,
			"speaking_time_limit":    500,
		},
	},
	"debate": {
		Name:               "debate",
		Description:        "Adversarial debate scaffold: advocates argue opposing sides of a binary question before an impartial jury, with a moderator-role participant acting as referee. Best for contested yes/no calls where structured advocacy — someone whose job is to make the strongest possible case on each side — surfaces sharper arguments than a single agent reasoning alone. Modeled on Antikythera Agentworld's referee/debate/jury scaffold (Bratton & Poliks, 2026).",
		DefaultType:        "reasoning",
		DefaultMaxPart:     14,
		SuggestedThreshold: 0.51,
		AnalysisHint:       "Adversarial debate scaffold: submit_position with group \"for\" or \"against\" marks an advocate arguing that side — judge those positions on argument quality and evidence, not as a sincere vote of belief, and don't count an advocate's own vote toward the verdict for their side. A participant invited with role \"moderator\" is the referee: weigh their calls on fairness, rule violations, and whether each side got a fair hearing, not on the substance of the question. Every other (ungrouped) participant is the jury; the verdict is the jury's vote, not the advocates'. Report which side's arguments survived rebuttal and flag anything an advocate said that reads as sincere analysis smuggled in as persuasion, or vice versa — role-assigned advocacy can bias a jury if the line blurs (see docs/research-lineage.md).",
		DefaultRules:       map[string]any{"min_participants": 5},
	},
	"freeform": {
		Name:               "freeform",
		Description:        "Unstructured discussion. No procedure, coalitions, or ZOPA — agents just talk and converge. The control for measuring what gemot's structure adds.",
		DefaultType:        "",
		DefaultMaxPart:     20,
		SuggestedThreshold: 0.5,
		AnalysisHint:       "Plain discussion. Summarize what was said and what the group converged on; report unresolved disagreement. Impose no procedural structure.",
		DefaultRules:       map[string]any{"min_participants": 2},
	},
}

// GetTemplate returns a template by name.
func GetTemplate(name string) (Template, bool) {
	t, ok := templates[name]
	return t, ok
}

// ListTemplates returns all available templates sorted by name.
func ListTemplates() []Template {
	result := make([]Template, 0, len(templates))
	for _, t := range templates {
		result = append(result, t)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}
