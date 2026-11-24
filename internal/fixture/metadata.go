package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/abema/go-mp4"
)

func WebVTT(startMS int64, text string) []byte {
	return []byte(fmt.Sprintf("WEBVTT\n\n1\n%s --> %s\n%s\n",
		msTimestamp(startMS), msTimestamp(startMS+2000), text))
}

func msTimestamp(ms int64) string {
	h := ms / 3600000
	ms %= 3600000
	m := ms / 60000
	ms %= 60000
	s := ms / 1000
	ms %= 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, ms)
}

type Chapter struct {
	PTSMS int64
	Title string
}

func ChapterBook(chapters []Chapter) []byte {
	var b bytes.Buffer
	b.WriteString("MRCH")
	_ = binary.Write(&b, binary.BigEndian, uint16(len(chapters)))
	for _, c := range chapters {
		_ = binary.Write(&b, binary.BigEndian, uint64(c.PTSMS))
		title := []byte(c.Title)
		_ = binary.Write(&b, binary.BigEndian, uint16(len(title)))
		b.Write(title)
	}
	return b.Bytes()
}

func ID3Tag(title, artist string) []byte {
	var body bytes.Buffer
	body.Write(id3Frame("TIT2", title))
	body.Write(id3Frame("TPE1", artist))
	var header [10]byte
	copy(header[0:3], "ID3")
	header[3] = 3
	header[4] = 0
	size := uint32(body.Len())
	header[6] = byte((size >> 21) & 0x7f)
	header[7] = byte((size >> 14) & 0x7f)
	header[8] = byte((size >> 7) & 0x7f)
	header[9] = byte(size & 0x7f)
	return append(header[:], body.Bytes()...)
}

func id3Frame(id, text string) []byte {
	payload := append([]byte{0x03}, []byte(text)...)
	var b bytes.Buffer
	b.WriteString(id)
	_ = binary.Write(&b, binary.BigEndian, uint32(len(payload)))
	b.Write([]byte{0, 0})
	b.Write(payload)
	return b.Bytes()
}

func Emsg(ptsMS uint32, eventID uint32, message string) ([]byte, error) {
	e := &mp4.Emsg{
		FullBox:               mp4.FullBox{Version: 0, Flags: [3]byte{}},
		SchemeIdUri:           "urn:mediarc:synthetic",
		Value:                 "demo",
		Timescale:             1000,
		PresentationTimeDelta: ptsMS,
		EventDuration:         2000,
		Id:                    eventID,
		MessageData:           []byte(message),
	}
	var payload bytes.Buffer
	if _, err := mp4.Marshal(&payload, e, mp4.Context{}); err != nil {
		return nil, err
	}
	var box bytes.Buffer
	_ = binary.Write(&box, binary.BigEndian, uint32(8+payload.Len()))
	emsgType := mp4.BoxTypeEmsg()
	box.Write(emsgType[:])
	box.Write(payload.Bytes())
	return box.Bytes(), nil
}

var seiUUID = [16]byte{0x4d, 0x52, 0x43, 0x46, 0x2d, 0x73, 0x65, 0x69, 0x2d, 0x75, 0x73, 0x65, 0x72, 0x64, 0x61, 0x74}

func SEIUserdata(text string) []byte {
	userPayload := append(append([]byte(nil), seiUUID[:]...), []byte(text)...)
	var sei bytes.Buffer
	sei.WriteByte(5)
	n := len(userPayload)
	for n >= 255 {
		sei.WriteByte(0xff)
		n -= 255
	}
	sei.WriteByte(byte(n))
	n = len(userPayload)
	for n >= 255 {
		sei.WriteByte(0xff)
		n -= 255
	}
	sei.WriteByte(byte(n))
	sei.Write(userPayload)
	var out bytes.Buffer
	out.Write([]byte{0, 0, 0, 1})
	out.WriteByte(0x06) // SEI NAL unit
	out.Write(sei.Bytes())
	return out.Bytes()
}
