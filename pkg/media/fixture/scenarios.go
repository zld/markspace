package fixture

import (
	"encoding/json"
)

// RepSpec defines one rendition's clock geometry and playlist attributes.
type RepSpec struct {
	Name       string
	Timescale  uint32
	Bandwidth  int
	Resolution string
	Codecs     string
	IsAudio    bool
	// Duration is the per-fragment duration in track timescale (200 ms).
	Duration uint32
}

// Reps is the fixed multi-bitrate ladder: every rendition places fragment
// boundaries on the same 200 ms grid, so a healthy run reports zero
// cross-rendition boundary mismatches.
var Reps = []RepSpec{
	{Name: "video_90000", Timescale: 90000, Bandwidth: 1200000, Resolution: "1280x720", Codecs: "avc1.64001f", Duration: 18000},
	{Name: "video_48000", Timescale: 48000, Bandwidth: 600000, Resolution: "640x360", Codecs: "avc1.4d401e", Duration: 9600},
	{Name: "audio_48000", Timescale: 48000, Bandwidth: 128000, Codecs: "mp4a.40.2", IsAudio: true, Duration: 9600},
}

// Dataset names.
const (
	DSClean  = "clean"
	DSEdge   = "edge"
	DSFaults = "faults"
)

// Fault injection points on the first representation (1-based fragment seq).
const (
	faultMfhdGapSeq   = FragmentsPerRep // last fragment: jump 24 -> 26, no dup
	faultTfdtRollback = 12
	faultMdatTruncate = 19
	mdatDropBytes     = 60
)

// AnchorRec is one representation's RTP clock anchor and timestamp stream.
type AnchorRec struct {
	ClockHz        uint32   `json:"clock_hz"`
	AnchorRTP      uint64   `json:"anchor_rtp"`
	AnchorSharedMs int64    `json:"anchor_shared_ms"`
	MediaToRTP     int64    `json:"media_to_rtp"`
	Timestamps     []uint32 `json:"timestamps"`
}

// AnchorFile is the sidecar mapping each representation to its publish clock.
type AnchorFile struct {
	Reps map[string]AnchorRec `json:"reps"`
}

// fragmentBytes builds the healthy fragment for rep r at 0-based index idx.
func fragmentBytes(r RepSpec, idx int) []byte {
	seq := uint32(idx + 1)
	tfdt := uint64(idx) * uint64(r.Duration)
	return mediaFragment(fragmentSpec{
		sequence:  seq,
		tfdt:      tfdt,
		tfdtVer:   1,
		duration:  r.Duration,
		size:      120,
		syncFlags: SyncFlags,
	})
}

// repFragments returns the (possibly fault-injected) fragment payloads for a
// representation in a given dataset.
func repFragments(dataset string, repIdx int, r RepSpec) [][]byte {
	out := make([][]byte, FragmentsPerRep)
	for i := 0; i < FragmentsPerRep; i++ {
		out[i] = fragmentBytes(r, i)
	}
	// Faults are injected only into the first representation.
	if dataset == DSFaults && repIdx == 0 {
		injectFaults(out, r)
	}
	return out
}

// injectFaults applies the three independently locatable defects.
func injectFaults(frags [][]byte, r RepSpec) {
	// 1) mfhd sequence gap on the final fragment (24 -> 26).
	patchMfhdSequence(frags[faultMfhdGapSeq-1], faultMfhdGapSeq+2)

	// 2) tfdt rollback: force fragment 12's base time backwards.
	rollbackIdx := faultTfdtRollback - 1
	patchTfdtValue(frags[rollbackIdx], 1000)

	// 3) mdat truncation on fragment 19: drop trailing payload bytes.
	truncIdx := faultMdatTruncate - 1
	frags[truncIdx] = truncateMdat(frags[truncIdx], mdatDropBytes)
}

// buildAnchors synthesizes per-representation RTP anchors. The edge dataset
// starts the first representation near the 32-bit ceiling so its timestamp
// stream wraps exactly once.
func buildAnchors(dataset string) AnchorFile {
	af := AnchorFile{Reps: map[string]AnchorRec{}}
	for ri, r := range Reps {
		rec := AnchorRec{ClockHz: r.Timescale}
		// Default: RTP clock equals media clock, both starting at zero.
		var base uint64
		if dataset == DSEdge && ri == 0 {
			// Place the origin 100000 ticks below the wrap point; the first
			// representation steps 18000 ticks/fragment, so it crosses 2^32
			// within the first few fragments, exactly once.
			base = (uint64(1) << 32) - 100000
		}
		rec.AnchorRTP = base
		rec.MediaToRTP = int64(base)
		rec.Timestamps = make([]uint32, FragmentsPerRep)
		for i := 0; i < FragmentsPerRep; i++ {
			media := uint64(i) * uint64(r.Duration)
			rec.Timestamps[i] = uint32((base + media) & 0xFFFFFFFF)
		}
		af.Reps[r.Name] = rec
	}
	return af
}

// marshalAnchors serializes the anchor sidecar deterministically.
func marshalAnchors(af AnchorFile) ([]byte, error) {
	return json.MarshalIndent(af, "", "  ")
}

// unmarshalAnchors parses an anchor sidecar.
func unmarshalAnchors(b []byte, af *AnchorFile) error {
	return json.Unmarshal(b, af)
}
