package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// BuildEmsg builds an emsg v1 payload (without the box header).
func BuildEmsg(seconds float64, scheme, value string, timescale uint32, id uint32, msg []byte) []byte {
	var body bytes.Buffer
	body.WriteByte(1)
	body.Write([]byte{0, 0, 0})
	binary.Write(&body, binary.BigEndian, timescale)
	binary.Write(&body, binary.BigEndian, uint64(seconds*float64(timescale)))
	binary.Write(&body, binary.BigEndian, uint32(1))
	binary.Write(&body, binary.BigEndian, id)
	body.WriteString(scheme)
	body.WriteByte(0)
	body.WriteString(value)
	body.WriteByte(0)
	body.Write(msg)
	return body.Bytes()
}

// BuildID3 builds an ID3v2.3 tag with TIT2 and TXXX frames.
func BuildID3(title, key, value string) []byte {
	var frames bytes.Buffer
	writeFrame := func(id string, text string) {
		var data bytes.Buffer
		data.WriteByte(3) // UTF-8
		data.WriteString(text)
		frames.WriteString(id)
		binary.Write(&frames, binary.BigEndian, uint32(data.Len()))
		frames.Write([]byte{0, 0})
		frames.Write(data.Bytes())
	}
	writeFrame("TIT2", title)
	writeFrame("TXXX", key+"\x00"+value)
	var tag bytes.Buffer
	tag.WriteString("ID3")
	tag.Write([]byte{3, 0, 0})
	size := frames.Len()
	tag.Write([]byte{byte(size >> 21 & 0x7f), byte(size >> 14 & 0x7f), byte(size >> 7 & 0x7f), byte(size & 0x7f)})
	tag.Write(frames.Bytes())
	return tag.Bytes()
}

// BuildWebVTT renders cues as a WebVTT document.
func BuildWebVTT(cues []Cue) []byte {
	var b bytes.Buffer
	b.WriteString("WEBVTT\n\n")
	for i, c := range cues {
		fmt.Fprintf(&b, "cue-%02d\n%s --> %s\n%s\n\n", i, vttStamp(c.StartMS), vttStamp(c.EndMS), c.Text)
	}
	return b.Bytes()
}

// Cue is a fixture subtitle cue.
type Cue struct {
	StartMS int64
	EndMS   int64
	Text    string
}

func vttStamp(ms int64) string {
	h := ms / 3600000
	m := ms / 60000 % 60
	s := ms / 1000 % 60
	r := ms % 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, r)
}

// BuildChapters renders a chapter list.
func BuildChapters(chapters []Chapter) []byte {
	var b bytes.Buffer
	for i, c := range chapters {
		fmt.Fprintf(&b, "CHAPTER%02d=%s\nCHAPTER%02dNAME=%s\n", i+1, vttStamp(c.StartMS), i+1, c.Title)
	}
	return b.Bytes()
}

// Chapter is a fixture chapter mark.
type Chapter struct {
	StartMS int64
	Title   string
}
