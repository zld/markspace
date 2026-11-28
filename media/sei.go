package media

import (
	"fmt"

	"mediad/timeline"
)

// SEIMessage is one SEI NAL unit found in a sample's Annex-B byte stream.
type SEIMessage struct {
	PayloadType uint8
	Payload     []byte
	// UUID payloads carry a 16-byte identifier followed by the opaque body.
	UUID []byte
	Body []byte
	// SampleIndex locates the NAL unit inside the sample.
	NALIndex int
	Offset   int64
}

// ParseSEI scans an Annex-B sample for SEI NAL units (type 6) and returns their
// messages. It understands user_data_unregistered (type 5) payloads, which is
// how timed metadata such as SCTE-35 and HDR static data travel in-band. A NAL
// unit whose length prefix or emulation-prevention payload is malformed is
// reported with its byte offset, and the rest of the sample is still scanned.
func ParseSEI(sample []byte, path string, trackID uint32, sampleOffset int64) ([]SEIMessage, timeline.Issues) {
	var messages []SEIMessage
	var issues timeline.Issues
	startCode := func(i int) (int, int, bool) {
		if i+3 <= len(sample) && sample[i] == 0 && sample[i+1] == 0 && sample[i+2] == 1 {
			return 3, i + 3, true
		}
		if i+4 <= len(sample) && sample[i] == 0 && sample[i+1] == 0 && sample[i+2] == 0 && sample[i+3] == 1 {
			return 4, i + 4, true
		}
		return 0, 0, false
	}
	nalIndex := 0
	for pos := 0; pos < len(sample); {
		hdr, payloadStart, ok := startCode(pos)
		if !ok {
			next, _, found := findStartCode(sample, pos+1)
			if !found {
				break
			}
			pos = next
			continue
		}
		nalIndex++
		if payloadStart >= len(sample) {
			break
		}
		nalType := sample[payloadStart] & 0x1f
		nextStart, _, found := findStartCode(sample, payloadStart+1)
		end := len(sample)
		if found {
			end = nextStart
		}
		if nalType == 6 {
			body := stripEmulationPrevention(sample[payloadStart+1 : end])
			msgs, err := parseSEIPayloads(body)
			if err != nil {
				issues.Add(timeline.Issue{Severity: timeline.SeverityError, Code: timeline.CodeInvalidLength,
					File: path, TrackID: trackID, ByteOffset: sampleOffset + int64(payloadStart),
					Message: fmt.Sprintf("SEI NAL %d: %v", nalIndex, err)})
			} else {
				for _, m := range msgs {
					m.Offset = sampleOffset + int64(payloadStart)
					m.NALIndex = nalIndex
					messages = append(messages, m)
				}
			}
		}
		pos = end
		_ = hdr
	}
	return messages, issues
}

func findStartCode(buf []byte, from int) (int, int, bool) {
	for i := from; i+3 <= len(buf); i++ {
		if buf[i] == 0 && buf[i+1] == 0 {
			if buf[i+2] == 1 {
				return i, 3, true
			}
			if i+4 <= len(buf) && buf[i+2] == 0 && buf[i+3] == 1 {
				return i, 4, true
			}
		}
	}
	return 0, 0, false
}

// stripEmulationPrevention removes 0x03 from 00 00 03 sequences.
func stripEmulationPrevention(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if i+2 < len(b) && b[i] == 0 && b[i+1] == 0 && b[i+2] == 3 {
			out = append(out, 0, 0)
			i += 2
			continue
		}
		out = append(out, b[i])
	}
	return out
}

func parseSEIPayloads(body []byte) ([]SEIMessage, error) {
	var out []SEIMessage
	pos := 0
	for pos < len(body) {
		payloadType := 0
		for pos < len(body) && body[pos] == 0xff {
			payloadType += 255
			pos++
		}
		if pos >= len(body) {
			return nil, fmt.Errorf("truncated payload_type")
		}
		payloadType += int(body[pos])
		pos++

		payloadSize := 0
		for pos < len(body) && body[pos] == 0xff {
			payloadSize += 255
			pos++
		}
		if pos >= len(body) {
			return nil, fmt.Errorf("truncated payload_size")
		}
		payloadSize += int(body[pos])
		pos++
		if pos+payloadSize > len(body) {
			return nil, fmt.Errorf("payload of %d bytes runs past SEI (%d left)", payloadSize, len(body)-pos)
		}
		msg := SEIMessage{PayloadType: uint8(payloadType), Payload: append([]byte(nil), body[pos:pos+payloadSize]...)}
		if payloadType == 5 && payloadSize >= 16 {
			msg.UUID = msg.Payload[:16]
			msg.Body = msg.Payload[16:]
		} else {
			msg.Body = msg.Payload
		}
		out = append(out, msg)
		pos += payloadSize
		// rbsp_trailing_bits: a lone 0x80 byte ends the payload list.
		if pos < len(body) && body[pos] == 0x80 {
			pos++
			if pos < len(body) && body[pos] == 0 {
				pos++
			}
		}
	}
	return out, nil
}

// SEIToEvents turns SEI user-data messages into events at the sample's time.
func SEIToEvents(msgs []SEIMessage, path string, trackID uint32, base timeline.Base, ticks int64) ([]timeline.Event, timeline.Issues) {
	var events []timeline.Event
	var issues timeline.Issues
	for i, m := range msgs {
		if len(m.UUID) != 16 {
			continue // only registered user data carries application metadata
		}
		scheme := fmt.Sprintf("sei-uuid-%x", m.UUID[:4])
		ev, err := timeline.NewEvent(timeline.KindSEI, timeline.Timestamp{Ticks: ticks, Base: base},
			timeline.Rational{}, scheme, m.Body,
			timeline.Source{File: path, TrackID: trackID, ByteOffset: m.Offset, Box: "sei", FrameIndex: i + 1})
		if err != nil {
			issues.AddError(timeline.CodeInvalidTime, err)
			continue
		}
		events = append(events, ev)
	}
	return events, issues
}
