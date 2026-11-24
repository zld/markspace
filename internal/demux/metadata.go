package demux

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/abema/go-mp4"

	"mediarc/internal/model"
)

// ParseEvent validates and normalizes the supported timed metadata payloads.
// It never silently accepts a truncated or unknown critical structure.
func ParseEvent(ev *model.TimedEvent) error {
	var err error
	switch ev.Kind {
	case model.MetaSubtitle:
		err = parseWebVTT(ev)
	case model.MetaChapter:
		err = parseChapters(ev)
	case model.MetaID3:
		err = parseID3(ev)
	case model.MetaEmsg:
		err = parseEmsg(ev)
	case model.MetaSEI:
		err = parseSEI(ev)
	default:
		err = model.NewError(model.StageDemux, model.CodeUnsupported,
			fmt.Sprintf("unsupported metadata kind %q", ev.Kind), nil)
	}
	if err != nil {
		if me, ok := model.AsMediaError(err); ok {
			return model.AtTrack(model.AtOffset(me, ev.SourceOffset), ev.TrackID)
		}
		return err
	}
	return nil
}

func parseWebVTT(ev *model.TimedEvent) error {
	s := string(ev.Payload)
	if !strings.HasPrefix(s, "WEBVTT") {
		return model.NewError(model.StageDemux, model.CodeCorruptPayload, "missing WEBVTT signature", nil)
	}
	if !strings.Contains(s, "-->") {
		return model.NewError(model.StageDemux, model.CodeTruncated, "subtitle has no cue timing", nil)
	}
	lines := strings.Split(s, "\n")
	var texts []string
	inCueText := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "-->") {
			inCueText = true
			continue
		}
		if inCueText && line != "" && !strings.HasPrefix(line, "NOTE") {
			texts = append(texts, line)
		} else if line == "" {
			inCueText = false
		}
	}
	if len(texts) == 0 {
		return model.NewError(model.StageDemux, model.CodeTruncated, "subtitle cue has no text", nil)
	}
	ev.Text = strings.Join(texts, " / ")
	if ev.Fields == nil {
		ev.Fields = map[string]string{}
	}
	ev.Fields["format"] = "webvtt"
	return nil
}

func parseChapters(ev *model.TimedEvent) error {
	b := ev.Payload
	if len(b) < 6 || string(b[:4]) != "MRCH" {
		return model.NewError(model.StageDemux, model.CodeCorruptPayload, "bad chapter signature", nil)
	}
	count := int(binary.BigEndian.Uint16(b[4:6]))
	pos := 6
	var titles []string
	for i := 0; i < count; i++ {
		if pos+10 > len(b) {
			return model.NewError(model.StageDemux, model.CodeTruncated, "chapter entry header truncated", nil)
		}
		// ptsMS uint64 followed by uint16 title length
		titleLen := int(binary.BigEndian.Uint16(b[pos+8 : pos+10]))
		pos += 10
		if pos+titleLen > len(b) {
			return model.NewError(model.StageDemux, model.CodeTruncated, "chapter title truncated", nil)
		}
		titles = append(titles, string(b[pos:pos+titleLen]))
		pos += titleLen
	}
	ev.Text = strings.Join(titles, " | ")
	if ev.Fields == nil {
		ev.Fields = map[string]string{}
	}
	ev.Fields["chapter_count"] = fmt.Sprint(count)
	return nil
}

func parseID3(ev *model.TimedEvent) error {
	b := ev.Payload
	if len(b) < 10 || string(b[:3]) != "ID3" {
		return model.NewError(model.StageDemux, model.CodeCorruptPayload, "missing ID3 signature", nil)
	}
	if b[3] != 3 && b[3] != 4 {
		return model.NewError(model.StageDemux, model.CodeUnsupported,
			fmt.Sprintf("ID3v2.%d is not supported", b[3]), nil)
	}
	size := synchsafe32(b[6:10])
	if int(size)+10 > len(b) {
		return model.NewError(model.StageDemux, model.CodeTruncated,
			fmt.Sprintf("ID3 tag size %d exceeds payload %d", size, len(b)-10), nil)
	}
	pos := 10
	frames := 0
	if ev.Fields == nil {
		ev.Fields = map[string]string{}
	}
	for pos < 10+int(size) {
		if pos+10 > len(b) {
			return model.NewError(model.StageDemux, model.CodeTruncated, "ID3 frame header truncated", nil)
		}
		id := string(b[pos : pos+4])
		if id == "\x00\x00\x00\x00" {
			break
		}
		frameSize := int(binary.BigEndian.Uint32(b[pos+4 : pos+8]))
		if b[3] == 4 {
			frameSize = int(synchsafe32(b[pos+4 : pos+8]))
		}
		pos += 10
		if pos+frameSize > len(b) {
			return model.NewError(model.StageDemux, model.CodeTruncated,
				fmt.Sprintf("ID3 frame %s truncated", id), nil)
		}
		frame := b[pos : pos+frameSize]
		if len(frame) > 1 && strings.HasPrefix(id, "T") {
			text := strings.Trim(string(frame[1:]), "\x00")
			ev.Fields["id3:"+id] = text
			if id == "TIT2" {
				ev.Text = text
			}
		} else {
			ev.Fields["id3:"+id] = fmt.Sprintf("%d-bytes", frameSize)
		}
		frames++
		pos += frameSize
	}
	ev.Fields["id3_frames"] = fmt.Sprint(frames)
	return nil
}

func synchsafe32(b []byte) uint32 {
	return uint32(b[0]&0x7f)<<21 | uint32(b[1]&0x7f)<<14 |
		uint32(b[2]&0x7f)<<7 | uint32(b[3]&0x7f)
}

func parseEmsg(ev *model.TimedEvent) error {
	r := bytes.NewReader(ev.Payload)
	bi, err := mp4.ReadBoxInfo(r)
	if err != nil {
		return model.NewError(model.StageDemux, model.CodeTruncated, "emsg box header truncated", err)
	}
	if bi.Type != mp4.BoxTypeEmsg() {
		return model.NewError(model.StageDemux, model.CodeUnsupported,
			fmt.Sprintf("expected emsg, got %s", bi.Type.String()), nil)
	}
	var emsg mp4.Emsg
	if _, err := mp4.Unmarshal(r, bi.Size-bi.HeaderSize, &emsg, mp4.Context{}); err != nil {
		return model.NewError(model.StageDemux, model.CodeTruncated, "emsg payload truncated or invalid", err)
	}
	if ev.Fields == nil {
		ev.Fields = map[string]string{}
	}
	ev.Fields["scheme_id_uri"] = emsg.SchemeIdUri
	ev.Fields["value"] = emsg.Value
	ev.Fields["timescale"] = fmt.Sprint(emsg.Timescale)
	ev.Fields["event_id"] = fmt.Sprint(emsg.Id)
	ev.Fields["event_duration"] = fmt.Sprint(emsg.EventDuration)
	ev.Text = string(emsg.MessageData)
	return nil
}

func parseSEI(ev *model.TimedEvent) error {
	b := ev.Payload
	start := bytes.Index(b, []byte{0, 0, 0, 1})
	if start < 0 {
		start = bytes.Index(b, []byte{0, 0, 1})
		if start < 0 {
			return model.NewError(model.StageDemux, model.CodeCorruptPayload, "SEI start code missing", nil)
		}
		start += 3
	} else {
		start += 4
	}
	if start >= len(b) {
		return model.NewError(model.StageDemux, model.CodeTruncated, "SEI NAL header missing", nil)
	}
	nalType := b[start] & 0x1f
	if nalType != 6 {
		return model.NewError(model.StageDemux, model.CodeUnsupported, fmt.Sprintf("NAL type %d is not SEI", nalType), nil)
	}
	pos := start + 1
	seiType := 0
	for pos < len(b) && b[pos] == 0xff {
		seiType += 255
		pos++
	}
	if pos >= len(b) {
		return model.NewError(model.StageDemux, model.CodeTruncated, "SEI type truncated", nil)
	}
	seiType += int(b[pos])
	pos++
	seiSize := 0
	for pos < len(b) && b[pos] == 0xff {
		seiSize += 255
		pos++
	}
	if pos >= len(b) {
		return model.NewError(model.StageDemux, model.CodeTruncated, "SEI size truncated", nil)
	}
	seiSize += int(b[pos])
	pos++
	if pos+seiSize > len(b) {
		return model.NewError(model.StageDemux, model.CodeTruncated, "SEI payload truncated", nil)
	}
	payload := b[pos : pos+seiSize]
	if ev.Fields == nil {
		ev.Fields = map[string]string{}
	}
	ev.Fields["nal_type"] = fmt.Sprint(nalType)
	ev.Fields["sei_type"] = fmt.Sprint(seiType)
	if seiType == 5 && len(payload) > 16 {
		ev.Fields["uuid"] = fmt.Sprintf("%x", payload[:16])
		ev.Text = string(payload[16:])
	} else {
		ev.Text = string(payload)
	}
	return nil
}
