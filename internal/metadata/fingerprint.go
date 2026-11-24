package metadata

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math/big"
)

// Key is the normalized identity of one merged event.  Time is reduced to
// lowest terms so 1/90000 and 8/720000 are the same instant, which lets a
// verifier compare an input merged on one timescale with output merged on
// another.
type Key struct {
	Kind    Kind
	TrackID uint32
	Num     string // reduced numerator, decimal
	Den     string // reduced denominator, decimal
	Digest  string
}

// KeyOf returns the normalized key for e.
func KeyOf(e Event) Key {
	num := big.NewInt(e.Time.Ticks)
	den := new(big.Int).SetUint64(uint64(e.Time.Timescale))
	g := new(big.Int).GCD(nil, nil, new(big.Int).Abs(num), den)
	if g.Sign() != 0 && g.Cmp(big.NewInt(1)) != 0 {
		num.Quo(num, g)
		den.Quo(den, g)
	}
	return Key{
		Kind: e.Kind, TrackID: e.TrackID,
		Num: num.String(), Den: den.String(), Digest: e.DigestHex(),
	}
}

// Keys returns the normalized key sequence of a merged result, in order.
func (m Merged) Keys() []Key {
	keys := make([]Key, len(m.Events))
	for i, e := range m.Events {
		keys[i] = KeyOf(e)
	}
	return keys
}

// Fingerprint is a stable digest of the ordered, de-duplicated event sequence.
// Two runs agree on it exactly when they agree on which events survived, in
// what order and with what content.
func (m Merged) Fingerprint() string {
	h := sha256.New()
	for _, e := range m.Events {
		k := KeyOf(e)
		h.Write([]byte(k.Kind))
		h.Write([]byte{0})
		var buf [8]byte
		binary.BigEndian.PutUint32(buf[:4], k.TrackID)
		h.Write(buf[:4])
		h.Write([]byte(k.Num))
		h.Write([]byte{'/'})
		h.Write([]byte(k.Den))
		h.Write([]byte{0})
		h.Write([]byte(k.Digest))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}
