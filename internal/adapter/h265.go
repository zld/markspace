package adapter

// H265Version is the single supported HEVC parser generation.
const H265Version = "h265-1"

// HEVC NAL unit types (ISO 23008-2 §7.4.2.2).
const (
	h265TypeIDRWRADL = 19
	h265TypeIDRNLP   = 20
	h265TypeVPS      = 32
	h265TypeSPS      = 33
	h265TypePPS      = 34
	h265MaxType      = 63
	h265MaxUnit      = 8 << 20
)

// H265Adapter parses Annex B H.265/HEVC elementary streams.
type H265Adapter struct{}

func init() { Register(H265Adapter{}) }

func (H265Adapter) Codec() string      { return "h265" }
func (H265Adapter) Version() string    { return H265Version }
func (H265Adapter) MaxUnitSize() int64 { return h265MaxUnit }

// NewSplitter returns a fresh streaming NAL splitter.
func (H265Adapter) NewSplitter() Splitter {
	return NewAnnexBScanner(classifyH265, h265MaxUnit)
}

func classifyH265(nal []byte) (typeCode int, isFrame, isKey bool) {
	if len(nal) < 2 {
		return -1, false, false
	}
	t := int((nal[0] >> 1) & 0x3f)
	if t > h265MaxType {
		return t, false, false
	}
	isVCL := t <= 31 // 0..31 are VCL NAL units
	isIDR := t == h265TypeIDRWRADL || t == h265TypeIDRNLP
	return t, isVCL, isIDR
}

// H265ParameterSet reports whether a unit is VPS/SPS/PPS metadata.
func H265ParameterSet(u Unit) bool {
	return u.TypeCode == h265TypeVPS || u.TypeCode == h265TypeSPS || u.TypeCode == h265TypePPS
}
