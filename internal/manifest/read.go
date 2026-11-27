// Package manifest parses and validates the per-frame input manifest. Any
// length, checksum or required-metadata violation is reported as the first
// locatable structured error; frames are never silently skipped.
package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"continuity/internal/model"
)

// Document is the on-disk manifest envelope.
type Document struct {
	Frames []model.Frame `json:"frames"`
}

// Read loads and validates the manifest at root/input/manifest.json. It does
// not load frame payloads; the index module does that after the cache check.
func Read(root string) ([]model.Frame, error) {
	path, err := model.SafeJoin(root, filepath.Join("input", "manifest.json"))
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, model.WrapError(model.StageManifest, 0, 0, "reading manifest", err)
	}
	return Parse(raw)
}

// Parse validates raw manifest JSON and assigns 1-based frame numbers.
func Parse(raw []byte) ([]model.Frame, error) {
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, model.WrapError(model.StageManifest, 0, 0, "manifest is not valid JSON", err)
	}
	if len(doc.Frames) == 0 {
		return nil, model.NewError(model.StageManifest, 0, 0, "manifest contains no frames")
	}

	seen := make(map[uint32]struct{}, len(doc.Frames))
	for i := range doc.Frames {
		f := &doc.Frames[i]
		num := model.FrameNumber(i + 1)
		f.Number = num
		if err := validateFrame(f, num); err != nil {
			return nil, err
		}
		if _, dup := seen[f.Sequence]; dup {
			return nil, model.NewError(model.StageManifest, num, 0,
				fmt.Sprintf("duplicate sequence number %d", f.Sequence))
		}
		seen[f.Sequence] = struct{}{}
	}
	return doc.Frames, nil
}

func validateFrame(f *model.Frame, num model.FrameNumber) error {
	stage := model.StageManifest
	if f.Track == 0 {
		return model.NewError(stage, num, 0, "track is required and must be non-zero")
	}
	if f.Sequence == 0 {
		return model.NewError(stage, num, 0, "sequence is required and must be non-zero")
	}
	if f.CaptureNanos < 0 {
		return model.NewError(stage, num, 0, "capture_nanos must be non-negative")
	}
	if f.ContainerID == "" {
		return model.NewError(stage, num, 0, "container_id is required")
	}
	if f.SampleRate == 0 {
		return model.NewError(stage, num, 0, "sample_rate is required and must be non-zero")
	}
	if !f.Channels.Valid() {
		return model.NewError(stage, num, 0, "channels must be mono or stereo")
	}
	if f.TargetLUFS > 0 || f.TargetLUFS < -120 {
		return model.NewError(stage, num, 0,
			fmt.Sprintf("target_lufs %.2f is outside the supported range [-120, 0]", f.TargetLUFS))
	}
	if f.PayloadLength == 0 {
		return model.NewError(stage, num, 0, "payload_length must be non-zero")
	}
	if len(f.SHA256) != 64 {
		return model.NewError(stage, num, 0, "sha256 must be a 64-character lowercase hex digest")
	}
	for _, c := range f.SHA256 {
		hex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		if !hex {
			return model.NewError(stage, num, 0, "sha256 contains non-hexadecimal characters")
		}
	}
	return nil
}

// VerifyPayload checks a loaded frame payload against its declared length and
// checksum, anchoring any mismatch to the frame and its container offset.
func VerifyPayload(f *model.Frame) error {
	if len(f.Payload) == 0 {
		return model.NewError(model.StageManifest, f.Number, int64(f.ContainerBytes),
			"zero-length frame payload")
	}
	if uint32(len(f.Payload)) != f.PayloadLength {
		return model.NewError(model.StageManifest, f.Number, int64(f.ContainerBytes),
			fmt.Sprintf("payload length mismatch: declared %d, actual %d",
				f.PayloadLength, len(f.Payload)))
	}
	if !model.VerifyChecksum(f.Payload, f.SHA256) {
		return model.NewError(model.StageManifest, f.Number, int64(f.ContainerBytes),
			"payload sha256 mismatch")
	}
	return nil
}
