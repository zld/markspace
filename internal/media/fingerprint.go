package media

import "crypto/sha256"

// HashFrame returns the content hash of one valid media frame. RTP sequence
// numbers, source offsets and wall-clock arrival are deliberately excluded so
// identical content at a different timeline position hashes identically.
func HashFrame(f Frame) [32]byte {
	h := sha256.New()
	h.Write([]byte("medpipe-frame-v1\n"))
	var meta [18]byte
	meta[0] = f.ContentType
	meta[1] = boolByte(f.Key)
	putUint64BE(meta[2:10], uint64(f.Duration))
	putUint64BE(meta[10:18], uint64(f.Energy))
	h.Write(meta[:])
	h.Write(f.Content)
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

func putUint64BE(b []byte, v uint64) {
	_ = b[7]
	for i := 7; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
}

// Fingerprint is a resumable SHA-256 chain of frame hashes. Chaining frame
// hashes rather than raw bytes makes checkpoint state small and explicit.
type Fingerprint struct {
	chain  [32]byte
	frames int
}

func NewFingerprint() *Fingerprint {
	return &Fingerprint{}
}

func FingerprintFromState(chain [32]byte, frames int) *Fingerprint {
	return &Fingerprint{chain: chain, frames: frames}
}

// Add incorporates one frame. If f.Hash is zero it is calculated from content.
func (f *Fingerprint) Add(frame Frame) {
	h := frame.Hash
	if h == [32]byte{} {
		h = HashFrame(frame)
	}
	s := sha256.New()
	s.Write([]byte("medpipe-fingerprint-v1\n"))
	s.Write(f.chain[:])
	s.Write(h[:])
	copy(f.chain[:], s.Sum(nil))
	f.frames++
}

func (f *Fingerprint) Sum() string {
	return formatHex(f.chain[:])
}

func (f *Fingerprint) Chain() [32]byte { return f.chain }
func (f *Fingerprint) Frames() int     { return f.frames }

type FingerprintState struct {
	Chain  [32]byte `json:"chain"`
	Frames int      `json:"frames"`
}

func (f *Fingerprint) State() FingerprintState {
	return FingerprintState{Chain: f.chain, Frames: f.frames}
}

func (f *Fingerprint) Restore(st FingerprintState) {
	f.chain = st.Chain
	f.frames = st.Frames
}

// Deduper maps exact segment fingerprints to the first segment index.
type Deduper struct {
	first map[string]int
}

func NewDeduper() *Deduper { return &Deduper{first: make(map[string]int)} }

// Observe returns duplicateOf >= 0 when fp was seen. The first occurrence is
// registered and returns -1. A different fingerprint never matches.
func (d *Deduper) Observe(fp string, segmentIndex int) (duplicateOf int) {
	if idx, ok := d.first[fp]; ok {
		return idx
	}
	d.first[fp] = segmentIndex
	return -1
}

func (d *Deduper) First(fp string) (int, bool) {
	i, ok := d.first[fp]
	return i, ok
}

type DeduperState struct {
	First map[string]int `json:"first"`
}

func (d *Deduper) State() DeduperState {
	m := make(map[string]int, len(d.first))
	for k, v := range d.first {
		m[k] = v
	}
	return DeduperState{First: m}
}

func (d *Deduper) Restore(st DeduperState) {
	d.first = make(map[string]int, len(st.First))
	for k, v := range st.First {
		d.first[k] = v
	}
}

func formatHex(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = digits[v>>4]
		out[i*2+1] = digits[v&0x0f]
	}
	return string(out)
}
