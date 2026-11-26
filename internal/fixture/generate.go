// Package fixture generates the deterministic demo corpus: two overlapping
// stream versions (one with a 32-bit timestamp wraparound, one with a source
// switch and a conflicting sample), a stream truncated at byte 41 and a
// misaligned index. Everything is derived from the seed, so runs repeat.
package fixture

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"os"

	"mediarc/internal/archive"
	"mediarc/internal/index"
	"mediarc/internal/model"

	"github.com/pion/rtp"
)

const (
	Timescale = 90000 // ticks per second
	Step      = 3000  // ticks per sample (30 fps)
	AUSize    = 36    // bytes per access unit
	Positions = 6     // samples per version
	// TruncSize is the exact byte length of the truncated stream.
	TruncSize = 41
)

// payload derives 25 deterministic content bytes for one sample position.
func payload(seed int64, pos int) []byte {
	var out []byte
	for ctr := 0; len(out) < 25; ctr++ {
		sum := sha1.Sum([]byte(fmt.Sprintf("%d:%d:%d", seed, pos, ctr)))
		out = append(out, sum[:]...)
	}
	return out[:25]
}

// au builds AUD + one VCL NAL, exactly AUSize bytes.
func au(idr bool, pl []byte) []byte {
	typ := byte(0x01)
	if idr {
		typ = 0x65
	}
	out := []byte{0, 0, 0, 1, 0x09, 0x10, 0, 0, 0, 1, typ}
	return append(out, pl...)
}

var eos = []byte{0, 0, 0, 1, 0x0B}

func rtpHdr(seq uint16, ts uint32) []byte {
	pkt := rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 0x4d5243},
		Payload: []byte{0},
	}
	b, err := pkt.Marshal()
	if err != nil {
		panic(err)
	}
	return b
}

// Generate writes the corpus into dir, which must be inside the run's
// output directory. Files: va/vb streams and indexes, trunc.h264 (exactly
// TruncSize bytes) and bad.index.json (misaligned + out-of-range records).
func Generate(dir string, seed int64) *model.Error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return &model.Error{Code: model.ErrInput, Msg: err.Error()}
	}
	write := func(name string, data []byte) *model.Error {
		p, merr := archive.SafeJoin(dir, name)
		if merr != nil {
			return merr
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return &model.Error{Code: model.ErrInput, Msg: err.Error()}
		}
		return nil
	}
	writeJSON := func(name string, f index.File) *model.Error {
		data, err := json.MarshalIndent(f, "", "  ")
		if err != nil {
			return &model.Error{Code: model.ErrInput, Msg: err.Error()}
		}
		return write(name, data)
	}

	base := uint32(1<<32 - Step) // first RTP timestamp; wraps at sample 1

	// Version A: all 6 positions, RTP clock wraps around at sample 1.
	var vaStream []byte
	vaIdx := index.File{Stream: "va.h264", Version: "va"}
	for i := 0; i < Positions; i++ {
		off := int64(len(vaStream))
		vaStream = append(vaStream, au(i == 0, payload(seed, i))...)
		vaIdx.Records = append(vaIdx.Records, index.Record{
			Codec: "h264", Track: 1, Offset: off, Length: AUSize,
			PTS: int64(i) * Step, Timescale: Timescale,
			RTP: rtpHdr(uint16(i), base+uint32(i*Step)),
		})
	}
	vaStream = append(vaStream, eos...)

	// Version B: positions 0,1,2,3,5 (position 4 missing); position 2 has
	// conflicting content; the RTP clock switches source at position 3.
	var vbStream []byte
	vbIdx := index.File{Stream: "vb.h264", Version: "vb"}
	for j, pos := range []int{0, 1, 2, 3, 5} {
		off := int64(len(vbStream))
		pl := payload(seed, pos)
		if pos == 2 {
			pl = payload(seed+999, pos) // conflicting content
		}
		vbStream = append(vbStream, au(pos == 0, pl)...)
		raw := base + uint32(pos*Step)
		if pos >= 3 {
			raw = uint32(100 + (pos-3)*100) // new source: clock jumps back
		}
		vbIdx.Records = append(vbIdx.Records, index.Record{
			Codec: "h264", Track: 1, Offset: off, Length: AUSize,
			PTS: int64(pos) * Step, Timescale: Timescale,
			RTP: rtpHdr(uint16(j), raw),
		})
	}
	vbStream = append(vbStream, eos...)

	// Truncated stream: one complete AU plus a cut-off AUD, TruncSize bytes.
	trunc := au(false, payload(seed, 100))[:TruncSize-5]
	trunc = append(trunc, 0, 0, 0, 1, 0x09)

	// Misaligned index: offset 50 is mid-AU; offset 216+100 overruns va.h264.
	badIdx := index.File{Stream: "va.h264", Version: "bad", Records: []index.Record{
		{Codec: "h264", Track: 1, Offset: 50, Length: 10, PTS: 0, Timescale: Timescale},
		{Codec: "h264", Track: 1, Offset: 216, Length: 100, PTS: 0, Timescale: Timescale},
	}}

	for _, w := range []struct {
		name string
		data []byte
	}{
		{"va.h264", vaStream},
		{"vb.h264", vbStream},
		{"trunc.h264", trunc},
	} {
		if merr := write(w.name, w.data); merr != nil {
			return merr
		}
	}
	for _, f := range []struct {
		name string
		idx  index.File
	}{
		{"va.index.json", vaIdx},
		{"vb.index.json", vbIdx},
		{"bad.index.json", badIdx},
	} {
		if merr := writeJSON(f.name, f.idx); merr != nil {
			return merr
		}
	}
	return nil
}
