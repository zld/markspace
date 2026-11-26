package fixtures

import (
	"encoding/binary"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"replaypipe/internal/metadata"
	"replaypipe/internal/mp4box"
)

// buildSEI returns Annex B data with one SEI NAL per fixture event.
func buildSEI() []byte {
	var out []byte
	for i, e := range EventSeconds {
		startNS := int64(e.Start * 1e9)
		out = append(out, metadata.EncodeSEI(startNS, e.Text)...)
		_ = i
	}
	return out
}

// buildRTP returns an RTP side-band capture at the fixture video clock rate,
// with marker packets at every event start.
func buildRTP() []byte {
	var out []byte
	seq := uint16(1000)
	ssrc := uint32(0x51ce)
	for i, e := range EventSeconds {
		ts := uint32(e.Start * Timescale)
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Marker:         true,
				PayloadType:    96,
				SequenceNumber: seq,
				Timestamp:      ts,
				SSRC:           ssrc,
			},
			Payload: []byte{0x01, byte(i)},
		}
		raw, err := pkt.Marshal()
		if err != nil {
			continue
		}
		out = append(out, lengthPrefixed(raw)...)
		seq++
		// A non-marker packet between events keeps the stream realistic.
		body := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: seq,
				Timestamp:      ts + 1500,
				SSRC:           ssrc,
			},
			Payload: []byte{0x02, byte(i)},
		}
		raw, err = body.Marshal()
		if err != nil {
			continue
		}
		out = append(out, lengthPrefixed(raw)...)
		seq++
	}
	return out
}

// lengthPrefixed frames one raw RTP packet as a 4-byte big-endian length
// followed by the packet, which is the capture format ParseRTP consumes.
func lengthPrefixed(raw []byte) []byte {
	out := make([]byte, 4, 4+len(raw))
	binary.BigEndian.PutUint32(out, uint32(len(raw)))
	return append(out, raw...)
}

// writeCorrupt derives the defect fixtures from the healthy file.
func writeCorrupt(root, normalPath string) ([]Corrupt, error) {
	healthy, err := os.ReadFile(normalPath)
	if err != nil {
		return nil, err
	}
	var out []Corrupt
	add := func(name, code string, data []byte) error {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		out = append(out, Corrupt{Name: name, Path: path, Code: code})
		return nil
	}
	// 1. Truncated box: cut inside the mdat payload.
	if err := add(TruncatedFile, "BOX_TRUNCATED", healthy[:len(healthy)-37]); err != nil {
		return nil, err
	}
	// 2. tfdt regression: a fragmented file whose second fragment moves back.
	if err := add(RegressionFile, "TIME_REGRESSION", regressionFragments()); err != nil {
		return nil, err
	}
	// 3. Time overflow: a sample table whose duration overflows ns mapping.
	if err := add(OverflowFile, "TIME_OVERFLOW", overflowFile()); err != nil {
		return nil, err
	}
	// 4. Event out of range: a cue past the media end.
	if err := add(OutOfRangeFile, "EVENT_OUT_OF_RANGE", outOfRangeSRT()); err != nil {
		return nil, err
	}
	return out, nil
}

// fragment is a minimal moof+mdat pair used to exercise tfdt handling.
type fragment struct {
	seq     uint32
	baseDTS uint64
	samples int
}

func buildFragmented(frags []fragment, timescale uint32) []byte {
	ftyp := mp4box.Box("ftyp", []byte("isom"), mp4box.U32(0x200), []byte("isom"), []byte("iso6"), []byte("cmff"))
	// An empty moov with one track and a trex default.
	tkhd := mp4box.FullBox("tkhd", 0, 3,
		mp4box.U32(0), mp4box.U32(0), mp4box.U32(1), mp4box.U32(0), mp4box.U32(0),
		mp4box.Zeros(8), mp4box.I16(0), mp4box.I16(0), mp4box.I16(0), mp4box.Zeros(2),
		mp4box.Matrix(), mp4box.Fixed1616(Width), mp4box.Fixed1616(Height))
	mdhd := mp4box.FullBox("mdhd", 0, 0, mp4box.U32(0), mp4box.U32(0), mp4box.U32(timescale), mp4box.U32(0), mp4box.U16(0x55c4), mp4box.U16(0))
	hdlr := mp4box.FullBox("hdlr", 0, 0, mp4box.U32(0), []byte("vide"), mp4box.Zeros(12), []byte("VideoHandler\x00"))
	mvhd := mp4box.FullBox("mvhd", 0, 0, mp4box.U32(0), mp4box.U32(0), mp4box.U32(1000), mp4box.U32(0),
		mp4box.U32(0x00010000), mp4box.I16(0x100), mp4box.Zeros(2), mp4box.Zeros(8),
		mp4box.Matrix(), mp4box.Zeros(24), mp4box.U32(2))
	trex := mp4box.FullBox("trex", 0, 0, mp4box.U32(1), mp4box.U32(1), mp4box.U32(FrameDur), mp4box.U32(600), mp4box.U32(0))
	moov := mp4box.Container("moov", mvhd,
		mp4box.Container("trak", tkhd, mp4box.Container("mdia", mdhd, hdlr,
			mp4box.Container("minf", mp4box.FullBox("vmhd", 0, 1, mp4box.Zeros(8))))))
	moov = insertMvex(moov, mp4box.Container("mvex", trex))
	out := append(append([]byte{}, ftyp...), moov...)
	for _, f := range frags {
		moof := mp4box.Container("moof",
			mp4box.FullBox("mfhd", 0, 0, mp4box.U32(f.seq)),
			mp4box.Container("traf",
				mp4box.FullBox("tfhd", 0, 0x020000, mp4box.U32(1)),
				mp4box.FullBox("tfdt", 1, 0, mp4box.U64(f.baseDTS)),
				moofTrun(f.samples),
			),
		)
		// Adjust trun data_offset to point past moof+mdat header.
		page := make([]byte, 0, len(moof)+8)
		trunBytes := moofTrun(f.samples)
		fixTrunDataOffset(trunBytes, int32(len(moof)+8))
		moof = mp4box.Container("moof",
			mp4box.FullBox("mfhd", 0, 0, mp4box.U32(f.seq)),
			mp4box.Container("traf",
				mp4box.FullBox("tfhd", 0, 0x020000, mp4box.U32(1)),
				mp4box.FullBox("tfdt", 1, 0, mp4box.U64(f.baseDTS)),
				trunBytes,
			),
		)
		page = append(page, moof...)
		mdatPayload := make([]byte, 0, f.samples*600)
		for i := 0; i < f.samples; i++ {
			mdatPayload = append(mdatPayload, pattern(byte(0x10+i), 600, i)...)
		}
		page = append(page, mp4box.Box("mdat", mdatPayload)...)
		out = append(out, page...)
	}
	return out
}

func insertMvex(moov, mvex []byte) []byte {
	// moov header is 8 bytes; insert mvex right after it.
	if len(moov) < 8 {
		return moov
	}
	payload := append(append([]byte{}, moov[8:]...), mvex...)
	return mp4box.Box("moov", payload)
}

func moofTrun(samples int) []byte {
	entries := make([]byte, 0, samples*8)
	for i := 0; i < samples; i++ {
		entries = append(entries, mp4box.U32(600)...) // size
	}
	// flags: data-offset + sample-size
	p := append([]byte{}, mp4box.U32(uint32(samples))...)
	p = append(p, mp4box.I32(0)...) // data_offset patched later
	p = append(p, entries...)
	return mp4box.FullBox("trun", 0, 0x000201, p)
}

func fixTrunDataOffset(trun []byte, off int32) {
	// Layout: size(4) type(4) version(1) flags(3) sample_count(4)
	// data_offset(4) entries...
	if len(trun) < 20 {
		return
	}
	binary.BigEndian.PutUint32(trun[16:20], uint32(off))
}

func regressionFragments() []byte {
	return buildFragmented([]fragment{
		{seq: 1, baseDTS: 0, samples: 4},
		{seq: 2, baseDTS: 12000, samples: 4},
		{seq: 3, baseDTS: 6000, samples: 4}, // regresses
	}, Timescale)
}

// overflowFile writes a fragmented file whose tfdt is far beyond the range
// that can be mapped to int64 nanoseconds, forcing TIME_OVERFLOW.
func overflowFile() []byte {
	return buildFragmented([]fragment{
		{seq: 1, baseDTS: 0, samples: 2},
		{seq: 2, baseDTS: 1 << 62, samples: 2},
	}, Timescale)
}

func outOfRangeSRT() []byte {
	return []byte("1\r\n00:00:02,000 --> 00:00:04,000\r\nkickoff\r\n\r\n" +
		"2\r\n00:00:45,000 --> 00:00:46,000\r\nafter the media ends\r\n\r\n")
}

// LegacyManifestBytes exposes the fixture legacy manifest content.
func LegacyManifestBytes() []byte { return []byte(legacyManifest()) }

var _ = mp4box.Zeros
