package bitstream

import (
	"io"
	"strconv"

	"github.com/zld/medcheck/pkg/media"
)

// H.265/HEVC NAL types relevant to keyframe indexing.
const (
	H265TypeTRAILN = 0
	H265TypeIDRWR  = 19
	H265TypeIDRNLP = 20
	H265TypeCRA    = 21
	H265TypeVPS    = 32
	H265TypeSPS    = 33
	H265TypePPS    = 34
	H265TypeAUD    = 35
)

func init() { RegisterParser(CodecH265, scanH265) }

func scanH265(r io.Reader, track string) (*Result, error) {
	return scanAnnexB(r, track, CodecH265, 2, classifyH265)
}

func classifyH265(header []byte, length int64) classifiedNAL {
	if len(header) < 2 {
		return classifiedNAL{kind: "empty", issueCat: media.CatTruncatedNAL, issueMsg: "missing H.265 NAL header"}
	}
	if header[0]&0x80 != 0 {
		return classifiedNAL{kind: "reserved", issueCat: media.CatIllegalLength, issueMsg: "H.265 forbidden_zero_bit is set"}
	}
	nalType := (header[0] >> 1) & 0x3f
	name := h265TypeName(nalType)
	key := nalType == H265TypeIDRWR || nalType == H265TypeIDRNLP || nalType == H265TypeCRA
	if length <= 2 && nalType < 32 {
		return classifiedNAL{kind: name, issueCat: media.CatTruncatedNAL, issueMsg: "H.265 VCL NAL has no slice payload"}
	}
	return classifiedNAL{kind: name, keyframe: key}
}

func h265TypeName(t byte) string {
	switch t {
	case H265TypeTRAILN:
		return "trail_n"
	case 1:
		return "trail_r"
	case H265TypeIDRWR:
		return "idr_w_radl"
	case H265TypeIDRNLP:
		return "idr_n_lp"
	case H265TypeCRA:
		return "cra"
	case H265TypeVPS:
		return "vps"
	case H265TypeSPS:
		return "sps"
	case H265TypePPS:
		return "pps"
	case H265TypeAUD:
		return "aud"
	default:
		return "nal_" + strconv.Itoa(int(t))
	}
}
