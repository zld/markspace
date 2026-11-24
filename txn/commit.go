package txn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"medkernel/media"
)

// Summary is the durable, JSON-serializable result of one committed stage.
type Summary struct {
	Stage      string             `json:"stage"`
	InputHash  string             `json:"input_hash"`
	OutputHash string             `json:"output_hash"`
	Metrics    map[string]float64 `json:"metrics"`
	OK         bool               `json:"ok"`
	Detail     string             `json:"detail,omitempty"`
}

// StageFunc produces an output content hash and metrics for one stage,
// writing any artifacts into workdir.
type StageFunc func(workdir string) (outputHash string, metrics map[string]float64, err error)

// CheckFunc validates stage metrics and returns a ClassValidation error on
// failure so the committer can roll the stage back.
type CheckFunc func(metrics map[string]float64) error

// Stage is one pipeline step with its input fingerprint and callbacks.
type Stage struct {
	Name      string
	InputHash string
	Run       StageFunc
	Check     CheckFunc
}

// Committer runs stages behind the WAL. A stage only becomes visible after
// its COMMIT record; failed checks roll the stage back and keep the journal.
type Committer struct {
	dir       string
	journal   *Journal
	committed map[string]Summary
	order     []string
	Rollbacks int
}

// NewCommitter opens (or reopens after a crash) a run directory and replays
// the journal to reconstruct the last committed boundary.
func NewCommitter(dir string) (*Committer, error) {
	j, err := OpenJournal(dir)
	if err != nil {
		return nil, err
	}
	recs, err := Replay(dir)
	if err != nil {
		return nil, err
	}
	c := &Committer{dir: dir, journal: j, committed: map[string]Summary{}}
	open := map[string]bool{}
	for _, r := range recs {
		switch r.Type {
		case RecBegin, RecCheck:
			open[r.Stage] = true
		case RecRollback:
			c.Rollbacks++
			delete(open, r.Stage)
		case RecCommit:
			var s Summary
			if err := json.Unmarshal(r.Data, &s); err != nil {
				return nil, media.Wrap(media.ClassIO, "txn.commit", err, "bad commit record")
			}
			if _, known := c.committed[r.Stage]; !known {
				c.order = append(c.order, r.Stage)
			}
			c.committed[r.Stage] = s
			delete(open, r.Stage)
		}
	}
	for range open { // stages interrupted before COMMIT rerun from scratch
		c.Rollbacks++
	}
	return c, nil
}

// Close releases the journal.
func (c *Committer) Close() error { return c.journal.Close() }

// RollbackCount reports how many stages were rolled back (including stages
// found dangling during crash replay).
func (c *Committer) RollbackCount() int { return c.Rollbacks }

// Execute runs one stage. An identical committed InputHash reuses the stored
// summary and skips rerun, which makes recovery output match a full rerun.
func (c *Committer) Execute(st Stage) (Summary, bool, error) {
	if prev, ok := c.committed[st.Name]; ok && prev.InputHash == st.InputHash {
		return prev, true, nil // reused from the committed boundary
	}
	workdir := filepath.Join(c.dir, st.Name)
	// A crashed run may have left partial artifacts; reruns always start
	// from an empty workdir so recovered output is byte-identical to a
	// fresh execution.
	if err := os.RemoveAll(workdir); err != nil {
		return Summary{}, false, media.Wrap(media.ClassIO, "txn.commit", err, "reset workdir")
	}
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return Summary{}, false, media.Wrap(media.ClassIO, "txn.commit", err, "stage workdir")
	}
	if err := c.journal.Append(RecBegin, st.Name, map[string]string{"input_hash": st.InputHash}); err != nil {
		return Summary{}, false, err
	}
	outHash, metrics, err := st.Run(workdir)
	if err != nil {
		c.failStage(st.Name, workdir, err.Error())
		return Summary{}, false, err
	}
	if err := c.journal.Append(RecCheck, st.Name, metrics); err != nil {
		return Summary{}, false, err
	}
	if st.Check != nil {
		if err := st.Check(metrics); err != nil {
			c.failStage(st.Name, workdir, err.Error())
			return Summary{}, false, err
		}
	}
	s := Summary{
		Stage: st.Name, InputHash: st.InputHash, OutputHash: outHash,
		Metrics: metrics, OK: true,
	}
	if err := c.journal.Append(RecCommit, st.Name, s); err != nil {
		return Summary{}, false, err
	}
	if _, known := c.committed[st.Name]; !known {
		c.order = append(c.order, st.Name)
	}
	c.committed[st.Name] = s
	return s, false, nil
}

func (c *Committer) failStage(name, workdir, detail string) {
	c.Rollbacks++
	_ = c.journal.Append(RecRollback, name, map[string]string{"detail": detail})
	_ = os.RemoveAll(workdir) // discard uncommitted artifacts, keep the journal
}

// Committed returns the summaries in commit order.
func (c *Committer) Committed() []Summary {
	out := make([]Summary, 0, len(c.order))
	for _, name := range c.order {
		out = append(out, c.committed[name])
	}
	return out
}

// CommitHash hashes the committed summary chain. The hash is deterministic
// for a given sequence of stage inputs and outputs, so a recovered run and a
// full rerun over the same input produce the identical value.
func (c *Committer) CommitHash() string {
	h := sha256.New()
	names := append([]string(nil), c.order...)
	sort.Strings(names)
	for _, name := range names {
		s := c.committed[name]
		h.Write([]byte(s.Stage))
		h.Write([]byte(s.InputHash))
		h.Write([]byte(s.OutputHash))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
