package server

import (
	"fmt"

	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
)

// dynamicPTMin is the first RTP payload type considered dynamic. Types
// below it are statically assigned and must not carry an H.264 video track.
const dynamicPTMin = 96

// ValidatedMedia bundles the single accepted video track and its H.264
// format after SDP validation succeeds.
type ValidatedMedia struct {
	Media  *description.Media
	Format *format.H264
}

// ValidateDescription enforces the ingest SDP policy:
//
//   - exactly one media track, and it must be video,
//   - exactly one format on that track, an H.264 format,
//   - a dynamic payload type (96..127),
//   - packetization mode 1 (non-interleaved), the only mode the fixture uses,
//   - a non-empty track control URL.
//
// A missing track, a duplicated track or any malformed attribute returns an
// error so the caller can reject the RECORD request.
func ValidateDescription(desc *description.Session) (*ValidatedMedia, error) {
	if desc == nil {
		return nil, fmt.Errorf("sdp: empty session description")
	}
	if len(desc.Medias) == 0 {
		return nil, fmt.Errorf("sdp: no media track present")
	}
	if len(desc.Medias) > 1 {
		return nil, fmt.Errorf("sdp: expected exactly one track, got %d", len(desc.Medias))
	}

	media := desc.Medias[0]
	if media.Type != description.MediaTypeVideo {
		return nil, fmt.Errorf("sdp: track is %s, expected video", media.Type)
	}
	if media.Control == "" {
		return nil, fmt.Errorf("sdp: missing track control URL")
	}
	if len(media.Formats) == 0 {
		return nil, fmt.Errorf("sdp: video track carries no format")
	}
	if len(media.Formats) > 1 {
		return nil, fmt.Errorf("sdp: video track must carry one format, got %d", len(media.Formats))
	}

	h264Format, ok := media.Formats[0].(*format.H264)
	if !ok {
		return nil, fmt.Errorf("sdp: expected H264 format, got %T", media.Formats[0])
	}
	if h264Format.PayloadTyp < dynamicPTMin {
		return nil, fmt.Errorf("sdp: payload type %d is static, H264 requires a dynamic type >= %d",
			h264Format.PayloadTyp, dynamicPTMin)
	}
	if h264Format.PacketizationMode != 1 {
		return nil, fmt.Errorf("sdp: unsupported packetization-mode %d, only mode 1 is accepted",
			h264Format.PacketizationMode)
	}

	return &ValidatedMedia{Media: media, Format: h264Format}, nil
}
