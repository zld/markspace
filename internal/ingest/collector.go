// Package ingest receives fragments delivered out of order by concurrent
// recording nodes and normalizes them into per-bitrate collections.
//
// Semantics:
//
//   - identical bytes for the same (bitrate, sequence) are an idempotent
//     re-delivery;
//   - different bytes for the same sequence are a hard conflict;
//   - fragments arriving after the expected window has been finalized are
//     recorded as out-of-window late arrivals and never mutate the result;
//   - sequence gaps are retained as explicit slots instead of shifting later
//     fragments forward.
package ingest

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"hlsforge/internal/cmaf"
	"hlsforge/internal/fixture"
	"hlsforge/internal/report"
)

// Fragment is one received fragment with its parsed timeline position.
type Fragment struct {
	Bitrate  int
	Seq      int
	BaseTime uint64
	Duration uint64 // timescale units (ms with the fixture timescale)
	Raw      []byte
	SHA256   [32]byte
	Parsed   *cmaf.Fragment
}

// TrackIngest is the normalized, sequence-indexed view of one bitrate.
type TrackIngest struct {
	Bitrate   int
	Timescale uint32
	PartMs    int64
	FromSeq   int
	ToSeq     int
	// Slots maps sequence number to the received fragment; nil means missing.
	Slots map[int]*Fragment
}

// Present returns the received fragments in sequence order.
func (t *TrackIngest) Present() []*Fragment {
	out := make([]*Fragment, 0, len(t.Slots))
	for seq := t.FromSeq; seq <= t.ToSeq; seq++ {
		if f := t.Slots[seq]; f != nil {
			out = append(out, f)
		}
	}
	return out
}

// Missing returns sequence numbers with no fragment inside the window.
func (t *TrackIngest) Missing() []int {
	var out []int
	for seq := t.FromSeq; seq <= t.ToSeq; seq++ {
		if t.Slots[seq] == nil {
			out = append(out, seq)
		}
	}
	return out
}

// Result is everything a collector run produced.
type Result struct {
	Tracks   []*TrackIngest
	Findings []report.Finding
}

// Collect walks one fixture inbox in delivery order. expectedFrom/expectedTo
// bound the accepted window; anything outside is rejected as late.
func Collect(inbox string, expectedFrom, expectedTo int, partMs int64) (*Result, error) {
	dirNames, err := fixture.SortedBitrateDirs(inbox)
	if err != nil {
		return nil, fmt.Errorf("ingest: read inbox: %w", err)
	}
	res := &Result{}
	for _, name := range dirNames {
		var bitrate int
		if _, err := fmt.Sscanf(name, "%d", &bitrate); err != nil {
			continue
		}
		track, findings, err := collectTrack(filepath.Join(inbox, name),
			bitrate, expectedFrom, expectedTo, partMs)
		if err != nil {
			return nil, err
		}
		res.Tracks = append(res.Tracks, track)
		res.Findings = append(res.Findings, findings...)
	}
	if len(res.Tracks) == 0 {
		return nil, fmt.Errorf("ingest: no bitrate directories under %s", inbox)
	}
	return res, nil
}

func collectTrack(dir string, bitrate, from, to int, partMs int64) (*TrackIngest, []report.Finding, error) {
	initRaw, err := os.ReadFile(filepath.Join(dir, fixture.InitName()))
	if err != nil {
		return nil, nil, fmt.Errorf("ingest: read init for %d: %w", bitrate, err)
	}
	init, err := cmaf.ParseInit(initRaw)
	if err != nil {
		return nil, nil, fmt.Errorf("ingest: init %d: %w", bitrate, err)
	}
	track := &TrackIngest{
		Bitrate: bitrate, Timescale: init.Timescale, PartMs: partMs,
		FromSeq: from, ToSeq: to, Slots: map[int]*Fragment{},
	}
	var findings []report.Finding
	order, err := readDeliveryOrder(dir)
	if err != nil {
		return nil, nil, err
	}
	seen := map[int][32]byte{}
	for _, fname := range order {
		if _, ok := fixture.ParseFragmentName(fname); !ok {
			findings = append(findings, report.Finding{
				Code: report.CodeFileError, Bitrate: bitrate, Seq: 0,
				Detail: "unrecognized fragment file name: " + fname})
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, fname))
		if err != nil {
			return nil, nil, fmt.Errorf("ingest: read %s: %w", fname, err)
		}
		parsed, err := cmaf.ParseFragment(raw, init.Timescale)
		if err != nil {
			return nil, nil, fmt.Errorf("ingest: parse %s: %w", fname, err)
		}
		// The sequence number is authoritative from mfhd, not the file name.
		seq := int(parsed.Seq)
		hash := sha256.Sum256(raw)
		if seq < from || seq > to {
			findings = append(findings, report.Finding{
				Code: report.CodeLateOutOfWindow, Bitrate: bitrate, Seq: seq,
				Detail: "fragment arrived outside the finalized window: " + fname})
			continue
		}
		if prev, ok := seen[seq]; ok {
			if prev != hash {
				findings = append(findings, report.Finding{
					Code: report.CodeConflict, Bitrate: bitrate, Seq: seq,
					Detail: "same sequence number delivered with different content"})
			}
			// Identical hash: idempotent re-delivery, nothing to store.
			continue
		}
		seen[seq] = hash
		track.Slots[seq] = &Fragment{
			Bitrate: bitrate, Seq: seq, BaseTime: parsed.BaseTime,
			Duration: parsed.Duration, Raw: raw, SHA256: hash, Parsed: parsed,
		}
	}
	for _, seq := range track.Missing() {
		findings = append(findings, report.Finding{
			Code: report.CodeMissingPart, Bitrate: bitrate, Seq: seq,
			Detail: "no fragment delivered for expected time slot"})
	}
	return track, findings, nil
}

func readDeliveryOrder(dir string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, fixture.IndexName()))
	if err != nil {
		return nil, fmt.Errorf("ingest: read delivery index: %w", err)
	}
	var names []string
	start := 0
	for i := 0; i <= len(raw); i++ {
		if i == len(raw) || raw[i] == '\n' {
			if i > start {
				names = append(names, string(raw[start:i]))
			}
			start = i + 1
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("ingest: empty delivery index in %s", dir)
	}
	return names, nil
}
