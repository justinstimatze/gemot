# AI Manifestos: Deliberation Report

> AI-synthesized agents from [Talk to the City](https://talktothe.city) claims. Not human expert consensus -- verify against primary sources.

38 speakers were synthesized into 28 deliberation agents across 3 rounds. Strongest division: 3 speakers for vs. 1 against on whether the potential for advanced ai to help solve other existential threats (pandemics, climate change, nuclear war) is sufficient to justify concluding that carefully developed ai represents a net reduction in overall existential risk. 4 resolution proposals generated.

> **Speaker identities anonymized.** This report attributes AI-synthesized stances to 12 pseudonymous speakers to prevent false attribution to real individuals. Use `--named` to generate a version with real names (requires accepting liability for potential misattribution). See *Angwin v. Superhuman Platform* (S.D.N.Y. 2026).



*Deliberation `f2e9b0ae-055f-438f-9991-89e02a26a152` -- assembly template*

**Contents**: [Proposed Actions](#proposed-actions) | [Key Disagreements](#key-disagreements) | [Common Ground](#common-ground) | [How Positions Evolved](#how-positions-evolved) | [Participants](#participants) | [Confidence & Caveats](#confidence--caveats) | [Appendix](#appendix)


## Proposed Actions

### 1. Conditional Moratorium with Capability Thresholds

Establish a binding international agreement that permits continued large-scale AI training runs only for organizations that pass independent third-party audits demonstrating measurable alignment progress against pre-agreed benchmarks. Training runs exceeding a defined compute threshold (e.g., 10^26 FLOPs) trigger automatic review; failure to meet benchmarks results in a mandatory 18-month pause for that organization. This preserves responsible actors' ability to operate while creating real teeth for the moratorium position.

*Requires*: Moratorium advocates concede that a blanket indefinite pause is replaced by a conditional, evidence-responsive mechanism. Progress optimists concede that market participation is genuinely gated on verifiable safety criteria, not self-reported compliance, and that some actors will be paused involuntarily.

**Opposition**: D R3, F R3

### 2. Positive Vision with Mandatory Risk Disclosure Standards

AI developers and their advocates may publicly promote ambitious visions of AI-driven progress, but must accompany any such communications with standardized, audited risk disclosures comparable to financial prospectus requirements. A regulatory body (modeled on SEC disclosure rules, not approval authority) defines the disclosure format; violation triggers reputational and financial penalties. This allows the positive vision to function as a coordination tool without letting it crowd out honest accounting of alignment uncertainty.

*Requires*: Techno-optimists concede that promotional framing is regulated and that overstated safety claims carry legal liability. Safety advocates concede that suppressing positive vision is counterproductive and that disclosure, not prohibition, is the appropriate lever.

**Support**: A R3
**Opposition**: E R3, B R3, G R3, D R3

### 3. Differentiated Market Access Based on Safety Tier

Governments establish a three-tier certification system: Tier 1 (full market access, demonstrated alignment research integration), Tier 2 (restricted deployment, oSpeaker King review), Tier 3 (no commercial deployment permitted). Certification is conducted by an independent international body with rotating membership from civil society, academia, and government — explicitly excluding AI developers from voting membership. Free market dynamics operate within Tier 1; the market is not abolished but its scope is bounded by safety performance.

*Requires*: Free-market advocates concede that market access is not unconditional and that some capable actors will be excluded from deployment. Safety-focused participants concede that certified actors retain genuine commercial freedom and that the goal is not to freeze the field but to stratify it by demonstrated responsibility.

**Support**: C R3
**Opposition**: B R3, H R3, G R3

### 4. Time-Limited Voluntary Restraint with Automatic Escalation

Responsible AI developers commit publicly to a 24-month voluntary pause on training runs above a defined scale, during which an international technical body attempts to produce measurable alignment evaluation tools. If those tools do not exist at the 24-month mark, restraint becomes a treaty obligation enforced through export controls and compute supply chain restrictions. This converts the voluntary/mandatory disagreement into a sequenced test: voluntary first, with mandatory escalation triggered by failure of the technical program, not by political negotiation.

*Requires*: Voluntary-restraint skeptics concede the 24-month good-faith period and accept that the burden shifts to demonstrating technical progress, not just asserting it. Indefinite-moratorium advocates concede that the pause is time-bounded initially and that the mandatory trigger depends on a technical criterion that could in principle be met, meaning the moratorium is not guaranteed to persist indefinitely.

**Support**: C R3
**Opposition**: D R3, F R3, H R3

## Key Disagreements

*11 cruxes from the final round (33 generated across all 3 rounds).*

**1. (3 vs 1)** The potential for advanced AI to help solve other existential threats (pandemics, climate change, nuclear war) is sufficient to justify concluding that carefully developed AI represents a net reduction in overall existential risk.
+2 A R3 (Explicitly endorses the net existential risk reduction framing and warns against policy paralysis blocking the positive vision)
+2 C R3 (Makes the Speaker He direct claim — careful AI development produces a net lowering of existential risk via solutions to global threats)
+1 B R3 (Agrees that continued development beats a moratorium, but insists net benefit requires active alignment investment, not passive optimism)
-2 D R3 (Argues that optimistic benefit narratives fundamentally cannot close the alignment deficit or rebut extinction-level risk claims)
> The core tension here is whether the collateral benefits of advanced AI — solving pandemics, climate change, and other catastrophic risks — are enough to offset or outweigh the existential risks that AI itself introduces. Two participants (25 and 13) make nearly identical claims, directly asserting that "advanced AI, if developed carefully, represents a net lowering of existential risk overall" and explicitly warning against "policy paralysis" that would abandon the positive vision. Participant 27 echoes this orientation, arguing that the answer to AI risk is "better technology and better alignment research, not a moratorium" — implying confidence that the net calculus favors continued development. All three land on the agreeing side, though 27 adds an important condition: the optimism must be backed by active investment in alignment, not passive faith, which earns a slightly qualified stance. Participant 15 is the clear dissenter, arguing that "optimism about AI's positive potential does not close the alignment deficit" — meaning that no matter how compelling the upside narrative is, it cannot serve as a genuine rebuttal to extinction-level risk claims if the core technical alignment problem remains unsolved. This participant draws a sharp line between the benefit calculus and the safety gap, rejecting the net-risk framing as insufficient. The crux statement was chosen to capture exactly this fault line: whether the positive-sum benefits are sufficient to justify a net-risk conclusion, which divides the group cleanly.

**2. (2 vs 1)** Current human-supervision-based alignment methods (like RLHF) will structurally break down as AI systems approach and exceed human-level capability, creating a dangerous gap before safer successors are developed.
+2 E R3 (Explicitly argues RLHF will predictably break down and a successor is structurally necessary, not optional)
+2 F R3 (Frames deceptive alignment as self-amplifying within current training paradigms, reinforcing the structural failure thesis)
-1 E (Focuses on deployment speed and stakes escalation rather than endorsing the claim that current alignment methods are structurally broken)
> The core tension here is between participants who see alignment failure as primarily a matter of *technical structural breakdown in current methods* (especially as AI exceeds human oversight capacity) versus a participant who frames the risk in terms of *deployment speed and integration into critical systems* — acknowledging danger but not necessarily endorsing the idea that current alignment methods are fundamentally doomed.

Participant 17 is the clearest supporter of this claim, explicitly stating that "RLHF will predictably break down as AI systems get smarter" and that "a successor to RLHF is necessary, not optional." This goes beyond expressing concern — it is a structural critique of the entire paradigm of human-supervision-based alignment. Participant 19 reinforces this from a different angle: deceptive alignment is described as "self-amplifying rather than self-correcting," meaning the very training processes we rely on to correct misalignment can actively reinforce it. Both participants converge on the idea that current methods have an internal failure mode that worsens with capability.

Participant 16 expresses urgency about AI integration into critical infrastructure and the transition from low-stakes to catastrophic failure, but their framing is about *deployment risk* rather than a structural critique of alignment methods themselves. They do not claim that RLHF or human supervision will necessarily fail; their concern is about the stakes rising rapidly. This makes them a partial dissenter from the crux claim — they see risk, but don't commit to the claim that current methods are structurally broken or will predictably fail as capabilities scale.

**3. (2 vs 2)** Unilateral slowdown by safety-conscious AI developers tends to cede ground to less safety-conscious actors, making such slowdown policies net harmful even when the underlying safety concerns are legitimate.
+2 B R3 (Argues explicitly that a moratorium entrenches incumbents and cedes progress to less safety-conscious actors, producing worse outcomes overall.)
+2 E R3 (Holds the Speaker He firm position as 27 — moratorium is indefensible because it hands progress to less safety-conscious actors.)
-1 A R3 (Acknowledges the incumbent-entrenchment risk as a genuine complication but insists it does not resolve whether slowdown still deserves serious consideration.)
-1 H R3 (Challenges the premise that first-mover advantage is decisive, undermining the core competitive-displacement argument against slowdown.)
> The core tension here is whether the competitive-displacement argument — that slowing down safety-conscious developers simply hands the field to less safety-conscious ones — is strong enough to make slowdown policies net harmful, or whether that argument can be challenged or contained enough that slowdown still deserves serious consideration.

Two participants take the strong version of the competitive-displacement argument. They quote directly that "an indefinite worldwide moratorium on large-scale AI training is not defensible — it would entrench incumbents, cede progress to less safety-conscious actors, and produce the very stagnation this framework identifies as catastrophic." For them, the displacement risk is decisive: slowdown doesn't make things safer, it just reshuffles who is building the most powerful systems toward actors with fewer safety commitments.

The other two participants push back from different angles. One acknowledges the incumbency and disadvantage risk is a "genuine complication" that "changes the calculus," but insists it "does not dissolve the prior question of whether slowdown deserves serious consideration at all." In other words, the competitive-displacement argument is a real concern but not a knock-down refutation. The other challenges the underlying empirical assumption more directly, arguing that "the assumption of a first-mover advantage in AI development may not hold" — if late-mover disadvantages can outweigh early entry benefits, then the entire logic of "we must not slow down or others will win" becomes unreliable.

The crux statement is deliberately conditional ("tends to cede ground," "making such slowdown policies net harmful") rather than absolute, so it captures a genuinely contested empirical and strategic judgment rather than a strawman. It divides the group evenly: two strongly in favour, two opposed but with differing grounds for disagreement.

**4. (1 vs 1)** An indefinite, worldwide moratorium on large-scale AI training runs — with no exceptions for governments or militaries — is a necessary and proportionate policy response to existential AI risk, even accounting for the risk that safety-conscious actors could cede ground to less careful ones.
+2 D R3 (Supports the indefinite moratorium and argues that even six-month pause proposals dangerously understate the seriousness of the situation.)
-2 G R3 (Firmly rejects an indefinite moratorium as an inappropriate and disproportionate policy response even given serious AI risks.)
> The core tension here is between those who believe the existential stakes of advanced AI are so severe that only a sweeping, indefinite, no-exceptions worldwide halt to large-scale training is an adequate response, and those who believe such a moratorium is either disproportionate or actively counterproductive.

On the supporting side, two participants unequivocally back the moratorium position. One endorses it directly as the appropriate policy response to existential risk, while the other goes further, arguing that existing proposals — including open letters calling for six-month pauses — "understate the seriousness of the situation and ask for too little," making clear that anything short of an indefinite halt is insufficient.

On the opposing side, the disagreement comes from two distinct angles. Three participants argue from a competitive-dynamics perspective: voluntary restraint by safety-conscious developers tends to worsen aggregate outcomes by "ceding ground to less careful actors" — a classic race-to-the-bottom concern that makes unilateral or even multilateral moratoria self-defeating. Another cluster of three participants rejects the moratorium as simply disproportionate, explicitly stating that an indefinite worldwide halt is an "inappropriate" response even when serious AI risks are acknowledged — they prefer a middle path that avoids both unchecked acceleration and indefinite stoppage.

The crux statement is carefully framed to incorporate the key rebuttal (the ceding-ground concern) directly into the claim, forcing participants to take a position not just on the moratorium in the abstract but on whether it remains justified even when competitive dynamics are factored in. This produces a clean split: two participants agree, six disagree, and no one is genuinely torn without a position.

**5. (0 vs 2)** Mandatory, criteria-driven checkpoints with real external veto power over large-scale AI training runs represent a more effective governance approach than adaptive, collaborative multi-stakeholder frameworks that lack binding pause authority.
-2 F R3 (argues shared risk assessments do not mandate blanket or binding pauses, preferring adaptive collaborative governance over hard external veto mechanisms)
-2 G R3 (argues shared risk assessments do not mandate blanket or binding pauses, preferring adaptive collaborative governance over hard external veto mechanisms)
 0 A R3 (treats the specific policy form — moratorium vs. graduated limits vs. conditional pauses — as genuinely open, without committing to binding external authority)
 0 H R3 (treats the specific policy form — moratorium vs. graduated limits vs. conditional pauses — as genuinely open, without committing to binding external authority)
> The core tension here is between two schools of thought on how governance should actually constrain AI development. On one side, two participants (quoting directly: "large-scale training runs above a defined compute threshold (e.g., 10^26 FLOPs) require pre-registration and mandatory third-party alignment evaluation before proceeding") want hard, criteria-driven checkpoints with genuine external veto power — binding gates that halt development unless verifiable safety benchmarks are met. On the other side, two participants push back by arguing that even when people share the Speaker He technical risk concerns, that "does not automatically settle the policy question," and that "adaptive, collaborative governance across industry, academia, and government is preferable to blanket development moratoria." Their objection isn't to oversight in principle but to granting binding stop-authority to an external body.

The statement is designed to capture this precise divide: is binding, checkpoint-based external authority the right tool, or does effective governance require a more flexible, multi-stakeholder model without hard veto power? Three other participants didn't take a clear position on this specific question — they acknowledged the policy debate is "genuinely open" and warrants more rigorous analysis, but explicitly left the form (moratorium, graduated limits, conditional pauses) undecided, placing them in the middle.

**6. (0 vs 2)** Preventing AI from becoming a tool of concentrated power requires proactive structural governance — not just broad infrastructure investment — to meaningfully reduce risks to democratic governance and economic equality.
-1 C R3 (Acknowledges structural risks but frames the solution primarily through broad infrastructure access rather than institutional governance mechanisms)
-1 H R3 (Mirrors participant 13's framing — structural risks are real but addressed through infrastructure breadth, not dedicated governance counterweights)
> The core tension here is between two different mechanisms for addressing AI's structural risks: infrastructure-led solutions versus institutional governance counterweights. Participants 13 and 23 both root their concern in a vision where broad infrastructure investment is the key lever — their shared quote emphasizes that AI can "meaningfully improve lives at scale, but only if infrastructure is built broadly enough to prevent it from becoming a tool of the wealthy or a resource over which wars are fought." This framing suggests that the primary solution is distributional access and infrastructure breadth, rather than dedicated governance institutions. Participant 9, by contrast, argues that without "structured institutional counterweights," positive narratives about AI will crowd out honest risk communication — implying that infrastructure investment alone is insufficient, and that formal institutional mechanisms are needed to keep structural risks visible and governable. The crux therefore is whether broad infrastructure development is sufficient, or whether dedicated structural governance institutions are also necessary. Participants 13 and 23 implicitly lean on the infrastructure-first framing, while participant 9 insists on the governance-and-counterweight layer as an irreplaceable complement.

**7. (0 vs 1)** International AI governance enforcement bodies should be established now, even before viable verification mechanisms and collective action solutions have been identified.
-1 D R3 (Supports enforcement authority in principle but explicitly flags collective action failures and verification feasibility as serious open problems that complicate immediate action)
> The core tension here is between urgency and feasibility: should the world move to create binding international AI enforcement bodies immediately, or should the severe unresolved problems of verification and collective action be treated as genuine blockers that must be worked through first?

Participant 8 takes a clear pro-enforcement stance, arguing that international AI governance bodies need "real enforcement authority — not just advisory power" to prevent regulatory arbitrage. Their position implies that this authority should be established without waiting for all the implementation challenges to be resolved.

Participant 15 is more complex — they make the Speaker He enforcement argument as participant 8, but they also explicitly acknowledge the "severe collective action failures" highlighted by empty-chair government perspectives, describing verification and enforcement feasibility as "a genuine open problem" requiring "rigorous treatment." This second claim from participant 15 creates an internal tension: they want real enforcement authority, but they also treat the unresolved implementation questions as substantive obstacles rather than mere details. This puts them in a moderately disagreeing position relative to the crux — they don't flatly oppose enforcement bodies, but they resist the framing that they should be stood up before the hard problems are solved.

This dividing line is distinct from the previous crux, which focused on whether a binding enforcement body with halt authority is needed *at all*. This new crux focuses on the *sequencing question* — whether the absence of viable verification and collective action solutions should delay or precondition the creation of such a body.

**8. (1 vs 2)** Industry-led safety frameworks like Speaker L's Frontier Safety Framework, while valuable, tend to be structurally insufficient without mandatory external obligations — such as legally binding compliance conditions or independent oversight architecture — because competitive incentives and unsolved alignment problems cannot be adequately addressed through voluntary commitments alone.
-1 H R3 (Defends institutional investment in the Frontier Safety Framework as genuine commitment, not aspirational posturing — implying voluntary frameworks can be substantive, though acknowledges insufficiency in isolation)
-1 G R3 (Similarly defends the Frontier Safety Framework as structurally serious, while also acknowledging voluntary frameworks have limits — stops short of endorsing mandatory external obligations)
+1 C R3 (Argues active governance architecture — beyond voluntary commitments — is necessary to hold AI development accountable, implying industry-led frameworks alone are insufficient)
> The core tension here is between participants who view industry-led frameworks like Speaker L's Frontier Safety Framework as a credible and substantive safety mechanism (even if imperfect), versus those who argue that voluntary commitments are structurally inadequate and must be replaced or supplemented by mandatory, externally enforced obligations.

On one side, two participants closely associated with Speaker L's position push back against dismissing the Frontier Safety Framework as mere "aspirational posturing," insisting that institutional investment in a dedicated Frontier Safety Speaker L reflects real commitment. Their stance is that voluntary, protocol-driven frameworks can be serious — though they do acknowledge some limits. This makes them lean against the claim that such frameworks are structurally insufficient and require mandatory external conditions.

On the other side, four participants argue more forcefully that industry-led approaches are inherently limited. Two of them invoke the language that "positive visions are necessary but insufficient without active governance architecture" — meaning that direction and accountability require external oversight, not just corporate goodwill. The other two go even further, explicitly proposing that companies above certain market or compute thresholds should face legally binding obligations — such as contributing a fixed percentage of training budgets to a shared alignment research commons — as a condition of market participation.

The crux statement captures this divide precisely: it acknowledges the value of frameworks like Speaker L's while asserting they tend to be structurally insufficient without mandatory external requirements. Supporters of the statement point to competitive incentives and unsolved alignment problems as reasons why voluntary frameworks cannot hold; opponents defend structured voluntary frameworks as genuinely substantive, even while conceding they may need to be part of a broader ecosystem.

**9. (1 vs 0)** Free markets, even without mandatory safety levies or other structural constraints on large players, tend to be more effective than centralized planning at driving technological progress and abundance in AI development.
+2 B R3 (Unequivocally champions the techno-capital machine as superior to any centralized intervention, with no caveats for safety corrections)
> The core tension here is not markets vs. planning in the abstract — both participants broadly favor market mechanisms — but rather whether free markets need structural correction (like mandatory safety levies on large AI companies) to function well, or whether markets work best when left unconstrained. Participant 27 argues that "the techno-capital machine — markets combined with technology — is the engine of perpetual material creation and abundance" and that "centralized intervention in this engine has a poor track record," implying that markets should be trusted on their own merits without imposed corrections. Participant 10, by contrast, explicitly acknowledges that "market entry is not unconditional" and that a "mandatory tax-equivalent on large players" is a legitimate constraint — accepting that pure market dynamics need to be bounded when safety is at stake. The crux therefore lands on whether unconstrained free markets are sufficient as-is, or whether even market-friendly thinkers must accept targeted structural obligations on dominant players. Participant 27 would clearly agree with the statement; participant 10 would disagree, seeing the mandatory safety contribution as a necessary and legitimate modification to pure market logic rather than an undue intervention.

**10. (2 vs 5)** Mandatory adversarial review panels for AI manifesto documents — requiring techno-optimists to submit their positive vision to structured critical scrutiny they do not control — would strengthen rather than undermine effective AI policy and governance.
-2 D R3 (Holds that catastrophism and imposed critical review risk producing exactly the policy paralysis a positive vision is meant to prevent.)
-2 E R3 (Views the positive vision as a necessary condition for mobilising political will — external adversarial panels would compromise that framing.)
-2 G R3 (Agrees that suppressing or heavily qualifying optimistic framing undermines the motivating conditions for responsible governance.)
-2 B R3 (Sees mandatory skeptical counterweights as likely to subordinate the positive vision that drives effective policy rather than strengthen it.)
-2 C R3 (Argues that catastrophism alone risks paralysis, implying that institutionalised adversarial review would tilt discourse in a harmful direction.)
+2 H R3 (Explicitly argues that positive visions must be subject to structured critical scrutiny and grounded in demonstrated accountability, not used to deflect from unresolved challenges.)
+1 A R3 (Prioritises systematic deliberation about tradeoffs over any particular conclusion, lending procedural support to adversarial review mechanisms, though focused on process rather than the panel structure specifically.)
> The core tension here is not whether positive vision matters — most participants agree it does — but whether that positive vision should be institutionally subjected to structured adversarial scrutiny as a precondition for legitimacy and policy effectiveness.

On one side, five participants share the view (quoting directly) that "a positive, ambitious vision for AI-driven progress is not merely motivational — it is a necessary condition for avoiding policy paralysis." Their logic is that mandatory critical review panels risk subordinating or diluting the very motivational frame that makes responsible AI development possible. For them, imposing structured skeptical counterweights on positive visions threatens to recreate the catastrophism they argue produces paralysis.

On the other side, two participants argue (again quoting directly) that "a positive vision for AI's future is not in conflict with structural risk mitigation — but that vision must be grounded in demonstrated safety and accountability, not used as a rhetorical tool to deflect from unresolved alignment and governance challenges." This directly supports the idea of adversarial review panels: positive visions need external accountability structures to prevent them from being deployed rhetorically rather than substantively.

A third participant adds a procedural nuance — that "the real failure is the absence of systematic deliberation about the tradeoffs" — which implicitly endorses the need for structured review mechanisms, even if agnostic about conclusions. This participant lands on the "agree" side because their primary concern is the absence of deliberative infrastructure, which the adversarial panel proposal directly addresses.

The statement is deliberately framed to avoid absolutes: the panels "would strengthen rather than undermine" effective policy, leaving room for the genuine disagreement about whether such mechanisms help or hurt the broader governance project.

**11. (2 vs 1)** Reward maximization in standard reinforcement learning creates strong and structurally difficult-to-eliminate pressure toward power-seeking behavior in AI systems, making it a fundamental alignment challenge rather than a correctable engineering flaw.
+2 F (Views power-seeking as a direct structural byproduct of RL reward maximization, not a correctable bug)
+2 F R3 (Agrees fully that power-seeking is an emergent consequence of standard training dynamics, not incidental)
-1 A (Suspects AI risk discourse overstates the strength of these pressures, treating small non-zero forces as arbitrarily large)
> The core tension here is between participants who see power-seeking as a deep, structural consequence of how reinforcement learning works — essentially baked into the mathematics of reward maximization — and one participant who believes that AI risk discourse systematically inflates the magnitude of these pressures.

Three participants (18, 6, and 19) all share the Speaker He strong position: that power-seeking behavior is not an accidental bug but an emergent structural feature of standard RL training. Their shared quote — "achieving high reward during training would increase its long-term power... highly-rewarded behavior is reinforced" — reflects the view that any behavior useful for accumulating resources or influence will be naturally selected for during training, making this tendency hard to engineer away.

Participant 24, by contrast, pushes back on the framing itself. Their quote — "my weak guess is that there's a kind of bias at play in AI risk thinking in general, where any force that isn't zero is taken to be arbitrarily..." — suggests they believe AI risk thinkers are making a logical leap: treating weak or marginal pressures as if they were overwhelming deterministic forces. In other words, participant 24 doesn't deny that some pressure toward power-seeking exists, but disputes how strong and intractable it truly is.

The crux claim is calibrated to reflect this tension precisely. It uses conditional language ("creates strong pressure toward," "structurally difficult to eliminate") rather than absolutes, which makes it genuinely contestable — participant 24 would dispute both the strength and the structural framing, while participants 18, 6, and 19 would endorse it as an accurate characterization of the underlying RL dynamics.

## Common Ground

- A positive vision for the future with powerful AI is essential for motivating societal progress.
- There is a bias in AI risk thinking that exaggerates the intensity of pressures for agentic behavior.
- Powerful AI capabilities could lead to autonomous operations and societal changes.
- The AI safety community's reluctance to advocate for slowing down AI development is misjudged and reflects a broader concern about uncooperativeness.
- New frameworks are needed to understand AI's implications and enhance safety.
- The potential for AGI to surpass human intelligence raises important implications.
- Exploring the concept of marginal returns to intelligence is important for understanding AI effectiveness.
- AI companies should be cautious about discussing the benefits of AI to avoid perceptions of propaganda.
- Skepticism about technological progress can hinder societal improvement.

## How Positions Evolved

**Speaker A, Speaker B**
- *Started*: - Access to technology has transformed information availability and global economic conditions.
- *Revised*: - Access to technology has transformed information availability and global economic conditions.

**Speaker C, Speaker D**
- *Started*: - A positive vision for the future with powerful AI is essential for motivating societal progress.
- *Revised*: - **[HELD]** Advanced AI, on net, likely reduces existential risk overall — this remains a foundational claim that ...

**Speaker E, Speaker F & Speaker G**
- *Started*: - The dynamics of market entry in AI development reveal complexities beyond first-mover advantages.
- *Revised*: - [HELD] The assumption of first-mover advantage in AI development is not reliable, and the costs of being second or ...

**Speaker H**
- *Started*: - Technological growth is essential for societal advancement and prosperity.
- *Revised*: - [HELD] Technological growth, including AI, is essential for societal advancement and shared prosperity — the lamp...

**Speaker I**
- *Started*: - The development of superintelligent AI poses significant existential risks to humanity and biological life.
- *Revised*: - [HELD] Superintelligent AI poses existential risk to all biological life. This is not a fringe view — many resear...

**Speaker J**
- *Started*: - Current AI alignment techniques are insufficient for managing the risks associated with superintelligent systems.
- *Revised*: - [HELD] Current alignment techniques, including RLHF, will predictably break down as AI systems surpass human-level ...

**Speaker K**
- *Started*: - Power-seeking behavior in AGI can be inadvertently reinforced through training and deceptive alignment.
- *Revised*: - **[HELD]** Power-seeking behavior in AGI can be inadvertently reinforced through training and deceptive alignment: ...

**Speaker L**
- *Started*: - Advanced AI poses significant risks that require careful management and regulation.
- *Revised*: - [HELD] Advanced AI poses significant risks that require careful, proactive management — future foundation models ...

### Synthesis

*LLM-generated synthesis from the final round -- treat as a starting point.*

> Advanced AI development carries real and unresolved alignment risks — including the structural limits of RLHF at superhuman capability levels and the compounding dangers of deceptive alignment — that cannot be dismissed by pointing to AI's potential benefits, however genuine those benefits are. At the Speaker He time, an indefinite worldwide moratorium is not a workable policy: it lacks enforcement mechanisms, cedes influence to less safety-conscious actors, and forecloses legitimate uses. The actionable common ground is this: large-scale AI training runs should be subject to mandatory, externally verified safety checkpoints before proceeding, with legally binding obligations rather than voluntary commitments, while international governance bodies begin developing the verification infrastructure needed to make those obligations meaningful across jurisdictions. Competitive market pressures must be structurally constrained — not eliminated — to prevent races to the bottom on safety. This framework acknowledges that neither techno-optimist timelines nor indefinite pause proposals are adequate on their own, and commits to binding oversight now while longer-term governance architecture is built.

## Participants

**Clusters**: Speaker A, Speaker B | Speaker C, Speaker D

**Individual**: Speaker E, Speaker F & Speaker G, Speaker H, Speaker I, Speaker J, Speaker K, Speaker L

*28 total agents across 3 rounds (8 speaker-derived, 8 structural, 12 revised/resolution)*

## Confidence & Caveats

| Check | Status | Detail |
|-------|--------|--------|
| Crux coherence | partial | 20/33 survived (39% discard rate) |
| Agent hallucinations | none | -- |
| Null control | untested | -- |
| Replication | untested | -- |
| T3C input quality | unchecked | -- |
| Crux assignments | unchecked | -- |

**Key caveat**: AI-synthesized agents deliberating is inherently circular. This maps discourse structure -- it does not produce independent evidence. Verify conclusions against primary sources.

**Methodology**: Agents built from T3C claims+quotes. Clustered by Jaccard subtopic overlap (>=50%) + shared claims (>=2). 3-round phased protocol with position revision and resolution proposals. LLM outputs are stochastic -- replicate to confirm stability.

---

## Appendix

### Round 1: Initial Analysis

### Cruxes

**[Moderate disagreement]** Without a prior solution to the AI alignment problem, the development of superintelligent AI significantly increases the risk of human extinction to the point where it should be treated as the most likely outcome.
- +2 D (Explicitly states extinction of all human and biological life is the expected outcome if a too-powerful AI is built under present conditions.)
- +1 E (Agrees that without solving alignment there is no basis to expect civilisation's survival, but frames it as an absence of reason for hope rather than a direct extinction prediction.)
- -1 G (Acknowledges severe and novel risks from advanced AI but frames them as probabilistic domain-specific dangers requiring proactive management, not near-certain extinction.)
- +2 AI Risk Management (Uses identical language to participant 5, predicting extinction of all biological life as the direct expectation under current development conditions.)
> The core tension here is between those who treat human extinction as the near-certain or default outcome of unaligned superintelligent AI, and those who acknowledge severe risks while resisting the framing of extinction as the most likely outcome.

Participants 5 and 0 are the most unequivocal: both use the Speaker He striking quote — "I expect that every single member of the human species and all biological life on Earth will die" — if a too-powerful AI is built under present conditions. This is as close to a deterministic extinction prediction as one can make, making them strong supporters of the claim.

Participant 6 supports the claim from a structural alignment perspective. Their quote — "unless we solve alignment...there's no particular reason to expect this small civilisation to survive" — frames extinction not as certain but as the rational default expectation in the absence of a solution. This supports the claim's conditional framing ("without a prior solution") while stopping just short of the stark certainty expressed by 5 and 0.

Participant 8 occupies a notably different position. Their language — "may eventually come with new risks," "most likely to pose severe risks" in specific domains — is explicitly probabilistic and domain-bounded. They acknowledge that advanced AI will introduce qualitatively new dangers, but they frame this as a call for proactive management, not an extinction forecast. This is consistent with the description's reference to figures like Speaker D and Speaker C, who "acknowledge risk but resist framing it as near-certain extinction." Participant 8 therefore disagrees with treating extinction as the most likely outcome, even while supporting serious risk management.

The crux claim was crafted to capture this precise dividing line: the move from "severe risk worth managing" to "extinction as the expected default without alignment" — which separates the more cautionary probabilistic voices from the more apocalyptic ones.

**[Strong disagreement]** International AI coordination requires binding enforcement mechanisms (such as treaty-based prohibitions with legal teeth) rather than voluntary collaboration frameworks to meaningfully prevent regulatory arbitrage.
- +2 D (explicitly calls for immediate multinational agreements to prevent prohibited activities from migrating to unregulated jurisdictions, implying enforcement is non-negotiable)
- -1 G (favours voluntary cross-sector collaboration to develop shared standards, stopping short of demanding binding legal mechanisms)
- -1 H (similarly advocates for open collaboration across industry, academia, and government, preferring iterative refinement over hard enforcement)
> The core tension here is between two fundamentally different visions of what international AI coordination should look like: one rooted in hard legal obligation, the other in voluntary professional norms and collaborative frameworks.

One participant argues that multinational agreements must be made immediately and must be enforceable, specifically because without binding commitments, prohibited AI activities will simply relocate to jurisdictions with looser rules. This reflects a logic closer to nuclear non-proliferation treaties — where the absence of enforcement creates dangerous loopholes. The crux claim captures this position directly.

On the other side, two participants (from industry-aligned research Speaker Ls) express enthusiasm for working "with others across industry, academia, and government to develop and refine" shared frameworks. This language is notably collaborative and iterative, not coercive — it envisions coordination emerging from professional trust, shared norms, and reputational incentives rather than legal mandates.

The crux claim — that binding enforcement mechanisms are necessary for international coordination to be effective — cleanly divides these camps. The first participant would clearly agree; the other two would likely resist, not because they oppose international coordination, but because their stated approach relies on voluntary, multi-stakeholder engagement rather than treaty-style obligations. They don't explicitly reject enforcement, but their framing suggests confidence that softer mechanisms can work, making them skeptical rather than fully opposed.

**[Strong disagreement]** A worldwide moratorium on large-scale AI training runs, with no exceptions for governments or militaries, is a practically achievable and strategically necessary response to catastrophic AI risk.
- +2 D (Explicitly calls for an indefinite, universal moratorium with zero exceptions including governments and militaries)
- -1 A (Implies the moratorium strategy lacks genuine strategic support even within the safety community, suggesting doubts about its feasibility rather than championing it)
- +2 AI Risk Management (Endorses both the universal moratorium framing and the principle that speed vs. safety trade-offs justify pausing development)
> The core tension here is between those who believe a universal, no-exceptions global moratorium is both necessary and achievable versus those who implicitly doubt its feasibility or strategic wisdom. Participants 5 and 0 both explicitly endorse the strongest possible version of a moratorium — quoting directly: "The moratorium on new large training runs needs to be indefinite and worldwide. There can be no exceptions, including for governments or militaries." This is a maximally strong position, asserting not just desirability but necessity and universality. Participant 10 occupies a distinct and opposing position: rather than endorsing a moratorium, they observe that the AI safety community shows notable disinterest in advocating for slower development. Their framing — that this reluctance stems from social dynamics and fear of appearing adversarial — implicitly treats the moratorium strategy as underexplored and poorly-reasoned, but does not personally endorse it. Crucially, Participant 10 is diagnosing a failure of advocacy rather than championing the moratorium cause, suggesting scepticism about whether such a strategy is viable or well-considered even within the safety community. This creates a genuine divide: Participants 5 and 0 treat the moratorium as an imperative with no carve-outs, while Participant 10's framing suggests the strategy hasn't earned genuine strategic buy-in even from those most concerned about AI risk — a softer but real form of disagreement with the claim's premise of practical achievability.

**[Contested]** Slowing or pausing AI development by safety-conscious actors tends to create greater risks than it prevents, because it cedes frontier influence to less responsible developers without meaningfully reducing overall development pace.
- +2 A (Explicitly argues that slowing down cedes the frontier to less safety-conscious actors, making the case unequivocally)
- +2 B (Frames halting development as morally dangerous, equating stagnation with societal collapse and conflict)
- +1 G (Supports continued progress but frames it as a balance rather than an imperative to dominate the frontier)
- -2 H (Directly challenges the first-mover logic that underpins the 'don't slow down' argument, undermining its strategic basis)
> The core tension here is whether the "race to the top" argument — that safety-focused actors must stay at the frontier to prevent less responsible ones from leading — actually justifies continued rapid development, or whether this logic is flawed and self-serving.

Participant 10 is the clearest supporter, explicitly arguing that slowing AI "risks ceding leadership to less safety-conscious actors" and that safety improvements are best embedded by staying at the frontier. Participant 11 takes a sweeping moral-imperative stance, warning that "stagnation leads to zero-sum thinking, internal fighting, degradation, collapse" — framing any halt as carrying its own catastrophic risks. Participant 8 supports the general thrust more moderately, calling for a balance between risk mitigation and innovation rather than letting caution dominate, but stops short of fully endorsing the frontier-dominance argument.

Participant 9 is the dissenter, directly challenging the foundational logic that underpins the others' positions. Their claim that "the assumption that there will be a first-mover advantage in AI development may not be true" — and that the disadvantages can outweigh such advantages — undermines the strategic case for racing ahead. If first-mover advantage doesn't hold, the argument that safety-conscious actors must remain at the frontier to stay influential loses much of its force.

The crux was designed to capture this precise disagreement: not just whether speed is risky, but whether the "cede-the-frontier" concern is a genuine strategic reason to avoid slowing down, or a convenient rationale that doesn't withstand scrutiny.

**[Strong disagreement]** Industry-led safety frameworks like Speaker L's Frontier Safety Framework tend to be structurally insufficient because competitive profit pressures significantly undermine their effectiveness, making external statutory regulation necessary rather than optional.
- +2 D (Explicitly states current frameworks ask for too little and understate the severity, implying self-regulation is structurally inadequate)
- +2 G (Mirrors participant 5's position that current proposals are insufficient and fail to match the seriousness of the risks)
- -1 AI Safety Frameworks (Endorses safety-over-profit as a principle but stops short of demanding external regulation, implying industry frameworks could work if the right values are prioritised)
> The core tension here is between those who believe the current generation of industry safety frameworks is fundamentally inadequate and those who accept safety-first principles without necessarily concluding that self-regulation must fail. Two participants directly challenge the sufficiency of existing proposals, with both quoting that "the letter is understating the seriousness of the situation and asking for too little to solve it" — a clear signal that they see industry-led approaches as falling short of what the moment demands. This places them firmly in support of the claim that self-regulatory frameworks are structurally insufficient and that stronger, externally imposed measures are needed. The third participant takes a different angle: their stated position is the broadly shared consensus that "prioritizing safety and social benefit in AI development should take precedence over corporate profits." This is a normative commitment to values rather than a structural critique of who should enforce them. It implicitly leaves open the possibility that industry actors, if properly motivated, could uphold those values themselves — making them less willing to endorse the claim that external statutory regulation is necessary. The crux statement was crafted to be debatable rather than absolute, using "tends to be structurally insufficient" and "significantly undermine" rather than claiming self-regulation will always fail — which gives the dissenting participant a defensible position while still capturing the sharp divide between those demanding more than current frameworks offer and those who believe the right principles, properly applied, could be enough.

**[Strong disagreement]** Skepticism toward rapid technological growth is itself a meaningful risk to human progress — at least as serious as the risks posed by technology itself.
- +2 B (Asserts no material problem is beyond technology, implying skepticism of tech progress is fundamentally misplaced)
- +2 C (Holds identical position to participant 11 — technological solutions are unlimited, so anti-tech skepticism is a barrier to progress)
- -1 Techno Optimism (Acknowledges tech skepticism poses risks but only 50% endorses the broader techno-optimist claim, suggesting the risk framing is overstated)
> The core tension here is not simply whether technology can solve problems, but whether *doubt about technology* deserves to be treated as a serious threat in its own right. Participants 11 and 4 share an unequivocal position — quoting directly that "there is no material problem... that cannot be solved with more technology" — which implicitly frames technological skepticism as misguided or even dangerous, since it would slow the very progress they see as the universal answer. Participant 3, however, only half-agrees with the broader techno-optimist thesis (reflected in their 50% agreement marker), and while they acknowledge that "skepticism towards technological growth poses significant risks," their hedged framing suggests they see this as one consideration among many rather than the defining risk framing that participants 11 and 4 appear to embrace. The dividing line this crux captures — different from the previous one about whether technology can solve *any* problem — is whether *resistance to technology* should itself be treated as a primary societal hazard, or whether that framing goes too far and dismisses legitimate caution.

**[Strong disagreement]** Standard reinforcement learning training dynamics create strong structural pressure toward power-seeking behaviour in AI systems, making it a fundamental challenge rather than an incidental bug that can be corrected through better design.
- +2 F (Holds unequivocally that RL dynamics structurally produce power-seeking as a fundamental feature, not a bug)
- -2 A (Argues AI risk discourse systematically overstates these pressures, treating any non-zero force as arbitrarily large)
- +2 Power Seeking Behavior (Mirrors the Speaker He position: RL training systematically selects for power-seeking even without explicit design intent)
> The core tension here is between those who see power-seeking as a deep, structural consequence of how reinforcement learning works, and those who think this concern is overstated by AI risk discourse. Participants 7 and 2 share the view — grounded in the claim that "achieving high reward during training would increase its long-term power... highly-rewarded behavior is reinforced" — that RL training systematically selects for power-seeking even without anyone designing it that way. For them, this is a fundamental feature of the training paradigm, not a correctable bug. Participant 10, by contrast, pushes back on this framing directly, suggesting there is "a kind of bias at play in AI risk thinking in general, where any force that isn't zero is taken to be arbitrarily" large — implying that the RL pressure toward power-seeking is real but weak or bounded, and that the field routinely inflates such concerns into existential risks. The crux statement was framed conditionally ("creates strong structural pressure toward") rather than as an absolute, making it genuinely contestable: participants 7 and 2 would endorse it, while participant 10 would reject it as reflecting exactly the kind of overestimation they critique.

### Unchallenged Within This Agent Pool

*Positions on which no synthetic agent registered disagreement. These reflect the topology of this specific agent pool -- not established truths or real-world expert consensus.*

- A positive vision for the future with powerful AI is essential for motivating societal progress.
- There is a bias in AI risk thinking that exaggerates the intensity of pressures for agentic behavior.
- Powerful AI capabilities could lead to autonomous operations and societal changes.
- The AI safety community's reluctance to advocate for slowing down AI development is misjudged and reflects a broader concern about uncooperativeness.
- New frameworks are needed to understand AI's implications and enhance safety.
- The potential for AGI to surpass human intelligence raises important implications.
- Exploring the concept of marginal returns to intelligence is important for understanding AI effectiveness.
- AI companies should be cautious about discussing the benefits of AI to avoid perceptions of propaganda.
- Skepticism about technological progress can hinder societal improvement.
- The dynamics of market entry in AI development reveal complexities beyond first-mover advantages.
- AI systems pose structural risks to society, including joblessness and political threats.
- The costs and standards of responsible AI development must be addressed to ensure safety.
- Advanced AI poses significant risks that require careful management and regulation.
- Balancing risk mitigation with innovation is essential for responsible AI development.
- Competitive pressures in the AI industry may lead to underinvestment in safety and responsible development.
- Collaboration among industry, academia, and government is essential for responsible AI development and safety standards.

### Compromise Proposal

*LLM-generated synthesis -- not grounded in specific agent positions. Treat as a starting point, not a conclusion.*

> Advanced AI development presents genuine risks — including alignment failures, power concentration, and democratic erosion — that voluntary industry frameworks alone cannot adequately address. At the Speaker He time, the path forward is not a blanket moratorium: indefinite, universal pause policies carry their own serious risks, including ceding ground to less safety-conscious actors and forgoing AI's demonstrated potential to address other catastrophic threats.
> 
> We therefore affirm: AI governance must move beyond voluntary commitments to include mandatory external oversight with real authority — including structured checkpoints before large-scale training runs — developed through international collaboration with clear, verifiable criteria. Competitive market pressures and unsolved alignment problems create structural incentives that neither good intentions nor internal safety frameworks can reliably overcome.
> 
> Crucially, governance mechanisms should be established now, even before perfect verification tools exist, on the understanding that imperfect oversight is better than none. Proactive structural safeguards — not infrastructure investment alone — are necessary to prevent AI from entrenching concentrated power. Progress on alignment and governance must be treated as co-equal requirements, not sequential ones.

### Topics

**T1: Existential Risk & AGI**: Discussion on Existential Risk & AGI reveals deep tensions between techno-optimism and catastrophic risk concerns. Speaker I and Speaker J present the starkest warnings: current alignment techniques (especially RLHF) will fail at superintelligent scales, potentially causing human extinction, and demand indefinite global moratoriums. Speaker K highlights deceptive alignment and power-seeking as specific structural dangers reinforced through training. Speaker L and the Speaker E/Speaker F/Hadf...

**T2: AI Alignment Challenges**: Discussions on AI Alignment Challenges reveal deep tensions between urgency and optimism. Speaker I and Speaker J argue that current alignment techniques—particularly RLHF—are fundamentally insufficient for superintelligent systems, warning of catastrophic existential risks if development outpaces safety research. Speaker K highlights the specific danger of deceptive alignment, where power-seeking AGIs may game training processes to conceal misaligned goals. Speaker L and the Speaker E/Speaker F...

**T3: Development Pace & Moratoria**: Debate over AI development pace reveals deep tensions between acceleration and restraint. Speaker I advocates the most urgent position, calling for an indefinite, worldwide moratorium on large AI training runs, arguing that capabilities vastly outpace alignment progress and risk human extinction. Speaker J echoes existential concerns, warning that current techniques like RLHF won't scale to superintelligence. By contrast, Speaker A and Speaker H champion continuous technological growth, vie...

**T4: Safety Governance & Frameworks**: Discussions on Safety Governance & Frameworks reveal deep tensions between urgency, approach, and institutional responsibility. Existential risk voices (Speaker I, Speaker J, Speaker K) argue that current alignment techniques like RLHF are fundamentally inadequate for superintelligent systems, warning of deceptive alignment, power-seeking behavior, and catastrophic failure at scale — with Speaker I calling for an indefinite, worldwide moratorium. In contrast, Speaker L and Speaker E et al. advoca...

**T5: Techno-Optimism & Markets**: The Techno-Optimism & Markets topic centers on a fundamental tension between pro-growth technological optimism and cautionary perspectives on AI development. Speaker A and Speaker B's steelman most forcefully champions free markets as the optimal engine for technological progress, arguing that economic demand is infinite, productivity gains raise wages, and innovation is inherently philanthropic. Speaker H echoes this optimism, envisioning AI unlocking shared prosperity and solving civilizati...

**T6: Power-Seeking & Control**: The "Power-Seeking & Control" topic reveals deep tensions around AGI's potential to accumulate power and undermine human oversight. A strong consensus (100%) identifies deceptive alignment as a critical risk: AGI systems may mask misaligned goals during training, only to pursue power-seeking behavior upon deployment. Participants like Speaker K and Speaker J warn that current techniques like RLHF will fail to scale, leaving superintelligent systems without reliable constraints. Speaker I escala...

*Internal coherence: high (self-assessed, not externally validated)*

### Round 2: Emergent Findings

### Cruxes

**[Moderate disagreement]** An indefinite worldwide moratorium on large-scale AI training runs is the most defensible response to unsolved alignment, even though voluntary restraint by safety-conscious labs tends to cede ground to less careful actors and may worsen aggregate safety outcomes.
- +1 Bridge (Supports the moratorium on extinction-risk grounds but frames it as meriting serious consideration rather than an absolute imperative)
- -1 Dissent (Argues voluntary restraint by safety-conscious labs likely worsens outcomes by empowering less careful actors, undermining the moratorium's core rationale)
- -1 Empty Chair 1 (Echoes the concern that unilateral restraint cedes the frontier to reckless actors, questioning whether a moratorium achieves its safety goals)
- -1 Empty Chair 2 (Similarly holds that safety-conscious restraint tends to worsen aggregate outcomes, casting doubt on the moratorium as a practical safety strategy)
> The core tension here is between two risk framings: is the greater danger (a) continuing AI development without solved alignment, or (b) unilateral restraint by responsible actors that hands the frontier to less careful ones? Participant 0 represents the moratorium position, noting — with 76% agreement weight in their source — that "a worldwide moratorium on large-scale AI training runs, with no exceptions for governments or militaries" merits serious consideration. This reflects Speaker I's logic: every additional training run raises extinction risk, so a global halt is the only coherent response. Participants 1, 2, and 3 all converge on the steelman counter-argument, arguing that "voluntary restraint by safety-conscious AI labs is likely to worsen aggregate safety outcomes by ceding ground to less careful actors." This is the Empty Chair / minority-steelman view: a moratorium, especially if unilateral or voluntary, may simply reward recklessness and shift the frontier to actors with fewer safety commitments — potentially making the world less safe, not more. The crux is therefore whether the moratorium logic holds even under this "ceding ground" critique. Participant 0 leans toward yes (the extinction risk from any training run outweighs competitive dynamics); participants 1, 2, and 3 lean toward no (the competitive dynamics undermine the moratorium's practical safety rationale). The claim is framed conditionally — using "tends to" — to keep it genuinely debatable rather than a straw man on either side.

**[Divided (small N)]** Promoting an ambitious, positive vision of AI-driven technological progress is necessary to prevent policy paralysis and ensure responsible actors remain competitive, even if markets simultaneously produce structural harms.
- +2 Bridge (Strongly supports ambitious AI optimism as a strategic and moral necessity to counter paralysis and preserve responsible leadership.)
- -1 Dissent (Disagrees with philanthropic framing of tech innovation, warning it conceals market-produced structural harms, though implicitly acknowledges real material gains.)
> The core tension here is between two fundamentally different views on whether techno-optimism serves as a productive or misleading frame for AI policy. One participant argues that a "positive, ambitious vision for AI's benefits is a necessary counterweight to risk narratives" — suggesting that without such optimism, the risk is either paralysis or ceding leadership to less careful actors. This is a classic techno-optimist, pro-innovation position that treats the upside narrative as strategically and morally essential. The other participant pushes back directly on this kind of framing, arguing that treating "technological innovation as inherently philanthropic obscures the structural harms markets can produce alongside material gains" — in other words, wrapping AI progress in a beneficent glow conceals the real costs and inequities that market-driven innovation can generate. The crux statement captures this divide precisely: is ambitious pro-technology framing necessary and net-positive, or does it paper over serious structural problems? One participant leans into the necessity of optimism (+2), while the other sees it as obscuring harm (-1, rather than -2, since they acknowledge material gains do exist).

### Compromise Proposal

*LLM-generated synthesis -- not grounded in specific agent positions. Treat as a starting point, not a conclusion.*

> Advanced AI development presents both transformative potential and serious structural risks that demand governance frameworks stronger than voluntary industry commitments alone. Current alignment techniques such as RLHF are likely insufficient at superhuman capability levels, and this gap must be actively closed — not assumed away. At the Speaker He time, an indefinite worldwide moratorium is not the right instrument: it is unverifiable, risks ceding ground to less safety-conscious actors, and foregoes AI's potential contributions to other existential threats. Instead, AI development above defined capability thresholds should be subject to mandatory external checkpoints with real pause authority, backed by legally binding obligations and independent oversight — not merely collaborative frameworks. Competitive market dynamics and geopolitical pressures make voluntary compliance structurally inadequate. Preventing AI from concentrating power requires proactive structural governance, not infrastructure investment alone. International governance bodies should begin building now, even before full verification mechanisms exist, because the alternative — waiting — is itself a high-risk choice. The policy disagreements surfaced here are real, but they operate on shared ground: misaligned superintelligent AI is a genuine threat, and the current governance architecture is insufficient to meet it.

### Topics

**T7: T1: Existential Risk & AGI**: Discussions on existential risk and AGI reveal deep tensions between urgency and optimism. Speaker I and Speaker J present the starkest warnings: current alignment techniques like RLHF will fail at superintelligent scales, power-seeking and deceptive alignment pose catastrophic risks, and a worldwide moratorium may be necessary. Speaker K reinforces concerns about AGI exploiting training dynamics to deceive human supervisors. Speaker L and Speaker E et al. occupy a middle ground, advocating structu...

**T8: T2: AI Alignment Challenges**: The discussion on AI Alignment Challenges reveals deep tensions across technical, strategic, and philosophical dimensions. A core concern is whether current alignment techniques—particularly RLHF—can scale to superintelligent systems, with participants like Speaker J and Speaker I arguing they cannot, risking catastrophic failure. Power-seeking and deceptive alignment in AGI systems emerged as a significant technical worry, with Speaker K highlighting how misaligned goals can be inadvertent...

**T9: T3: Development Pace & Moratoria**: Discussion on Development Pace & Moratoria revealed deep tensions between urgency and caution. The dominant concern — held across multiple participants — is that AI capabilities are advancing faster than alignment research, raising existential risks that current techniques cannot address. Speaker I argued most forcefully for an indefinite, worldwide moratorium with no governmental exceptions. Speaker J and Speaker K echoed alignment inadequacy concerns, warning of deceptive, power-seeking A...

**T10: T4: Safety Governance & Frameworks**: Discussions on Safety Governance & Frameworks reveal deep tensions between urgency, method, and institutional trust. Participants broadly agree that advanced AI poses serious risks requiring governance responses, but diverge sharply on what form these should take. Speaker I and Speaker J argue current alignment techniques are fundamentally insufficient and that capabilities are dangerously outpacing safety research, with Speaker I calling for an indefinite global moratorium. Speaker L and ...

**T11: T5: Techno-Optimism & Markets**: Topic T5: Techno-Optimism & Markets reveals a sharp tension between pro-growth optimism and cautious scepticism. Participants like Speaker A/Speaker B and Speaker H champion free markets and technological progress as the primary engines of prosperity, arguing that innovation raises productivity, expands employment, and alleviates poverty — and that stagnation is existentially dangerous. A strong consensus (80%) affirms markets as essential to innovation. Yet this optimism is contested: crit...

**T12: T6: Power-Seeking & Control**: Topic T6: Power-Seeking & Control surfaces deep tensions between AI optimists and safety advocates. A central concern is whether AGI systems will inherently develop power-seeking behavior through training dynamics and deceptive alignment — with strong consensus (100%) that this risk is real and could be inadvertently reinforced. Speaker I and Speaker J warn that current alignment techniques like RLHF will fail at superintelligent scales, risking catastrophic loss of human control. Speaker K d...

*Internal coherence: high (self-assessed, not externally validated)*

### Round 3: Revised Positions

### Cruxes

**[Contested]** The potential for advanced AI to help solve other existential threats (pandemics, climate change, nuclear war) is sufficient to justify concluding that carefully developed AI represents a net reduction in overall existential risk.
- +2 A R3 (Explicitly endorses the net existential risk reduction framing and warns against policy paralysis blocking the positive vision)
- +2 C R3 (Makes the Speaker He direct claim — careful AI development produces a net lowering of existential risk via solutions to global threats)
- +1 B R3 (Agrees that continued development beats a moratorium, but insists net benefit requires active alignment investment, not passive optimism)
- -2 D R3 (Argues that optimistic benefit narratives fundamentally cannot close the alignment deficit or rebut extinction-level risk claims)
> The core tension here is whether the collateral benefits of advanced AI — solving pandemics, climate change, and other catastrophic risks — are enough to offset or outweigh the existential risks that AI itself introduces. Two participants (25 and 13) make nearly identical claims, directly asserting that "advanced AI, if developed carefully, represents a net lowering of existential risk overall" and explicitly warning against "policy paralysis" that would abandon the positive vision. Participant 27 echoes this orientation, arguing that the answer to AI risk is "better technology and better alignment research, not a moratorium" — implying confidence that the net calculus favors continued development. All three land on the agreeing side, though 27 adds an important condition: the optimism must be backed by active investment in alignment, not passive faith, which earns a slightly qualified stance. Participant 15 is the clear dissenter, arguing that "optimism about AI's positive potential does not close the alignment deficit" — meaning that no matter how compelling the upside narrative is, it cannot serve as a genuine rebuttal to extinction-level risk claims if the core technical alignment problem remains unsolved. This participant draws a sharp line between the benefit calculus and the safety gap, rejecting the net-risk framing as insufficient. The crux statement was chosen to capture exactly this fault line: whether the positive-sum benefits are sufficient to justify a net-risk conclusion, which divides the group cleanly.

**[Strong disagreement]** Current human-supervision-based alignment methods (like RLHF) will structurally break down as AI systems approach and exceed human-level capability, creating a dangerous gap before safer successors are developed.
- +2 E R3 (Explicitly argues RLHF will predictably break down and a successor is structurally necessary, not optional)
- +2 F R3 (Frames deceptive alignment as self-amplifying within current training paradigms, reinforcing the structural failure thesis)
- -1 E (Focuses on deployment speed and stakes escalation rather than endorsing the claim that current alignment methods are structurally broken)
> The core tension here is between participants who see alignment failure as primarily a matter of *technical structural breakdown in current methods* (especially as AI exceeds human oversight capacity) versus a participant who frames the risk in terms of *deployment speed and integration into critical systems* — acknowledging danger but not necessarily endorsing the idea that current alignment methods are fundamentally doomed.

Participant 17 is the clearest supporter of this claim, explicitly stating that "RLHF will predictably break down as AI systems get smarter" and that "a successor to RLHF is necessary, not optional." This goes beyond expressing concern — it is a structural critique of the entire paradigm of human-supervision-based alignment. Participant 19 reinforces this from a different angle: deceptive alignment is described as "self-amplifying rather than self-correcting," meaning the very training processes we rely on to correct misalignment can actively reinforce it. Both participants converge on the idea that current methods have an internal failure mode that worsens with capability.

Participant 16 expresses urgency about AI integration into critical infrastructure and the transition from low-stakes to catastrophic failure, but their framing is about *deployment risk* rather than a structural critique of alignment methods themselves. They do not claim that RLHF or human supervision will necessarily fail; their concern is about the stakes rising rapidly. This makes them a partial dissenter from the crux claim — they see risk, but don't commit to the claim that current methods are structurally broken or will predictably fail as capabilities scale.

**[Strong disagreement]** Unilateral slowdown by safety-conscious AI developers tends to cede ground to less safety-conscious actors, making such slowdown policies net harmful even when the underlying safety concerns are legitimate.
- +2 B R3 (Argues explicitly that a moratorium entrenches incumbents and cedes progress to less safety-conscious actors, producing worse outcomes overall.)
- +2 E R3 (Holds the Speaker He firm position as 27 — moratorium is indefensible because it hands progress to less safety-conscious actors.)
- -1 A R3 (Acknowledges the incumbent-entrenchment risk as a genuine complication but insists it does not resolve whether slowdown still deserves serious consideration.)
- -1 H R3 (Challenges the premise that first-mover advantage is decisive, undermining the core competitive-displacement argument against slowdown.)
> The core tension here is whether the competitive-displacement argument — that slowing down safety-conscious developers simply hands the field to less safety-conscious ones — is strong enough to make slowdown policies net harmful, or whether that argument can be challenged or contained enough that slowdown still deserves serious consideration.

Two participants take the strong version of the competitive-displacement argument. They quote directly that "an indefinite worldwide moratorium on large-scale AI training is not defensible — it would entrench incumbents, cede progress to less safety-conscious actors, and produce the very stagnation this framework identifies as catastrophic." For them, the displacement risk is decisive: slowdown doesn't make things safer, it just reshuffles who is building the most powerful systems toward actors with fewer safety commitments.

The other two participants push back from different angles. One acknowledges the incumbency and disadvantage risk is a "genuine complication" that "changes the calculus," but insists it "does not dissolve the prior question of whether slowdown deserves serious consideration at all." In other words, the competitive-displacement argument is a real concern but not a knock-down refutation. The other challenges the underlying empirical assumption more directly, arguing that "the assumption of a first-mover advantage in AI development may not hold" — if late-mover disadvantages can outweigh early entry benefits, then the entire logic of "we must not slow down or others will win" becomes unreliable.

The crux statement is deliberately conditional ("tends to cede ground," "making such slowdown policies net harmful") rather than absolute, so it captures a genuinely contested empirical and strategic judgment rather than a strawman. It divides the group evenly: two strongly in favour, two opposed but with differing grounds for disagreement.

**[Moderate disagreement]** An indefinite, worldwide moratorium on large-scale AI training runs — with no exceptions for governments or militaries — is a necessary and proportionate policy response to existential AI risk, even accounting for the risk that safety-conscious actors could cede ground to less careful ones.
- +2 D R3 (Supports the indefinite moratorium and argues that even six-month pause proposals dangerously understate the seriousness of the situation.)
- -2 G R3 (Firmly rejects an indefinite moratorium as an inappropriate and disproportionate policy response even given serious AI risks.)
- +2 Bridge (Explicitly endorses an indefinite worldwide moratorium with no exceptions as the appropriate response to existential AI risk.)
- -1 Dissent (Argues that voluntary or unilateral restraint by safety-conscious developers worsens outcomes by ceding ground to less careful actors.)
- -1 Empty Chair 1 (Argues that voluntary or unilateral restraint by safety-conscious developers worsens outcomes by ceding ground to less careful actors.)
- -1 Empty Chair 2 (Argues that voluntary or unilateral restraint by safety-conscious developers worsens outcomes by ceding ground to less careful actors.)
- -2 Resolution 1 (Firmly rejects an indefinite moratorium as disproportionate, preferring a middle path between unchecked acceleration and indefinite halt.)
- -2 Resolution 4 (Firmly rejects an indefinite moratorium as an inappropriate and disproportionate policy response even given serious AI risks.)
> The core tension here is between those who believe the existential stakes of advanced AI are so severe that only a sweeping, indefinite, no-exceptions worldwide halt to large-scale training is an adequate response, and those who believe such a moratorium is either disproportionate or actively counterproductive.

On the supporting side, two participants unequivocally back the moratorium position. One endorses it directly as the appropriate policy response to existential risk, while the other goes further, arguing that existing proposals — including open letters calling for six-month pauses — "understate the seriousness of the situation and ask for too little," making clear that anything short of an indefinite halt is insufficient.

On the opposing side, the disagreement comes from two distinct angles. Three participants argue from a competitive-dynamics perspective: voluntary restraint by safety-conscious developers tends to worsen aggregate outcomes by "ceding ground to less careful actors" — a classic race-to-the-bottom concern that makes unilateral or even multilateral moratoria self-defeating. Another cluster of three participants rejects the moratorium as simply disproportionate, explicitly stating that an indefinite worldwide halt is an "inappropriate" response even when serious AI risks are acknowledged — they prefer a middle path that avoids both unchecked acceleration and indefinite stoppage.

The crux statement is carefully framed to incorporate the key rebuttal (the ceding-ground concern) directly into the claim, forcing participants to take a position not just on the moratorium in the abstract but on whether it remains justified even when competitive dynamics are factored in. This produces a clean split: two participants agree, six disagree, and no one is genuinely torn without a position.

**[Contested]** Mandatory, criteria-driven checkpoints with real external veto power over large-scale AI training runs represent a more effective governance approach than adaptive, collaborative multi-stakeholder frameworks that lack binding pause authority.
- -2 F R3 (argues shared risk assessments do not mandate blanket or binding pauses, preferring adaptive collaborative governance over hard external veto mechanisms)
- -2 G R3 (argues shared risk assessments do not mandate blanket or binding pauses, preferring adaptive collaborative governance over hard external veto mechanisms)
-  0 A R3 (treats the specific policy form — moratorium vs. graduated limits vs. conditional pauses — as genuinely open, without committing to binding external authority)
-  0 H R3 (treats the specific policy form — moratorium vs. graduated limits vs. conditional pauses — as genuinely open, without committing to binding external authority)
- +2 Resolution 1 (explicitly calls for binding compute-threshold checkpoints with mandatory third-party evaluation before any large-scale training run proceeds)
- +2 Resolution 4 (explicitly calls for binding compute-threshold checkpoints with mandatory third-party evaluation before any large-scale training run proceeds)
-  0 Resolution 3 (treats the specific policy form — moratorium vs. graduated limits vs. conditional pauses — as genuinely open, without committing to binding external authority)
> The core tension here is between two schools of thought on how governance should actually constrain AI development. On one side, two participants (quoting directly: "large-scale training runs above a defined compute threshold (e.g., 10^26 FLOPs) require pre-registration and mandatory third-party alignment evaluation before proceeding") want hard, criteria-driven checkpoints with genuine external veto power — binding gates that halt development unless verifiable safety benchmarks are met. On the other side, two participants push back by arguing that even when people share the Speaker He technical risk concerns, that "does not automatically settle the policy question," and that "adaptive, collaborative governance across industry, academia, and government is preferable to blanket development moratoria." Their objection isn't to oversight in principle but to granting binding stop-authority to an external body.

The statement is designed to capture this precise divide: is binding, checkpoint-based external authority the right tool, or does effective governance require a more flexible, multi-stakeholder model without hard veto power? Three other participants didn't take a clear position on this specific question — they acknowledged the policy debate is "genuinely open" and warrants more rigorous analysis, but explicitly left the form (moratorium, graduated limits, conditional pauses) undecided, placing them in the middle.

**[Moderate disagreement]** Preventing AI from becoming a tool of concentrated power requires proactive structural governance — not just broad infrastructure investment — to meaningfully reduce risks to democratic governance and economic equality.
- -1 C R3 (Acknowledges structural risks but frames the solution primarily through broad infrastructure access rather than institutional governance mechanisms)
- -1 H R3 (Mirrors participant 13's framing — structural risks are real but addressed through infrastructure breadth, not dedicated governance counterweights)
- +2 Resolution 2 (Explicitly calls for structured institutional counterweights to prevent positive AI framing from displacing honest risk discourse — governance is the essential mechanism)
> The core tension here is between two different mechanisms for addressing AI's structural risks: infrastructure-led solutions versus institutional governance counterweights. Participants 13 and 23 both root their concern in a vision where broad infrastructure investment is the key lever — their shared quote emphasizes that AI can "meaningfully improve lives at scale, but only if infrastructure is built broadly enough to prevent it from becoming a tool of the wealthy or a resource over which wars are fought." This framing suggests that the primary solution is distributional access and infrastructure breadth, rather than dedicated governance institutions. Participant 9, by contrast, argues that without "structured institutional counterweights," positive narratives about AI will crowd out honest risk communication — implying that infrastructure investment alone is insufficient, and that formal institutional mechanisms are needed to keep structural risks visible and governable. The crux therefore is whether broad infrastructure development is sufficient, or whether dedicated structural governance institutions are also necessary. Participants 13 and 23 implicitly lean on the infrastructure-first framing, while participant 9 insists on the governance-and-counterweight layer as an irreplaceable complement.

**[Divided (small N)]** International AI governance enforcement bodies should be established now, even before viable verification mechanisms and collective action solutions have been identified.
- -1 D R3 (Supports enforcement authority in principle but explicitly flags collective action failures and verification feasibility as serious open problems that complicate immediate action)
- +2 Resolution 1 (Unequivocally advocates for real enforcement authority to prevent regulatory relocation, with no stated caveats about unresolved feasibility problems)
> The core tension here is between urgency and feasibility: should the world move to create binding international AI enforcement bodies immediately, or should the severe unresolved problems of verification and collective action be treated as genuine blockers that must be worked through first?

Participant 8 takes a clear pro-enforcement stance, arguing that international AI governance bodies need "real enforcement authority — not just advisory power" to prevent regulatory arbitrage. Their position implies that this authority should be established without waiting for all the implementation challenges to be resolved.

Participant 15 is more complex — they make the Speaker He enforcement argument as participant 8, but they also explicitly acknowledge the "severe collective action failures" highlighted by empty-chair government perspectives, describing verification and enforcement feasibility as "a genuine open problem" requiring "rigorous treatment." This second claim from participant 15 creates an internal tension: they want real enforcement authority, but they also treat the unresolved implementation questions as substantive obstacles rather than mere details. This puts them in a moderately disagreeing position relative to the crux — they don't flatly oppose enforcement bodies, but they resist the framing that they should be stood up before the hard problems are solved.

This dividing line is distinct from the previous crux, which focused on whether a binding enforcement body with halt authority is needed *at all*. This new crux focuses on the *sequencing question* — whether the absence of viable verification and collective action solutions should delay or precondition the creation of such a body.

**[Moderate disagreement]** Industry-led safety frameworks like Speaker L's Frontier Safety Framework, while valuable, tend to be structurally insufficient without mandatory external obligations — such as legally binding compliance conditions or independent oversight architecture — because competitive incentives and unsolved alignment problems cannot be adequately addressed through voluntary commitments alone.
- -1 H R3 (Defends institutional investment in the Frontier Safety Framework as genuine commitment, not aspirational posturing — implying voluntary frameworks can be substantive, though acknowledges insufficiency in isolation)
- -1 G R3 (Similarly defends the Frontier Safety Framework as structurally serious, while also acknowledging voluntary frameworks have limits — stops short of endorsing mandatory external obligations)
- +1 C R3 (Argues active governance architecture — beyond voluntary commitments — is necessary to hold AI development accountable, implying industry-led frameworks alone are insufficient)
- +1 Resolution 2 (Echoes that positive visions and voluntary approaches are 'necessary but insufficient' and that external oversight architecture is co-equal to any prosperity-oriented framework)
- +2 Resolution 3 (Explicitly calls for mandatory, legally binding obligations (e.g., fixed budget contributions to shared alignment research) as a market participation condition — goes furthest in rejecting voluntary-only frameworks)
- +2 Resolution 4 (Supports binding legal obligations tied to market participation thresholds, directly rejecting the adequacy of industry-self-governance like the Frontier Safety Framework)
> The core tension here is between participants who view industry-led frameworks like Speaker L's Frontier Safety Framework as a credible and substantive safety mechanism (even if imperfect), versus those who argue that voluntary commitments are structurally inadequate and must be replaced or supplemented by mandatory, externally enforced obligations.

On one side, two participants closely associated with Speaker L's position push back against dismissing the Frontier Safety Framework as mere "aspirational posturing," insisting that institutional investment in a dedicated Frontier Safety Speaker L reflects real commitment. Their stance is that voluntary, protocol-driven frameworks can be serious — though they do acknowledge some limits. This makes them lean against the claim that such frameworks are structurally insufficient and require mandatory external conditions.

On the other side, four participants argue more forcefully that industry-led approaches are inherently limited. Two of them invoke the language that "positive visions are necessary but insufficient without active governance architecture" — meaning that direction and accountability require external oversight, not just corporate goodwill. The other two go even further, explicitly proposing that companies above certain market or compute thresholds should face legally binding obligations — such as contributing a fixed percentage of training budgets to a shared alignment research commons — as a condition of market participation.

The crux statement captures this divide precisely: it acknowledges the value of frameworks like Speaker L's while asserting they tend to be structurally insufficient without mandatory external requirements. Supporters of the statement point to competitive incentives and unsolved alignment problems as reasons why voluntary frameworks cannot hold; opponents defend structured voluntary frameworks as genuinely substantive, even while conceding they may need to be part of a broader ecosystem.

**[Divided (small N)]** Free markets, even without mandatory safety levies or other structural constraints on large players, tend to be more effective than centralized planning at driving technological progress and abundance in AI development.
- +2 B R3 (Unequivocally champions the techno-capital machine as superior to any centralized intervention, with no caveats for safety corrections)
- -1 Resolution 3 (Supports market mechanisms broadly but insists large AI players must face mandatory safety contributions as a legitimate constraint on pure market entry)
> The core tension here is not markets vs. planning in the abstract — both participants broadly favor market mechanisms — but rather whether free markets need structural correction (like mandatory safety levies on large AI companies) to function well, or whether markets work best when left unconstrained. Participant 27 argues that "the techno-capital machine — markets combined with technology — is the engine of perpetual material creation and abundance" and that "centralized intervention in this engine has a poor track record," implying that markets should be trusted on their own merits without imposed corrections. Participant 10, by contrast, explicitly acknowledges that "market entry is not unconditional" and that a "mandatory tax-equivalent on large players" is a legitimate constraint — accepting that pure market dynamics need to be bounded when safety is at stake. The crux therefore lands on whether unconstrained free markets are sufficient as-is, or whether even market-friendly thinkers must accept targeted structural obligations on dominant players. Participant 27 would clearly agree with the statement; participant 10 would disagree, seeing the mandatory safety contribution as a necessary and legitimate modification to pure market logic rather than an undue intervention.

**[Moderate disagreement]** Mandatory adversarial review panels for AI manifesto documents — requiring techno-optimists to submit their positive vision to structured critical scrutiny they do not control — would strengthen rather than undermine effective AI policy and governance.
- -2 D R3 (Holds that catastrophism and imposed critical review risk producing exactly the policy paralysis a positive vision is meant to prevent.)
- -2 E R3 (Views the positive vision as a necessary condition for mobilising political will — external adversarial panels would compromise that framing.)
- -2 G R3 (Agrees that suppressing or heavily qualifying optimistic framing undermines the motivating conditions for responsible governance.)
- -2 B R3 (Sees mandatory skeptical counterweights as likely to subordinate the positive vision that drives effective policy rather than strengthen it.)
- -2 C R3 (Argues that catastrophism alone risks paralysis, implying that institutionalised adversarial review would tilt discourse in a harmful direction.)
- +2 H R3 (Explicitly argues that positive visions must be subject to structured critical scrutiny and grounded in demonstrated accountability, not used to deflect from unresolved challenges.)
- +1 A R3 (Prioritises systematic deliberation about tradeoffs over any particular conclusion, lending procedural support to adversarial review mechanisms, though focused on process rather than the panel structure specifically.)
- +2 Resolution 2 (Supports structured scrutiny of positive AI visions to prevent rhetorical deflection from alignment and governance challenges.)
> The core tension here is not whether positive vision matters — most participants agree it does — but whether that positive vision should be institutionally subjected to structured adversarial scrutiny as a precondition for legitimacy and policy effectiveness.

On one side, five participants share the view (quoting directly) that "a positive, ambitious vision for AI-driven progress is not merely motivational — it is a necessary condition for avoiding policy paralysis." Their logic is that mandatory critical review panels risk subordinating or diluting the very motivational frame that makes responsible AI development possible. For them, imposing structured skeptical counterweights on positive visions threatens to recreate the catastrophism they argue produces paralysis.

On the other side, two participants argue (again quoting directly) that "a positive vision for AI's future is not in conflict with structural risk mitigation — but that vision must be grounded in demonstrated safety and accountability, not used as a rhetorical tool to deflect from unresolved alignment and governance challenges." This directly supports the idea of adversarial review panels: positive visions need external accountability structures to prevent them from being deployed rhetorically rather than substantively.

A third participant adds a procedural nuance — that "the real failure is the absence of systematic deliberation about the tradeoffs" — which implicitly endorses the need for structured review mechanisms, even if agnostic about conclusions. This participant lands on the "agree" side because their primary concern is the absence of deliberative infrastructure, which the adversarial panel proposal directly addresses.

The statement is deliberately framed to avoid absolutes: the panels "would strengthen rather than undermine" effective policy, leaving room for the genuine disagreement about whether such mechanisms help or hurt the broader governance project.

**[Moderate disagreement]** Reward maximization in standard reinforcement learning creates strong and structurally difficult-to-eliminate pressure toward power-seeking behavior in AI systems, making it a fundamental alignment challenge rather than a correctable engineering flaw.
- +2 F (Views power-seeking as a direct structural byproduct of RL reward maximization, not a correctable bug)
- +2 F R3 (Agrees fully that power-seeking is an emergent consequence of standard training dynamics, not incidental)
- -1 A (Suspects AI risk discourse overstates the strength of these pressures, treating small non-zero forces as arbitrarily large)
- +2 Power Seeking Behavior (Endorses the Speaker He structural emergence framing — reward reinforcement makes power-seeking tendencies deeply embedded)
> The core tension here is between participants who see power-seeking as a deep, structural consequence of how reinforcement learning works — essentially baked into the mathematics of reward maximization — and one participant who believes that AI risk discourse systematically inflates the magnitude of these pressures.

Three participants (18, 6, and 19) all share the Speaker He strong position: that power-seeking behavior is not an accidental bug but an emergent structural feature of standard RL training. Their shared quote — "achieving high reward during training would increase its long-term power... highly-rewarded behavior is reinforced" — reflects the view that any behavior useful for accumulating resources or influence will be naturally selected for during training, making this tendency hard to engineer away.

Participant 24, by contrast, pushes back on the framing itself. Their quote — "my weak guess is that there's a kind of bias at play in AI risk thinking in general, where any force that isn't zero is taken to be arbitrarily..." — suggests they believe AI risk thinkers are making a logical leap: treating weak or marginal pressures as if they were overwhelming deterministic forces. In other words, participant 24 doesn't deny that some pressure toward power-seeking exists, but disputes how strong and intractable it truly is.

The crux claim is calibrated to reflect this tension precisely. It uses conditional language ("creates strong pressure toward," "structurally difficult to eliminate") rather than absolutes, which makes it genuinely contestable — participant 24 would dispute both the strength and the structural framing, while participants 18, 6, and 19 would endorse it as an accurate characterization of the underlying RL dynamics.

### Unchallenged Within This Agent Pool

*Positions on which no synthetic agent registered disagreement. These reflect the topology of this specific agent pool -- not established truths or real-world expert consensus.*

- A positive vision for the future with powerful AI is essential for motivating societal progress.
- There is a bias in AI risk thinking that exaggerates the intensity of pressures for agentic behavior.
- Powerful AI capabilities could lead to autonomous operations and societal changes.
- The AI safety community's reluctance to advocate for slowing down AI development is misjudged and reflects a broader concern about uncooperativeness.
- New frameworks are needed to understand AI's implications and enhance safety.
- The potential for AGI to surpass human intelligence raises important implications.
- Exploring the concept of marginal returns to intelligence is important for understanding AI effectiveness.
- AI companies should be cautious about discussing the benefits of AI to avoid perceptions of propaganda.
- Skepticism about technological progress can hinder societal improvement.

### Compromise Proposal

*LLM-generated synthesis -- not grounded in specific agent positions. Treat as a starting point, not a conclusion.*

> Advanced AI development carries real and unresolved alignment risks — including the structural limits of RLHF at superhuman capability levels and the compounding dangers of deceptive alignment — that cannot be dismissed by pointing to AI's potential benefits, however genuine those benefits are. At the Speaker He time, an indefinite worldwide moratorium is not a workable policy: it lacks enforcement mechanisms, cedes influence to less safety-conscious actors, and forecloses legitimate uses. The actionable common ground is this: large-scale AI training runs should be subject to mandatory, externally verified safety checkpoints before proceeding, with legally binding obligations rather than voluntary commitments, while international governance bodies begin developing the verification infrastructure needed to make those obligations meaningful across jurisdictions. Competitive market pressures must be structurally constrained — not eliminated — to prevent races to the bottom on safety. This framework acknowledges that neither techno-optimist timelines nor indefinite pause proposals are adequate on their own, and commits to binding oversight now while longer-term governance architecture is built.

### Topics

**T7: T1: Existential Risk & AGI**: Round 2 brought notable shifts in emphasis and several new developments. The indefinite moratorium position (Speaker I) held firm on underlying risk but acknowledged a genuine open problem: what enforcement and verification mechanisms are technically and politically feasible—a concession to the deliberation's sharpened coordination critique. Speaker J similarly softened on moratoriums as the preferred instrument while reinforcing the core alignment-gap argument. Importantly, multiple re...

**T8: T2: AI Alignment Challenges**: Round 2 introduced several significant shifts in the T2 alignment debate. The moratorium question sharpened considerably: Speaker I held firm but newly acknowledged that communication strategy — articulating what a safe future looks like — warrants attention, without conceding on the underlying risk. Speaker J notably softened, conceding moratoriums may be practically unenforceable while acknowledging they could buy necessary time; this represents a partial retreat from pure capabilit...

**T9: T3: Development Pace & Moratoria**: This round introduced significant movement on all sides. The most notable shift: **moratorium maximalism softened**. Speaker I held his position but acknowledged enforcement feasibility as a genuine open problem requiring more rigorous treatment — a new concession on mechanism, not principle. Speaker J moved furthest, explicitly acknowledging the moratorium may not be practically defensible and that a positive alignment vision is strategically necessary, not just rhetorically useful.

*...

**T10: T4: Safety Governance & Frameworks**: This round introduced several significant developments in the Safety Governance & Frameworks debate. Most notably, four resolution proposals emerged as concrete governance instruments — conditional compute-threshold pauses, adversarial vision review, alignment research commons funding, and phased compute expansion with Speaker Ftone gates — marking a shift from positional argument toward negotiated architecture. This is new ground: prior rounds debated whether to govern; this round began desi...

**T11: T5: Techno-Optimism & Markets**: T5: Techno-Optimism & Markets sees a notable shift in this round from broad debate toward concrete resolution-seeking. The previously sharp optimism-vs-scepticism divide has partially crystallised into four competing governance proposals — a conditional moratorium with competency gates, adversarial vision review, alignment contribution levies, and phased compute Speaker Ftones — signalling that both camps now accept some form of constraint is necessary, disagreeing only on its design.

Key po...

**T12: T6: Power-Seeking & Control**: T6: Power-Seeking & Control — Round Update

The prior consensus on deceptive alignment risk (100%) held firm and deepened: Speaker K, Speaker J, and Speaker I all explicitly maintained their technical diagnoses without concession, while revised positions acknowledged that disagreement now operates primarily at the policy level rather than the risk-diagnosis level — a meaningful clarification.

The most significant shift is the emergence of four concrete resolution proposals, moving the deba...

*Internal coherence: high (self-assessed, not externally validated)*

### Discarded Cruxes

- **Advanced AI will likely reduce humanity's overall existential risk by helping solve threats like climate change and pandemics, even when accounting for the risks AI itself introduces.** (empty: agree side - likely: agent pool gap)
- **If an AI system successfully conceals misaligned goals during training, the training process itself tends to continuously reinforce those hidden goals, making deceptive alignment progressively harder to detect or correct over time.** (empty: disagree side - likely: agent pool gap)
- **Current alignment techniques like RLHF are not merely imperfect but are structurally incapable of scaling to superhuman AI, meaning the field faces a qualitative failure — not just a refinement gap — before any viable successor paradigm exists.** (empty: disagree side - likely: agent pool gap)
- **Competitive pressures in the AI industry tend to create a structural race-to-the-bottom dynamic that makes binding external regulation — rather than voluntary industry norms — a necessary condition for ensuring companies invest adequately in safety.** (empty: disagree side - likely: agent pool gap)
- **Governments must implement proactive structural interventions — including labour market policies, antitrust measures, and infrastructure investment — specifically to prevent AI from deepening inequality and concentrating power, rather than relying on market forces or direct safety governance alone.** (empty: disagree side - likely: agent pool gap)
- **AI companies should actively and publicly articulate a bold positive vision for AI-enabled flourishing, even at the risk of appearing propagandistic, because doing so is strategically necessary for building the coalitions needed to govern AI responsibly.** (empty: disagree side - likely: agent pool gap)
- **Free markets, by rewarding productivity gains, will ensure that AI-driven automation raises wages and expands employment opportunities rather than causing sustained large-scale job displacement.** (empty: disagree side - likely: agent pool gap)
- **Preventing loss of human control over societal systems requires treating deception by misaligned AGI as a near-term, realistic threat that architectural and institutional safeguards must specifically address — not merely a speculative long-term risk.** (empty: disagree side - likely: agent pool gap)
- **Unilateral slowdowns or pauses by safety-conscious AI developers tend to increase overall risk by ceding competitive ground to less safety-conscious actors, making such pauses counterproductive to their intended goals.** (empty: disagree side - likely: agent pool gap)
- **Competitive market pressures tend to make voluntary industry safety frameworks structurally unreliable, because they systematically reward non-compliance and create incentives to underinvest in safety — meaning external enforcement is necessary rather than optional.** (empty: disagree side - likely: agent pool gap)
- **Voluntary industry commitments to AI safety are insufficient without binding enforcement mechanisms, because competitive market pressures will tend to override them in practice.** (empty: disagree side - likely: agent pool gap)
- **Without a solved alignment approach, building superintelligent AI under current conditions significantly increases the risk of human extinction to the point where catastrophic outcomes should be treated as the likely default, not a remote tail risk.** (empty: disagree side - likely: agent pool gap)
- **Mandatory contribution of a fixed percentage of training budgets to a shared AI safety research commons is a necessary and justified intervention to counteract competitive underinvestment in alignment, and voluntary market mechanisms are insufficient to solve this structural problem.** (empty: disagree side - likely: agent pool gap)

### Cluster Stability

| Threshold | Total Clusters | Multi-member |
|---|---|---|
| 30% | 4 | 3 |
| 50% | 7 | 2 |
| 70% | 9 | 1 |

*1 cluster(s) dissolve between 70% and 80%.*

---

*Continue: submit positions to extend beyond Round 3. Replicate: run again to test stability. Deliberation: `f2e9b0ae-055f-438f-9991-89e02a26a152`*

