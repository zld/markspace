// Package provenance defines the processing identity (input digests, parser
// version, config digest) and the independent verification of committed
// index results.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mediacheck/internal/index"
	"mediacheck/internal/timeline"
)

// ParserVersion is the manifest/index parser version, part of the identity.
const ParserVersion = "1.0.0"

// ResultFile is the committed result document name inside the output dir.
const ResultFile = "result.json"

// Identity is the processing identity: input digests, parser version and
// config digest. Identical identities make retries reuse committed results.
type Identity struct {
	Inputs        map[string]string `json:"inputs"`
	ParserVersion string            `json:"parser_version"`
	ConfigDigest  string            `json:"config_digest"`
}

// ConfigDigest hashes configuration strings into the identity.
func ConfigDigest(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		io.WriteString(h, p)
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// ComputeIdentity digests the given root-relative input files.
func ComputeIdentity(root string, inputs []string, configDigest string) (Identity, error) {
	id := Identity{
		Inputs:        map[string]string{},
		ParserVersion: ParserVersion,
		ConfigDigest:  configDigest,
	}
	for _, rel := range inputs {
		d, err := index.DigestFile(filepath.Join(root, rel))
		if err != nil {
			return id, err
		}
		id.Inputs[rel] = d
	}
	return id, nil
}

// Digest returns the canonical digest of the identity.
func (id Identity) Digest() string {
	data, _ := json.Marshal(id) // map keys are sorted by encoding/json
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// DiffInputs explains why a committed identity no longer matches, i.e. the
// invalidation reason for the previous result.
func DiffInputs(old, new Identity) string {
	var diffs []string
	for k, v := range new.Inputs {
		if ov, ok := old.Inputs[k]; !ok {
			diffs = append(diffs, "added:"+k)
		} else if ov != v {
			diffs = append(diffs, "changed:"+k)
		}
	}
	for k := range old.Inputs {
		if _, ok := new.Inputs[k]; !ok {
			diffs = append(diffs, "removed:"+k)
		}
	}
	if old.ParserVersion != new.ParserVersion {
		diffs = append(diffs, "parser:"+old.ParserVersion+"->"+new.ParserVersion)
	}
	if old.ConfigDigest != new.ConfigDigest {
		diffs = append(diffs, "config-changed")
	}
	if len(diffs) == 0 {
		return "none"
	}
	sort.Strings(diffs)
	return strings.Join(diffs, ",")
}

// Result is the committed processing result.
type Result struct {
	Identity Identity     `json:"identity"`
	Events   int          `json:"events"`
	Index    *index.Index `json:"index"`
}

// LoadResult reads the committed result from dir.
func LoadResult(dir string) (*Result, error) {
	data, err := os.ReadFile(filepath.Join(dir, ResultFile))
	if err != nil {
		return nil, err
	}
	var res Result
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// DigestIndex returns the canonical digest of an index.
func DigestIndex(ix *index.Index) string {
	data, _ := json.Marshal(ix)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Verify independently re-checks a committed result: input digests, per-entry
// provenance (segment digest, probe), record completeness and boundary
// overlaps. It reports anomaly intervals, never mutates anything.
func Verify(root string, res *Result) ([]index.Anomaly, error) {
	if res == nil || res.Index == nil {
		return nil, timeline.New(timeline.KindMissingRecord, "provenance.Verify", root, 0, "no committed result")
	}
	ix := res.Index
	var out []index.Anomaly
	seen := map[string]bool{}
	add := func(a index.Anomaly) {
		key := fmt.Sprintf("%s|%s|%d|%d", a.Kind, a.Source, a.StartNs, a.EndNs)
		if !seen[key] {
			seen[key] = true
			out = append(out, a)
		}
	}
	for rel, want := range res.Identity.Inputs {
		got, err := index.DigestFile(filepath.Join(root, rel))
		if err != nil {
			add(index.Anomaly{Kind: timeline.KindMissingRecord, Source: rel,
				StartNs: ix.MediaStartNs, EndNs: ix.MediaEndNs, Detail: err.Error()})
			continue
		}
		if got != want {
			add(index.Anomaly{Kind: timeline.KindDigestMismatch, Source: rel,
				StartNs: ix.MediaStartNs, EndNs: ix.MediaEndNs,
				Detail: "input digest changed after commit"})
		}
	}
	for i, e := range ix.Entries {
		if e.ID != i {
			add(index.Anomaly{Kind: timeline.KindMissingRecord, Source: e.SegmentPath,
				StartNs: e.StartNs, EndNs: e.EndNs,
				Detail: fmt.Sprintf("entry id %d at position %d", e.ID, i)})
		}
		full := filepath.Join(root, e.SegmentPath)
		got, err := index.DigestFile(full)
		switch {
		case err != nil:
			add(index.Anomaly{Kind: timeline.KindMissingRecord, Source: e.SegmentPath,
				StartNs: e.StartNs, EndNs: e.EndNs, Detail: err.Error()})
		case got != e.SegmentDigest:
			add(index.Anomaly{Kind: timeline.KindDigestMismatch, Source: e.SegmentPath,
				StartNs: e.StartNs, EndNs: e.EndNs, Detail: "segment digest mismatch"})
		}
		if e.Anomalous {
			add(index.Anomaly{Kind: timeline.KindTruncated, Source: e.SegmentPath,
				StartNs: e.StartNs, EndNs: e.EndNs, Detail: "entry flagged anomalous at build"})
		} else if _, _, err := index.ProbeSegment(full); err != nil {
			add(index.Anomaly{Kind: timeline.KindTruncated, Source: e.SegmentPath,
				StartNs: e.StartNs, EndNs: e.EndNs, Detail: err.Error()})
		}
	}
	byRep := map[string][]index.Entry{}
	for _, e := range ix.Entries {
		byRep[e.Representation] = append(byRep[e.Representation], e)
	}
	for rep, es := range byRep {
		sort.Slice(es, func(i, j int) bool { return es[i].StartNs < es[j].StartNs })
		for i := 1; i < len(es); i++ {
			if es[i].StartNs < es[i-1].EndNs {
				add(index.Anomaly{Kind: timeline.KindOverlap, Source: rep,
					StartNs: es[i].StartNs, EndNs: es[i-1].EndNs,
					Detail: "boundary overlap within representation"})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartNs != out[j].StartNs {
			return out[i].StartNs < out[j].StartNs
		}
		return out[i].Source < out[j].Source
	})
	return out, nil
}

// AffectedEntries counts index entries covered by anomaly intervals.
func AffectedEntries(res *Result, anomalies []index.Anomaly) int {
	src := map[string]bool{}
	for _, a := range anomalies {
		src[a.Source] = true
	}
	n := 0
	for _, e := range res.Index.Entries {
		if e.Anomalous || src[e.SegmentPath] {
			n++
		}
	}
	return n
}
