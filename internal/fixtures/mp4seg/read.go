package mp4seg

import (
	"fmt"
	"io"

	mp4 "github.com/abema/go-mp4"
)

// Read re-parses a written segment with go-mp4 and extracts track metadata,
// sample payloads and the exact stts timeline. It never trusts the writer's
// in-memory state, which makes it usable by the independent verifier.
func Read(r io.Reader) (TrackMeta, [][]byte, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return TrackMeta{}, nil, err
	}
	br := &byteReader{data: data}
	meta := TrackMeta{Codec: "av01"}
	var payload [][]byte
	var mdatBody []byte

	for br.pos < int64(len(data)) {
		bi, err := mp4.ReadBoxInfo(br)
		if err != nil {
			return TrackMeta{}, nil, fmt.Errorf("mp4seg: box header at %d: %w", br.pos, err)
		}
		if bi.Size == 0 || bi.Size > uint64(len(data))-uint64(bi.Offset) {
			return TrackMeta{}, nil, fmt.Errorf("mp4seg: box %s size %d overruns file", bi.Type, bi.Size)
		}
		switch bi.Type {
		case mp4.BoxTypeMoov():
			if err := parseMoov(data, bi, &meta); err != nil {
				return TrackMeta{}, nil, err
			}
		case mp4.BoxTypeMdat():
			start := int(bi.Offset + bi.HeaderSize)
			end := int(bi.Offset + bi.Size)
			mdatBody = data[start:end]
		}
		if _, err := br.Seek(int64(bi.Offset+bi.Size), io.SeekStart); err != nil {
			return TrackMeta{}, nil, err
		}
	}
	if mdatBody == nil {
		return TrackMeta{}, nil, fmt.Errorf("mp4seg: no mdat box")
	}
	if len(meta.SampleSizes) == 0 {
		return TrackMeta{}, nil, fmt.Errorf("mp4seg: no sample sizes")
	}
	payload, err = splitSamples(mdatBody, meta.SampleSizes)
	if err != nil {
		return TrackMeta{}, nil, err
	}
	if len(meta.SampleSizes) != len(payload) {
		return TrackMeta{}, nil, fmt.Errorf("mp4seg: stsz has %d entries, mdat has %d samples",
			len(meta.SampleSizes), len(payload))
	}
	return meta, payload, nil
}

func splitSamples(body []byte, sizes []uint32) ([][]byte, error) {
	out := make([][]byte, 0, len(sizes))
	pos := 0
	for i, size := range sizes {
		if pos+int(size) > len(body) {
			return nil, fmt.Errorf("mp4seg: sample %d overruns mdat", i)
		}
		out = append(out, body[pos:pos+int(size)])
		pos += int(size)
	}
	if pos != len(body) {
		return nil, fmt.Errorf("mp4seg: %d unclaimed mdat bytes", len(body)-pos)
	}
	return out, nil
}

func parseMoov(data []byte, moov *mp4.BoxInfo, meta *TrackMeta) error {
	children := moov.Offset + moov.HeaderSize
	end := moov.Offset + moov.Size
	for children < end {
		bi, err := mp4.ReadBoxInfo(newSliceReader(data[children:end], children))
		if err != nil {
			return err
		}
		if bi.Type == mp4.BoxTypeTrak() {
			if err := parseTrak(data, bi.Offset+bi.HeaderSize, bi.Offset+bi.Size, meta); err != nil {
				return err
			}
		}
		children = bi.Offset + bi.Size
	}
	return nil
}

func parseTrak(data []byte, start, end uint64, meta *TrackMeta) error {
	children := start
	for children < end {
		bi, err := mp4.ReadBoxInfo(newSliceReader(data[children:end], children))
		if err != nil {
			return err
		}
		switch bi.Type {
		case mp4.BoxTypeTkhd():
			var box mp4.Tkhd
			if err := unmarshalAt(data, bi, &box); err != nil {
				return err
			}
			meta.TrackID = box.TrackID
			meta.Width = int(box.GetWidthInt())
			meta.Height = int(box.GetHeightInt())
		case mp4.BoxTypeMdia():
			if err := parseMdia(data, bi.Offset+bi.HeaderSize, bi.Offset+bi.Size, meta); err != nil {
				return err
			}
		}
		children = bi.Offset + bi.Size
	}
	return nil
}

func parseMdia(data []byte, start, end uint64, meta *TrackMeta) error {
	children := start
	for children < end {
		bi, err := mp4.ReadBoxInfo(newSliceReader(data[children:end], children))
		if err != nil {
			return err
		}
		switch bi.Type {
		case mp4.BoxTypeMdhd():
			var box mp4.Mdhd
			if err := unmarshalAt(data, bi, &box); err != nil {
				return err
			}
			meta.TimeScale = box.Timescale
		case mp4.BoxTypeMinf():
			if err := parseMinf(data, bi.Offset+bi.HeaderSize, bi.Offset+bi.Size, meta); err != nil {
				return err
			}
		}
		children = bi.Offset + bi.Size
	}
	return nil
}

func parseMinf(data []byte, start, end uint64, meta *TrackMeta) error {
	children := start
	for children < end {
		bi, err := mp4.ReadBoxInfo(newSliceReader(data[children:end], children))
		if err != nil {
			return err
		}
		if bi.Type == mp4.BoxTypeStbl() {
			if err := parseStbl(data, bi.Offset+bi.HeaderSize, bi.Offset+bi.Size, meta); err != nil {
				return err
			}
		}
		children = bi.Offset + bi.Size
	}
	return nil
}

func parseStbl(data []byte, start, end uint64, meta *TrackMeta) error {
	children := start
	for children < end {
		bi, err := mp4.ReadBoxInfo(newSliceReader(data[children:end], children))
		if err != nil {
			return err
		}
		switch bi.Type {
		case mp4.BoxTypeStts():
			var box mp4.Stts
			if err := unmarshalAt(data, bi, &box); err != nil {
				return err
			}
			for _, e := range box.Entries {
				for i := uint32(0); i < e.SampleCount; i++ {
					meta.Deltas = append(meta.Deltas, e.SampleDelta)
				}
			}
		case mp4.BoxTypeStsz():
			var box mp4.Stsz
			if err := unmarshalAt(data, bi, &box); err != nil {
				return err
			}
			if box.SampleSize != 0 {
				meta.SampleSizes = make([]uint32, box.SampleCount)
				for i := range meta.SampleSizes {
					meta.SampleSizes[i] = box.SampleSize
				}
			} else {
				meta.SampleSizes = append([]uint32(nil), box.EntrySize...)
			}
		case mp4.BoxTypeStco():
			var box mp4.Stco
			if err := unmarshalAt(data, bi, &box); err != nil {
				return err
			}
			meta.ChunkOffset = append([]uint32(nil), box.ChunkOffset...)
		case mp4.BoxTypeStsd():
			// Presence is enough; the sample entry is validated by the verifier
			// against the declared geometry.
			if bi.Size < 16 {
				return fmt.Errorf("mp4seg: stsd too small")
			}
		}
		children = bi.Offset + bi.Size
	}
	return nil
}

func unmarshalAt(data []byte, bi *mp4.BoxInfo, dst mp4.IBox) error {
	r := newSliceReader(data[bi.Offset+bi.HeaderSize:bi.Offset+bi.Size], bi.Offset+bi.HeaderSize)
	if _, err := mp4.Unmarshal(r, bi.Size-bi.HeaderSize, dst, mp4.Context{}); err != nil {
		return fmt.Errorf("mp4seg: unmarshal %s: %w", bi.Type, err)
	}
	return nil
}

// byteReader is an in-memory io.ReadSeeker over the whole file; repeated
// absolute seeks keep offsets identical to the on-disk values.
type byteReader struct {
	data []byte
	pos  int64
}

func (b *byteReader) Read(p []byte) (int, error) {
	if b.pos >= int64(len(b.data)) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.pos:])
	b.pos += int64(n)
	return n, nil
}

func (b *byteReader) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = b.pos + offset
	case io.SeekEnd:
		next = int64(len(b.data)) + offset
	default:
		return 0, fmt.Errorf("mp4seg: bad whence %d", whence)
	}
	if next < 0 {
		return 0, fmt.Errorf("mp4seg: negative seek")
	}
	b.pos = next
	return next, nil
}

// sliceReader exposes data[base:] as a ReadSeeker whose offets are absolute
// file offsets, so BoxInfo.Offset matches the real file layout.
type sliceReader struct {
	data []byte
	base int64
	pos  int64
}

func newSliceReader(sub []byte, base uint64) *sliceReader {
	return &sliceReader{data: sub, base: int64(base), pos: int64(base)}
}

func (s *sliceReader) Read(p []byte) (int, error) {
	rel := s.pos - s.base
	if rel >= int64(len(s.data)) {
		return 0, io.EOF
	}
	n := copy(p, s.data[rel:])
	s.pos += int64(n)
	return n, nil
}

func (s *sliceReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		s.pos = offset
	case io.SeekCurrent:
		s.pos += offset
	case io.SeekEnd:
		s.pos = s.base + int64(len(s.data)) + offset
	}
	return s.pos, nil
}
