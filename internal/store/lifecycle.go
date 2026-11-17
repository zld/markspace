package store

import "fmt"

// Enqueue registers a new candidate job; idempotent so the same candidate can
// never be claimed twice after a resume.
func (s *Store) Enqueue(jobID string, segment, qp int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.cp.Jobs[jobID]; exists {
		return nil
	}
	return s.appendLocked(Event{Kind: EvJobCreated, JobID: jobID, Segment: segment, QP: qp})
}

// Claim marks a queued/retryable job running. Returns false if not claimable.
func (s *Store) Claim(jobID string) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.cp.Jobs[jobID]
	if !ok || (j.State != StateQueued && j.State != StateRetryable) {
		return Job{}, false, nil
	}
	if err := s.appendLocked(Event{Kind: EvClaim, JobID: jobID}); err != nil {
		return Job{}, false, err
	}
	return *s.cp.Jobs[jobID], true, nil
}

// Retry records a transient failure; exceeding MaxAttempt turns it terminal.
func (s *Store) Retry(jobID string, cause error) (terminal bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.cp.Jobs[jobID]
	if !ok {
		return false, fmt.Errorf("store: unknown job %s", jobID)
	}
	if j.Attempts >= j.MaxAttempt {
		return true, s.appendLocked(Event{Kind: EvFailed, JobID: jobID, Error: cause.Error()})
	}
	return false, s.appendLocked(Event{Kind: EvRetry, JobID: jobID, Error: cause.Error()})
}

// Complete stores measured metrics for a finished candidate.
func (s *Store) Complete(jobID string, m CandidateMetrics) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendLocked(Event{Kind: EvCompleted, JobID: jobID, Metrics: &m})
}

// Fail permanently marks a job with a diagnosable cause.
func (s *Store) Fail(jobID string, cause error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendLocked(Event{Kind: EvFailed, JobID: jobID, Error: cause.Error()})
}

// MarkInterrupted records an externally triggered interruption (fail-after).
func (s *Store) MarkInterrupted() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendLocked(Event{Kind: EvRunInterrupt})
}

// MarkSegmentDone records the selected QP for a fully searched segment.
func (s *Store) MarkSegmentDone(segment, qp int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendLocked(Event{Kind: EvSegmentDone, Segment: segment, QP: qp})
}

// Snapshot returns a copy of all jobs for scheduler decisions.
func (s *Store) Snapshot() map[string]Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Job, len(s.cp.Jobs))
	for id, j := range s.cp.Jobs {
		out[id] = *j
	}
	return out
}

// Params returns the stored run parameters.
func (s *Store) Params() Parameters {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cp.Params
}

// Recovered returns how many unfinished candidates were requeued on open.
func (s *Store) Recovered() int { return s.recovered }

// DoneSegment reports the selected QP for a finished segment, if any.
func (s *Store) DoneSegment(seg int) (qp int, done bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	qp, done = s.cp.DoneSegs[seg]
	return qp, done
}
