package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"mediaroute/internal/model"
)

// ManifestVersion is bumped when the machine-readable schema changes.
const ManifestVersion = 1

// BuildInputs collects everything the manifest records.
type BuildInputs struct {
	Name         string
	Seed         int64
	OutputPath   string
	IndexPath    string
	OutputSize   uint64
	OutputSHA256 string
	// SourceBytes is the known size of the generated input stream. Bitrate is
	// measured over the nominal programme (known bytes over known duration), so
	// a dropped fragment does not make the measurement depend on damage.
	SourceBytes uint64
	TargetBPS   int64
	Source      *model.SourceSet
	Repairs     []model.RepairRecord
	Index       *model.Index
	Counts      Counts
	RTP         *model.RTPReport
}

// Counts mirrors the pipeline counters without importing it.
type Counts struct {
	MFHDGaps     int
	Duplicates   int
	RepairedTfdt int
	Truncated    int
	Dropped      int
	Recovered    int
}

// Build assembles the deterministic manifest. It refuses to produce a
// successful manifest when the source failed validation, so callers cannot
// accidentally emit a success record for an unrecoverable input.
func Build(in BuildInputs) (*model.Manifest, error) {
	if err := in.Source.Validate(); err != nil {
		return nil, err
	}
	if in.Index == nil {
		return nil, model.NewTaskError(model.CategoryCorruptIndex, Stage, "manifest requires a rebuilt index")
	}
	delivered := in.Source.DeliveredFragments()
	tracks := TrackSpans(in.Source.Tracks, delivered)
	alignment := AlignManifest(in.Index, tracks)

	totalSamples := 0
	validSamples := 0
	for i := range in.Source.Fragments {
		f := &in.Source.Fragments[i]
		n := f.SampleCount()
		totalSamples += n
		if !f.Discarded && !f.Truncated {
			validSamples += n
		}
	}

	// The delivered duration is the largest track span; repair aligns every
	// track to the same wall clock.
	var duration model.Micros
	for _, tm := range tracks {
		if tm.DurationMicros > duration {
			duration = tm.DurationMicros
		}
	}
	if duration <= 0 {
		return nil, model.NewTaskError(model.CategoryInvalidConfig, Stage, "delivered duration is zero")
	}

	bitrateBytes := in.SourceBytes
	if bitrateBytes == 0 {
		bitrateBytes = in.OutputSize
	}
	metrics, err := Compute(Inputs{
		TargetBPS:    in.TargetBPS,
		OutputBytes:  bitrateBytes,
		DurationUs:   duration,
		TotalSamples: totalSamples,
		ValidSamples: validSamples,
		Dropped:      in.Counts.Dropped,
		AlignmentUs:  alignment,
		IndexEntries: in.Index.Len(),
		RepairedTfdt: in.Counts.RepairedTfdt,
		MFHDGaps:     in.Counts.MFHDGaps,
		Truncated:    in.Counts.Truncated,
		Recovered:    in.Counts.Recovered,
		Duplicates:   in.Counts.Duplicates,
	})
	if err != nil {
		return nil, err
	}

	repairs := append([]model.RepairRecord(nil), in.Repairs...)
	sort.SliceStable(repairs, func(i, j int) bool {
		if repairs[i].TrackID != repairs[j].TrackID {
			return repairs[i].TrackID < repairs[j].TrackID
		}
		if repairs[i].Sequence != repairs[j].Sequence {
			return repairs[i].Sequence < repairs[j].Sequence
		}
		return repairs[i].Kind < repairs[j].Kind
	})

	bad := collectBadFragments(in.Source)

	m := &model.Manifest{
		Version:             ManifestVersion,
		Name:                in.Name,
		Seed:                in.Seed,
		OutputSHA256:        in.OutputSHA256,
		OutputSize:          in.OutputSize,
		SourceBytes:         bitrateBytes,
		OutputPath:          in.OutputPath,
		IndexPath:           in.IndexPath,
		TargetBPS:           metrics.TargetBPS,
		ActualBPS:           metrics.ActualBPS,
		BitrateDeltaPct:     metrics.BitrateDeltaPct,
		ValidSamplePct:      metrics.ValidSamplePct,
		DroppedFragments:    metrics.DroppedFragments,
		ManifestAlignmentUS: metrics.ManifestAlignUS,
		IndexEntries:        metrics.IndexEntries,
		RepairedTfdt:        metrics.RepairedTfdt,
		MFHDGaps:            metrics.MFHDGaps,
		TruncatedBoxes:      metrics.TruncatedBoxes,
		RecoveredFragments:  metrics.RecoveredFragments,
		DuplicateSequences:  metrics.DuplicateSequences,
		TotalSamples:        totalSamples,
		ValidSamples:        validSamples,
		DurationMicros:      duration,
		Tracks:              tracks,
		Repairs:             repairs,
		BadFragments:        bad,
		RTP:                 in.RTP,
		DamagedIndex:        in.Source.DamagedIndex,
		IndexIssues:         in.Source.IndexIssues,
		Index:               in.Index,
	}
	return m, nil
}

func collectBadFragments(set *model.SourceSet) []model.BadFragment {
	out := make([]model.BadFragment, 0, 4)
	for i := range set.Fragments {
		f := &set.Fragments[i]
		if !f.Truncated && !f.Discarded {
			continue
		}
		category := model.CategoryTruncatedBox
		detail := "fragment was isolated"
		if len(f.Issues) > 0 {
			category = f.Issues[0].Category
			detail = f.Issues[0].Detail
		}
		out = append(out, model.BadFragment{
			Index:     f.Index,
			Sequence:  f.Sequence,
			Range:     f.Range,
			Category:  category,
			Detail:    detail,
			Recovered: f.Recovered,
		})
	}
	return out
}

// MarshalCanonical renders the manifest as stable, indented JSON with a
// self-digest. Because the manifest contains no maps and all slices are
// pre-sorted, this output is byte-identical for identical input.
func MarshalCanonical(m *model.Manifest) ([]byte, error) {
	if m == nil {
		return nil, model.NewTaskError(model.CategoryInvalidConfig, Stage, "nil manifest")
	}
	digest, err := computeManifestDigest(m)
	if err != nil {
		return nil, err
	}
	m.ManifestSHA256 = digest
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, model.WrapTaskError(model.CategoryIO, Stage, "marshal manifest", err)
	}
	return append(b, '\n'), nil
}

// computeManifestDigest hashes the manifest payload with the self-digest field
// cleared, so verification can detect any change to the recorded data.
func computeManifestDigest(m *model.Manifest) (string, error) {
	cp := *m
	cp.ManifestSHA256 = ""
	b, err := json.Marshal(cp)
	if err != nil {
		return "", model.WrapTaskError(model.CategoryIO, Stage, "marshal manifest for digest", err)
	}
	return DigestBytes(b), nil
}

// VerifyManifestDigest recomputes the manifest's self-digest and reports
// whether the stored value matches.
func VerifyManifestDigest(m *model.Manifest) (bool, error) {
	want, err := computeManifestDigest(m)
	if err != nil {
		return false, err
	}
	return want == m.ManifestSHA256, nil
}

// Digest returns the manifest's self-digest, used to prove two runs on the
// same input produce the same record.
func Digest(m *model.Manifest) (string, error) {
	if m.ManifestSHA256 == "" {
		if _, err := MarshalCanonical(m); err != nil {
			return "", err
		}
	}
	return m.ManifestSHA256, nil
}

// DigestBytes is a helper for callers that already have canonical bytes.
func DigestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
