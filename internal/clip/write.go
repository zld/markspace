package clip

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"

	"replay/internal/clock"
	"replay/internal/media"
)

// identityMatrix is the standard unrotated matrix (16.16 fixed point).
var identityMatrix = [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}

// rleEntry is one run-length-encoded (count, value) pair.
type rleEntry struct {
	count uint32
	value uint32
}

func runLength(values []uint32) []rleEntry {
	var out []rleEntry
	for _, v := range values {
		if n := len(out); n > 0 && out[n-1].value == v {
			out[n-1].count++
		} else {
			out = append(out, rleEntry{count: 1, value: v})
		}
	}
	return out
}

// tables holds the rewritten sample tables for one output track.
type tables struct {
	track   *media.TrackTimeline
	src     *media.TrackBoxes
	rng     Range
	durs    []uint32
	ctsoff  []int32
	sizes   []uint32
	co64    []uint64 // absolute output offsets, filled during streaming
	stts    []mp4.SttsEntry
	ctts    []mp4.CttsEntry
	stsc    []mp4.StscEntry
	stss    []uint32 // 1-based output sample numbers
	basePTS int64
}

func buildTables(track *media.TrackTimeline, src *media.TrackBoxes, rng Range) *tables {
	count := rng.Last - rng.First + 1
	tb := &tables{track: track, src: src, rng: rng,
		durs: make([]uint32, 0, count), sizes: make([]uint32, 0, count),
		ctsoff: make([]int32, 0, count), basePTS: track.Samples[rng.First].PTS}
	hasCTTS := src.Ctts != nil
	hasSTSS := src.Stss != nil && track.Handler == media.HandlerVideo
	for i := rng.First; i <= rng.Last; i++ {
		s := track.Samples[i]
		tb.durs = append(tb.durs, s.Dur)
		tb.sizes = append(tb.sizes, s.Size)
		if hasCTTS {
			tb.ctsoff = append(tb.ctsoff, s.CTSOff)
		}
		if hasSTSS && s.IsSync {
			tb.stss = append(tb.stss, i-rng.First+1)
		}
	}
	for _, e := range runLength(tb.durs) {
		tb.stts = append(tb.stts, mp4.SttsEntry{SampleCount: e.count, SampleDelta: e.value})
	}
	if hasCTTS {
		// Normalize composition offsets so the output timeline starts at 0.
		first := tb.ctsoff[0]
		for i := range tb.ctsoff {
			tb.ctsoff[i] -= first
		}
		for _, e := range runLength(int32AsUint32(tb.ctsoff)) {
			tb.ctts = append(tb.ctts, mp4.CttsEntry{
				SampleCount: e.count, SampleOffsetV1: int32(e.value),
			})
		}
	}
	tb.stsc = []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: count, SampleDescriptionIndex: 1}}
	return tb
}

func int32AsUint32(in []int32) []uint32 {
	out := make([]uint32, len(in))
	for i, v := range in {
		out[i] = uint32(v)
	}
	return out
}

// Cut builds the fragment for spec at outPath. Samples are streamed from
// the source into a single mdat chunk; moov is written afterwards with
// co64 offsets so the output has no 32-bit offset limitation.
func (b *Builder) Cut(spec ClipSpec, outPath string) (res *ClipResult, retErr error) {
	vr, err := b.pickVideoRange(spec)
	if err != nil {
		return nil, err
	}
	var videoSrc, audioSrc *media.TrackBoxes
	for _, tb := range b.Info.Tracks {
		switch tb.TrackID {
		case b.TL.Video.TrackID:
			videoSrc = tb
		}
		if b.TL.Audio != nil && tb.TrackID == b.TL.Audio.TrackID {
			audioSrc = tb
		}
	}
	vt := buildTables(b.TL.Video, videoSrc, vr)
	var at *tables
	if ar := b.pickAudioRange(vr, b.TL.Video.Timescale); ar != nil {
		at = buildTables(b.TL.Audio, audioSrc, *ar)
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return nil, err
	}
	src, err := os.Open(b.Info.Path)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	b.f = src
	out, err := os.Create(outPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		out.Close()
		if retErr != nil {
			os.Remove(outPath)
		}
	}()
	w := mp4.NewWriter(out)
	if err := writeFtyp(w); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return nil, err
	}
	copies, err := b.streamTrack(vt, w, out)
	if err != nil {
		return nil, err
	}
	if at != nil {
		ac, err := b.streamTrack(at, w, out)
		if err != nil {
			return nil, err
		}
		copies = append(copies, ac...)
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	if err := writeMoov(w, b, vt, at); err != nil {
		return nil, err
	}
	fi, err := out.Stat()
	if err != nil {
		return nil, err
	}
	last := b.TL.Video.Samples[vr.Last]
	res = &ClipResult{
		EventID: spec.EventID, FileName: filepath.Base(outPath),
		Size: fi.Size(), VideoRange: vr,
		StartPTS: vt.basePTS,
		EndPTS:   last.PTS + int64(last.Dur),
		Duration: last.PTS + int64(last.Dur) - vt.basePTS,
		Timescale: b.TL.Video.Timescale, SampleOff: copies,
	}
	if at != nil {
		r := *at.rngRef()
		res.AudioRange = &r
	}
	// Hash the complete delivered file (boxes included) so the digest
	// matches what the cache verifies on a materialized artifact.
	sum, err := hashWholeFile(outPath)
	if err != nil {
		return nil, err
	}
	res.SHA256 = sum
	res.SHA256Hex = hex.EncodeToString(sum[:])
	return res, nil
}

// hashWholeFile streams the file through SHA-256 with a bounded buffer.
func hashWholeFile(path string) ([32]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 64*1024)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return [32]byte{}, err
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// streamTrack appends one track's samples into the open mdat, recording
// absolute output offsets and per-sample provenance.
func (b *Builder) streamTrack(tb *tables, w *mp4.Writer, dst io.Writer) ([]SampleCopy, error) {
	out := make([]SampleCopy, 0, tb.rng.Last-tb.rng.First+1)
	for i := tb.rng.First; i <= tb.rng.Last; i++ {
		s := tb.track.Samples[i]
		pos, err := w.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, err
		}
		tb.co64 = append(tb.co64, uint64(pos))
		if err := b.copySample(dst, s.Offset, s.Size); err != nil {
			return nil, err
		}
		out = append(out, SampleCopy{
			TrackID: tb.track.TrackID, OutputIndex: i - tb.rng.First,
			SourceIndex: i, SourceOffset: s.Offset, Size: s.Size,
		})
	}
	return out, nil
}

// copySample streams one sample with a bounded 64 KiB transfer buffer.
func (b *Builder) copySample(dst io.Writer, off int64, size uint32) error {
	if _, err := b.f.Seek(off, io.SeekStart); err != nil {
		return err
	}
	buf := make([]byte, 64*1024)
	remaining := int64(size)
	for remaining > 0 {
		n := int64(len(buf))
		if remaining < n {
			n = remaining
		}
		if _, err := io.ReadFull(b.f, buf[:n]); err != nil {
			return clock.NewError(clock.KindBoxTruncated, "mdat",
				"sample read past EOF").WithOffset(off)
		}
		if _, err := dst.Write(buf[:n]); err != nil {
			return err
		}
		remaining -= n
	}
	return nil
}
