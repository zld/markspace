package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"

	prtp "github.com/pion/rtp"

	"replaycheck/internal/clip"
	"replaycheck/internal/stream"
	"replaycheck/internal/wav"
)

const (
	sampleRate = 48000
	videoRate  = 90000
	fps        = 30
	// fixtureSampleSize is the uniform coded sample size in every video
	// fixture, which lets the damage oracle work without a sample table.
	fixtureSampleSize = 500
)

// fixtureSet holds every generated input.
type fixtureSet struct {
	Dir string

	NormalWAV string
	NormalMP4 string

	TruncWAV   string
	PartialWAV string
	CutMP4     string

	CleanTrace [][]byte
	DupTrace   [][]byte
	BurstTrace [][]byte

	// TruncWAVSize is the byte length of the truncated WAV image.
	TruncWAVSize int64
	// CutMP4Size and CutMP4BadOffset describe the truncated video.
	CutMP4Size      int64
	CutMP4BadOffset int64
}

func buildFixtures(dir string) (*fixtureSet, error) {
	for _, sub := range []string{"fixtures", "out", "state", "ckpt"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	fs := &fixtureSet{Dir: dir}
	base := filepath.Join(dir, "fixtures")

	// Four seconds of 48 kHz stereo PCM with a deterministic envelope.
	frames := 4 * sampleRate
	pcm := make([]byte, frames*2*2)
	for i := 0; i < frames; i++ {
		// A quiet anchor at the start, then an increasing ramp, so peaks and
		// boundaries are both observable.
		v := int16((i * 13) % 28000)
		binary.LittleEndian.PutUint16(pcm[i*4:], uint16(v))
		binary.LittleEndian.PutUint16(pcm[i*4+2:], uint16(v/3))
	}
	normalWAV := wav.BuildPCM(2, sampleRate, 16, pcm)
	fs.NormalWAV = filepath.Join(base, "normal.wav")
	if err := os.WriteFile(fs.NormalWAV, normalWAV, 0o644); err != nil {
		return nil, err
	}

	// Video: 30 fps, keyframe every 30 samples (one second), 90 kHz.
	normalMP4, err := stream.Build(stream.Config{
		Timescale: videoRate, FPS: fps, Width: 160, Height: 90,
		SampleCount: 4 * fps, KeyEvery: 30, SampleSize: fixtureSampleSize,
	})
	if err != nil {
		return nil, err
	}
	fs.NormalMP4 = filepath.Join(base, "normal.mp4")
	if err := os.WriteFile(fs.NormalMP4, normalMP4, 0o644); err != nil {
		return nil, err
	}

	// Truncated WAV: the data chunk still declares its original length.
	trunc := normalWAV[:len(normalWAV)-4800]
	fs.TruncWAV = filepath.Join(base, "truncated.wav")
	if err := os.WriteFile(fs.TruncWAV, trunc, 0o644); err != nil {
		return nil, err
	}

	// Incomplete trailing sample frame: 3 stray bytes beyond whole frames,
	// with the declared sizes consistent so the only defect is the tail.
	shortPCM := pcm[:sampleRate*2*2]
	partial := wav.BuildPCM(2, sampleRate, 16, append(append([]byte{}, shortPCM...), 0xAA, 0xBB, 0xCC))
	binary.LittleEndian.PutUint32(partial[4:8], uint32(len(partial)-8))
	binary.LittleEndian.PutUint32(partial[40:44], uint32(len(shortPCM)+3))
	fs.PartialWAV = filepath.Join(base, "partial-tail.wav")
	if err := os.WriteFile(fs.PartialWAV, partial, 0o644); err != nil {
		return nil, err
	}

	// Video whose last samples are missing from the file.
	cutMP4, err := stream.Build(stream.Config{
		Timescale: videoRate, FPS: fps, Width: 160, Height: 90,
		SampleCount: 4 * fps, KeyEvery: 30, SampleSize: fixtureSampleSize, TruncateBytes: 1200,
	})
	if err != nil {
		return nil, err
	}
	fs.CutMP4 = filepath.Join(base, "cut.mp4")
	if err := os.WriteFile(fs.CutMP4, cutMP4, 0o644); err != nil {
		return nil, err
	}

	// RTP traces over the same 4 s at 30 fps: 120 packets.
	fs.CleanTrace, _, err = buildTrace(traceSpec{n: 120})
	if err != nil {
		return nil, err
	}
	// Duplicate a few packets in place and swap a few neighbours. Every
	// sequence number still arrives, so loss must stay zero.
	fs.DupTrace, _, err = buildTrace(traceSpec{
		n:         120,
		dupAfter:  map[int]bool{10: true, 40: true, 80: true},
		swapPairs: []int{21, 41, 61},
	})
	if err != nil {
		return nil, err
	}
	// A burst loss: sequence numbers 60..64 are dropped entirely, plus one
	// duplicated packet early in the trace.
	fs.BurstTrace, _, err = buildTrace(traceSpec{
		n:        120,
		dupAfter: map[int]bool{20: true},
		drop:     map[int]bool{60: true, 61: true, 62: true, 63: true, 64: true},
	})
	if err != nil {
		return nil, err
	}
	return fs, nil
}

// traceSpec describes a receive trace.
type traceSpec struct {
	n int
	// dupAfter emits the packet with that sequence number twice, the copy
	// immediately following the original.
	dupAfter map[int]bool
	// swapPairs lists the first index of each adjacent pair to emit in
	// reverse order (i+1 then i), which is a plain reorder, not a loss.
	swapPairs []int
	// drop lists sequence numbers never sent.
	drop map[int]bool
}

// buildTrace emits wire-format RTP packets and the ordered sequence numbers
// it produced. Timestamps advance one video frame per sequence number at the
// 90 kHz fixture clock.
func buildTrace(spec traceSpec) ([][]byte, []uint16, error) {
	swapped := make(map[int]bool, len(spec.swapPairs))
	for _, i := range spec.swapPairs {
		swapped[i] = true
	}
	var seqs []uint16
	emit := func(seq int) {
		if spec.drop[seq] {
			return
		}
		seqs = append(seqs, uint16(seq))
	}
	for i := 0; i < spec.n; i++ {
		if swapped[i] {
			emit(i + 1)
			emit(i)
			i++
			continue
		}
		if swapped[i-1] && i > 0 {
			continue // already emitted by the pair
		}
		emit(i)
		if spec.dupAfter[i] {
			emit(i)
		}
	}
	out := make([][]byte, 0, len(seqs))
	for _, seq := range seqs {
		p := &prtp.Packet{
			Header: prtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: seq,
				Timestamp:      uint32(seq) * (videoRate / fps),
				SSRC:           0x1234,
				Marker:         int(seq)%fps == fps-1,
			},
			Payload: []byte{byte(seq >> 8), byte(seq), 0xAA},
		}
		buf, err := p.Marshal()
		if err != nil {
			return nil, nil, err
		}
		out = append(out, buf)
	}
	return out, seqs, nil
}

// eventSet returns the normal programme events in 90 kHz ticks. Each start
// sits a single tick after a keyframe and off a sample boundary, so the
// alignment rules are exercised while the deviation stays inside one audio
// sample period plus one video frame.
func eventSet() []clip.Event {
	return []clip.Event{
		{Name: "opening", StartTick: 90001, EndTick: 150000}, // 1.000s
		{Name: "clutch", StartTick: 180001, EndTick: 225000}, // 2.000s
		{Name: "final", StartTick: 270001, EndTick: 330000},  // 3.000s
	}
}

// mustReadFile reads a generated fixture; failures indicate a build bug.
func mustReadFile(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("fixture %s: %v", path, err))
	}
	return data
}

// firstMissingSampleOffset finds the first byte of the mdat payload the
// file cannot supply, assuming the fixture's uniform sample size. It is the
// oracle the damaged scenario is checked against, so it deliberately avoids
// the scanner's sample-table code path.
func firstMissingSampleOffset(img []byte, sampleSize int64) int64 {
	pos := int64(0)
	for pos+8 <= int64(len(img)) {
		size := int64(binary.BigEndian.Uint32(img[pos : pos+4]))
		typ := string(img[pos+4 : pos+8])
		if size == 1 {
			if pos+16 > int64(len(img)) {
				return pos
			}
			size = int64(binary.BigEndian.Uint64(img[pos+8 : pos+16]))
		}
		if typ != "mdat" {
			if size == 0 || pos+size > int64(len(img)) {
				return pos
			}
			pos += size
			continue
		}
		payload := pos + 8
		available := int64(len(img)) - payload
		if size > 0 && available >= size-8 {
			return -1 // mdat complete
		}
		if sampleSize <= 0 {
			return payload
		}
		return payload + (available/sampleSize)*sampleSize
	}
	return int64(len(img))
}
