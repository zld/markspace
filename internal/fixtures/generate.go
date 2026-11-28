// Package fixtures generates the project's self-contained sample data. Every
// byte is derived from the numeric seed and the case name, so a rerun writes
// identical files and the CLI's determinism claim can be checked end to end.
package fixtures

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"medialab/internal/ingest"
	"medialab/internal/model"
	"medialab/internal/pixel"
)

// Case names accepted by the CLI.
const (
	CaseNormal              = "normal"
	CaseCorruptHash         = "corrupt-hash"
	CaseReorderLoss         = "reorder-loss"
	CaseTimestampRegression = "timestamp-regression"
	CaseBudgetConflict      = "budget-conflict"
	CaseCacheBoundary       = "cache-boundary"
)

// Cases lists every generated scenario in stable order.
var Cases = []string{
	CaseNormal, CaseCorruptHash, CaseReorderLoss,
	CaseTimestampRegression, CaseBudgetConflict, CaseCacheBoundary,
}

// Spec describes one generated dataset.
type Spec struct {
	Case       string
	Seed       int64
	Dir        string
	Tracks     int
	FramesEach int
	Width      int
	Height     int
	FPS        int
	// Fragments is the number of RTP fragments per frame.
	Fragments int
}

// DefaultSpec fills unset fields for a case.
func DefaultSpec(caseName string, seed int64, dir string) Spec {
	s := Spec{Case: caseName, Seed: seed, Dir: dir, Tracks: 3, FramesEach: 5,
		Width: 64, Height: 48, FPS: 25, Fragments: 2}
	switch caseName {
	case CaseCorruptHash:
		// A frame whose bytes no longer match its declared digest.
	case CaseReorderLoss:
		s.FramesEach = 6
	case CaseTimestampRegression:
		s.FramesEach = 5
	case CaseBudgetConflict:
		s.FramesEach = 8
	case CaseCacheBoundary:
		s.Width, s.Height = 128, 96
		s.FramesEach = 4
	}
	return s
}

// Dataset is the generated fixture layout.
type Dataset struct {
	Spec     Spec
	Manifest string
	Frames   []model.Frame
	// Packets is the wire RTP stream per track, deliberately shuffled for the
	// reorder case and truncated for the loss case.
	Packets map[model.TrackID][][]byte
	// Note records what the case intends to demonstrate.
	Note string
}

// Generate writes frames, the manifest and RTP streams under spec.Dir.
func Generate(spec Spec) (*Dataset, error) {
	if err := os.MkdirAll(filepath.Join(spec.Dir, "frames"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(spec.Dir, "rtp"), 0o755); err != nil {
		return nil, err
	}
	ds := &Dataset{Spec: spec, Packets: make(map[model.TrackID][][]byte)}
	const frameGapNS = int64(1_000_000_000) / 25 // 25 fps cadence

	for tr := 1; tr <= spec.Tracks; tr++ {
		track := model.TrackID(tr)
		for i := 0; i < spec.FramesEach; i++ {
			seq := uint32(i + 1)
			pts := int64(i) * frameGapNS
			raw := synthImage(spec.Width, spec.Height, spec.Seed, tr, int(seq))
			encoded, err := pixel.Encode(raw, tr, seq)
			if err != nil {
				return nil, err
			}
			rel := filepath.ToSlash(filepath.Join("frames", fmt.Sprintf("t%d-%03d.mlp", tr, seq)))
			if err := os.WriteFile(filepath.Join(spec.Dir, rel), encoded, 0o644); err != nil {
				return nil, err
			}
			digest := hashHex(encoded)
			f := model.Frame{
				Track: track, Sequence: seq, PTS: model.Timestamp(pts),
				Width: spec.Width, Height: spec.Height, Size: len(encoded),
				Color: model.Color{Primaries: "bt709", Transfer: "bt709", Range: "full"},
				Path:  rel, SHA256: digest,
			}
			ds.Frames = append(ds.Frames, f)

			parts := splitPayload(encoded, spec.Fragments)
			for part, chunk := range parts {
				pkt := ingest.Packet{
					Track: track,
					// Sequence numbers stay unique across the whole fixture set
					// so a misrouted packet is detectable.
					Seq:    uint32(1000 + ((tr-1)*spec.FramesEach+i)*spec.Fragments + part),
					RTPTs:  uint32(i * (int(ingest.RTPClockRate) / 25)),
					Marker: part == len(parts)-1,
					// Frame is the track-local position; each track's stream is
					// reordered and assembled independently.
					Frame: i, Parts: len(parts), Part: part, Data: chunk,
				}
				b, err := ingest.MarshalPacket(pkt)
				if err != nil {
					return nil, err
				}
				ds.Packets[track] = append(ds.Packets[track], b)
			}
		}
	}

	switch spec.Case {
	case CaseCorruptHash:
		// Corrupt the declared digest of the second frame of the second track.
		idx := spec.FramesEach + 1
		if idx < len(ds.Frames) {
			bad := ds.Frames[idx].SHA256
			ds.Frames[idx].SHA256 = flipHex(bad)
			ds.Note = fmt.Sprintf("declared SHA-256 of track %d frame %d no longer matches its bytes",
				ds.Frames[idx].Track, ds.Frames[idx].Sequence)
		}
	case CaseReorderLoss:
		for tr := range ds.Packets {
			wire := ds.Packets[tr]
			if spec.Fragments >= 2 && len(wire) > spec.Fragments {
				// Drop the final fragment of the first frame so that sampled
				// output contains a deterministic fill frame.
				wire = append(wire[:spec.Fragments-1:spec.Fragments-1], wire[spec.Fragments:]...)
			}
			// Shuffle within the jitter window.
			for i := 0; i+3 < len(wire); i += 4 {
				wire[i], wire[i+2] = wire[i+2], wire[i]
			}
			ds.Packets[tr] = wire
		}
		ds.Note = "one fragment of each track's first frame dropped and neighbours reordered; missing packets are recoverable"
	case CaseTimestampRegression:
		// Track 2 frame 3 arrives before frame 2, producing a regression in
		// declaration order while the merged output stays sorted.
		if len(ds.Frames) > spec.FramesEach+2 {
			ds.Frames[spec.FramesEach+1].PTS = model.Timestamp(3 * frameGapNS)
			ds.Frames[spec.FramesEach+2].PTS = model.Timestamp(2 * frameGapNS)
		}
		ds.Note = "declared timestamps regress on track 2; merge reports it and still sorts"
	case CaseBudgetConflict:
		// The conflict is imposed by the CLI budget; the data stays valid.
		ds.Note = "valid data; the supplied budget leaves no feasible candidate"
	case CaseCacheBoundary:
		ds.Note = "large synthetic frames sized to press the worker cache cap"
	}

	manifest := ingest.Manifest{Version: 1, EpochNS: 0, Frames: ds.Frames}
	blob, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	ds.Manifest = filepath.Join(spec.Dir, "manifest.json")
	if err := os.WriteFile(ds.Manifest, blob, 0o644); err != nil {
		return nil, err
	}
	for tr, wire := range ds.Packets {
		path := filepath.Join(spec.Dir, "rtp", fmt.Sprintf("track-%d.rtp", tr))
		if err := os.WriteFile(path, packRTP(wire), 0o644); err != nil {
			return nil, err
		}
	}
	return ds, nil
}

// synthImage builds the deterministic synthetic frame for one track/sequence.
func synthImage(w, h int, seed int64, track int, seq int) pixel.Raw {
	r := pixel.Raw{W: w, H: h, Pix: make([]byte, w*h*3)}
	base := int(seed)*17 + track*53 + seq*29
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			// Smooth gradients plus a coarse checker so statistics and edges
			// both vary without randomness at the frame level.
			checker := ((x / 8) + (y / 8) + seq) % 2
			r.Pix[i] = byte((base + x*3 + y + checker*40) % 256)
			r.Pix[i+1] = byte((base/2 + x + y*2 + track*11) % 256)
			r.Pix[i+2] = byte((base/3 + x*2 + y*3 + checker*25) % 256)
		}
	}
	return r
}

func splitPayload(data []byte, n int) [][]byte {
	if n < 1 {
		n = 1
	}
	chunk := (len(data) + n - 1) / n
	if chunk == 0 {
		chunk = 1
	}
	var out [][]byte
	for start := 0; start < len(data); start += chunk {
		end := start + chunk
		if end > len(data) {
			end = len(data)
		}
		out = append(out, data[start:end])
	}
	if len(out) == 0 {
		out = [][]byte{{}}
	}
	return out
}

func hashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// flipHex changes one hex digit, keeping the string well-formed but wrong.
func flipHex(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] == '0' {
		b[0] = '1'
	} else {
		b[0] = '0'
	}
	return string(b)
}

// ReadRTP loads the generated RTP stream for a track.
func ReadRTP(dir string, track model.TrackID) ([]ingest.Packet, error) {
	path := filepath.Join(dir, "rtp", fmt.Sprintf("track-%d.rtp", track))
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []ingest.Packet
	for len(raw) > 0 {
		pkt, n, err := readOne(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, pkt)
		raw = raw[n:]
	}
	return out, nil
}

// readOne consumes one length-prefixed packet from the stream.
func readOne(b []byte) (ingest.Packet, int, error) {
	if len(b) < 4 {
		return ingest.Packet{}, 0, fmt.Errorf("fixtures: truncated RTP record")
	}
	n := int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	if n <= 0 || 4+n > len(b) {
		return ingest.Packet{}, 0, fmt.Errorf("fixtures: RTP record length %d overruns", n)
	}
	pkt, err := ingest.UnmarshalPacket(b[4 : 4+n])
	if err != nil {
		return ingest.Packet{}, 0, err
	}
	return pkt, 4 + n, nil
}

// packRTP writes 4-byte big-endian length-prefixed records so the raw RTP
// byte stream is self-delimiting and parseable without side metadata.
func packRTP(records [][]byte) []byte {
	var out []byte
	for _, r := range records {
		n := len(r)
		out = append(out, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
		out = append(out, r...)
	}
	return out
}
