// Package mp4seg muxes aligned, time-normalized units into minimal MP4
// fragment files and validates their box structure with go-mp4. The muxer
// never transcodes: it copies the scanned unit bytes verbatim and records
// the integer tick timeline in the 90 kHz movie timescale.
package mp4seg

import (
	"io"

	"github.com/abema/go-mp4"

	"gamepack/internal/clock"
	perr "gamepack/internal/errors"
	"gamepack/internal/stream"
)

const subsystem = "mp4"

// Sample is one muxing input: the scanned unit plus its normalized tick.
type Sample struct {
	Unit       stream.Unit
	Tick       clock.Tick
	IsKey      bool
	Codec      stream.Codec
	ConfigData []byte // codec-specific config box payload, first sample only
}

// FragmentResult describes a written fragment.
type FragmentResult struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	SampleCount int    `json:"sample_count"`
	Duration    clock.Tick `json:"duration_ticks"`
	SHA256      string `json:"sha256"`
}

// matrix is the standard ISO visual matrix (0x00010000 identity, last 0x40000000).
var matrix = [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}

func brandFor(codec stream.Codec) ([4]byte, [4]byte) {
	switch codec {
	case stream.CodecH265:
		return [4]byte{'h', 'e', 'i', 'c'}, [4]byte{'h', 'v', 'c', '1'}
	case stream.CodecAV1:
		return [4]byte{'a', 'v', '0', '1'}, [4]byte{'a', 'v', '0', '1'}
	default:
		return [4]byte{'i', 's', 'o', 'm'}, [4]byte{'a', 'v', 'c', '1'}
	}
}

// WriteFragment serializes the samples into a self-contained MP4 at w. The
// output uses the shared 90 kHz timescale; sample durations are integer tick
// deltas so the media timeline matches the normalized ladder exactly.
func WriteFragment(w io.WriteSeeker, samples []Sample) error {
	if len(samples) == 0 {
		return perr.New(subsystem, perr.CodeStage, "cannot mux empty fragment")
	}
	codec := samples[0].Codec
	major, entry := brandFor(codec)

	mw := mp4.NewWriter(w)

	// ftyp
	if _, err := start(mw, mp4.BoxTypeFtyp()); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Ftyp{
		MajorBrand:   major,
		MinorVersion: 0,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: major}, {CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
		},
	}, mp4.Context{}); err != nil {
		return wrapMarshal(err)
	}
	if err := end(mw); err != nil {
		return err
	}

	// moov > mvhd, trak
	if _, err := start(mw, mp4.BoxTypeMoov()); err != nil {
		return err
	}
	duration := samples[len(samples)-1].Tick - samples[0].Tick
	if err := writeMvhd(mw, duration); err != nil {
		return err
	}
	if _, err := start(mw, mp4.BoxTypeTrak()); err != nil {
		return err
	}
	if err := writeTkhd(mw, duration); err != nil {
		return err
	}
	if err := writeMdia(mw, samples, entry, codec, duration); err != nil {
		return err
	}
	if err := end(mw); err != nil { // trak
		return err
	}
	if err := end(mw); err != nil { // moov
		return err
	}

	// mdat with verbatim unit payloads.
	if _, err := start(mw, mp4.BoxTypeMdat()); err != nil {
		return err
	}
	for _, s := range samples {
		if _, err := mw.Write(s.Unit.Data); err != nil {
			return perr.New(subsystem, perr.CodeStage, "mdat write: %v", err)
		}
	}
	if err := end(mw); err != nil {
		return err
	}
	return nil
}

func writeMvhd(mw *mp4.Writer, duration clock.Tick) error {
	if _, err := start(mw, mp4.BoxTypeMvhd()); err != nil {
		return err
	}
	_, err := mp4.Marshal(mw, &mp4.Mvhd{
		Timescale:      uint32(clock.TicksPerSecond),
		DurationV0:     uint32(duration),
		Rate:           0x00010000,
		Volume:         0x0100,
		Matrix:         matrix,
		NextTrackID:    2,
	}, mp4.Context{})
	if err != nil {
		return wrapMarshal(err)
	}
	return end(mw)
}

func writeTkhd(mw *mp4.Writer, duration clock.Tick) error {
	if _, err := start(mw, mp4.BoxTypeTkhd()); err != nil {
		return err
	}
	_, err := mp4.Marshal(mw, &mp4.Tkhd{
		TrackID:     1,
		DurationV0:  uint32(duration),
		Matrix:      matrix,
		Width:       320 << 16,
		Height:      240 << 16,
	}, mp4.Context{})
	if err != nil {
		return wrapMarshal(err)
	}
	return end(mw)
}

func writeMdia(mw *mp4.Writer, samples []Sample, entry [4]byte,
	codec stream.Codec, duration clock.Tick) error {
	if _, err := start(mw, mp4.BoxTypeMdia()); err != nil {
		return err
	}
	if _, err := start(mw, mp4.BoxTypeMdhd()); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Mdhd{
		Timescale:  uint32(clock.TicksPerSecond),
		DurationV0: uint32(duration),
		Language:   [3]byte{'u', 'n', 'd'},
	}, mp4.Context{}); err != nil {
		return wrapMarshal(err)
	}
	if err := end(mw); err != nil {
		return err
	}
	if _, err := start(mw, mp4.BoxTypeHdlr()); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Hdlr{
		HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "vide",
	}, mp4.Context{}); err != nil {
		return wrapMarshal(err)
	}
	if err := end(mw); err != nil {
		return err
	}
	if _, err := start(mw, mp4.BoxTypeMinf()); err != nil {
		return err
	}
	if err := writeVmhd(mw); err != nil {
		return err
	}
	if err := writeDinf(mw); err != nil {
		return err
	}
	if err := writeStbl(mw, samples, entry, codec); err != nil {
		return err
	}
	if err := end(mw); err != nil { // minf
		return err
	}
	return end(mw) // mdia
}

func writeVmhd(mw *mp4.Writer) error {
	if _, err := start(mw, mp4.BoxTypeVmhd()); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Vmhd{}, mp4.Context{}); err != nil {
		return wrapMarshal(err)
	}
	return end(mw)
}

func writeDinf(mw *mp4.Writer) error {
	if _, err := start(mw, mp4.BoxTypeDinf()); err != nil {
		return err
	}
	if _, err := start(mw, mp4.BoxTypeDref()); err != nil {
		return err
	}
	dref := &mp4.Dref{EntryCount: 1}
	if _, err := mp4.Marshal(mw, dref, mp4.Context{}); err != nil {
		return wrapMarshal(err)
	}
	if _, err := start(mw, mp4.BoxTypeUrl()); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Url{}, mp4.Context{}); err != nil {
		return wrapMarshal(err)
	}
	if err := end(mw); err != nil { // url
		return err
	}
	if err := end(mw); err != nil { // dref
		return err
	}
	return end(mw) // dinf
}

func start(mw *mp4.Writer, t mp4.BoxType) (*mp4.BoxInfo, error) {
	bi, err := mw.StartBox(&mp4.BoxInfo{Type: t})
	if err != nil {
		return nil, perr.New(subsystem, perr.CodeStage, "start box %s: %v", t, err)
	}
	return bi, nil
}

func end(mw *mp4.Writer) error {
	if _, err := mw.EndBox(); err != nil {
		return perr.New(subsystem, perr.CodeStage, "end box: %v", err)
	}
	return nil
}

func wrapMarshal(err error) error {
	return perr.New(subsystem, perr.CodeStage, "mp4 marshal: %v", err).Wrap(err)
}

// renderToBytes is a test/diagnostic helper returning the full fragment.
func renderToBytes(samples []Sample) ([]byte, error) {
	seeker := &bufSeeker{}
	if err := WriteFragment(seeker, samples); err != nil {
		return nil, err
	}
	return seeker.Bytes(), nil
}
