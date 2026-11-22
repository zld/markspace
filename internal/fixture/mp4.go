// Package fixture builds the self-contained media fixtures used by tests and
// by the mediadump demo. Everything is generated in Go; no external media
// files are read.
//
// The MP4 fixture deliberately exercises the adapter's strictness: it has one
// good video track, one good audio track, and a variant whose last sample is
// truncated, so the byte-offset error path is covered without any binary
// fixtures checked in.
package fixture

import (
	"bytes"
	"encoding/hex"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/mp4"

	"example.com/mediaforge/internal/merrors"
)

// Sample layout of the canonical fixture.
const (
	VideoTrackID   = 1
	AudioTrackID   = 2
	VideoTimescale = 90000
	AudioTimescale = 48000

	// VideoDur is the usual per-frame duration: 30 fps at 90 kHz.
	VideoDur = 3000
	// AudioDur is one AAC-LC frame: 1024 samples at 48 kHz.
	AudioDur = 1024
)

var (
	avcSPS = mustHex("67640020accac05005bb0169e0000003002000000c9c4c000432380008647c12401cb1c31380")
	avcPPS = mustHex("68b5df20")
)

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// VideoDurations returns a VFR-looking duration list: mostly VideoDur with
// some shorter and longer frames, in a fixed repeating pattern so the fixture
// is deterministic.
func VideoDurations(n int) []uint32 {
	base := []uint32{3000, 3000, 1500, 4500, 3000, 3000, 3000, 2250, 3750, 3000}
	out := make([]uint32, n)
	for i := range out {
		out[i] = base[i%len(base)]
	}
	return out
}

// Keyframes returns whether video sample i (0-based) is a sync sample. Every
// eighth sample is a keyframe plus sample 0.
func Keyframes(n int) []bool {
	out := make([]bool, n)
	for i := range out {
		out[i] = i == 0 || i%8 == 0
	}
	return out
}

// SamplePayload produces deterministic, distinguishable bytes for a sample.
func SamplePayload(trackID uint32, idx, size int) []byte {
	out := make([]byte, size)
	for i := range out {
		out[i] = byte(int(trackID)*31 + idx*7 + i*13 + 1)
	}
	return out
}

// MP4Options tunes the generated file.
type MP4Options struct {
	VideoSamples int
	AudioSamples int
	// ConstantRate gives every video frame the same duration. When false the
	// frames use an irregular VFR pattern.
	ConstantRate bool
	// TruncateLast cuts bytes off the final video sample's data while leaving
	// the sample table intact, so the adapter must report a truncated sample.
	TruncateLast int
	// UnsupportedCodec replaces the video sample entry with HEVC, which the
	// v1 adapter refuses before writing anything.
	UnsupportedCodec bool
}

// DefaultMP4Options returns a balanced A/V fixture.
func DefaultMP4Options() MP4Options {
	return MP4Options{VideoSamples: 16, AudioSamples: 30, TruncateLast: 0}
}

// BuildMP4 assembles a progressive MP4 from generated samples.
func BuildMP4(opt MP4Options) ([]byte, error) {
	if opt.VideoSamples <= 0 || opt.AudioSamples <= 0 {
		return nil, merrors.New(merrors.StageAdapter, merrors.Invalid, "fixture needs samples")
	}
	f := mp4.NewFile()
	f.AddChild(mp4.CreateFtyp(), 0)
	moov := mp4.NewMoovBox()
	moov.AddChild(mp4.CreateMvhd())

	vdurs := VideoDurations(opt.VideoSamples)
	if opt.ConstantRate {
		for i := range vdurs {
			vdurs[i] = VideoDur
		}
	}
	keys := Keyframes(opt.VideoSamples)
	videoTrak, err := buildTrak(VideoTrackID, VideoTimescale, "video", opt.UnsupportedCodec,
		vdurs, keys, func(i int) []byte { return SamplePayload(VideoTrackID, i, 40+i%7) })
	if err != nil {
		return nil, err
	}
	adurs := make([]uint32, opt.AudioSamples)
	for i := range adurs {
		adurs[i] = AudioDur
	}
	akeys := make([]bool, opt.AudioSamples)
	for i := range akeys {
		akeys[i] = true
	}
	audioTrak, err := buildTrak(AudioTrackID, AudioTimescale, "audio", false,
		adurs, akeys, func(i int) []byte { return SamplePayload(AudioTrackID, i, 12+i%3) })
	if err != nil {
		return nil, err
	}
	moov.AddChild(videoTrak)
	moov.AddChild(audioTrak)
	moov.AddChild(mp4.NewMvexBox())
	f.AddChild(moov, 0)

	var videoData, audioData []byte
	for i := 0; i < opt.VideoSamples; i++ {
		videoData = append(videoData, SamplePayload(VideoTrackID, i, 40+i%7)...)
	}
	for i := 0; i < opt.AudioSamples; i++ {
		audioData = append(audioData, SamplePayload(AudioTrackID, i, 12+i%3)...)
	}
	// Audio is laid out first and video last, so truncating the tail of the
	// file always affects the video track and nothing else.
	data := append(append([]byte(nil), audioData...), videoData...)
	audioBytes := uint32(len(audioData))
	f.AddChild(&mp4.MdatBox{Data: data}, 0)

	// Encode once to find where mdat data lands, then fix the chunk offsets
	// and encode again. Neither fixpoint changes any byte count, so the
	// offsets stay correct.
	layout := func(dataOff uint32) {
		audioTrak.Mdia.Minf.Stbl.Stco.ChunkOffset[0] = dataOff
		videoTrak.Mdia.Minf.Stbl.Stco.ChunkOffset[0] = dataOff + audioBytes
	}
	layout(0)
	var buf bytes.Buffer
	if err := f.Encode(&buf); err != nil {
		return nil, merrors.Wrap(merrors.StageAdapter, merrors.Malformed, 0, err, "fixture encode")
	}
	firstMdat := findBoxData(buf.Bytes(), "mdat")
	if firstMdat < 0 {
		return nil, merrors.New(merrors.StageAdapter, merrors.Malformed, "fixture has no mdat")
	}
	layout(uint32(firstMdat))
	buf.Reset()
	if err := f.Encode(&buf); err != nil {
		return nil, merrors.Wrap(merrors.StageAdapter, merrors.Malformed, 0, err, "fixture re-encode")
	}
	out := buf.Bytes()
	mdatData := findBoxData(out, "mdat")
	if mdatData != firstMdat {
		return nil, merrors.New(merrors.StageAdapter, merrors.Malformed,
			"fixture layout shifted from %d to %d", firstMdat, mdatData)
	}
	if opt.TruncateLast > 0 {
		// Remove bytes from the end of the file while leaving every
		// sample-table entry untouched: exactly the corruption the adapter
		// must report. Video data is last, so only video samples are hit.
		if opt.TruncateLast > len(videoData) {
			return nil, merrors.New(merrors.StageAdapter, merrors.Invalid, "truncation too large")
		}
		out = out[:len(out)-opt.TruncateLast]
	}
	return out, nil
}

func trackBytes(n, base int) uint32 {
	total := 0
	for i := 0; i < n; i++ {
		mod := 7
		if base == 12 {
			mod = 3
		}
		total += base + i%mod
	}
	return uint32(total)
}

func buildTrak(id, timescale uint32, mediaType string, unsupported bool,
	durs []uint32, keys []bool, payload func(int) []byte) (*mp4.TrakBox, error) {
	trak := mp4.CreateEmptyTrak(id, timescale, mediaType, "und")
	if mediaType == "video" {
		if unsupported {
			// HEVC signalling: valid MP4, unsupported by the v1 adapter.
			vps := mustHex("40010c01ffff022000000300b0000003000003007b18b024")
			sps := mustHex("420101022000000300b0000003000003007ba0078200887db6718b92448053888892cf24a69272c9124922dc91aa48fca223ff000100016a02020201")
			pps := mustHex("4401c0252f053240")
			if err := trak.SetHEVCDescriptor("hvc1", [][]byte{vps}, [][]byte{sps}, [][]byte{pps}, nil, true); err != nil {
				return nil, err
			}
		} else {
			if err := trak.SetAVCDescriptor("avc1", [][]byte{avcSPS}, [][]byte{avcPPS}, true); err != nil {
				return nil, err
			}
		}
	} else {
		if err := trak.SetAACDescriptor(aac.AAClc, int(timescale)); err != nil {
			return nil, err
		}
	}

	stbl := trak.Mdia.Minf.Stbl
	stts := &mp4.SttsBox{}
	var keyframes []uint32
	sizes := make([]uint32, len(durs))
	for i, d := range durs {
		if i == 0 || d != durs[i-1] {
			stts.SampleCount = append(stts.SampleCount, 1)
			stts.SampleTimeDelta = append(stts.SampleTimeDelta, d)
		} else {
			stts.SampleCount[len(stts.SampleCount)-1]++
		}
		if keys[i] {
			keyframes = append(keyframes, uint32(i+1))
		}
		sizes[i] = uint32(len(payload(i)))
	}
	stbl.Stts = stts
	stbl.Stsc = &mp4.StscBox{
		Entries:             []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: uint32(len(durs)), FirstSampleNr: 1}},
		SampleDescriptionID: []uint32{1},
	}
	stbl.Stsz = &mp4.StszBox{SampleNumber: uint32(len(durs)), SampleSize: sizes}
	if mediaType == "video" && len(keyframes) != len(durs) {
		stbl.Stss = &mp4.StssBox{SampleNumber: keyframes}
	}
	stbl.Stco = &mp4.StcoBox{ChunkOffset: []uint32{0}}
	stbl.Children = []mp4.Box{stbl.Stsd, stbl.Stts, stbl.Stsc, stbl.Stsz}
	if stbl.Stss != nil {
		stbl.Children = append(stbl.Children, stbl.Stss)
	}
	stbl.Children = append(stbl.Children, stbl.Stco)
	return trak, nil
}

func findBoxData(buf []byte, typ string) int {
	off := 0
	for off+8 <= len(buf) {
		size := int(uint32(buf[off])<<24 | uint32(buf[off+1])<<16 | uint32(buf[off+2])<<8 | uint32(buf[off+3]))
		if size < 8 || off+size > len(buf) {
			return -1
		}
		if string(buf[off+4:off+8]) == typ {
			return off + 8
		}
		off += size
	}
	return -1
}
