// Package fixture generates the deterministic sample data used by make
// fixtures and make recover. All content is formula-based and seeded by the
// bitrate tier only, so regenerating produces byte-identical files and the
// repaired run reproduces the healthy summary.
package fixture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"rtpreplay/internal/rtplog"
	"rtpreplay/media"
)

// Stream geometry constants.
const (
	AudioPackets  = 120
	VideoPackets  = 90
	AudioTickStep = 160 // 20 ms at 8 kHz
	baseUnixMicro = 1_700_000_000_000_000
	AudioSSRC     = 0xA11D1000
	VideoSSRC     = 0x01DE0000
)

// Defect describes one injected damage for the manifest and audits.
type Defect struct {
	File   string `json:"file"`
	Kind   string `json:"kind"`
	Offset int64  `json:"offset"`
	Detail string `json:"detail"`
}

// Manifest is written next to the generated tracks.
type Manifest struct {
	Kind      string   `json:"kind"` // normal | damaged | unrecoverable
	Bitrates  []int    `json:"bitrates"`
	Defects   []Defect `json:"defects"`
	Reference string   `json:"reference,omitempty"`
}

// Spec is one pre-serialization RTP record.
type Spec struct {
	Seq     uint16
	TS      uint32
	Arrival time.Time
	Payload []byte
}

func arrivalAt(i int, stepMS int) time.Time {
	// Deterministic zero-mean micro-jitter in [-2, +2] ms (the four nonzero
	// residues sum to zero), zero every fifth packet.
	var jitterMS int
	switch i % 5 {
	case 1:
		jitterMS = -2
	case 2:
		jitterMS = 1
	case 3:
		jitterMS = 2
	case 4:
		jitterMS = -1
	}
	return time.UnixMicro(baseUnixMicro).Add(
		time.Duration(i*stepMS+jitterMS) * time.Millisecond)
}

// unixBaseTime returns the deterministic fixture epoch as a time.Time.
func unixBaseTime() time.Time { return time.UnixMicro(baseUnixMicro) }

// AudioSpecs builds the pristine audio track for one bitrate tier.
func AudioSpecs(seqStart uint16, tsStart uint32) []Spec {
	out := make([]Spec, AudioPackets)
	for i := 0; i < AudioPackets; i++ {
		payload := audioPayload(i)
		out[i] = Spec{
			Seq:     seqStart + uint16(i),
			TS:      tsStart + uint32(i*AudioTickStep),
			Arrival: arrivalAt(i, 20),
			Payload: payload,
		}
	}
	return out
}

// audioPayload returns 16 content bytes: zero for the two silent passages,
// otherwise a tone byte that repeats every ten packets (i and i+10 match).
func audioPayload(i int) []byte {
	b := make([]byte, 16)
	if (i >= 10 && i <= 19) || (i >= 60 && i <= 69) {
		return b // silence
	}
	tone := byte(40 + (i % 10))
	for k := range b {
		b[k] = tone
	}
	return b
}

// VideoSpecs builds the pristine VFR video track. When wrapNear is true the
// timestamps start below the 32-bit boundary so a rollover occurs mid-stream.
func VideoSpecs(wrapNear bool) []Spec {
	out := make([]Spec, VideoPackets)
	var ts uint32
	if wrapNear {
		ts = uint32(0xFFFFFFFF) - 30000
	}
	firstTS := ts
	for i := 0; i < VideoPackets; i++ {
		step := []uint32{3000, 3000, 4000}[i%3] // VFR: 33.3/33.3/44.4 ms
		if i > 0 {
			ts += step
		}
		b := make([]byte, 8)
		frame := byte(i % 12) // repeats every twelve frames
		for k := range b {
			b[k] = frame
		}
		// Arrival derived exactly from the sender timestamp so the drift
		// estimate stays near zero; integer truncation must not accumulate.
		arrivalUS := baseUnixMicro + int64(ts-firstTS)*1000/int64(media.VideoClockRate/1000)
		out[i] = Spec{
			Seq:     uint16(i),
			TS:      ts,
			Arrival: time.UnixMicro(arrivalUS),
			Payload: b,
		}
	}
	return out
}

// WriteRTPLog serializes specs into the bounded rtplog container.
func WriteRTPLog(path string, specs []Spec, ssrc uint32) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := rtplog.WriteHeader(f); err != nil {
		return err
	}
	for _, s := range specs {
		pkt := marshalPacket(ssrc, s.Seq, s.TS, s.Payload)
		if err := rtplog.WriteRecord(f, s.Arrival, pkt); err != nil {
			return err
		}
	}
	return nil
}

// WriteManifest writes the JSON manifest atomically.
func WriteManifest(dir string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644)
}

// AudioDir / VideoDir / MetaDir are the per-kind subdirectories.
func AudioDir(root string) string { return filepath.Join(root, "audio") }
func VideoDir(root string) string { return filepath.Join(root, "video") }
func MetaDir(root string) string  { return filepath.Join(root, "meta") }

func trackPath(root, kind string, kbps int) string {
	return filepath.Join(root, kind, itoa(kbps)+".rtplog")
}

// Supported bitrates are validated against the sample scope.
func validateBitrates(bitrates []int) error {
	if len(bitrates) == 0 {
		return media.NewError(media.KindBadInput, "NO_BITRATES", "fixture", -1, "at least one bitrate required")
	}
	for _, b := range bitrates {
		if b <= 0 {
			return media.NewError(media.KindBadInput, "BAD_BITRATE", "fixture", -1, "bitrate %d", b)
		}
	}
	return nil
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}
