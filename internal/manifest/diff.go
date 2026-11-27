package manifest

import (
	"sort"

	perr "gamepack/internal/errors"
)

// Diff reports how a freshly built manifest relates to a previously
// committed one. Reuse is allowed only when the input digest matches; an
// input change forces every entry to be reported as changed/new.
type Diff struct {
	NewCount     int `json:"new_count"`
	ReusedCount  int `json:"reused_count"`
	ChangedCount int `json:"changed_count"`
	InputChanged bool `json:"input_changed"`
	NewIDs       []string `json:"new_ids"`
	ChangedIDs   []string `json:"changed_ids"`
	ReusedIDs    []string `json:"reused_ids"`
}

// entryIndex maps entry id -> digest for constant-time comparison.
func entryIndex(m *Manifest) map[string]string {
	out := make(map[string]string, len(m.Entries))
	for _, e := range m.Entries {
		out[e.ID] = e.Digest
	}
	return out
}

// Compare computes new/reused/changed counts between a previous manifest
// (nil means a first run) and the candidate. When input digests differ, no
// entry is reused even if individual digests coincide. Both manifests are
// finalized first so freshly mutated entries carry current digests.
func Compare(prev, candidate *Manifest) (*Diff, error) {
	if candidate == nil {
		return nil, perr.New(subsystem, perr.CodeStage, "candidate manifest is nil")
	}
	if _, err := Finalize(candidate); err != nil {
		return nil, err
	}
	if prev != nil {
		if _, err := Finalize(prev); err != nil {
			return nil, err
		}
	}
	d := &Diff{}
	if prev == nil {
		for _, e := range candidate.Entries {
			d.NewCount++
			d.NewIDs = append(d.NewIDs, e.ID)
		}
		sort.Strings(d.NewIDs)
		return d, nil
	}

	d.InputChanged = prev.InputDigest != candidate.InputDigest
	old := entryIndex(prev)
	for _, e := range candidate.Entries {
		oldDigest, existed := old[e.ID]
		switch {
		case !existed:
			d.NewCount++
			d.NewIDs = append(d.NewIDs, e.ID)
		case d.InputChanged || oldDigest != e.Digest:
			// Input change invalidates reuse; otherwise only a digest drift
			// counts as a changed entry.
			d.ChangedCount++
			d.ChangedIDs = append(d.ChangedIDs, e.ID)
		default:
			d.ReusedCount++
			d.ReusedIDs = append(d.ReusedIDs, e.ID)
		}
	}
	sort.Strings(d.NewIDs)
	sort.Strings(d.ChangedIDs)
	sort.Strings(d.ReusedIDs)
	return d, nil
}

// Digest is a convenience wrapper exposing the manifest SHA-256.
func Digest(m *Manifest) (string, error) {
	if _, err := Finalize(m); err != nil {
		return "", err
	}
	return m.Digest, nil
}

// EntryCount returns the number of manifest entries.
func (m *Manifest) EntryCount() int { return len(m.Entries) }
