package fixture

import (
	"bytes"
	"encoding/binary"

	"archive.example/mediakernel/internal/metadata"

	"github.com/abema/go-mp4"
)

// ID3 builds a deterministic ID3v2.3 body with one TIT2 frame.
func ID3(text string) []byte {
	body := append([]byte{3}, []byte(text)...) // encoding 3 (UTF-8), then text
	frame := make([]byte, 0, 10+len(body))
	frame = append(frame, 'T', 'I', 'T', '2')
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], uint32(len(body)))
	frame = append(frame, sz[:]...)
	frame = append(frame, 0, 0) // frame flags
	frame = append(frame, body...)

	header := []byte{'I', 'D', '3', 3, 0, 0, 0, 0, 0, 0}
	// Syncsafe 28-bit size of the frames following the header.
	header[6] = byte((len(frame) >> 21) & 0x7f)
	header[7] = byte((len(frame) >> 14) & 0x7f)
	header[8] = byte((len(frame) >> 7) & 0x7f)
	header[9] = byte(len(frame) & 0x7f)
	return append(header, frame...)
}

// seiUUID is the fixed user_data_unregistered UUID for generated SEI.
var seiUUID = [16]byte{0x31, 0x36, 0x30, 0x32, 0x38, 0x00, 0x00, 0x00,
	0x61, 0x72, 0x63, 0x68, 0x69, 0x76, 0x65, 0x31}

// SEI builds a deterministic SEI NAL carrying a message and raw tail.
func SEI(message string, tail []byte) []byte {
	msg := append([]byte(message), 0)
	size := len(seiUUID) + len(msg)
	out := make([]byte, 0, 3+size+len(tail))
	out = append(out, 0x06) // NAL header: type 6 (SEI)
	out = append(out, 0x05) // payload type 5: user_data_unregistered
	out = append(out, byte(size))
	out = append(out, seiUUID[:]...)
	out = append(out, msg...)
	out = append(out, tail...)
	return out
}

// EmsgBox encodes an emsg box (version 1) with its own, independent timescale.
func EmsgBox(scheme, value string, timescale uint32, presentation uint64, duration uint32, id uint32, data []byte) []byte {
	emsg := &mp4.Emsg{
		FullBox:          mp4.FullBox{Version: 1},
		SchemeIdUri:      scheme,
		Value:            value,
		Timescale:        timescale,
		PresentationTime: presentation,
		EventDuration:    duration,
		Id:               id,
		MessageData:      data,
	}
	var payload bytes.Buffer
	if _, err := mp4.Marshal(&payload, emsg, mp4.Context{}); err != nil {
		panic("fixture: emsg marshal failed: " + err.Error())
	}
	return boxBytes("emsg", payload.Bytes())
}

// TimedBoxBytes encodes a generated timed box.
func TimedBoxBytes(it TimedItem) []byte {
	p := make([]byte, metadata.TimedBoxHeaderLen, metadata.TimedBoxHeaderLen+len(it.Text)+1+len(it.Payload))
	p[0] = 1 // version
	binary.BigEndian.PutUint64(p[4:12], it.Presentation)
	binary.BigEndian.PutUint32(p[12:16], it.Duration)
	binary.BigEndian.PutUint16(p[16:18], it.Sequence)
	p = append(p, []byte(it.Text)...)
	p = append(p, 0)
	p = append(p, it.Payload...)
	return boxBytes(it.BoxType, p)
}

// boxBytes wraps a payload in an 8-byte box header with the true size.
func boxBytes(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

// boxDeclared writes a box whose declared size may differ from its actual
// length, to inject deliberately damaged structure.
func boxDeclared(typ string, declared uint32, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], declared)
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

// trak is the set of timed items carried by one track in one fragment.
type traf struct {
	trackID uint32
	items   []TimedItem
	emsgBox []byte // a standalone emsg fragment (no track reference)
	extra   [][]byte
}

// buildMP4 assembles ftyp, moov (mvhd + one trak per declaration + traf base
// metadata), one moof per traf, then a trailing mfra marker.
func buildMP4(tracks []TrackDecl, trafs []traf) []byte {
	var out []byte
	out = append(out, ftypBox()...)
	out = append(out, moovBox(tracks)...)
	for _, f := range trafs {
		out = append(out, moofBox(f)...)
	}
	out = append(out, boxBytes("mfra", nil)...)
	return out
}

func ftypBox() []byte {
	payload := []byte{'i', 's', 'o', '5', 0, 0, 0, 0, 'a', 'r', 'c', 'h'}
	binary.BigEndian.PutUint32(payload[4:8], 0)
	return boxBytes("ftyp", payload)
}

func moovBox(tracks []TrackDecl) []byte {
	var body []byte
	body = append(body, mvhdBox()...)
	for _, t := range tracks {
		body = append(body, trakBox(t)...)
	}
	return boxBytes("moov", body)
}

func mvhdBox() []byte {
	mvhd := &mp4.Mvhd{
		FullBox:     mp4.FullBox{Version: 0},
		Timescale:   1000,
		DurationV0:  60000,
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
		NextTrackID: 7,
	}
	var buf bytes.Buffer
	if _, err := mp4.Marshal(&buf, mvhd, mp4.Context{}); err != nil {
		panic("fixture: mvhd marshal failed: " + err.Error())
	}
	return boxBytes("mvhd", buf.Bytes())
}

func trakBox(t TrackDecl) []byte {
	var body []byte
	body = append(body, tkhdBox(t)...)
	body = append(body, mdiaBox(t)...)
	return boxBytes("trak", body)
}

func tkhdBox(t TrackDecl) []byte {
	tkhd := &mp4.Tkhd{
		FullBox:    mp4.FullBox{Version: 0},
		TrackID:    t.TrackID,
		DurationV0: 60000,
		Matrix:     [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
	}
	if t.Handler == "soun" {
		tkhd.Volume = 0x0100
	}
	var buf bytes.Buffer
	if _, err := mp4.Marshal(&buf, tkhd, mp4.Context{}); err != nil {
		panic("fixture: tkhd marshal failed: " + err.Error())
	}
	return boxBytes("tkhd", buf.Bytes())
}

func mdiaBox(t TrackDecl) []byte {
	var body []byte
	body = append(body, mdhdBox(t)...)
	body = append(body, hdlrBox(t)...)
	body = append(body, minfBox()...)
	return boxBytes("mdia", body)
}

func mdhdBox(t TrackDecl) []byte {
	mdhd := &mp4.Mdhd{
		FullBox:    mp4.FullBox{Version: 0},
		Timescale:  t.Timescale,
		DurationV0: 60000,
		// Each element is the 5-bit ISO-639-2 code for a lowercase letter,
		// i.e. rune-0x60. "und" => 21, 14, 4.
		Language: [3]byte{byte('u' - 0x60), byte('n' - 0x60), byte('d' - 0x60)},
	}
	var buf bytes.Buffer
	if _, err := mp4.Marshal(&buf, mdhd, mp4.Context{}); err != nil {
		panic("fixture: mdhd marshal failed: " + err.Error())
	}
	return boxBytes("mdhd", buf.Bytes())
}

func hdlrBox(t TrackDecl) []byte {
	hdlr := &mp4.Hdlr{
		FullBox:     mp4.FullBox{Version: 0},
		HandlerType: [4]byte{t.Handler[0], t.Handler[1], t.Handler[2], t.Handler[3]},
		Name:        t.Name,
	}
	var buf bytes.Buffer
	if _, err := mp4.Marshal(&buf, hdlr, mp4.Context{}); err != nil {
		panic("fixture: hdlr marshal failed: " + err.Error())
	}
	return boxBytes("hdlr", buf.Bytes())
}

func minfBox() []byte {
	// Minimal media information container. Timed metadata lives in moof
	// fragments, so no sample tables or media headers are required.
	var body []byte
	body = append(body, boxBytes("vmhd", []byte{0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0})...)
	body = append(body, boxBytes("stbl", nil)...)
	return boxBytes("minf", body)
}

func moofBox(f traf) []byte {
	var body []byte
	body = append(body, boxBytes("mfhd", append(fullBox(0, 0), 0, 0, 0, 1))...)
	if f.emsgBox != nil {
		body = append(body, f.emsgBox...)
		return boxBytes("moof", body)
	}
	body = append(body, trafBox(f)...)
	return boxBytes("moof", body)
}

func trafBox(f traf) []byte {
	var body []byte
	// tfhd declares the track reference for the whole fragment.
	body = append(body, boxBytes("tfhd", append(fullBox(0, 0x020000), uint32be(f.trackID)...))...)
	// tfdt carries the fragment's base media decode time (version 0 form).
	var base uint32
	if len(f.items) > 0 {
		base = uint32(f.items[0].Presentation)
	}
	body = append(body, boxBytes("tfdt", append(fullBox(0, 0), uint32be(base)...))...)
	for _, extra := range f.extra {
		body = append(body, extra...)
	}
	for _, it := range f.items {
		body = append(body, TimedBoxBytes(it)...)
	}
	return boxBytes("traf", body)
}

func fullBox(version uint8, flags uint32) []byte {
	return []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
}

func uint32be(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}
