package index

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/abema/go-mp4"

	"replaypipe/internal/timebase"
)

// moofInfo records a movie fragment for later offset resolution.
type moofInfo struct {
	offset int64
	size   int64
}

type trexDefaults struct {
	sampleDescIndex uint32
	duration        uint32
	size            uint32
	flags           uint32
}

var syncFlagMask uint32 = 0x00010000 // sample_is_non_sync_sample

// parseMoov reads the movie box and every trak's sample tables.
func (p *parser) parseMoov(off, size int64) *timebase.Error {
	payload, e := p.readPayload(off, size, 8)
	if e != nil {
		return e
	}
	moov, e := childBoxes(payload, off+8)
	if e != nil {
		return e
	}
	p.tracks = map[uint32]*Track{}
	p.trex = map[uint32]trexDefaults{}
	for _, box := range moov {
		p.record(1, box.typ, box.off, uint64(box.size), uint64(box.hdr), "")
		switch box.typ {
		case "trak":
			if e := p.parseTrak(box); e != nil {
				return e
			}
		case "mvex":
			if e := p.parseMvex(box); e != nil {
				return e
			}
		}
	}
	return nil
}

func (p *parser) parseMvex(mvex boxView) *timebase.Error {
	kids, e := childBoxes(mvex.payload, mvex.off+mvex.hdr)
	if e != nil {
		return e
	}
	for _, box := range kids {
		if box.typ != "trex" {
			continue
		}
		var trex mp4.Trex
		if err := decode(box, &trex); err != nil {
			return timebase.Fail(timebase.CodeCorruptField, trex.TrackID, box.off).Wrap(err, "decode trex")
		}
		p.trex[trex.TrackID] = trexDefaults{
			sampleDescIndex: trex.DefaultSampleDescriptionIndex,
			duration:        trex.DefaultSampleDuration,
			size:            trex.DefaultSampleSize,
			flags:           trex.DefaultSampleFlags,
		}
	}
	return nil
}

func (p *parser) parseTrak(trak boxView) *timebase.Error {
	kids, e := childBoxes(trak.payload, trak.off+trak.hdr)
	if e != nil {
		return e
	}
	tr := &Track{Kind: TrackOther}
	var stbl boxView
	var haveStbl bool
	for _, box := range kids {
		p.record(2, box.typ, box.off, uint64(box.size), uint64(box.hdr), "")
		switch box.typ {
		case "tkhd":
			var tkhd mp4.Tkhd
			if err := decode(box, &tkhd); err != nil {
				return timebase.Fail(timebase.CodeCorruptField, tr.ID, box.off).Wrap(err, "decode tkhd")
			}
			tr.ID = tkhd.TrackID
			tr.Width = uint16(tkhd.Width >> 16)
			tr.Height = uint16(tkhd.Height >> 16)
		case "mdia":
			if e := p.parseMdia(box, tr, &stbl, &haveStbl); e != nil {
				return e
			}
		}
	}
	if tr.ID == 0 {
		return timebase.Newf(timebase.CodeCorruptField, 0, trak.off, "trak has no valid tkhd track id")
	}
	if tr.Timescale == 0 {
		return timebase.Newf(timebase.CodeCorruptField, tr.ID, trak.off, "track has no mdhd timescale")
	}
	b, err := timebase.FromTimescale(tr.Timescale)
	if err != nil {
		return timebase.Fail(timebase.CodeTimeOverflow, tr.ID, trak.off).Wrap(err, "track timescale")
	}
	tr.Base = b
	if haveStbl {
		if e := p.parseStbl(stbl, tr); e != nil {
			return e
		}
	}
	p.media.Tracks = append(p.media.Tracks, tr)
	p.tracks[tr.ID] = tr
	return nil
}

func (p *parser) parseMdia(mdia boxView, tr *Track, stbl *boxView, have *bool) *timebase.Error {
	kids, e := childBoxes(mdia.payload, mdia.off+mdia.hdr)
	if e != nil {
		return e
	}
	for _, box := range kids {
		p.record(3, box.typ, box.off, uint64(box.size), uint64(box.hdr), "")
		switch box.typ {
		case "mdhd":
			var mdhd mp4.Mdhd
			if err := decode(box, &mdhd); err != nil {
				return timebase.Fail(timebase.CodeCorruptField, tr.ID, box.off).Wrap(err, "decode mdhd")
			}
			tr.Timescale = mdhd.Timescale
			tr.Duration = int64(mdhd.GetDuration())
		case "hdlr":
			var hdlr mp4.Hdlr
			if err := decode(box, &hdlr); err != nil {
				return timebase.Fail(timebase.CodeCorruptField, tr.ID, box.off).Wrap(err, "decode hdlr")
			}
			tr.Handler = hdlr.HandlerType
			switch string(hdlr.HandlerType[:]) {
			case "vide":
				tr.Kind = TrackVideo
			case "soun":
				tr.Kind = TrackAudio
			case "text", "sbtl", "subt", "clcp":
				tr.Kind = TrackSubtitle
			default:
				tr.Kind = TrackOther
			}
		case "minf":
			minfKids, e := childBoxes(box.payload, box.off+box.hdr)
			if e != nil {
				return e
			}
			for _, mb := range minfKids {
				p.record(4, mb.typ, mb.off, uint64(mb.size), uint64(mb.hdr), "")
				if mb.typ == "stbl" {
					*stbl = mb
					*have = true
				}
			}
		}
	}
	return nil
}

func (p *parser) parseStbl(stbl boxView, tr *Track) *timebase.Error {
	kids, e := childBoxes(stbl.payload, stbl.off+stbl.hdr)
	if e != nil {
		return e
	}
	var stts mp4.Stts
	var ctts mp4.Ctts
	var stsz mp4.Stsz
	var stsc mp4.Stsc
	var stco mp4.Stco
	var co64 mp4.Co64
	var stss mp4.Stss
	var haveStco, haveCo64, haveStss bool
	for _, box := range kids {
		p.record(5, box.typ, box.off, uint64(box.size), uint64(box.hdr), "")
		switch box.typ {
		case "stsd":
			if e := p.parseStsd(box, tr); e != nil {
				return e
			}
		case "stts":
			if err := decode(box, &stts); err != nil {
				return timebase.Fail(timebase.CodeCorruptField, tr.ID, box.off).Wrap(err, "decode stts")
			}
		case "ctts":
			if err := decode(box, &ctts); err != nil {
				return timebase.Fail(timebase.CodeCorruptField, tr.ID, box.off).Wrap(err, "decode ctts")
			}
		case "stsz":
			if err := decode(box, &stsz); err != nil {
				return timebase.Fail(timebase.CodeCorruptField, tr.ID, box.off).Wrap(err, "decode stsz")
			}
		case "stsc":
			if err := decode(box, &stsc); err != nil {
				return timebase.Fail(timebase.CodeCorruptField, tr.ID, box.off).Wrap(err, "decode stsc")
			}
		case "stco":
			if err := decode(box, &stco); err != nil {
				return timebase.Fail(timebase.CodeCorruptField, tr.ID, box.off).Wrap(err, "decode stco")
			}
			haveStco = true
		case "co64":
			if err := decode(box, &co64); err != nil {
				return timebase.Fail(timebase.CodeCorruptField, tr.ID, box.off).Wrap(err, "decode co64")
			}
			haveCo64 = true
		case "stss":
			if err := decode(box, &stss); err != nil {
				return timebase.Fail(timebase.CodeCorruptField, tr.ID, box.off).Wrap(err, "decode stss")
			}
			haveStss = true
		}
	}
	if tr.Kind != TrackVideo && len(tr.Samples) > 0 {
		// Fragment-only audio/subtitle tracks already carry samples.
		return nil
	}
	if stsz.SampleCount == 0 {
		return nil
	}
	if !haveStco && !haveCo64 {
		return timebase.Newf(timebase.CodeCorruptField, tr.ID, stbl.off,
			"stbl has %d samples but no chunk offsets", stsz.SampleCount)
	}
	chunkOffsets := make([]uint64, 0, len(stco.ChunkOffset)+len(co64.ChunkOffset))
	for _, o := range stco.ChunkOffset {
		chunkOffsets = append(chunkOffsets, uint64(o))
	}
	chunkOffsets = append(chunkOffsets, co64.ChunkOffset...)
	if e := p.expandSamples(tr, stbl.off, &stts, &ctts, &stsz, &stsc, chunkOffsets, &stss, haveStss); e != nil {
		return e
	}
	_ = haveStco
	return nil
}

func (p *parser) parseStsd(stsd boxView, tr *Track) *timebase.Error {
	if len(stsd.payload) < 8 {
		return timebase.Newf(timebase.CodeBoxTruncated, tr.ID, stsd.off, "stsd payload truncated")
	}
	entries, e := childBoxes(stsd.payload[8:], stsd.off+stsd.hdr+8)
	if e != nil {
		return e
	}
	if len(entries) == 0 {
		return timebase.Newf(timebase.CodeCorruptField, tr.ID, stsd.off, "stsd has no sample entries")
	}
	entry := entries[0]
	copy(tr.Codec[:], []byte(entry.typ))
	switch entry.typ {
	case "avc1", "avc3", "mp4v", "hev1", "hvc1", "encv":
		box, _, err := mp4.UnmarshalAny(bytesReader(entry.payload), mp4.StrToBoxType(entry.typ), uint64(len(entry.payload)), mp4.Context{})
		if err != nil {
			return timebase.Fail(timebase.CodeCorruptField, tr.ID, entry.off).Wrap(err, "decode %s", entry.typ)
		}
		if vse, ok := box.(*mp4.VisualSampleEntry); ok {
			if tr.Width == 0 {
				tr.Width = vse.Width
			}
			if tr.Height == 0 {
				tr.Height = vse.Height
			}
		}
		p.setAvcC(tr, entry)
	case "mp4a", "enca", "ac-3", "ec-3", "Opus":
		box, _, err := mp4.UnmarshalAny(bytesReader(entry.payload), mp4.StrToBoxType(entry.typ), uint64(len(entry.payload)), mp4.Context{})
		if err != nil {
			return timebase.Fail(timebase.CodeCorruptField, tr.ID, entry.off).Wrap(err, "decode %s", entry.typ)
		}
		if ase, ok := box.(*mp4.AudioSampleEntry); ok {
			tr.SampleRate = uint32(ase.GetSampleRateInt())
		}
	}
	return nil
}

func (p *parser) expandSamples(tr *Track, stblOff int64, stts *mp4.Stts, ctts *mp4.Ctts,
	stsz *mp4.Stsz, stsc *mp4.Stsc, chunkOffsets []uint64, stss *mp4.Stss, haveStss bool) *timebase.Error {
	count := int(stsz.SampleCount)
	sizes := make([]uint32, count)
	for i := 0; i < count; i++ {
		if stsz.SampleSize != 0 {
			sizes[i] = stsz.SampleSize
		} else if i < len(stsz.EntrySize) {
			sizes[i] = stsz.EntrySize[i]
		} else {
			return timebase.Newf(timebase.CodeCorruptField, tr.ID, stblOff,
				"stsz missing entry for sample %d", i+1)
		}
	}
	durs, e := expandStts(stts, count, tr.ID, stblOff)
	if e != nil {
		return e
	}
	ctos := make([]int64, 0, count)
	if len(ctts.Entries) > 0 {
		for _, ent := range ctts.Entries {
			for k := uint32(0); k < ent.SampleCount && len(ctos) < count; k++ {
				ctos = append(ctos, cttsOffset(ent))
			}
		}
	}
	sync := map[int]bool{}
	if haveStss {
		for _, n := range stss.SampleNumber {
			if n == 0 || int(n) > count {
				return timebase.Newf(timebase.CodeCorruptField, tr.ID, stblOff,
					"stss sample number %d outside 1..%d", n, count)
			}
			sync[int(n)-1] = true
		}
	} else {
		for i := 0; i < count; i++ {
			sync[i] = true
		}
	}
	// Walk chunks and distribute samples.
	sampleIdx := 0
	dts := int64(0)
	stscIdx := 0
	for chunkNo := 0; chunkNo < len(chunkOffsets) && sampleIdx < count; chunkNo++ {
		// advance stsc run covering chunk (1-based)
		for stscIdx+1 < len(stsc.Entries) && int(stsc.Entries[stscIdx+1].FirstChunk) <= chunkNo+1 {
			stscIdx++
		}
		if len(stsc.Entries) == 0 {
			return timebase.Newf(timebase.CodeCorruptField, tr.ID, stblOff, "stsc has no entries")
		}
		perChunk := int(stsc.Entries[stscIdx].SamplesPerChunk)
		if perChunk <= 0 {
			return timebase.Newf(timebase.CodeCorruptField, tr.ID, stblOff,
				"stsc chunk %d has zero samples", chunkNo+1)
		}
		off := chunkOffsets[chunkNo]
		for s := 0; s < perChunk && sampleIdx < count; s++ {
			sz := sizes[sampleIdx]
			if off+uint64(sz) > uint64(p.size) {
				return timebase.Newf(timebase.CodeBoxTruncated, tr.ID, int64(off),
					"sample %d [%d,%d) exceeds file size %d", sampleIdx+1, off, off+uint64(sz), p.size)
			}
			var cto int64
			if sampleIdx < len(ctos) {
				cto = ctos[sampleIdx]
			}
			smp := Sample{
				TrackID: tr.ID, Index: sampleIdx, Offset: int64(off), Size: sz,
				DTS: dts, Duration: durs[sampleIdx], CTO: cto, Sync: sync[sampleIdx],
			}
			if e := p.digestSample(&smp); e != nil {
				return e
			}
			tr.Samples = append(tr.Samples, smp)
			off += uint64(sz)
			dts += durs[sampleIdx]
			sampleIdx++
		}
	}
	if sampleIdx != count {
		return timebase.Newf(timebase.CodeCorruptField, tr.ID, stblOff,
			"sample tables describe %d of %d samples", sampleIdx, count)
	}
	if tr.Duration == 0 {
		tr.Duration = dts
	}
	return nil
}

func expandStts(stts *mp4.Stts, count int, track uint32, off int64) ([]int64, *timebase.Error) {
	durs := make([]int64, 0, count)
	for _, ent := range stts.Entries {
		for k := uint32(0); k < ent.SampleCount; k++ {
			durs = append(durs, int64(ent.SampleDelta))
		}
	}
	if len(durs) < count {
		return nil, timebase.Newf(timebase.CodeCorruptField, track, off,
			"stts covers %d of %d samples", len(durs), count)
	}
	return durs[:count], nil
}

func (p *parser) digestSample(s *Sample) *timebase.Error {
	if s.Size == 0 {
		h := sha256.Sum256(nil)
		s.Digest = hex.EncodeToString(h[:])
		return nil
	}
	if uint64(s.Size) > p.limits.box() {
		return timebase.Newf(timebase.CodeByteLimit, s.TrackID, s.Offset,
			"sample size %d exceeds per-box limit %d", s.Size, p.limits.box())
	}
	if e := p.addBytes(uint64(s.Size), s.Offset); e != nil {
		return e
	}
	buf := make([]byte, s.Size)
	if _, err := p.f.ReadAt(buf, s.Offset); err != nil {
		return timebase.Fail(timebase.CodeBoxTruncated, s.TrackID, s.Offset).Wrap(err, "read sample")
	}
	h := sha256.Sum256(buf)
	s.Digest = hex.EncodeToString(h[:])
	return nil
}

// applyFragments appends samples from every moof/traf/trun to their tracks.
func (p *parser) applyFragments() *timebase.Error {
	off := int64(0)
	lastDTS := map[uint32]int64{}
	for _, tr := range p.media.Tracks {
		if len(tr.Samples) > 0 {
			s := tr.Samples[len(tr.Samples)-1]
			lastDTS[tr.ID] = s.DTS + s.Duration
		}
	}
	for off < p.size {
		// Locate next moof at top level by re-reading headers.
		typ, size, hdr, e := p.headerAt(off)
		if e != nil {
			return e
		}
		if typ != "moof" {
			off += size
			continue
		}
		if e := p.parseMoof(off, size, hdr, lastDTS); e != nil {
			return e
		}
		off += size
	}
	return nil
}

func (p *parser) headerAt(off int64) (string, int64, int64, *timebase.Error) {
	var h [8]byte
	if _, err := p.f.ReadAt(h[:], off); err != nil {
		return "", 0, 0, timebase.Fail(timebase.CodeBoxTruncated, 0, off).Wrap(err, "read box header")
	}
	size := int64(beU32(h[:]))
	typ := string(h[4:8])
	hdr := int64(8)
	if size == 1 {
		var ext [8]byte
		if _, err := p.f.ReadAt(ext[:], off+8); err != nil {
			return "", 0, 0, timebase.Fail(timebase.CodeBoxTruncated, 0, off).Wrap(err, "read largesize")
		}
		size = int64(beU64(ext[:]))
		hdr = 16
	} else if size == 0 {
		size = p.size - off
	}
	if size < hdr || off+size > p.size {
		return "", 0, 0, timebase.Newf(timebase.CodeBoxTruncated, 0, off,
			"box %q size %d invalid at offset %d", typ, size, off)
	}
	return typ, size, hdr, nil
}

func (p *parser) parseMoof(off, size, hdr int64, lastDTS map[uint32]int64) *timebase.Error {
	payload, e := p.readPayload(off, size, hdr)
	if e != nil {
		return e
	}
	kids, e := childBoxes(payload, off+hdr)
	if e != nil {
		return e
	}
	for _, box := range kids {
		p.record(1, box.typ, box.off, uint64(box.size), uint64(box.hdr), "")
		if box.typ != "traf" {
			continue
		}
		if e := p.parseTraf(box, off, lastDTS); e != nil {
			return e
		}
	}
	return nil
}

func (p *parser) parseTraf(traf boxView, moofOff int64, lastDTS map[uint32]int64) *timebase.Error {
	kids, e := childBoxes(traf.payload, traf.off+traf.hdr)
	if e != nil {
		return e
	}
	var tfhd mp4.Tfhd
	var tfdt mp4.Tfdt
	var truns []boxView
	haveTfhd, haveTfdt := false, false
	for _, box := range kids {
		p.record(2, box.typ, box.off, uint64(box.size), uint64(box.hdr), "")
		switch box.typ {
		case "tfhd":
			if err := decode(box, &tfhd); err != nil {
				return timebase.Fail(timebase.CodeCorruptField, 0, box.off).Wrap(err, "decode tfhd")
			}
			haveTfhd = true
		case "tfdt":
			if err := decode(box, &tfdt); err != nil {
				return timebase.Fail(timebase.CodeCorruptField, tfhd.TrackID, box.off).Wrap(err, "decode tfdt")
			}
			haveTfdt = true
		case "trun":
			truns = append(truns, box)
		}
	}
	if !haveTfhd {
		return timebase.Newf(timebase.CodeCorruptField, 0, traf.off, "traf has no tfhd")
	}
	tr := p.tracks[tfhd.TrackID]
	if tr == nil {
		return timebase.Newf(timebase.CodeCorruptField, tfhd.TrackID, traf.off,
			"tfhd references unknown track %d", tfhd.TrackID)
	}
	def, ok := p.trex[tfhd.TrackID]
	if !ok {
		def = trexDefaults{sampleDescIndex: 1}
	}
	baseMedia := lastDTS[tfhd.TrackID]
	if haveTfdt {
		raw := tfdt.GetBaseMediaDecodeTime()
		if raw > uint64(1<<62) {
			return timebase.Newf(timebase.CodeTimeOverflow, tr.ID, traf.off,
				"tfdt base decode time %d overflows nanosecond mapping", raw)
		}
		baseMedia = int64(raw)
		if prev, seen := lastDTS[tfhd.TrackID]; seen && baseMedia < prev {
			return timebase.Newf(timebase.CodeTimeRegression, tr.ID, traf.off,
				"tfdt %d precedes previous decode time %d", baseMedia, prev)
		}
	}
	baseData := moofOff
	if tfhd.CheckFlag(mp4.TfhdBaseDataOffsetPresent) {
		baseData = int64(tfhd.BaseDataOffset)
	}
	if !tfhd.CheckFlag(mp4.TfhdDefaultBaseIsMoof) && !tfhd.CheckFlag(mp4.TfhdBaseDataOffsetPresent) {
		baseData = moofOff
	}
	nextDTS := baseMedia
	for _, trunBox := range truns {
		var trun mp4.Trun
		if err := decode(trunBox, &trun); err != nil {
			return timebase.Fail(timebase.CodeCorruptField, tr.ID, trunBox.off).Wrap(err, "decode trun")
		}
		dataOff := int64(baseData)
		if trun.CheckFlag(0x000001) {
			dataOff += int64(trun.DataOffset)
		}
		cursor := dataOff
		for i := 0; i < int(trun.SampleCount); i++ {
			dur := int64(def.duration)
			size := int64(def.size)
			flags := def.flags
			if i < len(trun.Entries) {
				ent := trun.Entries[i]
				if trun.CheckFlag(0x000100) {
					dur = int64(ent.SampleDuration)
				}
				if trun.CheckFlag(0x000200) {
					size = int64(ent.SampleSize)
				}
				if trun.CheckFlag(0x000400) {
					flags = ent.SampleFlags
				}
			}
			if i == 0 && trun.CheckFlag(0x000004) {
				flags = trun.FirstSampleFlags
			}
			if dur < 0 || size < 0 {
				return timebase.Newf(timebase.CodeCorruptField, tr.ID, trunBox.off,
					"trun sample %d has negative duration/size", i+1)
			}
			if cursor+size > p.size {
				return timebase.Newf(timebase.CodeBoxTruncated, tr.ID, cursor,
					"fragment sample [%d,%d) exceeds file size %d", cursor, cursor+size, p.size)
			}
			smp := Sample{
				TrackID: tr.ID, Index: len(tr.Samples), Offset: cursor, Size: uint32(size),
				DTS: nextDTS, Duration: dur, Sync: flags&syncFlagMask == 0,
			}
			if e := p.digestSample(&smp); e != nil {
				return e
			}
			tr.Samples = append(tr.Samples, smp)
			cursor += size
			nextDTS += dur
		}
	}
	lastDTS[tr.ID] = nextDTS
	if tr.Duration == 0 {
		tr.Duration = nextDTS
	}
	return nil
}

// computeTrack validates timeline monotonicity and fills derived fields.
func (p *parser) computeTrack(tr *Track) *timebase.Error {
	if tr.Kind == TrackAudio && tr.SampleRate == 0 {
		tr.SampleRate = tr.Timescale
	}
	// Validate that the track's timeline maps to nanoseconds without
	// overflow. The first and last samples bound every interior timestamp.
	if len(tr.Samples) > 0 {
		last := tr.Samples[len(tr.Samples)-1]
		if _, e := tr.Base.ToNanos(last.DTS+last.Duration, tr.ID, last.Offset); e != nil {
			return e
		}
		if _, e := tr.Base.ToNanos(tr.Samples[0].DTS, tr.ID, tr.Samples[0].Offset); e != nil {
			return e
		}
	}
	return nil
}

// sampleReader reads a sample's payload bytes under the parser limits.
func (p *parser) sampleBytes(s Sample) ([]byte, *timebase.Error) {
	if s.Size == 0 {
		return nil, nil
	}
	buf := make([]byte, s.Size)
	if _, err := p.f.ReadAt(buf, s.Offset); err != nil {
		return nil, timebase.Fail(timebase.CodeBoxTruncated, s.TrackID, s.Offset).Wrap(err, "read sample")
	}
	return buf, nil
}

// ensure fmt used (debug helper kept intentionally minimal).
var _ = fmt.Sprintf

func cttsOffset(e mp4.CttsEntry) int64 {
	// Version 1 stores signed composition offsets; version 0 unsigned.
	if e.SampleOffsetV1 != 0 || e.SampleOffsetV0 == 0 {
		return int64(e.SampleOffsetV1)
	}
	return int64(e.SampleOffsetV0)
}

// setAvcC records the raw avcC payload found in the first visual sample entry
// so clips can be written with an independently decodable configuration.
func (p *parser) setAvcC(tr *Track, entry boxView) {
	kids, err := childBoxes(entry.payload, entry.off+entry.hdr)
	if err != nil {
		return
	}
	for _, k := range kids {
		if k.typ == "avcC" {
			tr.AvcC = append([]byte(nil), k.payload...)
			return
		}
	}
}
