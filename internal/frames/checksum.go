package frames

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"

	"mediarun/internal/fault"
)

func sha256Hasher() hash.Hash { return sha256.New() }

// VerifyDigest recomputes SHA-256 over the canonical header and payload and
// compares it with the stored digest. The fault carries the record offset so
// an operator can point at the exact bytes that failed.
func VerifyDigest(offset int64, index int, meta Metadata, payload []byte) error {
	if meta.Size != uint32(len(payload)) {
		e := fault.New(fault.StageChecksum, fault.KindLength,
			"declared size %d does not match payload %d", meta.Size, len(payload))
		e.At(offset, index).WithTrack(meta.Track)
		e.Recoverable = true
		return e
	}
	got := SumRecord(HeaderBytes(meta), payload)
	if got != meta.Checksum {
		e := fault.New(fault.StageChecksum, fault.KindChecksum,
			"stored=%s computed=%s", hex.EncodeToString(meta.Checksum[:8]), hex.EncodeToString(got[:8]))
		e.At(offset, index).WithTrack(meta.Track)
		e.Recoverable = true
		return e
	}
	return nil
}

// SumRecord computes the digest over arbitrary header bytes and payload.
func SumRecord(hdr, payload []byte) [DigestSize]byte {
	h := newDigest()
	h.Write(hdr)
	h.Write(payload)
	var out [DigestSize]byte
	copy(out[:], h.Sum(nil))
	return out
}
