// Package fixture generates self-contained CMAF/fMP4 and RTP sample data
// for tests and the demo. No external files are referenced; every byte is
// produced deterministically from a seed so runs are reproducible.
package fixture

import (
	"math/rand"

	"mediatool/internal/mp4"
	clockrtp "mediatool/internal/rtp"

	pionrtp "github.com/pion/rtp"
)

// VideoTrack and AudioTrack describe the canonical demo configuration.
func VideoTrack() TrackSpec {
	return TrackSpec{TrackID: 1, Timescale: 90000, MediaType: "video",
		CodecKey: "avc1", SampleDuration: 3003, VariableRate: true}
}

// AudioTrack mirrors VideoTrack for the audio stream.
func AudioTrack() TrackSpec {
	return TrackSpec{TrackID: 2, Timescale: 48000, MediaType: "audio",
		CodecKey: "mp4a", SampleDuration: 1024}
}

// Generate builds a group with n fragments per track. lateAt is the
// 0-based input order at which one video fragment is deliberately moved
// behind the watermark; pass -1 to disable the late-delivery scenario.
func Generate(seed int64, n int, lateAt int) *Group {
	if n < 1 {
		n = 1
	}
	rng := rand.New(rand.NewSource(seed))
	video := VideoTrack()
	audio := AudioTrack()
	tracks := []mp4.TrackInfo{
		{TrackID: video.TrackID, Timescale: video.Timescale,
			MediaType: video.MediaType, CodecKey: video.CodecKey},
		{TrackID: audio.TrackID, Timescale: audio.Timescale,
			MediaType: audio.MediaType, CodecKey: audio.CodecKey},
	}
	g := &Group{
		Seed:           seed,
		Init:           mp4.BuildInit(tracks),
		Tracks:         tracks,
		LateAt:         lateAt,
		VideoTargetBPS: VideoTargetBPS,
		AudioTargetBPS: AudioTargetBPS,
	}
	g.VideoFrags = genAlignedTrack(rng, video, n, VideoTargetBPS)
	g.AudioFrags = genAlignedTrack(rng, audio, n, AudioTargetBPS)
	g.Corrupted = genCorrupt(video)
	g.RTPVideo, g.RTPEvents = genRTP(rng, n)
	return g
}

// fragmentDuration returns the exact track-timescale duration of one grid
// fragment. Both 90 kHz and 48 kHz represent GridMS without rounding.
func fragmentDuration(timescale uint32) uint64 {
	return uint64(timescale) * GridMS / 1000
}

// genAlignedTrack emits n fragments, each spanning the same GridMS window,
// so the video and audio streams stay sample-aligned. Video splits its
// window into a variable number of variable-duration samples (VFR) while
// keeping the window total exact; audio uses fixed 1024-unit frames.
// Compressed payload size targets targetBPS so actual bitrate tracks the
// target within a tiny deterministic jitter.
func genAlignedTrack(rng *rand.Rand, spec TrackSpec, n, targetBPS int) []Fragment {
	out := make([]Fragment, 0, n)
	// Bytes per grid window for the requested bitrate: bps/8 * GridMS/1000.
	bytesPerWindow := targetBPS / 8 * GridMS / 1000
	dts := uint64(0)
	for i := 0; i < n; i++ {
		var durations []uint32
		windowTotal := fragmentDuration(spec.Timescale)
		switch {
		case spec.VariableRate:
			// 2..4 samples whose durations sum to the exact window total.
			sampleCount := 2 + rng.Intn(3)
			durations = make([]uint32, sampleCount)
			rest := windowTotal
			for s := 0; s < sampleCount; s++ {
				remainingSamples := sampleCount - s
				if s == sampleCount-1 {
					durations[s] = uint32(rest)
					break
				}
				// Aim for an even split with +/-15% deterministic jitter.
				even := rest / uint64(remainingSamples)
				lo := even - even*15/100
				hi := even + even*15/100
				if lo < 1 {
					lo = 1
				}
				if hi > rest-uint64(remainingSamples-1) {
					hi = rest - uint64(remainingSamples-1)
				}
				d := lo
				if hi > lo {
					d = lo + uint64(rng.Intn(int(hi-lo+1)))
				}
				durations[s] = uint32(d)
				rest -= d
			}
		default:
			// Fixed-size audio frames covering the exact window.
			frames := int(windowTotal / uint64(spec.SampleDuration))
			durations = make([]uint32, frames)
			for s := range durations {
				durations[s] = spec.SampleDuration
			}
		}

		// Distribute the window's target payload bytes across samples with
		// a +/-3% jitter, reconciling the remainder on the last sample so
		// every fragment totals exactly bytesPerWindow (min 1 byte each).
		sizes := make([]uint32, len(durations))
		remaining := bytesPerWindow
		for s := range sizes {
			even := remaining / (len(sizes) - s)
			if s == len(sizes)-1 {
				sizes[s] = uint32(remaining)
				break
			}
			lo := even * 97 / 100
			hi := even * 103 / 100
			if lo < 1 {
				lo = 1
			}
			maxKeep := remaining - (len(sizes) - s - 1)
			if hi > maxKeep {
				hi = maxKeep
			}
			size := lo
			if hi > lo {
				size = lo + rng.Intn(hi-lo+1)
			}
			sizes[s] = uint32(size)
			remaining -= size
		}

		payload := make([]byte, bytesPerWindow)
		rng.Read(payload) // deterministic compressed-looking bytes

		fragSpec := mp4.FragmentSpec{
			Seq:             i,
			TrackID:         spec.TrackID,
			BaseMediaDecode: dts,
			SampleDurations: durations,
			SampleSizes:     sizes,
			SamplePayload:   payload,
		}
		out = append(out, Fragment{
			Seq: i, TrackID: spec.TrackID, DTS: dts,
			Bytes:     mp4.BuildFragment(fragSpec),
			Durations: durations, Sizes: sizes, Payload: payload,
		})
		dts += windowTotal
	}
	return out
}

// genCorrupt builds parser-rejection fixtures.
func genCorrupt(spec TrackSpec) []CorruptCase {
	return []CorruptCase{
		{
			Name:  "truncated_header",
			Bytes: []byte{0x00, 0x00, 0x00},
			Class: "box_truncated",
		},
		{
			Name: "illegal_length",
			Bytes: append([]byte{0x00, 0x00, 0x00, 0x05},
				[]byte("moof")...),
			Class: "box_length",
		},
		{
			Name: "truncated_moof_payload",
			Bytes: append(
				mp4.BuildBox("moof", mp4.BuildMfhd(1))[:12],
				0xAA, 0xBB),
			Class: "box_truncated",
		},
	}
}

// genRTP builds a 90 kHz RTP stream containing one legal 32-bit wrap and
// paired wall-clock observations for drift estimation.
func genRTP(rng *rand.Rand, n int) ([]*pionrtp.Packet, []clockrtp.Observation) {
	pkts := make([]*pionrtp.Packet, n)
	events := make([]clockrtp.Observation, n)
	ts := uint32(0xFFFFFFF0) // close to the 32-bit boundary
	for i := 0; i < n; i++ {
		pkts[i] = &pionrtp.Packet{
			Header: pionrtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(i),
				Timestamp:      ts,
				SSRC:           1,
			},
			Payload: []byte{byte(i)},
		}
		events[i] = clockrtp.Observation{
			WallMS:    int64(i) * 100,
			MediaTime: uint64(ts),
		}
		ts += 9000 // 100 ms of a 90 kHz clock; wraps naturally
	}
	return pkts, events
}

// ReorderLate moves fragment at lateAt to the end so it arrives behind the
// watermark. It returns a new slice without mutating the original.
func ReorderLate(in []Fragment, lateAt int) []Fragment {
	if lateAt < 0 || lateAt >= len(in) {
		return in
	}
	out := make([]Fragment, 0, len(in))
	out = append(out, in[:lateAt]...)
	out = append(out, in[lateAt+1:]...)
	out = append(out, in[lateAt])
	return out
}

// RollbackTfdt rewrites the tfdt base media decode time of a valid
// fragment backwards, simulating a device restart. The returned bytes
// remain structurally valid; only the timestamp value changes.
func RollbackTfdt(f Fragment, newBase uint64) []byte {
	spec := mp4.FragmentSpec{
		Seq:             f.Seq,
		TrackID:         f.TrackID,
		BaseMediaDecode: newBase,
		SampleDurations: f.Durations,
		SampleSizes:     f.Sizes,
		SamplePayload:   f.Payload,
	}
	return mp4.BuildFragment(spec)
}
