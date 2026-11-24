// Package remux turns a repaired elementary stream into indexed MP4
// fragments.
//
// The muxer is index-driven: it never scans for keyframes itself. For each
// output segment it resolves the index's packet offsets, copies the exact
// elementary payload bytes those packets carry, and writes a self-contained
// MP4 (ftyp + moov + mdat). A uuid box inside moov carries the per-sample
// source offsets, so a segment can be audited on its own without the sidecar
// manifest.
package remux

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	mp4 "github.com/abema/go-mp4"

	"tsforge/internal/index"
	"tsforge/internal/ts"
)

// Timescale is the media timescale written into mdhd/mvhd (90 kHz).
const Timescale = 90000

// Limits on a single output segment.
const (
	MaxSamplesPerSegment = 20_000
	MaxBytesPerSegment   = 512 << 20
)

// SegmentSource is the resolved input for one output segment: the records
// that make it up and where to read their bytes.
type SegmentSource struct {
	Number      int
	Records     []index.Record
	Snapshot    string
	IndexDigest string
	// InputPath is the source TS, opened read-only by Write.
	InputPath string
}

// Sample is one payload resolved from the source TS.
type Sample struct {
	Record   index.Record
	Data     []byte
	Keyframe bool
}

// TrackTiming summarises a segment's timeline for the caller.
type TrackTiming struct {
	StartDTS  int64
	EndDTS    int64
	StartPTS  int64
	EndPTS    int64
	Count     int
	Keyframes int
}

// Segment is one emitted file plus the facts its manifest needs.
type Segment struct {
	Number        int
	Path          string
	Bytes         int64
	PayloadHash   string
	SHA256        string
	SampleCount   int
	VideoSamples  int
	AudioSamples  int
	Keyframes     int
	SnapshotIDs   []string
	SourceOffsets []int64
	VideoTiming   TrackTiming
	AudioTiming   TrackTiming
}

// Writer resolves samples and writes MP4 segments.
type Writer struct {
	// Width and Height are the declared display size for the video track.
	Width, Height uint16
	// AudioChannels and AudioRate describe the audio track.
	AudioChannels uint16
	AudioRate     uint32
	// Version is embedded in the provenance uuid box.
	Version string
}

// DefaultWriter returns fixture-appropriate track parameters.
func DefaultWriter() *Writer {
	return &Writer{Width: 1920, Height: 1080, AudioChannels: 2, AudioRate: 48000, Version: "1.0.0"}
}

// ResolveSamples reads the exact source packets cited by each record, in the
// order the record lists them. Duplicates never appear here because the index
// does not cite them.
func ResolveSamples(inputPath string, recs []index.Record) ([]Sample, error) {
	f, err := os.Open(inputPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := make([]Sample, 0, len(recs))
	for _, r := range recs {
		offsets := r.SourceOffsets
		if len(offsets) == 0 {
			offsets = []int64{r.PacketOffset}
		}
		var data []byte
		for i, off := range offsets {
			pkt, err := ts.ReadPacket(f, off)
			if err != nil {
				return nil, fmt.Errorf("remux: record %d: %w", r.Seq, err)
			}
			if pkt.PID != r.PID {
				return nil, fmt.Errorf("remux: record %d cites pid 0x%04x but packet at %d is 0x%04x",
					r.Seq, r.PID, off, pkt.PID)
			}
			if !pkt.HasPayload {
				return nil, fmt.Errorf("remux: record %d cites packet %d without payload", r.Seq, off)
			}
			d := pkt.Payload
			if i == 0 {
				if stripped, ok := StripPESHeader(d); ok {
					d = stripped
				}
			}
			data = append(data, d...)
		}
		out = append(out, Sample{Record: r, Data: data, Keyframe: r.Keyframe})
	}
	return out, nil
}

// StripPESHeader removes the PES header from the first packet of a PES.
// ok is false when the bytes do not begin a PES, in which case they are
// returned unchanged.
func StripPESHeader(d []byte) ([]byte, bool) {
	if len(d) < 9 || d[0] != 0x00 || d[1] != 0x00 || d[2] != 0x01 {
		return d, false
	}
	hdrLen := int(d[8])
	if 9+hdrLen > len(d) {
		return nil, false
	}
	return d[9+hdrLen:], true
}

// WriteSegment writes one MP4 file for src and returns its facts. Records
// from more than one PSI snapshot are allowed only when the change coincides
// with a keyframe: that keyframe starts a new output, so an index entry can
// never straddle a remap.
func (w *Writer) WriteSegment(src SegmentSource, outPath string) (*Segment, error) {
	if len(src.Records) == 0 {
		return nil, errors.New("remux: segment without records")
	}
	if len(src.Records) > MaxSamplesPerSegment {
		return nil, fmt.Errorf("remux: segment has %d samples, limit %d", len(src.Records), MaxSamplesPerSegment)
	}
	openSnap := src.Records[0].SnapshotID
	for i := range src.Records {
		r := src.Records[i]
		if r.SnapshotID != openSnap && !r.Keyframe {
			return nil, fmt.Errorf("remux: sample %d crosses PSI mapping %s -> %s without a random access point",
				r.Seq, openSnap, r.SnapshotID)
		}
	}
	samples, err := ResolveSamples(src.InputPath, src.Records)
	if err != nil {
		return nil, err
	}

	var vSamples, aSamples []Sample
	for i := range samples {
		switch samples[i].Record.Kind {
		case index.KindVideo:
			vSamples = append(vSamples, samples[i])
		case index.KindAudio:
			aSamples = append(aSamples, samples[i])
		}
	}
	if len(vSamples) == 0 {
		return nil, errors.New("remux: segment has no video samples")
	}
	if len(vSamples) > 0 && !vSamples[0].Keyframe {
		return nil, fmt.Errorf("remux: segment %d does not open on a keyframe (seq %d)",
			src.Number, vSamples[0].Record.Seq)
	}

	vPayload, err := concatPayload(vSamples)
	if err != nil {
		return nil, err
	}
	aPayload, err := concatPayload(aSamples)
	if err != nil {
		return nil, err
	}

	seg := &Segment{
		Number:       src.Number,
		SHA256:       "",
		SampleCount:  len(samples),
		VideoSamples: len(vSamples),
		AudioSamples: len(aSamples),
		SnapshotIDs:  uniqueSnapshots(src.Records),
	}
	for _, s := range samples {
		seg.SourceOffsets = append(seg.SourceOffsets, sourceOffsetsOf(s.Record)...)
	}
	for _, s := range vSamples {
		if s.Keyframe {
			seg.Keyframes++
		}
	}
	seg.VideoTiming = TimingOf(vSamples)
	seg.AudioTiming = TimingOf(aSamples)

	// Build the complete file in memory, then commit atomically: a crash
	// mid-write can never leave a partial segment visible.
	var buf bytes.Buffer
	if err := w.buildFile(&buf, src, seg, vSamples, aSamples, vPayload, aPayload); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(buf.Bytes())
	seg.SHA256 = hex.EncodeToString(sum[:])
	// The payload digest covers elementary bytes only, independent of
	// container options. Each sample's SHA-256 is concatenated and hashed
	// again, matching the digest the pipeline records per index record.
	var ph strings.Builder
	for _, s := range samples {
		fh := sha256.Sum256(s.Data)
		ph.WriteString(hex.EncodeToString(fh[:]))
	}
	psum := sha256.Sum256([]byte(ph.String()))
	seg.PayloadHash = hex.EncodeToString(psum[:])

	if err := writeAtomic(outPath, buf.Bytes()); err != nil {
		return nil, err
	}
	seg.Path = outPath
	seg.Bytes = int64(buf.Len())
	return seg, nil
}

// SourceOffsetsOrFirst returns the record's packet citations.
func sourceOffsetsOf(r index.Record) []int64 {
	if len(r.SourceOffsets) > 0 {
		return r.SourceOffsets
	}
	return []int64{r.PacketOffset}
}

func TimingOf(samples []Sample) TrackTiming {
	if len(samples) == 0 {
		return TrackTiming{}
	}
	t := TrackTiming{
		StartDTS: samples[0].Record.DTS,
		EndDTS:   samples[len(samples)-1].Record.DTS,
		StartPTS: samples[0].Record.PTS,
		EndPTS:   samples[0].Record.PTS,
		Count:    len(samples),
	}
	for _, s := range samples {
		if s.Keyframe {
			t.Keyframes++
		}
		if s.Record.PTS > t.EndPTS {
			t.EndPTS = s.Record.PTS
		}
	}
	return t
}

func uniqueSnapshots(recs []index.Record) []string {
	seen := make(map[string]bool)
	var out []string
	for _, r := range recs {
		if !seen[r.SnapshotID] {
			seen[r.SnapshotID] = true
			out = append(out, r.SnapshotID)
		}
	}
	sort.Strings(out)
	return out
}

func concatPayload(samples []Sample) ([]byte, error) {
	var out []byte
	for _, s := range samples {
		if len(out)+len(s.Data) > MaxBytesPerSegment {
			return nil, fmt.Errorf("remux: segment exceeds %d-byte limit", MaxBytesPerSegment)
		}
		out = append(out, s.Data...)
	}
	return out, nil
}

// ---- MP4 writing ----

func (w *Writer) buildFile(buf *bytes.Buffer, src SegmentSource, seg *Segment,
	vSamples, aSamples []Sample, vPayload, aPayload []byte) error {

	if err := writeFtyp(buf); err != nil {
		return err
	}
	// moov includes the provenance uuid so a segment is self-describing.
	var moovBytes bytes.Buffer
	if err := w.writeMoov(&moovBytes, src, seg, vSamples, aSamples, vPayload, aPayload); err != nil {
		return err
	}
	if err := appendBox(buf, "moov", moovBytes.Bytes()); err != nil {
		return err
	}
	return appendBox(buf, "mdat", append(append([]byte(nil), vPayload...), aPayload...))
}

func writeFtyp(out *bytes.Buffer) error {
	ftyp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', '6'},
		MinorVersion: 0,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: mp4.BrandISO6()},
			{CompatibleBrand: mp4.BrandAVC1()},
			{CompatibleBrand: mp4.BrandMP41()},
		},
	}
	return marshalBox(out, mp4.BoxTypeFtyp(), ftyp)
}

// marshalBox writes one box header plus a marshalled payload.
func marshalBox(out *bytes.Buffer, t mp4.BoxType, box mp4.IImmutableBox) error {
	var payload bytes.Buffer
	if _, err := mp4.Marshal(&payload, box, mp4.Context{}); err != nil {
		return err
	}
	return appendBox(out, t.String(), payload.Bytes())
}

// appendBox writes a box with size = 8 + len(payload).
func appendBox(out *bytes.Buffer, typ string, payload []byte) error {
	if len(typ) != 4 {
		return fmt.Errorf("remux: invalid box type %q", typ)
	}
	size := uint32(8 + len(payload))
	out.Write([]byte{byte(size >> 24), byte(size >> 16), byte(size >> 8), byte(size)})
	out.WriteString(typ)
	out.Write(payload)
	return nil
}

// container writes a container box whose children are produced in order.
func container(out *bytes.Buffer, typ string, children func(*bytes.Buffer) error) error {
	var body bytes.Buffer
	if err := children(&body); err != nil {
		return err
	}
	return appendBox(out, typ, body.Bytes())
}

// fullBox is a helper for boxes with version/flags.
func fullBox(typ string, version uint8, flags uint32, body []byte) []byte {
	payload := make([]byte, 4, 4+len(body))
	payload[0] = version
	payload[1] = byte(flags >> 16)
	payload[2] = byte(flags >> 8)
	payload[3] = byte(flags)
	payload = append(payload, body...)
	return payload
}

func boxBytes(typ string, payload []byte) []byte {
	var b bytes.Buffer
	_ = appendBox(&b, typ, payload)
	return b.Bytes()
}

func u16(v uint16) []byte { return []byte{byte(v >> 8), byte(v)} }
func u32(v uint32) []byte { return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)} }
func u64(v uint64) []byte {
	return []byte{byte(v >> 56), byte(v >> 48), byte(v >> 40), byte(v >> 32),
		byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}

// writeMoov assembles the container hierarchy.
func (w *Writer) writeMoov(out *bytes.Buffer, src SegmentSource, seg *Segment,
	vSamples, aSamples []Sample, vPayload, aPayload []byte) error {

	// Layout of the upcoming mdat is needed for stco. Compute it exactly by
	// measuring the moov, then writing chunk offsets relative to the final
	// file, which is ftyp + moov + mdat.
	//
	// Strategy: build moov with chunk offsets of zero, measure bytes, compute
	// the offsets, then rebuild. The second build has identical size because
	// offsets are fixed-width.
	build := func(chunkV int64, chunkA int64) ([]byte, error) {
		var b bytes.Buffer
		moovBody := func(mo *bytes.Buffer) error {
			if err := writeMvhd(mo, seg, vSamples, aSamples); err != nil {
				return err
			}
			if err := w.writeTrak(mo, true, seg, vSamples, chunkV, w.Width, w.Height); err != nil {
				return err
			}
			if len(aSamples) > 0 {
				if err := w.writeTrak(mo, false, seg, aSamples, chunkA, 0, 0); err != nil {
					return err
				}
			}
			return w.writeProvenance(mo, src, seg)
		}
		if err := container(&b, "moov", moovBody); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}

	first, err := build(0, 0)
	if err != nil {
		return err
	}
	// ftyp size is fixed; moov is first; mdat header is 8 bytes.
	moovLen := int64(len(first))
	// The final file is ftyp + moov + mdat. Measure ftyp from the same
	// writer used for the real output so the offsets can never drift.
	var ftypTmp bytes.Buffer
	if err := writeFtyp(&ftypTmp); err != nil {
		return err
	}
	ftypLen := int64(ftypTmp.Len())
	mdatStart := ftypLen + moovLen + 8
	final, err := build(mdatStart, mdatStart+int64(len(vPayload)))
	if err != nil {
		return err
	}
	if int64(len(final)) != moovLen {
		return fmt.Errorf("remux: moov size changed between passes (%d -> %d)", moovLen, len(final))
	}
	// appendBox re-wraps the already-framed "moov" bytes; the caller expects
	// bare children, so strip the 8-byte header the container helper added.
	if len(final) < 8 || string(final[4:8]) != "moov" {
		return fmt.Errorf("remux: malformed moov container")
	}
	out.Write(final[8:])
	return nil
}

func writeMvhd(out *bytes.Buffer, seg *Segment, v, a []Sample) error {
	duration := uint32(0)
	end := int64(0)
	for _, s := range v {
		if s.Record.DTS+s.Record.Duration > end {
			end = s.Record.DTS + s.Record.Duration
		}
	}
	for _, s := range a {
		if s.Record.DTS+s.Record.Duration > end {
			end = s.Record.DTS + s.Record.Duration
		}
	}
	start := int64(0)
	if len(v) > 0 {
		start = v[0].Record.DTS
	}
	if end > start {
		// Duration is relative to the segment's first sample so every
		// segment is independently decodable from zero.
		duration = uint32(end - start)
	}
	mvhd := &mp4.Mvhd{
		FullBox:     mp4.FullBox{Version: 0},
		Timescale:   Timescale,
		DurationV0:  duration,
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
		NextTrackID: nextTrackID(v, a),
	}
	return marshalBox(out, mp4.BoxTypeMvhd(), mvhd)
}

func nextTrackID(v, a []Sample) uint32 {
	if len(a) > 0 {
		return 3
	}
	return 2
}

// writeTrak emits one track. The first sample's DTS becomes media time zero
// for this segment, captured in the edit list so presentation times remain
// comparable to the repaired timeline.
func (w *Writer) writeTrak(out *bytes.Buffer, video bool, seg *Segment, samples []Sample, chunkOffset int64, width, height uint16) error {
	if len(samples) == 0 {
		return nil
	}
	baseDTS := samples[0].Record.DTS
	body := func(t *bytes.Buffer) error {
		trackID := uint32(1)
		if !video {
			trackID = 2
		}
		dur := uint32(0)
		for _, s := range samples {
			dur += uint32(s.Record.Duration)
		}
		tkhd := &mp4.Tkhd{
			FullBox:    mp4.FullBox{Version: 0, Flags: [3]byte{0, 0, 1}},
			TrackID:    trackID,
			DurationV0: dur,
			Matrix:     [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
			Width:      uint32(width) << 16,
			Height:     uint32(height) << 16,
		}
		if !video {
			tkhd.Volume = 0x0100
		}
		if err := marshalBox(t, mp4.BoxTypeTkhd(), tkhd); err != nil {
			return err
		}
		// edts/elst maps media time zero to the segment's first DTS on the
		// repaired timeline, so a player reports the original presentation
		// time rather than a rebased one.
		if baseDTS > 0 {
			elst := &mp4.Elst{
				FullBox:    mp4.FullBox{Version: 0},
				EntryCount: 1,
				Entries: []mp4.ElstEntry{{
					SegmentDurationV0: dur,
					MediaTimeV0:       int32(baseDTS),
					MediaRateInteger:  1,
				}},
			}
			var elstOut bytes.Buffer
			if err := marshalBox(&elstOut, mp4.BoxTypeElst(), elst); err != nil {
				return err
			}
			if err := container(t, "edts", func(e *bytes.Buffer) error {
				e.Write(elstOut.Bytes())
				return nil
			}); err != nil {
				return err
			}
		}
		if err := container(t, "mdia", func(m *bytes.Buffer) error {
			mdhd := &mp4.Mdhd{
				FullBox:    mp4.FullBox{Version: 0},
				Timescale:  Timescale,
				DurationV0: dur,
				Language:   [3]byte{'u', 'n', 'd'},
			}
			if err := marshalBox(m, mp4.BoxTypeMdhd(), mdhd); err != nil {
				return err
			}
			hdlr := &mp4.Hdlr{FullBox: mp4.FullBox{Version: 0}}
			if video {
				hdlr.HandlerType = [4]byte{'v', 'i', 'd', 'e'}
				hdlr.Name = "VideoHandler"
			} else {
				hdlr.HandlerType = [4]byte{'s', 'o', 'u', 'n'}
				hdlr.Name = "SoundHandler"
			}
			if err := marshalBox(m, mp4.BoxTypeHdlr(), hdlr); err != nil {
				return err
			}
			return container(m, "minf", func(mi *bytes.Buffer) error {
				if video {
					if err := marshalBox(mi, mp4.BoxTypeVmhd(), &mp4.Vmhd{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 1}}}); err != nil {
						return err
					}
				} else {
					if err := marshalBox(mi, mp4.BoxTypeSmhd(), &mp4.Smhd{}); err != nil {
						return err
					}
				}
				if err := writeDinf(mi); err != nil {
					return err
				}
				return container(mi, "stbl", func(sb *bytes.Buffer) error {
					return writeStbl(sb, video, samples, baseDTS, chunkOffset, w)
				})
			})
		}); err != nil {
			return err
		}
		return nil
	}
	return container(out, "trak", body)
}

func writeDinf(out *bytes.Buffer) error {
	return container(out, "dinf", func(d *bytes.Buffer) error {
		return container(d, "dref", func(df *bytes.Buffer) error {
			df.Write(fullBox("dref", 0, 0, append(u32(1), boxBytes("url ", fullBox("url ", 0, 0x000001, nil))...)))
			return nil
		})
	})
}

// writeStbl writes the sample table. Samples must be in decode order with
// strictly increasing DTS.
func writeStbl(out *bytes.Buffer, video bool, samples []Sample, baseDTS int64, chunkOffset int64, w *Writer) error {
	if err := writeStsd(out, video, w); err != nil {
		return err
	}
	// stts run-length encodes decode deltas.
	type run struct{ count, delta uint32 }
	var runs []run
	for i := range samples {
		d := samples[i].Record.Duration
		if i+1 < len(samples) {
			d = samples[i+1].Record.DTS - samples[i].Record.DTS
		}
		if d <= 0 {
			d = 1
		}
		dd := uint32(d)
		if len(runs) > 0 && runs[len(runs)-1].delta == dd {
			runs[len(runs)-1].count++
		} else {
			runs = append(runs, run{count: 1, delta: dd})
		}
	}
	sttsBody := u32(uint32(len(runs)))
	for _, r := range runs {
		sttsBody = append(sttsBody, u32(r.count)...)
		sttsBody = append(sttsBody, u32(r.delta)...)
	}
	if err := appendBox(out, "stts", fullBox("stts", 0, 0, sttsBody)); err != nil {
		return err
	}
	// ctts carries the PTS-DTS offset; version 1 is signed, which is
	// required for B-frame reordering.
	var cruns []struct {
		count  uint32
		offset int32
	}
	for i := range samples {
		off := int32(samples[i].Record.PTS - samples[i].Record.DTS)
		if len(cruns) > 0 && cruns[len(cruns)-1].offset == off {
			cruns[len(cruns)-1].count++
		} else {
			cruns = append(cruns, struct {
				count  uint32
				offset int32
			}{count: 1, offset: off})
		}
	}
	cttsBody := u32(uint32(len(cruns)))
	for _, c := range cruns {
		cttsBody = append(cttsBody, u32(c.count)...)
		cttsBody = append(cttsBody, u32(uint32(c.offset))...)
	}
	if err := appendBox(out, "ctts", fullBox("ctts", 1, 0, cttsBody)); err != nil {
		return err
	}
	// stsc: one sample per chunk keeps offsets exact and auditable.
	stscBody := append(u32(1), u32(1)...)
	stscBody = append(stscBody, u32(1)...)
	stscBody = append(stscBody, u32(1)...)
	if err := appendBox(out, "stsc", fullBox("stsc", 0, 0, stscBody)); err != nil {
		return err
	}
	// stsz
	var stszBody []byte
	stszBody = append(stszBody, u32(0)...) // varying sizes
	stszBody = append(stszBody, u32(uint32(len(samples)))...)
	for _, s := range samples {
		stszBody = append(stszBody, u32(uint32(len(s.Data)))...)
	}
	if err := appendBox(out, "stsz", fullBox("stsz", 0, 0, stszBody)); err != nil {
		return err
	}
	// stco with one chunk per sample, so every source offset is explicit.
	stcoBody := u32(uint32(len(samples)))
	off := uint64(chunkOffset)
	for _, s := range samples {
		if off > 0xFFFFFFFF {
			return fmt.Errorf("remux: chunk offset %d exceeds 32-bit stco; use co64", off)
		}
		stcoBody = append(stcoBody, u32(uint32(off))...)
		off += uint64(len(s.Data))
	}
	if err := appendBox(out, "stco", fullBox("stco", 0, 0, stcoBody)); err != nil {
		return err
	}
	// stss lists keyframes for video tracks.
	if video {
		var keyIdx []uint32
		for i, s := range samples {
			if s.Keyframe {
				keyIdx = append(keyIdx, uint32(i+1))
			}
		}
		if len(keyIdx) == 0 {
			return errors.New("remux: video track has no sync sample")
		}
		body := u32(uint32(len(keyIdx)))
		for _, k := range keyIdx {
			body = append(body, u32(k)...)
		}
		if err := appendBox(out, "stss", fullBox("stss", 0, 0, body)); err != nil {
			return err
		}
	}
	return nil
}

func writeStsd(out *bytes.Buffer, video bool, w *Writer) error {
	return container(out, "stsd", func(st *bytes.Buffer) error {
		st.Write(fullBox("stsd", 0, 0, u32(1)))
		if video {
			vse := &mp4.VisualSampleEntry{
				SampleEntry: mp4.SampleEntry{AnyTypeBox: mp4.AnyTypeBox{Type: mp4.BoxTypeAvc1()}},
			}
			vse.DataReferenceIndex = 1
			vse.Width = w.Width
			vse.Height = w.Height
			vse.Horizresolution = 72 << 16
			vse.Vertresolution = 72 << 16
			vse.FrameCount = 1
			vse.Depth = 0x18
			vse.PreDefined3 = -1
			var entry bytes.Buffer
			if _, err := mp4.Marshal(&entry, vse, mp4.Context{}); err != nil {
				return err
			}
			avcc := &mp4.AVCDecoderConfiguration{
				AnyTypeBox:                 mp4.AnyTypeBox{Type: mp4.BoxTypeAvcC()},
				ConfigurationVersion:       1,
				Profile:                    0x42,
				ProfileCompatibility:       0xE0,
				Level:                      0x1F,
				LengthSizeMinusOne:         3,
				NumOfSequenceParameterSets: 1,
				NumOfPictureParameterSets:  1,
			}
			avcc.SequenceParameterSets = []mp4.AVCParameterSet{
				{Length: 9, NALUnit: []byte{0x67, 0x42, 0xE0, 0x1F, 0x8C, 0x8D, 0x40, 0x50, 0x1E}},
			}
			avcc.PictureParameterSets = []mp4.AVCParameterSet{
				{Length: 4, NALUnit: []byte{0x68, 0xCE, 0x3C, 0x80}},
			}
			var avccOut bytes.Buffer
			if err := marshalBox(&avccOut, mp4.BoxTypeAvcC(), avcc); err != nil {
				return err
			}
			return appendBox(st, "avc1", append(entry.Bytes(), avccOut.Bytes()...))
		}
		ase := &mp4.AudioSampleEntry{
			SampleEntry:  mp4.SampleEntry{AnyTypeBox: mp4.AnyTypeBox{Type: mp4.BoxTypeMp4a()}},
			ChannelCount: w.AudioChannels,
			SampleSize:   16,
			SampleRate:   w.AudioRate << 16,
		}
		ase.DataReferenceIndex = 1
		var entry bytes.Buffer
		if _, err := mp4.Marshal(&entry, ase, mp4.Context{}); err != nil {
			return err
		}
		// esds with an AAC-LC AudioSpecificConfig.
		asc := []byte{0x12, 0x10}
		esds := esdsBox(asc, w.AudioChannels, w.AudioRate)
		return appendBox(st, "mp4a", append(entry.Bytes(), esds...))
	})
}

// esdsBox builds a minimal MPEG-4 esds descriptor tree for AAC.
func esdsBox(asc []byte, channels uint16, rate uint32) []byte {
	decSpecific := []byte{0x05, byte(len(asc))}
	decSpecific = append(decSpecific, asc...)
	decConfig := []byte{
		0x04, byte(13 + len(decSpecific)),
		0x40,             // object type: AAC
		0x15,             // stream type audio, upstream 0, reserved 0
		0x00, 0x00, 0x00, // buffer size DB
		0x00, 0x00, 0x00, 0x00, // max bitrate
		0x00, 0x00, 0x00, 0x00, // avg bitrate
	}
	decConfig = append(decConfig, decSpecific...)
	slConfig := []byte{0x06, 0x01, 0x02}
	esDesc := []byte{0x03, byte(3 + len(decConfig) + len(slConfig)), 0x00, 0x01, 0x00}
	esDesc = append(esDesc, decConfig...)
	esDesc = append(esDesc, slConfig...)
	payload := fullBox("esds", 0, 0, esDesc)
	return boxBytes("esds", payload)
}

// ---- provenance uuid box ----

// uuidBoxType is the private box type that carries per-segment provenance.
const uuidBoxType = "tsfg"

// writeProvenance embeds the segment's source citation. The box is a
// standard uuid box: 4-byte size, "uuid", 16-byte UUID, then JSON metadata.
func (w *Writer) writeProvenance(out *bytes.Buffer, src SegmentSource, seg *Segment) error {
	meta := map[string]any{
		"tool":         "tsforge",
		"version":      w.Version,
		"segment":      seg.Number,
		"index_digest": src.IndexDigest,
		"snapshots":    seg.SnapshotIDs,
		"payload_sha":  seg.PayloadHash,
		// The record citations identify the source unambiguously by content.
		// The absolute source path is intentionally omitted so identical
		// input content produces byte-identical segments even when the input
		// file lives at a different path (for example, a demo rerun into a
		// different --out directory). The sidecar manifest carries the path.
		"records": provenanceRecords(src.Records),
	}
	b, err := stableJSON(meta)
	if err != nil {
		return err
	}
	// UUID: fixed namespace plus a digest of the index revision, so no two
	// distinct provenance blocks share an identifier.
	u := provenanceUUID(src.IndexDigest, seg.Number)
	payload := append(make([]byte, 0, 16+len(b)), u[:]...)
	payload = append(payload, b...)
	return appendBox(out, "uuid", payload)
}

func provenanceUUID(indexDigest string, segment int) [16]byte {
	var u [16]byte
	copy(u[:4], []byte(uuidBoxType))
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", indexDigest, segment)))
	copy(u[4:], h[:12])
	return u
}

func provenanceRecords(recs []index.Record) []map[string]any {
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		out = append(out, map[string]any{
			"pid":     r.PID,
			"kind":    string(r.Kind),
			"offsets": sourceOffsetsOf(r),
			"dts":     r.DTS,
			"pts":     r.PTS,
			"key":     r.Keyframe,
			"snap":    r.SnapshotID,
			"sha":     r.PayloadHash,
		})
	}
	return out
}

func stableJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

// ---- atomic file ops ----

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-seg-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var _ = binary.BigEndian
var _ = Timescale
