package fixture

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"

	"mediaprep/internal/source"
)

type MXFSpec struct {
	Path             string
	TrackID          int
	Role             string
	Codec            string
	SampleRate       int
	Channels         int
	Bits             int
	PCM              []byte
	DescriptorFields map[string]string
	TrackFields      map[string]string
	ContainerFields  map[string]string
	TruncatedKLV     bool
}

func WriteMXF(spec MXFSpec) (string, error) {
	if spec.TrackID == 0 {
		spec.TrackID = 1
	}
	if spec.Role == "" {
		spec.Role = "audio"
	}
	if spec.Codec == "" {
		spec.Codec = "pcm_s16le"
	}
	if spec.SampleRate == 0 {
		spec.SampleRate = 48000
	}
	if spec.Channels == 0 {
		spec.Channels = 1
	}
	if spec.Bits == 0 {
		spec.Bits = 16
	}

	info, err := json.Marshal(map[string]any{
		"id": spec.TrackID, "role": spec.Role, "codec": spec.Codec,
		"sample_rate": spec.SampleRate, "channels": spec.Channels, "bits": spec.Bits,
	})
	if err != nil {
		return "", err
	}
	var data []byte
	data = append(data, klv(source.MXFKeyPartition, []byte("mediaprep-mxf-pcm-subset"))...)
	if fields, err := json.Marshal(spec.ContainerFields); err == nil && len(spec.ContainerFields) > 0 {
		data = append(data, klv(source.MXFKeyContainer, fields)...)
	}
	data = append(data, klv(source.MXFKeyTrackInfo, info)...)
	if len(spec.DescriptorFields) > 0 {
		b, _ := json.Marshal(spec.DescriptorFields)
		data = append(data, klv(source.MXFKeyDescriptor, b)...)
	}
	if len(spec.TrackFields) > 0 {
		b, _ := json.Marshal(spec.TrackFields)
		data = append(data, klv(source.MXFKeyTrackMeta, b)...)
	}
	data = append(data, klv(source.MXFKeyPCM, spec.PCM)...)
	if spec.TruncatedKLV {
		bad := make([]byte, 16)
		copy(bad, source.MXFKeyPCM[:])
		lenHeader := []byte{0x88}
		lenHeader = binary.BigEndian.AppendUint64(lenHeader, 4096)
		data = append(data, bad...)
		data = append(data, lenHeader...)
		data = append(data, make([]byte, 16)...) // physically truncated value
	}
	if err := os.MkdirAll(filepath.Dir(spec.Path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(spec.Path, data, 0o644); err != nil {
		return "", err
	}
	return spec.Path, nil
}

func klv(key [16]byte, value []byte) []byte {
	out := append([]byte{}, key[:]...)
	// Long-form BER with eight length bytes: valid and easy to locate.
	out = append(out, 0x88)
	out = binary.BigEndian.AppendUint64(out, uint64(len(value)))
	out = append(out, value...)
	return out
}
