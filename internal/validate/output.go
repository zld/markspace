// Package validate independently re-reads the committed output and checks
// every delivery constraint. It never trusts in-memory results: frame count,
// monotonic timestamps, target sample rate, channel layout, loudness error and
// hashes are all verified from the files on disk. A single failure marks the
// whole run failed; a partially successful manifest is never reported done.
package validate

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"continuity/internal/model"
)

// Output layout inside the workspace.
const (
	OutputDir       = "output"
	ContainerName   = "frames.mp4"
	ManifestName    = "output-manifest.json"
	ReportName      = "validation.json"
	prefixLen       = 2
	maxLoudnessDrift = 1.0
)

// Manifest is the final, independently verifiable output document.
type Manifest struct {
	FrameCount    int                  `json:"frame_count"`
	SampleRate    uint32               `json:"sample_rate"`
	Channels      model.ChannelLayout  `json:"channels"`
	TargetLUFS    float64              `json:"target_lufs"`
	Frames        []model.OutputFrame  `json:"frames"`
	OutputSHA256  string               `json:"output_sha256"`
	ManifestSHA256 string              `json:"manifest_sha256"`
}

// Report is the validator's own result document.
type Report struct {
	Status   string `json:"status"` // "validated" or "failed"
	Frames   int    `json:"validated_frames"`
	Reason   string `json:"reason,omitempty"`
	Manifest *Manifest `json:"manifest,omitempty"`
}

// WriteOutput commits the transformed frames into the output container using
// the same 2-byte length-prefixed framing as the input, then writes the final
// manifest. Both writes are atomic so an interrupted run leaves no committed
// half-file for recovery to mistake for finished work.
func WriteOutput(root string, frames []model.OutputFrame, rate uint32,
	layout model.ChannelLayout, targetLUFS float64) (*Manifest, error) {

	dir, err := model.SafeJoin(root, OutputDir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	containerPath, err := model.SafeJoin(root, filepath.Join(OutputDir, ContainerName))
	if err != nil {
		return nil, err
	}
	var raw []byte
	for i := range frames {
		pkt := frames[i].Payload
		hdr := make([]byte, prefixLen)
		binary.BigEndian.PutUint16(hdr, uint16(len(pkt)))
		raw = append(raw, hdr...)
		raw = append(raw, pkt...)
	}
	if err = model.AtomicWrite(containerPath, raw, 0o644); err != nil {
		return nil, err
	}

	m := &Manifest{
		FrameCount:   len(frames),
		SampleRate:   rate,
		Channels:     layout,
		TargetLUFS:   targetLUFS,
		Frames:       frames,
		OutputSHA256: model.Checksum(raw),
	}
	manifestPath, err := model.SafeJoin(root, filepath.Join(OutputDir, ManifestName))
	if err != nil {
		return nil, err
	}
	// Hash the manifest body without its self-referential hash field by
	// marshalling the frame list and delivery parameters.
	bodyHash, err := model.CanonicalHash(bodyOf(m))
	if err != nil {
		return nil, err
	}
	m.ManifestSHA256 = bodyHash
	doc, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err = model.AtomicWrite(manifestPath, doc, 0o644); err != nil {
		return nil, err
	}
	return m, nil
}

type bodyDoc struct {
	FrameCount int                 `json:"frame_count"`
	SampleRate uint32              `json:"sample_rate"`
	Channels   model.ChannelLayout `json:"channels"`
	TargetLUFS float64             `json:"target_lufs"`
	Frames     []model.OutputFrame `json:"frames"`
	OutputSHA256 string            `json:"output_sha256"`
}

func bodyOf(m *Manifest) bodyDoc {
	return bodyDoc{
		FrameCount: m.FrameCount, SampleRate: m.SampleRate, Channels: m.Channels,
		TargetLUFS: m.TargetLUFS, Frames: m.Frames, OutputSHA256: m.OutputSHA256,
	}
}

// Verify re-reads the output from disk and checks all constraints, returning a
// structured report. The returned error is non-nil on any failed constraint.
func Verify(root string, expectedFrames int, rate uint32,
	layout model.ChannelLayout, targetLUFS float64) (*Report, *Manifest, error) {

	containerPath, err := model.SafeJoin(root, filepath.Join(OutputDir, ContainerName))
	if err != nil {
		return nil, nil, err
	}
	raw, err := os.ReadFile(containerPath)
	if err != nil {
		return nil, nil, model.WrapError(model.StageValidate, 0, 0, "reading output container", err)
	}
	manifestPath, err := model.SafeJoin(root, filepath.Join(OutputDir, ManifestName))
	if err != nil {
		return nil, nil, err
	}
	doc, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, nil, model.WrapError(model.StageValidate, 0, 0, "reading output manifest", err)
	}
	var m Manifest
	if err = json.Unmarshal(doc, &m); err != nil {
		return nil, nil, model.WrapError(model.StageValidate, 0, 0, "output manifest is not valid JSON", err)
	}

	fail := func(reason string) (*Report, *Manifest, error) {
		se := model.NewError(model.StageValidate, 0, 0, reason)
		return &Report{Status: "failed", Reason: reason, Manifest: &m}, &m, se
	}

	if m.FrameCount != expectedFrames || len(m.Frames) != expectedFrames {
		return fail(fmt.Sprintf("frame count mismatch: manifest %d, expected %d", m.FrameCount, expectedFrames))
	}
	if m.SampleRate != rate || m.Channels != layout {
		return fail("delivery geometry (sample rate / channels) does not match plan")
	}
	if model.Checksum(raw) != m.OutputSHA256 {
		return fail("output container sha256 does not match manifest")
	}
	bodyHash, err := model.CanonicalHash(bodyOf(&m))
	if err != nil || bodyHash != m.ManifestSHA256 {
		return fail("manifest self-hash does not match its contents")
	}

	// Re-walk the framed container and compare each re-read payload to the
	// manifest entry, checking monotonic time and the loudness tolerance.
	var prevNanos int64 = -1
	cursor := 0
	for i := range m.Frames {
		f := &m.Frames[i]
		if cursor+prefixLen > len(raw) {
			return fail(fmt.Sprintf("frame %d missing length prefix in container", i+1))
		}
		pktLen := int(binary.BigEndian.Uint16(raw[cursor : cursor+prefixLen]))
		cursor += prefixLen
		if cursor+pktLen > len(raw) {
			return fail(fmt.Sprintf("frame %d payload truncated in container", i+1))
		}
		payload := raw[cursor : cursor+pktLen]
		cursor += pktLen
		if model.Checksum(payload) != f.SHA256 || uint32(pktLen) != f.PayloadLength {
			return fail(fmt.Sprintf("frame %d on-disk hash/length mismatch", i+1))
		}
		if f.CaptureNanos <= prevNanos {
			return fail(fmt.Sprintf("frame %d capture time is not strictly monotonic", i+1))
		}
		prevNanos = f.CaptureNanos
		if f.SampleRate != rate || f.Channels != layout {
			return fail(fmt.Sprintf("frame %d geometry does not match delivery target", i+1))
		}
		drift := f.MeasuredLUFS - targetLUFS
		if drift < 0 {
			drift = -drift
		}
		if drift > maxLoudnessDrift {
			return fail(fmt.Sprintf("frame %d loudness drift %.2f exceeds tolerance %.2f",
				i+1, drift, maxLoudnessDrift))
		}
	}
	if cursor != len(raw) {
		return fail("trailing bytes after final frame in output container")
	}

	report := &Report{Status: "validated", Frames: expectedFrames, Manifest: &m}
	return report, &m, nil
}

// WriteReport persists the validation report atomically.
func WriteReport(root string, r *Report) error {
	path, err := model.SafeJoin(root, filepath.Join(OutputDir, ReportName))
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return model.AtomicWrite(path, raw, 0o644)
}
