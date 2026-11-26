package cmaf

import (
	"encoding/binary"
	"io"

	"example.com/archiveverify/internal/model"
)

func parseTRUN(r io.ReaderAt, source string, b BoxHeader, tf *TrackFragment) ([]Sample, error) {
	// TRUN can contain many entries; bound by box payload already admitted.
	buf := make([]byte, b.PayloadSize)
	if _, err := r.ReadAt(buf, b.PayloadOffset); err != nil && err != io.EOF {
		return nil, model.NewParseError(source, "trun", b.Offset, b.PayloadOffset, err)
	}
	if int64(len(buf)) != b.PayloadSize || len(buf) < 8 {
		return nil, model.NewParseError(source, "trun", b.Offset, b.PayloadOffset+int64(len(buf)),
			io.ErrUnexpectedEOF)
	}
	version := buf[0]
	flags := uint32(buf[1])<<16 | uint32(buf[2])<<8 | uint32(buf[3])
	count := binary.BigEndian.Uint32(buf[4:8])
	pos := 8
	if flags&0x000001 != 0 { // data-offset, signed but not needed for duration extraction
		if len(buf)-pos < 4 {
			return nil, io.ErrUnexpectedEOF
		}
		pos += 4
	}
	var firstFlags uint32
	if flags&0x000004 != 0 {
		if len(buf)-pos < 4 {
			return nil, io.ErrUnexpectedEOF
		}
		firstFlags = binary.BigEndian.Uint32(buf[pos : pos+4])
		pos += 4
	}

	out := make([]Sample, 0, count)
	wantDur := flags&0x000100 != 0
	wantSize := flags&0x000200 != 0
	wantFlags := flags&0x000400 != 0
	wantCTO := flags&0x000800 != 0
	for i := uint32(0); i < count; i++ {
		s := Sample{Index: int(i), Duration: tf.DefaultDuration, Size: tf.DefaultSize, Flags: tf.DefaultFlags}
		if i == 0 && flags&0x000004 != 0 {
			s.Flags = firstFlags
		}
		if wantDur {
			if len(buf)-pos < 4 {
				return nil, model.NewParseError(source, "trun", b.Offset, b.PayloadOffset+int64(pos), io.ErrUnexpectedEOF)
			}
			s.Duration = binary.BigEndian.Uint32(buf[pos : pos+4])
			pos += 4
		}
		if wantSize {
			if len(buf)-pos < 4 {
				return nil, model.NewParseError(source, "trun", b.Offset, b.PayloadOffset+int64(pos), io.ErrUnexpectedEOF)
			}
			s.Size = binary.BigEndian.Uint32(buf[pos : pos+4])
			pos += 4
		}
		if wantFlags {
			if len(buf)-pos < 4 {
				return nil, model.NewParseError(source, "trun", b.Offset, b.PayloadOffset+int64(pos), io.ErrUnexpectedEOF)
			}
			s.Flags = binary.BigEndian.Uint32(buf[pos : pos+4])
			pos += 4
		}
		if wantCTO {
			if len(buf)-pos < 4 {
				return nil, model.NewParseError(source, "trun", b.Offset, b.PayloadOffset+int64(pos), io.ErrUnexpectedEOF)
			}
			raw := binary.BigEndian.Uint32(buf[pos : pos+4])
			if version == 1 {
				s.CompositionOffset = int64(int32(raw))
			} else {
				s.CompositionOffset = int64(raw)
			}
			s.HasCompositionOffset = true
			pos += 4
		}
		// ISO sample_is_non_sync_sample is bit 16 of the 32-bit sample flags.
		s.Sync = s.Flags&0x00010000 == 0
		out = append(out, s)
	}
	return out, nil
}
