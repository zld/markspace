package pipeline

import (
	"io"
	"math"
	"os"
	"path/filepath"

	mp4 "github.com/abema/go-mp4"

	"mediacheck/ebml"
	"mediacheck/timeline"
)

// trackSample is one contiguous mdat entry with its size and duration.
type trackSample struct {
	payload  []byte
	duration uint32
}

// remuxAndPublish writes a minimal valid MP4 to a temp file, then atomically
// renames it into place and only then writes the completion marker. An
// interruption leaves the previous output untouched and no marker.
func remuxAndPublish(finalPath string, scan *ebml.ScanResult, video, audioTrack *ebml.TrackSpec) error {
	videoSamples, audioSamples := collectSamples(scan, video, audioTrack)
	if len(videoSamples) == 0 || len(audioSamples) == 0 {
		return timeline.New(timeline.CatConfig, "cannot remux empty track")
	}

	dir := filepath.Dir(finalPath)
	tmp, err := os.CreateTemp(dir, ".mediacheck-*.mp4.tmp")
	if err != nil {
		return timeline.New(timeline.CatIO, "cannot create temp output").Wrapf(err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if err := writeMP4(tmp, video, audioTrack, videoSamples, audioSamples); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return timeline.New(timeline.CatIO, "output fsync failed").Wrapf(err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return timeline.New(timeline.CatIO, "output close failed").Wrapf(err)
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		cleanup()
		return timeline.New(timeline.CatIO, "output rename failed").Wrapf(err)
	}
	marker := finalPath + ".complete"
	if err := os.WriteFile(marker, []byte("ok\n"), 0o644); err != nil {
		return timeline.New(timeline.CatIO, "completion marker failed").Wrapf(err)
	}
	return nil
}

func collectSamples(scan *ebml.ScanResult, video, audioTrack *ebml.TrackSpec) (v, a []trackSample) {
	for _, blk := range scan.Blocks {
		s := trackSample{payload: blk.Payload}
		switch blk.TrackNumber {
		case video.Number:
			s.duration = uint32(math.Round(float64(24000) / 24)) // movie timescale 24k
			v = append(v, s)
		case audioTrack.Number:
			s.duration = uint32(24000 / 24)
			a = append(a, s)
		}
	}
	return v, a
}

func writeMP4(w ioWriteSeeker, video, audioTrack *ebml.TrackSpec, vs, as []trackSample) error {
	bw := &boxWriter{w: mp4.NewWriter(w)}
	if err := bw.writeFtyp(); err != nil {
		return err
	}
	// mdat first so moov stco offsets can point at real payload positions.
	videoPayloads := make([][]byte, len(vs))
	audioPayloads := make([][]byte, len(as))
	for i, s := range vs {
		videoPayloads[i] = s.payload
	}
	for i, s := range as {
		audioPayloads[i] = s.payload
	}
	mdatOff, err := bw.writeMdat(videoPayloads, audioPayloads)
	if err != nil {
		return err
	}
	return bw.writeMoov(video, audioTrack, vs, as, mdatOff)
}

// writeMoov builds the movie box with one trak per stream and explicit sample
// tables (stts/stsc/stsz/stco) so the file is independently parseable.
func (b *boxWriter) writeMoov(video, audioTrack *ebml.TrackSpec, vs, as []trackSample, mdatOff uint64) error {
	if _, err := b.start("moov"); err != nil {
		return err
	}
	if err := b.writeMvhd(vs, as); err != nil {
		return err
	}
	if err := b.writeTrak(1, video, vs, mdatOff, true); err != nil {
		return err
	}
	videoBytes := sampleByteTotal(vs)
	if err := b.writeTrak(2, audioTrack, as, mdatOff+videoBytes, false); err != nil {
		return err
	}
	return b.end()
}

func sampleByteTotal(s []trackSample) uint64 {
	var n uint64
	for _, x := range s {
		n += uint64(len(x.payload))
	}
	return n
}

func (b *boxWriter) writeMvhd(vs, as []trackSample) error {
	if _, err := b.start("mvhd"); err != nil {
		return err
	}
	var duration uint32
	for _, s := range vs {
		duration += s.duration
	}
	payload := fullBox(0, 0)
	putU32b(&payload, 0) // creation
	putU32b(&payload, 0) // modification
	putU32b(&payload, 24000)
	putU32b(&payload, duration)
	payload = append(payload, putU32(0x00010000)...) // rate 1.0
	payload = append(payload, 0x01, 0x00)            // volume
	payload = append(payload, make([]byte, 10)...)   // reserved
	payload = append(payload, identityMatrix()...)
	payload = append(payload, make([]byte, 24)...) // pre-defined
	putU32b(&payload, 3)                           // next track ID
	if err := b.raw(payload); err != nil {
		return err
	}
	return b.end()
}

// writeTrak emits one track: tkhd/mdhd/hdlr/minf/stbl with sample tables.
func (b *boxWriter) writeTrak(trackID uint32, spec *ebml.TrackSpec, samples []trackSample,
	chunkStart uint64, isVideo bool) error {
	if _, err := b.start("trak"); err != nil {
		return err
	}
	if err := b.writeTkhd(trackID, spec, samples, isVideo); err != nil {
		return err
	}
	if _, err := b.start("mdia"); err != nil {
		return err
	}
	if err := b.writeMdhd(trackID, spec, samples, isVideo); err != nil {
		return err
	}
	if err := b.writeHdlr(isVideo); err != nil {
		return err
	}
	if err := b.writeMinf(spec, samples, chunkStart, isVideo); err != nil {
		return err
	}
	if err := b.end(); err != nil { // mdia
		return err
	}
	return b.end() // trak
}

func (b *boxWriter) writeTkhd(trackID uint32, spec *ebml.TrackSpec, samples []trackSample, isVideo bool) error {
	if _, err := b.start("tkhd"); err != nil {
		return err
	}
	var duration uint32
	for _, s := range samples {
		duration += s.duration
	}
	payload := fullBox(0, 0x03) // enabled+in movie
	putU32b(&payload, 0)
	putU32b(&payload, 0)
	putU32b(&payload, trackID)
	putU32b(&payload, 0)
	putU32b(&payload, duration)
	payload = append(payload, make([]byte, 8)...) // reserved
	payload = append(payload, 0, 0)               // layer
	payload = append(payload, 0, 0)               // alt group
	if isVideo {
		payload = append(payload, 0, 0) // volume
	} else {
		payload = append(payload, 0x01, 0x00)
	}
	payload = append(payload, 0, 0) // reserved
	payload = append(payload, identityMatrix()...)
	if isVideo {
		putU32b(&payload, spec.Width<<16)
		putU32b(&payload, spec.Height<<16)
	} else {
		payload = append(payload, make([]byte, 8)...)
	}
	if err := b.raw(payload); err != nil {
		return err
	}
	return b.end()
}

func (b *boxWriter) writeMdhd(trackID uint32, spec *ebml.TrackSpec, samples []trackSample, isVideo bool) error {
	if _, err := b.start("mdhd"); err != nil {
		return err
	}
	var timescale uint32 = 24000
	var duration uint32
	for _, s := range samples {
		duration += s.duration
	}
	_ = trackID
	payload := fullBox(0, 0)
	putU32b(&payload, 0)
	putU32b(&payload, 0)
	putU32b(&payload, timescale)
	putU32b(&payload, duration)
	payload = append(payload, 0x55, 0xC4) // language 'und'
	payload = append(payload, 0, 0)       // quality
	if err := b.raw(payload); err != nil {
		return err
	}
	return b.end()
}

func (b *boxWriter) writeHdlr(isVideo bool) error {
	if _, err := b.start("hdlr"); err != nil {
		return err
	}
	handler := "soun"
	if isVideo {
		handler = "vide"
	}
	payload := fullBox(0, 0)
	putU32b(&payload, 0) // pre-defined
	payload = append(payload, []byte(handler)...)
	payload = append(payload, make([]byte, 12)...)
	payload = append(payload, []byte(handler+"\x00")...)
	if err := b.raw(payload); err != nil {
		return err
	}
	return b.end()
}

type ioWriteSeeker interface {
	io.Writer
	io.Seeker
}
