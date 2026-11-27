// Package fingerprint deduplicates media content across representations. A
// fingerprint is the content digest plus the exact time range and byte size:
// identical bytes at a different time or with a different length are not
// duplicates, which keeps cross-bitrate merging from hiding real anomalies.
package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"

	"mediabatch/internal/clock"
	"mediabatch/internal/fault"
)

// Record is one scanned segment's content identity.
type Record struct {
	ContentSHA     string     `json:"contentSha"`
	Size           int64      `json:"size"`
	Start          clock.Time `json:"start"`
	End            clock.Time `json:"end"`
	Manifest       string     `json:"manifest"`
	Representation string     `json:"representation"`
	Segment        string     `json:"segment"`
}

// Key is the deduplication key. Content equality is necessary but not
// sufficient; the time range and size are part of the identity.
func (r Record) Key() string {
	return fmt.Sprintf("%s|%d|%s|%s", r.ContentSHA, r.Size, r.Start, r.End)
}

// Group is a set of records considered the same content.
type Group struct {
	Key        string     `json:"key"`
	ContentSHA string     `json:"contentSha"`
	Size       int64      `json:"size"`
	Start      clock.Time `json:"start"`
	End        clock.Time `json:"end"`
	// Canonical is the record that gets published.
	Canonical Record `json:"canonical"`
	// Aliases are the merged cross-representation records.
	Aliases []Record `json:"aliases"`
}

// DupCount is the number of records merged away (aliases).
func (g Group) DupCount() int { return len(g.Aliases) }

// Index is the dedup result over a batch.
type Index struct {
	Groups []Group    `json:"groups"`
	Issues fault.List `json:"issues"`
	// Rejected lists records that looked like content matches but disagreed on
	// time or size; they are never merged.
	Rejected []Record `json:"rejected"`
}

// Duplicates returns the total number of merged aliases.
func (ix Index) Duplicates() int {
	n := 0
	for _, g := range ix.Groups {
		n += g.DupCount()
	}
	return n
}

// Build groups records deterministically. A record with the same content hash
// but a different time range or size is kept separate and reported with
// E_FP_RANGE_DRIFT, so a real timing anomaly is never mistaken for a duplicate.
func Build(records []Record) Index {
	var ix Index
	byContent := map[string][]Record{}
	for _, r := range records {
		if r.ContentSHA == "" {
			continue
		}
		byContent[r.ContentSHA] = append(byContent[r.ContentSHA], r)
	}
	hashes := make([]string, 0, len(byContent))
	for h := range byContent {
		hashes = append(hashes, h)
	}
	sort.Strings(hashes)
	for _, h := range hashes {
		set := byContent[h]
		sort.SliceStable(set, func(i, j int) bool {
			if c := set[i].Start.Cmp(set[j].Start); c != 0 {
				return c < 0
			}
			if set[i].Representation != set[j].Representation {
				return set[i].Representation < set[j].Representation
			}
			return set[i].Segment < set[j].Segment
		})
		groups := map[string]*Group{}
		var order []string
		for _, r := range set {
			k := r.Key()
			g, ok := groups[k]
			if !ok {
				groups[k] = &Group{
					Key: k, ContentSHA: r.ContentSHA, Size: r.Size,
					Start: r.Start, End: r.End, Canonical: r,
				}
				order = append(order, k)
				continue
			}
			g.Aliases = append(g.Aliases, r)
		}
		// Detect same-content records that differ in range or size.
		if len(order) > 1 {
			for _, k := range order {
				g := groups[k]
				for _, other := range order {
					if other == k {
						continue
					}
					o := groups[other]
					ix.Rejected = append(ix.Rejected, g.Canonical)
					ix.Issues = append(ix.Issues, driftIssue(g.Canonical, o.Canonical))
					break
				}
			}
		}
		for _, k := range order {
			ix.Groups = append(ix.Groups, *groups[k])
		}
	}
	sort.SliceStable(ix.Groups, func(i, j int) bool {
		a, b := ix.Groups[i], ix.Groups[j]
		if c := a.Start.Cmp(b.Start); c != 0 {
			return c < 0
		}
		if a.Representation() != b.Representation() {
			return a.Representation() < b.Representation()
		}
		return a.Key < b.Key
	})
	ix.Issues.Sort()
	return ix
}

// Representation returns the canonical record's representation.
func (g Group) Representation() string { return g.Canonical.Representation }

func driftIssue(got, want Record) fault.Issue {
	return fault.Issue{
		Code: fault.CodeFingerprintDrift, Severity: fault.SeverityError, Action: fault.ActionIsolated,
		Message: fmt.Sprintf("identical content at %s..%s (%d bytes) also appears at %s..%s (%d bytes); not merged",
			got.Start, got.End, got.Size, want.Start, want.End, want.Size),
		Source: fault.Source{
			Manifest: got.Manifest, Representation: got.Representation, Segment: got.Segment,
			Loc:   fault.Locator{File: got.Segment},
			Start: got.Start, End: got.End,
		},
	}
}

// HashFile returns the SHA-256 of a file without loading it fully into memory.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// FromResult converts a scan result into a fingerprint record.
func FromResult(manifest, rep, segment string, start, end clock.Time, contentSHA string, size int64) Record {
	return Record{
		ContentSHA: contentSHA, Size: size, Start: start, End: end,
		Manifest: manifest, Representation: rep, Segment: segment,
	}
}
