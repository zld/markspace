package rtp

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// metaPath locates the sidecar descriptor next to a packet dump.
// stream.rtp -> stream.meta.json
func metaPath(p string) string {
	ext := filepath.Ext(p)
	return strings.TrimSuffix(p, ext) + ".meta.json"
}

func decodeMeta(b []byte) (StreamMeta, error) {
	var m StreamMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return StreamMeta{}, err
	}
	if m.FrameTicks == 0 {
		return StreamMeta{}, errZeroFrameDuration
	}
	if m.Kind == "" {
		m.Kind = "audio"
	}
	return m, nil
}

// EncodeMeta serializes a descriptor for fixture generation.
func EncodeMeta(m StreamMeta) ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}
