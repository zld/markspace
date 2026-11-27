// Package journal records the per-frame state machine and atomic checkpoints.
// The legal lifecycle is discovered -> planned -> processing -> processed ->
// validated; any failure moves a frame to failed, and a failed frame can never
// be promoted directly back to validated. Recovery resumes after the last
// validated frame and redoes every unvalidated temporary output.
package journal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"continuity/internal/model"
)

// State is one lifecycle stage of a frame.
type State string

const (
	StateDiscovered State = "discovered"
	StatePlanned    State = "planned"
	StateProcessing State = "processing"
	StateProcessed  State = "processed"
	StateValidated  State = "validated"
	StateFailed     State = "failed"
)

// rank defines the only legal forward ordering; failed is terminal.
var rank = map[State]int{
	StateDiscovered: 1,
	StatePlanned:    2,
	StateProcessing: 3,
	StateProcessed:  4,
	StateValidated:  5,
}

const (
	// DirName is the journal directory inside the workspace.
	DirName = "journal"
	// FileName is the single checkpoint file, replaced atomically on commit.
	FileName = "state.json"
)

// Entry is the recorded state of one frame.
type Entry struct {
	Number model.FrameNumber `json:"number"`
	Track  uint32            `json:"track"`
	State  State             `json:"state"`
	Reason string            `json:"reason,omitempty"`
}

// Checkpoint is the full persisted journal document.
type Checkpoint struct {
	Entries map[model.FrameNumber]*Entry `json:"entries"`
}

// Journal is the in-memory state machine bound to one workspace.
type Journal struct {
	mu   sync.Mutex
	root string
	cp   Checkpoint
}

// Open loads the journal, starting empty when no checkpoint exists yet.
func Open(root string) (*Journal, error) {
	j := &Journal{root: root, cp: Checkpoint{Entries: map[model.FrameNumber]*Entry{}}}
	path, err := model.SafeJoin(root, filepath.Join(DirName, FileName))
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return j, nil
	}
	if err != nil {
		return nil, model.WrapError(model.StageJournal, 0, 0, "reading checkpoint", err)
	}
	if err = json.Unmarshal(raw, &j.cp); err != nil {
		return nil, model.WrapError(model.StageJournal, 0, 0, "checkpoint is corrupt", err)
	}
	if j.cp.Entries == nil {
		j.cp.Entries = map[model.FrameNumber]*Entry{}
	}
	return j, nil
}

// Transition validates and applies a state change, then atomically persists
// the checkpoint. Failed is terminal: once failed, only failed may follow.
// The mutex makes concurrent worker transitions safe and serializes the
// atomic checkpoint writes.
func (j *Journal) Transition(num model.FrameNumber, track uint32, to State, reason string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	cur := StateDiscovered
	if e, ok := j.cp.Entries[num]; ok {
		cur = e.State
	}
	if cur == StateFailed && to != StateFailed {
		return model.NewError(model.StageJournal, num, 0,
			fmt.Sprintf("illegal transition %s -> %s: failed is terminal", cur, to))
	}
	if to != StateFailed && rank[to] <= rank[cur] && to != cur {
		return model.NewError(model.StageJournal, num, 0,
			fmt.Sprintf("illegal backward transition %s -> %s", cur, to))
	}
	if _, known := rank[to]; to != StateFailed && !known {
		return model.NewError(model.StageJournal, num, 0, "unknown state: "+string(to))
	}
	j.cp.Entries[num] = &Entry{Number: num, Track: track, State: to, Reason: reason}
	return j.persistLocked()
}

// persistLocked writes the checkpoint; callers must hold j.mu.
func (j *Journal) persistLocked() error {
	dir, err := model.SafeJoin(j.root, DirName)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path, err := model.SafeJoin(j.root, filepath.Join(DirName, FileName))
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(j.cp, "", "  ")
	if err != nil {
		return err
	}
	return model.AtomicWrite(path, raw, 0o644)
}

// StateOf returns the current state of a frame, defaulting to discovered.
func (j *Journal) StateOf(num model.FrameNumber) State {
	j.mu.Lock()
	defer j.mu.Unlock()
	if e, ok := j.cp.Entries[num]; ok {
		return e.State
	}
	return StateDiscovered
}

// CountByState returns how many frames currently occupy each state.
func (j *Journal) CountByState() map[State]int {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := map[State]int{}
	for _, e := range j.cp.Entries {
		out[e.State]++
	}
	return out
}

// CumulativeCounts reports how many frames have reached each lifecycle stage
// at least once. Because the state machine only moves forward, a fully
// completed run shows the total frame count at every stage even after a
// resume.
func (j *Journal) CumulativeCounts() map[State]int {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := map[State]int{
		StateDiscovered: 0, StatePlanned: 0, StateProcessing: 0,
		StateProcessed: 0, StateValidated: 0, StateFailed: 0,
	}
	for _, e := range j.cp.Entries {
		if e.State == StateFailed {
			out[StateFailed]++
			continue
		}
		for s, r := range rank {
			if rank[e.State] >= r {
				out[s]++
			}
		}
	}
	return out
}

// HasFailed reports whether any frame is in the terminal failed state.
func (j *Journal) HasFailed() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, e := range j.cp.Entries {
		if e.State == StateFailed {
			return true
		}
	}
	return false
}

// ResumePoint returns the 1-based frame number at which processing should
// continue: one past the last validated frame. Unvalidated frames (including
// any temporary processed output) are redone.
func (j *Journal) ResumePoint(total int) model.FrameNumber {
	j.mu.Lock()
	defer j.mu.Unlock()
	last := model.FrameNumber(0)
	nums := make([]model.FrameNumber, 0, len(j.cp.Entries))
	for num, e := range j.cp.Entries {
		if e.State == StateValidated {
			nums = append(nums, num)
		}
	}
	sort.Slice(nums, func(i, k int) bool { return nums[i] < nums[k] })
	// Only a contiguous validated prefix counts; a hole forces redo from there.
	for _, num := range nums {
		if num == last+1 {
			last = num
			continue
		}
		break
	}
	if int(last) >= total {
		return model.FrameNumber(total)
	}
	return last + 1
}

// ResetInProgress rolls every frame left in processing or processed (i.e.
// work that was never validated) back to planned. Their temporary outputs are
// therefore redone on resume; failed frames are untouched because that state
// is terminal.
func (j *Journal) ResetInProgress() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, e := range j.cp.Entries {
		if e.State == StateProcessing || e.State == StateProcessed {
			e.State = StatePlanned
			e.Reason = "reset: unvalidated work redone on resume"
		}
	}
	return j.persistLocked()
}

// AssertReusable fails if a previous run left a terminal failed frame, since
// such a workspace cannot be resumed without regenerating input.
func (j *Journal) AssertReusable() error {
	if j.HasFailed() {
		return model.NewError(model.StageJournal, 0, 0,
			"previous run contains a failed frame; regenerate the workspace to retry")
	}
	return nil
}
