package movie

import (
	"encoding/binary"
	"fmt"

	"mp4seek/internal/box"
)

// parseTable decodes one stbl child box into the track's sample tables.
func (t *Track) parseTable(rd *box.Reader, k box.Box) error {
	var err error
	switch k.Type {
	case "stsd":
		err = t.parseStsd(rd, k)
	case "stts":
		err = t.parseStts(rd, k)
	case "ctts":
		err = t.parseCtts(rd, k)
	case "stss":
		err = t.parseStss(rd, k)
	case "stsc":
		err = t.parseStsc(rd, k)
	case "stsz":
		err = t.parseStsz(rd, k)
	case "stco":
		err = t.parseStco(rd, k, false)
	case "co64":
		err = t.parseStco(rd, k, true)
	}
	return err
}

func (t *Track) parseStsd(rd *box.Reader, b box.Box) error {
	data, err := rd.ReadData(b)
	if err != nil {
		return err
	}
	if len(data) < 8 {
		return fmt.Errorf("stsd: truncated (%d bytes)", len(data))
	}
	t.Descs = int(binary.BigEndian.Uint32(data[4:8]))
	return nil
}

func (t *Track) parseStts(rd *box.Reader, b box.Box) error {
	data, err := rd.ReadData(b)
	if err != nil {
		return err
	}
	c := &cursor{b: data, typ: "stts"}
	if err := c.skip(4); err != nil {
		return err
	}
	n, err := c.u32()
	if err != nil {
		return err
	}
	t.Stts = make([]SttsEntry, 0, n)
	for i := uint32(0); i < n; i++ {
		cnt, err := c.u32()
		if err != nil {
			return err
		}
		d, err := c.u32()
		if err != nil {
			return err
		}
		t.Stts = append(t.Stts, SttsEntry{Count: cnt, Delta: d})
	}
	return nil
}

func (t *Track) parseCtts(rd *box.Reader, b box.Box) error {
	data, err := rd.ReadData(b)
	if err != nil {
		return err
	}
	c := &cursor{b: data, typ: "ctts"}
	v, err := c.u8()
	if err != nil {
		return err
	}
	if err := c.skip(3); err != nil {
		return err
	}
	n, err := c.u32()
	if err != nil {
		return err
	}
	t.Ctts = make([]CttsEntry, 0, n)
	for i := uint32(0); i < n; i++ {
		cnt, err := c.u32()
		if err != nil {
			return err
		}
		off, err := c.u32()
		if err != nil {
			return err
		}
		o := int64(off)
		if v == 1 {
			o = int64(int32(off)) // signed composition offset
		}
		t.Ctts = append(t.Ctts, CttsEntry{Count: cnt, Offset: o})
	}
	return nil
}

func (t *Track) parseStss(rd *box.Reader, b box.Box) error {
	data, err := rd.ReadData(b)
	if err != nil {
		return err
	}
	c := &cursor{b: data, typ: "stss"}
	if err := c.skip(4); err != nil {
		return err
	}
	n, err := c.u32()
	if err != nil {
		return err
	}
	t.HasStss = true
	t.Stss = make([]uint32, 0, n)
	for i := uint32(0); i < n; i++ {
		s, err := c.u32()
		if err != nil {
			return err
		}
		t.Stss = append(t.Stss, s)
	}
	return nil
}

func (t *Track) parseStsc(rd *box.Reader, b box.Box) error {
	data, err := rd.ReadData(b)
	if err != nil {
		return err
	}
	c := &cursor{b: data, typ: "stsc"}
	if err := c.skip(4); err != nil {
		return err
	}
	n, err := c.u32()
	if err != nil {
		return err
	}
	t.Stsc = make([]StscEntry, 0, n)
	for i := uint32(0); i < n; i++ {
		fc, err := c.u32()
		if err != nil {
			return err
		}
		spc, err := c.u32()
		if err != nil {
			return err
		}
		di, err := c.u32()
		if err != nil {
			return err
		}
		t.Stsc = append(t.Stsc, StscEntry{FirstChunk: fc, SamplesPerChunk: spc, DescIndex: di})
	}
	return nil
}

func (t *Track) parseStsz(rd *box.Reader, b box.Box) error {
	data, err := rd.ReadData(b)
	if err != nil {
		return err
	}
	c := &cursor{b: data, typ: "stsz"}
	if err := c.skip(4); err != nil {
		return err
	}
	uniform, err := c.u32()
	if err != nil {
		return err
	}
	n, err := c.u32()
	if err != nil {
		return err
	}
	if uint64(n)*4 > uint64(len(data)) {
		return fmt.Errorf("stsz: sample count %d exceeds payload", n)
	}
	t.Sizes = make([]int64, n)
	if uniform != 0 {
		for i := range t.Sizes {
			t.Sizes[i] = int64(uniform)
		}
		return nil
	}
	for i := range t.Sizes {
		s, err := c.u32()
		if err != nil {
			return err
		}
		t.Sizes[i] = int64(s)
	}
	return nil
}

func (t *Track) parseStco(rd *box.Reader, b box.Box, wide bool) error {
	data, err := rd.ReadData(b)
	if err != nil {
		return err
	}
	c := &cursor{b: data, typ: b.Type}
	if err := c.skip(4); err != nil {
		return err
	}
	n, err := c.u32()
	if err != nil {
		return err
	}
	t.Chunks = make([]int64, 0, n)
	for i := uint32(0); i < n; i++ {
		var off uint64
		if wide {
			off, err = c.u64()
		} else {
			var o32 uint32
			o32, err = c.u32()
			off = uint64(o32)
		}
		if err != nil {
			return err
		}
		t.Chunks = append(t.Chunks, int64(off))
	}
	return nil
}
