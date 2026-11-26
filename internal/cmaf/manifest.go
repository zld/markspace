package cmaf

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"rtx/internal/model"
)

// Manifest is the input list for one run directory. Every relative path is
// resolved against the manifest's own directory and must stay inside the work
// directory; absolute and parent-escaping paths are rejected.
type Manifest struct {
	Version int `json:"version"`
	// MediaRate is the unified media time base numerator/denominator in ticks
	// per second. Fixtures use 720000/1 so both 90kHz and 48kHz land exactly.
	MediaRate struct {
		Num int64 `json:"num"`
		Den int64 `json:"den"`
	} `json:"media_rate"`
	Sources []Source `json:"sources"`
	Events  []Event  `json:"events"`
}

// Source declares one media source: its init segment, its fragments, its RTP
// trace and the explicit RTP anchor that ties its clock to media time.
type Source struct {
	ID         string        `json:"id"`
	Init       string        `json:"init"`
	Fragments  []FragmentRef `json:"fragments"`
	RTPTrace   string        `json:"rtp_trace"`
	RTPAnchors []RTPAnchor   `json:"rtp_anchors"`
	// MediaOffset is the explicit media-grid tick at which this source's
	// timeline begins. A source switch is placed only by these offsets, never
	// inferred from fragment contents.
	MediaOffset int64 `json:"media_offset_ticks"`
}

// FragmentRef points at a fragment file and gives it an explicit order.
type FragmentRef struct {
	Path  string `json:"path"`
	Order int    `json:"order"`
}

// RTPAnchor is the manifest encoding of an explicit source anchor. It ties an
// RTP timestamp (with its 32-bit wrap count) to an absolute media instant.
type RTPAnchor struct {
	Source       string `json:"source"`
	RTPTimestamp uint32 `json:"rtp_timestamp"`
	Wrap         int64  `json:"wrap"`
	MediaTicks   int64  `json:"media_ticks"`
	ClockRate    int64  `json:"clock_rate"`
}

// Event kinds. Only "fragment" events are media-bearing; the rest document the
// scenario for evidence output.
const (
	EventFragment     = "fragment"
	EventSourceSwitch = "source_switch"
	EventLoss         = "rtp_loss"
)

// Event is a timestamped note in the input list.
type Event struct {
	Kind       string  `json:"kind"`
	Source     string  `json:"source"`
	Path       string  `json:"path,omitempty"`
	Order      int     `json:"order,omitempty"`
	MFHD       *uint32 `json:"mfhd,omitempty"`
	Note       string  `json:"note,omitempty"`
	MediaTicks int64   `json:"media_ticks,omitempty"`
}

// LoadManifest reads and validates a manifest.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, model.E(model.CodeManifest, "load manifest", "%v", err).WithPath(path).WithCause(err)
	}
	return ParseManifest(path, data)
}

// ParseManifest validates manifest bytes.
func ParseManifest(path string, data []byte) (*Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, model.E(model.CodeManifest, "parse manifest", "%v", err).WithPath(path).WithCause(err)
	}
	if err := m.validate(path); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *Manifest) validate(path string) error {
	if m.Version != 1 {
		return model.E(model.CodeManifest, "validate manifest", "unsupported manifest version %d", m.Version).WithPath(path)
	}
	if m.MediaRate.Num <= 0 || m.MediaRate.Den <= 0 {
		return model.E(model.CodeManifest, "validate manifest", "media rate %d/%d is not positive", m.MediaRate.Num, m.MediaRate.Den).WithPath(path)
	}
	if len(m.Sources) == 0 {
		return model.E(model.CodeManifest, "validate manifest", "no sources").WithPath(path)
	}
	seen := map[string]bool{}
	for _, s := range m.Sources {
		if s.ID == "" {
			return model.E(model.CodeManifest, "validate manifest", "source with empty id").WithPath(path)
		}
		if seen[s.ID] {
			return model.E(model.CodeManifest, "validate manifest", "duplicate source id %q", s.ID).WithPath(path)
		}
		seen[s.ID] = true
		for _, f := range s.Fragments {
			if err := checkRelPath(f.Path); err != nil {
				return model.E(model.CodeManifest, "validate manifest", "source %s: %v", s.ID, err).WithPath(path)
			}
		}
		if s.RTPTrace != "" {
			if err := checkRelPath(s.RTPTrace); err != nil {
				return model.E(model.CodeManifest, "validate manifest", "source %s trace: %v", s.ID, err).WithPath(path)
			}
		}
		if s.Init != "" {
			if err := checkRelPath(s.Init); err != nil {
				return model.E(model.CodeManifest, "validate manifest", "source %s init: %v", s.ID, err).WithPath(path)
			}
		}
	}
	for _, e := range m.Events {
		if e.Path != "" {
			if err := checkRelPath(e.Path); err != nil {
				return model.E(model.CodeManifest, "validate manifest", "event: %v", err).WithPath(path)
			}
		}
	}
	return nil
}

// checkRelPath rejects absolute paths, parent escapes, empty paths and NUL.
func checkRelPath(p string) error {
	if p == "" {
		return errString("empty path")
	}
	if filepath.IsAbs(p) {
		return errString("absolute path " + p + " is not allowed")
	}
	clean := filepath.Clean(p)
	if clean == ".." || len(clean) > 2 && clean[:3] == "../" {
		return errString("path " + p + " escapes the work directory")
	}
	if filepath.Base(clean) == "." {
		return errString("path " + p + " does not name a file")
	}
	return nil
}

type errString string

func (e errString) Error() string { return string(e) }

// FragmentsInOrder returns fragments sorted by declared order, failing on
// duplicate order values so output cannot depend on map iteration.
func (s *Source) FragmentsInOrder() ([]FragmentRef, error) {
	out := make([]FragmentRef, len(s.Fragments))
	copy(out, s.Fragments)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Order < out[j-1].Order; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	for i := 1; i < len(out); i++ {
		if out[i].Order == out[i-1].Order {
			return nil, model.E(model.CodeManifest, "order fragments", "source %s has duplicate fragment order %d", s.ID, out[i].Order)
		}
	}
	return out, nil
}

// Digest returns the SHA-256 of the manifest bytes for checkpoint binding.
func DigestBytes(data []byte) [32]byte { return Digest(data) }

// WriteManifest serializes a manifest with stable formatting.
func WriteManifest(w io.Writer, m *Manifest) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}

// WriteManifestFile writes a manifest atomically.
func WriteManifestFile(path string, m *Manifest) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".manifest-*")
	if err != nil {
		return err
	}
	if err := WriteManifest(tmp, m); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
