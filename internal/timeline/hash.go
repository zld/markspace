package timeline

import "hash/fnv"

// hash64 wraps FNV-1a with fixed-width integer encodings. Only deterministic
// byte sequences ever enter the hash, so timeline hashes are stable across
// processes and platforms.
type hash64 struct{ h interface {
	Write([]byte) (int, error)
	Sum64() uint64
} }

func newHash() *hash64 {
	return &hash64{h: fnv.New64a()}
}

func (x *hash64) writeBytes(b []byte) { _, _ = x.h.Write(b) }

func (x *hash64) write(s string) { x.writeBytes([]byte(s)) }

func (x *hash64) writeInt64(v int64) {
	var buf [8]byte
	u := uint64(v)
	for i := 7; i >= 0; i-- {
		buf[i] = byte(u)
		u >>= 8
	}
	x.writeBytes(buf[:])
}

func (x *hash64) writeUint64(v uint64) { x.writeInt64(int64(v)) }

func (x *hash64) writeInt(v int) { x.writeInt64(int64(v)) }

func (x *hash64) sum64() uint64 { return x.h.Sum64() }
