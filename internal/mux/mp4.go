// Package mux writes deterministic MP4 files with a rebuilt sample index.
// Audio is stored as 16-bit big-endian PCM in an ISO 23001-5 `ipcm` sample
// entry, so no external codec is required.
package mux

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"mediaforge/internal/mediaerr"
)

// Range is a half-open sample interval that could not be recovered.
type Range struct {
	Start int64 `json:"start_sample"`
	End   int64 `json:"end_sample"`
}

// Spec describes the audio to write. ChunkFrames lists the frame count of
// each chunk in playback order; frames are read sequentially from PCM.
type Spec struct {
	Path        string
	Rate        int
	Channels    int
	PCM         []int16 // interleaved, 16-bit
	ChunkFrames []int
	Missing     []Range
}

// Info is the result of writing a file.
type Info struct {
	SampleRate    int
	Channels      int
	Frames        int64
	DurationS     float64
	Chunks        int
	DataBytes     int64
	IndexComplete bool
	Missing       []Range
}

// Write emits an MP4 with ftyp, mdat and a trailing moov. The index is
// complete whenever every resumed interval was replaced with silence; the
// missing ranges are still reported so callers can disclose them.
func Write(spec Spec) (Info, error) {
	if spec.Rate <= 0 || (spec.Channels != 1 && spec.Channels != 2) {
		return Info{}, mediaerr.New(mediaerr.CodeParam,
			"invalid audio spec: rate=%d channels=%d", spec.Rate, spec.Channels)
	}
	frames := int64(len(spec.PCM) / spec.Channels)
	if frames == 0 {
		return Info{}, mediaerr.New(mediaerr.CodeParam, "no audio frames to mux")
	}
	chunkSum := int64(0)
	for _, n := range spec.ChunkFrames {
		if n <= 0 {
			return Info{}, mediaerr.New(mediaerr.CodeParam, "empty chunk in index")
		}
		chunkSum += int64(n)
	}
	if chunkSum != frames {
		return Info{}, mediaerr.New(mediaerr.CodeParam,
			"chunk frames %d do not match PCM frames %d", chunkSum, frames)
	}
	dataBytes := frames * int64(spec.Channels) * 2
	if dataBytes > 4<<30 {
		return Info{}, mediaerr.New(mediaerr.CodeParam, "output exceeds 4 GiB stco limit")
	}

	f, err := os.Create(spec.Path)
	if err != nil {
		return Info{}, mediaerr.New(mediaerr.CodeIO, "create %s: %v", spec.Path, err)
	}
	defer f.Close()
	w := mp4.NewWriter(f)
	if err := writeFtyp(w); err != nil {
		return Info{}, err
	}
	dataStart, err := writeMdat(w, spec.PCM, dataBytes)
	if err != nil {
		return Info{}, err
	}
	offsets := chunkOffsets(dataStart, spec.ChunkFrames, spec.Channels)
	info, err := writeMoov(w, spec, frames, dataBytes, offsets)
	if err != nil {
		return Info{}, err
	}
	if err := f.Sync(); err != nil {
		return Info{}, mediaerr.New(mediaerr.CodeIO, "sync %s: %v", spec.Path, err)
	}
	return info, nil
}

func writeFtyp(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return mediaerr.New(mediaerr.CodeIO, "ftyp: %v", err)
	}
	ftyp := &mp4.Ftyp{MajorBrand: mp4.BrandISOM(), MinorVersion: 0x200}
	for _, b := range [][4]byte{mp4.BrandISOM(), mp4.BrandISO2(), mp4.BrandISO4(), mp4.BrandMP41()} {
		ftyp.AddCompatibleBrand(b)
	}
	if _, err := mp4.Marshal(w, ftyp, mp4.Context{}); err != nil {
		return mediaerr.New(mediaerr.CodeIO, "ftyp payload: %v", err)
	}
	_, err := w.EndBox()
	return err
}

// writeMdat streams the PCM payload with a 32-bit size and returns the
// absolute offset of the first data byte.
func writeMdat(w *mp4.Writer, pcm []int16, dataBytes int64) (int64, error) {
	bi := &mp4.BoxInfo{
		Type:       mp4.BoxTypeMdat(),
		Size:       uint64(dataBytes) + mp4.SmallHeaderSize,
		HeaderSize: mp4.SmallHeaderSize,
	}
	if _, err := w.StartBox(bi); err != nil {
		return 0, mediaerr.New(mediaerr.CodeIO, "mdat: %v", err)
	}
	start, err := w.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, mediaerr.New(mediaerr.CodeIO, "mdat seek: %v", err)
	}
	buf := make([]byte, 8192)
	for i := 0; i < len(pcm); {
		n := 0
		for n+2 <= len(buf) && i < len(pcm) {
			binary.BigEndian.PutUint16(buf[n:], uint16(pcm[i]))
			n += 2
			i++
		}
		if _, err := w.Write(buf[:n]); err != nil {
			return 0, mediaerr.New(mediaerr.CodeIO, "mdat write: %v", err)
		}
	}
	if _, err := w.EndBox(); err != nil {
		return 0, mediaerr.New(mediaerr.CodeIO, "mdat close: %v", err)
	}
	return start, nil
}

func chunkOffsets(dataStart int64, chunks []int, channels int) []uint32 {
	out := make([]uint32, len(chunks))
	cur := dataStart
	for i, n := range chunks {
		out[i] = uint32(cur)
		cur += int64(n) * int64(channels) * 2
	}
	return out
}

func writeMoov(w *mp4.Writer, spec Spec, frames, dataBytes int64, offsets []uint32) (Info, error) {
	info := Info{
		SampleRate: spec.Rate, Channels: spec.Channels, Frames: frames,
		DurationS: float64(frames) / float64(spec.Rate), Chunks: len(spec.ChunkFrames),
		DataBytes: dataBytes, IndexComplete: len(spec.Missing) == 0, Missing: spec.Missing,
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return Info{}, mediaerr.New(mediaerr.CodeIO, "moov: %v", err)
	}
	if err := writeMvhd(w, spec.Rate, frames); err != nil {
		return Info{}, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return Info{}, err
	}
	if err := writeTkhd(w, spec.Rate, frames); err != nil {
		return Info{}, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return Info{}, err
	}
	if err := writeMdhd(w, spec.Rate, frames); err != nil {
		return Info{}, err
	}
	if err := writeHdlr(w); err != nil {
		return Info{}, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return Info{}, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeSmhd()}); err != nil {
		return Info{}, err
	}
	if _, err := mp4.Marshal(w, &mp4.Smhd{}, mp4.Context{}); err != nil {
		return Info{}, err
	}
	if _, err := w.EndBox(); err != nil {
		return Info{}, err
	}
	if err := writeDinf(w); err != nil {
		return Info{}, err
	}
	if err := writeStbl(w, spec, frames, offsets); err != nil {
		return Info{}, err
	}
	for i := 0; i < 2; i++ { // minf, mdia (writeStbl closes stbl)
		if _, err := w.EndBox(); err != nil {
			return Info{}, err
		}
	}
	if _, err := w.EndBox(); err != nil { // trak
		return Info{}, err
	}
	if _, err := w.EndBox(); err != nil { // moov
		return Info{}, err
	}
	return info, nil
}

func matrix() [9]int32 {
	return [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}
}

func writeMvhd(w *mp4.Writer, rate int, frames int64) error {
	bi := &mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}
	if _, err := w.StartBox(bi); err != nil {
		return err
	}
	_, err := mp4.Marshal(w, &mp4.Mvhd{
		Timescale: uint32(rate), DurationV0: uint32(frames),
		Rate: 0x10000, Volume: 0x100, Matrix: matrix(), NextTrackID: 2,
		PreDefined: [6]int32{0, 0, 0, 0, 0, 0},
	}, mp4.Context{})
	if err != nil {
		return mediaerr.New(mediaerr.CodeIO, "mvhd: %v", err)
	}
	_, err = w.EndBox()
	return err
}

func writeTkhd(w *mp4.Writer, rate int, frames int64) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTkhd(), Size: 8, HeaderSize: 8}); err != nil {
		return err
	}
	_, err := mp4.Marshal(w, &mp4.Tkhd{
		FullBox:    mp4.FullBox{Flags: [3]byte{0, 0, 3}},
		TrackID:    1,
		DurationV0: uint32(frames),
		Volume:     0x100,
		Matrix:     matrix(),
	}, mp4.Context{})
	if err != nil {
		return mediaerr.New(mediaerr.CodeIO, "tkhd: %v", err)
	}
	_, err = w.EndBox()
	return err
}

func writeMdhd(w *mp4.Writer, rate int, frames int64) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	_, err := mp4.Marshal(w, &mp4.Mdhd{
		Timescale: uint32(rate), DurationV0: uint32(frames),
		Language: [3]byte{'u', 'n', 'd'},
	}, mp4.Context{})
	if err != nil {
		return mediaerr.New(mediaerr.CodeIO, "mdhd: %v", err)
	}
	_, err = w.EndBox()
	return err
}

func writeHdlr(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}); err != nil {
		return err
	}
	_, err := mp4.Marshal(w, &mp4.Hdlr{HandlerType: [4]byte{'s', 'o', 'u', 'n'}, Name: "SoundHandler"}, mp4.Context{})
	if err != nil {
		return mediaerr.New(mediaerr.CodeIO, "hdlr: %v", err)
	}
	_, err = w.EndBox()
	return err
}

func writeDinf(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDref()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Dref{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeUrl()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Url{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 1}}}, mp4.Context{}); err != nil {
		return err
	}
	for i := 0; i < 3; i++ {
		if _, err := w.EndBox(); err != nil {
			return err
		}
	}
	return nil
}

func writeStbl(w *mp4.Writer, spec Spec, frames int64, offsets []uint32) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if err := writeStsd(w, spec.Rate, spec.Channels); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStts()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stts{
		EntryCount: 1, Entries: []mp4.SttsEntry{{SampleCount: uint32(frames), SampleDelta: 1}},
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if err := writeStsc(w, spec.ChunkFrames); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsz()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsz{
		SampleSize: uint32(spec.Channels * 2), SampleCount: uint32(frames),
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStco()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stco{EntryCount: uint32(len(offsets)), ChunkOffset: offsets}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_, err := w.EndBox() // stbl
	return err
}

func writeStsc(w *mp4.Writer, chunks []int) error {
	var entries []mp4.StscEntry
	for i, n := range chunks {
		if len(entries) == 0 || entries[len(entries)-1].SamplesPerChunk != uint32(n) {
			entries = append(entries, mp4.StscEntry{
				FirstChunk: uint32(i + 1), SamplesPerChunk: uint32(n), SampleDescriptionIndex: 1,
			})
		}
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsc()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsc{EntryCount: uint32(len(entries)), Entries: entries}, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeStsd(w *mp4.Writer, rate, channels int) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeIpcm()}); err != nil {
		return err
	}
	entry := &mp4.AudioSampleEntry{
		SampleEntry:  mp4.SampleEntry{AnyTypeBox: mp4.AnyTypeBox{Type: mp4.BoxTypeIpcm()}},
		ChannelCount: uint16(channels), SampleSize: 16,
		SampleRate: uint32(rate) << 16,
	}
	if _, err := mp4.Marshal(w, entry, mp4.Context{}); err != nil {
		return mediaerr.New(mediaerr.CodeIO, "ipcm entry: %v", err)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypePcmC()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.PcmC{FormatFlags: 0, PCMSampleSize: 16}, mp4.Context{}); err != nil {
		return err
	}
	for i := 0; i < 3; i++ { // pcmC, ipcm, stsd
		if _, err := w.EndBox(); err != nil {
			return err
		}
	}
	return nil
}

// Pitch returns a stable human-readable description used in logs.
func (i Info) Pitch() string {
	return fmt.Sprintf("%d Hz, %d ch, %.3fs", i.SampleRate, i.Channels, i.DurationS)
}
