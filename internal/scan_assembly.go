package medpipe

import (
	"bytes"

	"github.com/pion/rtp"
)

// ingest handles one RTP packet: frame reassembly, metadata capture and a
// single clock normalization per access unit, flushing the prior unit first.
func (s *Scanner) ingest(pkt *rtp.Packet, off int64) *PipelineError {
	if len(s.pending) > maxBufferedFrames {
		return s.b.FatalAt(CodeInvalidRTP, "frame reassembly buffer exceeded cap",
			"increase chunk throughput or fix the missing marker bits", off)
	}
	if s.havePend && pkt.Timestamp != s.pendTS {
		if perr := s.flushPending(); perr != nil {
			return perr
		}
	}
	if !s.havePend {
		s.pendOff, s.pendSeq, s.pendTS = off, pkt.SequenceNumber, pkt.Timestamp
		s.havePend = true
	}
	s.pending = append(s.pending, pkt.Payload)
	if pkt.Marker {
		// A metadata-only marker group keeps the access unit open so the
		// coded frame sharing its timestamp can still arrive.
		joined := bytes.Join(s.pending, nil)
		if _, isMeta := classifyFrame(s.opts.Codec, joined); isMeta {
			if fm, err := parseMetaPayload(metaBody(s.opts.Codec, joined)); err == nil {
				s.pendMeta = &fm
			}
			s.pending = nil
			return nil
		}
		if perr := s.flushPending(); perr != nil {
			return perr
		}
	}
	return nil
}

// finalizeUnit performs the single clock sample for one coded access unit.
func (s *Scanner) finalizeUnit(payload []byte) *PipelineError {
	rec, err := s.clock.Push(s.pendSeq, s.pendTS)
	if err != nil {
		if pe, ok := err.(*PipelineError); ok {
			pe.OffsetSet, pe.Offset = true, s.pendOff
			return pe
		}
		return s.b.FatalAt(CodeTimestampRepair, err.Error(), "repair the timestamp track", s.pendOff)
	}
	s.emit(payload, s.pendOff, *rec)
	return nil
}

// flushPending finalizes the access unit at a timestamp/marker boundary.
func (s *Scanner) flushPending() *PipelineError {
	if !s.havePend {
		return nil
	}
	payload := bytes.Join(s.pending, nil)
	perr := s.finalizeUnit(payload)
	s.pending, s.havePend, s.pendMeta = nil, false, nil
	return perr
}

// emit classifies the complete unit and appends it. Metadata units update the
// next coded frame's metadata instead of becoming index entries.
func (s *Scanner) emit(payload []byte, off int64, rec SampleRecord) {
	keyframe, isMeta := classifyFrame(s.opts.Codec, payload)
	if isMeta {
		fm, err := parseMetaPayload(metaBody(s.opts.Codec, payload))
		if err == nil {
			s.pendMeta = &fm
		}
		return
	}
	f := Frame{
		Codec:      s.opts.Codec,
		Keyframe:   keyframe,
		ByteOffset: off,
		RawPayload: append([]byte(nil), payload...),
	}
	if s.pendMeta != nil {
		f.Meta, f.HasMeta = *s.pendMeta, true
		s.pendMeta = nil
	}
	s.result.Units = append(s.result.Units, UnitEvent{Frame: f, Sample: rec})
}

// metaBody strips the codec-specific NAL/OBU header bytes before TLV parsing.
func metaBody(codec Codec, payload []byte) []byte {
	switch codec {
	case CodecH264:
		return payload[1:]
	case CodecH265:
		return payload[2:]
	default: // AV1: 1-byte OBU header
		if len(payload) > 1 {
			return payload[1:]
		}
		return payload
	}
}
