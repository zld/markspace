package ebml

import (
	"errors"
	"io"
	"math"
)

// childFn receives a child element's ID, payload and the absolute offset of
// the payload. payloadOff is valid even for zero-length payloads.
type childFn func(id uint32, payload []byte, payloadOff int64) error

// walkChildren iterates the direct children of an in-memory metadata element.
// Unknown or overrunning child sizes are fatal: metadata is never silently
// dropped.
func walkChildren(body []byte, base int64, owner string, fn childFn) error {
	off := base
	for len(body) > 0 {
		id, idw, err := ParseID(body)
		if err != nil {
			return parseError(ErrKindInvalidVINT, off, id, 0, "invalid "+owner+" child id", err)
		}
		sz, err := ParseSize(body[idw:])
		if err != nil {
			return parseError(ErrKindInvalidVINT, off+int64(idw), id, 0, "invalid "+owner+" child size", err)
		}
		need := int64(idw + sz.Width)
		if sz.Unknown || uint64(len(body)) < uint64(need)+sz.Value {
			return parseError(ErrKindTruncated, off, id, 0, owner+" child exceeds parent", io.ErrUnexpectedEOF)
		}
		payloadLen := int64(sz.Value)
		if err := fn(id, body[need:need+payloadLen], off+need); err != nil {
			return err
		}
		off += need + payloadLen
		body = body[need+payloadLen:]
	}
	return nil
}

func (p *parser) readEBMLHeader(h Header) error {
	body, err := p.rd.ReadPayload(h, p.opts.MaxMetadataSize)
	if err != nil {
		return p.wrapErr(h, err)
	}
	p.elements++
	err = walkChildren(body, h.DataOffset, "EBML", func(id uint32, payload []byte, off int64) error {
		p.elements++
		switch id {
		case IDDocType:
			p.doc.DocType = string(payload)
		case IDDocTypeVersion:
			p.doc.DocTypeVer = readUint(payload)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if p.doc.DocType != "matroska" && p.doc.DocType != "webm" {
		return parseError(ErrKindStructure, h.Offset, h.ID, 0, "unsupported DocType "+p.doc.DocType, nil)
	}
	return nil
}

// Span returns the element's full byte span including the header. For
// unknown-size elements the length covers the header only; the payload is
// unbounded and is tracked by the parser cursor instead.
func (h Header) Span() Span {
	length := h.HeaderSize
	if !h.UnknownSize {
		length += int64(h.Size)
	}
	return Span{Offset: h.Offset, Length: length}
}

func (p *parser) readSegment(h Header) error {
	if p.doc.Segment.Span.Length != 0 {
		return parseError(ErrKindStructure, h.Offset, h.ID, 0, "multiple Segment elements are not supported", nil)
	}
	p.doc.Segment = Segment{Span: h.Span(), UnknownSize: h.UnknownSize}
	p.elements++
	end := int64(math.MaxInt64)
	if !h.UnknownSize {
		end = h.DataOffset + int64(h.Size)
	}
	for {
		if !h.UnknownSize && p.rd.Offset() >= end {
			return nil
		}
		child, err := p.next()
		if errors.Is(err, io.EOF) {
			if !h.UnknownSize {
				return parseError(ErrKindTruncated, end, h.ID, 0, "segment truncated before declared end", io.ErrUnexpectedEOF)
			}
			return nil
		}
		if err != nil {
			return p.wrapErr(child, err)
		}
		if !h.UnknownSize && child.Offset >= end {
			return parseError(ErrKindStructure, child.Offset, child.ID, 0, "element starts past segment end", nil)
		}
		switch child.ID {
		case IDInfo:
			if err := p.readInfo(child); err != nil {
				return err
			}
		case IDTracks:
			if err := p.readTracks(child); err != nil {
				return err
			}
		case IDCluster:
			if err := p.readCluster(child); err != nil {
				return err
			}
		default:
			if err := p.discard(child); err != nil {
				return err
			}
		}
	}
}

func (p *parser) readInfo(h Header) error {
	body, err := p.rd.ReadPayload(h, p.opts.MaxMetadataSize)
	if err != nil {
		return p.wrapErr(h, err)
	}
	p.elements++
	return walkChildren(body, h.DataOffset, "Info", func(id uint32, payload []byte, off int64) error {
		p.elements++
		switch id {
		case IDTimecodeScale:
			p.doc.TimecodeScale = readUint(payload)
			if p.doc.TimecodeScale == 0 {
				return parseError(ErrKindStructure, off, id, 0, "TimecodeScale is zero", nil)
			}
		case IDDuration:
			p.doc.Duration = readFloat(payload)
		}
		return nil
	})
}

func (p *parser) readTracks(h Header) error {
	body, err := p.rd.ReadPayload(h, p.opts.MaxMetadataSize)
	if err != nil {
		return p.wrapErr(h, err)
	}
	p.elements++
	err = walkChildren(body, h.DataOffset, "Tracks", func(id uint32, payload []byte, off int64) error {
		p.elements++
		if id != IDTrackEntry {
			return nil
		}
		tr, err := parseTrackEntry(payload, off)
		if err != nil {
			return err
		}
		p.doc.Tracks = append(p.doc.Tracks, tr)
		return nil
	})
	if err != nil {
		return err
	}
	if len(p.doc.Tracks) == 0 {
		return parseError(ErrKindStructure, h.Offset, h.ID, 0, "document has no tracks", nil)
	}
	return nil
}

func parseTrackEntry(body []byte, base int64) (Track, error) {
	var tr Track
	err := walkChildren(body, base, "TrackEntry", func(id uint32, payload []byte, off int64) error {
		switch id {
		case IDTrackNumber:
			tr.Number = readUint(payload)
		case IDTrackUID:
			tr.UID = readUint(payload)
		case IDTrackType:
			tr.Type = TrackType(readUint(payload))
		case IDCodecID:
			tr.CodecID = string(payload)
		case IDDefaultDuration:
			tr.DefaultDuration = readUint(payload)
		case IDVideo:
			w, h, err := parseVideo(payload, off)
			if err != nil {
				return err
			}
			tr.PixelWidth, tr.PixelHeight = w, h
		case IDAudio:
			f, ch, err := parseAudio(payload, off)
			if err != nil {
				return err
			}
			tr.SamplingFreq, tr.Channels = f, ch
		}
		return nil
	})
	if err != nil {
		return tr, err
	}
	if tr.Number == 0 {
		return tr, parseError(ErrKindStructure, base, IDTrackEntry, 0, "TrackEntry has no TrackNumber", nil)
	}
	return tr, nil
}

func parseVideo(body []byte, base int64) (w, h uint64, err error) {
	err = walkChildren(body, base, "Video", func(id uint32, payload []byte, off int64) error {
		switch id {
		case IDPixelWidth:
			w = readUint(payload)
		case IDPixelHeight:
			h = readUint(payload)
		}
		return nil
	})
	return w, h, err
}

func parseAudio(body []byte, base int64) (freq float64, channels uint64, err error) {
	err = walkChildren(body, base, "Audio", func(id uint32, payload []byte, off int64) error {
		switch id {
		case IDSamplingFreq:
			freq = readFloat(payload)
		case IDChannels:
			channels = readUint(payload)
		}
		return nil
	})
	return freq, channels, err
}
