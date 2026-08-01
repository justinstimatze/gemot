package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Engine is a minimal UCI client. The notnil/chess uci package keeps only the
// last "info" line, which discards every MultiPV variation but one — and MultiPV
// is the whole point here, since it is what gives each agent a menu of candidate
// moves to argue over. So we speak UCI directly.
type Engine struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *bufio.Reader
	closed bool
}

// Eval is a score from the side-to-move's point of view.
type Eval struct {
	CP   int `json:"cp"`             // centipawns; valid when Mate == 0
	Mate int `json:"mate,omitempty"` // mate in N moves, signed; 0 means no forced mate
}

// mateScore is the centipawn value a forced mate is worth. Large enough to
// dominate any material evaluation, small enough to keep arithmetic in range.
const mateScore = 30000

// Centipawns collapses an Eval to a single comparable number so personality
// bonuses (also in centipawns) can be added to it.
func (e Eval) Centipawns() int {
	if e.Mate != 0 {
		if e.Mate > 0 {
			return mateScore - e.Mate*100
		}
		return -mateScore - e.Mate*100
	}
	return e.CP
}

func (e Eval) String() string {
	if e.Mate != 0 {
		return fmt.Sprintf("mate in %d", e.Mate)
	}
	return fmt.Sprintf("%+.2f", float64(e.CP)/100)
}

// Line is one MultiPV variation returned by the engine.
type Line struct {
	Rank  int      `json:"rank"` // 1 = engine's preferred move
	UCI   string   `json:"uci"`
	Eval  Eval     `json:"eval"`
	Depth int      `json:"depth"`
	PV    []string `json:"pv"` // principal variation in UCI notation
}

// engineSearchPath lists directories that ship chess engines but are not always
// on PATH — Debian puts Stockfish in /usr/games, which no shell exports.
var engineSearchPath = []string{"/usr/games", "/usr/local/bin", "/opt/homebrew/bin", "/usr/bin"}

// ResolveEngine finds an engine binary by name, falling back to the usual
// install locations when it is not on PATH. An explicit path is used as given.
func ResolveEngine(name string) (string, error) {
	if strings.ContainsRune(name, '/') {
		return name, nil
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, dir := range engineSearchPath {
		candidate := dir + "/" + name
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("engine %q not found on PATH or in %s", name, strings.Join(engineSearchPath, ", "))
}

// NewEngine starts a UCI engine subprocess and completes the handshake.
func NewEngine(path string, options map[string]string) (*Engine, error) {
	cmd := exec.Command(path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", path, err)
	}
	e := &Engine{cmd: cmd, stdin: stdin, out: bufio.NewReaderSize(stdout, 1<<16)}

	if err := e.send("uci"); err != nil {
		return nil, err
	}
	if err := e.waitFor("uciok"); err != nil {
		return nil, err
	}
	for name, value := range options {
		if err := e.send(fmt.Sprintf("setoption name %s value %s", name, value)); err != nil {
			return nil, err
		}
	}
	if err := e.sync(); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Engine) send(s string) error {
	_, err := io.WriteString(e.stdin, s+"\n")
	return err
}

func (e *Engine) waitFor(token string) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		line, err := e.out.ReadString('\n')
		if err != nil {
			return fmt.Errorf("waiting for %q: %w", token, err)
		}
		if strings.HasPrefix(strings.TrimSpace(line), token) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %q", token)
		}
	}
}

func (e *Engine) sync() error {
	if err := e.send("isready"); err != nil {
		return err
	}
	return e.waitFor("readyok")
}

// NewGame resets the engine's internal state between games so a run is
// reproducible rather than contaminated by the previous game's hash table.
func (e *Engine) NewGame() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.send("ucinewgame"); err != nil {
		return err
	}
	return e.sync()
}

// SearchOpts configures one Analyze call.
type SearchOpts struct {
	Depth       int      // fixed search depth in plies
	MultiPV     int      // number of variations to return (1 = best move only)
	SearchMoves []string // restrict the search to these root moves (UCI notation)
}

// Analyze searches a position and returns the MultiPV variations, ranked best first.
func (e *Engine) Analyze(fen string, opts SearchOpts) ([]Line, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, fmt.Errorf("engine closed")
	}
	if opts.MultiPV < 1 {
		opts.MultiPV = 1
	}
	if opts.Depth < 1 {
		opts.Depth = 12
	}

	if err := e.send(fmt.Sprintf("setoption name MultiPV value %d", opts.MultiPV)); err != nil {
		return nil, err
	}
	if err := e.send("position fen " + fen); err != nil {
		return nil, err
	}
	goCmd := fmt.Sprintf("go depth %d", opts.Depth)
	if len(opts.SearchMoves) > 0 {
		goCmd += " searchmoves " + strings.Join(opts.SearchMoves, " ")
	}
	if err := e.send(goCmd); err != nil {
		return nil, err
	}

	// Keep the deepest info line seen for each MultiPV slot. The engine emits
	// one set per iteration, so later lines supersede earlier ones.
	best := map[int]Line{}
	for {
		raw, err := e.out.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("reading search output: %w", err)
		}
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "bestmove") {
			break
		}
		if !strings.HasPrefix(line, "info ") {
			continue
		}
		parsed, ok := parseInfo(line)
		if !ok {
			continue
		}
		if prev, seen := best[parsed.Rank]; !seen || parsed.Depth >= prev.Depth {
			best[parsed.Rank] = parsed
		}
	}

	lines := make([]Line, 0, len(best))
	for rank := 1; rank <= len(best); rank++ {
		if l, ok := best[rank]; ok {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("engine returned no variations for %s", fen)
	}
	return lines, nil
}

// parseInfo extracts a Line from a UCI "info" line. Lines without both a score
// and a pv (progress reports like "info depth 3 currmove e2e4") are skipped.
func parseInfo(line string) (Line, bool) {
	fields := strings.Fields(line)
	l := Line{Rank: 1}
	var hasScore, hasPV bool
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "depth":
			if v, err := atoiAt(fields, i+1); err == nil {
				l.Depth = v
			}
		case "multipv":
			if v, err := atoiAt(fields, i+1); err == nil {
				l.Rank = v
			}
		case "score":
			if i+2 < len(fields) {
				v, err := strconv.Atoi(fields[i+2])
				if err == nil {
					switch fields[i+1] {
					case "cp":
						l.Eval = Eval{CP: v}
						hasScore = true
					case "mate":
						l.Eval = Eval{Mate: v}
						hasScore = true
					}
				}
			}
		case "pv":
			l.PV = append([]string(nil), fields[i+1:]...)
			if len(l.PV) > 0 {
				l.UCI = l.PV[0]
				hasPV = true
			}
			i = len(fields)
		}
	}
	// Bound-only scores are mid-search estimates; the engine will re-report the
	// same variation with a real score before the iteration ends.
	if strings.Contains(line, "lowerbound") || strings.Contains(line, "upperbound") {
		return Line{}, false
	}
	return l, hasScore && hasPV
}

func atoiAt(fields []string, i int) (int, error) {
	if i >= len(fields) {
		return 0, fmt.Errorf("index out of range")
	}
	return strconv.Atoi(fields[i])
}

// Close shuts the engine down.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	_ = e.send("quit") //nolint:errcheck
	done := make(chan error, 1)
	go func() { done <- e.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		return e.cmd.Process.Kill()
	}
}
