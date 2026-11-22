// Package repair restores monotonic media timing from lossy mobile-network
// delivery: 32-bit RTP timestamp wrap-around, sequence gaps (missing
// samples) and small-scale reordering. Large backward jumps that cannot be
// classified as wrap or jitter return a stable recoverable error instead of
// silently rewriting history.
//
// The RTP sidecar is a test-kit generated, self-describing stream of fixed
// records, one per packet. Only headers are inspected; payloads are never
// decoded.
package repair

import (
	"encoding/binary"
	"io"
	"os"

	"github.com/pion/rtp"

	"mediacore/internal/clock"
)

// SidecarMagic prefixes every RTP sidecar.
const SidecarMagic = "MCRTP001"

// SidecarHeader is the 24-byte fixed prefix.
type SidecarHeader struct {
	Magic       [8]byte
	TrackID     uint32
	ClockRate   uint32 // RTP clock rate, equals track timescale
	PacketCount uint32
}

const sidecarHeaderSize = 24

// PacketRecord is the fixed 16-byte per-packet row on disk:
//
//	0..2  RTP header bytes (first 12 of the serialized RTP packet)
//	12..16 payload length carried by this packet
type PacketRecord struct {
	RTP       rtp.Header
	WireBytes uint32
}

const recordSize = 16

// WriteSidecar serializes packets deterministically for the fixture generator.
func WriteSidecar(path string, hdr SidecarHeader, pkts []rtp.Header, wire []uint32) error {
	if len(pkts) != len(wire) || int(hdr.PacketCount) != len(pkts) {
		return clock.ErrInvalidArgument("repair.sidecar: packet count mismatch")
	}
	f, err := os.Create(path)
	if err != nil {
		return clock.ErrRecoverablef("repair.sidecar", "create: %v", err)
	}
	defer f.Close()
	hb := make([]byte, sidecarHeaderSize)
	copy(hb[0:8], SidecarMagic)
	binary.BigEndian.PutUint32(hb[8:12], hdr.TrackID)
	binary.BigEndian.PutUint32(hb[12:16], hdr.ClockRate)
	binary.BigEndian.PutUint32(hb[16:20], hdr.PacketCount)
	if _, err := f.Write(hb); err != nil {
		return clock.ErrRecoverablef("repair.sidecar", "header: %v", err)
	}
	buf := make([]byte, recordSize)
	for i := range pkts {
		raw, err := pkts[i].Marshal()
		if err != nil {
			return clock.ErrRecoverablef("repair.sidecar", "marshal %d: %v", i, err)
		}
		if len(raw) < 12 {
			return clock.ErrInvariantf("repair.sidecar", "packet %d header shorter than 12 bytes", i)
		}
		copy(buf[0:12], raw[0:12])
		binary.BigEndian.PutUint32(buf[12:16], wire[i])
		if _, err := f.Write(buf); err != nil {
			return clock.ErrRecoverablef("repair.sidecar", "record %d: %v", i, err)
		}
	}
	return nil
}

// ReadSidecarHeader validates the prefix and returns the declared counts.
func ReadSidecarHeader(r io.ReaderAt) (SidecarHeader, error) {
	var h SidecarHeader
	buf := make([]byte, sidecarHeaderSize)
	if _, err := r.ReadAt(buf, 0); err != nil {
		return h, clock.ErrRecoverablef("repair.sidecar", "read header: %v", err)
	}
	if string(buf[0:8]) != SidecarMagic {
		return h, clock.ErrRecoverablef("repair.sidecar", "bad magic %q", string(buf[0:8]))
	}
	h.TrackID = binary.BigEndian.Uint32(buf[8:12])
	h.ClockRate = binary.BigEndian.Uint32(buf[12:16])
	h.PacketCount = binary.BigEndian.Uint32(buf[16:20])
	if h.ClockRate == 0 || h.PacketCount == 0 {
		return h, clock.ErrRecoverablef("repair.sidecar", "zero clock rate or packet count")
	}
	return h, nil
}

// ReadPacket performs a bounds-checked fixed-width record read.
func ReadPacket(r io.ReaderAt, index uint32) (PacketRecord, error) {
	var rec PacketRecord
	buf := make([]byte, recordSize)
	off := int64(sidecarHeaderSize) + int64(index)*recordSize
	if _, err := r.ReadAt(buf, off); err != nil {
		return rec, clock.ErrRecoverablef("repair.sidecar", "packet %d: %v", index, err)
	}
	var h rtp.Header
	if n, err := h.Unmarshal(buf[0:12]); err != nil || n != 12 {
		return rec, clock.ErrRecoverablef("repair.sidecar", "packet %d rtp unmarshal: %v", index, err)
	}
	rec.RTP = h
	rec.WireBytes = binary.BigEndian.Uint32(buf[12:16])
	return rec, nil
}
