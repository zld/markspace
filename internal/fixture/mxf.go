package fixture

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

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
	data = appendFieldKLVs(data, source.MXFKeyContainer, spec.ContainerFields)
	data = append(data, klv(source.MXFKeyTrackInfo, info)...)
	data = appendFieldKLVs(data, source.MXFKeyDescriptor, spec.DescriptorFields)
	data = appendFieldKLVs(data, source.MXFKeyTrackMeta, spec.TrackFields)
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

func appendFieldKLVs(data []byte, key [16]byte, fields map[string]string) []byte {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		payload, _ := json.Marshal(map[string]string{name: fields[name]})
		data = append(data, klv(key, payload)...)
	}
	return data
}

func klv(key [16]byte, value []byte) []byte {
	out := append([]byte{}, key[:]...)
	// Long-form BER with eight length bytes: valid and easy to locate.
	out = append(out, 0x88)
	out = binary.BigEndian.AppendUint64(out, uint64(len(value)))
	out = append(out, value...)
	return out
}
