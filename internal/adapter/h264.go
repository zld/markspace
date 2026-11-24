package adapter

// H264Version is the single supported parser generation.
const H264Version = "h264-1"

// H.264 NAL unit types (ISO 14496-10 §7.4.1).
const (
	h264NALIDR  = 5
	h264NALSPS  = 7
	h264NALPPS  = 8
	h264NALMax  = 31
	h264MaxUnit = 8 << 20 // 8 MiB hard cap, rejects pathological length growth
)

// H264Adapter parses Annex B H.264 elementary streams.
type H264Adapter struct{}

func init() { Register(H264Adapter{}) }

func (H264Adapter) Codec() string      { return "h264" }
func (H264Adapter) Version() string    { return H264Version }
func (H264Adapter) MaxUnitSize() int64 { return h264MaxUnit }

// NewSplitter returns a fresh streaming NAL splitter.
func (H264Adapter) NewSplitter() Splitter {
	return NewAnnexBScanner(classifyH264, h264MaxUnit)
}

func classifyH264(nal []byte) (typeCode int, isFrame, isKey bool) {
	t := int(nal[0] & 0x1f)
	if t == 0 || t > h264NALMax {
		// Reserved/unspecified type: still surface the unit, never as a frame.
		return t, false, false
	}
	isVCL := t >= 1 && t <= 5
	return t, isVCL, t == h264NALIDR
}

// H264ParameterSet reports whether a unit is SPS/PPS (non-VCL metadata).
func H264ParameterSet(u Unit) bool {
	return u.TypeCode == h264NALSPS || u.TypeCode == h264NALPPS
}
