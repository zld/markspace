// Package demux reads MP4/MOV containers and RTP dumps into the unified
// event model. Unsupported structures and truncated data are reported
// with track, raw byte offset and error category; nothing is dropped
// silently.
package demux

import (
	"io"

	"github.com/abema/go-mp4"

	"mediacore/internal/clock"
	"mediacore/internal/model"
)

// Options bounds resource use and selects the unified output timescale.
type Options struct {
	MaxBoxSize    uint64 // reject boxes larger than this
	MaxSamples    int    // per-track sample cap
	MaxSampleSize uint64 // per-sample payload cap
	Timescale     uint32 // unified output scale (default clock.UnifiedTimescale)
}

func (o Options) defaults() Options {
	if o.MaxBoxSize == 0 {
		o.MaxBoxSize = 256 << 20
	}
	if o.MaxSamples == 0 {
		o.MaxSamples = 1 << 20
	}
	if o.MaxSampleSize == 0 {
		o.MaxSampleSize = 64 << 20
	}
	if o.Timescale == 0 {
		o.Timescale = clock.UnifiedTimescale
	}
	return o
}

var containerTypes = map[mp4.BoxType]bool{
	mp4.BoxTypeMoov(): true, mp4.BoxTypeTrak(): true, mp4.BoxTypeMdia(): true,
	mp4.BoxTypeMinf(): true, mp4.BoxTypeStbl(): true, mp4.BoxTypeUdta(): true,
	mp4.BoxTypeEdts(): true, mp4.BoxTypeDinf(): true,
}

type sample struct {
	offset uint64
	size   uint32
	pts    int64 // track timescale
	dur    uint32
}

type trackBuilder struct {
	trackID   uint32
	timescale uint32
	handler   string
	entryType string
	stts      *mp4.Stts
	ctts      *mp4.Ctts
	stsz      *mp4.Stsz
	stsc      *mp4.Stsc
	chunks    []uint64
	mediaTime int64 // elst media time, track timescale
}

type mp4Demuxer struct {
	r      io.ReadSeeker
	size   int64
	opts   Options
	tracks []model.Track
	events []model.Event
	seq    int
}

func trackKind(handler, entry string) (model.TrackKind, bool) {
	switch handler {
	case "vide":
		return model.TrackVideo, true
	case "soun":
		return model.TrackAudio, true
	case "text", "sbtl", "subt":
		return model.TrackSubtitle, true
	case "meta":
		return model.TrackMeta, true
	}
	return "", false
}

func readAt(r io.ReadSeeker, buf []byte, off int64) (int, error) {
	if s, ok := r.(io.ReaderAt); ok {
		return s.ReadAt(buf, off)
	}
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	return io.ReadFull(r, buf)
}
