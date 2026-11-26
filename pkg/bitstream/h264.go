package bitstream

import (
	"io"
	"strconv"

	"github.com/zld/medcheck/pkg/media"
)

// H.264 NAL unit types used by the indexer.
const (
	H264TypeNonIDR = 1
	H264TypeIDR    = 5
	H264TypeSEI    = 6
	H264TypeSPS    = 7
	H264TypePPS    = 8
	H264TypeAUD    = 9
)

func init() { RegisterParser(CodecH264, scanH264) }

func scanH264(r io.Reader, track string) (*Result, error) {
	return scanAnnexB(r, track, CodecH264, 1, classifyH264)
}

func classifyH264(header []byte, length int64) classifiedNAL {
	if len(header) < 1 {
		return classifiedNAL{kind: "empty", issueCat: media.CatTruncatedNAL, issueMsg: "missing H.264 NAL header"}
	}
	b := header[0]
	if b&0x80 != 0 {
		return classifiedNAL{kind: "reserved", issueCat: media.CatIllegalLength, issueMsg: "H.264 forbidden_zero_bit is set"}
	}
	nalType := b & 0x1f
	name := h264TypeName(nalType)
	if length <= 1 && (nalType == H264TypeIDR || nalType == H264TypeNonIDR) {
		return classifiedNAL{kind: name, issueCat: media.CatTruncatedNAL, issueMsg: "H.264 VCL NAL has no RBSP payload"}
	}
	return classifiedNAL{kind: name, keyframe: nalType == H264TypeIDR}
}

func h264TypeName(t byte) string {
	switch t {
	case H264TypeNonIDR:
		return "non_idr_slice"
	case 2:
		return "dp_a"
	case 3:
		return "dp_b"
	case 4:
		return "dp_c"
	case H264TypeIDR:
		return "idr_slice"
	case H264TypeSEI:
		return "sei"
	case H264TypeSPS:
		return "sps"
	case H264TypePPS:
		return "pps"
	case H264TypeAUD:
		return "aud"
	default:
		return "nal_" + strconv.Itoa(int(t))
	}
}
