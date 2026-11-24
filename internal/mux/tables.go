package mux

import (
	"encoding/binary"

	gomp4 "github.com/abema/go-mp4"

	"medpipe/internal/demux"
	"medpipe/internal/fault"
)

func writeMINF(w *gomp4.Writer, t *demux.Track, offsets []uint32) *fault.Error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMinf()}); err != nil {
		return commitErr("minf.start", err)
	}
	if t.Kind == "audio" {
		if fe := writeFullBox(w, gomp4.BoxTypeSmhd(), 0, 0, make([]byte, 4)); fe != nil {
			return fe
		}
	} else {
		vmhd := make([]byte, 8) // graphicsmode + opcolor
		if fe := writeFullBox(w, gomp4.BoxTypeVmhd(), 0, 1, vmhd); fe != nil {
			return fe
		}
	}
	if fe := writeDINF(w); fe != nil {
		return fe
	}
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeStbl()}); err != nil {
		return commitErr("stbl.start", err)
	}
	if fe := writeSTSD(w, t); fe != nil {
		return fe
	}
	if fe := writeSTTS(w, t); fe != nil {
		return fe
	}
	if fe := writeSTSC(w); fe != nil {
		return fe
	}
	if fe := writeSTSZ(w, t); fe != nil {
		return fe
	}
	if fe := writeSTCO(w, offsets); fe != nil {
		return fe
	}
	if _, err := w.EndBox(); err != nil { // stbl
		return commitErr("stbl.end", err)
	}
	if _, err := w.EndBox(); err != nil { // minf
		return commitErr("minf.end", err)
	}
	return nil
}

func writeDINF(w *gomp4.Writer) *fault.Error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeDinf()}); err != nil {
		return commitErr("dinf.start", err)
	}
	urlBox := make([]byte, 12)
	binary.BigEndian.PutUint32(urlBox[0:4], 12)
	copy(urlBox[4:8], "url ")
	urlBox[11] = 1 // flags=1: self-contained
	payload := make([]byte, 4+len(urlBox))
	binary.BigEndian.PutUint32(payload[0:4], 1)
	copy(payload[4:], urlBox)
	if fe := writeFullBox(w, gomp4.BoxTypeDref(), 0, 0, payload); fe != nil {
		return fe
	}
	if _, err := w.EndBox(); err != nil {
		return commitErr("dinf.end", err)
	}
	return nil
}

func writeSTSD(w *gomp4.Writer, t *demux.Track) *fault.Error {
	entry := buildSampleEntry(t)
	payload := make([]byte, 4+len(entry))
	binary.BigEndian.PutUint32(payload[0:4], 1)
	copy(payload[4:], entry)
	return writeFullBox(w, gomp4.BoxTypeStsd(), 0, 0, payload)
}

func buildSampleEntry(t *demux.Track) []byte {
	if t.Kind == "audio" || t.Codec == "mp4a" {
		content := make([]byte, 8+20)
		binary.BigEndian.PutUint16(content[6:8], 1)  // data_ref_index
		binary.BigEndian.PutUint16(content[8:10], 0) // entry_version
		binary.BigEndian.PutUint16(content[16:18], t.Channels)
		if t.Channels == 0 {
			binary.BigEndian.PutUint16(content[16:18], 2)
		}
		binary.BigEndian.PutUint16(content[18:20], 16) // sample_size
		sr := t.SampleRate
		if sr == 0 {
			sr = 48000
		}
		binary.BigEndian.PutUint32(content[24:28], sr<<16)
		return boxBytes("mp4a", content)
	}

	content := make([]byte, 8+70)
	binary.BigEndian.PutUint16(content[6:8], 1) // data_ref_index
	binary.BigEndian.PutUint16(content[24:26], t.Width)
	binary.BigEndian.PutUint16(content[26:28], t.Height)
	binary.BigEndian.PutUint32(content[28:32], 0x00480000)
	binary.BigEndian.PutUint32(content[32:36], 0x00480000)
	binary.BigEndian.PutUint16(content[40:42], 1) // frame_count
	// compressorname [32] at 42:74 remains empty
	binary.BigEndian.PutUint16(content[74:76], 0x0018) // depth
	binary.BigEndian.PutUint16(content[76:78], 0xffff)
	avcC := []byte{
		1, 66, 0xc0, 30, 0xff, 0xe1,
		0x00, 0x03, 0x67, 0x42, 0xc0,
		0x01,
		0x00, 0x03, 0x68, 0xce, 0x38,
	}
	content = append(content, boxBytes("avcC", avcC)...)
	return boxBytes("avc1", content)
}

func writeSTTS(w *gomp4.Writer, t *demux.Track) *fault.Error {
	n := len(t.Samples)
	payload := make([]byte, 4+8*n)
	binary.BigEndian.PutUint32(payload[0:4], uint32(n))
	for i, s := range t.Samples {
		off := 4 + i*8
		binary.BigEndian.PutUint32(payload[off:off+4], 1)
		binary.BigEndian.PutUint32(payload[off+4:off+8], uint32(s.DurationMS))
	}
	return writeFullBox(w, gomp4.BoxTypeStts(), 0, 0, payload)
}

func writeSTSC(w *gomp4.Writer) *fault.Error {
	payload := make([]byte, 4+12)
	binary.BigEndian.PutUint32(payload[0:4], 1)
	binary.BigEndian.PutUint32(payload[4:8], 1)
	binary.BigEndian.PutUint32(payload[8:12], 1)
	binary.BigEndian.PutUint32(payload[12:16], 1)
	return writeFullBox(w, gomp4.BoxTypeStsc(), 0, 0, payload)
}

func writeSTSZ(w *gomp4.Writer, t *demux.Track) *fault.Error {
	n := len(t.Samples)
	payload := make([]byte, 8+4*n)
	binary.BigEndian.PutUint32(payload[0:4], 0) // sample_size=0: variable
	binary.BigEndian.PutUint32(payload[4:8], uint32(n))
	for i, s := range t.Samples {
		binary.BigEndian.PutUint32(payload[8+4*i:12+4*i], uint32(len(s.Data)))
	}
	return writeFullBox(w, gomp4.BoxTypeStsz(), 0, 0, payload)
}

func writeSTCO(w *gomp4.Writer, offsets []uint32) *fault.Error {
	payload := make([]byte, 4+4*len(offsets))
	binary.BigEndian.PutUint32(payload[0:4], uint32(len(offsets)))
	for i, off := range offsets {
		binary.BigEndian.PutUint32(payload[4+4*i:8+4*i], off)
	}
	return writeFullBox(w, gomp4.BoxTypeStco(), 0, 0, payload)
}

func boxBytes(typ string, content []byte) []byte {
	out := make([]byte, 8+len(content))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], content)
	return out
}
