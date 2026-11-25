package manifest

import (
	"fmt"

	"replaycheck/internal/model"
	"replaycheck/internal/policy"
)

// Report is the outcome of verifying a manifest.
type Report struct {
	Valid          bool       `json:"valid"`
	Segments       int        `json:"segments"`
	TotalBytes     int64      `json:"totalBytes"`
	SummedBytes    int64      `json:"summedBytes"`
	DigestOK       bool       `json:"digestOk"`
	ManifestDigest string     `json:"manifestDigest"`
	MinQualityPPM  policy.PPM `json:"minQualityPpm"`
	Issues         []string   `json:"issues"`
}

// Verify checks a manifest against the expected source and policy. Rules:
// entries are contiguous and non-overlapping half-open intervals, every derived
// digest recomputes, the declared total equals the sum, the floor is respected,
// and the manifest digest matches its own contents.
func Verify(m *Manifest, wantSource, wantPolicy string, minQuality policy.PPM) (Report, error) {
	rep := Report{Segments: len(m.Segments), ManifestDigest: m.Digest, MinQualityPPM: minQuality, TotalBytes: m.TotalBytes}
	if len(m.Segments) == 0 {
		rep.Issues = append(rep.Issues, "manifest has no segments")
	}
	if m.SourceDigest != wantSource {
		rep.Issues = append(rep.Issues, fmt.Sprintf("source digest %s does not match expected %s", m.SourceDigest, wantSource))
	}
	if m.PolicyDigest != wantPolicy {
		rep.Issues = append(rep.Issues, fmt.Sprintf("policy digest %s does not match expected %s", m.PolicyDigest, wantPolicy))
	}
	var sum int64
	for i, e := range m.Segments {
		if e.Index != i {
			rep.Issues = append(rep.Issues, fmt.Sprintf("segment %d has index %d", i, e.Index))
		}
		if e.EndTick <= e.StartTick {
			rep.Issues = append(rep.Issues, fmt.Sprintf("segment %d has non-positive span [%d,%d)", i, e.StartTick, e.EndTick))
		}
		if i > 0 {
			prev := m.Segments[i-1]
			switch {
			case prev.EndTick > e.StartTick:
				rep.Issues = append(rep.Issues, fmt.Sprintf("segments %d and %d overlap at %d", i-1, i, e.StartTick))
			case prev.EndTick < e.StartTick:
				rep.Issues = append(rep.Issues, fmt.Sprintf("gap between segments %d and %d: [%d,%d)", i-1, i, prev.EndTick, e.StartTick))
			}
		}
		if e.Bytes <= 0 {
			rep.Issues = append(rep.Issues, fmt.Sprintf("segment %d has no bytes", i))
		}
		if e.QualityPPM < minQuality {
			rep.Issues = append(rep.Issues, fmt.Sprintf("segment %d quality %d below floor %d", i, e.QualityPPM, minQuality))
		}
		if !model.ValidDigest(e.SourceDigest) {
			rep.Issues = append(rep.Issues, fmt.Sprintf("segment %d source digest malformed", i))
		}
		again, err := derivedDigest(e)
		if err != nil {
			rep.Issues = append(rep.Issues, fmt.Sprintf("segment %d digest error: %v", i, err))
		} else if again != e.DerivedDigest {
			rep.Issues = append(rep.Issues, fmt.Sprintf("segment %d derived digest does not recompute", i))
		}
		if len(e.Triggers) != 2 {
			rep.Issues = append(rep.Issues, fmt.Sprintf("segment %d does not name both boundary triggers", i))
		}
		sum += e.Bytes
	}
	rep.SummedBytes = sum
	if sum != m.TotalBytes {
		rep.Issues = append(rep.Issues, fmt.Sprintf("declared total %d != summed %d", m.TotalBytes, sum))
	}
	digest, err := Digest(m)
	if err != nil {
		return rep, err
	}
	rep.DigestOK = digest == m.Digest
	if !rep.DigestOK {
		rep.Issues = append(rep.Issues, "manifest digest does not match contents")
	}
	rep.Valid = len(rep.Issues) == 0
	if !rep.Valid {
		return rep, model.NewError(model.StageManifest, "verify", model.TrackRef{}, 0, rep.Issues[0])
	}
	return rep, nil
}

// Diff reports field-level differences between two manifests, so a rerun that
// diverges says exactly which field changed instead of only "not equal".
type Diff struct {
	Fields []string `json:"fields"`
	Count  int      `json:"count"`
	Same   bool     `json:"same"`
}

func (d *Diff) add(format string, args ...any) {
	d.Fields = append(d.Fields, fmt.Sprintf(format, args...))
}

// Compare lists every differing field, including per-segment fields.
func Compare(a, b *Manifest) Diff {
	var d Diff
	if a.Version != b.Version {
		d.add("version: %d != %d", a.Version, b.Version)
	}
	if a.Fixture != b.Fixture {
		d.add("fixture: %s != %s", a.Fixture, b.Fixture)
	}
	if a.SourceDigest != b.SourceDigest {
		d.add("sourceDigest: %s != %s", a.SourceDigest, b.SourceDigest)
	}
	if a.PolicyDigest != b.PolicyDigest {
		d.add("policyDigest: %s != %s", a.PolicyDigest, b.PolicyDigest)
	}
	if a.TotalBytes != b.TotalBytes {
		d.add("totalBytes: %d != %d", a.TotalBytes, b.TotalBytes)
	}
	if a.MinQualityPPM != b.MinQualityPPM {
		d.add("minQualityPpm: %d != %d", a.MinQualityPPM, b.MinQualityPPM)
	}
	if len(a.Segments) != len(b.Segments) {
		d.add("segmentCount: %d != %d", len(a.Segments), len(b.Segments))
	}
	n := len(a.Segments)
	if len(b.Segments) < n {
		n = len(b.Segments)
	}
	for i := 0; i < n; i++ {
		x, y := a.Segments[i], b.Segments[i]
		if x.StartTick != y.StartTick {
			d.add("segments[%d].startTick: %d != %d", i, x.StartTick, y.StartTick)
		}
		if x.EndTick != y.EndTick {
			d.add("segments[%d].endTick: %d != %d", i, x.EndTick, y.EndTick)
		}
		if x.SourceDigest != y.SourceDigest {
			d.add("segments[%d].sourceDigest: %s != %s", i, x.SourceDigest, y.SourceDigest)
		}
		if x.DerivedDigest != y.DerivedDigest {
			d.add("segments[%d].derivedDigest: %s != %s", i, x.DerivedDigest, y.DerivedDigest)
		}
		if x.Bytes != y.Bytes {
			d.add("segments[%d].bytes: %d != %d", i, x.Bytes, y.Bytes)
		}
		if x.QualityPPM != y.QualityPPM {
			d.add("segments[%d].qualityPpm: %d != %d", i, x.QualityPPM, y.QualityPPM)
		}
		if x.Level != y.Level {
			d.add("segments[%d].level: %s != %s", i, x.Level, y.Level)
		}
	}
	d.Count = len(d.Fields)
	d.Same = d.Count == 0
	return d
}
