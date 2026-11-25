package fixture

import (
	"bytes"
	"encoding/binary"
	"os"
)

// MediaSpec describes one deterministic track to embed in an MP4 fixture.
type MediaSpec struct {
	Kind       byte // 'v' video, 'a' audio
	Timescale  uint32
	FrameCount uint32
	SampleSize uint32 // payload bytes per sample
	FrameDelta uint32 // default stts delta in track ticks
	Deltas     []uint32
	Key        byte // payload seed
}

// MP4Spec is the full MP4 fixture description.
type MP4Spec struct {
	Tracks []MediaSpec
}

// WriteMP4 renders a deterministic MP4 file containing ftyp, moov and mdat.
func WriteMP4(path string, spec MP4Spec) error {
	data, err := BuildMP4(spec)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// BuildMP4 returns the rendered bytes. stco offsets are computed from the
// measured layout, then patched into the emitted moov.
func BuildMP4(spec MP4Spec) ([]byte, error) {
	ftyp := ftypBox()
	moov := buildMoov(spec)
	mdatPayload := buildMdatPayload(spec)
	mdat := newBox("mdat").raw(mdatPayload)

	// Measure the exact stco entry positions by emitting a throwaway copy;
	// this guarantees the recorded offsets match the real byte layout.
	var probe bytes.Buffer
	probe.Write(ftyp)
	var stcoEntryPos []uint64
	moov.emitStcoPositions(&probe, &stcoEntryPos)
	ftypLen := uint64(len(ftyp))

	// Layout: ftyp | moov | mdat. Each track owns one chunk, laid out in
	// track order at the start of the mdat payload.
	mdatPayloadOff := ftypLen + moov.size() + 8
	patches := make([]patch, len(stcoEntryPos))
	running := mdatPayloadOff
	for i, pos := range stcoEntryPos {
		chunkBytes := uint64(spec.Tracks[i].FrameCount) * uint64(spec.Tracks[i].SampleSize)
		patches[i] = patch{at: pos, value: running}
		running += chunkBytes
	}

	var out bytes.Buffer
	out.Write(ftyp)
	emitWithPatches(moov, &out, patches)
	mdat.emit(&out)
	return out.Bytes(), nil
}

// patch is one deterministic, ordered stco overwrite.
type patch struct {
	at    uint64
	value uint64
}

func buildMoov(spec MP4Spec) *boxBuilder {
	moov := newBox("moov")
	for _, tr := range spec.Tracks {
		mdhd := newBox("mdhd").raw(fullBox(0, 0)).
			u32(0).u32(0).u32(tr.Timescale).u32(durationTicks(tr)).
			raw([]byte{0x55, 0xc4, 0, 0})
		hdlr := newBox("hdlr").raw(fullBox(0, 0)).u32(0)
		if tr.Kind == 'v' {
			hdlr.raw([]byte("vide"))
		} else {
			hdlr.raw([]byte("soun"))
		}
		hdlr.raw(make([]byte, 12)).raw([]byte{0})

		stts := newBox("stts").raw(fullBox(0, 0))
		runs := runLength(effectiveDeltas(tr))
		stts.u32(uint32(len(runs)))
		for _, rn := range runs {
			stts.u32(rn.count).u32(rn.delta)
		}
		stsz := newBox("stsz").raw(fullBox(0, 0)).u32(tr.SampleSize).u32(tr.FrameCount)
		stsc := newBox("stsc").raw(fullBox(0, 0)).u32(1).
			u32(1).u32(tr.FrameCount).u32(1)
		// stco: full-box header, one entry, and a 4-byte placeholder that
		// BuildMP4 overwrites with the absolute chunk offset.
		stco := newBox("stco").raw(fullBox(0, 0)).u32(1).u32(0)

		stbl := newBox("stbl").child(stts).child(stsz).child(stsc).child(stco)
		minf := newBox("minf").child(stbl)
		mdia := newBox("mdia").child(mdhd).child(hdlr).child(minf)
		moov.child(newBox("trak").child(mdia))
	}
	return moov
}

func buildMdatPayload(spec MP4Spec) []byte {
	var payload []byte
	for _, tr := range spec.Tracks {
		for s := uint32(0); s < tr.FrameCount; s++ {
			payload = append(payload, deterministicBytes(tr.Key+byte(s), int(tr.SampleSize))...)
		}
	}
	return payload
}

func emitWithPatches(b *boxBuilder, out *bytes.Buffer, patches []patch) {
	b.emit(out)
	raw := out.Bytes()
	for _, p := range patches {
		binary.BigEndian.PutUint32(raw[p.at:p.at+4], uint32(p.value))
	}
}

type runEntry struct {
	count uint32
	delta uint32
}

func effectiveDeltas(tr MediaSpec) []uint32 {
	if len(tr.Deltas) > 0 {
		return tr.Deltas
	}
	d := make([]uint32, tr.FrameCount)
	for i := range d {
		d[i] = tr.FrameDelta
	}
	return d
}

func runLength(deltas []uint32) []runEntry {
	var out []runEntry
	for _, d := range deltas {
		if n := len(out); n > 0 && out[n-1].delta == d {
			out[n-1].count++
			continue
		}
		out = append(out, runEntry{count: 1, delta: d})
	}
	return out
}

func durationTicks(tr MediaSpec) uint32 {
	var sum uint64
	for _, d := range effectiveDeltas(tr) {
		sum += uint64(d)
	}
	return uint32(sum)
}

func ftypBox() []byte {
	b := newBox("ftyp").
		raw([]byte("isom")).u32(0x200).raw([]byte("isom")).raw([]byte("iso2"))
	var buf bytes.Buffer
	b.emit(&buf)
	return buf.Bytes()
}

// StandaloneFtyp returns a valid file containing nothing but an ftyp box,
// used to exercise the missing-track error path.
func StandaloneFtyp() []byte {
	return ftypBox()
}
