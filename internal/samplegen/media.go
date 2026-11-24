package samplegen

import (
	"encoding/binary"
	"fmt"
)

// DemoPIDs are stable across fixtures and explicit mapping rules.
const (
	VideoPID uint16 = 0x0100
	AudioPID uint16 = 0x0101
	PMTPID   uint16 = 0x1000
)

// MediaSpec builds the lossless baseline used by demo and tests.
func MediaSpec(seed int64) TSSpec {
	spec := TSSpec{PMTPID: PMTPID, VideoPID: VideoPID, AudioPID: AudioPID, PATVersion: 0, PMTVersion: 0, IncludePCR: true}
	for i := 0; i < 12; i++ {
		frame := make([]byte, 24+i%7)
		binary.BigEndian.PutUint32(frame[0:4], 0x4d454456) // "MEDV"
		binary.BigEndian.PutUint32(frame[4:8], uint32(i))
		for j := 8; j < len(frame); j++ {
			frame[j] = byte((seed + int64(i)*31 + int64(j)*7) & 0xff)
		}
		spec.Video = append(spec.Video, SampleSpec{
			PTS:      uint64(i) * 3600, // 40 ms
			Data:     frame,
			Keyframe: i%3 == 0,
		})
	}
	for i := 0; i < 24; i++ {
		frame := make([]byte, 16+i%5)
		binary.BigEndian.PutUint32(frame[0:4], 0x4d454441) // "MEDA"
		binary.BigEndian.PutUint32(frame[4:8], uint32(i))
		for j := 8; j < len(frame); j++ {
			frame[j] = byte((seed + int64(i)*17 + int64(j)*11) & 0xff)
		}
		spec.Audio = append(spec.Audio, SampleSpec{
			PTS:  uint64(i) * 1800, // 20 ms
			Data: frame,
		})
	}
	return spec
}

// BuildVersionSwitchTS creates PAT/PMT v0, a bad-CRC v1 candidate that is
// ignored, a complete v1 PAT/PMT, and then media samples.
func BuildVersionSwitchTS(seed int64, includeBadCRC bool) []byte {
	spec := MediaSpec(seed)
	b := &packetBuilder{cc: map[uint16]uint8{}}
	var out []byte

	out = append(out, b.psiPacket(0, BuildPAT(0, PMTPID))...)
	out = append(out, b.psiPacket(PMTPID, BuildPMT(0, PMTPID, VideoPID, AudioPID))...)

	pat1 := BuildPAT(1, PMTPID)
	pmt1 := BuildPMT(1, PMTPID, VideoPID, AudioPID)
	if includeBadCRC {
		badPAT := append([]byte(nil), pat1...)
		badPAT[len(badPAT)-1] ^= 0xff
		out = append(out, b.psiPacket(0, badPAT)...)
	}
	out = append(out, b.psiPacket(0, pat1)...)
	out = append(out, b.psiPacket(PMTPID, pmt1)...)

	for _, s := range spec.Video {
		out = append(out, b.pcrPacket(VideoPID, s.PTS)...)
		pes := BuildPES(0xe0, s.PTS, s.Data)
		for _, pkt := range b.mediaPackets(VideoPID, pes) {
			out = append(out, pkt...)
		}
	}
	for _, s := range spec.Audio {
		pes := BuildPES(0xc0, s.PTS, s.Data)
		for _, pkt := range b.mediaPackets(AudioPID, pes) {
			out = append(out, pkt...)
		}
	}
	return out
}

// ApplyCCJump corrupts the continuity counter of the nth TS packet matching
// pid. It returns the modified byte slice and affected offset.
func ApplyCCJump(ts []byte, pid uint16, packetIndex int) (int64, error) {
	idx := -1
	count := 0
	for off := 0; off+188 <= len(ts); off += 188 {
		if uint16(ts[off+1]&0x1f)<<8|uint16(ts[off+2]) == pid {
			if count == packetIndex {
				idx = off
				break
			}
			count++
		}
	}
	if idx < 0 {
		return -1, fmt.Errorf("pid 0x%04x packet %d not found", pid, packetIndex)
	}
	cc := ts[idx+3] & 0x0f
	ts[idx+3] = (ts[idx+3] & 0xf0) | ((cc + 3) & 0x0f)
	return int64(idx), nil
}

// Truncate returns a deterministic short tail.
func Truncate(ts []byte, tailBytes int) []byte {
	if tailBytes <= 0 || tailBytes >= 188 {
		tailBytes = 37
	}
	cut := len(ts) - 188 + tailBytes
	if cut < 0 {
		cut = len(ts) - tailBytes
	}
	return append([]byte(nil), ts[:cut]...)
}
