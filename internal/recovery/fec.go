// Package recovery implements single-packet-loss repair using XOR parity.
//
// A parity packet covers a group of consecutive media packets. When exactly
// one member of a protected group is missing the parity reconstructs it:
//
//	missing = parity XOR (every other member payload)
//
// Payloads must all be the same length, as XOR gives no way to recover the
// length of a lost member. Groups are addressed by the sequence number of
// their first member and are bounded in size, both in packets and in total
// bytes, so the recovery cache cannot grow without limit.
//
// Multi-packet loss and checksum failures are never guessed at: the affected
// group is reported with an explicit Unrecoverable error and the cache moves
// on, leaving later groups decodable.
package recovery

import (
	"sort"

	"example.com/mediaforge/internal/merrors"
)

// Default bounds.
const (
	DefaultGroupSize     = 4
	DefaultMaxGroups     = 256
	DefaultMaxGroupBytes = 64 * 1024
)

// Config bounds the recovery cache.
type Config struct {
	// GroupSize is the number of media packets covered by one parity packet.
	GroupSize int
	// MaxGroups bounds the number of cached groups.
	MaxGroups int
	// MaxGroupBytes bounds the bytes stored for one group including parity.
	MaxGroupBytes int
}

// DefaultConfig returns conservative bounds.
func DefaultConfig() Config {
	return Config{
		GroupSize:     DefaultGroupSize,
		MaxGroups:     DefaultMaxGroups,
		MaxGroupBytes: DefaultMaxGroupBytes,
	}
}

func (c Config) validate() error {
	if c.GroupSize < 1 || c.GroupSize > 16 {
		return merrors.New(merrors.StageFEC, merrors.Invalid,
			"group size %d outside 1..16", c.GroupSize)
	}
	if c.MaxGroups <= 0 || c.MaxGroups > 1<<16 {
		return merrors.New(merrors.StageFEC, merrors.Invalid,
			"max groups %d outside 1..65536", c.MaxGroups)
	}
	if c.MaxGroupBytes <= 0 || c.MaxGroupBytes > 1<<20 {
		return merrors.New(merrors.StageFEC, merrors.Invalid,
			"max group bytes %d outside 1..1048576", c.MaxGroupBytes)
	}
	return nil
}

// groupKey identifies a protected group by the sequence number of its first
// member, which makes reconstruction independent of arrival order.
type groupKey struct {
	start uint16
	ssrc  uint32
}

type group struct {
	payloads   map[uint16][]byte
	parity     []byte
	haveParity bool
	size       int
}

// Result reports the outcome of a push or a repair attempt.
type Result struct {
	// Recovered lists sequence numbers reconstructed from parity.
	Recovered []uint16
	// Unrecoverable lists groups whose loss could not be repaired.
	Unrecoverable []Gap
}

// Gap describes an unrecoverable region.
type Gap struct {
	SSRC   uint32
	From   uint16
	Count  int
	Reason merrors.Category
	Detail string
}

// Cache holds parity groups for one SSRC.
type Cache struct {
	cfg  Config
	ssrc uint32

	groups  map[groupKey]*group
	order   []groupKey      // insertion order, bounded by MaxGroups
	rebuilt map[uint16]bool // sequence numbers whose payload came from parity

	recovered     int
	unrecoverable int
	checksumFail  int
}

// NewCache creates a recovery cache for ssrc.
func NewCache(ssrc uint32, cfg Config) (*Cache, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Cache{
		cfg: cfg, ssrc: ssrc,
		groups:  make(map[groupKey]*group),
		rebuilt: make(map[uint16]bool),
	}, nil
}

// Stats reports recovery counters.
type Stats struct {
	Recovered     int
	Unrecoverable int
	ChecksumFail  int
	Groups        int
}

// Stats returns counters.
func (c *Cache) Stats() Stats {
	return Stats{
		Recovered:     c.recovered,
		Unrecoverable: c.unrecoverable,
		ChecksumFail:  c.checksumFail,
		Groups:        len(c.groups),
	}
}

func (c *Cache) keyFor(seq uint16) groupKey {
	start := seq - uint16(int(seq)%c.cfg.GroupSize)
	return groupKey{start: start, ssrc: c.ssrc}
}

func (c *Cache) group(k groupKey) (*group, error) {
	g, ok := c.groups[k]
	if ok {
		return g, nil
	}
	if len(c.groups) >= c.cfg.MaxGroups {
		// Evict the oldest group; its window has passed.
		oldest := c.order[0]
		delete(c.groups, oldest)
		c.order = c.order[1:]
	}
	g = &group{payloads: make(map[uint16][]byte), size: c.cfg.GroupSize}
	c.groups[k] = g
	c.order = append(c.order, k)
	return g, nil
}

func (c *Cache) groupBytes(g *group) int {
	n := len(g.parity)
	for _, p := range g.payloads {
		n += len(p)
	}
	return n
}

// AddMedia records a media payload for recovery. Payload length is checked
// against the group so that reconstruction can be exact.
func (c *Cache) AddMedia(ssrc uint32, seq uint16, payload []byte) error {
	if ssrc != c.ssrc {
		return merrors.Packet(merrors.StageFEC, merrors.Invalid, ssrc, seq,
			"media for unexpected SSRC")
	}
	g, err := c.group(c.keyFor(seq))
	if err != nil {
		return err
	}
	if len(g.parity) > 0 && len(payload) != len(g.parity) {
		return merrors.Packet(merrors.StageFEC, merrors.Truncated, c.ssrc, seq,
			"payload %d bytes, group parity %d bytes", len(payload), len(g.parity))
	}
	if _, dup := g.payloads[seq]; dup {
		return merrors.Packet(merrors.StageFEC, merrors.Duplicate, c.ssrc, seq,
			"duplicate media payload")
	}
	if c.groupBytes(g)+len(payload) > c.cfg.MaxGroupBytes {
		return merrors.Packet(merrors.StageFEC, merrors.Bounds, c.ssrc, seq,
			"group exceeds %d bytes", c.cfg.MaxGroupBytes)
	}
	g.payloads[seq] = append([]byte(nil), payload...)
	return nil
}

// AddParity records the XOR parity packet for the group containing seq. A
// parity packet whose length disagrees with any stored media cannot be used
// for repair and is rejected as a checksum failure.
func (c *Cache) AddParity(ssrc uint32, seq uint16, payload []byte) error {
	if ssrc != c.ssrc {
		return merrors.Packet(merrors.StageFEC, merrors.Invalid, ssrc, seq,
			"parity for unexpected SSRC")
	}
	g, err := c.group(c.keyFor(seq))
	if err != nil {
		return err
	}
	for _, p := range g.payloads {
		if len(p) != len(payload) {
			c.checksumFail++
			return merrors.Packet(merrors.StageFEC, merrors.Checksum, c.ssrc, seq,
				"parity %d bytes, media %d bytes", len(payload), len(p))
		}
	}
	if g.haveParity {
		return merrors.Packet(merrors.StageFEC, merrors.Duplicate, c.ssrc, seq,
			"duplicate parity packet")
	}
	if c.groupBytes(g)+len(payload) > c.cfg.MaxGroupBytes {
		return merrors.Packet(merrors.StageFEC, merrors.Bounds, c.ssrc, seq,
			"group exceeds %d bytes", c.cfg.MaxGroupBytes)
	}
	g.parity = append([]byte(nil), payload...)
	g.haveParity = true
	return nil
}

// Repair tries to reconstruct every missing member of the group containing
// seq. The caller supplies the set of sequence numbers it expects in the
// group (for example by knowing the stream's packet count) through expected.
//
// A group with more than one missing member is unrecoverable, as is a group
// that lacks parity. Both are reported explicitly; the group is then retired
// so later groups continue unaffected.
func (c *Cache) Repair(seq uint16, expected []uint16) (Result, error) {
	g, ok := c.groups[c.keyFor(seq)]
	if !ok {
		return Result{}, merrors.Packet(merrors.StageFEC, merrors.Empty, c.ssrc, seq,
			"no group for sequence")
	}
	if !g.haveParity {
		return Result{}, merrors.Packet(merrors.StageFEC, merrors.Unrecoverable, c.ssrc, seq,
			"group has no parity")
	}

	var missing []uint16
	for _, want := range expected {
		if c.keyFor(want) != c.keyFor(seq) {
			continue
		}
		if _, have := g.payloads[want]; !have {
			missing = append(missing, want)
		}
	}
	sort.Slice(missing, func(i, j int) bool {
		return int(int16(missing[i]-missing[j])) < 0
	})

	if len(missing) == 0 {
		return Result{}, nil
	}
	if len(missing) > 1 {
		c.unrecoverable++
		res := Result{Unrecoverable: []Gap{{
			SSRC: c.ssrc, From: missing[0], Count: len(missing),
			Reason: merrors.Unrecoverable,
			Detail: "multiple packets lost in one parity group",
		}}}
		delete(c.groups, c.keyFor(seq))
		return res, merrors.Packet(merrors.StageFEC, merrors.Unrecoverable, c.ssrc, missing[0],
			"%d packets lost in one group", len(missing))
	}

	rec := make([]byte, len(g.parity))
	copy(rec, g.parity)
	for s, p := range g.payloads {
		if s == missing[0] {
			continue
		}
		if len(p) != len(rec) {
			c.checksumFail++
			delete(c.groups, c.keyFor(seq))
			return Result{}, merrors.Packet(merrors.StageFEC, merrors.Checksum, c.ssrc, missing[0],
				"inconsistent payload length %d vs %d", len(p), len(rec))
		}
		xorInto(rec, p)
	}
	g.payloads[missing[0]] = rec
	c.rebuilt[missing[0]] = true
	c.recovered++
	return Result{Recovered: []uint16{missing[0]}}, nil
}

// Rebuilt returns a payload that was reconstructed from parity, and whether
// seq was rebuilt at all. Payloads that arrived normally are not reported
// here.
func (c *Cache) Rebuilt(seq uint16) ([]byte, bool) {
	if !c.rebuilt[seq] {
		return nil, false
	}
	g, ok := c.groups[c.keyFor(seq)]
	if !ok {
		return nil, false
	}
	p, ok := g.payloads[seq]
	return p, ok
}

// Retire drops a completed group from the cache.
func (c *Cache) Retire(seq uint16) {
	delete(c.groups, c.keyFor(seq))
	delete(c.rebuilt, seq)
	for i, k := range c.order {
		if k == c.keyFor(seq) {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

func xorInto(dst, src []byte) {
	for i := range dst {
		dst[i] ^= src[i]
	}
}

// MakeParity computes the XOR of a group's payloads; all must be equal length.
func MakeParity(payloads [][]byte) ([]byte, error) {
	if len(payloads) == 0 {
		return nil, merrors.New(merrors.StageFEC, merrors.Empty, "no payloads")
	}
	n := len(payloads[0])
	out := make([]byte, n)
	for i, p := range payloads {
		if len(p) != n {
			return nil, merrors.At(merrors.StageFEC, merrors.Malformed, int64(i),
				"payload %d is %d bytes, expected %d", i, len(p), n)
		}
		xorInto(out, p)
	}
	return out, nil
}

// GroupStart returns the first sequence number of the group containing seq,
// which callers use to generate and address parity packets.
func (c *Cache) GroupStart(seq uint16) uint16 { return c.keyFor(seq).start }
