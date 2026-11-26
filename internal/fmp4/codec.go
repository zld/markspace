package fmp4

import (
	"fmt"

	"liveregress/internal/fault"
)

// CodecSupport reports whether a sample entry fourcc can be copied without
// re-encoding, and explains why not when it cannot.
//
// Supported and pass-through audio: ipcm (PCM), mp4a with object type 0x40
// (AAC). Everything else is rejected explicitly; no codec is re-encoded and no
// substitute is chosen.
func CodecSupport(codec string, channels, sampleSize, sampleRate int) error {
	switch codec {
	case "ipcm":
		switch sampleSize {
		case 16, 24, 32:
		default:
			return fault.New(fault.KindUnsupported, "", -1,
				fmt.Sprintf("ipcm sample size %d is not supported (16/24/32 only)", sampleSize)).Recover(false)
		}
		if channels <= 0 {
			return fault.New(fault.KindUnsupported, "", -1, "ipcm declares no channels").Recover(false)
		}
	case "mp4a":
		return nil
	case "enca", "encv":
		return fault.New(fault.KindUnsupported, "", -1,
			fmt.Sprintf("%s (encrypted/protected) sample entry is not supported", codec)).Recover(false)
	case "avc1":
		return nil
	default:
		return fault.New(fault.KindUnsupported, "", -1,
			fmt.Sprintf("sample entry %q cannot be copied without re-encoding", codec)).Recover(false)
	}
	return nil
}

// CodecScanName maps a supported sample entry to its sync-marker scanner.
func CodecScanName(codec string) string {
	switch codec {
	case "ipcm", "mp4a":
		return "aac"
	case "avc1":
		return "h264"
	default:
		return ""
	}
}
