package testkit

import (
	"bytes"

	"archive-recovery/internal/adapter"
	"archive-recovery/internal/index"
	"archive-recovery/internal/job"
	"archive-recovery/internal/media"
)

// PTSHint re-exports the job-layer timestamp hint so CLI and tests share one
// fixture type.
type PTSHint = job.PTSHint

// Corpus is one fully self-contained recovery fixture.
type Corpus struct {
	Codec         string
	Seed          int64
	Scenario      string
	ChunkBytes    int64
	Stream        []byte
	PTS           []PTSHint
	LegacyIndex   []byte
	WantTruncated int
	WantKeyframes int
	WantFrames    int
}

// lcg is a deterministic linear congruential generator; identical seeds
// always yield identical fixture bytes.
type lcg struct{ state uint64 }

func newRNG(seed int64) *lcg {
	return &lcg{state: uint64(seed)*6364136223846793005 + 1442695040888963407}
}

func (r *lcg) next() uint64 {
	r.state = r.state*6364136223846793005 + 1442695040888963407
	return r.state
}

func (r *lcg) intn(n int) int { return int(r.next() % uint64(n)) }

const (
	defaultFrames   = 24
	defaultKeyEvery = 6
	tickStep        = 3000
	wrapMod33       = int64(1) << 33
)

// Generate builds the requested fixture. chunkBytes controls both the
// boundary scenario padding math and the later job configuration.
func Generate(codec, scenario string, seed int64, chunkBytes int64) (*Corpus, error) {
	if chunkBytes <= 0 {
		return nil, media.NewError(media.ClassInvalidArgument, "testkit", "chunk-bytes must be positive")
	}
	if _, err := adapter.Latest(codec); err != nil {
		return nil, err
	}
	switch scenario {
	case ScenarioNormal, ScenarioTruncated, ScenarioBadIndex, ScenarioWrapPTS, ScenarioBoundary:
	default:
		return nil, media.NewError(media.ClassInvalidArgument, "testkit",
			"unknown scenario: "+scenario)
	}
	rng := newRNG(seed)
	n := defaultFrames + rng.intn(5) // 24..28 frames, seed-dependent
	keyEvery := defaultKeyEvery
	// Scale frame payloads so the stream spans several chunks even for large
	// --chunk-bytes values, keeping interruption and resume demonstrable.
	payloadSize := targetPayloadSize(chunkBytes, n)

	stream := encodeStream(codec, n, keyEvery, payloadSize, rng)
	if scenario == ScenarioBoundary {
		stream = alignKeyframeToBoundary(codec, stream, chunkBytes)
	}
	wantTrunc := 0
	if scenario == ScenarioTruncated {
		stream = appendTruncation(codec, stream)
		wantTrunc = 1
	}

	pts := buildPTS(scenario, n)
	c := &Corpus{
		Codec: codec, Seed: seed, Scenario: scenario, ChunkBytes: chunkBytes,
		Stream: stream, PTS: pts, WantTruncated: wantTrunc,
		WantFrames: n, WantKeyframes: (n-1)/keyEvery + 1,
	}
	if scenario == ScenarioBadIndex {
		idx, err := buildCorruptLegacyIndex(codec, stream, int64(len(stream)))
		if err != nil {
			return nil, err
		}
		c.LegacyIndex = idx
	}
	return c, nil
}

// targetPayloadSize chooses per-frame payload bytes so the fixture spans at
// least four chunks while keeping every unit smaller than one chunk.
func targetPayloadSize(chunkBytes int64, frames int) int {
	target := int(chunkBytes) / 4
	if target < 4 {
		target = 4
	}
	if target > 4096 {
		target = 4096
	}
	return target
}

func encodeStream(codec string, n, keyEvery, payloadSize int, rng *lcg) []byte {
	switch codec {
	case "h264":
		return jitterAnnexB(encH264Stream(n, keyEvery, payloadSize), rng)
	case "h265":
		return jitterAnnexB(encH265Stream(n, keyEvery, payloadSize), rng)
	default:
		return jitterAV1(encAV1Stream(n, keyEvery, payloadSize), rng)
	}
}

// jitterAnnexB adds 0..3 zero padding bytes between NAL units; zero padding
// is legal Annex B and must not shift unit offsets in the index.
func jitterAnnexB(stream []byte, rng *lcg) []byte {
	var out []byte
	for i := 0; i < len(stream); {
		rel, scLen := findSC(stream[i:])
		if rel < 0 {
			out = append(out, stream[i:]...)
			break
		}
		abs := i + rel
		out = append(out, stream[i:abs]...)
		next := nextStartCode(stream, abs+scLen)
		unitEnd := len(stream)
		if next >= 0 {
			unitEnd = next
		}
		out = append(out, stream[abs:unitEnd]...)
		if next >= 0 {
			out = append(out, make([]byte, rng.intn(4))...)
			i = next
		} else {
			break
		}
	}
	return out
}

func jitterAV1(stream []byte, rng *lcg) []byte {
	// AV1 has no inter-OBU zero padding; vary payload via padding OBUs
	// inserted before every third frame OBU.
	var out []byte
	pos := 0
	obuCount := 0
	for pos < len(stream) {
		hdrLen := 1
		_, lebLen, ok := decodeAt(stream, pos+hdrLen)
		if !ok {
			return stream
		}
		size, _, _ := decodeAt(stream, pos+hdrLen)
		end := pos + hdrLen + lebLen + int(size)
		if obuCount > 0 && obuCount%3 == 0 {
			out = append(out, insertAV1Padding(rng.intn(4))...)
		}
		out = append(out, stream[pos:end]...)
		pos = end
		obuCount++
	}
	return out
}

func decodeAt(p []byte, at int) (size int, n int, ok bool) {
	var v uint64
	for i := 0; i < 8 && at+i < len(p); i++ {
		b := p[at+i]
		v |= uint64(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			return int(v), i + 1, true
		}
	}
	return 0, 0, false
}

func findSC(p []byte) (int, int) {
	for i := 0; i+2 < len(p); i++ {
		if p[i] == 0 && p[i+1] == 0 && p[i+2] == 1 {
			if i > 0 && p[i-1] == 0 {
				return i - 1, 4
			}
			return i, 3
		}
	}
	return -1, 0
}

func nextStartCode(p []byte, from int) int {
	rel, _ := findSC(p[from:])
	if rel < 0 {
		return -1
	}
	return from + rel
}

// alignKeyframeToBoundary pads the stream so the first frame OBU/NAL begins
// exactly one byte before a chunk boundary, forcing the keyframe unit to
// span the boundary. AV1 padding OBUs carry header overhead, so the padding
// payload size is adjusted iteratively until the measured offset matches.
func alignKeyframeToBoundary(codec string, stream []byte, chunkBytes int64) []byte {
	firstFrame := locateFirstFrame(codec, stream)
	if firstFrame < 0 {
		return stream
	}
	if codec != "av1" {
		// Annex B zero padding shifts offsets byte-for-byte.
		wantPad := (int(chunkBytes) - 1 - firstFrame) % int(chunkBytes)
		if wantPad < 0 {
			wantPad += int(chunkBytes)
		}
		return insertAnnexBPadding(stream, wantPad)
	}
	cb := int(chunkBytes)
	// Search a padding payload whose total OBU size places the first frame
	// exactly one byte before a chunk boundary. Two cycles cover the residue
	// gaps where the LEB128 length field grows by a byte.
	for p := 0; p < 2*cb; p++ {
		pad := insertAV1Padding(p)
		if (len(pad)+firstFrame)%cb == cb-1 {
			return append(pad, stream...)
		}
	}
	return stream // best effort; tests assert alignment and catch drift
}

func locateFirstFrame(codec string, stream []byte) int {
	a, err := adapter.Latest(codec)
	if err != nil {
		return -1
	}
	sp := a.NewSplitter()
	units, _, err := sp.Write(stream)
	if err != nil {
		return -1
	}
	tail, _ := sp.Close()
	for _, u := range append(units, tail...) {
		if u.IsFrame {
			return int(u.Offset)
		}
	}
	return -1
}

func buildPTS(scenario string, n int) []PTSHint {
	pts := make([]PTSHint, n)
	for i := range pts {
		pts[i] = PTSHint{PTS: int64(i) * tickStep, Has: true}
	}
	if scenario != ScenarioWrapPTS {
		return pts
	}
	pts[2].Has = false // missing
	if n > 8 {
		pts[8].Has = false // second missing, distinct frame
	}
	pts[4] = PTSHint{PTS: pts[3].PTS, Has: true}            // duplicate
	pts[6] = PTSHint{PTS: pts[5].PTS - tickStep, Has: true} // regressive
	// 33-bit wrap on the last two frames.
	pts[n-2] = PTSHint{PTS: wrapMod33 - 2*tickStep, Has: true}
	pts[n-1] = PTSHint{PTS: tickStep, Has: true}
	return pts
}

// buildCorruptLegacyIndex parses the stream, emits a valid index and flips
// one entry's offset past end-of-file so the reader must rebuild that region.
func buildCorruptLegacyIndex(codec string, stream []byte, streamSize int64) ([]byte, error) {
	a, err := adapter.Latest(codec)
	if err != nil {
		return nil, err
	}
	sp := a.NewSplitter()
	units, _, err := sp.Write(stream)
	if err != nil {
		return nil, err
	}
	tail, dangling := sp.Close()
	units = append(units, tail...)
	_ = dangling // the dangling region is simply absent from the legacy index

	var entries []index.Entry
	seq := uint32(0)
	for _, u := range units {
		if !u.IsFrame {
			continue
		}
		entries = append(entries, index.Entry{
			FrameSeq: seq, Offset: u.Offset, Length: u.Length, IsKeyframe: u.IsKeyframe,
		})
		seq++
	}
	if len(entries) > 1 {
		entries[1].Offset = streamSize + 512 // guaranteed out of bounds
	}
	var buf bytes.Buffer
	rai := index.RandomAccessIndex{StreamSize: streamSize, Entries: entries}
	if err := rai.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
