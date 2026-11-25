package media

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	auditerrors "media-audit/internal/errors"
	"media-audit/internal/wire"
)

func EncodeIntegrity(start, end int64, sum [sha256.Size]byte) []byte {
	var b wire.Buffer
	alg := [8]byte{}
	copy(alg[:], "SHA256")
	b.Raw(alg[:])
	b.U64(uint64(start))
	b.U64(uint64(end))
	b.Raw(sum[:])
	return b.Bytes()
}

func DecodeIntegrity(payload []byte, elementOffset int64) (Integrity, error) {
	if len(payload) < 8+16+sha256.Size {
		return Integrity{}, fmt.Errorf("short integrity payload: %d", len(payload))
	}
	alg := string(payload[:6])
	start := int64(u64(payload, 8))
	end := int64(u64(payload, 16))
	digest := payload[24 : 24+sha256.Size]
	return Integrity{
		Algorithm:     alg,
		Range:         auditerrors.ByteRange{Start: start, End: end},
		Digest:        hex.EncodeToString(digest),
		ElementOffset: elementOffset,
	}, nil
}

func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
