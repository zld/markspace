package fixture

import (
	"os"
	"path/filepath"
	"sort"
)

// StreamSpec describes one RTP track to generate.
type StreamSpec struct {
	Track    string
	Codec    string // ingest.CodecOpus / ingest.CodecAAC
	ClockHz  uint32
	SSRC     uint32
	Payload  uint8
	Packets  int // number of sequence-consecutive packets before gaps
	DropSeqs []uint16
	DupSeqs  []uint16
	BaseSeq  uint16
	StartTS  uint32
	TStep    uint32
}

// OpusStreamSpec builds a healthy Opus spec: config 12 (WB, 20 ms), code 0.
func OpusStreamSpec(track string, packets int) StreamSpec {
	return StreamSpec{
		Track: track, Codec: "opus", ClockHz: 48000, SSRC: 0x42420001,
		Payload: 96, Packets: packets, BaseSeq: 100, StartTS: 0, TStep: 960,
	}
}

// AACStreamSpec builds a healthy AAC-hbr spec at 44.1 kHz.
func AACStreamSpec(track string, packets int) StreamSpec {
	return StreamSpec{
		Track: track, Codec: "aac", ClockHz: 44100, SSRC: 0x42420002,
		Payload: 97, Packets: packets, BaseSeq: 200, StartTS: 0, TStep: 1024,
	}
}

// BuildRTPStream writes a deterministic length-prefixed RTP file. The
// returned size is the exact file length in bytes.
func BuildRTPStream(dir string, spec StreamSpec) (string, int64, error) {
	drop := map[uint16]bool{}
	for _, s := range spec.DropSeqs {
		drop[s] = true
	}
	dup := map[uint16]bool{}
	for _, s := range spec.DupSeqs {
		dup[s] = true
	}
	var frames [][]byte
	for i := 0; i < spec.Packets; i++ {
		seq := spec.BaseSeq + uint16(i)
		if drop[seq] {
			continue
		}
		payload := streamPayload(spec, i)
		pkt := RTPPacket(spec.SSRC, seq, spec.StartTS+uint32(i)*spec.TStep, spec.Payload, payload)
		frames = append(frames, pkt)
		if dup[seq] {
			frames = append(frames, RTPPacket(spec.SSRC, seq,
				spec.StartTS+uint32(i)*spec.TStep, spec.Payload, payload))
		}
	}
	path := filepath.Join(dir, spec.Track+".rtp")
	if err := WriteRTPFile(path, frames); err != nil {
		return "", 0, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", 0, err
	}
	return path, fi.Size(), nil
}

// BuildRTPStreamPayloads is like BuildRTPStream but lets the caller supply
// explicit per-packet payloads, e.g. oversized AAC access units.
func BuildRTPStreamPayloads(dir string, spec StreamSpec, payloads [][]byte) (string, int64, error) {
	var frames [][]byte
	for i, payload := range payloads {
		seq := spec.BaseSeq + uint16(i)
		frames = append(frames, RTPPacket(spec.SSRC, seq,
			spec.StartTS+uint32(i)*spec.TStep, spec.Payload, payload))
	}
	path := filepath.Join(dir, spec.Track+".rtp")
	if err := WriteRTPFile(path, frames); err != nil {
		return "", 0, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", 0, err
	}
	return path, fi.Size(), nil
}

func streamPayload(spec StreamSpec, idx int) []byte {
	if spec.Codec == "opus" {
		// config 12 => 20 ms WB, code 0 => one frame; 40-byte packets.
		return OpusPayload(12, 0, 40)
	}
	// AAC: one 32-byte AU per packet.
	return AACPayload(32)
}

// DamageAt corrupts the frame starting at targetOffset so its declared
// length exceeds the bytes that remain, forcing the streaming parser to
// report a truncated-frame error pinned to targetOffset. The file keeps
// every earlier frame intact. It returns the target offset for
// cross-checking against the parser's reported one.
func DamageAt(path string, targetOffset int64) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if targetOffset+2 > int64(len(data)) {
		return 0, os.ErrInvalid
	}
	// Read the real frame length, then inflate the 16-bit prefix so the
	// declared packet runs past EOF.
	realLen := int(uint16(data[targetOffset])<<8 | uint16(data[targetOffset+1]))
	if realLen <= 0 {
		return 0, os.ErrInvalid
	}
	bogus := realLen + len(data)
	if bogus > 0xffff || bogus <= realLen {
		bogus = 0xffff
	}
	data[targetOffset] = byte(bogus >> 8)
	data[targetOffset+1] = byte(bogus)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return 0, err
	}
	return targetOffset, nil
}

// FindFrameStart returns the byte offset of the nth RTP frame's length
// prefix (0-based), by walking the framing of an undamaged file.
func FindFrameStart(path string, frameIndex int) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var pos int64
	for i := 0; i <= frameIndex; i++ {
		if i == frameIndex {
			return pos, nil
		}
		frameLen := int(uint16(data[pos])<<8 | uint16(data[pos+1]))
		pos += 2 + int64(frameLen)
	}
	return pos, nil
}

// TruncateMP4 corrupts the stsz box so its declared size extends beyond
// its parent container, modeling a truncated box while keeping every
// ancestor header readable. The scanner must then pinpoint the stsz start
// offset. The returned offset is that truncation point.
func TruncateMP4(path string) (int64, error) {
	off, err := BoxOffset(path, "stsz")
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	// Inflate the 32-bit box size to the maximum; parent boundary check
	// then rejects it at the stsz offset.
	data[off] = 0x7f
	data[off+1] = 0xff
	data[off+2] = 0xff
	data[off+3] = 0xff
	return off, os.WriteFile(path, data, 0o644)
}

// BoxOffset returns the start offset of the first box of the given type.
func BoxOffset(path, boxType string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	offsets := walkBoxOffsets(data, 0, int64(len(data)))
	got, ok := offsets[boxType]
	if !ok {
		return 0, os.ErrNotExist
	}
	return got, nil
}

func walkBoxOffsets(data []byte, start, end int64) map[string]int64 {
	out := map[string]int64{}
	pos := start
	for pos+8 <= end {
		size := int64(uint32(data[pos])<<24 | uint32(data[pos+1])<<16 |
			uint32(data[pos+2])<<8 | uint32(data[pos+3]))
		t := string(data[pos+4 : pos+8])
		if size == 0 || pos+size > end {
			break
		}
		if _, seen := out[t]; !seen {
			out[t] = pos
		}
		if isContainer(t) {
			for k, v := range walkBoxOffsets(data, pos+8, pos+size) {
				if _, seen := out[k]; !seen {
					out[k] = v
				}
			}
		}
		pos += size
	}
	return out
}

func isContainer(t string) bool {
	switch t {
	case "moov", "trak", "mdia", "minf", "stbl", "dinf", "udta", "edts":
		return true
	}
	return false
}

// SortedTracks returns track names in stable order for deterministic DAGs.
func SortedTracks(tracks []string) []string {
	out := append([]string(nil), tracks...)
	sort.Strings(out)
	return out
}
