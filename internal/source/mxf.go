package source

import (
	"encoding/json"
	"fmt"
	"os"

	"mediaprep/internal/model"
)

var (
	mxfULPrefix = [12]byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x53, 0x01, 0x01, 0x0d, 0x01, 0x01, 0x01}

	MXFKeyPartition  = mxfKey("PRT1")
	MXFKeyTrackInfo  = mxfKey("TRK1")
	MXFKeyDescriptor = mxfKey("DSC1")
	MXFKeyTrackMeta  = mxfKey("TMD1")
	MXFKeyContainer  = mxfKey("CMD1")
	MXFKeyPCM        = mxfKey("PCM1")
)

func mxfKey(suffix string) [16]byte {
	var k [16]byte
	copy(k[:12], mxfULPrefix[:])
	copy(k[12:], suffix)
	return k
}

type mxfKLV struct {
	key        [16]byte
	start      int64
	lengthOff  int64
	valueStart int64
	length     int64
	next       int64
}

// readKLV reads one SMPTE-style 16-byte UL + BER length triplet. The returned
// start offset is the first source offset of the KLV and is used in errors.
func readKLV(data []byte, off int64, track string) (mxfKLV, error) {
	k := mxfKLV{start: off}
	if off+16 > int64(len(data)) {
		return k, model.NewError(model.CodeTruncated, model.PhaseParse, track, off,
			"truncated MXF KLV key")
	}
	copy(k.key[:], data[off:off+16])
	p := off + 16
	k.lengthOff = p
	if p >= int64(len(data)) {
		return k, model.NewError(model.CodeTruncated, model.PhaseParse, track, off,
			"missing MXF BER length")
	}
	first := data[p]
	if first < 0x80 {
		k.length = int64(first)
		k.valueStart = p + 1
	} else {
		n := int(first & 0x7f)
		if first == 0x80 {
			return k, model.NewError(model.CodeUnsupportedCodec, model.PhaseParse, track, off,
				"indefinite-length MXF KLV is not supported")
		}
		if n == 0 || n > 8 {
			return k, model.NewError(model.CodeLengthOutOfRange, model.PhaseParse, track, p,
				"invalid MXF BER length width %d", n)
		}
		if p+1+int64(n) > int64(len(data)) {
			return k, model.NewError(model.CodeTruncated, model.PhaseParse, track, off,
				"truncated MXF BER long-form length")
		}
		var length uint64
		for i := 0; i < n; i++ {
			length = length<<8 | uint64(data[p+1+int64(i)])
		}
		if length > uint64(1<<62) {
			return k, model.NewError(model.CodeLengthOutOfRange, model.PhaseParse, track, p,
				"MXF KLV length %d is out of range", length)
		}
		k.length = int64(length)
		k.valueStart = p + 1 + int64(n)
	}
	if k.length < 0 || k.valueStart < 0 || k.valueStart+k.length < k.valueStart {
		return k, model.NewError(model.CodeLengthOutOfRange, model.PhaseParse, track, off,
			"MXF KLV length overflow")
	}
	end := k.valueStart + k.length
	if end > int64(len(data)) {
		return k, model.NewError(model.CodeTruncated, model.PhaseParse, track, off,
			"MXF KLV %s declares %d bytes but source ends at %d", mxfKeyName(k.key), k.length, len(data))
	}
	k.next = end
	return k, nil
}

func mxfKeyName(k [16]byte) string {
	switch k {
	case MXFKeyPartition:
		return "Partition"
	case MXFKeyTrackInfo:
		return "TrackInfo"
	case MXFKeyDescriptor:
		return "DescriptorFields"
	case MXFKeyTrackMeta:
		return "TrackFields"
	case MXFKeyContainer:
		return "ContainerFields"
	case MXFKeyPCM:
		return "PCM"
	default:
		return fmt.Sprintf("UL:% x", k)
	}
}

type mxfTrackInfo struct {
	ID         int    `json:"id"`
	Role       string `json:"role"`
	Codec      string `json:"codec"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
	Bits       int    `json:"bits"`
}

func ParseMXFFile(path string) (*MediaFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, model.NewError(model.CodeLengthOutOfRange, model.PhaseParse, "", -1, "read MXF: %v", err)
	}
	return ParseMXFBytes(data, path)
}

func ParseMXFBytes(data []byte, path string) (*MediaFile, error) {
	file := &MediaFile{Path: path, Format: "mxf", Size: int64(len(data))}
	var track *Track
	off := int64(0)
	for off < int64(len(data)) {
		label := ""
		if track != nil {
			label = track.Label
		}
		klv, err := readKLV(data, off, label)
		if err != nil {
			return nil, err
		}
		value := data[klv.valueStart:klv.next]
		switch klv.key {
		case MXFKeyTrackInfo:
			var info mxfTrackInfo
			if err := json.Unmarshal(value, &info); err != nil {
				return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseParse, label, klv.start,
					"invalid MXF track info: %v", err)
			}
			if info.Codec != "pcm_s16le" {
				return nil, model.NewError(model.CodeUnsupportedCodec, model.PhaseParse,
					fmt.Sprintf("mxf:%d", info.ID), klv.start,
					"MXF essence codec %q is unsupported; only little-endian signed 16-bit PCM is enabled", info.Codec)
			}
			if info.SampleRate <= 0 || info.Channels <= 0 || info.Bits != 16 {
				return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseParse,
					fmt.Sprintf("mxf:%d", info.ID), klv.start, "invalid PCM descriptor")
			}
			track = &Track{
				Index: len(file.Tracks), ID: info.ID, Label: fmt.Sprintf("mxf:%d", info.ID),
				Role: info.Role, Codec: info.Codec,
				Clock: model.NewClock(1, int64(info.SampleRate)),
				PCM:   &PCMFormat{Channels: info.Channels, Bits: info.Bits, SampleRate: info.SampleRate},
			}
			if track.Role == "" {
				track.Role = "audio"
			}
			file.Tracks = append(file.Tracks, track)

		case MXFKeyDescriptor, MXFKeyTrackMeta, MXFKeyContainer:
			scope := ScopeSampleOrDescriptor
			if klv.key == MXFKeyTrackMeta {
				scope = ScopeTrack
			}
			if klv.key == MXFKeyContainer {
				scope = ScopeContainer
			}
			fields, perr := parseFieldMap(value, scope, label, klv.start)
			if perr != nil {
				return nil, perr
			}
			if scope == ScopeContainer {
				file.ContainerFields = append(file.ContainerFields, fields...)
			} else if track != nil {
				track.RawFields = append(track.RawFields, fields...)
			}

		case MXFKeyPCM:
			if track == nil || track.PCM == nil {
				return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseParse, label, klv.start,
					"PCM KLV without preceding PCM track descriptor")
			}
			blockAlign := track.PCM.Channels * track.PCM.Bits / 8
			if len(value)%blockAlign != 0 {
				return nil, model.NewError(model.CodeTruncated, model.PhaseParse, track.Label, klv.valueStart,
					"PCM payload %d bytes is not aligned to block size %d", len(value), blockAlign)
			}
			duration := int64(len(value) / blockAlign)
			payload := append([]byte(nil), value...)
			track.Samples = append(track.Samples, Sample{
				Index: len(track.Samples), Offset: klv.valueStart, Size: len(payload),
				DTS: track.Duration, PTS: track.Duration, Duration: duration,
				KeyFrame: true, Payload: payload,
			})
			track.Duration += duration

		default:
			// Unknown KLV essence/metadata is skipped after validating its
			// length. Unsupported codecs are rejected by their descriptor.
		}
		off = klv.next
	}
	if len(file.Tracks) == 0 {
		return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseParse, "", 0, "MXF contains no tracks")
	}
	// Container-level descriptive metadata is the lowest-priority tier for
	// every track.
	for _, tr := range file.Tracks {
		tr.RawFields = append(tr.RawFields, file.ContainerFields...)
	}
	return file, nil
}

func parseFieldMap(value []byte, scope Scope, track string, offset int64) ([]RawField, error) {
	var values map[string]string
	if err := json.Unmarshal(value, &values); err != nil {
		return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseParse, track, offset,
			"invalid MXF provenance payload: %v", err)
	}
	out := make([]RawField, 0, len(values))
	for name, val := range values {
		out = append(out, RawField{Name: name, Value: val, Scope: scope, Track: track, Offset: offset})
	}
	return out, nil
}
