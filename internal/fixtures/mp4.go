// Package fixtures generates the local MXF and MP4 sample assets used by the
// verification command and the subsystem tests. Nothing is downloaded.
package fixtures

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// Color is one nclx colour description used by the generators.
type Color struct {
	Primaries uint16
	Transfer  uint16
	Matrix    uint16
	FullRange bool
}

// Crop is a symmetric clean-aperture margin set.
type Crop struct {
	Left, Top, Right, Bottom uint32
}

// TrackSpec describes one synthetic track for the MP4 generator.
type TrackSpec struct {
	ID         uint32
	Kind       string // "video" or "audio"
	Timescale  uint32
	SampleDur  uint32
	Samples    int
	KeyEvery   int
	Width      uint16
	Height     uint16
	Rotation   int
	Crop       *Crop
	Color      *Color
	ZeroDeltas []int // 1-based sample numbers whose stts delta is zero
}

// MP4File is a generated MP4 plus the per-sample facts needed by tests.
type MP4File struct {
	Bytes     []byte
	Samples   []MP4Sample
	Truncated []byte
}

// MP4Sample describes one generated sample.
type MP4Sample struct {
	TrackID  uint32
	Number   int
	DTS      int64 // ticks
	Duration int64 // ticks
	Offset   int64 // absolute file offset of the payload
	Size     uint32
	Keyframe bool
	Payload  []byte
}

const (
	VideoTimescale = 90000
	VideoSampleDur = 3000 // 30 fps
	VideoSamples   = 16
	AudioTimescale = 48000
	AudioSampleDur = 1024
	AudioSamples   = 16
)

// VideoTimescaleRational is 3000/90000 = 1/30 s per video sample.
func Sample35Nanos() int64 { return 0 }

// GenerateMP4 builds a two-track MP4 with 16 samples per track. Sample
// payloads carry a "RPLY" tag so byte offsets are verifiable. The moov box is
// written after mdat to exercise offset-based parsing rather than fast start.
func GenerateMP4(video, audio TrackSpec) (*MP4File, error) {
	if video.Kind == "" {
		video = TrackSpec{ID: 1, Kind: "video", Timescale: VideoTimescale, SampleDur: VideoSampleDur, Samples: VideoSamples, KeyEvery: 8, Width: 1920, Height: 1080, Rotation: 90,
			Color: &Color{Primaries: 1, Transfer: 1, Matrix: 1}}
	}
	if audio.Kind == "" {
		audio = TrackSpec{ID: 2, Kind: "audio", Timescale: AudioTimescale, SampleDur: AudioSampleDur, Samples: AudioSamples}
	}
	specs := []TrackSpec{video, audio}
	sizes := make([][]uint32, len(specs))
	payloads := make([][][]byte, len(specs))
	for ti, spec := range specs {
		for i := 0; i < spec.Samples; i++ {
			sz := 48
			if spec.Kind == "video" {
				sz = 64 + (i*7)%23
			} else {
				sz = 32 + (i*5)%11
			}
			p := bytes.Repeat([]byte{byte('a' + ti)}, sz)
			copy(p, formatTag(ti+1, i+1))
			payloads[ti] = append(payloads[ti], p)
			sizes[ti] = append(sizes[ti], uint32(sz))
		}
	}

	// Layout: ftyp, mdat, moov. mdat payload start is known after ftyp.
	ftyp := ftypBox()
	mdatPayloadStart := int64(len(ftyp)) + 8
	var mdatPayload []byte
	absOffsets := make([][]uint32, len(specs))
	for ti := range specs {
		absOffsets[ti] = make([]uint32, len(payloads[ti]))
		for i, p := range payloads[ti] {
			absOffsets[ti][i] = uint32(mdatPayloadStart + int64(len(mdatPayload)))
			mdatPayload = append(mdatPayload, p...)
		}
	}
	mdat := box("mdat", mdatPayload)

	var traks [][]byte
	for ti, spec := range specs {
		keys := make([]uint32, 0)
		for i := 0; i < spec.Samples; i++ {
			if spec.KeyEvery <= 0 || i%spec.KeyEvery == 0 {
				keys = append(keys, uint32(i+1))
			}
		}
		duration := uint32(spec.Samples * int(spec.SampleDur))
		var stbl []byte
		if spec.Kind == "video" {
			c := Color{Primaries: 1, Transfer: 1, Matrix: 1}
			if spec.Color != nil {
				c = *spec.Color
			}
			stbl = videoStblWithDeltas(sizes[ti], absOffsets[ti], c, spec.Crop, spec.Width, spec.Height, keys, spec.SampleDur, spec.ZeroDeltas)
			traks = append(traks, videoTrak(stbl, duration, spec.Width, spec.Height, spec.Rotation))
		} else {
			stbl = audioStblWithDeltas(sizes[ti], absOffsets[ti], spec.SampleDur, spec.ZeroDeltas)
			traks = append(traks, audioTrak(stbl, duration))
		}
	}
	mvhdDur := uint64(VideoSamples * VideoSampleDur / 1000)
	moov := box("moov", cat(
		mvhdBox(VideoTimescale, mvhdDur, uint32(len(specs)+1)),
		cat(traks...),
	))
	out := cat(ftyp, mdat, moov)

	res := &MP4File{Bytes: out}
	for ti, spec := range specs {
		dts := int64(0)
		for i := 0; i < spec.Samples; i++ {
			key := spec.KeyEvery <= 0 || i%spec.KeyEvery == 0
			res.Samples = append(res.Samples, MP4Sample{
				TrackID: spec.ID, Number: i + 1, DTS: dts,
				Duration: int64(spec.SampleDur), Offset: int64(absOffsets[ti][i]),
				Size: sizes[ti][i], Keyframe: key, Payload: payloads[ti][i],
			})
			dts += int64(spec.SampleDur)
		}
	}
	// Truncated variant: cut inside the mdat payload so the box header claims
	// more bytes than exist.
	cut := mdatPayloadStart + int64(len(mdatPayload))/3
	res.Truncated = append([]byte(nil), out[:cut]...)
	return res, nil
}

// videoStblWithDeltas emits a single stts entry per run of equal deltas so a
// zero delta can be injected for duplicate timestamps.
func videoStblWithDeltas(sizes []uint32, offsets []uint32, c Color, crop *Crop, w, h uint16, keys []uint32, delta uint32, zeros []int) []byte {
	return box("stbl", cat(
		stsdBox(videoEntryBox(c, crop, w, h)),
		sttsRuns(len(sizes), delta, zeros),
		stscBox(), stszBox(sizes), stcoBox(offsets), stssBox(keys)))
}

func audioStblWithDeltas(sizes []uint32, offsets []uint32, delta uint32, zeros []int) []byte {
	return box("stbl", cat(
		stsdBox(audioEntryBox()),
		sttsRuns(len(sizes), delta, zeros),
		stscBox(), stszBox(sizes), stcoBox(offsets)))
}

// sttsRuns builds an stts box where the listed 1-based sample numbers have a
// zero delta and all others use delta.
func sttsRuns(count int, delta uint32, zeros []int) []byte {
	zeroSet := make(map[int]bool, len(zeros))
	for _, z := range zeros {
		zeroSet[z] = true
	}
	type run struct {
		delta uint32
		n     uint32
	}
	var runs []run
	for i := 1; i <= count; i++ {
		d := delta
		if zeroSet[i] {
			d = 0
		}
		if len(runs) > 0 && runs[len(runs)-1].delta == d {
			runs[len(runs)-1].n++
		} else {
			runs = append(runs, run{delta: d, n: 1})
		}
	}
	var p []byte
	p = append(p, fullBoxPayload(0, [3]byte{}, nil)...)
	p = append(p, u32(uint32(len(runs)))...)
	for _, r := range runs {
		p = append(p, u32(r.n)...)
		p = append(p, u32(r.delta)...)
	}
	return box("stts", p)
}

// Truncate returns data cut at off. It exists so callers can build corrupt
// fixtures without duplicating slicing logic.
func Truncate(data []byte, off int) []byte {
	if off < 0 || off > len(data) {
		return nil
	}
	return append([]byte(nil), data[:off]...)
}

// Binary helpers shared with the MXF generator.
func putU32(b []byte, v uint32) []byte {
	var t [4]byte
	binary.BigEndian.PutUint32(t[:], v)
	return append(b, t[:]...)
}

func putU16(b []byte, v uint16) []byte {
	var t [2]byte
	binary.BigEndian.PutUint16(t[:], v)
	return append(b, t[:]...)
}

func must(err error) {
	if err != nil {
		panic(fmt.Sprintf("fixture: %v", err))
	}
}
