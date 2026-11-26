package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"rtx/internal/cmaf"
	"rtx/internal/evidence"
	"rtx/internal/index"
	"rtx/internal/model"
	"rtx/internal/repair"
)

// WritePlan is the base media decode time one track's tfdt must carry in the
// output. The value is on the shared, absolute media grid, so concatenated
// outputs form one continuous timeline across a tfdt repair and across a source
// switch. Reason and Repair explain why the value differs from the input.
type WritePlan struct {
	TrackID uint32
	Value   uint64
	Reason  string
	Repair  *repair.Repair
}

// planRepairs derives, for every fragment track, the absolute tfdt to write.
// Where a fragment's own tfdt already matches the absolute value and the source
// starts at zero, no plan is emitted and the bytes are left untouched.
func planRepairs(tl *repair.Timeline) map[string]map[uint32]WritePlan {
	mediaRate := tl.MediaRate
	out := map[string]map[uint32]WritePlan{}
	for _, ft := range tl.Fragments {
		frag := ft.Fragment
		key := ft.Source + "\x00" + frag.Path
		for _, span := range ft.Tracks {
			value, err := model.FromTicks(span.Start.Ticks, mediaRate, span.Rate)
			if err != nil {
				// Index construction already proved the span lands exactly;
				// keep the original bytes defensively rather than truncating.
				continue
			}
			if uint64(value) == span.OriginalTFD {
				continue
			}
			plan := WritePlan{TrackID: span.TrackID, Value: uint64(value)}
			if span.RepairedTFD != span.OriginalTFD {
				plan.Reason = "tfdt_rollback"
			} else {
				plan.Reason = "source_offset"
			}
			if out[key] == nil {
				out[key] = map[uint32]WritePlan{}
			}
			out[key][span.TrackID] = plan
		}
	}
	// Attach the exact repair record so the emitted explanation stays precise.
	for i := range tl.Repairs {
		r := &tl.Repairs[i]
		key := r.Source + "\x00" + r.Fragment
		plan, ok := out[key][r.TrackID]
		if !ok {
			continue
		}
		plan.Repair = r
		out[key][r.TrackID] = plan
	}
	return out
}

// writeRecord writes one source fragment's repaired artifact: the original
// fragment bytes with the planned absolute tfdt written in, named by source and
// order so the output is independent of absolute input paths.
func writeRecord(outDir string, rec Record, plans map[uint32]WritePlan) ([]ArtifactRecord, error) {
	name := fmt.Sprintf("%s-%04d.m4s", sanitize(rec.SourceID), rec.Order)
	data, err := buildRepairedFragment(rec, plans)
	if err != nil {
		return nil, err
	}
	return writeArtifact(outDir, name, data)
}

// buildRepairedFragment applies every planned tfdt patch for one record.
func buildRepairedFragment(rec Record, plans map[uint32]WritePlan) ([]byte, error) {
	data := rec.Raw
	for _, track := range rec.Fragment.Tracks {
		plan, ok := plans[track.TrackID]
		if !ok {
			continue
		}
		patched, err := cmaf.PatchTFDT(data, &track, plan.Value)
		if err != nil {
			return nil, err
		}
		data = patched
	}
	return data, nil
}

func cacheSum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeArtifact(outDir, name string, data []byte) ([]ArtifactRecord, error) {
	path := filepath.Join(outDir, name)
	if err := writeFileAtomic(path, data); err != nil {
		return nil, model.E(model.CodeInvalidInput, "write artifact", "%v", err).WithPath(path)
	}
	sum, size, err := FileDigest(path)
	if err != nil {
		return nil, model.E(model.CodeInvalidInput, "digest artifact", "%v", err).WithPath(path)
	}
	return []ArtifactRecord{{Name: name, SHA256: sum, Size: size}}, nil
}

// writeDerived emits the index, timeline, evidence and report artifacts,
// returning their records plus the metrics.
func writeDerived(opts Options, res *Result, records []Record, tl *repair.Timeline, idx *index.Index, switches []cmaf.Event) ([]ArtifactRecord, *evidence.Metrics, error) {
	var artifacts []ArtifactRecord

	idxBytes, err := cmaf.MarshalDeterministic(idx)
	if err != nil {
		return nil, nil, model.E(model.CodeInternal, "encode index", "%v", err)
	}
	a, err := writeArtifact(opts.OutDir, "index.json", idxBytes)
	if err != nil {
		return nil, nil, err
	}
	artifacts = append(artifacts, a...)
	res.IndexSum = cacheSum(idxBytes)

	tlBytes, err := cmaf.MarshalDeterministic(tl)
	if err != nil {
		return nil, nil, model.E(model.CodeInternal, "encode timeline", "%v", err)
	}
	if a, err := writeArtifact(opts.OutDir, "timeline.json", tlBytes); err != nil {
		return nil, nil, err
	} else {
		artifacts = append(artifacts, a...)
	}

	samples, err := indexSamples(records, idx)
	if err != nil {
		return nil, nil, err
	}
	var metrics *evidence.Metrics
	if opts.SkipEvidence {
		metrics = &evidence.Metrics{}
	} else {
		metrics, err = evidence.Compute(samples, evidence.TrackIDs{Video: TrackSpecs.VideoTrackID, Audio: TrackSpecs.AudioTrackID})
		if err != nil {
			return nil, nil, err
		}
	}
	mBytes, err := cmaf.MarshalDeterministic(metrics)
	if err != nil {
		return nil, nil, model.E(model.CodeInternal, "encode metrics", "%v", err)
	}
	if a, err := writeArtifact(opts.OutDir, "metrics.json", mBytes); err != nil {
		return nil, nil, err
	} else {
		artifacts = append(artifacts, a...)
	}

	// A digest over the input fragment bytes, so repeat runs can assert
	// byte-for-byte equality of the media they consumed.
	h := sha256.New()
	for _, rec := range records {
		h.Write([]byte(rec.SourceID))
		h.Write([]byte{0})
		h.Write(rec.Raw)
	}
	res.MediaSum = hex.EncodeToString(h.Sum(nil))

	return artifacts, metrics, nil
}

// indexSamples joins index entries back to the bytes they point at.
func indexSamples(records []Record, idx *index.Index) ([]evidence.Sample, error) {
	byPath := map[string]Record{}
	for _, rec := range records {
		byPath[rec.Fragment.Path] = rec
	}
	rate := idx.Rate()
	out := make([]evidence.Sample, 0, len(idx.Entries))
	for _, e := range idx.Entries {
		rec, ok := byPath[e.Fragment]
		if !ok {
			return nil, model.E(model.CodeInternal, "evidence", "index entry references unknown fragment %s", e.Fragment)
		}
		data, err := cmaf.SampleData(rec.Raw, cmaf.Sample{DataOffset: e.ByteOffset, Size: e.Size})
		if err != nil {
			return nil, err
		}
		out = append(out, evidence.Sample{
			TrackID:  e.TrackID,
			Fragment: e.Fragment,
			Offset:   e.ByteOffset,
			Start:    model.MustTime(e.MediaTicks, rate),
			End:      model.MustTime(e.MediaTicks+e.MediaDuration, rate),
			Data:     data,
		})
	}
	return out, nil
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "source"
	}
	return string(out)
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".out-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// rtpFacts are included in the run report from the verified RTP stage.
type rtpFacts struct {
	Source        string `json:"source"`
	Packets       int    `json:"packets"`
	GapPackets    int    `json:"gap_packets"`
	SequenceWraps int    `json:"sequence_wraps"`
	OutOfOrder    int    `json:"out_of_order"`
	TimestampWrap bool   `json:"timestamp_wrap"`
}

// Report is the human- and machine-readable run summary.
type Report struct {
	OK                 bool                 `json:"ok"`
	Resume             bool                 `json:"resume"`
	Records            int                  `json:"records"`
	Cursor             int                  `json:"cursor"`
	CacheHits          int                  `json:"cache_hits"`
	CacheMisses        int                  `json:"cache_misses"`
	Gaps               int                  `json:"gap"`
	TFDTRepairs        int                  `json:"tfdt_rollback"`
	SourceSwitch       int                  `json:"source_switch"`
	RepairDetail       []repair.Repair      `json:"repairs"`
	SequenceGaps       []repair.SequenceGap `json:"sequence_gaps"`
	SourceSwitchEvents []cmaf.Event         `json:"source_switch_events,omitempty"`
	TraceFacts         []rtpFacts           `json:"rtp"`
	Timeline           []assemblyEntry      `json:"written_timeline,omitempty"`
	IndexDigest        string               `json:"index_sha256"`
	MediaDigest        string               `json:"media_sha256"`
	ManifestSum        string               `json:"manifest_sha256"`
	InputSum           string               `json:"input_sha256"`
}

// writeReport emits report.json from the finished result.
func writeReport(opts Options, res *Result, facts []rtpFacts, switches []cmaf.Event, plans map[string]map[uint32]WritePlan, records []Record) ([]ArtifactRecord, error) {
	rep := &Report{
		OK:                 true,
		Resume:             res.Resume,
		Records:            res.Records,
		Cursor:             res.Cursor,
		CacheHits:          res.CacheHits,
		CacheMisses:        res.CacheMisses,
		Gaps:               res.Gaps,
		TFDTRepairs:        res.Repairs,
		SourceSwitch:       res.SourceSwitch,
		RepairDetail:       res.Timeline.Repairs,
		SequenceGaps:       res.Timeline.SequenceGaps,
		SourceSwitchEvents: switches,
		TraceFacts:         facts,
		Timeline:           assemblyFromPlans(plans, records),
		IndexDigest:        res.IndexSum,
		MediaDigest:        res.MediaSum,
		ManifestSum:        res.ManifestSum,
		InputSum:           res.InputSum,
	}
	data, err := cmaf.MarshalDeterministic(rep)
	if err != nil {
		return nil, model.E(model.CodeInternal, "encode report", "%v", err)
	}
	return writeArtifact(opts.OutDir, "report.json", data)
}

// switchAssembly reports, for the report, the absolute tfdt carried by each
// written fragment track so a reader can see the unified timeline.
type assemblyEntry struct {
	Fragment  string `json:"fragment"`
	TrackID   uint32 `json:"track_id"`
	TFDTTicks int64  `json:"tfdt_ticks"`
	Reason    string `json:"reason,omitempty"`
}

func assemblyFromPlans(plans map[string]map[uint32]WritePlan, records []Record) []assemblyEntry {
	var out []assemblyEntry
	for _, rec := range records {
		key := rec.SourceID + "\x00" + rec.Fragment.Path
		byTrack := plans[key]
		for trackID, plan := range byTrack {
			out = append(out, assemblyEntry{
				Fragment:  filepath.Base(rec.RelPath),
				TrackID:   trackID,
				TFDTTicks: int64(plan.Value),
				Reason:    plan.Reason,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Fragment != out[j].Fragment {
			return out[i].Fragment < out[j].Fragment
		}
		return out[i].TrackID < out[j].TrackID
	})
	return out
}
