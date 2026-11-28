package manifest

import (
	"fmt"
	"os"

	"mediapack/internal/model"
)

// VerifyReport is the outcome of one independent verification pass.
type VerifyReport struct {
	OK            bool     `json:"ok"`
	ManifestHash  string   `json:"manifest_sha256"`
	FrameCount    int      `json:"frame_count"`
	LayerCount    int      `json:"layer_count"`
	SegmentChecks int      `json:"segment_checks"`
	FrameChecks   int      `json:"frame_checks"`
	Differences   []string `json:"differences"`
}

// Verify re-reads the sealed manifest and cross-checks every layer's frame
// records against it: frame range, tick ordering, segment boundaries and the
// per-frame SHA-256 entries. It does not trust in-memory state.
func Verify(root string) (*VerifyReport, error) {
	m, _, hash, err := Read(root)
	if err != nil {
		return &VerifyReport{OK: false, Differences: []string{err.Error()}}, err
	}
	rep := &VerifyReport{
		OK: true, ManifestHash: hash,
		LayerCount: len(m.Layers),
	}
	if m.Version != ManifestVersion {
		rep.addf("manifest version %d unsupported, want %d", m.Version, ManifestVersion)
	}
	for _, layer := range m.Layers {
		records, err := ReadFrameRecords(root, layer.Name)
		if err != nil {
			rep.add(err)
			continue
		}
		if err := compareLayer(layer, records, rep); err != nil {
			rep.add(err)
		}
	}
	rep.FrameCount = m.FrameCount
	if len(rep.Differences) > 0 {
		rep.OK = false
	}
	return rep, nil
}

// compareLayer walks one layer's segments and frame records together.
func compareLayer(layer LayerEntry, records []FrameEntry, rep *VerifyReport) error {
	if len(records) != len(layer.Frames) {
		rep.addf("layer %s frame record count %d != manifest %d",
			layer.Name, len(records), len(layer.Frames))
	}
	rep.FrameChecks += len(layer.Frames)
	// Every manifest frame must match its on-disk record exactly.
	for i, want := range layer.Frames {
		if i >= len(records) {
			rep.addf("layer %s missing frame record %d", layer.Name, i)
			continue
		}
		got := records[i]
		if got != want {
			rep.addf("layer %s frame %d differs: record=%+v manifest=%+v",
				layer.Name, i, got, want)
		}
	}
	// Segment boundaries must cover every frame range without overlap.
	rep.SegmentChecks += len(layer.Segments)
	nextFrame := 0
	for _, seg := range layer.Segments {
		if seg.FrameStart != nextFrame {
			rep.addf("layer %s segment %d starts at frame %d, want %d",
				layer.Name, seg.Index, seg.FrameStart, nextFrame)
		}
		if seg.FrameEnd < seg.FrameStart {
			rep.addf("layer %s segment %d inverted frame range", layer.Name, seg.Index)
		}
		if seg.TickEnd < seg.TickStart {
			rep.addf("layer %s segment %d inverted tick range", layer.Name, seg.Index)
		}
		nextFrame = seg.FrameEnd + 1
	}
	if nextFrame != len(layer.Frames) {
		rep.addf("layer %s segments cover %d frames, manifest lists %d",
			layer.Name, nextFrame, len(layer.Frames))
	}
	// Gaps and completeness must agree.
	for _, seg := range layer.Segments {
		hasGap := len(seg.Gaps) > 0
		if seg.Complete == hasGap {
			rep.addf("layer %s segment %d complete flag disagrees with gaps",
				layer.Name, seg.Index)
		}
	}
	return nil
}

func (r *VerifyReport) add(err error) {
	r.Differences = append(r.Differences, err.Error())
}

func (r *VerifyReport) addf(format string, args ...interface{}) {
	r.Differences = append(r.Differences, fmt.Sprintf(format, args...))
}

// GuardVerifiedArtifact refuses to overwrite an already-verified output.
// Corrupt input must never replace sealed artifacts.
func GuardVerifiedArtifact(root string) error {
	mPath, perr := SafePath(root, FileManifest)
	if perr != nil {
		return perr
	}
	if _, err := os.Stat(mPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	m, _, _, err := Read(root)
	if err != nil {
		// Present but unreadable: treat as sealed and refuse to overwrite.
		return err
	}
	if m.Stage == model.StageVerified {
		return model.NewFieldError(model.KindInvalidState, root, -1, "stage",
			"verified artifact cannot be overwritten; choose a new output directory")
	}
	return nil
}
