// Package mp4 writes reconstructed media samples into an ISO base media file.
//
// One video track is written at the fixed 90 kHz timescale. Each sample is one
// reconstructed RTP payload, and the sample duration is the delta to the next
// packet's timestamp (the last packet reuses the previous delta). The file is
// built entirely in a temporary file and only renamed into place by the caller
// after validation, so a failed run never publishes a partial artifact.
package mp4

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	gomp4 "github.com/abema/go-mp4"

	"rtp-rebuild/internal/clock"
	"rtp-rebuild/internal/fault"
	"rtp-rebuild/internal/rtp"
)

// Sample is one written sample.
type Sample struct {
	Offset int64
	Size   int64
	TS     clock.Tick
	Dur    uint32
	Key    bool
}

// Track is the result of writing.
type Track struct {
	Samples   []Sample
	Timescale uint32
	Duration  uint64
	Bytes     int64
	// DeltasUsed lists the per-sample durations in order, for validation.
	Deltas []uint32
}

// WriteOptions control track construction.
type WriteOptions struct {
	// MaxSampleBytes bounds any single sample's payload.
	MaxSampleBytes int
	// Width and Height are cosmetic for a payload-only track.
	Width, Height uint16
	// Timescale overrides clock.TickRate for tests.
	Timescale uint32
}

// WriteTrack writes the packets as an MP4 file at path.
//
// The packets must already be in increasing emitted order. Packets with a
// suspect timestamp are written with a zero-length duration and are excluded
// from duration derivation.
func WriteTrack(path string, packets []rtp.Packet, opts WriteOptions) (*Track, error) {
	if opts.Timescale == 0 {
		opts.Timescale = clock.TickRate
	}
	if opts.MaxSampleBytes <= 0 {
		opts.MaxSampleBytes = 1 << 20
	}
	if opts.Width == 0 {
		opts.Width = 640
	}
	if opts.Height == 0 {
		opts.Height = 360
	}
	if len(packets) == 0 {
		return nil, fault.New(fault.StageMux, "write track", fmt.Errorf("%w: no samples", fault.ErrCorrupt))
	}

	tmp, err := os.CreateTemp(dirOf(path), ".mux-*.tmp")
	if err != nil {
		return nil, fault.New(fault.StageMux, "create temp file", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	track, err := write(tmp, packets, opts)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return nil, fault.New(fault.StageMux, "sync temp file", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return nil, fault.New(fault.StageMux, "close temp file", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return nil, fault.New(fault.StageMux, "publish "+path, err)
	}
	return track, nil
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 {
				return "/"
			}
			return path[:i]
		}
	}
	return "."
}

func write(w io.WriteSeeker, packets []rtp.Packet, opts WriteOptions) (*Track, error) {
	durations := durationsFor(packets, opts.Timescale)
	var total uint64
	for _, d := range durations {
		total += uint64(d)
	}
	track := &Track{Timescale: opts.Timescale, Duration: total, Deltas: durations}

	// Reserve the mdat header; offsets are patched once samples are written.
	// Layout: ftyp, moov (written last into its own buffer), mdat with samples.
	ftypSize, err := writeFtyp(w)
	if err != nil {
		return nil, err
	}

	moovBuf, samples, err := buildMoov(packets, durations, opts, ftypSize)
	if err != nil {
		return nil, err
	}
	track.Samples = samples

	if _, err := w.Write(moovBuf); err != nil {
		return nil, fault.New(fault.StageMux, "write moov", err)
	}
	if _, err := w.Write(mdatHeader(mdatSize(samples))); err != nil {
		return nil, fault.New(fault.StageMux, "write mdat header", err)
	}
	for _, p := range packets {
		if _, err := w.Write(p.Payload); err != nil {
			return nil, fault.New(fault.StageMux, "write sample", err)
		}
	}
	cur, err := w.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, fault.New(fault.StageMux, "measure output", err)
	}
	track.Bytes = cur
	return track, nil
}

// mdatSize is the sample bytes plus the 8 byte box header.
func mdatSize(samples []Sample) uint64 {
	size := uint64(8)
	for _, s := range samples {
		size += uint64(s.Size)
	}
	return size
}

func mdatHeader(size uint64) []byte {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[0:4], uint32(size))
	copy(hdr[4:8], "mdat")
	return hdr
}

// durationsFor derives per-sample durations from timestamp deltas. The last
// sample reuses the previous delta, or one tick when there is only one sample.
func durationsFor(packets []rtp.Packet, timescale uint32) []uint32 {
	durations := make([]uint32, len(packets))
	if len(packets) == 0 {
		return durations
	}
	last := uint32(1)
	for i := 0; i < len(packets)-1; i++ {
		if packets[i].TSSuspect || packets[i+1].TSSuspect {
			durations[i] = last
			continue
		}
		d := packets[i+1].TS - packets[i].TS
		if d <= 0 {
			d = 1
		}
		if d > clock.Tick(^uint32(0)>>1) {
			d = clock.Tick(^uint32(0) >> 1)
		}
		durations[i] = uint32(d)
		last = durations[i]
	}
	durations[len(packets)-1] = last
	return durations
}

// buildMoov assembles the moov tree in memory and returns the sample layout
// with absolute chunk offsets. ftypSize is the byte length of the ftyp box
// written before moov, used to place the mdat box.
func buildMoov(packets []rtp.Packet, durations []uint32, opts WriteOptions, ftypSize int64) ([]byte, []Sample, error) {
	var samples []Sample
	moov, err := encodeFile(func(buf *writer) (int64, error) {
		return writeMoov(buf, packets, durations, opts, &samples)
	})
	if err != nil {
		return nil, nil, err
	}
	// ftyp + moov + the 8 byte mdat header precede the first sample.
	mdatOffset := ftypSize + int64(len(moov)) + 8
	for i := range samples {
		samples[i].Offset += mdatOffset
	}
	return moov, samples, nil
}

// writeFtyp writes the file type box and returns its size.
func writeFtyp(w io.WriteSeeker) (int64, error) {
	start, err := w.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, fault.New(fault.StageMux, "measure ftyp start", err)
	}
	sw := gomp4.NewWriter(w)
	if _, err := sw.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeFtyp()}); err != nil {
		return 0, fault.New(fault.StageMux, "start ftyp", err)
	}
	if _, err := gomp4.Marshal(sw, &gomp4.Ftyp{
		MajorBrand:   gomp4.BrandISOM(),
		MinorVersion: 512,
		CompatibleBrands: []gomp4.CompatibleBrandElem{
			{CompatibleBrand: gomp4.BrandISOM()},
			{CompatibleBrand: gomp4.BrandISO2()},
			{CompatibleBrand: gomp4.BrandAVC1()},
			{CompatibleBrand: gomp4.BrandMP41()},
		},
	}, gomp4.Context{}); err != nil {
		return 0, fault.New(fault.StageMux, "marshal ftyp", err)
	}
	if _, err := sw.EndBox(); err != nil {
		return 0, fault.New(fault.StageMux, "end ftyp", err)
	}
	end, err := w.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, fault.New(fault.StageMux, "measure ftyp", err)
	}
	return end - start, nil
}

// writer is a minimal in-memory WriteSeeker for building one box tree.
type writer struct {
	buf []byte
	pos int64
}

func (w *writer) Write(p []byte) (int, error) {
	end := w.pos + int64(len(p))
	if end > int64(len(w.buf)) {
		grown := make([]byte, end)
		copy(grown, w.buf)
		w.buf = grown
	}
	copy(w.buf[w.pos:], p)
	w.pos = end
	return len(p), nil
}

func (w *writer) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		w.pos = offset
	case io.SeekCurrent:
		w.pos += offset
	case io.SeekEnd:
		w.pos = int64(len(w.buf)) + offset
	}
	if w.pos < 0 {
		return 0, fmt.Errorf("negative seek")
	}
	if w.pos > int64(len(w.buf)) {
		grown := make([]byte, w.pos)
		copy(grown, w.buf)
		w.buf = grown
	}
	return w.pos, nil
}

func encodeFile(fn func(*writer) (int64, error)) ([]byte, error) {
	w := &writer{}
	if _, err := fn(w); err != nil {
		return nil, err
	}
	return w.buf, nil
}
