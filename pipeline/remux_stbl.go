package pipeline

import (
	"mediacheck/ebml"
	"mediacheck/timeline"
)

// writeMinf emits media information: media header, data reference and the
// sample table describing every chunk offset and size.
func (b *boxWriter) writeMinf(spec *ebml.TrackSpec, samples []trackSample, chunkStart uint64, isVideo bool) error {
	if _, err := b.start("minf"); err != nil {
		return err
	}
	// Media header box: vmhd (video) or smhd (audio).
	if _, err := b.start(mediaHeaderType(isVideo)); err != nil {
		return err
	}
	if err := b.raw(fullBox(0, 0)); err != nil {
		return err
	}
	if !isVideo {
		if err := b.raw([]byte{0, 0}); err != nil { // smhd balance
			return err
		}
	}
	if err := b.end(); err != nil {
		return err
	}
	if err := b.writeDinf(); err != nil {
		return err
	}
	if err := b.writeStbl(spec, samples, chunkStart, isVideo); err != nil {
		return err
	}
	return b.end()
}

func mediaHeaderType(isVideo bool) string {
	if isVideo {
		return "vmhd"
	}
	return "smhd"
}

func (b *boxWriter) writeDinf() error {
	if _, err := b.start("dinf"); err != nil {
		return err
	}
	if _, err := b.start("dref"); err != nil {
		return err
	}
	payload := fullBox(0, 0)
	putU32b(&payload, 1) // one entry
	if err := b.raw(payload); err != nil {
		return err
	}
	if _, err := b.start("url "); err != nil {
		return err
	}
	if err := b.raw(fullBox(0, 1)); err != nil { // self-contained flag
		return err
	}
	if err := b.end(); err != nil { // url
		return err
	}
	if err := b.end(); err != nil { // dref
		return err
	}
	return b.end() // dinf
}

func (b *boxWriter) writeStbl(spec *ebml.TrackSpec, samples []trackSample, chunkStart uint64, isVideo bool) error {
	if _, err := b.start("stbl"); err != nil {
		return err
	}
	if err := b.writeStsd(spec, isVideo); err != nil {
		return err
	}
	if err := b.writeStts(samples); err != nil {
		return err
	}
	if err := b.writeStsc(samples); err != nil {
		return err
	}
	if err := b.writeStsz(samples); err != nil {
		return err
	}
	if err := b.writeStco(samples, chunkStart); err != nil {
		return err
	}
	return b.end()
}

// writeStsd emits a single harness sample entry. It is structurally sized so
// generic parsers can walk the table even though the codec is test-private.
func (b *boxWriter) writeStsd(spec *ebml.TrackSpec, isVideo bool) error {
	if _, err := b.start("stsd"); err != nil {
		return err
	}
	payload := fullBox(0, 0)
	putU32b(&payload, 1) // one sample description
	if err := b.raw(payload); err != nil {
		return err
	}
	entryType := "mp4a"
	if isVideo {
		entryType = "mp4v"
	}
	if _, err := b.start(entryType); err != nil {
		return err
	}
	entry := make([]byte, 0)
	entry = append(entry, make([]byte, 6)...) // reserved
	putU16b(&entry, 1)                        // data reference index
	if isVideo {
		putU16b(&entry, 0) // pre-defined
		putU16b(&entry, 0) // reserved
		entry = append(entry, make([]byte, 12)...)
		putU16b(&entry, uint16(spec.Width))
		putU16b(&entry, uint16(spec.Height))
		putU32b(&entry, 0x00480000) // horizresolution 72 dpi
		putU32b(&entry, 0x00480000)
		putU32b(&entry, 0) // reserved
		putU16b(&entry, 1) // frame count
		var compressor [32]byte
		entry = append(entry, compressor[:]...)
		putU16b(&entry, 0x0018) // depth 24
		putU16b(&entry, 0xFFFF) // pre-defined
	} else {
		putU16b(&entry, uint16(spec.Channels))
		putU16b(&entry, uint16(spec.BitDepth))
		putU16b(&entry, 0)
		putU32b(&entry, spec.SampleRate<<16)
	}
	if err := b.raw(entry); err != nil {
		return err
	}
	if err := b.end(); err != nil { // entry
		return err
	}
	return b.end() // stsd
}

func (b *boxWriter) writeStts(samples []trackSample) error {
	if _, err := b.start("stts"); err != nil {
		return err
	}
	payload := fullBox(0, 0)
	putU32b(&payload, uint32(len(samples)))
	// Every sample shares the same duration: one run covers them all.
	if len(samples) > 0 {
		putU32b(&payload, uint32(len(samples)))
		putU32b(&payload, samples[0].duration)
	}
	if err := b.raw(payload); err != nil {
		return err
	}
	return b.end()
}

func (b *boxWriter) writeStsc(samples []trackSample) error {
	if _, err := b.start("stsc"); err != nil {
		return err
	}
	payload := fullBox(0, 0)
	putU32b(&payload, 1) // one run
	putU32b(&payload, 1) // first chunk
	putU32b(&payload, 1) // one sample per chunk
	putU32b(&payload, 1) // sample description index
	if err := b.raw(payload); err != nil {
		return err
	}
	return b.end()
}

func (b *boxWriter) writeStsz(samples []trackSample) error {
	if _, err := b.start("stsz"); err != nil {
		return err
	}
	payload := fullBox(0, 0)
	putU32b(&payload, 0) // uniform size = 0, sizes follow
	putU32b(&payload, uint32(len(samples)))
	for _, s := range samples {
		putU32b(&payload, uint32(len(s.payload)))
	}
	if err := b.raw(payload); err != nil {
		return err
	}
	return b.end()
}

// writeStco gives the absolute file offset of every one-sample chunk.
func (b *boxWriter) writeStco(samples []trackSample, chunkStart uint64) error {
	if _, err := b.start("stco"); err != nil {
		return err
	}
	payload := fullBox(0, 0)
	putU32b(&payload, uint32(len(samples)))
	off := chunkStart
	for _, s := range samples {
		if off > 0xFFFFFFFF {
			return timeline.New(timeline.CatOverflow, "chunk offset exceeds 32-bit stco")
		}
		putU32b(&payload, uint32(off))
		off += uint64(len(s.payload))
	}
	if err := b.raw(payload); err != nil {
		return err
	}
	return b.end()
}
