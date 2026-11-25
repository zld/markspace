// Package samplegen creates small, external-asset-free MP4 fixtures for tests
// and the mediadump self-check.
package samplegen

import (
	"encoding/binary"
	"hash/crc32"
	"sort"

	"github.com/zld/mediacore/internal/model"
	"github.com/zld/mediacore/internal/recover"
)

// SampleSpec describes one generated sample box.
type SampleSpec struct {
	Index        int
	StartTick    uint64
	DurationTick uint32
	Payload      []byte
}

// TrackSpec describes one generated track.
type TrackSpec struct {
	ID        int
	Kind      string
	TimeScale uint32
	OffsetNS  model.NS
	Samples   []SampleSpec
}

// Spec is a complete generated MP4 fixture.
type Spec struct {
	MovieTimeScale uint32
	IncludeIndex   bool
	Tracks         []TrackSpec
}

// SampleLocation records absolute byte positions.
type SampleLocation struct {
	TrackID       int
	Index         int
	BoxOffset     int64
	PayloadOffset int64
	DataStart     int64
	DeclaredEnd   int64
}

// Generated contains bytes and exact injection locations.
type Generated struct {
	Data      []byte
	Locations []SampleLocation
}

// Location finds a sample location.
func (g *Generated) Location(trackID, index int) (SampleLocation, bool) {
	for _, l := range g.Locations {
		if l.TrackID == trackID && l.Index == index {
			return l, true
		}
	}
	return SampleLocation{}, false
}

// Generate builds an MP4-like file composed of standard metadata boxes plus
// self-describing recoverable MCS1 sample boxes.
func Generate(spec Spec) (*Generated, error) {
	movieScale := spec.MovieTimeScale
	if movieScale == 0 {
		movieScale = uint32(model.NSPerSecond)
	}

	traks := make([][]byte, 0, len(spec.Tracks))
	for _, tr := range spec.Tracks {
		lastEnd := uint64(0)
		for _, s := range tr.Samples {
			if end := s.StartTick + uint64(s.DurationTick); end > lastEnd {
				lastEnd = end
			}
		}
		localDuration, err := model.TicksToNS(lastEnd, tr.TimeScale)
		if err != nil {
			return nil, err
		}
		trackMovieDuration := uint32(tr.OffsetNS + localDuration)

		tkhd := tkhdBox(uint32(tr.ID), trackMovieDuration, tr.Kind)
		elst := elstBox(uint32(tr.OffsetNS), uint32(localDuration))
		mdhd := mdhdBox(tr.TimeScale, uint32(lastEnd))
		hdlr := hdlrBox(tr.Kind)
		stblChildren := []byte{}
		if spec.IncludeIndex && len(tr.Samples) > 0 {
			stts := sttsBox(uint32(len(tr.Samples)), tr.Samples[0].DurationTick)
			stblChildren = append(stblChildren, stts...)
		}
		stbl := box("stbl", stblChildren)
		minf := box("minf", stbl)
		mdia := box("mdia", append(append(mdhd, hdlr...), minf...))
		edts := box("edts", elst)
		trak := box("trak", append(append(tkhd, edts...), mdia...))
		traks = append(traks, trak)
	}

	moovPayload := mvhdBox(movieScale, 1_000_000_000)
	for _, t := range traks {
		moovPayload = append(moovPayload, t...)
	}
	moov := box("moov", moovPayload)
	ftyp := ftypBox()
	out := append([]byte{}, ftyp...)
	out = append(out, moov...)

	type queued struct {
		tr    TrackSpec
		s     SampleSpec
		start model.NS
	}
	var queue []queued
	for _, tr := range spec.Tracks {
		for _, s := range tr.Samples {
			local, err := model.TicksToNS(s.StartTick, tr.TimeScale)
			if err != nil {
				return nil, err
			}
			queue = append(queue, queued{tr: tr, s: s, start: tr.OffsetNS + local})
		}
	}
	sort.SliceStable(queue, func(i, j int) bool {
		if queue[i].start != queue[j].start {
			return queue[i].start < queue[j].start
		}
		return queue[i].tr.ID < queue[j].tr.ID
	})

	g := &Generated{}
	for _, q := range queue {
		payload := make([]byte, recover.SampleHeaderSize+len(q.s.Payload))
		copy(payload[0:4], recover.SampleMagic[:])
		binary.BigEndian.PutUint32(payload[4:8], uint32(q.tr.ID))
		binary.BigEndian.PutUint32(payload[8:12], uint32(q.s.Index))
		binary.BigEndian.PutUint64(payload[12:20], q.s.StartTick)
		binary.BigEndian.PutUint32(payload[20:24], q.s.DurationTick)
		binary.BigEndian.PutUint32(payload[24:28], crc32.ChecksumIEEE(q.s.Payload))
		copy(payload[recover.SampleHeaderSize:], q.s.Payload)
		b := box("mcs ", payload)

		boxOff := int64(len(out))
		loc := SampleLocation{
			TrackID: q.tr.ID, Index: q.s.Index, BoxOffset: boxOff,
			PayloadOffset: boxOff + 8, DataStart: boxOff + 8 + recover.SampleHeaderSize,
			DeclaredEnd: boxOff + int64(len(b)),
		}
		g.Locations = append(g.Locations, loc)
		out = append(out, b...)
	}
	g.Data = out
	return g, nil
}

func box(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func fullBox(typ string, version byte, flags uint32, body []byte) []byte {
	head := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return box(typ, append(head, body...))
}

func ftypBox() []byte {
	p := make([]byte, 16)
	copy(p[0:4], "isom")
	binary.BigEndian.PutUint32(p[4:8], 0x00000200)
	copy(p[8:12], "isom")
	copy(p[12:16], "mp41")
	return box("ftyp", p)
}

func mvhdBox(timescale, duration uint32) []byte {
	body := make([]byte, 96) // after FullBox: 96 bytes, total payload 100.
	binary.BigEndian.PutUint32(body[8:12], timescale)
	binary.BigEndian.PutUint32(body[12:16], duration)
	binary.BigEndian.PutUint32(body[16:20], 0x00010000) // rate 1.0
	binary.BigEndian.PutUint16(body[20:22], 0x0100)     // volume
	identityMatrix(body[32:68])
	binary.BigEndian.PutUint32(body[92:96], 3) // next track ID
	return fullBox("mvhd", 0, 0, body)
}

func tkhdBox(id, duration uint32, kind string) []byte {
	body := make([]byte, 80) // after FullBox, total payload 84.
	binary.BigEndian.PutUint32(body[8:12], id)
	binary.BigEndian.PutUint32(body[16:20], duration)
	if kind == "audio" {
		binary.BigEndian.PutUint16(body[32:34], 0x0100)
	}
	identityMatrix(body[36:72])
	if kind == "video" {
		binary.BigEndian.PutUint32(body[72:76], 320<<16)
		binary.BigEndian.PutUint32(body[76:80], 180<<16)
	}
	return fullBox("tkhd", 0, 7, body) // enabled/in-movie/preview flags
}

func mdhdBox(timescale, duration uint32) []byte {
	body := make([]byte, 20) // after FullBox: 20 bytes, total payload 24.
	binary.BigEndian.PutUint32(body[8:12], timescale)
	binary.BigEndian.PutUint32(body[12:16], duration)
	binary.BigEndian.PutUint16(body[16:18], 0x55C4) // language "und"-ish
	return fullBox("mdhd", 0, 0, body)
}

func hdlrBox(kind string) []byte {
	handler := "vide"
	if kind == "audio" {
		handler = "soun"
	}
	body := make([]byte, 20) // predef + handler + reserved, then name appended.
	copy(body[4:8], handler)
	body = append(body, []byte("mediacore-fixture\x00")...)
	return fullBox("hdlr", 0, 0, body)
}

func sttsBox(sampleCount, delta uint32) []byte {
	body := make([]byte, 12)
	binary.BigEndian.PutUint32(body[0:4], 1)           // entry_count
	binary.BigEndian.PutUint32(body[4:8], sampleCount) // sample_count
	binary.BigEndian.PutUint32(body[8:12], delta)      // sample_delta
	return fullBox("stts", 0, 0, body)
}

func elstBox(emptyDuration, mediaDuration uint32) []byte {
	entries := 1
	if emptyDuration > 0 {
		entries = 2
	}
	body := make([]byte, 4+12*entries)
	binary.BigEndian.PutUint32(body[0:4], uint32(entries))
	at := 4
	if emptyDuration > 0 {
		binary.BigEndian.PutUint32(body[at:at+4], emptyDuration)
		binary.BigEndian.PutUint32(body[at+4:at+8], 0xFFFFFFFF) // media_time = -1 (empty edit)
		binary.BigEndian.PutUint16(body[at+8:at+10], 1)
		at += 12
	}
	binary.BigEndian.PutUint32(body[at:at+4], mediaDuration)
	binary.BigEndian.PutUint32(body[at+4:at+8], 0)
	binary.BigEndian.PutUint16(body[at+8:at+10], 1)
	return fullBox("elst", 0, 0, body)
}

func identityMatrix(dst []byte) {
	binary.BigEndian.PutUint32(dst[0:4], 0x00010000)
	binary.BigEndian.PutUint32(dst[16:20], 0x00010000)
	binary.BigEndian.PutUint32(dst[32:36], 0x40000000)
}
