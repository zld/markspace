// Package demo wires the reusable media kernel into one deterministic
// end-to-end run: it generates manifests and RTP fixtures from a seed,
// injects configurable link faults, recovers and synthesizes the timeline,
// exports off-screen frames and reconciles everything through the reporter.
package demo

import (
	"math/rand"

	"github.com/medialab/medialab/internal/manifest"
	"github.com/medialab/medialab/internal/rtprecover"
	"github.com/pion/rtp"
)

// tickStep is the simulation cadence: one RTP packet per track per 100 ms.
const tickStep int64 = 100

// Fault selects a self-generated corruption scenario.
type Fault string

const (
	FaultNone        Fault = ""
	FaultTruncated   Fault = "truncated"
	FaultChecksum    Fault = "checksum"
	FaultLate        Fault = "late"
	FaultBurstLoss   Fault = "burst"
	FaultBandwidth   Fault = "bandwidth"
	FaultRender      Fault = "render"
	FaultComposite   Fault = "corrupt"
)

// Config is the full demo parameter set mapped from CLI flags.
type Config struct {
	Seed           int64
	DurationSec    int64
	Tracks         int
	LossBurst      int
	WindowCapacity int
	QueueCapacity  int
	TargetBps      int64
	OutDir         string
	Fault          Fault
}

// Fixture carries every generated input for one run.
type Fixture struct {
	Manifest *manifest.Manifest
	Packets  map[string][]*rtp.Packet // track id -> source-order packets
	// BandwidthTrace is indexed by simulation chunk (one sample per chunk).
	BandwidthTrace []int64
	// DroppedSeq records source packets withheld to simulate burst loss.
	DroppedSeq map[string]map[uint16]bool
	// LateSeq records packets pushed far outside the reorder window.
	LateSeq map[string]map[uint16]bool
}

// BuildFixture generates the manifest, packets and bandwidth trace purely
// from cfg.Seed, so repeated runs produce byte-identical inputs.
func BuildFixture(cfg Config) *Fixture {
	rng := rand.New(rand.NewSource(cfg.Seed))
	durationMs := cfg.DurationSec * 1000
	m := buildManifest(cfg, durationMs)

	fx := &Fixture{
		Manifest:     m,
		Packets:      map[string][]*rtp.Packet{},
		DroppedSeq:   map[string]map[uint16]bool{},
		LateSeq:      map[string]map[uint16]bool{},
	}
	for i := range m.Tracks {
		tr := &m.Tracks[i]
		pkts, dropped, late := buildTrackPackets(cfg, tr, rng)
		fx.Packets[tr.ID] = pkts
		if len(dropped) > 0 {
			fx.DroppedSeq[tr.ID] = dropped
		}
		if len(late) > 0 {
			fx.LateSeq[tr.ID] = late
		}
	}
	fx.BandwidthTrace = buildBandwidthTrace(cfg, rng, durationMs)
	return fx
}

func buildManifest(cfg Config, durationMs int64) *manifest.Manifest {
	tracks := make([]manifest.TrackSpec, 0, cfg.Tracks)
	var edits []manifest.Edit
	// Non-zero source start: every track begins one hour after its epoch.
	const sourceStartMs int64 = 3_600_000
	for i := 0; i < cfg.Tracks; i++ {
		id := trackID(i)
		kind := manifest.KindAudio
		var rate uint32 = 48_000
		var keyframes []int64
		if i == 0 {
			kind = manifest.KindVideo
			rate = 90_000
			// Sparse keyframes every 5 seconds, plus the mandatory first one.
			for k := int64(0); k < durationMs; k += 5_000 {
				keyframes = append(keyframes, k)
			}
		}
		tracks = append(tracks, manifest.TrackSpec{
			ID: id, Kind: kind, ClockRate: rate,
			SSRC: 1000 + uint32(i), StartMs: sourceStartMs,
			DurationMs: durationMs, Keyframes: keyframes,
		})
		edits = append(edits, manifest.Edit{
			Track: id, SourceStartMs: 0, TimelineStartMs: 0,
			DurationMs: durationMs,
		})
	}
	return &manifest.Manifest{
		Version: 1, Seed: cfg.Seed, TickHz: 1000,
		Tracks: tracks, Edits: edits,
	}
}

func buildTrackPackets(cfg Config, tr *manifest.TrackSpec, rng *rand.Rand) (
	[]*rtp.Packet, map[uint16]bool, map[uint16]bool,
) {
	count := int(tr.DurationMs / tickStep)
	pkts := make([]*rtp.Packet, 0, count)
	dropped := map[uint16]bool{}
	late := map[uint16]bool{}
	keyframeAt := map[int64]bool{}
	for _, k := range tr.Keyframes {
		keyframeAt[k/tickStep] = true
	}
	video := tr.Kind == manifest.KindVideo
	burst := cfg.LossBurst
	if burst <= 0 {
		burst = 1
	}
	// Deterministic fault placement: first burst on the video track mid-run.
	burstStart := count / 2
	for n := 0; n < count; n++ {
		seq := uint16(n)
		if injectBurst(cfg.Fault, video, n, burstStart, burst) {
			dropped[seq] = true
			continue
		}
		flags := byte(0)
		if video {
			flags |= rtprecover.FlagVideo
			if keyframeAt[int64(n)] {
				flags |= rtprecover.FlagKeyframe
			}
		} else {
			flags |= rtprecover.FlagAudio
		}
		pkts = append(pkts, buildPacket(tr, seq, n, flags))
		if injectLate(cfg.Fault, video, n) {
			late[seq] = true
		}
	}
	// A small, deterministic set of benign duplicates/reorderings for the
	// healthy path; corruption scenarios add their own heavier faults.
	pkts = applyLinkNoise(cfg.Fault, pkts, tr, rng, video)
	return pkts, dropped, late
}

func buildPacket(tr *manifest.TrackSpec, seq uint16, n int, flags byte) *rtp.Packet {
	body := []byte{byte(n >> 8), byte(n)}
	env := rtprecover.Envelope{Flags: flags, Body: body}
	ts := uint32(int64(n)*tickStep*int64(tr.ClockRate) / 1000)
	return &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 96, SequenceNumber: seq,
			Timestamp: ts, SSRC: tr.SSRC,
		},
		Payload: env.Marshal(),
	}
}

// applyLinkNoise introduces bounded, deterministic reordering. Only adjacent
// packets are swapped and the swap distance stays at one position, which the
// reorder window always absorbs; no packet is ever displaced beyond it.
// Duplicate packets are generated separately by the feeder so their seq
// bookkeeping stays independent of packet-slice indexing.
func applyLinkNoise(fault Fault, pkts []*rtp.Packet, tr *manifest.TrackSpec,
	rng *rand.Rand, video bool,
) []*rtp.Packet {
	out := append([]*rtp.Packet(nil), pkts...)
	swaps := 0
	for i := 1; i < len(out) && swaps < 4; i++ {
		if i%40 == 0 {
			out[i], out[i-1] = out[i-1], out[i]
			swaps++
		}
	}
	return out
}

func buildBandwidthTrace(cfg Config, rng *rand.Rand, durationMs int64) []int64 {
	chunks := int(durationMs / tickStep / 10) // one sample per second
	trace := make([]int64, chunks)
	base := cfg.TargetBps
	for i := range trace {
		// +/-10% jitter around the configured target.
		jitter := 1.0 + (rng.Float64()*0.2 - 0.1)
		trace[i] = int64(float64(base) * jitter)
	}
	if cfg.Fault == FaultBandwidth || cfg.Fault == FaultComposite {
		// Hard drop to zero across the middle quarter of the run.
		lo, hi := chunks/2, chunks*3/4
		for i := lo; i < hi; i++ {
			trace[i] = 0
		}
	}
	return trace
}

func injectBurst(fault Fault, video bool, n, start, burst int) bool {
	if !video {
		return false
	}
	switch fault {
	case FaultBurstLoss, FaultComposite, FaultChecksum:
		return n >= start && n < start+burst
	default:
		return false
	}
}

func injectLate(fault Fault, video bool, n int) bool {
	if !video {
		return false
	}
	// Only the dedicated late case delays packets; the composite case keeps
	// its own fault set (burst, checksum, bandwidth, render) so late drops
	// stay attributable to one cause.
	if fault == FaultLate {
		return n == 25 || n == 26
	}
	return false
}

func trackID(i int) string {
	if i == 0 {
		return "v0"
	}
	return "a" + string(rune('0'+i-1))
}
