package calibration

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
)

// WilsonInterval returns the 95% Wilson score confidence interval for a
// sample proportion. n=0 returns [0, 1] (no information). The interval
// is preferred over the normal approximation at small n because it
// doesn't degenerate at p=0 or p=1.
func WilsonInterval(successes, n int) [2]float64 {
	if n <= 0 {
		return [2]float64{0, 1}
	}
	const z = 1.96 // 95%
	p := float64(successes) / float64(n)
	nf := float64(n)
	denom := 1 + z*z/nf
	center := (p + z*z/(2*nf)) / denom
	half := z * math.Sqrt((p*(1-p)+z*z/(4*nf))/nf) / denom
	low := center - half
	high := center + half
	if low < 0 {
		low = 0
	}
	if high > 1 {
		high = 1
	}
	return [2]float64{low, high}
}

// MakeReferenceClass is the canonical bucket → ReferenceClass conversion.
// Exported for tests; the runner calls Aggregate which uses it.
func MakeReferenceClass(deliberationType string, fleetOK, voteOnlyOK, soloOK, n int) ReferenceClass {
	rc := ReferenceClass{DeliberationType: deliberationType, N: n}
	if n > 0 {
		rc.Rate = float64(fleetOK) / float64(n)
		rc.VoteOnlyRate = float64(voteOnlyOK) / float64(n)
		rc.SoloBaselineRate = float64(soloOK) / float64(n)
	}
	rc.CI95 = WilsonInterval(fleetOK, n)
	return rc
}

// Aggregate computes per-deliberation-type ReferenceClass entries from a
// finished Run, plus an "_all" bucket and a held-out summary. Held-out
// questions are excluded from the public map — the public-reported rate
// is over the public-corpus subset only. The held-out rate is returned
// separately for the operator to inspect (it should track the public
// rate; large divergence signals overfit).
func Aggregate(run *Run, corpus *Corpus) (public map[string]ReferenceClass, heldOut ReferenceClass) {
	qByID := make(map[string]Question, len(corpus.Questions))
	for _, q := range corpus.Questions {
		qByID[q.ID] = q
	}

	type counts struct{ fleet, vote, solo, n int }
	publicCounts := map[string]*counts{}
	heldCounts := &counts{}

	for _, r := range run.Results {
		q, ok := qByID[r.QuestionID]
		if !ok {
			continue
		}
		c := heldCounts
		if !q.HeldOut {
			if publicCounts[q.DeliberationType] == nil {
				publicCounts[q.DeliberationType] = &counts{}
			}
			c = publicCounts[q.DeliberationType]
		}
		c.n++
		if r.FleetCorrect {
			c.fleet++
		}
		if r.VoteOnlyCorrect {
			c.vote++
		}
		if r.SoloCorrect {
			c.solo++
		}
	}

	public = map[string]ReferenceClass{}
	all := &counts{}
	for dt, c := range publicCounts {
		public[dt] = MakeReferenceClass(dt, c.fleet, c.vote, c.solo, c.n)
		all.fleet += c.fleet
		all.vote += c.vote
		all.solo += c.solo
		all.n += c.n
	}
	if all.n > 0 {
		public["_all"] = MakeReferenceClass("_all", all.fleet, all.vote, all.solo, all.n)
	}
	heldOut = MakeReferenceClass("_held_out", heldCounts.fleet, heldCounts.vote, heldCounts.solo, heldCounts.n)
	return public, heldOut
}

// BuildEmbeddedRun produces the snapshot the CI job commits to
// internal/calibration/embed/latest.json. Held-out is omitted from the
// embedded JSON — only public reference classes ship in the binary.
func BuildEmbeddedRun(run *Run, corpus *Corpus) *EmbeddedRun {
	public, _ := Aggregate(run, corpus)
	measuredAt := run.FinishedAt
	if measuredAt.IsZero() {
		measuredAt = time.Now()
	}
	return &EmbeddedRun{
		CorpusVersion:    run.CorpusVersion,
		GemotVersion:     run.GemotVersion,
		ModelVersion:     run.ModelVersion,
		MeasuredAt:       measuredAt,
		ReferenceClasses: public,
	}
}

// WriteEmbeddedRun serializes an EmbeddedRun as the canonical JSON the
// embed loader expects.
func WriteEmbeddedRun(w io.Writer, er *EmbeddedRun) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(er)
}

// FormatReport renders a human-readable table for `gemot calibration
// report` CLI output. Per-deliberation-type rows are sorted; "_all" is
// always last. Held-out is shown as a separate row prefixed with a
// dashed divider.
func FormatReport(run *Run, corpus *Corpus) string {
	public, heldOut := Aggregate(run, corpus)

	types := make([]string, 0, len(public))
	for k := range public {
		if k != "_all" {
			types = append(types, k)
		}
	}
	sort.Strings(types)
	types = append(types, "_all")

	var b strings.Builder
	fmt.Fprintf(&b, "Calibration run %s (corpus %s, gemot %s, model %s)\n",
		run.ID, run.CorpusVersion, run.GemotVersion, run.ModelVersion)
	fmt.Fprintf(&b, "Started %s, finished %s\n\n",
		run.StartedAt.Format(time.RFC3339), run.FinishedAt.Format(time.RFC3339))

	fmt.Fprintf(&b, "%-16s %5s %8s %8s %8s %14s %8s\n",
		"deliberation_type", "n", "fleet", "vote_only", "solo", "ci_95(fleet)", "lift")
	fmt.Fprintln(&b, strings.Repeat("-", 78))
	for _, dt := range types {
		c := public[dt]
		fmt.Fprintf(&b, "%-16s %5d %8.1f%% %8.1f%% %8.1f%% [%.2f, %.2f]   %+5.1fpp\n",
			dt, c.N,
			c.Rate*100, c.VoteOnlyRate*100, c.SoloBaselineRate*100,
			c.CI95[0], c.CI95[1],
			(c.Rate-c.SoloBaselineRate)*100,
		)
	}
	fmt.Fprintln(&b, strings.Repeat("-", 78))
	if heldOut.N > 0 {
		fmt.Fprintf(&b, "%-16s %5d %8.1f%% %8.1f%% %8.1f%% (held-out, not embedded)\n",
			"_held_out", heldOut.N,
			heldOut.Rate*100, heldOut.VoteOnlyRate*100, heldOut.SoloBaselineRate*100,
		)
	}
	return b.String()
}
