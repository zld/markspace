package demo

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"

	"mediad/internal/ingest"
	"mediad/internal/manifest"
	"mediad/internal/model"
	"mediad/internal/normalize"
	"mediad/internal/pipeline"
)

// codecConfig returns a minimal codec-specific configuration record for the
// MP4 sample entry. The fixture's synthetic bitstreams only require the record
// to be present and byte-stable.
func codecConfig(codec string) []byte {
	switch codec {
	case ingest.CodecH264:
		// version/profile/compat/level + length-size-minus-one marker.
		return []byte{0x01, 0x42, 0xC0, 0x1E, 0xFF, 0xE1, 0x00, 0x04, 0x67, 0x42, 0xC0, 0x1E}
	case ingest.CodecH265:
		return []byte{0x01, 0x01, 0x60, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	case ingest.CodecAV1:
		// marker=1, version=1, profile=0, level=0; no config OBUs.
		return []byte{0x81, 0x00, 0x00, 0x00}
	default:
		return nil
	}
}

// samplePayload rebuilds length-prefixed sample bytes from the raw stream and
// scanned units. For Annex-B it strips the 3/4-byte start code; for AV1 it
// keeps the OBU bytes (header + LEB128 + payload) and length-prefixes them.
func samplePayload(codec string, stream []byte, units []ingest.AccessUnit) []byte {
	out := make([]byte, 0, len(stream))
	for _, u := range units {
		start := int(u.Offset)
		end := start + int(u.Length)
		if end > len(stream) {
			end = len(stream)
		}
		body := stream[start:end]
		if codec == ingest.CodecH264 || codec == ingest.CodecH265 {
			// Strip a leading 4-byte then 3-byte Annex-B start code.
			if len(body) >= 4 && body[0] == 0 && body[1] == 0 && body[2] == 0 && body[3] == 1 {
				body = body[4:]
			} else if len(body) >= 3 && body[0] == 0 && body[1] == 0 && body[2] == 1 {
				body = body[3:]
			}
		}
		var lb [4]byte
		binary.BigEndian.PutUint32(lb[:], uint32(len(body)))
		out = append(out, lb[:]...)
		out = append(out, body...)
	}
	return out
}

// trackJSON renders a canonical deterministic encoding of the track.
func trackJSON(t *normalize.Track) []byte {
	b, err := json.Marshal(t)
	if err != nil {
		panic(err) // track is always JSON-serializable
	}
	return b
}

// writeJSONBytes serializes v deterministically with a trailing newline.
func writeJSONBytes(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// readTrack loads the track written by the normalize committed stage.
func (p *Processor) readTrack(sourceSHA string) (*normalize.Track, error) {
	path := filepath.Join(p.engine.CommittedDir(sourceSHA, pipeline.StageNormalize), "track.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	t := &normalize.Track{}
	if err := json.Unmarshal(b, t); err != nil {
		return nil, err
	}
	return t, nil
}

// readDigest reads one digest field from the normalize stage digests.json.
func (p *Processor) readDigest(sourceSHA, field string) (string, error) {
	path := filepath.Join(p.engine.CommittedDir(sourceSHA, pipeline.StageNormalize), "digests.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	doc := map[string]string{}
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", err
	}
	v, ok := doc[field]
	if !ok {
		return "", model.New(model.ErrStageState, "", -1, "missing digest field %s", field)
	}
	return v, nil
}

// loadEntryArtifact reads the committed manifest-stage entry.
func (p *Processor) loadEntryArtifact(st *pipeline.State, sourceSHA string) (*manifest.Entry, error) {
	path := filepath.Join(p.engine.CommittedDir(sourceSHA, pipeline.StageManifest), "entry.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	entry := &manifest.Entry{}
	if err := json.Unmarshal(b, entry); err != nil {
		return nil, err
	}
	return entry, nil
}

// modelDigestMismatch builds a structured digest error.
func modelDigestMismatch(recorded, got string) error {
	return model.New(model.ErrDigestMismatch, "", -1, "stream digest changed: recorded %s got %s", recorded, got)
}
