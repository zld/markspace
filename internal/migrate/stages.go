// Package migrate runs the release as a sequence of replayable stages.
//
// The five stages are 解析 (parse), 音频处理 (audio), 清单暂存 (manifest
// staging), 校验 (verify) and 提交 (commit).  Each stage records a durable
// status, a resume position (byte offset or sample index) and the fault it
// stopped on.  A re-run replays completed stages cheaply, reusing committed
// artifacts, and resumes a failed stage from its last complete boundary.
package migrate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"example.com/assetpack/internal/fault"
)

// Position is the last complete boundary a resumed run starts from: an input
// byte offset and a media sample index.
type Position struct {
	Offset int64 `json:"offset"`
	Sample int64 `json:"sample"`
}

// Stage is the identity of one migration stage.
type Stage string

const (
	StageParse         Stage = "parse"
	StageAudio         Stage = "audio"
	StageStageManifest Stage = "manifest-stage"
	StageVerify        Stage = "verify"
	StageCommit        Stage = "commit"
)

// Order is the canonical execution order of the five stages.
var Order = []Stage{StageParse, StageAudio, StageStageManifest, StageVerify, StageCommit}

// Status is the durable state of one stage.
type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped"
)

// Record is the persisted state of one stage.
type Record struct {
	Stage     Stage        `json:"stage"`
	Status    Status       `json:"status"`
	Offset    int64        `json:"offset"`
	Sample    int64        `json:"sample"`
	Attempts  int          `json:"attempts"`
	Detail    string       `json:"detail,omitempty"`
	Fault     *fault.Fault `json:"fault,omitempty"`
	UpdatedAt string       `json:"updated_at"`
}

// Journal is the full stage history.
type Journal struct {
	Release string   `json:"release"`
	Records []Record `json:"records"`
}

// NewJournal builds a pending journal for the five stages.
func NewJournal(release string) *Journal {
	j := &Journal{Release: release}
	for _, s := range Order {
		j.Records = append(j.Records, Record{Stage: s, Status: StatusPending, Offset: -1, Sample: -1})
	}
	return j
}

func (j *Journal) index(s Stage) int {
	for i := range j.Records {
		if j.Records[i].Stage == s {
			return i
		}
	}
	return -1
}

// Get returns the record for a stage.
func (j *Journal) Get(s Stage) (Record, bool) {
	i := j.index(s)
	if i < 0 {
		return Record{}, false
	}
	return j.Records[i], true
}

// Set updates a record and stamps the time.
func (j *Journal) Set(s Stage, status Status, offset, sample int64, detail string, f *fault.Fault) {
	i := j.index(s)
	if i < 0 {
		return
	}
	j.Records[i].Status = status
	j.Records[i].Offset = offset
	j.Records[i].Sample = sample
	j.Records[i].Detail = detail
	j.Records[i].Fault = f
	j.Records[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if status == StatusRunning || status == StatusFailed {
		j.Records[i].Attempts++
	}
}

// CompletedCount is the number of stages that finished successfully.
func (j *Journal) CompletedCount() int {
	n := 0
	for _, r := range j.Records {
		if r.Status == StatusDone || r.Status == StatusSkipped {
			n++
		}
	}
	return n
}

// AllDone reports that every stage completed.
func (j *Journal) AllDone() bool { return j.CompletedCount() == len(Order) }

// LastFailure returns the first failed record in execution order.
func (j *Journal) LastFailure() (Record, bool) {
	for _, s := range Order {
		r, ok := j.Get(s)
		if ok && r.Status == StatusFailed {
			return r, true
		}
	}
	return Record{}, false
}

// ResumeFrom returns the earliest stage that is not complete, which is the
// stage a resumed run must start from.
func (j *Journal) ResumeFrom() Stage {
	for _, s := range Order {
		r, _ := j.Get(s)
		if r.Status != StatusDone && r.Status != StatusSkipped {
			return s
		}
	}
	return StageCommit
}

// Persist writes the journal atomically.
func (j *Journal) Persist(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fault.New(fault.StageState, fault.KindIO, "create state dir: %v", err)
	}
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return fault.New(fault.StageState, fault.KindIO, "encode journal: %v", err)
	}
	tmp, err := os.CreateTemp(dir, "stages.json.tmp*")
	if err != nil {
		return fault.New(fault.StageState, fault.KindIO, "create temp journal: %v", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return fault.New(fault.StageState, fault.KindIO, "write journal: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fault.New(fault.StageState, fault.KindIO, "sync journal: %v", err)
	}
	if err := tmp.Close(); err != nil {
		return fault.New(fault.StageState, fault.KindIO, "close journal: %v", err)
	}
	if err := os.Rename(name, filepath.Join(dir, "stages.json")); err != nil {
		return fault.New(fault.StageState, fault.KindIO, "rename journal: %v", err)
	}
	return nil
}

// LoadJournal reads a persisted journal, or returns a fresh one.
func LoadJournal(dir, release string) (*Journal, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "stages.json"))
	if os.IsNotExist(err) {
		return NewJournal(release), nil
	}
	if err != nil {
		return nil, fault.New(fault.StageState, fault.KindIO, "read journal: %v", err)
	}
	var j Journal
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, fault.New(fault.StageState, fault.KindMissing, "journal is corrupt: %v", err)
	}
	// Guarantee the five canonical stages exist even if the file is older.
	for _, s := range Order {
		if _, ok := j.Get(s); !ok {
			j.Records = append(j.Records, Record{Stage: s, Status: StatusPending, Offset: -1, Sample: -1})
		}
	}
	sort.SliceStable(j.Records, func(a, b int) bool {
		return stageRank(j.Records[a].Stage) < stageRank(j.Records[b].Stage)
	})
	return &j, nil
}

func stageRank(s Stage) int {
	for i, v := range Order {
		if v == s {
			return i
		}
	}
	return len(Order)
}

// FailAfter returns the stage at which a run should fail, for the fault
// injection demo.  An empty value means "never fail".
func FailAfter(name string) (Stage, error) {
	if name == "" {
		return "", nil
	}
	for _, s := range Order {
		if string(s) == name {
			return s, nil
		}
	}
	return "", fault.New(fault.StageCLI, fault.KindInvalidArg,
		"unknown stage %q (known: %s)", name, StageList())
}

// StageList renders the valid stage names for error messages.
func StageList() string {
	out := ""
	for i, s := range Order {
		if i > 0 {
			out += ", "
		}
		out += string(s)
	}
	return out
}

// Summary renders one line per stage, in execution order, for the terminal.
func (j *Journal) Summary() []string {
	out := make([]string, 0, len(Order))
	for _, s := range Order {
		r, _ := j.Get(s)
		pos := ""
		if r.Offset >= 0 {
			pos = fmt.Sprintf(" offset=%d", r.Offset)
		}
		if r.Sample >= 0 {
			pos += fmt.Sprintf(" sample=%d", r.Sample)
		}
		line := fmt.Sprintf("%-14s %-7s%s", r.Stage, r.Status, pos)
		if r.Detail != "" {
			line += " " + r.Detail
		}
		out = append(out, line)
	}
	return out
}
