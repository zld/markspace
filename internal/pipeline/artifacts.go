package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"rtx/internal/cmaf"
	"rtx/internal/evidence"
	"rtx/internal/index"
	"rtx/internal/model"
	"rtx/internal/repair"
)

// RepairPlan maps a fragment path and track to the tfdt value that must be
// written into the output artifact, and the record explaining why.
type RepairPlan struct {
	Fragment string
	TrackID  uint32
	Value    uint64
	Repair   repair.Repair
}

// planRepairs maps repair records to concrete tfdt patches keyed by fragment.
func planRepairs(tl *repair.Timeline) map[string]map[uint32]RepairPlan {
	out := map[string]map[uint32]RepairPlan{}
	for _, r := range tl.Repairs {
		key := r.Source + "\x00" + r.Fragment
		if out[key] == nil {
			out[key] = map[uint32]RepairPlan{}
		}
		out[key][r.TrackID] = RepairPlan{Fragment: r.Fragment, TrackID: r.TrackID, Value: r.Repaired, Repair: r}
	}
	return out
}

// writeRecord writes one source fragment's repaired artifact: the original
// fragment bytes with any repairing tfdt patch applied, named by source and
// order so the output is independent of absolute input paths.
func writeRecord(outDir string, rec Record, plans map[uint32]RepairPlan) ([]ArtifactRecord, error) {
	name := fmt.Sprintf("%s-%04d.m4s", sanitize(rec.SourceID), rec.Order)
	data, err := buildRepairedFragment(rec, plans)
	if err != nil {
		return nil, err
	}
	return writeArtifact(outDir, name, data)
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

// buildRepairedFragment applies every planned tfdt patch for one record.
func buildRepairedFragment(rec Record, plans map[uint32]RepairPlan) ([]byte, error) {
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

// writeDerived emits the index, timeline, evidence and a report, returning
// their artifact records plus the metrics.
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

	// Assemble evidence samples over the repaired index.
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
	OK           bool                 `json:"ok"`
	Resume       bool                 `json:"resume"`
	Records      int                  `json:"records"`
	Cursor       int                  `json:"cursor"`
	CacheHits    int                  `json:"cache_hits"`
	CacheMisses  int                  `json:"cache_misses"`
	Gaps         int                  `json:"gap"`
	TFDTRepairs  int                  `json:"tfdt_rollback"`
	SourceSwitch int                  `json:"source_switch"`
	RepairDetail []repair.Repair      `json:"repairs"`
	SequenceGaps []repair.SequenceGap `json:"sequence_gaps"`
	TraceFacts   []rtpFacts           `json:"rtp"`
	IndexDigest  string               `json:"index_sha256"`
	MediaDigest  string               `json:"media_sha256"`
	ManifestSum  string               `json:"manifest_sha256"`
	InputSum     string               `json:"input_sha256"`
}

// writeReport emits report.json from the finished result.
func writeReport(opts Options, res *Result, facts []rtpFacts) ([]ArtifactRecord, error) {
	rep := &Report{
		OK:           true,
		Resume:       res.Resume,
		Records:      res.Records,
		Cursor:       res.Cursor,
		CacheHits:    res.CacheHits,
		CacheMisses:  res.CacheMisses,
		Gaps:         res.Gaps,
		TFDTRepairs:  res.Repairs,
		SourceSwitch: res.SourceSwitch,
		RepairDetail: res.Timeline.Repairs,
		SequenceGaps: res.Timeline.SequenceGaps,
		TraceFacts:   facts,
		IndexDigest:  res.IndexSum,
		MediaDigest:  res.MediaSum,
		ManifestSum:  res.ManifestSum,
		InputSum:     res.InputSum,
	}
	data, err := cmaf.MarshalDeterministic(rep)
	if err != nil {
		return nil, model.E(model.CodeInternal, "encode report", "%v", err)
	}
	return writeArtifact(opts.OutDir, "report.json", data)
}
