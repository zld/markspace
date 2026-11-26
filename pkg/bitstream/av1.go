package bitstream

import (
	"io"
	"strconv"

	"github.com/zld/medcheck/pkg/media"
)

// AV1 OBU types used by the indexer.
const (
	AV1OBUSequenceHeader       = 1
	AV1OBUTemporalDelimiter    = 2
	AV1OBUFrameHeader          = 3
	AV1OBUTileGroup            = 4
	AV1OBUMetadata             = 5
	AV1OBUFrame                = 6
	AV1OBURedundantFrameHeader = 7
)

func init() { RegisterParser(CodecAV1, scanAV1) }

func scanAV1(r io.Reader, track string) (*Result, error) {
	res := &Result{Codec: CodecAV1, TrackID: track}
	s := newByteScanner(r)

	for {
		start, headerOff, hdr, err := readOBUHeader(s, track)
		if err == io.EOF {
			res.Size = s.offset
			return res, nil
		}
		if err != nil {
			if me, ok := err.(*media.Error); ok {
				if me.Sequence < 0 {
					me.Sequence = int64(len(res.Units))
				}
				res.Issues = append(res.Issues, me)
				res.Size = s.offset
				return res, nil
			}
			return nil, err
		}

		u := Unit{Index: len(res.Units), Codec: CodecAV1, Offset: start, HeaderOffset: headerOff}
		hasSize := hdr&0x02 != 0
		var size uint64
		if hasSize {
			size, _, err = readLEB128(s, track)
			if err != nil {
				me := err.(*media.Error)
				me.Sequence = int64(u.Index)
				res.Issues = append(res.Issues, me)
				u.Complete = false
				u.End = s.offset
				u.Length = u.End - u.Offset
				res.Units = append(res.Units, u)
				res.Size = s.offset
				return res, nil
			}
		}

		payloadStart := s.offset
		u.PayloadOffset = payloadStart
		payload, readErr := readExactOrRest(s, size, hasSize)
		if readErr != nil {
			off := start
			if readErr == io.ErrUnexpectedEOF {
				res.Issues.Add(media.CatTruncatedOBU, track, int64(u.Index), off, "OBU type %d declared size %d is truncated", obuType(hdr), size)
			} else if me, ok := readErr.(*media.Error); ok {
				res.Issues = append(res.Issues, me)
			} else {
				return nil, readErr
			}
			u.Complete = false
			u.Kind = av1Name(obuType(hdr))
			u.End = s.offset
			u.Length = u.End - u.Offset
			res.Units = append(res.Units, u)
			res.Size = s.offset
			return res, nil
		}

		u.End = s.offset
		u.Length = u.End - u.Offset
		u.Complete = true
		t := obuType(hdr)
		u.Kind = av1Name(t)
		if t == AV1OBUFrame || t == AV1OBUFrameHeader {
			if ft, ok := av1FrameType(payload); ok && ft == 0 {
				u.Keyframe = true
				u.Kind = "key_frame_" + u.Kind
			}
		}
		res.Units = append(res.Units, u)
	}
}

func readOBUHeader(s *byteScanner, track string) (start, headerEnd int64, hdr byte, err error) {
	b, off, err := s.readAt()
	if err != nil {
		return -1, -1, 0, err
	}
	if b&0x80 != 0 {
		return off, s.offset, b, media.NewError(media.CatIllegalLength, track, -1, off, "AV1 forbidden_zero_bit is set")
	}
	headerEnd = s.offset
	if b&0x04 != 0 { // extension flag
		_, extOff, extErr := s.readAt()
		if extErr != nil {
			return off, headerEnd, b, media.NewError(media.CatTruncatedOBU, track, -1, extOff, "AV1 extension header truncated")
		}
		headerEnd = s.offset
	}
	return off, headerEnd, b, nil
}

func obuType(h byte) byte { return (h >> 3) & 0x0f }

func readLEB128(s *byteScanner, track string) (uint64, int, error) {
	var value uint64
	var shift uint
	n := 0
	for i := 0; ; i++ {
		b, off, err := s.readAt()
		if err != nil {
			return 0, n, media.NewError(media.CatTruncatedOBU, track, -1, s.offset, "AV1 LEB128 size truncated near byte %d", off)
		}
		n++
		if i >= 10 {
			return 0, n, media.NewError(media.CatIllegalLength, track, -1, off, "AV1 LEB128 size is too long")
		}
		if i == 9 && (b&0x80 != 0 || b&0x7e != 0) {
			return 0, n, media.NewError(media.CatIllegalLength, track, -1, off, "AV1 LEB128 size overflows uint64")
		}
		value |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return value, n, nil
		}
		shift += 7
	}
}

func readExactOrRest(s *byteScanner, size uint64, hasSize bool) ([]byte, error) {
	if hasSize {
		buf := make([]byte, size)
		for i := uint64(0); i < size; i++ {
			b, _, err := s.readAt()
			if err != nil {
				return buf[:i], io.ErrUnexpectedEOF
			}
			buf[i] = b
		}
		return buf, nil
	}
	return io.ReadAll(s.r)
}

func av1FrameType(payload []byte) (byte, bool) {
	if len(payload) < 1 {
		return 0, false
	}
	if payload[0]&0x80 != 0 { // show_existing_frame
		return 0, false
	}
	return (payload[0] >> 5) & 0x03, true
}

func av1Name(t byte) string {
	names := map[byte]string{
		1: "sequence_header", 2: "temporal_delimiter", 3: "frame_header",
		4: "tile_group", 5: "metadata", 6: "frame", 7: "redundant_frame_header",
		8: "tile_list", 15: "padding",
	}
	if n, ok := names[t]; ok {
		return n
	}
	return "obu_" + strconv.Itoa(int(t))
}
