package cmaf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"example.com/archiveverify/internal/model"
)

func readPayload(r io.ReaderAt, b BoxHeader, max int) ([]byte, error) {
	if b.PayloadSize < 0 || b.PayloadSize > int64(max) {
		return nil, fmt.Errorf("payload %d exceeds read cap %d", b.PayloadSize, max)
	}
	buf := make([]byte, b.PayloadSize)
	if _, err := r.ReadAt(buf, b.PayloadOffset); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	} else if int64(len(buf)) != b.PayloadSize {
		return nil, io.ErrUnexpectedEOF
	}
	return buf, nil
}

func parseMFHD(r io.ReaderAt, source string, b BoxHeader) (uint32, error) {
	section := io.NewSectionReader(r, b.PayloadOffset, b.PayloadSize)
	mfhd := &mp4.Mfhd{}
	if _, err := mp4.Unmarshal(section, uint64(b.PayloadSize), mfhd, mp4.Context{}); err != nil {
		return 0, model.NewParseError(source, "mfhd", b.Offset, b.PayloadOffset, err)
	}
	return mfhd.SequenceNumber, nil
}

func parseTRAF(r io.ReaderAt, source string, traf BoxHeader, init *Init, maxBox int64) (*TrackFragment, error) {
	kids, err := ScanRange(r, source, traf.PayloadOffset, traf.End, maxBox)
	if err != nil {
		return nil, err
	}
	tf := &TrackFragment{}
	var trunBoxes []BoxHeader
	for _, b := range kids {
		switch b.Type {
		case "tfhd":
			if err := parseTFHD(r, source, b, tf); err != nil {
				return nil, err
			}
		case "tfdt":
			if err := parseTFDT(r, source, b, tf); err != nil {
				return nil, err
			}
		case "trun":
			trunBoxes = append(trunBoxes, b)
		}
	}
	if tf.TrackID == 0 {
		return nil, model.NewParseError(source, "tfhd", traf.Offset, traf.Offset,
			errors.New("tfhd missing track ID"))
	}
	if init != nil {
		if t := init.Track(tf.TrackID); t != nil {
			tf.Timescale = t.TimeScale
			tf.TREXDefaults = Defaults{Duration: t.DefaultDuration, Size: t.DefaultSize, Flags: t.DefaultFlags}
			if tf.DefaultDuration == 0 {
				tf.DefaultDuration = t.DefaultDuration
			}
			if tf.DefaultSize == 0 {
				tf.DefaultSize = t.DefaultSize
			}
			if tf.DefaultFlags == 0 {
				tf.DefaultFlags = t.DefaultFlags
			}
		}
	}
	for _, b := range trunBoxes {
		ss, err := parseTRUN(r, source, b, tf)
		if err != nil {
			return nil, err
		}
		tf.Samples = append(tf.Samples, ss...)
	}
	if len(tf.Samples) == 0 {
		return nil, model.NewParseError(source, "trun", traf.Offset, traf.End,
			errors.New("track fragment has no samples"))
	}
	return tf, nil
}

func parseTFHD(r io.ReaderAt, source string, b BoxHeader, tf *TrackFragment) error {
	buf, err := readPayload(r, b, 64)
	if err != nil || len(buf) < 8 {
		return model.NewParseError(source, "tfhd", b.Offset, b.PayloadOffset, io.ErrUnexpectedEOF)
	}
	flags := uint32(buf[1])<<16 | uint32(buf[2])<<8 | uint32(buf[3])
	tf.TrackID = binary.BigEndian.Uint32(buf[4:8])
	pos := 8
	need := func(n int) bool { return len(buf)-pos >= n }
	if flags&0x000001 != 0 { // base-data-offset
		if !need(8) {
			return io.ErrUnexpectedEOF
		}
		pos += 8
	}
	if flags&0x000002 != 0 { // sample-description-index
		if !need(4) {
			return io.ErrUnexpectedEOF
		}
		pos += 4
	}
	if flags&0x000008 != 0 {
		if !need(4) {
			return io.ErrUnexpectedEOF
		}
		tf.DefaultDuration = binary.BigEndian.Uint32(buf[pos : pos+4])
		pos += 4
	}
	if flags&0x000010 != 0 {
		if !need(4) {
			return io.ErrUnexpectedEOF
		}
		tf.DefaultSize = binary.BigEndian.Uint32(buf[pos : pos+4])
		pos += 4
	}
	if flags&0x000020 != 0 {
		if !need(4) {
			return io.ErrUnexpectedEOF
		}
		tf.DefaultFlags = binary.BigEndian.Uint32(buf[pos : pos+4])
	}
	return nil
}

func parseTFDT(r io.ReaderAt, source string, b BoxHeader, tf *TrackFragment) error {
	tf.TFDTOffset = b.Offset
	buf, err := readPayload(r, b, 16)
	if err != nil || len(buf) < 4 {
		return model.NewParseError(source, "tfdt", b.Offset, b.PayloadOffset, io.ErrUnexpectedEOF)
	}
	version := buf[0]
	tf.TFDTVersion = version
	if version == 1 {
		if len(buf) < 12 {
			return model.NewParseError(source, "tfdt", b.Offset, b.PayloadOffset+int64(len(buf)),
				io.ErrUnexpectedEOF)
		}
		tf.BaseDecodeTime = binary.BigEndian.Uint64(buf[4:12])
		return nil
	}
	if len(buf) < 8 {
		return model.NewParseError(source, "tfdt", b.Offset, b.PayloadOffset+int64(len(buf)),
			io.ErrUnexpectedEOF)
	}
	tf.BaseDecodeTime = uint64(binary.BigEndian.Uint32(buf[4:8]))
	return nil
}
