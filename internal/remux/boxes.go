// Box tree builders for the remux writer. Leaf boxes are marshaled with
// github.com/abema/go-mp4; container boxes are assembled manually with
// computed sizes so the whole moov can be built in memory before any byte
// hits the output.
package remux

import (
	"bytes"
	"encoding/binary"

	gomp4 "github.com/abema/go-mp4"

	"mediacheck/internal/adapter"
)

func leaf(box gomp4.IImmutableBox) ([]byte, error) {
	var buf bytes.Buffer
	_, err := gomp4.Marshal(&buf, box, gomp4.Context{})
	return buf.Bytes(), err
}

func container(typ string, children ...[]byte) []byte {
	size := 8
	for _, c := range children {
		size += len(c)
	}
	out := make([]byte, 0, size)
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(size))
	copy(hdr[4:8], typ)
	out = append(out, hdr[:]...)
	for _, c := range children {
		out = append(out, c...)
	}
	return out
}

func mustLeaf(box gomp4.IImmutableBox) []byte {
	b, err := leaf(box)
	if err != nil {
		panic(err)
	}
	return b
}

var identityMatrix = [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}

func buildMoov(specs []TrackSpec, chunkOffsets []uint64, movieDurMs uint32) ([]byte, error) {
	mvhd := &gomp4.Mvhd{
		Timescale:   1000,
		DurationV0:  movieDurMs,
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      identityMatrix,
		NextTrackID: uint32(len(specs) + 1),
	}
	children := [][]byte{container("mvhd", mustLeaf(mvhd))}
	for i, spec := range specs {
		children = append(children, buildTrak(spec, chunkOffsets[i], movieDurMs))
	}
	return container("moov", children...), nil
}

func buildTrak(spec TrackSpec, chunkOffset uint64, movieDurMs uint32) []byte {
	tkhd := &gomp4.Tkhd{
		TrackID:    spec.ID,
		DurationV0: movieDurMs,
		Matrix:     identityMatrix,
	}
	tkhd.SetFlags(0x000003) // enabled | in movie
	if spec.Kind == adapter.KindAudio {
		tkhd.Volume = 0x0100
	} else {
		tkhd.Width = 320 << 16
		tkhd.Height = 240 << 16
	}
	mdhd := &gomp4.Mdhd{
		Timescale:  uint32(spec.Timescale),
		DurationV0: uint32(durationTicks(spec)),
		Language:   [3]byte{'u', 'n', 'd'},
	}
	hdlr := &gomp4.Hdlr{Name: "MediaHandler"}
	var mediaHeader []byte
	if spec.Kind == adapter.KindAudio {
		hdlr.HandlerType = [4]byte{'s', 'o', 'u', 'n'}
		smhd := &gomp4.Smhd{}
		mediaHeader = container("smhd", mustLeaf(smhd))
	} else {
		hdlr.HandlerType = [4]byte{'v', 'i', 'd', 'e'}
		vmhd := &gomp4.Vmhd{}
		vmhd.SetFlags(0x000001)
		mediaHeader = container("vmhd", mustLeaf(vmhd))
	}
	url := &gomp4.Url{}
	url.SetFlags(gomp4.UrlSelfContained)
	drefPayload := mustLeaf(&gomp4.Dref{EntryCount: 1})
	urlBox := container("url ", mustLeaf(url))
	drefBox := container("dref", drefPayload, urlBox)
	dinf := container("dinf", drefBox)

	stbl := container("stbl",
		buildStsd(spec),
		buildStts(spec),
		buildCtts(spec),
		buildStsc(spec),
		buildStsz(spec),
		buildStss(spec),
		buildStco(chunkOffset),
	)
	minf := container("minf", mediaHeader, dinf, stbl)
	mdia := container("mdia", container("mdhd", mustLeaf(mdhd)), container("hdlr", mustLeaf(hdlr)), minf)
	return container("trak", container("tkhd", mustLeaf(tkhd)), mdia)
}

func durationTicks(spec TrackSpec) int64 {
	if len(spec.Samples) == 0 {
		return 0
	}
	last := spec.Samples[len(spec.Samples)-1]
	return last.DTS + last.Duration
}

func buildStsd(spec TrackSpec) []byte {
	var entry []byte
	var entryType gomp4.BoxType
	if spec.Kind == adapter.KindAudio {
		e := &gomp4.AudioSampleEntry{
			ChannelCount: 2,
			SampleSize:   16,
			SampleRate:   uint32(spec.Timescale) << 16,
		}
		e.DataReferenceIndex = 1
		e.SetType(gomp4.StrToBoxType("mp4a"))
		entry = mustLeaf(e)
		entryType = e.GetType()
	} else {
		e := &gomp4.VisualSampleEntry{
			Width:           320,
			Height:          240,
			Horizresolution: 0x00480000,
			Vertresolution:  0x00480000,
			FrameCount:      1,
			Depth:           0x0018,
			PreDefined3:     -1,
		}
		e.DataReferenceIndex = 1
		e.SetType(gomp4.StrToBoxType("avc1"))
		entry = mustLeaf(e)
		entryType = e.GetType()
	}
	stsd := mustLeaf(&gomp4.Stsd{EntryCount: 1})
	entryBox := container(string(entryType[:]), entry)
	return container("stsd", stsd, entryBox)
}

func buildStts(spec TrackSpec) []byte {
	stts := &gomp4.Stts{}
	for _, s := range spec.Samples {
		d := uint32(s.Duration)
		if n := len(stts.Entries); n > 0 && stts.Entries[n-1].SampleDelta == d {
			stts.Entries[n-1].SampleCount++
		} else {
			stts.Entries = append(stts.Entries, gomp4.SttsEntry{SampleCount: 1, SampleDelta: d})
		}
	}
	stts.EntryCount = uint32(len(stts.Entries))
	return container("stts", mustLeaf(stts))
}

func buildCtts(spec TrackSpec) []byte {
	hasOffset := false
	for _, s := range spec.Samples {
		if s.CTS != s.DTS {
			hasOffset = true
			break
		}
	}
	if !hasOffset {
		return nil
	}
	ctts := &gomp4.Ctts{}
	ctts.SetVersion(1)
	for _, s := range spec.Samples {
		off := int32(s.CTS - s.DTS)
		if n := len(ctts.Entries); n > 0 && ctts.Entries[n-1].SampleOffsetV1 == off {
			ctts.Entries[n-1].SampleCount++
		} else {
			ctts.Entries = append(ctts.Entries, gomp4.CttsEntry{SampleCount: 1, SampleOffsetV1: off})
		}
	}
	ctts.EntryCount = uint32(len(ctts.Entries))
	return container("ctts", mustLeaf(ctts))
}

func buildStsc(spec TrackSpec) []byte {
	n := uint32(len(spec.Samples))
	stsc := &gomp4.Stsc{EntryCount: 1}
	if n > 0 {
		stsc.Entries = []gomp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: n, SampleDescriptionIndex: 1}}
	}
	return container("stsc", mustLeaf(stsc))
}

func buildStsz(spec TrackSpec) []byte {
	stsz := &gomp4.Stsz{SampleCount: uint32(len(spec.Samples))}
	for _, s := range spec.Samples {
		stsz.EntrySize = append(stsz.EntrySize, s.Size)
	}
	return container("stsz", mustLeaf(stsz))
}

func buildStss(spec TrackSpec) []byte {
	stss := &gomp4.Stss{}
	nonKey := 0
	for i, s := range spec.Samples {
		if s.Keyframe {
			stss.SampleNumber = append(stss.SampleNumber, uint32(i+1))
		} else {
			nonKey++
		}
	}
	if nonKey == 0 {
		return nil // all samples are keyframes; stss omitted
	}
	stss.EntryCount = uint32(len(stss.SampleNumber))
	return container("stss", mustLeaf(stss))
}

func buildStco(offset uint64) []byte {
	stco := &gomp4.Stco{EntryCount: 1, ChunkOffset: []uint32{uint32(offset)}}
	return container("stco", mustLeaf(stco))
}
