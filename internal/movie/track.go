package movie

import (
	"fmt"

	"mp4seek/internal/box"
	"mp4seek/internal/timeline"
)

func parseTrak(rd *box.Reader, trak box.Box) (*Track, error) {
	t := &Track{}
	kids, err := rd.Children(trak)
	if err != nil {
		return nil, err
	}
	for _, k := range kids {
		switch k.Type {
		case "tkhd":
			if err := t.parseTkhd(rd, k); err != nil {
				return nil, err
			}
		case "edts":
			if err := t.parseEdts(rd, k); err != nil {
				return nil, err
			}
		case "mdia":
			if err := t.parseMdia(rd, k); err != nil {
				return nil, err
			}
		}
	}
	if t.Timescale == 0 {
		return nil, fmt.Errorf("trak %d: missing mdhd timescale", t.ID)
	}
	return t, nil
}

func (t *Track) parseTkhd(rd *box.Reader, b box.Box) error {
	data, err := rd.ReadData(b)
	if err != nil {
		return err
	}
	c := &cursor{b: data, typ: "tkhd"}
	v, err := c.u8()
	if err != nil {
		return err
	}
	if err := c.skip(3); err != nil {
		return err
	}
	var id uint32
	if v == 1 {
		if err := c.skip(16); err != nil {
			return err
		}
		id, err = c.u32()
	} else {
		if err := c.skip(8); err != nil {
			return err
		}
		id, err = c.u32()
	}
	if err != nil {
		return err
	}
	t.ID = int(id)
	return nil
}

func (t *Track) parseEdts(rd *box.Reader, edts box.Box) error {
	elst, err := find(rd, edts, "elst")
	if err != nil || elst.Type == "" {
		return err
	}
	data, err := rd.ReadData(elst)
	if err != nil {
		return err
	}
	edits, err := timeline.ParseElst(data)
	if err != nil {
		return fmt.Errorf("trak %d: %w", t.ID, err)
	}
	t.Edits = edits
	return nil
}

func (t *Track) parseMdia(rd *box.Reader, mdia box.Box) error {
	kids, err := rd.Children(mdia)
	if err != nil {
		return err
	}
	for _, k := range kids {
		switch k.Type {
		case "mdhd":
			if err := t.parseMdhd(rd, k); err != nil {
				return err
			}
		case "hdlr":
			if err := t.parseHdlr(rd, k); err != nil {
				return err
			}
		case "minf":
			if err := t.parseMinf(rd, k); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *Track) parseMdhd(rd *box.Reader, b box.Box) error {
	data, err := rd.ReadData(b)
	if err != nil {
		return err
	}
	c := &cursor{b: data, typ: "mdhd"}
	v, err := c.u8()
	if err != nil {
		return err
	}
	if err := c.skip(3); err != nil {
		return err
	}
	if v == 1 {
		if err := c.skip(16); err != nil {
			return err
		}
		ts, err := c.u32()
		if err != nil {
			return err
		}
		dur, err := c.u64()
		if err != nil {
			return err
		}
		t.Timescale, t.Duration = int64(ts), int64(dur)
	} else {
		if err := c.skip(8); err != nil {
			return err
		}
		ts, err := c.u32()
		if err != nil {
			return err
		}
		dur, err := c.u32()
		if err != nil {
			return err
		}
		t.Timescale, t.Duration = int64(ts), int64(dur)
	}
	return nil
}

func (t *Track) parseHdlr(rd *box.Reader, b box.Box) error {
	data, err := rd.ReadData(b)
	if err != nil {
		return err
	}
	if len(data) < 12 {
		return fmt.Errorf("hdlr: truncated (%d bytes)", len(data))
	}
	t.Handler = string(data[8:12])
	return nil
}

// parseMinf walks minf/stbl; sample table payloads are decoded by the
// table parsers in tables.go.
func (t *Track) parseMinf(rd *box.Reader, minf box.Box) error {
	stbl, err := find(rd, minf, "stbl")
	if err != nil || stbl.Type == "" {
		return err
	}
	kids, err := rd.Children(stbl)
	if err != nil {
		return err
	}
	for _, k := range kids {
		if err := t.parseTable(rd, k); err != nil {
			return fmt.Errorf("trak %d: %w", t.ID, err)
		}
	}
	return nil
}

// parseTable decodes one stbl child; table parsers land in the next
// batch, until then unknown tables are skipped.
func (t *Track) parseTable(rd *box.Reader, b box.Box) error {
	return nil
}
