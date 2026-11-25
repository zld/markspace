package index

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// HashFile streams a file in fixed chunks so large shards are not loaded whole.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.CopyBuffer(h, f, make([]byte, 64*1024))
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// DigestSamples returns a stable digest over the identity and positions of samples.
// It intentionally uses a compact line-oriented encoding rather than map iteration.
func DigestSamples(idx *EntryIndex) string {
	h := sha256.New()
	io.WriteString(h, MappingRuleVersion+"\n")
	io.WriteString(h, idx.StableKey+"\n")
	for _, s := range idx.Samples {
		io.WriteString(h, s.StableKey)
		io.WriteString(h, "|")
		writeInt64(h, s.ByteOffset)
		io.WriteString(h, "|")
		writeInt64(h, s.ByteSize)
		io.WriteString(h, "|")
		writeInt64(h, s.StartU)
		io.WriteString(h, "|")
		writeInt64(h, s.EndU)
		io.WriteString(h, "\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeInt64(w io.Writer, v int64) {
	var b [24]byte
	i := len(b)
	neg := v < 0
	if neg {
		if v == -1<<63 {
			io.WriteString(w, "-9223372036854775808")
			return
		}
		v = -v
	}
	for {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
		if v == 0 {
			break
		}
	}
	if neg {
		i--
		b[i] = '-'
	}
	w.Write(b[i:])
}
