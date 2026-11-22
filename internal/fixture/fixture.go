package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
)

// RecordHeader is the on-disk prefix of every audio sample in generated
// fixtures. It makes sample boundaries independently verifiable, which is what
// lets recovery rebuild a missing index instead of guessing.
//
//	magic u16 | version u8 | channels u8 | rate u32 | payloadBytes u32 |
//	seq u32 | frames u32 | crc32 u32
const RecordHeader = 24

const (
	recordMagic   = 0xAF01
	recordVersion = 1
)

// Kind names one generated input.
type Kind string

const (
	KindClean     Kind = "clean"
	KindNoMoov    Kind = "no-moov"
	KindTruncated Kind = "truncated"
	KindBadRange  Kind = "bad-range"
	KindDepCycle  Kind = "dep-cycle"
	KindWrap      Kind = "wrap"
	KindNoIndex   Kind = "no-index"
	KindGarbled   Kind = "garbled"
)

// Kinds lists every fixture the kernel claims to handle.
var Kinds = []Kind{KindClean, KindNoMoov, KindTruncated, KindBadRange, KindDepCycle, KindWrap, KindNoIndex, KindGarbled}

// Plan describes the media a fixture should contain.
type Plan struct {
	Kind            Kind
	SampleRate      int
	Channels        int
	Seconds         float64
	FramesPerSample int // PCM frames per container sample (AAC-like granularity)
	SamplesPerChunk int
	ToneHz          float64
}

// DefaultPlan is a deterministic stereo source with a non-zero start time.
func DefaultPlan(k Kind) Plan {
	return Plan{
		Kind:            k,
		SampleRate:      44100,
		Channels:        2,
		Seconds:         2.0,
		FramesPerSample: 1024,
		SamplesPerChunk: 10,
		ToneHz:          440,
	}
}

// Media is the generated PCM plus the facts the writer must describe. PCM is
// interleaved and already clamped to [-1,1]; the container stores int16.
type Media struct {
	Plan      Plan
	PCM       []float64
	Frames    int
	StartTS   uint32
	Timescale uint32
}

// GeneratePCM produces a deterministic tone with a short silent lead-in so
// loudness gates have real silence to measure.
func GeneratePCM(p Plan) *Media {
	if p.SampleRate <= 0 || p.Channels <= 0 || p.Seconds <= 0 || p.FramesPerSample <= 0 {
		p = DefaultPlan(p.Kind)
	}
	frames := int(math.Round(p.Seconds * float64(p.SampleRate)))
	pcm := make([]float64, frames*p.Channels)
	lead := p.SampleRate / 20 // 50 ms
	const amp = 0.25
	for i := 0; i < frames; i++ {
		var v float64
		if i >= lead {
			t := float64(i-lead) / float64(p.SampleRate)
			v = amp * math.Sin(2*math.Pi*p.ToneHz*t)
			fade := math.Min(1, float64(frames-lead-i)/float64(p.SampleRate)/0.05)
			v *= math.Max(0, fade)
		}
		for c := 0; c < p.Channels; c++ {
			gain := 1.0
			if c == 1 {
				gain = 0.8
			}
			pcm[i*p.Channels+c] = v * gain
		}
	}
	start := uint32(1024)
	if p.Kind == KindWrap {
		// 32-bit RTP timestamps must wrap inside this file.
		start = math.MaxUint32 - uint32(p.SampleRate/2)
	}
	return &Media{Plan: p, PCM: pcm, Frames: frames, StartTS: start, Timescale: uint32(p.SampleRate)}
}

// Write creates the fixture at dir/<kind>.mp4 and returns the path.
func Write(dir string, m *Media) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, string(m.Plan.Kind)+".mp4")
	b, err := Build(m)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", fmt.Errorf("fixture: write %s: %w", path, err)
	}
	return path, nil
}

// Ensure writes every fixture kind into dir and returns their paths.
func Ensure(dir string) (map[Kind]string, error) {
	out := make(map[Kind]string, len(Kinds))
	for _, k := range Kinds {
		p, err := Write(dir, GeneratePCM(DefaultPlan(k)))
		if err != nil {
			return nil, err
		}
		out[k] = p
	}
	return out, nil
}

// Build returns the encoded bytes for m. It is exported so tests can inspect
// and mutate generated files without touching disk.
func Build(m *Media) ([]byte, error) {
	records, sizes, err := encodeRecords(m)
	if err != nil {
		return nil, err
	}
	ftyp := buildFTyp()
	per := m.Plan.SamplesPerChunk
	if per <= 0 {
		per = 10
	}
	nChunks := chunkCount(len(sizes), per)

	nSamples := len(sizes)
	placeholder := make([]int, nChunks)
	for i := range placeholder {
		placeholder[i] = 1 // non-zero so patchStco replaces every entry
	}
	probe := &layout{}
	moovLen := len(buildMoov(m, probe, sizes, placeholder))
	mdatPayload := len(ftyp) + moovLen + 8
	offsets := chunkOffsets(m, sizes, mdatPayload)

	lay := &layout{}
	moov := buildMoov(m, lay, sizes, placeholder)
	if len(moov) != moovLen || len(lay.stcoOffsets) != len(probe.stcoOffsets) {
		return nil, fmt.Errorf("fixture: moov layout is not stable")
	}
	patchStco(moov, lay, offsets)
	if m.Plan.Kind == KindDepCycle {
		// A second track that depends on track 1 while track 1 depends on it.
		// The cycle moov has different offsets, so it is rendered separately.
		probeCycle := &layout{}
		moovLen2 := len(buildMoovCycle(m, probeCycle, sizes, placeholder))
		mdatPayload2 := len(ftyp) + moovLen2 + 8
		offsets2 := chunkOffsets(m, sizes, mdatPayload2)
		layCycle := &layout{}
		moov = buildMoovCycle(m, layCycle, sizes, placeholder)
		if len(moov) != moovLen2 {
			return nil, fmt.Errorf("fixture: cycle moov layout is not stable")
		}
		patchStco(moov, layCycle, offsets2)
	}
	_ = nSamples

	out := bytes.Buffer{}
	out.Write(ftyp)
	if m.Plan.Kind != KindNoMoov {
		out.Write(moov)
	}
	writeMdat(&out, records)
	b := out.Bytes()
	return applyDamage(b, m, lay)
}

// patchStco fills the recorded stco entry positions with final offsets.
func patchStco(moov []byte, lay *layout, offsets []int) {
	for i, pos := range lay.stcoOffsets {
		if i < len(offsets) {
			binary.BigEndian.PutUint32(moov[pos:], uint32(offsets[i]))
		}
	}
}

func writeMdat(out *bytes.Buffer, records [][]byte) {
	payload := 0
	for _, r := range records {
		payload += len(r)
	}
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(payload+8))
	copy(hdr[4:], "mdat")
	out.Write(hdr[:])
	for _, r := range records {
		out.Write(r)
	}
}

func chunkCount(samples, per int) int {
	if per <= 0 {
		per = 10
	}
	return (samples + per - 1) / per
}

func chunkOffsets(m *Media, sizes []uint32, payloadStart int) []int {
	per := m.Plan.SamplesPerChunk
	if per <= 0 {
		per = 10
	}
	n := chunkCount(len(sizes), per)
	offs := make([]int, n)
	pos := payloadStart
	for c := 0; c < n; c++ {
		offs[c] = pos
		for i := 0; i < per && c*per+i < len(sizes); i++ {
			pos += int(sizes[c*per+i])
		}
	}
	return offs
}

// encodeRecords turns PCM into self-verifying sample records.
func encodeRecords(m *Media) ([][]byte, []uint32, error) {
	fps := m.Plan.FramesPerSample
	if fps <= 0 {
		fps = 1024
	}
	ch := m.Plan.Channels
	total := (m.Frames + fps - 1) / fps
	records := make([][]byte, 0, total)
	sizes := make([]uint32, 0, total)
	for s := 0; s < total; s++ {
		start := s * fps
		end := start + fps
		if end > m.Frames {
			end = m.Frames
		}
		frames := end - start
		payload := make([]byte, frames*ch*2)
		for i := 0; i < frames; i++ {
			for c := 0; c < ch; c++ {
				v := m.PCM[(start+i)*ch+c]
				iv := int32(math.Round(v * 32767))
				if iv > 32767 {
					iv = 32767
				} else if iv < -32768 {
					iv = -32768
				}
				binary.LittleEndian.PutUint16(payload[(i*ch+c)*2:], uint16(int16(iv)))
			}
		}
		rec := make([]byte, RecordHeader+len(payload))
		binary.BigEndian.PutUint16(rec[0:], recordMagic)
		rec[2] = recordVersion
		rec[3] = byte(ch)
		binary.BigEndian.PutUint32(rec[4:], uint32(m.Plan.SampleRate))
		binary.BigEndian.PutUint32(rec[8:], uint32(len(payload)))
		binary.BigEndian.PutUint32(rec[12:], uint32(s))
		binary.BigEndian.PutUint32(rec[16:], uint32(frames))
		binary.BigEndian.PutUint32(rec[20:], crc32.ChecksumIEEE(payload))
		copy(rec[RecordHeader:], payload)
		records = append(records, rec)
		sizes = append(sizes, uint32(len(rec)))
	}
	return records, sizes, nil
}

// layout records where semantic tables landed so damage can be applied by
// role instead of by brittle byte arithmetic.
type layout struct {
	stcoOffsets []int // absolute file offsets of each stco entry
	moovStart   int
	moovEnd     int
}
