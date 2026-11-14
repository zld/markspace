// Package queue implements the job state machine, the persistent state log
// and the concurrent worker pool.
package queue

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// State is the lifecycle state of a job.
type State string

const (
	Pending  State = "pending"
	Running  State = "running"
	Success  State = "success"
	Failed   State = "failed"
	Canceled State = "canceled"
)

// transitions encodes the legal state machine edges. Failed -> Running is the
// retry edge; terminal states (Success, Canceled) have no outgoing edges.
var transitions = map[State]map[State]bool{
	Pending: {Running: true, Canceled: true},
	Running: {Success: true, Failed: true, Canceled: true},
	Failed:  {Running: true},
}

// CanTransition reports whether moving from s to `to` is legal.
func (s State) CanTransition(to State) bool {
	return transitions[s][to]
}

// Job is one unit of work tracked by the queue.
type Job struct {
	ID       string `json:"id"`
	Input    string `json:"input"`
	State    State  `json:"state"`
	Attempts int    `json:"attempts"`
	Err      string `json:"error,omitempty"`
}

// Transition moves the job to `to`, validating the edge.
func (j *Job) Transition(to State) error {
	if !j.State.CanTransition(to) {
		return fmt.Errorf("queue: illegal state transition %s -> %s for job %s", j.State, to, j.ID)
	}
	j.State = to
	return nil
}

// LogEntry is one line of the JSONL state log. Type is "header" (run
// metadata, carries the config digest) or "transition".
type LogEntry struct {
	Type    string `json:"type"`
	TS      string `json:"ts"`
	Digest  string `json:"digest,omitempty"`
	JobID   string `json:"job_id,omitempty"`
	From    State  `json:"from,omitempty"`
	To      State  `json:"to,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
	Error   string `json:"error,omitempty"`
}

// StateLog appends state transitions to a JSONL file so an interrupted run
// can be resumed from disk.
type StateLog struct {
	mu  sync.Mutex
	enc *json.Encoder
	f   *os.File
}

// NewStateLog opens (creating if needed) the log in append-only mode.
func NewStateLog(path string) (*StateLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("queue: open state log: %w", err)
	}
	return &StateLog{enc: json.NewEncoder(f), f: f}, nil
}

// Close flushes and closes the underlying file.
func (l *StateLog) Close() error { return l.f.Close() }

// WriteHeader records the run header with the config digest.
func (l *StateLog) WriteHeader(digest string) error {
	return l.write(LogEntry{Type: "header", TS: now(), Digest: digest})
}

// LogTransition records one validated state change.
func (l *StateLog) LogTransition(jobID string, from, to State, attempt int, err error) {
	e := LogEntry{Type: "transition", TS: now(), JobID: jobID, From: from, To: to, Attempt: attempt}
	if err != nil {
		e.Error = err.Error()
	}
	_ = l.write(e)
}

func (l *StateLog) write(e LogEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.enc.Encode(e)
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// ReadLog replays a state log, returning the run digest and each job's final
// observed state. Used by --resume.
func ReadLog(path string) (digest string, final map[string]State, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil, fmt.Errorf("queue: read state log: %w", err)
	}
	defer f.Close()
	final = map[string]State{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var e LogEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue // tolerate a torn final line from a crash
		}
		switch e.Type {
		case "header":
			digest = e.Digest
		case "transition":
			final[e.JobID] = e.To
		}
	}
	return digest, final, sc.Err()
}
