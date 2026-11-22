package container

import "archivist/internal/model"

func (g *generator) writeTrak(w *Writer, ts TrackSpec) {
	w.WriteContainer(BoxTrak, func(c *Writer) {
		g.writeTkhd(c, ts)
		if ts.StartDelay > 0 {
			g.writeEdts(c, ts)
		}
		c.WriteContainer(BoxMdia, func(m *Writer) {
			g.writeMdhd(m, ts)
			g.writeHdlr(m, ts)
			m.WriteContainer(BoxMinf, func(mf *Writer) {
				mf.WriteContainer(BoxStbl, func(s *Writer) {
					g.writeStsd(s, ts)
					g.writeStts(s, ts)
					if ts.Kind == model.KindVideo && g.spec.Corruption != CorruptMissingKeyframe {
						g.writeStss(s, ts)
					}
					g.writeStsc(s, ts)
					g.writeStsz(s, ts)
					g.writeStco(s, ts)
				})
			})
		})
	})
}

func (g *generator) writeTkhd(w *Writer, ts TrackSpec) {
	body := make([]byte, 80) // tkhd v0 fixed body
	put := func(off int, v uint32) {
		body[off] = byte(v >> 24)
		body[off+1] = byte(v >> 16)
		body[off+2] = byte(v >> 8)
		body[off+3] = byte(v)
	}
	// tkhd v0 body: creation(4) modification(4) track_ID(4) reserved(4)
	// duration(4) ... volume at 24, matrix at 36, width/height at 72/76.
	put(8, ts.ID)                       // track_ID
	put(20, uint32(trackDuration(ts)))  // duration in movie timescale
	if ts.Kind == model.KindAudio {
		body[24] = 0x01 // volume
	}
	matrix := []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	for i, mv := range matrix {
		put(36+i*4, mv)
	}
	if ts.Kind == model.KindVideo {
		put(72, 320<<16) // width 16.16
		put(76, 240<<16) // height 16.16
	}
	w.WriteBox(BoxTkhd, FullBox(0, 0x000007, body)) // enabled+in movie+in preview
}

func (g *generator) writeEdts(w *Writer, ts TrackSpec) {
	w.WriteContainer(BoxEdts, func(e *Writer) {
		var body []byte
		body = PutU32(body, 1) // entry count
		body = PutU32(body, uint32(trackDuration(ts)))
		body = PutU32(body, uint32(ts.StartDelay)) // media_time (positive delay)
		body = PutU16(body, 1)                     // media_rate_integer
		body = PutU16(body, 0)                     // media_rate_fraction
		e.WriteBox(BoxElst, FullBox(0, 0, body))
	})
}

func (g *generator) writeMdhd(w *Writer, ts TrackSpec) {
	body := make([]byte, 20) // mdhd v0 fixed body (ISO 14496)
	put := func(off int, v uint32) {
		body[off] = byte(v >> 24)
		body[off+1] = byte(v >> 16)
		body[off+2] = byte(v >> 8)
		body[off+3] = byte(v)
	}
	put(8, ts.Timescale)               // timescale
	put(12, uint32(trackDuration(ts))) // duration
	body[16] = 0x55
	body[17] = 0xC4 // packed ISO-639 language code (und=0x55C4)
	// body[18:20] pre_defined stays zero
	w.WriteBox(BoxMdhd, FullBox(0, 0, body))
}

func (g *generator) writeHdlr(w *Writer, ts TrackSpec) {
	// body layout: pre_defined(4) handler_type(4) reserved(12) name(NUL-term)
	var body []byte
	body = PutU32(body, 0) // pre_defined
	if ts.Kind == model.KindVideo {
		body = append(body, 'v', 'i', 'd', 'e')
	} else {
		body = append(body, 's', 'o', 'u', 'n')
	}
	body = append(body, make([]byte, 12)...) // three reserved uint32
	if ts.Kind == model.KindVideo {
		body = append(body, []byte("video-handler\x00")...)
	} else {
		body = append(body, []byte("audio-handler\x00")...)
	}
	w.WriteBox(BoxHdlr, FullBox(0, 0, body))
}

func (g *generator) writeStsd(w *Writer, ts TrackSpec) {
	var entryBox []byte
	if ts.Kind == model.KindVideo {
		entry := make([]byte, 78) // VisualSampleEntry fixed fields
		entry[9] = 1             // data_reference_index
		entry[24] = byte(320 >> 8)
		entry[25] = byte(320 & 0xFF)
		entry[26] = byte(240 >> 8)
		entry[27] = byte(240 & 0xFF)
		entry[28] = 0x00
		entry[29] = 0x48
		entry[30] = 0x00
		entry[31] = 0x00 // horizresolution 72dpi
		entry[32] = 0x00
		entry[33] = 0x48
		entry[34] = 0x00
		entry[35] = 0x00 // vertresolution
		entry[40] = 1 // frame_count
		entry[74] = 0x18
		entry[76] = 0xFF
		entry[77] = 0xFF
		var colr []byte
		colr = append(colr, []byte(ts.Color.ColorType)...)
		colr = PutU16(colr, ts.Color.Primaries)
		colr = PutU16(colr, ts.Color.Transfer)
		colr = PutU16(colr, ts.Color.Matrix)
		entry = append(entry, boxBytes(BoxColr, colr)...)
		entryBox = boxBytes(FourCC{'a', 'v', 'c', '1'}, entry)
	} else {
		entry := make([]byte, 28) // AudioSampleEntry fixed fields
		entry[9] = 1              // data_reference_index
		entry[16] = byte(ts.Channels >> 8)
		entry[17] = byte(ts.Channels) // channelcount at body[16:18]
		entry[19] = 0x10              // samplesize 16
		// samplerate is 16.16 fixed point at body[20:24]; integer Hz sits high.
		entry[20] = byte(ts.SampleRate >> 8)
		entry[21] = byte(ts.SampleRate & 0xFF)
		entryBox = boxBytes(FourCC{'m', 'p', '4', 'a'}, entry)
	}
	var body []byte
	body = PutU32(body, 1) // entry_count
	body = append(body, entryBox...)
	w.WriteBox(BoxStsd, FullBox(0, 0, body))
}

func (g *generator) writeStts(w *Writer, ts TrackSpec) {
	var body []byte
	body = PutU32(body, 1) // one run
	body = PutU32(body, ts.SampleCount)
	body = PutU32(body, ts.SampleDur)
	w.WriteBox(BoxStts, FullBox(0, 0, body))
}

func (g *generator) writeStss(w *Writer, ts TrackSpec) {
	keys := g.keyframeSamples(ts)
	var body []byte
	body = PutU32(body, uint32(len(keys)))
	for _, k := range keys {
		body = PutU32(body, k+1) // stss is 1-based
	}
	w.WriteBox(BoxStss, FullBox(0, 0, body))
}

func (g *generator) writeStsc(w *Writer, ts TrackSpec) {
	var body []byte
	body = PutU32(body, 1)
	body = PutU32(body, 1)                 // first_chunk
	body = PutU32(body, 1)                 // one sample per chunk (one chunk per sample)
	body = PutU32(body, 1)                 // sample_description_index
	w.WriteBox(BoxStsc, FullBox(0, 0, body))
}

func (g *generator) writeStsz(w *Writer, ts TrackSpec) {
	var body []byte
	body = PutU32(body, 0) // uniform size = 0 -> table follows
	body = PutU32(body, ts.SampleCount)
	for _, sz := range g.sizes[ts.ID] {
		body = PutU32(body, sz)
	}
	w.WriteBox(BoxStsz, FullBox(0, 0, body))
}

// mdatBase computes the absolute file offset where mdat payload begins.
func (g *generator) mdatBase() uint64 {
	return g.mdatOffset + 8
}

func (g *generator) writeStco(w *Writer, ts TrackSpec) {
	base := g.mdatBase()
	// payload offset of this track inside mdat = sum of earlier track payloads.
	var trackPayloadOff uint64
	for _, earlier := range g.spec.Tracks {
		if earlier.ID == ts.ID {
			break
		}
		trackPayloadOff += uint64(len(g.payloads[earlier.ID]))
	}
	var body []byte
	body = PutU32(body, ts.SampleCount) // one chunk per sample
	off := base + trackPayloadOff
	for i := uint32(0); i < ts.SampleCount; i++ {
		body = PutU32(body, uint32(off))
		off += uint64(g.sizes[ts.ID][i])
	}
	w.WriteBox(BoxStco, FullBox(0, 0, body))
}
