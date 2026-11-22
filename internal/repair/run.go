package repair

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"os"

	"mediacore/internal/clock"
)

// RunResult is the deterministic outcome of one repair pass.
type RunResult struct {
	Header SidecarHeader
	Stats  Stats
	// Digest covers the canonical extended point stream
	// (u64 seq, i64 ts, u32 raw, u32 wire per point) so repeated runs over
	// identical sidecars always produce the same string.
	Digest string
	Points uint64
}

// RunFile streams a sidecar through an Engine with constant memory. The
// context is honored between packets, so a slow-consumer cancellation stops
// the pass with a classified canceled error and no partial digest.
func RunFile(ctx context.Context, path string, cfg Config) (*RunResult, error) {
	const op = "repair.run"
	f, err := os.Open(path)
	if err != nil {
		return nil, clock.ErrRecoverablef(op, "open sidecar: %v", err)
	}
	defer f.Close()

	hdr, err := ReadSidecarHeader(f)
	if err != nil {
		return nil, err
	}
	eng, err := NewEngine(cfg)
	if err != nil {
		return nil, err
	}
	sum := sha256.New()
	var buf [24]byte // canonical point encoding width
	res := &RunResult{Header: hdr}

	var i uint32
	for i = 0; i < hdr.PacketCount; i++ {
		if err := ctx.Err(); err != nil {
			return nil, clock.ErrCanceled(op, err)
		}
		rec, err := ReadPacket(f, i)
		if err != nil {
			return nil, err
		}
		points, err := eng.Push(rec.RTP.SequenceNumber, rec.RTP.Timestamp, rec.WireBytes)
		if err != nil {
			return nil, err
		}
		for _, p := range points {
			hashPoint(sum, buf[:], p)
			res.Points++
		}
	}
	res.Stats = eng.Stats()
	res.Digest = hex.EncodeToString(sum.Sum(nil))
	return res, nil
}

func hashPoint(h hash.Hash, buf []byte, p Point) {
	binary.BigEndian.PutUint64(buf[0:8], p.Sequence)
	binary.BigEndian.PutUint64(buf[8:16], uint64(p.Timestamp))
	binary.BigEndian.PutUint32(buf[16:20], p.RawTimestamp)
	binary.BigEndian.PutUint32(buf[20:24], p.WireBytes)
	h.Write(buf)
}
