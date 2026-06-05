package calibration

import (
	"fmt"

	"github.com/justinstimatze/gemot/types"
)

// Lookup returns the CalibrationField that analyze action:get_result
// should attach for a deliberation of the given type. Returns nil when
// no matching reference class exists in the embedded run — the mechanism
// never claims accuracy it can't back, so callers must let nil flow
// through to JSON serialization (omitempty drops the field entirely).
//
// No fallback across deliberation types: a corpus with only
// reasoning-type questions populates the calibration field only on
// reasoning deliberations. Claiming a reasoning-corpus rate applies to
// a negotiation deliberation would be over-extrapolation; the design
// requires same-type reference classes.
//
// The minN parameter sets a floor below which the per-type rate is
// suppressed (we don't want to publish a 100% rate from n=2). Callers
// typically pass 8. Pass 1 to disable the floor.
func Lookup(deliberationType string, minN int) *types.CalibrationField {
	er, err := LoadEmbeddedRun()
	if err != nil || er == nil {
		return nil
	}
	if len(er.ReferenceClasses) == 0 {
		return nil
	}
	rc, ok := er.ReferenceClasses[deliberationType]
	if !ok || rc.N < minN {
		return nil
	}
	basis := fmt.Sprintf("%s-type deliberations in corpus %s", deliberationType, er.CorpusVersion)
	return &types.CalibrationField{
		Rate:             rc.Rate,
		VoteOnlyRate:     rc.VoteOnlyRate,
		SoloBaselineRate: rc.SoloBaselineRate,
		CompromiseLift:   rc.Rate - rc.VoteOnlyRate,
		N:                rc.N,
		Basis:            basis,
		CI95:             rc.CI95,
		CorpusVersion:    er.CorpusVersion,
		MeasuredAt:       er.MeasuredAt,
	}
}
