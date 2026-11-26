package samplegen

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/abema/go-mp4"

	"assetpack/internal/adapter"
	"assetpack/internal/clock"
)

// BuiltContainer is a generated MP4/fMP4 reference input.
type BuiltContainer struct {
	Format string
	Data   []byte
}

// BuildContainer deterministically builds the local mp4/v1 or fmp4/v1 fixture.
func BuildContainer(sequence int, fragmented bool) (*BuiltContainer, error) {
	format := adapter.FormatMP4
	if fragmented {
		format = adapter.FormatFMP4
	}
	videoData, videoDescs := buildVideoSamples(sequence)
	audioData, audioDescs := buildAudioSamples()

	var mdat bytes.Buffer
	rel := int64(0)
	for i := range videoDescs {
		videoDescs[i].Offset = rel
		videoDescs[i].Size = len(videoData[i])
		mdat.Write(videoData[i])
		rel += int64(len(videoData[i]))
	}
	for i := range audioDescs {
		audioDescs[i].Offset = rel
		audioDescs[i].Size = len(audioData[i])
		mdat.Write(audioData[i])
		rel += int64(len(audioData[i]))
	}

	man := adapter.MP4ManifestBox{
		APIVersion: 1, Format: format, Duration: 180000,
		Tracks: []adapter.MP4TrackBox{
			{
				ID: "video", Kind: "video", Codec: adapter.ID{Name: adapter.CodecRGBA8, Version: 1},
				Timebase: clock.Frame24fps, Width: Width, Height: Height, Samples: videoDescs,
			},
			{
				ID: "audio", Kind: "audio", Codec: adapter.ID{Name: adapter.CodecPCMS16, Version: 1},
				Timebase: clock.Timebase48k, SampleRate: AudioRate, Channels: 1, Samples: audioDescs,
			},
		},
	}

	ftyp := ftypBox()
	emsg, err := emsgBox(sequence)
	if err != nil {
		return nil, err
	}
	moof := []byte{}
	if fragmented {
		moof = box("moof", box("mfhd", fullBoxPayload(0, uint32(sequence))))
	}
	preambleLen := len(ftyp) + len(emsg) + len(moof)

	var apmdBox []byte
	for attempt := 0; attempt < 5; attempt++ {
		payload, err := json.Marshal(man)
		if err != nil {
			return nil, err
		}
		apmdBox = box("apmd", payload)
		payloadStart := int64(preambleLen + len(apmdBox) + 8)
		relVideo := int64(0)
		for i := range man.Tracks[0].Samples {
			man.Tracks[0].Samples[i].Offset = payloadStart + relVideo
			relVideo += int64(man.Tracks[0].Samples[i].Size)
		}
		relAudio := relVideo
		for i := range man.Tracks[1].Samples {
			man.Tracks[1].Samples[i].Offset = payloadStart + relAudio
			relAudio += int64(man.Tracks[1].Samples[i].Size)
		}
		finalPayload, err := json.Marshal(man)
		if err != nil {
			return nil, err
		}
		if len(finalPayload) == len(payload) {
			apmdBox = box("apmd", finalPayload)
			break
		}
	}

	var out bytes.Buffer
	out.Write(ftyp)
	out.Write(emsg)
	out.Write(moof)
	out.Write(apmdBox)
	out.Write(box("mdat", mdat.Bytes()))
	return &BuiltContainer{Format: format, Data: out.Bytes()}, nil
}

func buildVideoSamples(sequence int) ([][]byte, []adapter.MP4SampleBox) {
	data := make([][]byte, FrameCount)
	desc := make([]adapter.MP4SampleBox, FrameCount)
	for i := 0; i < FrameCount; i++ {
		data[i] = VideoFrame(sequence, i)
		key := i%12 == 0
		var pre []int
		if !key {
			gop := (i / 12) * 12
			for p := gop; p < i; p++ {
				pre = append(pre, p)
			}
		}
		desc[i] = adapter.MP4SampleBox{
			Index: i, PTS: int64(i), Dur: 1, Size: FrameBytes, Key: key, Preroll: pre,
		}
	}
	return data, desc
}

func buildAudioSamples() ([][]byte, []adapter.MP4SampleBox) {
	data := make([][]byte, AudioSamples/ChunkSamples)
	desc := make([]adapter.MP4SampleBox, AudioSamples/ChunkSamples)
	for c := range data {
		data[c] = AudioChunk(c)
		desc[c] = adapter.MP4SampleBox{
			Index: c, PTS: int64(c * ChunkSamples), Dur: ChunkSamples, Size: len(data[c]), Key: true,
		}
	}
	return data, desc
}

func box(typ string, payload []byte) []byte {
	size := 8 + len(payload)
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[:4], uint32(size))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

func ftypBox() []byte {
	p := []byte("isom")
	p = binary.BigEndian.AppendUint32(p, 0x200)
	p = append(p, "isom"...)
	p = append(p, "mp41"...)
	return box("ftyp", p)
}

func fullBoxPayload(flags uint32, sequence uint32) []byte {
	b := []byte{0, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return binary.BigEndian.AppendUint32(b, sequence)
}

func emsgBox(sequence int) ([]byte, error) {
	e := &mp4.Emsg{
		FullBox:          mp4.FullBox{Version: 1},
		SchemeIdUri:      "urn:assetpack:cue",
		Value:            fmt.Sprintf("sequence-%d", sequence),
		Timescale:        90000,
		PresentationTime: 45000,
		EventDuration:    90000,
		Id:               uint32(100 + sequence),
		MessageData:      []byte("range-start"),
	}
	var p bytes.Buffer
	if _, err := mp4.Marshal(&p, e, mp4.Context{}); err != nil {
		return nil, err
	}
	return box("emsg", p.Bytes()), nil
}

// TruncateMDAT simulates a partially copied mdat by removing payload tail bytes.
func TruncateMDAT(data []byte, n int) []byte {
	if n <= 0 || n > len(data) {
		n = 96
	}
	return append([]byte(nil), data[:len(data)-n]...)
}
