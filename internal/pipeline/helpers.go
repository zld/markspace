package pipeline

import (
	"encoding/json"
	"os"

	"pwf/internal/ingest"
	"pwf/internal/media"
	"pwf/internal/quality"
)

// provenanceSchema is mixed into the manifest node key.
const provenanceSchema = "pwf-manifest-v1"

// ingestArtifact is the serialized output of an ingest node. It carries
// enough detail to rebuild provenance and measurement after a resume.
type ingestArtifact struct {
	Kind        string                  `json:"kind"`
	Track       string                  `json:"track"`
	Codec       string                  `json:"codec"`
	ClockHz     uint32                  `json:"clock_hz"`
	RTP         *ingest.RTPStreamResult `json:"rtp,omitempty"`
	MP4         *ingest.MP4TrackResult  `json:"mp4,omitempty"`
	Measurement *quality.Measurement    `json:"measurement"`
}

func runIngest(src Source) ([]byte, error) {
	art := ingestArtifact{Kind: string(src.Kind), Track: src.Track, Codec: src.Codec, ClockHz: src.ClockHz}
	switch src.Kind {
	case KindRTP:
		f, err := os.Open(src.Path)
		if err != nil {
			return nil, media.NewError(media.KindCorrupt, media.StageParseRTP,
				"cannot open rtp source", err).WithTrack(src.Track)
		}
		defer f.Close()
		res, err := ingest.StreamRTP(f, src.Track, src.Codec, src.ClockHz)
		if err != nil {
			return nil, err
		}
		art.RTP = res
		m, err := quality.MeasureRTP(res)
		if err != nil {
			return nil, err
		}
		art.Measurement = m
	case KindMP4:
		res, err := ingest.ParseMP4(src.Path, src.Track, src.Codec)
		if err != nil {
			return nil, err
		}
		art.MP4 = res
		m, err := quality.MeasureMP4(res)
		if err != nil {
			return nil, err
		}
		art.Measurement = m
	default:
		return nil, media.NewError(media.KindUsage, media.StagePipeline,
			"unknown source kind "+string(src.Kind), nil).WithTrack(src.Track)
	}
	return json.MarshalIndent(art, "", "  ")
}

func decodeArtifact(raw []byte) (*ingestArtifact, error) {
	var art ingestArtifact
	if err := json.Unmarshal(raw, &art); err != nil {
		return nil, media.NewError(media.KindCheckpoint, media.StagePipeline,
			"ingest artifact malformed", err)
	}
	if art.Measurement == nil {
		return nil, media.NewError(media.KindUndetermined, media.StagePipeline,
			"ingest artifact lacks measurement", nil).WithTrack(art.Track)
	}
	return &art, nil
}
