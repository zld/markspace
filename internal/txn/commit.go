package txn

import (
	"crypto/sha256"
	"errors"
	"fmt"
)

// Stage is one unit of pipeline work. The function receives the running
// state bag and returns the bytes whose SHA-256 is journaled before
// commit. Stage order defines the commit boundary ordering.
type Stage struct {
	Name string
	// Run executes the stage. Returning an error aborts and rolls back.
	Run func(state map[string][]byte) ([]byte, error)
	// Validate optionally asserts properties of the stage output before
	// the commit is durable. A failed validation never commits.
	Validate func(output []byte) error
}

// CrashHook is consulted after each journaled step. Returning an error
// simulates an injected interruption: the process stops without a commit.
type CrashHook func(stage string, step string) error

// ErrInterrupted is wrapped around a hook-triggered stop so callers can
// distinguish injected interruptions from genuine stage failures.
var ErrInterrupted = errors.New("txn: interrupted by injected fault")

// Coordinator executes an ordered list of stages against one journal and
// guarantees atomic boundary advancement.
type Coordinator struct {
	journal *Journal
	hook    CrashHook
}

// NewCoordinator binds a journal and optional crash hook.
func NewCoordinator(j *Journal, hook CrashHook) *Coordinator {
	return &Coordinator{journal: j, hook: hook}
}

// Run executes every stage that has not already committed. Already
// committed stages are skipped (their durable boundary is preserved),
// which makes restart-and-resume idempotent. The returned state bag
// contains each committed stage's last output bytes.
func (c *Coordinator) Run(stages []Stage, state map[string][]byte) error {
	for _, st := range stages {
		if c.journal.Committed(st.Name) {
			// Restart after a crash or a repeated run: the durable boundary
			// is preserved, but the deterministic stage still runs so the
			// in-memory state matches a fresh execution byte-for-byte. No
			// journal writes or commits happen for an existing boundary.
			output, err := st.Run(state)
			if err != nil {
				return fmt.Errorf("txn: replay stage %q failed: %w", st.Name, err)
			}
			state[st.Name] = output
			continue
		}
		if err := c.journal.Begin(st.Name); err != nil {
			return err
		}
		if err := c.fire(st.Name, "begin"); err != nil {
			return err
		}

		output, err := st.Run(state)
		if err != nil {
			_ = c.journal.Rollback(st.Name)
			return fmt.Errorf("txn: stage %q failed: %w", st.Name, err)
		}
		if st.Validate != nil {
			if err := st.Validate(output); err != nil {
				_ = c.journal.Rollback(st.Name)
				return fmt.Errorf("txn: stage %q validation: %w", st.Name, err)
			}
		}

		sum := sha256.Sum256(output)
		if err := c.journal.WriteChecksum(st.Name, sum[:]); err != nil {
			return err
		}
		if err := c.fire(st.Name, "checksum"); err != nil {
			return err
		}
		// pre-commit fires while the stage is journaled but its boundary is
		// not yet durable: this is the realistic interruption window.
		if err := c.fire(st.Name, "pre-commit"); err != nil {
			return err
		}
		if err := c.journal.Commit(st.Name); err != nil {
			return err
		}
		if err := c.fire(st.Name, "commit"); err != nil {
			return err
		}
		state[st.Name] = output
	}
	return nil
}

// CommittedCount returns how many stage boundaries are durable.
func (c *Coordinator) CommittedCount() int {
	c.journal.mu.Lock()
	defer c.journal.mu.Unlock()
	return len(c.journal.committed)
}

// StageNames returns the durable committed set.
func (c *Coordinator) StageNames() []string {
	c.journal.mu.Lock()
	defer c.journal.mu.Unlock()
	out := make([]string, 0, len(c.journal.committed))
	for name := range c.journal.committed {
		out = append(out, name)
	}
	return out
}

func (c *Coordinator) fire(stage, step string) error {
	if c.hook == nil {
		return nil
	}
	if err := c.hook(stage, step); err != nil {
		return fmt.Errorf("%w at %s/%s", ErrInterrupted, stage, step)
	}
	return nil
}

// Checksum is exported for report hashing of final outputs.
func Checksum(b []byte) [32]byte { return sha256.Sum256(b) }
