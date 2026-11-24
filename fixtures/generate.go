// Package fixtures builds deterministic synthetic MP4/MOV inputs and RTP
// sequences for tests and the demo command. Samples are generated, never
// downloaded; every byte of output is a function of the fixture spec.
package fixtures

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	mp4 "github.com/abema/go-mp4"

	"github.com/zld/mediapipe/index"
)

// TrackSpec describes one generated track.
type TrackSpec struct {
	Kind        string // "video" or "audio"
	Timescale   uint32
	SampleCount int
	SampleDelta uint32 // ticks per sample
	SyncEvery   int    // 0 means every sample is a keyframe
	// SyncOffsetPhase shifts the first keyframe by this many samples, which
	// models representation layers whose GOP phase differs slightly.
	SyncOffsetPhase int
	PayloadSize     int
}

// Spec describes one generated file.
type Spec struct {
	Name           string
	MovieTimescale uint32
	Tracks         []TrackSpec

	// Corrupt selects a deliberate whole-file defect: "mov", "truncate_moov",
	// "truncate_mdat", or "corrupt_box".
	Corrupt string
	// FrameDropEvery multiplies every Nth sample delta, creating a timing gap.
	FrameDropEvery int
	// WrapAt is the video sample number where the composition timeline resets
	// to a near-zero value, simulating a 32-bit timestamp wraparound.
	WrapAt int
	// TruncateBytes removes that many bytes from the file end.
	TruncateBytes int64
	// CorruptBox overwrites the payload after a box type with junk.
	CorruptBox string
	// ZeroDuration declares zero movie and track durations while keeping the
	// sample tables intact, producing an inconsistent container.
	ZeroDuration bool
}

// DefaultSpec returns a healthy eight-second 30 fps file with one
// AAC-timed audio track spanning the same interval.
func DefaultSpec(name string) Spec {
	return Spec{
		Name:           name,
		MovieTimescale: 1000,
		Tracks: []TrackSpec{
			{Kind: "video", Timescale: 30000, SampleCount: 240, SampleDelta: 1000, SyncEvery: 30, PayloadSize: 137},
			// 8 s at 48 kHz with 1024-tick AAC frames is 375 samples.
			{Kind: "audio", Timescale: 48000, SampleCount: 375, SampleDelta: 1024, SyncEvery: 0, PayloadSize: 61},
		},
	}
}

// Result describes generated outputs.
type Result struct {
	MainPath string
	RPTPath  string
}

// Generate writes the file described by spec into dir. A matching RTP
// sequence is written beside it for structurally healthy inputs.
func Generate(spec Spec, dir string) (*Result, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	data, err := buildFile(spec)
	if err != nil {
		return nil, err
	}
	if spec.TruncateBytes > 0 {
		if int64(len(data)) <= spec.TruncateBytes {
			return nil, fmt.Errorf("truncate %d exceeds file size %d", spec.TruncateBytes, len(data))
		}
		data = data[:int64(len(data))-spec.TruncateBytes]
	}
	mainPath := filepath.Join(dir, spec.Name)
	if err := index.WriteAtomic(mainPath, data); err != nil {
		return nil, err
	}

	res := &Result{MainPath: mainPath}
	if spec.Corrupt == "" && spec.TruncateBytes == 0 && spec.CorruptBox == "" {
		rseq := BuildRTPSequence(spec.Tracks[0], 0, 0, 0)
		b, err := json.Marshal(rseq)
		if err != nil {
			return nil, err
		}
		rseqPath := filepath.Join(dir, trimExt(spec.Name)+".rtp.json")
		if err := index.WriteAtomic(rseqPath, b); err != nil {
			return nil, err
		}
		res.RPTPath = rseqPath
	}
	return res, nil
}

// ftypBrands lists the major brand followed by compatible brands.
func (s Spec) ftypBrands() [][4]byte {
	if s.Corrupt == "mov" {
		q := [4]byte{'q', 't', ' ', ' '}
		return [][4]byte{q, q}
	}
	return [][4]byte{{'i', 's', 'o', 'm'}, {'m', 'p', '4', '2'}}
}

// ftypBytes writes a valid ftyp box with a pre-computed size: the 8-byte
// header, major brand, minor version, then compatible brands.
func ftypBytes(size uint32, brands [][4]byte) []byte {
	b := make([]byte, 0, size)
	b = append(b, byte(size>>24), byte(size>>16), byte(size>>8), byte(size))
	b = append(b, 'f', 't', 'y', 'p')
	b = append(b, brands[0][:]...)
	b = append(b, 0x00, 0x00, 0x02, 0x00) // minor_version 0x200
	for _, br := range brands[1:] {
		b = append(b, br[:]...)
	}
	return b
}

func trimExt(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			return name[:i]
		}
	}
	return name
}

type sampleRef struct {
	offset uint32
	size   uint32
}

func buildFile(spec Spec) ([]byte, error) {
	if len(spec.Tracks) == 0 {
		return nil, fmt.Errorf("spec %q has no tracks", spec.Name)
	}

	// The leading ftyp is written manually so its size is known before mdat
	// chunk offsets are computed.
	// ftyp: 8-byte header, 4-byte major brand, 4-byte minor version, then
	// one 4-byte entry per compatible brand (the major brand is not repeated).
	brands := spec.ftypBrands()
	ftypLen := uint32(16 + (len(brands)-1)*4)
	payloadBase := ftypLen + 8 // ftyp + mdat header

	var payload bytes.Buffer
	plans := make([][]sampleRef, 0, len(spec.Tracks))
	for ti, ts := range spec.Tracks {
		if ts.Timescale == 0 || ts.SampleCount <= 0 || ts.SampleDelta == 0 {
			return nil, fmt.Errorf("track %d has invalid timing", ti)
		}
		refs := make([]sampleRef, 0, ts.SampleCount)
		for i := 0; i < ts.SampleCount; i++ {
			size := uint32(ts.PayloadSize)
			if size == 0 {
				size = 1
			}
			refs = append(refs, sampleRef{offset: payloadBase + uint32(payload.Len()), size: size})
			payload.Write(makeSample(ts.Kind, i, int(size)))
		}
		plans = append(plans, refs)
	}

	out := newSeekBuffer()
	out.Write(ftypBytes(ftypLen, brands))
	w := mp4.NewWriter(out)
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, &mp4.Mdat{Data: payload.Bytes()}, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	if err := writeMoov(w, spec, plans); err != nil {
		return nil, err
	}

	data := out.Bytes()
	switch spec.Corrupt {
	case "truncate_moov":
		data = data[:len(data)-len(data)/3]
	case "truncate_mdat":
		data = breakMdatSize(data)
	case "corrupt_box":
		typ := spec.CorruptBox
		if typ == "" {
			typ = "stts"
		}
		data = corruptBox(data, typ)
	}
	return data, nil
}

// breakMdatSize rewrites the mdat size field so it claims more bytes than the
// file contains, the on-disk signature of a truncated transfer.
func breakMdatSize(data []byte) []byte {
	i := bytes.Index(data, []byte("mdat"))
	if i < 4 {
		return data
	}
	start := i - 4
	big := uint32(len(data)-start) + uint32(len(data)/2)
	data[start] = byte(big >> 24)
	data[start+1] = byte(big >> 16)
	data[start+2] = byte(big >> 8)
	data[start+3] = byte(big)
	return data
}

func corruptBox(data []byte, typ string) []byte {
	i := bytes.Index(data, []byte(typ))
	if i < 0 {
		return data
	}
	for k := 0; k < 16 && i+4+k < len(data); k++ {
		data[i+4+k] = 0xFF
	}
	return data
}

func makeSample(kind string, i, size int) []byte {
	b := make([]byte, size)
	b[0] = byte(i)
	if size > 1 {
		b[1] = byte(kind[0])
	}
	for j := 2; j < size; j++ {
		b[j] = byte((i*31 + j*7) & 0xFF)
	}
	return b
}

func writeMoov(w *mp4.Writer, spec Spec, plans [][]sampleRef) error {
	dur := uint64(0)
	for ti, ts := range spec.Tracks {
		d := uint64(len(plans[ti])) * uint64(ts.SampleDelta)
		dMovie := d * uint64(spec.MovieTimescale) / uint64(ts.Timescale)
		if dMovie > dur {
			dur = dMovie
		}
	}
	if spec.ZeroDuration {
		dur = 0
	}
	if err := start(w, mp4.BoxTypeMoov()); err != nil {
		return err
	}
	if err := start(w, mp4.BoxTypeMvhd()); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Mvhd{
		Timescale: spec.MovieTimescale, DurationV0: uint32(dur),
		Rate: 0x00010000, Volume: 0x0100, Matrix: defaultMatrix(),
		NextTrackID: uint32(len(spec.Tracks) + 1),
	}); err != nil {
		return err
	}
	if err := end(w); err != nil {
		return err
	}
	for ti, ts := range spec.Tracks {
		if err := writeTrak(w, spec, ts, plans[ti], uint32(ti+1)); err != nil {
			return err
		}
	}
	return end(w)
}

func start(w *mp4.Writer, typ mp4.BoxType) error {
	_, err := w.StartBox(&mp4.BoxInfo{Type: typ})
	return err
}

func end(w *mp4.Writer) error {
	_, err := w.EndBox()
	return err
}

func marshal(w *mp4.Writer, b mp4.IImmutableBox) error {
	_, err := mp4.Marshal(w, b, mp4.Context{})
	return err
}

func defaultMatrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}

func writeTrak(w *mp4.Writer, spec Spec, ts TrackSpec, refs []sampleRef, id uint32) error {
	kind4 := [4]byte{'v', 'i', 'd', 'e'}
	name := "vide"
	if ts.Kind == "audio" {
		kind4 = [4]byte{'s', 'o', 'u', 'n'}
		name = "soun"
	}
	mdhdDur := uint64(len(refs)) * uint64(ts.SampleDelta)
	if spec.ZeroDuration {
		mdhdDur = 0
	}
	tkhdDur := mdhdDur * uint64(spec.MovieTimescale) / uint64(ts.Timescale)

	if err := start(w, mp4.BoxTypeTrak()); err != nil {
		return err
	}
	if err := start(w, mp4.BoxTypeTkhd()); err != nil {
		return err
	}
	tkhd := &mp4.Tkhd{TrackID: id, DurationV0: uint32(tkhdDur), Matrix: defaultMatrix()}
	if ts.Kind == "audio" {
		tkhd.Volume = 0x0100
	} else {
		tkhd.Width, tkhd.Height = 480<<16, 270<<16
	}
	if err := marshal(w, tkhd); err != nil {
		return err
	}
	if err := end(w); err != nil {
		return err
	}

	if err := start(w, mp4.BoxTypeMdia()); err != nil {
		return err
	}
	if err := start(w, mp4.BoxTypeMdhd()); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Mdhd{Timescale: ts.Timescale, DurationV0: uint32(mdhdDur),
		Language: [3]byte{'u', 'n', 'd'}}); err != nil {
		return err
	}
	if err := end(w); err != nil {
		return err
	}
	if err := start(w, mp4.BoxTypeHdlr()); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Hdlr{HandlerType: kind4, Name: name}); err != nil {
		return err
	}
	if err := end(w); err != nil {
		return err
	}

	if err := start(w, mp4.BoxTypeMinf()); err != nil {
		return err
	}
	if ts.Kind == "audio" {
		if err := start(w, mp4.BoxTypeSmhd()); err != nil {
			return err
		}
		if err := marshal(w, &mp4.Smhd{}); err != nil {
			return err
		}
	} else {
		if err := start(w, mp4.BoxTypeVmhd()); err != nil {
			return err
		}
		if err := marshal(w, &mp4.Vmhd{}); err != nil {
			return err
		}
	}
	if err := end(w); err != nil { // vmhd/smhd
		return err
	}
	if err := writeDinf(w); err != nil {
		return err
	}
	if err := writeStbl(w, spec, ts, refs); err != nil {
		return err
	}
	if err := end(w); err != nil { // minf
		return err
	}
	if err := end(w); err != nil { // mdia
		return err
	}
	return end(w) // trak
}

func writeDinf(w *mp4.Writer) error {
	if err := start(w, mp4.BoxTypeDinf()); err != nil {
		return err
	}
	if err := start(w, mp4.BoxTypeDref()); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Dref{EntryCount: 1}); err != nil {
		return err
	}
	if err := start(w, mp4.BoxTypeUrl()); err != nil {
		return err
	}
	urlBox := &mp4.Url{}
	urlBox.SetFlags(mp4.UrlSelfContained)
	if err := marshal(w, urlBox); err != nil {
		return err
	}
	if err := end(w); err != nil { // url
		return err
	}
	if err := end(w); err != nil { // dref
		return err
	}
	return end(w) // dinf
}

func writeStbl(w *mp4.Writer, spec Spec, ts TrackSpec, refs []sampleRef) error {
	if err := start(w, mp4.BoxTypeStbl()); err != nil {
		return err
	}
	if err := start(w, mp4.BoxTypeStsd()); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Stsd{EntryCount: 0}); err != nil {
		return err
	}
	if err := end(w); err != nil {
		return err
	}

	sttsEntries := buildStts(spec, ts, len(refs))
	if err := start(w, mp4.BoxTypeStts()); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Stts{EntryCount: uint32(len(sttsEntries)), Entries: sttsEntries}); err != nil {
		return err
	}
	if err := end(w); err != nil {
		return err
	}

	if spec.WrapAt > 0 && ts.Kind == "video" {
		if err := writeWrapCtts(w, spec, ts, len(refs)); err != nil {
			return err
		}
	}

	if err := start(w, mp4.BoxTypeStsc()); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{
		{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1}}}); err != nil {
		return err
	}
	if err := end(w); err != nil {
		return err
	}

	sizes := make([]uint32, len(refs))
	chunks := make([]uint32, len(refs))
	for i, r := range refs {
		sizes[i] = r.size
		chunks[i] = r.offset
	}
	if err := start(w, mp4.BoxTypeStsz()); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Stsz{SampleCount: uint32(len(refs)), EntrySize: sizes}); err != nil {
		return err
	}
	if err := end(w); err != nil {
		return err
	}
	if err := start(w, mp4.BoxTypeStco()); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Stco{EntryCount: uint32(len(chunks)), ChunkOffset: chunks}); err != nil {
		return err
	}
	if err := end(w); err != nil {
		return err
	}

	if ts.SyncEvery > 0 {
		// Sample 1 is always a sync sample (the stream must start decodable);
		// SyncOffsetPhase shifts only the periodic keyframes after it. A small
		// phase models a layer whose GOP grid differs by a frame or two.
		sync := []uint32{1}
		phase := ts.SyncOffsetPhase
		if phase > ts.SyncEvery-1 {
			phase = ts.SyncEvery - 1
		}
		if phase < 0 {
			phase = 0
		}
		for i := 1 + ts.SyncEvery + phase; i <= len(refs); i += ts.SyncEvery {
			sync = append(sync, uint32(i))
		}
		if err := start(w, mp4.BoxTypeStss()); err != nil {
			return err
		}
		if err := marshal(w, &mp4.Stss{EntryCount: uint32(len(sync)), SampleNumber: sync}); err != nil {
			return err
		}
		if err := end(w); err != nil {
			return err
		}
	}
	return end(w)
}

func buildStts(spec Spec, ts TrackSpec, n int) []mp4.SttsEntry {
	entries := make([]mp4.SttsEntry, 0, n)
	appendDelta := func(delta uint32) {
		if k := len(entries); k > 0 && entries[k-1].SampleDelta == delta {
			entries[k-1].SampleCount++
			return
		}
		entries = append(entries, mp4.SttsEntry{SampleCount: 1, SampleDelta: delta})
	}
	for i := 0; i < n; i++ {
		delta := ts.SampleDelta
		if spec.FrameDropEvery > 0 && (i+1)%spec.FrameDropEvery == 0 {
			// A dropped frame lasts three frame intervals instead of one, a
			// visible timing gap that stays within A/V sync tolerance.
			delta = ts.SampleDelta * 3
		}
		appendDelta(delta)
	}
	return entries
}

// writeWrapCtts emits a version-1 ctts whose composition offset resets the
// composition timeline at WrapAt, which is how a wraparound presents in the
// file format. Samples before the reset are presented normally; from the reset
// onward, composition time restarts near zero.
func writeWrapCtts(w *mp4.Writer, spec Spec, ts TrackSpec, samples int) error {
	offset := int64(0)
	if spec.WrapAt > 0 && spec.WrapAt < samples {
		// Amount that maps sample WrapAt's decode time back to zero.
		offset = -int64(spec.WrapAt) * int64(ts.SampleDelta)
	}
	entries := []mp4.CttsEntry{
		{SampleCount: uint32(spec.WrapAt), SampleOffsetV1: 0},
		{SampleCount: uint32(samples - spec.WrapAt), SampleOffsetV1: int32(offset)},
	}
	if err := start(w, mp4.BoxTypeCtts()); err != nil {
		return err
	}
	ctts := &mp4.Ctts{EntryCount: uint32(len(entries)), Entries: entries}
	ctts.SetVersion(1)
	if err := marshal(w, ctts); err != nil {
		return err
	}
	return end(w)
}
