package mux

import (
	"encoding/binary"

	gomp4 "github.com/abema/go-mp4"

	"medpipe/internal/demux"
	"medpipe/internal/fault"
)

func writeFileType(w *gomp4.Writer) *fault.Error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeFtyp()}); err != nil {
		return commitErr("ftyp.start", err)
	}
	_, err := gomp4.Marshal(w, &gomp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 512,
		CompatibleBrands: []gomp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'i', 's', 'o', '2'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
		},
	}, gomp4.Context{})
	if err != nil {
		return commitErr("ftyp.payload", err)
	}
	if _, err := w.EndBox(); err != nil {
		return commitErr("ftyp.end", err)
	}
	return nil
}

func writeMovie(w *gomp4.Writer, mappings []demux.Mapping, offsets [][]uint32) *fault.Error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMoov()}); err != nil {
		return commitErr("moov.start", err)
	}
	var movieDuration uint32
	for _, m := range mappings {
		d := trackDuration(m.Track)
		if d > movieDuration {
			movieDuration = d
		}
	}
	if fe := writeMVHD(w, movieDuration, uint32(len(mappings)+1)); fe != nil {
		return fe
	}
	for i, m := range mappings {
		if fe := writeTrak(w, uint32(i+1), m, offsets[i]); fe != nil {
			return fe
		}
	}
	if _, err := w.EndBox(); err != nil {
		return commitErr("moov.end", err)
	}
	return nil
}

func writeMVHD(w *gomp4.Writer, duration, nextTrackID uint32) *fault.Error {
	p := make([]byte, 96)
	binary.BigEndian.PutUint32(p[8:12], 1000) // timescale
	binary.BigEndian.PutUint32(p[12:16], duration)
	binary.BigEndian.PutUint32(p[16:20], 0x00010000) // rate 1.0
	binary.BigEndian.PutUint16(p[20:22], 0x0100)     // volume 1.0
	putIdentityMatrix(p[32:68])
	for i := 68; i < 92; i++ {
		p[i] = 0
	}
	binary.BigEndian.PutUint32(p[92:96], nextTrackID)
	return writeFullBox(w, gomp4.BoxTypeMvhd(), 0, 0, p)
}

func writeTrak(w *gomp4.Writer, id uint32, m demux.Mapping, offsets []uint32) *fault.Error {
	t := m.Track
	duration := trackDuration(t)
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeTrak()}); err != nil {
		return commitErr("trak.start", err)
	}
	if fe := writeTKHD(w, id, duration, t); fe != nil {
		return fe
	}
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMdia()}); err != nil {
		return commitErr("mdia.start", err)
	}
	if fe := writeMDHD(w, duration); fe != nil {
		return fe
	}
	if fe := writeHDLR(w, t.Kind); fe != nil {
		return fe
	}
	if fe := writeMINF(w, t, offsets); fe != nil {
		return fe
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return commitErr("mdia.end", err)
	}
	if _, err := w.EndBox(); err != nil { // trak
		return commitErr("trak.end", err)
	}
	return nil
}

func writeTKHD(w *gomp4.Writer, id, duration uint32, t *demux.Track) *fault.Error {
	p := make([]byte, 80)
	binary.BigEndian.PutUint32(p[8:12], id)
	binary.BigEndian.PutUint32(p[16:20], duration)
	// reserved 8 bytes at 20:28
	if t.Kind == "audio" {
		binary.BigEndian.PutUint16(p[32:34], 0x0100) // volume
	}
	putIdentityMatrix(p[36:72])
	width := uint32(t.Width) << 16
	height := uint32(t.Height) << 16
	if t.Kind == "audio" {
		width, height = 0, 0
	}
	binary.BigEndian.PutUint32(p[72:76], width)
	binary.BigEndian.PutUint32(p[76:80], height)
	return writeFullBox(w, gomp4.BoxTypeTkhd(), 0, 0x000007, p)
}

func writeMDHD(w *gomp4.Writer, duration uint32) *fault.Error {
	p := make([]byte, 20)
	binary.BigEndian.PutUint32(p[8:12], 1000)
	binary.BigEndian.PutUint32(p[12:16], duration)
	binary.BigEndian.PutUint16(p[16:18], 0x55c4) // language "und"
	return writeFullBox(w, gomp4.BoxTypeMdhd(), 0, 0, p)
}

func writeHDLR(w *gomp4.Writer, kind string) *fault.Error {
	handler := [4]byte{'v', 'i', 'd', 'e'}
	name := "Medpipe Video"
	if kind == "audio" {
		handler = [4]byte{'s', 'o', 'u', 'n'}
		name = "Medpipe Audio"
	}
	p := make([]byte, 8+12+len(name)+1)
	binary.BigEndian.PutUint32(p[0:4], 0) // pre_defined
	copy(p[4:8], handler[:])
	// reserved 12 bytes at 8:20
	copy(p[20:], append([]byte(name), 0))
	return writeFullBox(w, gomp4.BoxTypeHdlr(), 0, 0, p)
}

func putIdentityMatrix(b []byte) {
	if len(b) < 36 {
		return
	}
	binary.BigEndian.PutUint32(b[0:4], 0x00010000)
	binary.BigEndian.PutUint32(b[16:20], 0x00010000)
	binary.BigEndian.PutUint32(b[32:36], 0x40000000)
}

func trackDuration(t *demux.Track) uint32 {
	var d int64
	for _, s := range t.Samples {
		d += s.DurationMS
	}
	if d <= 0 {
		d = int64(len(t.Samples)) * 40
	}
	return uint32(d)
}

func writeFullBox(w *gomp4.Writer, typ gomp4.BoxType, version byte, flags uint32, payload []byte) *fault.Error {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: typ}); err != nil {
		return commitErr("box.start:"+typ.String(), err)
	}
	vf := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	if _, err := w.Write(vf); err != nil {
		return commitErr("box.version:"+typ.String(), err)
	}
	if _, err := w.Write(payload); err != nil {
		return commitErr("box.payload:"+typ.String(), err)
	}
	if _, err := w.EndBox(); err != nil {
		return commitErr("box.end:"+typ.String(), err)
	}
	return nil
}

func commitErr(stage string, err error) *fault.Error {
	return fault.Wrap(fault.ErrCommit, "mux.mp4."+stage, 0, "MP4 box write failed", err)
}
