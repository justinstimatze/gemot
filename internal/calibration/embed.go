package calibration

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

//go:embed corpus/v2.json
var corpusBytes []byte

//go:embed embed/latest.json
var embeddedRunBytes []byte

var (
	loadOnce     sync.Once
	loadedCorpus *Corpus
	loadedRun    *EmbeddedRun
	loadErr      error
)

// LoadCorpus returns the embedded corpus, or the corpus at the path named by
// the GEMOT_CALIBRATION_CORPUS env var if set (development override). The
// returned *Corpus is cached on first call.
//
// In CI/release runs that haven't authored the corpus yet, the embedded
// JSON is the placeholder {"version":"v1","questions":[]} — callers must
// be tolerant of empty corpora rather than panic on first ship.
func LoadCorpus() (*Corpus, error) {
	loadOnce.Do(loadEmbedded)
	if loadErr != nil {
		return nil, loadErr
	}
	return loadedCorpus, nil
}

// LoadEmbeddedRun returns the embedded last-CI-run result, which populates
// the calibration field on analyze action:get_result. Returns a non-nil
// *EmbeddedRun even when the embedded data is the placeholder — callers
// check len(rc.ReferenceClasses) before claiming a rate.
func LoadEmbeddedRun() (*EmbeddedRun, error) {
	loadOnce.Do(loadEmbedded)
	if loadErr != nil {
		return nil, loadErr
	}
	return loadedRun, nil
}

func loadEmbedded() {
	loadedCorpus, loadErr = loadCorpusFromEmbedOrEnv()
	if loadErr != nil {
		return
	}
	loadedRun, loadErr = loadRunFromEmbed()
}

func loadCorpusFromEmbedOrEnv() (*Corpus, error) {
	raw := corpusBytes
	if override := os.Getenv("GEMOT_CALIBRATION_CORPUS"); override != "" {
		b, err := os.ReadFile(override)
		if err != nil {
			return nil, fmt.Errorf("reading GEMOT_CALIBRATION_CORPUS=%q: %w", override, err)
		}
		raw = b
	}
	var c Corpus
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parsing calibration corpus: %w", err)
	}
	return &c, nil
}

func loadRunFromEmbed() (*EmbeddedRun, error) {
	var r EmbeddedRun
	if err := json.Unmarshal(embeddedRunBytes, &r); err != nil {
		return nil, fmt.Errorf("parsing embedded calibration run: %w", err)
	}
	if r.ReferenceClasses == nil {
		r.ReferenceClasses = map[string]ReferenceClass{}
	}
	return &r, nil
}
