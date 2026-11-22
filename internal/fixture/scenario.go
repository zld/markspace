package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"

	"github.com/pion/rtp"
)

// ContentSpec describes synthetic content that is both written as MP4 and
// streamed as RTP, so container-derived boundaries and network-derived event
// times stay comparable.
type ContentSpec struct {
	DurationMS int64
	SegmentMS  int64
	// Bitrates is the list of video representations in kbps.
	Bitrates []int
	// KeyframeMS is the GOP length shared by all representations.
	KeyframeMS int64
	// FrameRate is the video frame rate in frames per second.
	FrameRate int
	// AudioRate is audio samples per second.
	AudioRate int
	// AudioFrameMS is the audio packetization interval.
	AudioFrameMS int64
	Seed         int64
}

// VideoTrackSpecs expands ContentSpec into one video track per bitrate. Every
// track has keyframes on the same instants, as a real encoder would, so the
// aligner has common boundaries to work with. Bitrate changes sample sizes
// only.
func (c ContentSpec) VideoTrackSpecs() []MovieSpec {
	// Timestamps are derived from exact 90 kHz ticks, never rounded
	// milliseconds, so a keyframe lands precisely on every GOP boundary.
	const timescale = uint32(90000)
	frameTicks := timescale / uint32(c.FrameRate)
	if frameTicks == 0 {
		frameTicks = 1
	}
	gop := int64(c.KeyframeMS) * int64(c.FrameRate) / 1000
	if gop <= 0 {
		gop = 1
	}
	// Cover the requested duration, rounded up to whole frames.
	durationTicks := uint64(c.DurationMS) * uint64(timescale) / 1000
	frameCount := int(durationTicks/uint64(frameTicks)) + 1
	bytesPerFrame := func(kbps int) uint32 {
		bits := int64(kbps) * 1000 * int64(frameTicks) / int64(timescale)
		size := bits / 8 / 2 // halve to model compressible payload
		if size < 1 {
			size = 1
		}
		if size > 1<<20 {
			size = 1 << 20
		}
		return uint32(size)
	}
	specs := make([]MovieSpec, 0, len(c.Bitrates))
	for i, kbps := range c.Bitrates {
		tr := TrackSpec{
			ID:        uint32(i + 1),
			Handler:   "vide",
			Timescale: timescale,
			Width:     640,
			Height:    360,
			Seed:      uint64(c.Seed)*100 + uint64(i),
		}
		for frame := 0; frame < frameCount; frame++ {
			tr.Samples = append(tr.Samples, Sample{
				Delta: frameTicks,
				Size:  bytesPerFrame(kbps),
				Sync:  int64(frame)%gop == 0,
			})
		}
		specs = append(specs, MovieSpec{
			Brand:     "isom",
			Timescale: 1000,
			Tracks:    []TrackSpec{tr},
		})
	}
	return specs
}

// AudioTrackSpec is a 48 kHz audio track covering the same duration.
func (c ContentSpec) AudioTrackSpec() TrackSpec {
	frameSamples := uint32(48) * uint32(c.AudioFrameMS/1000)
	if frameSamples == 0 {
		frameSamples = 1024
	}
	tr := TrackSpec{ID: uint32(len(c.Bitrates) + 1), Handler: "soun", Timescale: 48000, Seed: uint64(c.Seed) + 999}
	var covered int64
	for covered < c.DurationMS {
		tr.Samples = append(tr.Samples, Sample{Delta: frameSamples, Size: 60, Sync: true})
		covered += int64(frameSamples) * 1000 / 48000
	}
	return tr
}

// RepresentationName renders a bitrate as the manifest's representation name.
func RepresentationName(kbps int) string {
	// fmt-free for deterministic ordering; callers format as needed.
	return itoa(kbps) + "kbps"
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// NetworkSpec parameterises the synthetic RTP channel.
type NetworkSpec struct {
	Packets     int
	Loss        float64 // independent loss probability
	BurstLoss   int     // length of the injected burst at BurstAt
	BurstAt     int     // packet index where the burst starts
	JitterMS    float64
	Reorder     int // how many adjacent packets may swap
	StartSeq    uint16
	PayloadType uint8
	// TimestampStep advances the RTP timestamp per packet, in 90 kHz ticks.
	// Zero means 3000 (one 30 fps frame).
	TimestampStep uint32
	// DeadlineMS is the nominal per-packet network deadline used to measure
	// latency; zero means 20ms.
	DeadlineMS int64
	Seed       int64
}

// PatchBoxUint32 finds the first box of typ and overwrites the big-endian
// uint32 at payloadOffset (relative to the box type field) with value. It is
// used by fault fixtures and returns an error instead of panicking so tests
// and the verify command can report a located failure.
func PatchBoxUint32(data []byte, typ string, payloadOffset int, value uint32) ([]byte, error) {
	idx := bytes.Index(data, []byte(typ))
	if idx < 0 || len(typ) != 4 {
		return nil, fmt.Errorf("fixture: box %q not found", typ)
	}
	out := append([]byte(nil), data...)
	pos := idx + payloadOffset
	if pos+4 > len(out) {
		return nil, fmt.Errorf("fixture: patch offset %d beyond %d bytes", pos, len(out))
	}
	binary.BigEndian.PutUint32(out[pos:], value)
	return out, nil
}

// PatchChunkOffsets rewrites every stco chunk offset to the first one, so all
// chunks claim the same start and their samples overlap.
func PatchChunkOffsets(data []byte, chunkSize int) ([]byte, error) {
	idx := bytes.Index(data, []byte("stco"))
	if idx < 0 {
		return nil, fmt.Errorf("fixture: stco not found")
	}
	out := append([]byte(nil), data...)
	base := idx + 12 // type(4) + version/flags(4) + entry_count(4)
	if base+chunkSize > len(out) {
		return nil, fmt.Errorf("fixture: stco truncated at %d", len(out))
	}
	first := binary.BigEndian.Uint32(out[base:])
	for p := base + chunkSize; p+4 <= len(out); p += chunkSize {
		binary.BigEndian.PutUint32(out[p:], first)
	}
	return out, nil
}

// PatchChunkOffsetsReversed reverses the stco chunk offsets, so later samples
// point earlier in the file than earlier ones: the byte layout contradicts
// decode order.
func PatchChunkOffsetsReversed(data []byte) ([]byte, error) {
	idx := bytes.Index(data, []byte("stco"))
	if idx < 0 {
		return nil, fmt.Errorf("fixture: stco not found")
	}
	out := append([]byte(nil), data...)
	base := idx + 8 // type(4) + version/flags(4)
	if base+8 > len(out) {
		return nil, fmt.Errorf("fixture: stco truncated")
	}
	count := int(binary.BigEndian.Uint32(out[base:]))
	start := base + 4
	if start+count*4 > len(out) {
		return nil, fmt.Errorf("fixture: stco declares %d offsets beyond file", count)
	}
	for i := 0; i < count/2; i++ {
		a := start + i*4
		b := start + (count-1-i)*4
		var va, vb [4]byte
		copy(va[:], out[a:a+4])
		copy(vb[:], out[b:b+4])
		copy(out[a:a+4], vb[:])
		copy(out[b:b+4], va[:])
	}
	return out, nil
}

// NetworkTrace is the outcome of simulating a channel.
type NetworkTrace struct {
	Packets  []*rtp.Packet
	Arrivals []int64 // simulated arrival time in ms, parallel to Packets
	// Deadlines is the nominal arrival time in ms per packet.
	Deadlines []int64
	// Dropped lists sequence numbers the channel discarded.
	Dropped []uint16
}

// SimulateNetwork produces a deterministic loss/reorder/jitter trace. Arrival
// order is deliberately shuffled relative to sequence order.
func SimulateNetwork(spec NetworkSpec) NetworkTrace {
	rng := rand.New(rand.NewSource(spec.Seed))
	type slot struct {
		pkt      *rtp.Packet
		arrival  int64
		deadline int64
		order    int
	}
	if spec.TimestampStep == 0 {
		spec.TimestampStep = 3000
	}
	if spec.DeadlineMS == 0 {
		spec.DeadlineMS = 20
	}
	var slots []slot
	var dropped []uint16
	for i := 0; i < spec.Packets; i++ {
		seq := spec.StartSeq + uint16(i)
		inBurst := spec.BurstLoss > 0 && i >= spec.BurstAt && i < spec.BurstAt+spec.BurstLoss
		if inBurst || rng.Float64() < spec.Loss {
			dropped = append(dropped, seq)
			continue
		}
		jitter := rng.Int63n(int64(spec.JitterMS*2)+1) - int64(spec.JitterMS)
		arrival := int64(i)*spec.DeadlineMS + jitter
		if arrival < 0 {
			arrival = 0
		}
		deadline := int64(i) * spec.DeadlineMS
		slots = append(slots, slot{
			pkt: &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					SequenceNumber: seq,
					Timestamp:      uint32(i) * spec.TimestampStep,
					PayloadType:    spec.PayloadType,
					SSRC:           0x0badcafe,
				},
				Payload: make([]byte, 32),
			},
			arrival:  arrival,
			deadline: deadline,
			order:    i,
		})
	}
	// Swap neighbours to model reordering inside the window.
	for k := 0; k < spec.Reorder && k+1 < len(slots); k++ {
		idx := rng.Intn(len(slots) - 1)
		slots[idx], slots[idx+1] = slots[idx+1], slots[idx]
	}
	// Stable sort by arrival; equal arrivals keep trace order.
	for i := 1; i < len(slots); i++ {
		for j := i; j > 0 && slots[j].arrival < slots[j-1].arrival; j-- {
			slots[j], slots[j-1] = slots[j-1], slots[j]
		}
	}
	trace := NetworkTrace{Dropped: dropped}
	for _, s := range slots {
		trace.Packets = append(trace.Packets, s.pkt)
		trace.Arrivals = append(trace.Arrivals, s.arrival)
		trace.Deadlines = append(trace.Deadlines, s.deadline)
	}
	return trace
}
