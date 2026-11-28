package media

import (
	"bytes"
	"fmt"

	"github.com/abema/go-mp4"

	"mediad/timeline"
)

// EmsgInfo is one parsed emsg box. Version 1 carries an absolute presentation
// time in the box's own timescale; version 0 carries a delta from the segment's
// earliest presentation time, which the caller supplies.
type EmsgInfo struct {
	Version          uint8
	Timescale        uint32
	PresentationTime uint64
	DeltaTime        uint32
	EventDuration    uint32
	ID               uint32
	Scheme           string
	Value            string
	Message          []byte
	Offset           int64
	Size             int64
}

// ParseEmsg parses one emsg box payload. A declared string or message length
// that runs past the box is a located invalid_length issue rather than a
// partial parse.
func ParseEmsg(payload []byte, path string, offset int64) (EmsgInfo, error) {
	var info EmsgInfo
	info.Offset = offset
	info.Size = int64(len(payload))
	if len(payload) < 4 {
		return info, &timeline.LocatedError{Issue: timeline.Issue{
			Severity: timeline.SeverityError, Code: timeline.CodeTruncated, File: path, ByteOffset: offset,
			Message: fmt.Sprintf("emsg payload is %d bytes, too short for version/flags", len(payload))}}
	}
	version := payload[0]
	info.Version = version
	r := bytes.NewReader(payload)
	var box mp4.Emsg
	if _, err := mp4.Unmarshal(r, uint64(len(payload)), &box, mp4.Context{}); err != nil {
		return info, &timeline.LocatedError{Issue: timeline.Issue{
			Severity: timeline.SeverityError, Code: timeline.CodeInvalidLength, File: path, ByteOffset: offset,
			Message: "emsg: " + err.Error()}, Err: err}
	}
	info.Timescale = box.Timescale
	info.EventDuration = box.EventDuration
	info.ID = box.Id
	info.Message = box.MessageData
	info.Scheme = box.SchemeIdUri
	info.Value = box.Value
	if version == 0 {
		info.DeltaTime = box.PresentationTimeDelta
	} else {
		info.PresentationTime = box.PresentationTime
	}
	if info.Timescale == 0 {
		return info, &timeline.LocatedError{Issue: timeline.Issue{
			Severity: timeline.SeverityError, Code: timeline.CodeInvalidTime, File: path, ByteOffset: offset,
			Message: "emsg timescale is zero"}}
	}
	return info, nil
}

// EmsgToEvent places an emsg on the shared timeline. For version 0 the
// caller's baseTime is added; for version 1 the box time is absolute.
func EmsgToEvent(info EmsgInfo, path string, baseTime uint64) (timeline.Event, error) {
	ticks := int64(info.PresentationTime)
	if info.Version == 0 {
		ticks = int64(baseTime) + int64(info.DeltaTime)
	}
	base := timeline.Base{Name: "emsg", TimeScale: info.Timescale}
	at := timeline.Timestamp{Ticks: ticks, Base: base}
	secs, err := at.Seconds()
	if err != nil {
		return timeline.Event{}, err
	}
	var dur timeline.Rational
	if info.EventDuration > 0 {
		dur, err = timeline.NewRational(int64(info.EventDuration), int64(info.Timescale))
		if err != nil {
			return timeline.Event{}, err
		}
	}
	return timeline.Event{
		Kind:         timeline.KindEmsg,
		Time:         at,
		Presentation: secs,
		Duration:     dur,
		Scheme:       info.Scheme,
		Payload:      append([]byte(nil), info.Message...),
		Source:       timeline.Source{File: path, ByteOffset: info.Offset, ByteLength: info.Size, Box: "emsg"},
	}, nil
}

// ID3Frame is one frame from an ID3v2 tag.
type ID3Frame struct {
	ID     string
	Data   []byte
	Offset int64
}

// ParseID3 parses an ID3v2.3/2.4 tag. It validates the synchsafe size against
// the buffer, so a truncated tag is rejected with its declared length rather
// than yielding half a frame list.
func ParseID3(data []byte, path string, offset int64) ([]ID3Frame, error) {
	if len(data) < 10 || string(data[:3]) != "ID3" {
		return nil, &timeline.LocatedError{Issue: timeline.Issue{
			Severity: timeline.SeverityError, Code: timeline.CodeUnsupported, File: path, ByteOffset: offset,
			Message: "not an ID3v2 tag"}}
	}
	version := data[3]
	if version != 3 && version != 4 {
		return nil, &timeline.LocatedError{Issue: timeline.Issue{
			Severity: timeline.SeverityError, Code: timeline.CodeUnsupported, File: path, ByteOffset: offset,
			Message: fmt.Sprintf("unsupported ID3v2.%d", version)}}
	}
	tagSize := int(synchsafe(data[6:10]))
	if 10+tagSize > len(data) {
		return nil, &timeline.LocatedError{Issue: timeline.Issue{
			Severity: timeline.SeverityError, Code: timeline.CodeTruncated, File: path, ByteOffset: offset,
			Message: fmt.Sprintf("ID3 tag declares %d bytes but only %d present", tagSize, len(data)-10)}}
	}
	body := data[10 : 10+tagSize]
	var frames []ID3Frame
	pos := 0
	frameHeaderLen := 10
	for pos+frameHeaderLen <= len(body) {
		if body[pos] == 0 {
			break // padding
		}
		id := string(body[pos : pos+4])
		sizeBytes := body[pos+4 : pos+8]
		var size int
		if version == 4 {
			size = int(synchsafe(sizeBytes))
		} else {
			size = int(uint32(sizeBytes[0])<<24 | uint32(sizeBytes[1])<<16 | uint32(sizeBytes[2])<<8 | uint32(sizeBytes[3]))
		}
		start := pos + frameHeaderLen
		if size < 0 || start+size > len(body) {
			return nil, &timeline.LocatedError{Issue: timeline.Issue{
				Severity: timeline.SeverityError, Code: timeline.CodeInvalidLength, File: path,
				ByteOffset: offset + int64(10+pos),
				Message:    fmt.Sprintf("ID3 frame %q declares %d bytes past tag body", id, size)}}
		}
		frames = append(frames, ID3Frame{ID: id, Data: append([]byte(nil), body[start:start+size]...), Offset: offset + int64(10+start)})
		pos = start + size
	}
	return frames, nil
}

// ID3FramesToEvents turns paired timestamp/frame data into events. Channel
// layouts differ; callers supply the frame's own media timestamp in ticks.
func ID3FramesToEvents(frames []ID3Frame, path string, base timeline.Base, ticks []int64) ([]timeline.Event, timeline.Issues) {
	var events []timeline.Event
	var issues timeline.Issues
	for i, f := range frames {
		if i >= len(ticks) {
			issues.Add(timeline.Issue{Severity: timeline.SeverityError, Code: timeline.CodeInvalidTime,
				File: path, FrameIndex: i, Message: "ID3 frame has no timestamp"})
			continue
		}
		ev, err := timeline.NewEvent(timeline.KindID3, timeline.Timestamp{Ticks: ticks[i], Base: base},
			timeline.Rational{}, f.ID, f.Data, timeline.Source{File: path, ByteOffset: f.Offset, Box: "ID3", FrameIndex: i})
		if err != nil {
			issues.AddError(timeline.CodeInvalidTime, err)
			continue
		}
		events = append(events, ev)
	}
	return events, issues
}

func synchsafe(b []byte) uint32 {
	return uint32(b[0]&0x7f)<<21 | uint32(b[1]&0x7f)<<14 | uint32(b[2]&0x7f)<<7 | uint32(b[3]&0x7f)
}
