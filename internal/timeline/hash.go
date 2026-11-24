package timeline

// fnv1a64 is a hand-rolled FNV-1a 64-bit hash whose entire state is one
// uint64. Keeping the state explicit (instead of hidden inside hash/fnv) lets
// the engine freeze finalized intervals and later resume hashing from the
// stored state. It produces byte-identical results to hash/fnv's New64a for
// the same input sequence.
//
// FNV-1a: h = offsetBasis; for each byte b { h ^= b; h *= prime } mod 2^64.
const (
	fnvOffsetBasis64 uint64 = 14695981039346656037
	fnvPrime64       uint64 = 1099511628211
)

type hash64 struct {
	state uint64
}

func newHash() *hash64 { return &hash64{state: fnvOffsetBasis64} }

// resumeHash continues a hash from a previously frozen state. Zero restores
// the standard offset basis, so a fresh hash and a resumed-from-zero one agree.
func resumeHash(state uint64) *hash64 {
	if state == 0 {
		state = fnvOffsetBasis64
	}
	return &hash64{state: state}
}

func (x *hash64) writeBytes(b []byte) {
	h := x.state
	for _, c := range b {
		h ^= uint64(c)
		h *= fnvPrime64
	}
	x.state = h
}

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

func (x *hash64) sum64() uint64 { return x.state }
