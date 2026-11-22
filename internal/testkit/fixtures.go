package testkit

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"mediacore/internal/clock"
	"mediacore/internal/repair"
	"mediacore/internal/scan"
)

// rng64 is a deterministic xorshift generator so every fixture is byte
// reproducible from its integer seed. No math/rand global state is used.
type rng64 struct{ state uint64 }

func newRNG(seed uint64) *rng64 {
	if seed == 0 {
		seed = 0x9e3779b97f4a7c15
	}
	return &rng64{state: seed}
}

func (r *rng64) next() uint64 {
	x := r.state
	x ^= x << 13
	x ^= x >> 7
	x ^= x << 17
	r.state = x
	return x
}

// bounded returns a value in [base*(1-jitter), base*(1+jitter)] with integer
// rounding and a guaranteed positive result.
func (r *rng64) bounded(base uint32, jitterPct uint32) uint32 {
	if jitterPct == 0 {
		return base
	}
	span := uint64(base) * uint64(jitterPct) / 100
	off := r.next() % (2*span + 1)
	v := int64(base) - int64(span) + int64(off)
	if v < 1 {
		v = 1
	}
	return uint32(v)
}

// WireSizes builds the logical per-sample network byte sizes such that the
// rounded mean equals the bitrate-derived target exactly: after the jittered
// pass, the residual is folded into the last sample.
func WireSizes(count uint32, timescale, delta uint32, targetBPS uint64, jitterPct uint32, seed uint64) ([]uint32, error) {
	if count == 0 || delta == 0 {
		return nil, clock.ErrInvalidArgument("testkit: count/delta zero")
	}
	// mean sample size in bytes = targetBPS * delta / (8 * timescale)
	mean := targetBPS * uint64(delta) / (8 * uint64(timescale))
	if mean == 0 || mean > math.MaxUint32 {
		return nil, clock.ErrInvalidArgument("testkit: computed mean size %d out of range", mean)
	}
	r := newRNG(seed)
	sizes := make([]uint32, count)
	var total uint64
	for i := range sizes {
		v := r.bounded(uint32(mean), jitterPct)
		sizes[i] = v
		total += uint64(v)
	}
	wantTotal := mean * uint64(count)
	diff := int64(wantTotal) - int64(total)
	last := int64(sizes[count-1]) + diff
	if last <= 0 {
		return nil, clock.ErrInvariantf("testkit.sizes", "residual correction produced %d", last)
	}
	sizes[count-1] = uint32(last)
	return sizes, nil
}

// WriteManifest computes wire sizes, hashes the MP4 and writes the sidecar.
func WriteManifest(spec MP4Spec) (*scan.Manifest, error) {
	sizes, err := WireSizes(spec.SampleCount, spec.Timescale, spec.SampleDelta, spec.TargetBPS, spec.JitterPct, spec.Seed)
	if err != nil {
		return nil, err
	}
	digest, err := scan.DigestFile(spec.Path)
	if err != nil {
		return nil, err
	}
	basis := clock.TimeBase{Timescale: spec.Timescale}
	start, err := basis.ToNanos(spec.MediaStartTicks)
	if err != nil {
		return nil, err
	}
	end, err := basis.ToNanos(spec.MediaStartTicks + int64(spec.SampleCount)*int64(spec.SampleDelta))
	if err != nil {
		return nil, err
	}
	m := &scan.Manifest{
		ManifestVersion: scan.ManifestVersion,
		TrackID:         spec.TrackID,
		Timescale:       spec.Timescale,
		SampleCount:     spec.SampleCount,
		StartNanos:      int64(start),
		EndNanos:        int64(end),
		MediaStartTicks: spec.MediaStartTicks,
		WireSizes:       sizes,
		ForceNonSync:    append([]uint32(nil), spec.ForceNonSync...),
		JitterSeed:      spec.Seed,
		DigestHex:       digest,
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, clock.ErrInvariantf("testkit.manifest", "marshal: %v", err)
	}
	if err := os.WriteFile(spec.Manifest, data, 0o644); err != nil {
		return nil, clock.ErrRecoverablef("testkit.manifest", "write: %v", err)
	}
	return m, nil
}

// WriteRTPSidecar synthesizes the packet stream for repair testing. When
// wrapAt > 0, packet wrapAt's timestamp is placed just below the 32-bit
// boundary so wrapAt+1 naturally wraps via modular uint32 addition. When
// gapAt > 0, one sequence number is skipped there.
func WriteRTPSidecar(path string, trackID, clockRate, count uint32, deltaTS uint32,
	wrapAt, gapAt uint32) error {
	pkts := make([]rtp.Header, count)
	wire := make([]uint32, count)
	var ts uint32 = 1000
	if wrapAt > 0 && wrapAt < count {
		// Position wrapAt near the boundary with room for one delta.
		ts = uint32(0x100000000 - uint64(deltaTS)*uint64(wrapAt+1))
	}
	seq := uint16(0)
	for i := uint32(0); i < count; i++ {
		if i > 0 {
			ts += deltaTS // uint32 addition wraps at 2^32 naturally
		}
		if gapAt > 0 && i == gapAt {
			seq++ // skip one sequence number: a dropped packet
		}
		pkts[i] = rtp.Header{
			Version: 2, PayloadType: 96,
			SequenceNumber: seq, Timestamp: ts, SSRC: 0x4d434f52,
		}
		wire[i] = 1000 + (i % 500)
		seq++
	}
	return repair.WriteSidecar(path, repair.SidecarHeader{
		TrackID: trackID, ClockRate: clockRate, PacketCount: count,
	}, pkts, wire)
}

// FixtureSet groups the on-disk paths produced for one scenario.
type FixtureSet struct {
	Dir      string
	MP4      string
	Manifest string
	RTP      string
	Index    string
	Checkpoint string
	Output   string
}

// PrepareDirs creates the fixture layout under root and returns path helpers.
func PrepareDirs(root, name string) (FixtureSet, error) {
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "cache"), 0o755); err != nil {
		return FixtureSet{}, clock.ErrRecoverablef("testkit.dir", "mkdir: %v", err)
	}
	fs := FixtureSet{Dir: dir}
	fs.MP4 = filepath.Join(dir, "track.mp4")
	fs.Manifest = filepath.Join(dir, "manifest.json")
	fs.RTP = filepath.Join(dir, "packets.rtp")
	fs.Index = filepath.Join(dir, "cache")
	fs.Checkpoint = filepath.Join(dir, "checkpoint.json")
	fs.Output = filepath.Join(dir, "output.bin")
	return fs, nil
}

var _ = context.Background
