package media

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
)

// FixtureTrack describes one synthetic track.
type FixtureTrack struct {
	ID        uint32
	Codec     string // "avc1" or "mp4a"
	Lang      string // 3-letter ISO-639-2/T
	Timescale uint32
	Samples   int
	SyncEvery int // every Nth sample is a sync sample; 0 means all
	Edits     []EditEntry
	Width     uint16
	Height    uint16
	Channels  uint16
	Rate      uint32
}

// FixtureOptions controls deterministic sample file generation.
type FixtureOptions struct {
	Brand     string // "isom" (default) or "qt  "
	MovieTS   uint32
	Tracks    []FixtureTrack
	Seed      int64
	SampleMin int // minimum sample payload bytes (default 64)
	SampleMax int // maximum sample payload bytes (default 512)
}

// Fixture is a generated file plus the ground truth about it.
type Fixture struct {
	Bytes       []byte
	MovieTS     uint32
	Tracks      []FixtureTrack
	TrackSample [][][]byte // per track, per sample payload (decode order)
	SyncCount   map[uint32]int
	TotalBytes  int
}

// BuildFixture generates a valid, deterministic MP4/MOV file. Sample payloads
// are reproducible from (seed, track id, sample index) so a test can corrupt a
// single byte and know exactly which sample was affected.
func BuildFixture(opts FixtureOptions) (*Fixture, error) {
	if len(opts.Tracks) == 0 {
		return nil, NewError(ClassInvalidArgument, "fixture", 0, "no tracks")
	}
	if opts.MovieTS == 0 {
		opts.MovieTS = 1000
	}
	if opts.Brand == "" {
		opts.Brand = "isom"
	}
	if opts.SampleMin <= 0 {
		opts.SampleMin = 64
	}
	if opts.SampleMax < opts.SampleMin {
		opts.SampleMax = opts.SampleMin
	}
	if opts.SampleMax > 64*1024 {
		return nil, NewError(ClassInvalidArgument, "fixture", 0, "sample max too large")
	}

	fx := &Fixture{MovieTS: opts.MovieTS, Tracks: opts.Tracks, SyncCount: map[uint32]int{}}
	payloads := make([]*trackPayload, len(opts.Tracks))
	samples := make([][][]byte, len(opts.Tracks))
	seenIDs := map[uint32]bool{}
	for ti, t := range opts.Tracks {
		if t.ID == 0 || seenIDs[t.ID] {
			return nil, NewError(ClassMappingConflict, "fixture", 0, "duplicate or zero track id %d", t.ID)
		}
		seenIDs[t.ID] = true
		if t.Samples <= 0 {
			return nil, NewError(ClassInvalidArgument, "fixture", 0, "track %d has no samples", t.ID)
		}
		if t.Timescale == 0 {
			return nil, NewError(ClassTimebase, "fixture", 0, "track %d timescale", t.ID)
		}
		lang := [3]byte{'u', 'n', 'd'}
		if len(t.Lang) == 3 {
			copy(lang[:], t.Lang)
		}
		if t.Codec != "avc1" && t.Codec != "mp4a" {
			return nil, NewError(ClassUnsupported, "fixture", 0, "unsupported fixture codec %q", t.Codec)
		}
		pt := &trackPayload{
			ID: t.ID, Codec: t.Codec, Lang: lang, Timescale: t.Timescale,
			Width: t.Width, Height: t.Height, Channels: t.Channels, Rate: t.Rate,
			Edits: t.Edits,
		}
		if pt.Codec == "avc1" {
			if pt.Width == 0 {
				pt.Width = 640
			}
			if pt.Height == 0 {
				pt.Height = 360
			}
		} else {
			if pt.Channels == 0 {
				pt.Channels = 2
			}
			if pt.Rate == 0 {
				pt.Rate = pt.Timescale
			}
		}
		baseDur := uint32(1000)
		if pt.Codec == "avc1" {
			baseDur = 512
		} else {
			baseDur = 1024
		}
		for i := 0; i < t.Samples; i++ {
			dur := baseDur
			if i%7 == 3 {
				dur += 1 // slight jitter exercises stts run encoding
			}
			pt.Durs = append(pt.Durs, dur)
			off := int64(0)
			if pt.Codec == "avc1" && i%3 != 0 {
				off = int64(baseDur * 2) // B-frame style composition offset
			}
			pt.Offsets = append(pt.Offsets, off)
			isSync := t.SyncEvery <= 0 || i%t.SyncEvery == 0
			if isSync {
				pt.SyncIdx = append(pt.SyncIdx, uint32(i+1))
				fx.SyncCount[t.ID]++
			}
			n := opts.SampleMin
			if opts.SampleMax > opts.SampleMin {
				n += int(sampleRand(opts.Seed, t.ID, i) % uint64(opts.SampleMax-opts.SampleMin+1))
			}
			pt.Sizes = append(pt.Sizes, uint32(n))
			samples[ti] = append(samples[ti], makeSample(opts.Seed, t.ID, i, n))
			fx.TotalBytes += n
		}
		if len(pt.SyncIdx) == 0 {
			return nil, NewError(ClassInvalidSample, "fixture", 0, "track %d has no sync samples", t.ID)
		}
		payloads[ti] = pt
	}

	raw, err := BuildFile(opts.Brand, opts.MovieTS, payloads, samples)
	if err != nil {
		return nil, WrapError(ClassIntegrity, "fixture.build", 0, err, "build failed")
	}
	fx.Bytes = raw
	fx.TrackSample = samples
	return fx, nil
}

func sampleRand(seed int64, trackID uint32, idx int) uint64 {
	h := fnv.New64a()
	var b [16]byte
	binary.BigEndian.PutUint64(b[0:], uint64(seed))
	binary.BigEndian.PutUint32(b[8:], trackID)
	binary.BigEndian.PutUint32(b[12:], uint32(idx))
	h.Write(b[:])
	return h.Sum64()
}

// makeSample fills a payload with reproducible bytes; the first four bytes
// encode track and index so byte-level corruption is easy to locate.
func makeSample(seed int64, trackID uint32, idx, n int) []byte {
	out := make([]byte, n)
	var b [16]byte
	binary.BigEndian.PutUint64(b[0:], uint64(seed))
	binary.BigEndian.PutUint32(b[8:], trackID)
	binary.BigEndian.PutUint32(b[12:], uint32(idx))
	for off := 0; off < n; off += 16 {
		h := fnv.New64a()
		h.Write(b[:])
		h.Write([]byte{byte(off >> 24), byte(off >> 16), byte(off >> 8), byte(off)})
		sum := h.Sum64()
		binary.BigEndian.PutUint64(out[off:mini(off+8, n)], sum)
		if off+8 < n {
			binary.BigEndian.PutUint64(out[off+8:mini(off+16, n)], sum^0xa5a5a5a5a5a5a5a5)
		}
	}
	return out
}

func mini(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Describe summarizes a fixture for terminal output.
func (f *Fixture) Describe() string {
	return fmt.Sprintf("fixture tracks=%d bytes=%d movie_ts=%d", len(f.Tracks), len(f.Bytes), f.MovieTS)
}
