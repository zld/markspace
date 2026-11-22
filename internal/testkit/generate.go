// Package testkit generates every fixture the kernel consumes. All data is
// synthesized locally from integer seeds: no external files, no network.
// The generated MP4 files are structurally valid (go-mp4 written) while
// payload bytes are compact placeholders; logical network sizes live in the
// manifest sidecar so 3600 s manifests stay cheap on disk yet remain
// numerically honest for bitrate and quality statistics.
package testkit

import (
	"encoding/binary"
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"

	"mediacore/internal/clock"
)

// MP4Spec fully determines one generated MP4 + manifest pair.
type MP4Spec struct {
	Path        string
	Manifest    string
	TrackID     uint32
	Timescale   uint32
	SampleCount uint32
	// SampleDelta is the constant stts delta (ticks). Duration is
	// SampleCount * SampleDelta ticks.
	SampleDelta uint32
	// KeyEvery marks every Nth sample (1-based numbering) as sync. 0 means
	// every sample is sync.
	KeyEvery uint32
	// MediaStartTicks sets the mdhd-origin edit-list non-zero start.
	MediaStartTicks int64
	// TargetBPS calibrates the mean logical wire size; jitter is seeded.
	TargetBPS uint64
	JitterPct uint32
	Seed      uint64
	// ForceNonSync suppresses sync flags for these 1-based numbers.
	ForceNonSync []uint32
	// ForceMdhdDuration, when non-zero, overrides the computed mdhd duration.
	// Used by the edit-out-of-bounds fixture so the edit window genuinely
	// exceeds the declared media duration.
	ForceMdhdDuration uint32
}

// WriteMP4 emits a single-track MP4: ftyp, moov (tkhd/mdhd/edts/stbl) and
// mdat. Physical payloads are 8-byte deterministic placeholders each.
func WriteMP4(spec MP4Spec) error {
	if spec.SampleCount == 0 || spec.Timescale == 0 || spec.SampleDelta == 0 {
		return clock.ErrInvalidArgument("testkit: sample count, timescale and delta must be non-zero")
	}
	f, err := os.Create(spec.Path)
	if err != nil {
		return clock.ErrRecoverablef("testkit.mp4", "create: %v", err)
	}
	defer f.Close()
	w := mp4.NewWriter(f)

	if err := startBox(w, mp4.BoxTypeFtyp()); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '2'}},
		},
	}, mp4.Context{}); err != nil {
		return clock.ErrRecoverablef("testkit.mp4", "ftyp: %v", err)
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	// The media timeline must cover the edit-list window: samples start at
	// MediaStartTicks, so mdhd duration spans the full start + samples.
	samplesSpan := uint64(spec.SampleCount) * uint64(spec.SampleDelta)
	duration := uint64(spec.MediaStartTicks) + samplesSpan
	if spec.ForceMdhdDuration != 0 {
		duration = uint64(spec.ForceMdhdDuration)
	}
	if err := writeMoov(w, spec, duration); err != nil {
		return err
	}
	if err := writeMdat(w, spec); err != nil {
		return err
	}
	return nil
}

func startBox(w *mp4.Writer, t mp4.BoxType) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: t}); err != nil {
		return clock.ErrRecoverablef("testkit.mp4", "start box %s: %v", string(t[:]), err)
	}
	return nil
}

func endBox(w *mp4.Writer) error {
	if _, err := w.EndBox(); err != nil {
		return clock.ErrRecoverablef("testkit.mp4", "end box: %v", err)
	}
	return nil
}

func writeMoov(w *mp4.Writer, spec MP4Spec, duration uint64) error {
	if err := startBox(w, mp4.BoxTypeMoov()); err != nil {
		return err
	}
	if err := startBox(w, mp4.BoxTypeTrak()); err != nil {
		return err
	}
	if err := leafBox(w, mp4.BoxTypeTkhd(), &mp4.Tkhd{
		FullBox:    mp4.FullBox{Version: 0},
		TrackID:    spec.TrackID,
		DurationV0: uint32(duration),
		Width:      320 << 16,
		Height:     240 << 16,
	}); err != nil {
		return err
	}
	if err := startBox(w, mp4.BoxTypeMdia()); err != nil {
		return err
	}
	if err := leafBox(w, mp4.BoxTypeMdhd(), &mp4.Mdhd{
		FullBox:    mp4.FullBox{Version: 0},
		Timescale:  spec.Timescale,
		DurationV0: uint32(duration),
		Language:   [3]byte{'u', 'n', 'd'},
	}); err != nil {
		return err
	}
	if err := startBox(w, mp4.BoxTypeMinf()); err != nil {
		return err
	}
	if err := writeStbl(w, spec); err != nil {
		return err
	}
	if err := endBox(w); err != nil { // minf
		return err
	}
	if err := endBox(w); err != nil { // mdia
		return err
	}
	// edts is a sibling of mdia under trak, per ISO 14496-12.
	if err := writeEdts(w, spec, duration); err != nil {
		return err
	}
	if err := endBox(w); err != nil { // trak
		return err
	}
	return endBox(w) // moov
}

// leafBox wraps Marshal with the required StartBox/EndBox pair.
func leafBox(w *mp4.Writer, t mp4.BoxType, box mp4.IBox) error {
	if err := startBox(w, t); err != nil {
		return err
	}
	if err := marshalBox(w, box); err != nil {
		return err
	}
	return endBox(w)
}

func marshalBox(w io.Writer, box mp4.IBox) error {
	if _, err := mp4.Marshal(w, box, mp4.Context{}); err != nil {
		return clock.ErrRecoverablef("testkit.mp4", "marshal: %v", err)
	}
	return nil
}

func writeEdts(w *mp4.Writer, spec MP4Spec, duration uint64) error {
	if spec.MediaStartTicks == 0 {
		return nil
	}
	if err := startBox(w, mp4.BoxTypeEdts()); err != nil {
		return err
	}
	elst := &mp4.Elst{FullBox: mp4.FullBox{Version: 0}, EntryCount: 1}
	samplesSpan := uint64(spec.SampleCount) * uint64(spec.SampleDelta)
	elst.Entries = []mp4.ElstEntry{{
		SegmentDurationV0: uint32(samplesSpan),
		MediaTimeV0:       int32(spec.MediaStartTicks),
		MediaRateInteger:  1,
	}}
	if err := leafBox(w, mp4.BoxTypeElst(), elst); err != nil {
		return err
	}
	return endBox(w) // edts
}

func writeStbl(w *mp4.Writer, spec MP4Spec) error {
	if err := startBox(w, mp4.BoxTypeStbl()); err != nil {
		return err
	}
	if err := writeStsd(w, spec); err != nil {
		return err
	}
	if err := leafBox(w, mp4.BoxTypeStts(), &mp4.Stts{
		EntryCount: 1,
		Entries:    []mp4.SttsEntry{{SampleCount: spec.SampleCount, SampleDelta: spec.SampleDelta}},
	}); err != nil {
		return err
	}
	if err := writeStss(w, spec); err != nil {
		return err
	}
	if err := leafBox(w, mp4.BoxTypeStsc(), &mp4.Stsc{
		EntryCount: 1,
		Entries:    []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: spec.SampleCount, SampleDescriptionIndex: 1}},
	}); err != nil {
		return err
	}
	if err := leafBox(w, mp4.BoxTypeStsz(), &mp4.Stsz{
		SampleSize:  physicalPayload,
		SampleCount: spec.SampleCount,
	}); err != nil {
		return err
	}
	// stco: single chunk offset; mdat directly follows stbl/minf/mdia/trak/
	// moov close (4 boxes * 8 bytes) plus the mdat header.
	pos, err := w.Seek(0, io.SeekCurrent)
	if err != nil {
		return clock.ErrRecoverablef("testkit.mp4", "stco position: %v", err)
	}
	chunkOffset := uint64(pos) + 8 /* this stco box */ +
		8 /* stbl end */ + 8 /* minf end */ + 8 /* mdia end */ + 8 /* trak end */ +
		8 /* moov end */ + 8 /* mdat header */
	if err := leafBox(w, mp4.BoxTypeStco(), &mp4.Stco{
		EntryCount:  1,
		ChunkOffset: []uint32{uint32(chunkOffset)},
	}); err != nil {
		return err
	}
	return endBox(w) // stbl
}

// writeStsd emits one avc1 visual sample entry. The entry carries no codec
// config (payloads are never decoded), but its presence makes the track a
// valid single-description video track for the scanner.
func writeStsd(w *mp4.Writer, spec MP4Spec) error {
	if err := startBox(w, mp4.BoxTypeStsd()); err != nil {
		return err
	}
	if err := marshalBox(w, &mp4.Stsd{FullBox: mp4.FullBox{}, EntryCount: 1}); err != nil {
		return err
	}
	entry := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeAvc1()},
			DataReferenceIndex: 1,
		},
		Width:           320,
		Height:          240,
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
		PreDefined3:     -1,
	}
	if err := leafBox(w, mp4.BoxTypeAvc1(), entry); err != nil {
		return err
	}
	return endBox(w) // stsd
}

func writeStss(w *mp4.Writer, spec MP4Spec) error {
	if spec.KeyEvery == 0 {
		return nil // absent stss => all samples are sync
	}
	var nums []uint32
	for n := uint32(1); n <= spec.SampleCount; n++ {
		if (n-1)%spec.KeyEvery == 0 {
			nums = append(nums, n)
		}
	}
	for _, forced := range spec.ForceNonSync {
		for i, n := range nums {
			if n == forced {
				nums = append(nums[:i], nums[i+1:]...)
				break
			}
		}
	}
	// Always emit the stss box (even when empty) so the scanner distinguishes
	// "explicitly no sync samples" from "stss absent => all sync".
	return leafBox(w, mp4.BoxTypeStss(), &mp4.Stss{
		EntryCount:   uint32(len(nums)),
		SampleNumber: nums,
	})
}

func writeMdat(w *mp4.Writer, spec MP4Spec) error {
	if err := startBox(w, mp4.BoxTypeMdat()); err != nil {
		return err
	}
	payload := make([]byte, physicalPayload)
	for i := uint32(0); i < spec.SampleCount; i++ {
		binary.BigEndian.PutUint32(payload[0:4], i)
		binary.BigEndian.PutUint32(payload[4:8], uint32(spec.Seed))
		if _, err := w.Write(payload); err != nil {
			return clock.ErrRecoverablef("testkit.mp4", "mdat sample %d: %v", i, err)
		}
	}
	return endBox(w)
}

// physicalPayload is the compact placeholder size (8 bytes) of every sample
// inside mdat; logical network sizes are carried by the manifest.
const physicalPayload = 8
