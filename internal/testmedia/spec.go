package testmedia

// Kind enumerates the supported synthetic track kinds.
type Kind string

const (
	// KindVideo is an AVC-style video track (codec fourCC "avc1").
	KindVideo Kind = "video"
	// KindAudio is an AAC-style audio track (codec fourCC "mp4a").
	KindAudio Kind = "audio"
)

// TrackSpec describes one synthetic track and its compressed samples.
type TrackSpec struct {
	Kind Kind
	// Codec is the sample-entry fourCC. Use an unsupported value (e.g. "zyz1")
	// to exercise codec-compatibility failures.
	Codec string
	// Timescale is the track (mdhd) time base in ticks per second.
	Timescale uint32
	// Samples is the number of compressed samples to generate.
	Samples int
	// SampleSize is the fixed byte size of every sample payload.
	SampleSize int
	// Delta is the constant duration of each sample in track ticks.
	Delta uint32
	// KeyframeEvery marks every n-th video sample a sync sample (0 -> all).
	KeyframeEvery int
	// Seed salts the deterministic payload generator; distinct tracks should
	// use distinct seeds.
	Seed uint64
}

// MediaSpec is a complete two-or-more-track container specification.
type MediaSpec struct {
	// MovieTimescale is the mvhd time base.
	MovieTimescale uint32
	Tracks         []TrackSpec
}

// DefaultSpec returns the canonical healthy fixture used by the demo: an AVC
// video track at 25fps/90kHz and an AAC audio track at 48kHz.
func DefaultSpec(seed uint64) MediaSpec {
	return MediaSpec{
		MovieTimescale: 90000,
		Tracks: []TrackSpec{
			{
				Kind: KindVideo, Codec: "avc1", Timescale: 90000,
				Samples: 48, SampleSize: 160, Delta: 3600,
				KeyframeEvery: 12, Seed: seed + 1,
			},
			{
				Kind: KindAudio, Codec: "mp4a", Timescale: 48000,
				Samples: 96, SampleSize: 64, Delta: 1024,
				KeyframeEvery: 0, Seed: seed + 2,
			},
		},
	}
}

// SampleBytes deterministically derives the compressed bytes of one sample.
// The generator is a splitmix64 stream seeded by (trackSeed, sampleIndex), so
// bytes never repeat accidentally and remain identical on every machine.
func SampleBytes(trackSeed uint64, sampleIndex, size int) []byte {
	out := make([]byte, size)
	state := trackSeed ^ (uint64(sampleIndex)*0x9e3779b97f4a7c15 + 1)
	for i := 0; i < size; i++ {
		// splitmix64
		state += 0x9e3779b97f4a7c15
		z := state
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		z ^= z >> 31
		out[i] = byte(z >> 24)
	}
	return out
}
