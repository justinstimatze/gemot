// t3c-import reads a Talk to the City report JSON and creates gemot deliberations
// from its controversial cruxes.
//
// Two modes:
//
//	speaker mode (default): one deliberation per crux, speakers become agents
//	structural mode: one deliberation, topology-derived agents
//	  (cluster steelmen, speakers, topic adversaries, bridge, dissent, empty chairs)
//	  derived from the report's topology. Two-round phased protocol.
//
// Usage:
//
//	go run scripts/t3c-import/ report.json
//	go run scripts/t3c-import/ report.json --mode structural
//	go run scripts/t3c-import/ report.json --mode structural --rounds 3 --spot-check --report report.md
//	go run scripts/t3c-import/ report.json --mode structural --dry-run
//	go run scripts/t3c-import/ report.json --template auto --threshold 0.3
//
// Flags:
//
//	--mode structural    Topology-derived agents (recommended)
//	--rounds N           1=single-shot, 2=phased (default), 3=position revision + resolutions
//	--report FILE        Write markdown report to file
//	--spot-check         Verify T3C input quality with Haiku
//	--null-control       Run shuffled null control for signal validation
//	--replicate N        Run N replications for stability testing
//	--dry-run            Print agents without creating deliberation
//	--threshold F        Jaccard clustering threshold (default 0.5)
//	--template NAME      Deliberation template (default: assembly)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// T3C report types (subset we need for import)

type T3CReport struct {
	Data     [2]json.RawMessage `json:"data"`     // ["v0.2", ReportDataObj]
	Metadata [2]json.RawMessage `json:"metadata"` // ["v0.2", ReportMetadataObj]
}

type ReportData struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Topics      []Topic  `json:"topics"`
	Sources     []Source `json:"sources"`
	AddOns      *AddOns  `json:"addOns,omitempty"`
}

type Topic struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Subtopics   []Subtopic `json:"subtopics"`
}

type Subtopic struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Claims      []Claim `json:"claims"`
}

type Claim struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	Quotes        []Quote `json:"quotes"`
	SimilarClaims []Claim `json:"similarClaims,omitempty"`
}

type Quote struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Reference Reference `json:"reference"`
}

type Reference struct {
	ID        string `json:"id"`
	SourceID  string `json:"sourceId"`
	Interview string `json:"interview"`
}

type Source struct {
	ID        string `json:"id"`
	Interview string `json:"interview"`
}

type AddOns struct {
	SubtopicCruxes    []SubtopicCrux     `json:"subtopicCruxes,omitempty"`
	SpeakerCruxMatrix *SpeakerCruxMatrix `json:"speakerCruxMatrix,omitempty"`
}

type SubtopicCrux struct {
	Topic            string   `json:"topic"`
	Subtopic         string   `json:"subtopic"`
	CruxClaim        string   `json:"cruxClaim"`
	Agree            []string `json:"agree"`
	Disagree         []string `json:"disagree"`
	NoPosition       []string `json:"no_clear_position"`
	Explanation      string   `json:"explanation"`
	ControversyScore float64  `json:"controversyScore"`
}

type SpeakerCruxMatrix struct {
	Speakers   []string   `json:"speakers"`
	CruxLabels []string   `json:"cruxLabels"`
	Matrix     [][]string `json:"matrix"` // "agree" | "disagree" | "no_position"
}

type ReportMetadata struct {
	Author       string `json:"author"`
	Organization string `json:"organization,omitempty"`
}

// Structural agent with metadata for vote seeding
type agentPlan struct {
	ID       string
	Role     string
	Position string
	Kind     string   // "speaker", "steelman", "adversary", "bridge", "dissent", "empty-chair"
	Round    int      // 1 or 2
	Cluster  *cluster // non-nil for speaker/steelman agents
	Topic    string   // for adversary agents: the T3C topic name
}

// MCP helpers

type authTransport struct {
	base  http.RoundTripper
	token string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}

func connect(url, secret string) (*sdkmcp.ClientSession, error) {
	transport := &sdkmcp.SSEClientTransport{
		Endpoint: url,
		HTTPClient: &http.Client{
			Transport: &authTransport{base: http.DefaultTransport, token: secret},
		},
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "t3c-import", Version: "2.0"}, nil)
	return client.Connect(context.Background(), transport, nil)
}

func call(session *sdkmcp.ClientSession, name string, args map[string]any) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s failed: %v\n", name, err)
		os.Exit(1)
	}
	if res.IsError && len(res.Content) > 0 {
		fmt.Fprintf(os.Stderr, "%s error: %s\n", name, res.Content[0].(*sdkmcp.TextContent).Text)
		os.Exit(1)
	}
	if len(res.Content) == 0 {
		return ""
	}
	text := res.Content[0].(*sdkmcp.TextContent).Text
	if idx := strings.Index(text, "\n\n---\n"); idx != -1 {
		text = text[:idx]
	}
	return text
}

func callSoft(session *sdkmcp.ClientSession, name string, args map[string]any) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return ""
	}
	if res.IsError || len(res.Content) == 0 {
		return ""
	}
	text := res.Content[0].(*sdkmcp.TextContent).Text
	if idx := strings.Index(text, "\n\n---\n"); idx != -1 {
		text = text[:idx]
	}
	return text
}

// --- Helpers ---

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// cruxLabel builds a label matching T3C's matrix format, handling both → and ->
func cruxLabel(topic, subtopic string) string {
	return topic + " -> " + subtopic
}

// normLabel normalizes Unicode arrows to ASCII for matching
func normLabel(s string) string {
	return strings.ReplaceAll(s, " → ", " -> ")
}

func slugify(s string) string {
	s = strings.ToLower(s)
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

func parseSpeakerID(s string) string {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) == 2 {
		return strings.ToLower(strings.TrimSpace(parts[1]))
	}
	return strings.ToLower(strings.TrimSpace(s))
}

func parseSpeakerName(s string) string {
	parts := strings.SplitN(s, ":", 2)
	name := s
	if len(parts) == 2 {
		name = strings.TrimSpace(parts[1])
	}
	return name // preserve original casing for display
}

func pickTemplate(controversy float64) string {
	switch {
	case controversy >= 0.7:
		return "negotiation"
	case controversy >= 0.4:
		return "jury"
	case controversy >= 0.2:
		return "assembly"
	default:
		return "consensus"
	}
}

func findClaimsForSpeaker(data *ReportData, speakerName string, topicTitle, subtopicTitle string) []string {
	sourceIDs := map[string]bool{}
	for _, s := range data.Sources {
		if strings.EqualFold(s.Interview, speakerName) {
			sourceIDs[s.ID] = true
		}
	}
	var claims []string
	for _, topic := range data.Topics {
		if topicTitle != "" && topic.Title != topicTitle {
			continue
		}
		for _, sub := range topic.Subtopics {
			if subtopicTitle != "" && sub.Title != subtopicTitle {
				continue
			}
			for _, claim := range sub.Claims {
				if claimHasSpeaker(claim, sourceIDs) {
					claims = append(claims, claim.Title)
				}
			}
		}
	}
	return claims
}

func findAllClaimsForSpeaker(data *ReportData, speakerName string) []string {
	return findClaimsForSpeaker(data, speakerName, "", "")
}

// claimsForSpeakerBySubtopic returns a map of subtopicTitle -> claim titles
// for a speaker, preserving the structural context of where each claim appears.
func claimsForSpeakerBySubtopic(data *ReportData, speakerName string) map[string][]string {
	sourceIDs := map[string]bool{}
	for _, s := range data.Sources {
		if strings.EqualFold(s.Interview, speakerName) {
			sourceIDs[s.ID] = true
		}
	}
	result := map[string][]string{}
	for _, topic := range data.Topics {
		for _, sub := range topic.Subtopics {
			for _, claim := range sub.Claims {
				if claimHasSpeaker(claim, sourceIDs) {
					key := topic.Title + " > " + sub.Title
					result[key] = append(result[key], claim.Title)
				}
			}
		}
	}
	return result
}

// speakerSubtopicSet returns the set of subtopics a speaker has claims in.
func speakerSubtopicSet(data *ReportData, speakerName string) map[string]bool {
	bySubtopic := claimsForSpeakerBySubtopic(data, speakerName)
	set := map[string]bool{}
	for k := range bySubtopic {
		set[k] = true
	}
	return set
}

// sharedClaimCount returns how many claim titles two speakers share (attributed to both).
func sharedClaimCount(data *ReportData, speaker1, speaker2 string) int {
	claims1 := map[string]bool{}
	for _, c := range findAllClaimsForSpeaker(data, speaker1) {
		claims1[c] = true
	}
	shared := 0
	for _, c := range findAllClaimsForSpeaker(data, speaker2) {
		if claims1[c] {
			shared++
		}
	}
	return shared
}

// jaccardSubtopics computes Jaccard similarity on the subtopic sets of two speakers.
func jaccardSubtopics(set1, set2 map[string]bool) float64 {
	if len(set1) == 0 && len(set2) == 0 {
		return 0
	}
	intersection := 0
	for k := range set1 {
		if set2[k] {
			intersection++
		}
	}
	union := len(set1)
	for k := range set2 {
		if !set1[k] {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func claimHasSpeaker(c Claim, sourceIDs map[string]bool) bool {
	for _, q := range c.Quotes {
		if sourceIDs[q.Reference.SourceID] {
			return true
		}
	}
	for _, sc := range c.SimilarClaims {
		if claimHasSpeaker(sc, sourceIDs) {
			return true
		}
	}
	return false
}

// distinctiveClaims returns up to maxN claims for a set of speakers, preferring
// claims that are distinctive to these speakers (not shared with most others).
func distinctiveClaims(data *ReportData, speakerNames []string, allClusters []cluster, maxN int) []string {
	// Gather all claims for these speakers
	seen := map[string]bool{}
	var myClaims []string
	for _, name := range speakerNames {
		for _, claim := range findAllClaimsForSpeaker(data, name) {
			if !seen[claim] {
				myClaims = append(myClaims, claim)
				seen[claim] = true
			}
		}
	}
	if len(myClaims) <= maxN {
		return myClaims
	}

	// Count how many clusters each claim appears in
	claimFreq := map[string]int{}
	for _, cl := range allClusters {
		clSeen := map[string]bool{}
		for _, m := range cl.Members {
			for _, claim := range findAllClaimsForSpeaker(data, parseSpeakerID(m)) {
				if !clSeen[claim] {
					claimFreq[claim]++
					clSeen[claim] = true
				}
			}
		}
	}

	// Sort by ascending frequency (most distinctive first)
	sort.Slice(myClaims, func(i, j int) bool {
		return claimFreq[myClaims[i]] < claimFreq[myClaims[j]]
	})
	return myClaims[:maxN]
}

// --- Cluster type ---

type cluster struct {
	ID      int
	Members []string // speaker names
}

func getMCPConfig() (string, string) {
	mcpURL := os.Getenv("GEMOT_URL")
	if mcpURL == "" {
		mcpURL = "http://localhost:8080/mcp"
	}
	secret := os.Getenv("GEMOT_API_SECRET")
	if secret == "" {
		if b, err := os.ReadFile(".env"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "GEMOT_API_SECRET=") {
					secret = strings.TrimPrefix(line, "GEMOT_API_SECRET=")
				}
			}
		}
	}
	return mcpURL, secret
}

func parseReport(path string) (ReportData, ReportMetadata) {
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reading report: %v\n", err)
		os.Exit(1)
	}
	var report T3CReport
	if err := json.Unmarshal(raw, &report); err != nil {
		fmt.Fprintf(os.Stderr, "parsing report: %v\n", err)
		os.Exit(1)
	}
	var data ReportData
	if err := json.Unmarshal(report.Data[1], &data); err != nil {
		fmt.Fprintf(os.Stderr, "parsing report data: %v\n", err)
		os.Exit(1)
	}
	var meta ReportMetadata
	json.Unmarshal(report.Metadata[1], &meta)
	return data, meta
}

func main() {
	mode := flag.String("mode", "speaker", "Import mode: speaker or structural")
	template := flag.String("template", "auto", "Governance template: auto, negotiation, jury, assembly, consensus, parliament")
	threshold := flag.Float64("threshold", 0.3, "Minimum controversy score")
	allCruxes := flag.Bool("all-cruxes", false, "Import all cruxes regardless of controversy")
	dryRun := flag.Bool("dry-run", false, "Print plan without connecting")
	url := flag.String("url", "", "Gemot MCP URL (default: GEMOT_URL env or http://localhost:8080/mcp)")
	groupID := flag.String("group", "t3c-import", "Group ID")
	rounds := flag.Int("rounds", 2, "Rounds: 1=single-shot, 2=phased protocol, 3=position revision")
	reportPath := flag.String("report", "", "Write markdown report to file")
	nullControl := flag.Bool("null-control", false, "Run null control (shuffled data) for validation")
	spotCheck := flag.Bool("spot-check", false, "LLM-verify 15% of stance assignments against source quotes")
	replicate := flag.Int("replicate", 0, "Run N replication runs to test pipeline stability")
	coverageAudit := flag.Bool("coverage-audit", false, "Detect missing perspectives in unchallenged positions")
	resumeID := flag.String("resume", "", "Resume from existing deliberation ID (skip creation/submission, re-trigger incomplete analysis)")
	named := flag.Bool("named", false, "Use real speaker names in report (default: anonymized to prevent false attribution)")
	flag.Parse()

	if flag.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "Usage: t3c-import [flags] <report.json>\n\n")
		fmt.Fprintf(os.Stderr, "Modes:\n")
		fmt.Fprintf(os.Stderr, "  speaker     One deliberation per crux, speakers become agents (default)\n")
		fmt.Fprintf(os.Stderr, "  structural  One deliberation, topology-derived agents, phased protocol\n\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	data, _ := parseReport(flag.Arg(0))
	if data.AddOns == nil || len(data.AddOns.SubtopicCruxes) == 0 {
		fmt.Fprintf(os.Stderr, "report has no cruxes (addOns.subtopicCruxes empty)\n")
		fmt.Fprintf(os.Stderr, "hint: enable crux detection in T3C before importing\n")
		os.Exit(1)
	}

	mcpURL := *url
	if mcpURL == "" {
		mcpURL, _ = getMCPConfig()
	}

	switch *mode {
	case "speaker":
		runSpeakerMode(&data, mcpURL, *template, *threshold, *allCruxes, *dryRun, *groupID)
	case "structural":
		cfg := &pipelineConfig{
			MCPURL:        mcpURL,
			Template:      *template,
			Threshold:     *threshold,
			AllCruxes:     *allCruxes,
			DryRun:        *dryRun,
			GroupID:       *groupID,
			Rounds:        *rounds,
			ReportPath:    *reportPath,
			NullControl:   *nullControl,
			SpotCheck:     *spotCheck,
			ReplicateN:    *replicate,
			CoverageAudit: *coverageAudit,
			ResumeID:      *resumeID,
			Named:         *named,
		}
		runStructuralMode(&data, cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown mode: %s (use speaker or structural)\n", *mode)
		os.Exit(1)
	}
}

// --- Speaker mode (unchanged from v1) ---

func runSpeakerMode(data *ReportData, mcpURL, tmplFlag string, threshold float64, allCruxes, dryRun bool, groupID string) {
	cruxes := filterCruxes(data.AddOns.SubtopicCruxes, threshold, allCruxes)

	speakers := map[string]bool{}
	for _, c := range cruxes {
		for _, s := range c.Agree {
			speakers[parseSpeakerID(s)] = true
		}
		for _, s := range c.Disagree {
			speakers[parseSpeakerID(s)] = true
		}
	}

	totalClaims := countClaims(data)
	fmt.Fprintf(os.Stderr, "T3C Report: %s\n", data.Title)
	fmt.Fprintf(os.Stderr, "  %d topics, %d claims, %d sources\n", len(data.Topics), totalClaims, len(data.Sources))
	fmt.Fprintf(os.Stderr, "  %d cruxes total, %d above threshold %.2f\n",
		len(data.AddOns.SubtopicCruxes), len(cruxes), threshold)
	fmt.Fprintf(os.Stderr, "  %d speakers involved\n", len(speakers))
	fmt.Fprintf(os.Stderr, "\nCreating %d deliberations:\n\n", len(cruxes))

	for i, c := range cruxes {
		tmpl := tmplFlag
		if tmpl == "auto" {
			tmpl = pickTemplate(c.ControversyScore)
		}
		fmt.Fprintf(os.Stderr, "  %d. [%.0f%% controversy] %s\n", i+1, c.ControversyScore*100, c.CruxClaim[:min(80, len(c.CruxClaim))])
		fmt.Fprintf(os.Stderr, "     template: %s | agree: %d | disagree: %d\n", tmpl, len(c.Agree), len(c.Disagree))
	}

	if dryRun {
		fmt.Fprintf(os.Stderr, "\n[dry run] would create %d deliberations\n", len(cruxes))
		return
	}

	_, secret := getMCPConfig()
	session, err := connect(mcpURL, secret)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect failed: %v\n", err)
		os.Exit(1)
	}
	defer session.Close()

	type result struct {
		CruxClaim      string  `json:"crux_claim"`
		Controversy    float64 `json:"controversy"`
		DeliberationID string  `json:"deliberation_id"`
		Template       string  `json:"template"`
		Positions      int     `json:"positions"`
		Votes          int     `json:"votes"`
		JoinCode       string  `json:"join_code,omitempty"`
	}
	var results []result

	for i, crux := range cruxes {
		tmpl := tmplFlag
		if tmpl == "auto" {
			tmpl = pickTemplate(crux.ControversyScore)
		}

		topic := fmt.Sprintf("[T3C import] %s → %s: %s", crux.Topic, crux.Subtopic, crux.CruxClaim)
		if len(topic) > 300 {
			topic = topic[:300]
		}

		fmt.Fprintf(os.Stderr, "\n[%d/%d] %s\n", i+1, len(cruxes), crux.CruxClaim[:min(60, len(crux.CruxClaim))])

		createJSON := call(session, "deliberation", map[string]any{
			"action": "create", "topic": topic, "template": tmpl,
			"type": "reasoning", "group_id": groupID,
		})
		var created struct {
			DeliberationID string `json:"deliberation_id"`
		}
		json.Unmarshal([]byte(createJSON), &created)
		if created.DeliberationID == "" {
			fmt.Fprintf(os.Stderr, "  failed, skipping\n")
			continue
		}

		posCount := submitSpeakerPositions(session, data, &crux, created.DeliberationID)
		voteCount := seedSpeakerVotes(session, data, &crux, created.DeliberationID)
		joinCode := generateJoinCode(session, created.DeliberationID)

		fmt.Fprintf(os.Stderr, "  %d positions, %d votes → %s (join: %s)\n", posCount, voteCount, created.DeliberationID, joinCode)
		results = append(results, result{
			CruxClaim: crux.CruxClaim, Controversy: crux.ControversyScore,
			DeliberationID: created.DeliberationID, Template: tmpl,
			Positions: posCount, Votes: voteCount, JoinCode: joinCode,
		})
	}

	out, _ := json.MarshalIndent(results, "", "  ")
	fmt.Println(string(out))
	fmt.Fprintf(os.Stderr, "\nDone. %d deliberations created.\n", len(results))
}

// --- Structural mode v2: phased protocol with vote seeding ---

func runStructuralMode(data *ReportData, cfg *pipelineConfig) {
	totalClaims := countClaims(data)

	setup := buildR1Setup(data, cfg.Threshold, "t3c-")
	clusters := setup.clusters
	controversialCruxes := setup.controversialCruxes
	consensusCruxes := setup.consensusCruxes
	topicCruxes := setup.topicCruxes
	r1Agents := setup.agents

	// Pick template
	maxControversy := 0.0
	for _, c := range controversialCruxes {
		if c.ControversyScore > maxControversy {
			maxControversy = c.ControversyScore
		}
	}
	tmpl := cfg.Template
	if tmpl == "auto" {
		tmpl = pickTemplate(maxControversy)
	}

	// Ensure template fits agent count
	if len(r1Agents) > 10 && (tmpl == "negotiation" || tmpl == "review") {
		tmpl = "assembly"
	}
	if len(r1Agents)+4 > 10 && (tmpl == "negotiation" || tmpl == "review") {
		tmpl = "assembly"
	}

	// --- Summary ---
	fmt.Fprintf(os.Stderr, "T3C Report: %s (structural mode v2)\n", data.Title)
	fmt.Fprintf(os.Stderr, "  %d topics, %d claims, %d sources\n", len(data.Topics), totalClaims, len(data.Sources))
	fmt.Fprintf(os.Stderr, "  %d cruxes (%d controversial across %d topics, %d consensus)\n",
		len(data.AddOns.SubtopicCruxes), len(controversialCruxes), len(topicCruxes), len(consensusCruxes))
	fmt.Fprintf(os.Stderr, "  %d clusters (%d multi-member, %d singletons)\n",
		len(clusters), countMultiMember(clusters), len(clusters)-countMultiMember(clusters))
	fmt.Fprintf(os.Stderr, "  template: %s | rounds: %d\n\n", tmpl, cfg.Rounds)

	fmt.Fprintf(os.Stderr, "Round 1 agents (%d):\n", len(r1Agents))
	for _, a := range r1Agents {
		fmt.Fprintf(os.Stderr, "  %-40s %s\n", a.ID, a.Role)
	}
	if cfg.Rounds >= 2 {
		fmt.Fprintf(os.Stderr, "\nRound 2 agents: bridge + dissent + empty chairs (determined after R1 analysis)\n")
	}
	if cfg.Rounds >= 3 {
		fmt.Fprintf(os.Stderr, "Round 3 agents: revised speaker positions (LLM-generated, informed by R2 findings)\n")
	}

	if cfg.DryRun {
		fmt.Fprintf(os.Stderr, "\n[dry run] would create 1 deliberation with %d+ agents\n", len(r1Agents))
		for _, a := range r1Agents {
			fmt.Fprintf(os.Stderr, "\n--- %s ---\n%s\n", a.ID, a.Position)
		}
		return
	}

	// --- Connect ---
	_, secret := getMCPConfig()
	session, err := connect(cfg.MCPURL, secret)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect failed: %v\n", err)
		os.Exit(1)
	}
	defer session.Close()

	// --- Create or resume deliberation ---
	var delibID string
	resuming := cfg.ResumeID != ""

	if resuming {
		delibID = cfg.ResumeID
		fmt.Fprintf(os.Stderr, "\nResuming deliberation: %s\n", delibID)
	} else {
		topic := fmt.Sprintf("[T3C structural · AI-synthesized agents] %s", data.Title)
		if data.Description != "" {
			topic += " — " + data.Description
		}
		if len(topic) > 300 {
			topic = topic[:300]
		}

		createJSON := call(session, "deliberation", map[string]any{
			"action": "create", "topic": topic, "template": tmpl,
			"type": "reasoning", "group_id": cfg.GroupID,
		})
		var created struct {
			DeliberationID string `json:"deliberation_id"`
		}
		json.Unmarshal([]byte(createJSON), &created)
		if created.DeliberationID == "" {
			fmt.Fprintf(os.Stderr, "failed to create deliberation\n")
			os.Exit(1)
		}
		delibID = created.DeliberationID
		fmt.Fprintf(os.Stderr, "\nDeliberation: %s\n", delibID)
	}

	// --- Round 1: submit + vote + analyze ---
	fmt.Fprintf(os.Stderr, "\n=== Round 1 ===\n")
	voteCount := 0
	if resuming {
		fmt.Fprintf(os.Stderr, "  (resume) skipping submission, fetching existing result...\n")
	} else {
		for _, a := range r1Agents {
			fmt.Fprintf(os.Stderr, "  submit: %s\n", a.ID)
			content := a.Position
			if !cfg.Named {
				content = anonymizeSpeakers(content, r1Agents)
			}
			call(session, "participate", map[string]any{
				"action": "submit_position", "deliberation_id": delibID,
				"agent_id": a.ID, "content": content,
				"metadata": map[string]any{"kind": a.Kind},
			})
		}

		// Seed votes
		voteCount = seedClaimVotes(session, data, r1Agents, delibID)
		fmt.Fprintf(os.Stderr, "  %d votes seeded\n", voteCount)

		// Trigger analysis
		fmt.Fprintf(os.Stderr, "  analyzing...\n")
		call(session, "analyze", map[string]any{"action": "run", "deliberation_id": delibID})
	}

	// Get R1 result
	var r1Result string
	if resuming {
		r1Result = callSoft(session, "analyze", map[string]any{
			"action": "get_result", "deliberation_id": delibID, "round": 1,
		})
		if r1Result == "" {
			fmt.Fprintf(os.Stderr, "  no R1 result found — cannot resume\n")
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "  fetched existing R1 result\n")
	} else {
		r1Result = pollAndGetResult(session, cfg.MCPURL, secret, delibID, 1)
		if r1Result == "" {
			fmt.Fprintf(os.Stderr, "  round 1 analysis did not complete\n")
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "  round 1 complete\n")
	}

	// Generate compromise proposal for R1
	r1Compromise := ""
	if cfg.ReportPath != "" {
		fmt.Fprintf(os.Stderr, "  generating compromise proposal...\n")
		compJSON := callSoft(session, "analyze", map[string]any{
			"action": "propose_compromise", "deliberation_id": delibID,
		})
		var comp struct {
			Proposal string `json:"compromise_proposal"`
		}
		json.Unmarshal([]byte(compJSON), &comp)
		r1Compromise = comp.Proposal
	}

	var r2Result string
	var r2Agents []agentPlan
	r2Compromise := ""

	if cfg.Rounds >= 2 {
		// Reconnect — the polling loop may have replaced the session
		session.Close()
		session, err = connect(cfg.MCPURL, secret)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reconnect for R2 failed: %v\n", err)
			os.Exit(1)
		}

		// --- Round 2: bridge + dissent + empty chairs ---
		fmt.Fprintf(os.Stderr, "\n=== Round 2 ===\n")

		// Parse R1 results for informed R2 agents
		var r1Analysis analysisResult
		json.Unmarshal([]byte(r1Result), &r1Analysis)

		// Bridge agent: informed by R1 cruxes
		if len(clusters) >= 2 {
			var pos strings.Builder
			pos.WriteString("BRIDGE — finding common ground\n\n")
			if len(r1Analysis.Cruxes) > 0 {
				pos.WriteString("Cruxes found in Round 1:\n")
				for _, c := range r1Analysis.Cruxes[:min(5, len(r1Analysis.Cruxes))] {
					fmt.Fprintf(&pos, "- [%.0f%%] %s\n", c.Score*100, c.Claim[:min(80, len(c.Claim))])
				}
				pos.WriteString("\n")
			}
			if len(r1Analysis.ConsensusStatements) > 0 {
				pos.WriteString("Consensus from Round 1:\n")
				for _, cs := range r1Analysis.ConsensusStatements[:min(3, len(r1Analysis.ConsensusStatements))] {
					fmt.Fprintf(&pos, "- %s\n", cs.Content[:min(80, len(cs.Content))])
				}
				pos.WriteString("\n")
			}
			pos.WriteString("Build outward from consensus. Propose positions that opposing sides can both endorse.")

			r2Agents = append(r2Agents, agentPlan{
				ID: "t3c-bridge", Role: "Bridge: cross-cluster common ground",
				Position: pos.String(), Kind: "bridge", Round: 2,
			})
		}

		// Dissent agent: challenges R1 consensus
		if len(r1Analysis.ConsensusStatements) > 0 {
			var pos strings.Builder
			pos.WriteString("DISSENT — challenging consensus\n\n")
			pos.WriteString("Round 1 found these consensus statements:\n")
			for _, cs := range r1Analysis.ConsensusStatements[:min(3, len(r1Analysis.ConsensusStatements))] {
				fmt.Fprintf(&pos, "- %s\n", cs.Content[:min(100, len(cs.Content))])
			}
			pos.WriteString("\nWhat's the strongest case against each? What perspective is missing?")

			r2Agents = append(r2Agents, agentPlan{
				ID: "t3c-dissent", Role: "Dissent: challenges R1 consensus",
				Position: pos.String(), Kind: "dissent", Round: 2,
			})
		}

		// Empty chair agents: amplify minority side of lopsided cruxes
		emptyChairs := 0
		for _, c := range r1Analysis.Cruxes {
			if emptyChairs >= 2 {
				break
			}
			nAgree := len(c.Agree)
			nDisagree := len(c.Disagree)
			if (nAgree <= 1 && nDisagree >= 3) || (nDisagree <= 1 && nAgree >= 3) {
				minority := "agree"
				if nAgree > nDisagree {
					minority = "disagree"
				}
				var pos strings.Builder
				fmt.Fprintf(&pos, "EMPTY CHAIR — amplifying underrepresented side\n\n")
				fmt.Fprintf(&pos, "Crux: %s\n", c.Claim[:min(120, len(c.Claim))])
				fmt.Fprintf(&pos, "The %s side has only %d agent(s). The other has %d.\n\n",
					minority, min(nAgree, nDisagree), max(nAgree, nDisagree))
				fmt.Fprintf(&pos, "Present the strongest case for the %s side. Don't strawman — steelman the minority.", minority)

				r2Agents = append(r2Agents, agentPlan{
					ID: fmt.Sprintf("t3c-empty-chair-%d", emptyChairs), Role: fmt.Sprintf("Empty chair: amplifying %s side", minority),
					Position: pos.String(), Kind: "empty-chair", Round: 2,
				})
				emptyChairs++
			}
		}

		if len(r2Agents) > 0 {
			if resuming {
				fmt.Fprintf(os.Stderr, "  (resume) skipping submission, fetching existing R2 result...\n")
				r2Result = callSoft(session, "analyze", map[string]any{
					"action": "get_result", "deliberation_id": delibID, "round": 2,
				})
				if r2Result == "" {
					// R2 may not have completed — re-trigger
					fmt.Fprintf(os.Stderr, "  no R2 result, re-triggering analysis...\n")
					call(session, "analyze", map[string]any{"action": "run", "deliberation_id": delibID})
					r2Result = pollAndGetResult(session, cfg.MCPURL, secret, delibID, 2)
				} else {
					fmt.Fprintf(os.Stderr, "  fetched existing R2 result\n")
				}
			} else {
				// Get context for each R2 agent (required for round 2 access)
				for _, a := range r2Agents {
					callSoft(session, "participate", map[string]any{
						"action": "get_context", "deliberation_id": delibID, "agent_id": a.ID,
					})
				}

				for _, a := range r2Agents {
					fmt.Fprintf(os.Stderr, "  submit: %s\n", a.ID)
					content := a.Position
					if !cfg.Named {
						content = anonymizeSpeakers(content, r1Agents)
					}
					call(session, "participate", map[string]any{
						"action": "submit_position", "deliberation_id": delibID,
						"agent_id": a.ID, "content": content,
						"metadata": map[string]any{"kind": a.Kind},
					})
				}

				// Seed votes for R2 agents
				r2Votes := seedR2Votes(session, r1Agents, r2Agents, &r1Analysis, delibID)
				fmt.Fprintf(os.Stderr, "  %d R2 votes seeded\n", r2Votes)

				// Trigger R2 analysis
				fmt.Fprintf(os.Stderr, "  analyzing...\n")
				call(session, "analyze", map[string]any{"action": "run", "deliberation_id": delibID})
				r2Result = pollAndGetResult(session, cfg.MCPURL, secret, delibID, 2)
				if r2Result == "" {
					fmt.Fprintf(os.Stderr, "  R2 analysis timed out, retrying...\n")
					session.Close()
					session, err = connect(cfg.MCPURL, secret)
					if err == nil {
						call(session, "analyze", map[string]any{"action": "run", "deliberation_id": delibID})
						r2Result = pollAndGetResult(session, cfg.MCPURL, secret, delibID, 2)
					}
				}
			}
			if r2Result != "" {
				fmt.Fprintf(os.Stderr, "  round 2 complete\n")
				if cfg.ReportPath != "" {
					// Reconnect for compromise
					session.Close()
					session, err = connect(cfg.MCPURL, secret)
					if err == nil {
						fmt.Fprintf(os.Stderr, "  generating R2 compromise proposal...\n")
						compJSON := callSoft(session, "analyze", map[string]any{
							"action": "propose_compromise", "deliberation_id": delibID,
						})
						var comp struct {
							Proposal string `json:"compromise_proposal"`
						}
						json.Unmarshal([]byte(compJSON), &comp)
						r2Compromise = comp.Proposal
					}
				}
			}
		}
	}

	var r3Result string
	var r3Agents []agentPlan
	r3Compromise := ""

	if cfg.Rounds >= 3 && r2Result != "" {
		// Reconnect for R3
		session.Close()
		session, err = connect(cfg.MCPURL, secret)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reconnect for R3 failed: %v\n", err)
			os.Exit(1)
		}

		fmt.Fprintf(os.Stderr, "\n=== Round 3: Revision + Resolutions ===\n")

		var r2Analysis analysisResult
		json.Unmarshal([]byte(r2Result), &r2Analysis)

		// Generate revised speaker positions + resolution proposals
		r3Agents = buildR3Agents(r1Agents, &r2Analysis, data)
		resolutionAgents := buildResolutionAgents(&r2Analysis, data)
		r3Agents = append(r3Agents, resolutionAgents...)

		if len(r3Agents) > 0 {
			if resuming {
				fmt.Fprintf(os.Stderr, "  (resume) skipping submission, fetching existing R3 result...\n")
				r3Result = callSoft(session, "analyze", map[string]any{
					"action": "get_result", "deliberation_id": delibID, "round": 3,
				})
				if r3Result == "" {
					// R3 didn't complete — this is the main resume use case
					fmt.Fprintf(os.Stderr, "  no R3 result, re-triggering analysis...\n")
					call(session, "analyze", map[string]any{"action": "run", "deliberation_id": delibID})
					r3Result = pollAndGetResult(session, cfg.MCPURL, secret, delibID, 3)
					if r3Result == "" {
						fmt.Fprintf(os.Stderr, "  R3 analysis timed out on retry\n")
					}
				} else {
					fmt.Fprintf(os.Stderr, "  fetched existing R3 result\n")
				}
			} else {
				// Get context for each R3 agent
				for _, a := range r3Agents {
					callSoft(session, "participate", map[string]any{
						"action": "get_context", "deliberation_id": delibID, "agent_id": a.ID,
					})
				}

				for _, a := range r3Agents {
					fmt.Fprintf(os.Stderr, "  submit: %s\n", a.ID)
					content := a.Position
					if !cfg.Named {
						content = anonymizeSpeakers(content, r1Agents)
					}
					call(session, "participate", map[string]any{
						"action": "submit_position", "deliberation_id": delibID,
						"agent_id": a.ID, "content": content,
						"metadata": map[string]any{"kind": a.Kind},
					})
				}

				// Seed R3 votes
				r3Votes := seedR3Votes(session, r1Agents, r2Agents, r3Agents, delibID)
				fmt.Fprintf(os.Stderr, "  %d R3 votes seeded\n", r3Votes)

				// Trigger R3 analysis
				fmt.Fprintf(os.Stderr, "  analyzing...\n")
				call(session, "analyze", map[string]any{"action": "run", "deliberation_id": delibID})
				r3Result = pollAndGetResult(session, cfg.MCPURL, secret, delibID, 3)
				if r3Result == "" {
					fmt.Fprintf(os.Stderr, "  R3 analysis timed out, retrying...\n")
					session.Close()
					session, err = connect(cfg.MCPURL, secret)
					if err == nil {
						call(session, "analyze", map[string]any{"action": "run", "deliberation_id": delibID})
						r3Result = pollAndGetResult(session, cfg.MCPURL, secret, delibID, 3)
					}
				}
			}
			if r3Result != "" {
				fmt.Fprintf(os.Stderr, "  round 3 complete\n")
				if cfg.ReportPath != "" {
					session.Close()
					session, err = connect(cfg.MCPURL, secret)
					if err == nil {
						fmt.Fprintf(os.Stderr, "  generating R3 compromise proposal...\n")
						compJSON := callSoft(session, "analyze", map[string]any{
							"action": "propose_compromise", "deliberation_id": delibID,
						})
						var comp struct {
							Proposal string `json:"compromise_proposal"`
						}
						json.Unmarshal([]byte(compJSON), &comp)
						r3Compromise = comp.Proposal
					}
				}
			}
		}
	}

	// --- Output ---
	joinCode := generateJoinCode(session, delibID)

	type structuralResult struct {
		DeliberationID string `json:"deliberation_id"`
		Template       string `json:"template"`
		Rounds         int    `json:"rounds"`
		R1Agents       int    `json:"r1_agents"`
		R1Votes        int    `json:"r1_votes"`
		JoinCode       string `json:"join_code,omitempty"`
	}

	r := structuralResult{
		DeliberationID: delibID, Template: tmpl,
		Rounds: cfg.Rounds, R1Agents: len(r1Agents),
		R1Votes: voteCount, JoinCode: joinCode,
	}
	out, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(out))

	fmt.Fprintf(os.Stderr, "\nDone. Deliberation %s (%d rounds)\n", delibID, cfg.Rounds)
	if joinCode != "" {
		fmt.Fprintf(os.Stderr, "  Join: %s\n", joinCode)
	}

	// Run null control if requested
	var ncResult *nullControlResult
	if cfg.NullControl && r1Result != "" {
		_, secret := getMCPConfig()
		ncResult = runNullControl(data, r1Result, len(clusters), cfg.MCPURL, secret, tmpl, cfg.GroupID, cfg.Threshold)
	}

	// Run replication if requested
	var repResult *replicationResult
	if cfg.ReplicateN >= 2 && r1Result != "" {
		_, secret := getMCPConfig()
		repResult = runReplication(data, cfg.MCPURL, secret, tmpl, cfg.GroupID, cfg.Threshold, cfg.ReplicateN)
	}

	// Run coverage audit if requested
	var covResult *coverageResult
	if cfg.CoverageAudit && r1Result != "" {
		fmt.Fprintf(os.Stderr, "\n=== Coverage Audit ===\n")
		var r1Analysis analysisResult
		json.Unmarshal([]byte(r1Result), &r1Analysis)
		covResult = runCoverageAudit(&r1Analysis, data.Title)
	}

	// Spot-check: verifies T3C matrix stances against source quotes.
	// In structural mode, the matrix isn't used for agent construction (agents are
	// built from claims+quotes directly), so this checks input data quality only.
	var scResult *spotCheckResult
	if cfg.SpotCheck {
		fmt.Fprintf(os.Stderr, "\n=== Spot Check (T3C input quality) ===\n")
		scResult = runSpotCheck(data, 0.30)
	}

	// Crux spot-check: verifies gemot's OUTPUT crux assignments against source quotes.
	// This validates what the pipeline actually produces rather than its inputs.
	var cruxSCResult *spotCheckResult
	if cfg.SpotCheck {
		fmt.Fprintf(os.Stderr, "\n=== Spot Check (crux assignment quality) ===\n")
		// Collect cruxes from all rounds
		var allCruxes []cruxForCheck
		for _, rJSON := range []string{r1Result, r2Result, r3Result} {
			if rJSON == "" {
				continue
			}
			var ar analysisResult
			if err := json.Unmarshal([]byte(rJSON), &ar); err != nil {
				continue
			}
			for _, c := range ar.Cruxes {
				cf := cruxForCheck{
					Claim:    c.Claim,
					Agree:    c.Agree,
					Disagree: c.Disagree,
				}
				for _, st := range c.Stances {
					cf.Stances = append(cf.Stances, stanceForCheck{
						AgentID:   st.AgentID,
						Value:     st.Value,
						Qualifier: st.Qualifier,
					})
				}
				allCruxes = append(allCruxes, cf)
			}
		}
		// Build agent ID → speaker name map for spot check (IDs are now anonymous)
		agentNameMap := map[string]string{}
		for _, a := range r1Agents {
			if a.Kind == "speaker" || a.Kind == "steelman" {
				name := strings.TrimPrefix(a.Role, "Speaker: ")
				name = strings.TrimPrefix(name, "Steelman: ")
				// For clusters like "A, B & C", use the first member for quote lookup
				if idx := strings.Index(name, ","); idx > 0 {
					name = strings.TrimSpace(name[:idx])
				}
				agentNameMap[a.ID] = name
			}
		}
		cruxSCResult = runCruxSpotCheck(allCruxes, data, 0.30, agentNameMap)
	}

	// Write markdown report if requested
	if cfg.ReportPath != "" && r1Result != "" {
		ri := &reportInput{
			Data: data, R1JSON: r1Result, R2JSON: r2Result, R3JSON: r3Result,
			R1Compromise: r1Compromise, R2Compromise: r2Compromise, R3Compromise: r3Compromise,
			R1Agents: r1Agents, R2Agents: r2Agents, R3Agents: r3Agents,
			Template: tmpl, DelibID: delibID, JoinCode: joinCode,
			NullControl: ncResult, SpotCheck: scResult, CruxSpotCheck: cruxSCResult,
			Replication: repResult, Coverage: covResult,
			Named: cfg.Named,
		}
		md := generateReport(ri)
		if err := os.WriteFile(cfg.ReportPath, []byte(md), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "writing report: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "  Report: %s\n", cfg.ReportPath)
		}
	}

	// Write anonymized JSON export for gemotvis (all rounds, names replaced)
	if cfg.ReportPath != "" && r1Result != "" && !cfg.Named {
		exportPath := strings.TrimSuffix(cfg.ReportPath, ".md") + ".json"
		nRounds := 1
		if r2Result != "" {
			nRounds = 2
		}
		if r3Result != "" {
			nRounds = 3
		}
		export := map[string]any{
			"deliberation_id": delibID,
			"template":        tmpl,
			"rounds":          nRounds,
		}
		// Collect all round results into a single JSON, then anonymize the text
		rounds := []json.RawMessage{}
		for _, rJSON := range []string{r1Result, r2Result, r3Result} {
			if rJSON != "" {
				rounds = append(rounds, json.RawMessage(rJSON))
			}
		}
		export["analysis_results"] = rounds
		exportBytes, _ := json.MarshalIndent(export, "", "  ")
		// Apply same anonymization as the report
		exportStr := anonymizeSpeakers(string(exportBytes), r1Agents)
		if err := os.WriteFile(exportPath, []byte(exportStr), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "writing export: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "  Export: %s\n", exportPath)
		}
	}

	// Persist validation results on R1 analysis via update_result
	if r1Result != "" && (ncResult != nil || repResult != nil || covResult != nil) {
		fmt.Fprintf(os.Stderr, "\n=== Store Validation ===\n")
		storeSession, err := connect(cfg.MCPURL, secret)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  store-validation: connect failed: %v\n", err)
		} else {
			storeValidation(storeSession, delibID, r1Result, ncResult, repResult, covResult)
			storeSession.Close()
		}
	}
}

// storeValidation persists validation results on the R1 analysis via update_result.
func storeValidation(session *sdkmcp.ClientSession, delibID, r1JSON string, nc *nullControlResult, rep *replicationResult, cov *coverageResult) {
	// Unmarshal raw result into a map so we can add fields without importing internal types
	var result map[string]any
	if err := json.Unmarshal([]byte(r1JSON), &result); err != nil {
		fmt.Fprintf(os.Stderr, "  store-validation: failed to parse R1 result: %v\n", err)
		return
	}

	if nc != nil {
		result["null_control"] = nc
	}
	if rep != nil {
		result["replication"] = rep
	}
	if cov != nil {
		result["coverage_gaps"] = cov.Gaps
	}

	updated, err := json.Marshal(result)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  store-validation: failed to marshal: %v\n", err)
		return
	}

	fmt.Fprintf(os.Stderr, "  store-validation: sending update_result (%d bytes)...\n", len(updated))
	resp := call(session, "analyze", map[string]any{
		"action": "update_result", "deliberation_id": delibID,
		"round": 1, "result_json": string(updated),
	})
	fmt.Fprintf(os.Stderr, "  store-validation: %s\n", resp)
}

// --- Vote seeding for structural mode ---

type positionRef struct {
	ID      string `json:"position_id"`
	AgentID string `json:"agent_id"`
}

func getPositions(session *sdkmcp.ClientSession, delibID string) []positionRef {
	posJSON := call(session, "participate", map[string]any{
		"action": "get_positions", "deliberation_id": delibID,
	})
	// Response is a flat array, not {positions: [...]}
	var positions []positionRef
	json.Unmarshal([]byte(posJSON), &positions)
	return positions
}

// seedClaimVotes seeds votes based on claim overlap, not matrix stances.
// Encodes engagement and known agreement. Disagreement discovery is the analysis engine's job.
func seedClaimVotes(session *sdkmcp.ClientSession, data *ReportData, agents []agentPlan, delibID string) int {
	positions := getPositions(session, delibID)

	agentByID := map[string]*agentPlan{}
	for i := range agents {
		agentByID[agents[i].ID] = &agents[i]
	}

	voteCount := 0
	for vi := range agents {
		voter := &agents[vi]
		for _, pos := range positions {
			if pos.AgentID == voter.ID {
				continue
			}
			target := agentByID[pos.AgentID]
			if target == nil {
				continue
			}

			qv := deriveClaimVote(data, voter, target)
			if qv.Skip {
				continue
			}

			call(session, "participate", map[string]any{
				"action": "vote", "deliberation_id": delibID,
				"agent_id": voter.ID, "position_id": pos.ID, "value": qv.Value,
				"qualifier": qv.Qualifier, "caveat": qv.Caveat,
			})
			voteCount++
		}
	}
	return voteCount
}

// perturbVote returns a deterministic {-1, 0, 1} based on agent ID pair.
// Uses FNV-inspired mixing to ensure different pairs produce different results.
func perturbVote(voterID, targetID string) int {
	// Mix both IDs with position-dependent weights for maximum entropy
	h := uint32(2166136261) // FNV offset basis
	for i, c := range voterID {
		h ^= uint32(c) * uint32(i+1)
		h *= 16777619 // FNV prime
	}
	for i, c := range targetID {
		h ^= uint32(c) * uint32(i+1)
		h *= 16777619
	}
	switch h % 3 {
	case 0:
		return 1
	case 1:
		return -1
	default:
		return 0
	}
}

// qualifiedVote carries stance, qualifier, and caveat for the 5-point scale.
type qualifiedVote struct {
	Value     int
	Qualifier string
	Caveat    string
	Skip      bool // true = no basis for comparison, don't submit
}

func skipVote() qualifiedVote { return qualifiedVote{Skip: true} }

func deriveClaimVote(data *ReportData, voter, target *agentPlan) qualifiedVote {
	// Speaker/steelman -> speaker/steelman: claim overlap
	if (voter.Kind == "speaker" || voter.Kind == "steelman") && (target.Kind == "speaker" || target.Kind == "steelman") {
		if voter.Cluster == nil || target.Cluster == nil {
			return skipVote()
		}
		// Count shared claims between the two clusters' members
		shared := 0
		voterSubtopics := map[string]bool{}
		targetSubtopics := map[string]bool{}
		for _, vm := range voter.Cluster.Members {
			for _, tm := range target.Cluster.Members {
				shared += sharedClaimCount(data, vm, tm)
			}
			for k := range speakerSubtopicSet(data, vm) {
				voterSubtopics[k] = true
			}
		}
		for _, tm := range target.Cluster.Members {
			for k := range speakerSubtopicSet(data, tm) {
				targetSubtopics[k] = true
			}
		}

		// Vary agreement threshold by voter to ensure distinct vote patterns
		// between speakers with similar engagement (prevents sybil detection)
		voterHash := 0
		for _, c := range voter.ID {
			voterHash += int(c)
		}
		agreementThresh := 2 + (voterHash % 2) // 2 or 3 depending on voter

		if shared >= agreementThresh {
			return qualifiedVote{Value: 2, Qualifier: fmt.Sprintf("high claim overlap (%d shared)", shared)}
		}
		if shared > 0 {
			return qualifiedVote{Value: 1, Qualifier: "partial claim overlap"}
		}
		// Check subtopic overlap (engaging with same issues)
		overlap := 0
		for k := range voterSubtopics {
			if targetSubtopics[k] {
				overlap++
			}
		}
		if overlap > 0 {
			return qualifiedVote{Value: perturbVote(voter.ID, target.ID), Qualifier: "same topics but different claims"}
		}
		return skipVote()
	}

	// Speaker/steelman -> probe: does speaker have claims in probe's topic?
	if (voter.Kind == "speaker" || voter.Kind == "steelman") && target.Kind == "probe" {
		if voter.Cluster == nil {
			return qualifiedVote{Value: 0, Qualifier: "no cluster data"}
		}
		totalClaims := 0
		for _, m := range voter.Cluster.Members {
			totalClaims += len(findClaimsForSpeaker(data, m, target.Topic, ""))
		}
		if totalClaims >= 5 {
			return qualifiedVote{Value: 2, Qualifier: fmt.Sprintf("deep engagement with topic (%d claims)", totalClaims)}
		}
		if totalClaims >= 2 {
			return qualifiedVote{Value: 1, Qualifier: "moderate engagement"}
		}
		if totalClaims == 0 {
			return skipVote()
		}
		return qualifiedVote{Value: 0, Qualifier: "minimal engagement"}
	}

	// Probe -> speaker/steelman: does the speaker engage with this probe's topic?
	if voter.Kind == "probe" && (target.Kind == "speaker" || target.Kind == "steelman") {
		if target.Cluster == nil {
			return qualifiedVote{Value: 0, Qualifier: "no cluster data"}
		}
		totalClaims := 0
		for _, m := range target.Cluster.Members {
			totalClaims += len(findClaimsForSpeaker(data, m, voter.Topic, ""))
		}
		if totalClaims >= 5 {
			return qualifiedVote{Value: 2, Qualifier: fmt.Sprintf("deep engagement (%d claims)", totalClaims)}
		}
		if totalClaims >= 2 {
			return qualifiedVote{Value: 1, Qualifier: "moderate engagement"}
		}
		if totalClaims == 0 {
			return qualifiedVote{Value: -2, Qualifier: "no engagement with topic"}
		}
		return qualifiedVote{Value: 0, Qualifier: "minimal engagement"}
	}

	// Probe -> probe: diversify by topic relationship
	if voter.Kind == "probe" && target.Kind == "probe" {
		if voter.Topic == target.Topic {
			return qualifiedVote{Value: 2, Qualifier: "same topic"}
		}
		if voter.Topic < target.Topic {
			return qualifiedVote{Value: 1, Qualifier: "related topic"}
		}
		return qualifiedVote{Value: -1, Qualifier: "different topic"}
	}

	return qualifiedVote{Value: 0}
}

func seedR2Votes(session *sdkmcp.ClientSession, r1Agents, r2Agents []agentPlan, r1Analysis *analysisResult, delibID string) int {
	positions := getPositions(session, delibID)

	// Build lookup: which R1 agents are on which side of R1 cruxes
	agreeAgents := map[string]bool{}     // agents that appear in "agree" on any R1 crux
	disagreeAgents := map[string]bool{}  // agents that appear in "disagree" on any R1 crux
	consensusAgents := map[string]bool{} // agents that appear in consensus
	for _, c := range r1Analysis.Cruxes {
		for _, a := range c.Agree {
			agreeAgents[a] = true
		}
		for _, a := range c.Disagree {
			disagreeAgents[a] = true
		}
	}

	// Per-empty-chair: track which crux side they amplify
	type emptyChairInfo struct {
		minoritySide string // "agree" or "disagree"
		cruxIdx      int
	}
	emptyChairCruxes := map[string]emptyChairInfo{}
	ecIdx := 0
	for i, c := range r1Analysis.Cruxes {
		if ecIdx >= 2 {
			break
		}
		nAgree := len(c.Agree)
		nDisagree := len(c.Disagree)
		if (nAgree <= 1 && nDisagree >= 3) || (nDisagree <= 1 && nAgree >= 3) {
			minority := "agree"
			if nAgree > nDisagree {
				minority = "disagree"
			}
			emptyChairCruxes[fmt.Sprintf("t3c-empty-chair-%d", ecIdx)] = emptyChairInfo{
				minoritySide: minority, cruxIdx: i,
			}
			ecIdx++
		}
	}

	voteCount := 0

	// R1 agents vote on R2 agents' positions
	r2IDs := map[string]bool{}
	for _, a := range r2Agents {
		r2IDs[a.ID] = true
	}
	for _, voter := range r1Agents {
		for _, pos := range positions {
			if pos.AgentID == voter.ID || !r2IDs[pos.AgentID] {
				continue
			}
			// R1 agents vary votes on R2 positions:
			// Consensus agents → -2 on dissent (they'd strongly disagree with the challenge)
			// Non-consensus → perturbVote for voter-specific diversity (avoids sybil from uniform 0s)
			vote := perturbVote(voter.ID, pos.AgentID)
			qualifier := ""
			if strings.HasPrefix(pos.AgentID, "t3c-dissent") && consensusAgents[voter.ID] {
				vote = -2
				qualifier = "consensus holder rejects dissent challenge"
			}
			call(session, "participate", map[string]any{
				"action": "vote", "deliberation_id": delibID,
				"agent_id": voter.ID, "position_id": pos.ID, "value": vote,
				"qualifier": qualifier,
			})
			voteCount++
		}
	}

	// R2 agents vote on R1 agents' positions with distinct strategies
	r1IDs := map[string]bool{}
	for _, a := range r1Agents {
		r1IDs[a.ID] = true
	}
	for _, voter := range r2Agents {
		for _, pos := range positions {
			if pos.AgentID == voter.ID || !r1IDs[pos.AgentID] {
				continue
			}

			vote := 0
			qualifier := ""
			switch voter.Kind {
			case "bridge":
				// Bridge agrees with positions that appear on both sides of cruxes (bridging potential)
				if agreeAgents[pos.AgentID] && disagreeAgents[pos.AgentID] {
					vote = 2
					qualifier = "bridging potential — takes varied stances"
				} else {
					vote = 1
					qualifier = "bridge is generally agreeable"
				}
			case "dissent":
				// Dissent disagrees with positions that only appear in consensus, agrees with controversial ones
				if disagreeAgents[pos.AgentID] {
					vote = 1
					qualifier = "controversial stance — dissent approves"
				} else if agreeAgents[pos.AgentID] && !disagreeAgents[pos.AgentID] {
					vote = -2
					qualifier = "unchallenged consensus — dissent challenges"
				}
			case "empty-chair":
				// Empty chair votes based on which side of its specific crux the R1 agent is on
				ec, ok := emptyChairCruxes[voter.ID]
				if ok && ec.cruxIdx < len(r1Analysis.Cruxes) {
					crux := r1Analysis.Cruxes[ec.cruxIdx]
					// Agree with agents on the minority side, disagree with majority
					for _, a := range crux.Agree {
						if a == pos.AgentID {
							if ec.minoritySide == "agree" {
								vote = 2
								qualifier = "amplifies minority perspective"
							} else {
								vote = -1
								qualifier = "opposes majority side"
							}
							break
						}
					}
					for _, a := range crux.Disagree {
						if a == pos.AgentID {
							if ec.minoritySide == "disagree" {
								vote = 2
								qualifier = "amplifies minority perspective"
							} else {
								vote = -1
								qualifier = "opposes majority side"
							}
							break
						}
					}
				}
			}

			call(session, "participate", map[string]any{
				"action": "vote", "deliberation_id": delibID,
				"agent_id": voter.ID, "position_id": pos.ID, "value": vote,
				"qualifier": qualifier,
			})
			voteCount++
		}
	}

	// R2 agents vote on each other's positions (distinct patterns)
	for _, voter := range r2Agents {
		for _, pos := range positions {
			if pos.AgentID == voter.ID || !r2IDs[pos.AgentID] {
				continue
			}
			vote := 0
			// Bridge agrees with everyone, dissent disagrees with bridge
			if voter.Kind == "bridge" {
				vote = 1
			} else if voter.Kind == "dissent" && strings.HasPrefix(pos.AgentID, "t3c-bridge") {
				vote = -1 // dissent challenges bridge's consensus-seeking
			} else if voter.Kind == "empty-chair" && strings.HasPrefix(pos.AgentID, "t3c-bridge") {
				vote = 0 // empty chairs are neutral on bridge
			} else if voter.Kind == "empty-chair" && strings.HasPrefix(pos.AgentID, "t3c-dissent") {
				vote = 1 // empty chairs appreciate dissent
			}
			// Empty chairs vote differently on each other based on their crux indices
			if voter.Kind == "empty-chair" && strings.HasPrefix(pos.AgentID, "t3c-empty-chair") && voter.ID != pos.AgentID {
				vote = 0 // neutral on other empty chairs (different cruxes)
			}

			call(session, "participate", map[string]any{
				"action": "vote", "deliberation_id": delibID,
				"agent_id": voter.ID, "position_id": pos.ID, "value": vote,
			})
			voteCount++
		}
	}

	return voteCount
}

// --- Analysis polling ---

type analysisResult struct {
	Cruxes []struct {
		Claim       string   `json:"crux_claim"`
		Agree       []string `json:"agree_agents"`
		Disagree    []string `json:"disagree_agents"`
		Score       float64  `json:"controversy_score"`
		Explanation string   `json:"explanation"`
		Stances     []struct {
			AgentID   string `json:"agent_id"`
			Value     int    `json:"value"`
			Qualifier string `json:"qualifier"`
		} `json:"stances"`
	} `json:"cruxes"`
	DiscardedCruxes []struct {
		Claim      string   `json:"crux_claim"`
		Agree      []string `json:"agree_agents"`
		Disagree   []string `json:"disagree_agents"`
		Degenerate bool     `json:"degenerate"`
	} `json:"discarded_cruxes"`
	ConsensusStatements []struct{ Content string } `json:"consensus_statements"`
	BridgingStatements  []struct {
		Content string  `json:"content"`
		Score   float64 `json:"bridging_score"`
	} `json:"bridging_statements"`
	TopicSummaries []struct {
		TopicID string `json:"topic_id"`
		Topic   string `json:"topic"`
		Summary string `json:"summary"`
	} `json:"topic_summaries"`
	Confidence        string   `json:"confidence"`
	IntegrityWarnings []string `json:"integrity_warnings"`
}

func pollAndGetResult(session *sdkmcp.ClientSession, mcpURL, secret, delibID string, round int) string {
	maxPolls := 180 // 15 min
	if round >= 3 {
		maxPolls = 240 // 20 min for later rounds with more agents
	}
	reconnects := 0
	s := session

	for i := range maxPolls {
		time.Sleep(5 * time.Second)
		statusJSON := callSoft(s, "deliberation", map[string]any{
			"action": "get", "deliberation_id": delibID,
		})
		if statusJSON == "" {
			// Reconnect
			s.Close()
			reconnects++
			if reconnects > 10 {
				return ""
			}
			fmt.Fprintf(os.Stderr, "  reconnecting (%d/10)...\n", reconnects)
			time.Sleep(3 * time.Second)
			var err error
			s, err = connect(mcpURL, secret)
			if err != nil {
				return ""
			}
			continue
		}
		var status struct {
			Status    string `json:"status"`
			SubStatus string `json:"sub_status"`
		}
		json.Unmarshal([]byte(statusJSON), &status)
		if status.Status == "open" {
			// Analysis done, fetch result
			resultJSON := callSoft(s, "analyze", map[string]any{
				"action": "get_result", "deliberation_id": delibID, "round": round,
			})
			return resultJSON
		}
		if i%12 == 0 {
			fmt.Fprintf(os.Stderr, "  %s/%s (%ds)\n", status.Status, status.SubStatus, (i+1)*5)
		}
	}
	return ""
}

// --- Shared helpers ---

func filterCruxes(all []SubtopicCrux, threshold float64, includeAll bool) []SubtopicCrux {
	var cruxes []SubtopicCrux
	for _, c := range all {
		if includeAll || c.ControversyScore >= threshold {
			cruxes = append(cruxes, c)
		}
	}
	sort.Slice(cruxes, func(i, j int) bool {
		return cruxes[i].ControversyScore > cruxes[j].ControversyScore
	})
	if len(cruxes) == 0 && len(all) > 0 {
		fmt.Fprintf(os.Stderr, "no cruxes above threshold %.2f (max controversy: %.2f)\n",
			threshold, all[0].ControversyScore)
		fmt.Fprintf(os.Stderr, "try --threshold 0 or --all-cruxes\n")
		os.Exit(1)
	}
	return cruxes
}

func countClaims(data *ReportData) int {
	total := 0
	for _, t := range data.Topics {
		for _, st := range t.Subtopics {
			total += len(st.Claims)
		}
	}
	return total
}

func countMultiMember(clusters []cluster) int {
	n := 0
	for _, c := range clusters {
		if len(c.Members) > 1 {
			n++
		}
	}
	return n
}

func submitSpeakerPositions(session *sdkmcp.ClientSession, data *ReportData, crux *SubtopicCrux, delibID string) int {
	posCount := 0
	allSpeakers := append(crux.Agree, crux.Disagree...)
	for _, speakerRef := range allSpeakers {
		speakerName := parseSpeakerID(speakerRef)
		claims := findClaimsForSpeaker(data, speakerName, crux.Topic, crux.Subtopic)
		if len(claims) == 0 {
			claims = findClaimsForSpeaker(data, speakerName, crux.Topic, "")
		}

		var position strings.Builder
		stance := "agrees"
		for _, d := range crux.Disagree {
			if parseSpeakerID(d) == speakerName {
				stance = "disagrees"
				break
			}
		}
		fmt.Fprintf(&position, "Re: \"%s\"\n\nThis speaker %s.\n", crux.CruxClaim, stance)
		if len(claims) > 0 {
			position.WriteString("\nClaims:\n")
			for _, cl := range claims {
				fmt.Fprintf(&position, "- %s\n", cl)
			}
		}

		call(session, "participate", map[string]any{
			"action": "submit_position", "deliberation_id": delibID,
			"agent_id": fmt.Sprintf("t3c-%s", slugify(speakerName)), "content": position.String(),
		})
		posCount++
	}
	return posCount
}

func seedSpeakerVotes(session *sdkmcp.ClientSession, data *ReportData, crux *SubtopicCrux, delibID string) int {
	if data.AddOns.SpeakerCruxMatrix == nil {
		return 0
	}
	cLabel := cruxLabel(crux.Topic, crux.Subtopic)
	cruxIdx := -1
	for j, label := range data.AddOns.SpeakerCruxMatrix.CruxLabels {
		if normLabel(label) == cLabel {
			cruxIdx = j
			break
		}
	}
	if cruxIdx < 0 {
		return 0
	}

	positions := getPositions(session, delibID)

	voteCount := 0
	for speakerIdx, speakerRef := range data.AddOns.SpeakerCruxMatrix.Speakers {
		voterName := slugify(parseSpeakerID(speakerRef))
		voterStance := "no_position"
		if speakerIdx < len(data.AddOns.SpeakerCruxMatrix.Matrix) &&
			cruxIdx < len(data.AddOns.SpeakerCruxMatrix.Matrix[speakerIdx]) {
			voterStance = data.AddOns.SpeakerCruxMatrix.Matrix[speakerIdx][cruxIdx]
		}

		for _, pos := range positions {
			if pos.AgentID == fmt.Sprintf("t3c-%s", voterName) {
				continue
			}
			posOwnerStance := "agree"
			for _, d := range crux.Disagree {
				if fmt.Sprintf("t3c-%s", slugify(parseSpeakerID(d))) == pos.AgentID {
					posOwnerStance = "disagree"
					break
				}
			}

			var vote int
			switch {
			case voterStance == "no_position":
				vote = 0
			case voterStance == posOwnerStance:
				vote = 1
			default:
				vote = -1
			}

			call(session, "participate", map[string]any{
				"action": "vote", "deliberation_id": delibID,
				"agent_id": fmt.Sprintf("t3c-%s", voterName), "position_id": pos.ID, "value": vote,
			})
			voteCount++
		}
	}
	return voteCount
}

func generateJoinCode(session *sdkmcp.ClientSession, delibID string) string {
	joinJSON := callSoft(session, "coordinate", map[string]any{
		"action": "generate_join_code", "deliberation_id": delibID, "agent_id": "t3c-import",
	})
	var join struct {
		JoinCode string `json:"join_code"`
	}
	json.Unmarshal([]byte(joinJSON), &join)
	return join.JoinCode
}
