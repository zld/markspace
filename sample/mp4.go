package sample

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

// MP4Options controls the generated MP4 fixtures.
type MP4Options struct {
	// VideoSamples and AudioSamples are the per-track sample counts.
	VideoSamples int
	AudioSamples int
	// TruncateBytes removes this many bytes from the end of the file.
	TruncateBytes int
	// IndexShiftWidens every chunk offset by this many bytes.
	IndexShiftWidens uint32
	// OmitIndex drops the moov box entirely.
	OmitIndex bool
}

// MP4Fixture is a generated MP4 on disk.
type MP4Fixture struct {
	// Name is the file name inside the sample directory.
	Name string
	// Path is the absolute path on disk.
	Path string
	// Size is the written byte count.
	Size int64
	// IntactSamples is the sample count the index should recover on a healthy
	// file.
	IntactSamples int
	// ExpectedRecovered is the number of samples that survive truncation or a
	// broken index, as computed by the generator's own layout knowledge.
	ExpectedRecovered int
	// FirstUnreadableHint is the generator's own truncation offset, or -1.
	FirstUnreadableHint int64
}

// box helpers shared with the container fixture tests.
func box(boxType string, body []byte) []byte {
	out := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(out, uint32(8+len(body)))
	copy(out[4:8], boxType)
	return append(out, body...)
}

func fullBox(boxType string, version uint8, flags uint32, body []byte) []byte {
	header := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return box(boxType, append(header, body...))
}

func be32(v uint32) []byte {
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, v)
	return out
}

func be64(v uint64) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint64(out, v)
	return out
}

func join(parts ...[]byte) []byte {
	var out []byte
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

type genTrack struct {
	handle     string
	id         uint32
	timescale  uint32
	sampleDur  uint32
	sampleSize uint32
	count      uint32
	chunk      uint32
	shift      uint32
	withSync   bool
}

func genFtyp() []byte {
	return box("ftyp", join([]byte("isom"), be32(0x200), []byte("isom"), []byte("iso2"), []byte("mp41")))
}

// genMoov builds moov for tracks whose payloads lie consecutively from
// mdatDataStart, in the order given.
func genMoov(tracks []genTrack, mdatDataStart int64) []byte {
	var duration uint64
	for _, track := range tracks {
		if d := uint64(track.sampleDur) * uint64(track.count); d*1000/uint64(track.timescale) > duration {
			duration = d * 1000 / uint64(track.timescale)
		}
	}
	mvhd := fullBox("mvhd", 0, 0, join(
		be32(0), be32(0), be32(1000), be32(uint32(duration)),
		be32(0x00010000), []byte{0x01, 0x00}, make([]byte, 10),
		be32(0x00010000), be32(0), be32(0),
		be32(0), be32(0x00010000), be32(0),
		be32(0), be32(0), be32(0x40000000),
		make([]byte, 24), be32(uint32(len(tracks))+1),
	))
	var traks [][]byte
	base := mdatDataStart
	for _, track := range tracks {
		traks = append(traks, genTrak(track, base))
		base += int64(track.count) * int64(track.sampleSize)
	}
	return box("moov", join(append([][]byte{mvhd}, traks...)...))
}

// genTrak builds one trak whose chunk offsets start at trackDataStart.
func genTrak(track genTrack, trackDataStart int64) []byte {
	duration := uint64(track.sampleDur) * uint64(track.count)
	tkhd := fullBox("tkhd", 0, 7, join(
		be32(0), be32(0), be32(track.id), be32(0), be32(uint32(duration)),
		make([]byte, 8), make([]byte, 8),
		be32(0x00010000), be32(0), be32(0),
		be32(0), be32(0x00010000), be32(0),
		be32(0), be32(0), be32(0x40000000),
		be32(640<<16), be32(360<<16),
	))
	mdhd := fullBox("mdhd", 0, 0, join(
		be32(0), be32(0), be32(track.timescale), be32(uint32(duration)),
		[]byte{0x55, 0xC4, 0}, be32(0),
	))
	hdlr := fullBox("hdlr", 0, 0, join(be32(0), []byte(track.handle), make([]byte, 12), []byte("mediarc\x00")))
	stts := fullBox("stts", 0, 0, join(be32(1), be32(track.count), be32(track.sampleDur)))
	stsz := fullBox("stsz", 0, 0, join(be32(track.sampleSize), be32(track.count)))
	chunks := (track.count + track.chunk - 1) / track.chunk
	var offsets [][]byte
	for i := uint32(0); i < chunks; i++ {
		offset := trackDataStart + int64(i)*int64(track.chunk)*int64(track.sampleSize) + int64(track.shift)
		offsets = append(offsets, be32(uint32(offset)))
	}
	stco := fullBox("stco", 0, 0, join(be32(chunks), join(offsets...)))
	stsc := fullBox("stsc", 0, 0, join(be32(1), be32(1), be32(track.chunk), be32(1)))
	parts := [][]byte{fullBox("stsd", 0, 0, be32(0)), stts, stsz, stsc, stco}
	if track.withSync {
		var numbers [][]byte
		for i := uint32(1); i <= track.count; i += 4 {
			numbers = append(numbers, be32(i))
		}
		parts = append(parts, fullBox("stss", 0, 0, join(be32(uint32(len(numbers))), join(numbers...))))
	}
	stbl := box("stbl", join(parts...))
	minf := box("minf", join(box("vmhd", join(be32(1), make([]byte, 8))), stbl))
	mdia := box("mdia", join(mdhd, hdlr, minf))
	return box("trak", join(tkhd, mdia))
}

// BuildMP4 writes an MP4 fixture into dir and reports its layout. The
// per-track layout is: video samples of 512 bytes in chunks of 5, audio samples
// of 256 bytes in chunks of 10.
func BuildMP4(dir string, name string, opts MP4Options) (MP4Fixture, error) {
	if opts.VideoSamples <= 0 {
		opts.VideoSamples = 20
	}
	if opts.AudioSamples <= 0 {
		opts.AudioSamples = 40
	}
	tracks := []genTrack{
		{handle: "vide", id: 1, timescale: 90000, sampleDur: 3000, sampleSize: 512, count: uint32(opts.VideoSamples), chunk: 5, shift: opts.IndexShiftWidens, withSync: true},
		{handle: "soun", id: 2, timescale: 48000, sampleDur: 1024, sampleSize: 256, count: uint32(opts.AudioSamples), chunk: 10, shift: opts.IndexShiftWidens},
	}
	payloadLen := int64(0)
	for _, track := range tracks {
		payloadLen += int64(track.count) * int64(track.sampleSize)
	}
	probe := genMoov(tracks, 0)
	mdatDataStart := int64(len(genFtyp())) + int64(len(probe)) + 8
	moov := genMoov(tracks, mdatDataStart)
	payload := make([]byte, payloadLen)
	// Fill with a deterministic pattern so a recovered byte range is stable.
	for i := range payload {
		payload[i] = byte((i*7 + 13) % 251)
	}
	mdatHeader := make([]byte, 8)
	binary.BigEndian.PutUint32(mdatHeader, uint32(8+len(payload)))
	copy(mdatHeader[4:], "mdat")

	var data []byte
	if opts.OmitIndex {
		data = join(genFtyp(), mdatHeader, payload)
	} else {
		data = join(genFtyp(), moov, mdatHeader, payload)
	}
	intact := 0
	for _, track := range tracks {
		intact += int(track.count)
	}
	fixture := MP4Fixture{
		Name:                name,
		Path:                filepath.Join(dir, name),
		IntactSamples:       intact,
		ExpectedRecovered:   intact,
		FirstUnreadableHint: -1,
	}
	if opts.TruncateBytes > 0 {
		if opts.TruncateBytes >= len(data) {
			return MP4Fixture{}, fmt.Errorf("truncation of %d bytes would remove the whole %d-byte file", opts.TruncateBytes, len(data))
		}
		// The scanner reports the header offset of the box whose declared size
		// runs past the new end of file, not the end itself.
		mdatBoxOffset := int64(len(genFtyp()))
		if !opts.OmitIndex {
			mdatBoxOffset += int64(len(moov))
		}
		data = data[:len(data)-opts.TruncateBytes]
		fixture.FirstUnreadableHint = mdatBoxOffset
		if fixture.FirstUnreadableHint >= int64(len(data)) {
			fixture.FirstUnreadableHint = int64(len(data))
		}
		fixture.ExpectedRecovered = expectedSurvivors(tracks, mdatDataStart, int64(len(data)))
	}
	if opts.OmitIndex {
		fixture.ExpectedRecovered = 0
	}
	fixture.Size = int64(len(data))
	if err := os.WriteFile(fixture.Path, data, 0o644); err != nil {
		return MP4Fixture{}, fmt.Errorf("write %s: %w", name, err)
	}
	return fixture, nil
}

// expectedSurvivors counts indexed samples whose byte extent stays in the file.
func expectedSurvivors(tracks []genTrack, mdatDataStart int64, size int64) int {
	survivors := 0
	base := mdatDataStart
	for _, track := range tracks {
		chunks := (track.count + track.chunk - 1) / track.chunk
		for chunk := uint32(0); chunk < chunks; chunk++ {
			offset := base + int64(chunk)*int64(track.chunk)*int64(track.sampleSize)
			per := track.chunk
			if chunk == chunks-1 {
				per = track.count - chunk*track.chunk
			}
			for sample := uint32(0); sample < per; sample++ {
				sampleStart := offset + int64(sample)*int64(track.sampleSize)
				if sampleStart+int64(track.sampleSize) <= size {
					survivors++
				}
			}
		}
		base += int64(track.count) * int64(track.sampleSize)
	}
	return survivors
}
