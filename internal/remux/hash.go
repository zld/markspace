package remux

import "crypto/sha256"

// payloadHasher accumulates a digest over every emitted codec payload in
// deterministic merge order. Container framing is deliberately excluded so
// the digest proves payloads survived lossless remux even if container
// structure changes between formats.
type payloadHasher struct {
	h [32]byte
}

func newPayloadHasher() *payloadHasher {
	return &payloadHasher{}
}

// write extends the running digest. Empty payloads still count as a sample.
func (p *payloadHasher) write(b []byte) {
	sum := sha256.New()
	sum.Write(p.h[:])
	sum.Write(b)
	copy(p.h[:], sum.Sum(nil))
}

func (p *payloadHasher) sum() [32]byte { return p.h }
