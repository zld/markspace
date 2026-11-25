package fixture

import (
	"bytes"
	"encoding/binary"

	"rtptrace/internal/manifest"
	"rtptrace/internal/timeline"
)

// BuildMP4 emits the small deterministic ISO-BMFF subset understood by the
// acceptance reader. It is not a playable encoder; it contains boxes only.
func BuildMP4(m *manifest.Manifest) ([]byte, error) {
	var out bytes.Buffer
	out.Write(ftypBox())
	moovPayload := mvhdBox(uint32(m.Duration.Milliseconds()))
	for _, t := range m.Tracks {
		trak, err := trakBox(t)
		if err != nil {
			return nil, err
		}
		moovPayload = append(moovPayload, trak...)
	}
	out.Write(box("moov", moovPayload))
	return out.Bytes(), nil
}

func box(typ string, payload []byte) []byte {
	if len(typ) != 4 {
		panic("fixture: box type must be four characters")
	}
	buf := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], uint32(8+len(payload)))
	copy(buf[4:8], typ)
	copy(buf[8:], payload)
	return buf
}

func fullBox(typ string, version byte, flags uint32, payload []byte) []byte {
	head := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return box(typ, append(head, payload...))
}

func ftypBox() []byte {
	p := make([]byte, 0, 16)
	p = appendFourCC(p, "isom")
	p = binary.BigEndian.AppendUint32(p, 0x200)
	p = appendFourCC(p, "isom")
	p = appendFourCC(p, "mp42")
	return box("ftyp", p)
}

func appendFourCC(b []byte, s string) []byte {
	var v [4]byte
	copy(v[:], s)
	return append(b, v[:]...)
}

func mvhdBox(durationMS uint32) []byte {
	var p bytes.Buffer
	writeU32(&p, 0) // creation
	writeU32(&p, 0) // modification
	writeU32(&p, 1000)
	writeU32(&p, durationMS)
	writeU32(&p, 0x00010000) // rate 1.0
	writeU16(&p, 0x0100)     // volume
	writeU16(&p, 0)          // reserved
	writeU32(&p, 0)
	writeU32(&p, 0)
	// Identity matrix.
	for _, v := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		writeU32(&p, v)
	}
	for i := 0; i < 6; i++ {
		writeU32(&p, 0)
	}
	writeU32(&p, 4) // next track id
	return fullBox("mvhd", 0, 0, p.Bytes())
}

func trakBox(t manifest.Track) ([]byte, error) {
	tkhd, err := tkhdBox(t)
	if err != nil {
		return nil, err
	}
	payload := append([]byte{}, tkhd...)
	if t.EditList != nil {
		payload = append(payload, elstBox(*t.EditList)...)
	}
	mdia, err := mdiaBox(t)
	if err != nil {
		return nil, err
	}
	payload = append(payload, mdia...)
	return box("trak", payload), nil
}

func tkhdBox(t manifest.Track) ([]byte, error) {
	var p bytes.Buffer
	writeU32(&p, 0)
	writeU32(&p, 0)
	writeU32(&p, t.ID)
	writeU32(&p, 0)
	duration := uint32(0)
	if t.EditList != nil {
		for _, e := range t.EditList.Edits {
			duration += uint32(e.SegmentDuration)
		}
	}
	writeU32(&p, duration)
	writeU32(&p, 0)
	writeU32(&p, 0)
	writeU16(&p, 0) // layer
	writeU16(&p, 0) // alternate group
	if t.Kind == manifest.TrackAudio {
		writeU16(&p, 0x0100)
	} else {
		writeU16(&p, 0)
	}
	writeU16(&p, 0)
	for _, v := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		writeU32(&p, v)
	}
	if t.Kind == manifest.TrackVideo {
		writeU32(&p, 1920<<16)
		writeU32(&p, 1080<<16)
	} else {
		writeU32(&p, 0)
		writeU32(&p, 0)
	}
	return fullBox("tkhd", 0, 7, p.Bytes()), nil // enabled/in-movie/preview flags
}

func elstBox(l timeline.EditList) []byte {
	var p bytes.Buffer
	writeU32(&p, uint32(len(l.Edits)))
	for _, e := range l.Edits {
		writeU32(&p, uint32(e.SegmentDuration))
		writeU32(&p, uint32(e.MediaTime)) // int32 bits are identical for our positive values
		writeU16(&p, 1)
		writeU16(&p, 0)
	}
	return fullBox("elst", 0, 0, p.Bytes())
}

func mdiaBox(t manifest.Track) ([]byte, error) {
	var mdhd bytes.Buffer
	writeU32(&mdhd, 0)
	writeU32(&mdhd, 0)
	writeU32(&mdhd, t.SourceTimescale)
	n := uint32(len(t.Samples))
	writeU32(&mdhd, uint32(t.StartTimeTicks)+n*t.SourceTimescale)
	writeU16(&mdhd, 0x55c4) // und language packed representation
	writeU16(&mdhd, 0)

	handler := "vide"
	if t.Kind == manifest.TrackAudio {
		handler = "soun"
	}
	var hdlr bytes.Buffer
	writeU32(&hdlr, 0)
	hdlr.Write(appendFourCC(nil, handler))
	for i := 0; i < 3; i++ {
		writeU32(&hdlr, 0)
	}
	hdlr.WriteByte(0) // empty name

	stbl, err := stblBox(t)
	if err != nil {
		return nil, err
	}
	var minf bytes.Buffer
	minf.Write(box("nmhd", nil))
	minf.Write(stbl)
	var mdia bytes.Buffer
	mdia.Write(fullBox("mdhd", 0, 0, mdhd.Bytes()))
	mdia.Write(fullBox("hdlr", 0, 0, hdlr.Bytes()))
	mdia.Write(box("minf", minf.Bytes()))
	return box("mdia", mdia.Bytes()), nil
}

func stblBox(t manifest.Track) ([]byte, error) {
	var stts bytes.Buffer
	writeU32(&stts, 1)
	writeU32(&stts, uint32(len(t.Samples)))
	writeU32(&stts, t.SourceTimescale) // fixture uses one one-second sample

	var stsz bytes.Buffer
	writeU32(&stsz, 0)
	declared := t.DeclaredSampleCount
	if declared == 0 {
		declared = uint32(len(t.Samples))
	}
	writeU32(&stsz, declared)
	for _, s := range t.Samples {
		writeU32(&stsz, uint32(s.SizeBytes))
	}

	var stss bytes.Buffer
	sync := make([]uint32, 0)
	for _, s := range t.Samples {
		if s.Sync {
			sync = append(sync, s.Index)
		}
	}
	if t.Kind == manifest.TrackVideo && len(sync) > 0 {
		writeU32(&stss, uint32(len(sync)))
		for _, idx := range sync {
			writeU32(&stss, idx)
		}
	}

	var p bytes.Buffer
	p.Write(fullBox("stts", 0, 0, stts.Bytes()))
	if t.Kind == manifest.TrackVideo && len(sync) > 0 {
		p.Write(fullBox("stss", 0, 0, stss.Bytes()))
	}
	p.Write(fullBox("stsz", 0, 0, stsz.Bytes()))
	return box("stbl", p.Bytes()), nil
}

func writeU16(b *bytes.Buffer, v uint16) {
	var x [2]byte
	binary.BigEndian.PutUint16(x[:], v)
	b.Write(x[:])
}

func writeU32(b *bytes.Buffer, v uint32) {
	var x [4]byte
	binary.BigEndian.PutUint32(x[:], v)
	b.Write(x[:])
}
