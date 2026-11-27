package pipeline

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"mediakernel/internal/cache"
	"mediakernel/internal/wav"
)

// Stage artifact digests are deterministic SHA-256 hashes over each stage's
// canonical outputs. They make checkpoint matching independent of in-memory
// addresses and let a resumed run verify cheap stages without re-executing
// heavy ones.

func digestHeader(h wav.Header) string {
	hsh := sha256.New()
	var u4 [4]byte
	var u8 [8]byte
	putU32 := func(v uint32) {
		binary.LittleEndian.PutUint32(u4[:], v)
		hsh.Write(u4[:])
	}
	putU16 := func(v uint16) {
		binary.LittleEndian.PutUint16(u8[:2], v)
		hsh.Write(u8[:2])
	}
	putI64 := func(v int64) {
		binary.LittleEndian.PutUint64(u8[:], uint64(v))
		hsh.Write(u8[:])
	}
	putU32(h.SampleRate)
	putU16(h.Channels)
	putU16(h.BitsPerSample)
	putI64(h.DataOffset)
	putI64(h.DataLength)
	putI64(h.FileLength)
	return hex.EncodeToString(hsh.Sum(nil))
}

func digestValidation(channels uint16, rate uint32, layoutName string) string {
	hsh := sha256.New()
	var u2 [2]byte
	var u4 [4]byte
	binary.LittleEndian.PutUint16(u2[:], channels)
	hsh.Write(u2[:])
	binary.LittleEndian.PutUint32(u4[:], rate)
	hsh.Write(u4[:])
	hsh.Write([]byte(layoutName))
	return hex.EncodeToString(hsh.Sum(nil))
}

// QueryDigest summarizes the interval verification deterministically:
// interval count plus the start frame of each interval.
func QueryDigest(frameStarts []int64, intervalCount int) string {
	hsh := sha256.New()
	var u8 [8]byte
	putI64 := func(v int64) {
		binary.LittleEndian.PutUint64(u8[:], uint64(v))
		hsh.Write(u8[:])
	}
	putI64(int64(intervalCount))
	for _, f := range frameStarts {
		putI64(f)
	}
	return hex.EncodeToString(hsh.Sum(nil))
}

// ReportDigest is the final pipeline output digest; the report package
// renders the same canonical payload so CLI and checkpoint values agree.
func ReportDigest(p cache.Params, indexDigest string, queryDigest string, frames int64) string {
	hsh := sha256.New()
	hsh.Write([]byte(ParamsDigest(p)))
	hsh.Write([]byte(indexDigest))
	hsh.Write([]byte(queryDigest))
	var u8 [8]byte
	binary.LittleEndian.PutUint64(u8[:], uint64(frames))
	hsh.Write(u8[:])
	return hex.EncodeToString(hsh.Sum(nil))
}
