package pipeline

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"mediarun/internal/fault"
	"mediarun/internal/frames"
	"mediarun/internal/provenance"
	"mediarun/internal/recovery"
)

// buildManifest assembles the final document with stable ordering.
func (r *Runner) buildManifest() (*provenance.Manifest, error) {
	inputs := []provenance.Input{}
	for _, pair := range []struct{ name, path, hash string }{
		{"frames", r.inputs.FramesPath, r.hashes.frames},
		{"audio", r.inputs.AudioPath, r.hashes.audio},
	} {
		if pair.hash == "" {
			continue
		}
		info, err := os.Stat(pair.path)
		if err != nil {
			return nil, fault.Wrap(fault.StagePipeline, fault.KindIO, err, "stat %s", pair.path)
		}
		inputs = append(inputs, provenance.Input{
			Name: pair.name, Path: filepath.Base(pair.path), Bytes: info.Size(), SHA256: pair.hash,
		})
	}
	m := &provenance.Manifest{
		Version:    provenance.Version,
		Tool:       "mediarun",
		ConfigHash: r.cfg.Hash(),
		Timescale:  r.cfg.Timescale,
		Inputs:     inputs,
		Stages:     r.stages,
		Segments:   r.segments,
		Faults:     r.faultRecords(),
		Metrics:    r.metrics,
	}
	// Segment-local faults (sync drift, timeline holes) already reach the
	// manifest through the sync stage summary, which recordStage copies into
	// r.faults. They are not hoisted again here, or every drift would be
	// counted twice. The segment records keep their own copy for per-fragment
	// review.
	if n := len(r.records); n > 0 {
		m.TimelineEnd = int64(r.records[n-1].Meta.PTS)
	}
	sort.SliceStable(m.Stages, func(i, j int) bool {
		return stageIndex(m.Stages[i].Name) < stageIndex(m.Stages[j].Name)
	})
	return m, nil
}

func (r *Runner) faultRecords() []provenance.FaultRecord {
	out := make([]provenance.FaultRecord, 0, len(r.faults))
	for _, e := range r.faults {
		out = append(out, provenance.FromFault(e))
	}
	provenance.SortFaults(out)
	return out
}

// writeManifest serializes and atomically replaces manifest.json.
func (r *Runner) writeManifest(m *provenance.Manifest) error {
	path := filepath.Join(r.dir, ManifestName)
	if err := provenance.WriteAtomic(path, m); err != nil {
		return err
	}
	return r.cp.Mark(r.dir, "manifest", provenance.HashBytesHex(mustJSON(m)))
}

func mustJSON(v any) []byte {
	b, err := provenance.CanonicalJSON(v)
	if err != nil {
		return nil
	}
	return b
}

// recordsDigest hashes only metadata so metadata-only records hash identically
// to full ones.
func recordsDigest(recs []*frames.Record) string {
	h := newDigest()
	for _, rec := range recs {
		fmt.Fprintf(h, "%d|%d|%d|%d|%x|%d\n", rec.Offset, rec.Meta.PTS, rec.Meta.DTS, rec.Meta.Flags, rec.Meta.Checksum[:4], rec.Meta.Size)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// audioDigest hashes the sealed unit sequence.
func audioDigest(units []recovery.Unit) string {
	h := newDigest()
	for _, u := range units {
		fmt.Fprintf(h, "%d|%d|%d|%t\n", u.Seq, u.RTPTimestamp, u.TickSpan, u.Silence)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func isEOF(err error) bool { return errors.Is(err, io.EOF) }

// countParseFaults counts faults raised by the parse and checksum stages,
// which is what the acceptance line reports as parse_errors.
func countParseFaults(fs fault.List) int {
	n := 0
	for _, e := range fs {
		if e.Stage == fault.StageParse || e.Stage == fault.StageChecksum {
			n++
		}
	}
	return n
}

// audioStatsPath is the sidecar summary the generator writes next to the
// audio fixture. The pipeline records its digest so the manifest describes
// every input, including the declared RTP base timestamp.
func audioStatsPath(dir string) string { return filepath.Join(dir, "audio.stats") }
