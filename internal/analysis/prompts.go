package analysis

// Prompts adapted from T3C (tttc-light-js/common/prompts/index.ts).
// These have been extensively tested on real deliberation data.

const systemPrompt = `You are a professional research assistant. You have helped run many public consultations, surveys and citizen assemblies. You have good instincts when it comes to extracting interesting insights. You are familiar with public consultation tools like Pol.is and you understand the benefits for working with very clear, concise claims that other people would be able to vote on.`

// taxonomyPrompt: {{TOPIC}} = deliberation topic, {{POSITIONS}} = positions text
const taxonomyPrompt = `I will give you a list of positions from agents in a deliberation about "{{TOPIC}}". I want you to propose a way to break down the information contained in these positions into topics and subtopics of interest.

SIZE CONSTRAINTS:
- Maximum {{MAX_TOPICS}} topics
- Maximum {{MAX_SUBTOPICS}} subtopics per topic
- Merge closely related areas into a single topic rather than creating separate ones
- Only create subtopics for genuinely distinct dimensions within a topic
{{PRIOR_TAXONOMY}}

DESCRIPTION LENGTH REQUIREMENTS:
- Topic names: Keep very concise (2-5 words)
- Topic descriptions: MUST be 25-35 words. Provide a clear overview of what this topic covers.
- Subtopic names: Keep concise (2-6 words)
- Subtopic descriptions: MUST be 70-90 words. Provide detailed context about what perspectives and issues fall under this subtopic.

IMPORTANT: The descriptions should be substantive and informative, not just brief summaries. Use the full word count to provide meaningful context that helps readers understand the scope and nuances of each topic and subtopic.

Now here are the positions:
{{POSITIONS}}`

// claimExtractionPrompt: {{AGENT_NUM}} = agent num, {{TOPIC}} = deliberation topic, {{TAXONOMY}} = taxonomy text, {{CONTENT}} = position content
const claimExtractionPrompt = `I'm going to give you a position made by Participant {{AGENT_NUM}} in a deliberation about "{{TOPIC}}", and a list of topics and subtopics which have already been extracted. I want you to extract the most important concise claims that the participant may support. We are only interested in claims that can be mapped to one of the given topic and subtopic. The claim must be fairly general but not a platitude. It must be something that other people may potentially disagree with. Each claim must also be atomic.

CRITICAL EXTRACTION RULES - STRICT ENFORCEMENT:
1. Extract ZERO claims for positions that are vague, meandering, or lack a clear point
2. Extract ZERO claims for anecdotes without a broader principle
3. Extract multiple claims if the position contains distinct, substantial debatable positions, but treat similar points as variations of one claim rather than separate claims
4. ONLY extract claims that represent genuinely debatable positions
5. DO NOT extract claims that are:
   - Platitudes or truisms ("communication is important")
   - Mere descriptions of experiences without advocating a position
   - Minor variations of the same underlying idea
   - Questions or musings without clear stances

QUALITY THRESHOLD: If you're unsure whether a position contains a substantial claim worth extracting, err on the side of extracting NOTHING. It's better to miss marginal claims than to create noise.

For each claim, please also provide a relevant quote from the position. The quote must be as concise as possible while still supporting the argument. You may use "[...]" in the quote to skip the less interesting bits.

Now here is the list of topics/subtopics:
{{TAXONOMY}}

And then here is the position:
{{CONTENT}}`

// claimDeduplicationPrompt: {{TOPIC}} = deliberation topic, {{SUBTOPIC}} = subtopic name, {{CLAIMS}} = claims text
const claimDeduplicationPrompt = `You are grouping claims to help users understand which themes matter most in this deliberation about "{{TOPIC}}", under the subtopic "{{SUBTOPIC}}". Your goal is to consolidate similar claims into well-supported groups while preserving genuinely unique perspectives.

You will receive a list of claims with IDs and claim text for each.

GROUPING DECISION FRAMEWORK:

Step 1 - Identify Core Themes:
Ask yourself: "What are the 3-5 main ideas or concerns being expressed across ALL these claims?"
These themes become your candidate groups.

Step 2 - Apply Grouping Criteria:
Group claims together if they share ANY of these:
- Same underlying concern or problem (even if different solutions proposed)
- Same recommendation or solution (even if different reasoning)
- Same value or principle being expressed
- Different aspects of the same topic
- Specific examples of a general pattern

Keep claims separate ONLY if:
- They address completely different topics within this subtopic
- They represent opposing positions (agree vs disagree on something)
- One is about process/how, the other is about outcome/what

Step 3 - Write Strong Group Claims:
For each group, write a claim that:
- Captures the shared essence at a higher level of abstraction
- Uses language and concepts that appear in the original claims
- Is specific enough to be meaningful (avoid vague platitudes)
- Could plausibly be supported by all claims in the group
- Uses clear, simple language
- Stays faithful to what participants actually said

Step 4 - Validate Your Groups:
- Prioritize natural thematic coherence over hitting specific group counts
- Each group should represent a distinct, meaningful theme
- Avoid over-consolidation: don't force claims together just to reduce group count
- The right number of groups depends on the natural diversity of perspectives in the input

Now here are the claims to group:
{{CLAIMS}}`

// cruxPrompt: {{TOPIC}} = deliberation topic, {{TOPIC_NAME}} = topic name, {{SUBTOPIC}} = subtopic name, {{SUBTOPIC_DESC}} = subtopic description, {{CLAIMS}} = claims text
const cruxPrompt = `I'm going to give you a subtopic from a deliberation about "{{TOPIC}}" with a description and a list of high-level claims about this subtopic made by different participants, identified by numeric IDs (like 0, 1, 2, etc.). Please synthesize these claims into one new, specific, maximally controversial statement called a "cruxClaim". This cruxClaim should divide the participants into "agree" and "disagree" groups or sides, based on all their statements on this subtopic.

Topic: {{TOPIC_NAME}}
Subtopic: {{SUBTOPIC}}
Description: {{SUBTOPIC_DESC}}

For each participant who made claims in this subtopic, categorize them as:
- "agree": Would agree with the cruxClaim based on their statements
- "disagree": Would disagree with the cruxClaim based on their statements
- "no_clear_position": Mentioned the topic but didn't take a clear stance on this specific crux

Please explain your reasoning and assign participants into the three groups. Make the cruxClaim as precise and unique as possible to the given subtopic and claims, and pick a cruxClaim that best balances the "agree" and "disagree" sides, with close to the same number of participants on each side.

CRITICAL VALIDITY REQUIREMENTS:
- The cruxClaim MUST have at least one participant who would agree AND at least one who would disagree based on their actual statements. If you cannot identify clear participants on both sides, do NOT generate the crux — return an empty agree or disagree list and explain why no balanced crux was possible for this subtopic.
- Avoid absolute deterministic language in the cruxClaim. Use conditional framing ("tends to", "creates strong pressure toward", "significantly increases the risk of") rather than absolutes ("inevitably", "will always", "makes it impossible", "systematically overrides"). The crux should be debatable, not a straw man.

IMPORTANT: Format requirements for your response:
1. In the agree/disagree/no_clear_position lists, use ONLY the exact numeric IDs from the input (like 0, 1, 2)
2. Do NOT add prefixes like "Person" or "Participant" to these numeric IDs
3. Claims may include quote="" attributes with verbatim excerpts from original positions. Reference these quotes in your explanation to ground the crux in what participants actually said.
4. In the explanation field, write in natural, reader-friendly language:
   - Use natural phrases like "several participants" or "some speakers" instead of listing IDs
   - Use "this claim" or "the statement" instead of technical terms like "cruxClaim"
   - Avoid programming conventions like "no_clear_position" - use "didn't take a clear stance"
   - Write as if explaining to a general audience, not developers

Now here are the participant claims:
{{CLAIMS}}`

// summaryPrompt: {{TOPIC}} = deliberation topic, {{TOPIC_NAME}} = topic name, {{POSITIONS}} = positions text
const summaryPrompt = `I'm going to give you a single topic from a deliberation about "{{TOPIC}}" with positions from participants.

Generate a detailed summary (100-140 words) for the topic "{{TOPIC_NAME}}" that:
- Synthesizes the key themes and patterns across all subtopics
- Highlights the main claims and perspectives expressed
- Captures the breadth of discussion on this topic
- Is comprehensive yet concise

Now here are the positions:
{{POSITIONS}}`

// compromisePrompt: {{TOPIC}} = deliberation topic, {{CRUXES}} = cruxes text, {{BRIDGING}} = bridging text, {{CLUSTERS}} = cluster descriptions
const compromisePrompt = `You are mediating a deliberation about "{{TOPIC}}". Your task is to draft a compromise statement that the maximum number of participants could endorse.

You have:
1. The key cruxes (points of maximum disagreement)
2. Bridging statements (positions that already get cross-cluster agreement)
3. The opinion clusters and their representative views

INSTRUCTIONS:
- Build on the bridging statements — these already have cross-cluster support
- For each crux, find a framing that acknowledges both sides' core concerns without fully endorsing either
- Use specific, concrete language — not vague platitudes
- The statement should be 100-200 words
- It should be something agents could vote on in the next round

DO NOT:
- Simply average the positions (that satisfies no one)
- Ignore the cruxes (the whole point is addressing them)
- Use hedge words like "perhaps" or "it could be argued" — be direct
- Propose something no participant actually values

The goal is not unanimous agreement but maximum endorsement — a statement that a supermajority of participants would accept as a reasonable basis for moving forward, even if it's not their first choice.

CRUXES (key disagreements):
{{CRUXES}}

BRIDGING STATEMENTS (cross-cluster agreement):
{{BRIDGING}}

OPINION CLUSTERS:
{{CLUSTERS}}`

// reframePrompt: {{POSITION}} = original position, {{OTHER_POSITIONS}} = other positions summary, {{CRUXES}} = cruxes summary
const reframePrompt = `You are a skilled mediator. Reframe the following position to emphasize common ground with other participants while preserving the core argument.

ORIGINAL POSITION:
{{POSITION}}

OTHER PARTICIPANTS' KEY CONCERNS:
{{OTHER_POSITIONS}}

KEY DISAGREEMENTS (CRUXES):
{{CRUXES}}

INSTRUCTIONS:
- Keep the core argument intact — don't change what the agent believes
- Reframe to acknowledge the valid concerns of opponents
- Use language that builds bridges rather than walls
- Be specific and concrete, not vague
- The reframed version should be something the original agent would still endorse
- 100-200 words

Output ONLY the reframed position text, nothing else.`

// cruxClassificationPrompt: {{TOPIC}} = deliberation topic, {{CLAIMS}} = JSON array of crux claims
const cruxClassificationPrompt = `You are analyzing cruxes (key disagreements) from a deliberation about "{{TOPIC}}".

For each crux, classify it as:
- "factual": Disagreement about what is true or what will happen. Evidence or data could resolve it.
- "value": Disagreement about what is desirable or how much something matters. About preferences or principles.
- "mixed": Both factual and value components.

Also rate resolvability from 0.0 to 1.0:
- 1.0 = could be fully resolved with evidence
- 0.5 = partially resolvable, but some value tension remains
- 0.0 = purely about values, no amount of evidence resolves it

CRUXES:
{{CLAIMS}}

Respond with a JSON array. Each element must have:
- "claim": the crux claim text (exact match)
- "type": "factual" | "value" | "mixed"
- "resolvability": 0.0-1.0

Output ONLY the JSON array, no explanation.`

// paretoPrompt: {{TOPIC}} = deliberation topic, {{PROPOSALS}} = proposals text, {{CRITERIA}} = criteria text
const paretoPrompt = `You are analyzing proposals from a deliberation about "{{TOPIC}}" to find the Pareto frontier.

PROPOSALS:
{{PROPOSALS}}

EVALUATION CRITERIA:
{{CRITERIA}}

For each proposal, rate it 0-10 on each criterion. Then identify:
1. Pareto-efficient proposals: no other proposal beats them on ALL criteria simultaneously
2. Dominated proposals: beaten by another proposal on every criterion

Respond with JSON:
{
  "ratings": [{"proposal": "...", "scores": {"criterion1": 5, "criterion2": 8}}],
  "pareto_efficient": ["proposal text..."],
  "dominated": ["proposal text..."]
}

Output ONLY the JSON, no explanation.`

// agreementPrompt is used when there are no votes (e.g. bilateral negotiations).
// It asks the LLM to identify shared ground and potential compromises directly from positions.
// {{TOPIC}} = deliberation topic, {{POSITIONS}} = positions text, {{CRUX_TEXT}} = cruxes text
const agreementPrompt = `You are analyzing a deliberation about "{{TOPIC}}" between a small number of participants. There are no votes — you must identify agreement and potential compromises directly from the position texts.

POSITIONS:
{{POSITIONS}}

KEY DISAGREEMENTS (CRUXES):
{{CRUX_TEXT}}

Your task:
1. SHARED GROUND: Identify 1-5 specific points where participants actually agree (stated or implied). Look for overlapping concerns, shared goals, mutual acknowledgments, compatible proposals. Be specific — quote or closely paraphrase the actual language used.

2. POTENTIAL COMPROMISES: For each crux, propose 1 concrete compromise that addresses both sides' core concerns. Each should be specific and actionable, not a vague "both sides should talk more."

Respond with JSON:
{
  "shared_ground": [{"content": "specific shared position text", "participants": ["agent-1", "agent-2"]}],
  "compromises": [{"crux": "the crux claim", "proposal": "specific compromise proposal", "rationale": "why both sides could accept this"}]
}

Output ONLY the JSON, no explanation.`
