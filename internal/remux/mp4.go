// Package remux writes the audited H.264/AAC fragment set back out as a
// minimal, probeable MP4 using go-mp4, then reads the file back to verify it
// before publishing. Output goes to a temporary file and is atomically
// renamed only after verification, so a failed run never leaves a manifest or
// media file that downstream tooling could mistake for a good deliverable.
package remux

import (
	"io"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"

	"mediakernel/internal/clock"
	"mediakernel/internal/mediaerr"
)

// Track describes one remuxed track in master-clock terms.
type Track struct {
	ID           uint32
	Kind         string // video | audio
	Codec        string // avc1 / mp4a
	Timescale    uint32
	SampleCount  uint32
	SampleDelta  uint32 // constant per-sample delta in the track timescale
	Width        uint16
	Height       uint16
	ChannelCount uint16
	SampleRate   uint32
	Payload      []byte // deterministic synthetic sample bytes
}

// Summary is the verification ledger for one remuxed file.
type Summary struct {
	Path         string `json:"path"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	Tracks       int    `json:"tracks"`
	VideoSamples uint32 `json:"video_samples"`
	AudioSamples uint32 `json:"audio_samples"`
	DurationTick uint64 `json:"duration_tick"`
	Verified     bool   `json:"verified"`
}

// Input bundles the tracks and the expected master-clock duration.
type Input struct {
	Tracks       []Track
	DurationTick uint64
}

func (in Input) validate() error {
	if len(in.Tracks) == 0 {
		return mediaerr.New(mediaerr.CatRemux, "no_track", "remux requires at least one track")
	}
	if len(in.Tracks) > mediaerr.MaxStreams {
		return mediaerr.New(mediaerr.CatValidation, "track_cap", "too many tracks")
	}
	if in.DurationTick == 0 || in.DurationTick > mediaerr.MaxDurationTick {
		return mediaerr.New(mediaerr.CatValidation, "duration", "duration out of allowed range")
	}
	hasVideo := false
	for _, tr := range in.Tracks {
		if tr.Timescale == 0 {
			return mediaerr.New(mediaerr.CatTimebase, "zero_timescale", "track timescale must be positive")
		}
		if tr.SampleCount == 0 || tr.SampleDelta == 0 {
			return mediaerr.New(mediaerr.CatRemux, "empty_track", "track has no samples")
		}
		switch tr.Kind {
		case "video":
			hasVideo = true
			if tr.Codec != "avc1" {
				return mediaerr.New(mediaerr.CatRemux, "codec", "only avc1 video is supported")
			}
		case "audio":
			if tr.Codec != "mp4a" {
				return mediaerr.New(mediaerr.CatRemux, "codec", "only mp4a audio is supported")
			}
		default:
			return mediaerr.New(mediaerr.CatRemux, "kind", "unknown track kind "+tr.Kind)
		}
	}
	if !hasVideo {
		return mediaerr.New(mediaerr.CatRemux, "no_video", "a video track is required in v1")
	}
	return nil
}

// WriteAtomically writes to path + ".tmp", verifies the result and only then
// renames it into place. The destination is never left half-written.
func WriteAtomically(path string, in Input) (*Summary, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, mediaerr.New(mediaerr.CatRemux, "mkdir", err.Error()).Wrap(err)
	}
	tmp, err := os.CreateTemp(dir, ".remux-*.tmp")
	if err != nil {
		return nil, mediaerr.New(mediaerr.CatRemux, "tmp", err.Error()).Wrap(err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if err := writeMP4(tmp, in); err != nil {
		_ = tmp.Close()
		cleanup()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return nil, mediaerr.New(mediaerr.CatRemux, "close", err.Error()).Wrap(err)
	}
	sum, err := verifyAndHash(tmpName, in)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return nil, mediaerr.New(mediaerr.CatRemux, "rename", err.Error()).Wrap(err)
	}
	sum.Path = path
	return sum, nil
}

// writeMP4 emits ftyp + moov(trak...) + mdat through the go-mp4 Writer.
func writeMP4(w io.WriteSeeker, in Input) error {
	mw := mp4.NewWriter(w)
	if err := writeFtyp(mw); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return wrapMP4(err)
	}
	if err := writeMvhd(mw, in); err != nil {
		return err
	}
	for _, tr := range in.Tracks {
		if err := writeTrak(mw, tr); err != nil {
			return err
		}
	}
	if _, err := mw.EndBox(); err != nil { // moov
		return wrapMP4(err)
	}
	if err := writeMdat(mw, in); err != nil {
		return err
	}
	return nil
}

func writeFtyp(mw *mp4.Writer) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return wrapMP4(err)
	}
	ftyp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 512,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'i', 's', 'o', '2'}},
			{CompatibleBrand: [4]byte{'a', 'v', 'c', '1'}},
		},
	}
	if _, err := mp4.Marshal(mw, ftyp, mp4.Context{}); err != nil {
		return wrapMP4(err)
	}
	_, err := mw.EndBox()
	return wrapMP4(err)
}

func writeMvhd(mw *mp4.Writer, in Input) error {
	if err := clock.CheckI64(in.DurationTick); err != nil {
		return mediaerr.New(mediaerr.CatTimebase, "duration_range", err.Error())
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return wrapMP4(err)
	}
	mvhd := &mp4.Mvhd{
		FullBox:     mp4.FullBox{},
		Timescale:   uint32(clock.MasterHZ),
		DurationV0:  uint32(in.DurationTick),
		NextTrackID: uint32(len(in.Tracks)) + 1,
	}
	if _, err := mp4.Marshal(mw, mvhd, mp4.Context{}); err != nil {
		return wrapMP4(err)
	}
	_, err := mw.EndBox()
	return wrapMP4(err)
}

func writeTrak(mw *mp4.Writer, tr Track) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return wrapMP4(err)
	}
	// tkhd
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTkhd()}); err != nil {
		return wrapMP4(err)
	}
	tkhd := &mp4.Tkhd{
		FullBox:    mp4.FullBox{},
		TrackID:    tr.ID,
		DurationV0: trackDuration(tr),
		Width:      uint32(tr.Width) << 16,
		Height:     uint32(tr.Height) << 16,
	}
	if tr.Kind == "audio" {
		tkhd.Volume = 0x0100
	}
	if _, err := mp4.Marshal(mw, tkhd, mp4.Context{}); err != nil {
		return wrapMP4(err)
	}
	if _, err := mw.EndBox(); err != nil {
		return wrapMP4(err)
	}
	if err := writeMdia(mw, tr); err != nil {
		return err
	}
	_, err := mw.EndBox() // trak
	return wrapMP4(err)
}

func writeMdia(mw *mp4.Writer, tr Track) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return wrapMP4(err)
	}
	// mdhd
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return wrapMP4(err)
	}
	mdhd := &mp4.Mdhd{
		FullBox:    mp4.FullBox{},
		Timescale:  tr.Timescale,
		DurationV0: trackDuration(tr),
	}
	if _, err := mp4.Marshal(mw, mdhd, mp4.Context{}); err != nil {
		return wrapMP4(err)
	}
	if _, err := mw.EndBox(); err != nil {
		return wrapMP4(err)
	}
	// hdlr
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}); err != nil {
		return wrapMP4(err)
	}
	handler := "vide"
	if tr.Kind == "audio" {
		handler = "soun"
	}
	hdlr := &mp4.Hdlr{HandlerType: strTo4(handler), Name: "mediakernel"}
	if _, err := mp4.Marshal(mw, hdlr, mp4.Context{}); err != nil {
		return wrapMP4(err)
	}
	if _, err := mw.EndBox(); err != nil {
		return wrapMP4(err)
	}
	if err := writeMinf(mw, tr); err != nil {
		return err
	}
	_, err := mw.EndBox() // mdia
	return wrapMP4(err)
}
