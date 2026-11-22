package container

import (
	"archivist/internal/model"
)

// parseColor extracts the colr box nested in the video sample entry.
func parseColor(tr *model.Track, stbl *Box) {
	stsd := FindFirst(stbl.Children, BoxStsd)
	if stsd == nil {
		tr.Color = model.ColorMeta{Present: false}
		return
	}
	fb, ok := AsFullBox(stsd.Payload)
	if !ok || len(fb.Body) < 4 {
		return
	}
	// entry_count(4) + sample entry: 8-byte head + 78-byte visual fields.
	body := fb.Body[4:]
	if len(body) < 86 {
		return
	}
	rest := body[86:]
	colrOff := findRawBox(rest, BoxColr)
	if colrOff < 0 {
		tr.Color = model.ColorMeta{Present: false}
		return
	}
	payload := rest[colrOff+8:]
	if len(payload) < 8 {
		return
	}
	tr.Color = model.ColorMeta{
		Present:   true,
		ColorType: string(payload[0:4]),
		Primaries: U16(payload[4:6]),
		Transfer:  U16(payload[6:8]),
		Matrix:    U16(payload[8:10]),
	}
}

// parseAudio extracts channel count and sample rate from the audio sample
// entry and builds a normalized channel label list.
func parseAudio(tr *model.Track, stbl *Box) {
	stsd := FindFirst(stbl.Children, BoxStsd)
	if stsd == nil {
		return
	}
	fb, ok := AsFullBox(stsd.Payload)
	if !ok || len(fb.Body) < 4 {
		return
	}
	body := fb.Body[4:]
	if len(body) < 36 { // 8 head + 28 AudioSampleEntry
		return
	}
	audio := body[8:36]
	channels := U16(audio[8:10])
	sampleRate := uint32(U16(audio[16:18])) // 16.16 fixed, fixtures use integer
	labels := canonicalLabels(channels)
	tr.Audio = model.ChannelLayout{
		Count:      channels,
		Labels:     labels,
		SampleRate: sampleRate,
		Valid:      len(labels) == int(channels),
	}
}

// canonicalLabels maps channel counts to the standard ITU speaker order.
func canonicalLabels(n uint16) []string {
	table := map[int][]string{
		1: {"C"},
		2: {"L", "R"},
		6: {"L", "R", "C", "LFE", "Ls", "Rs"},
		8: {"L", "R", "C", "LFE", "Ls", "Rs", "Lb", "Rb"},
	}
	if labels, ok := table[int(n)]; ok {
		return labels
	}
	out := make([]string, n)
	for i := range out {
		out[i] = "CH" + itoa(uint64(i + 1))
	}
	return out
}

// findRawBox locates a box header inside raw (non-parsed) bytes.
func findRawBox(data []byte, t FourCC) int {
	for i := 0; i+8 <= len(data); {
		size := int(binaryBE32(data[i : i+4]))
		if size < 8 || i+size > len(data) {
			return -1
		}
		if data[i+4] == t[0] && data[i+5] == t[1] && data[i+6] == t[2] && data[i+7] == t[3] {
			return i
		}
		i += size
	}
	return -1
}

func binaryBE32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
