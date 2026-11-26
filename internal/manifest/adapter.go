package manifest

import (
	"encoding/json"

	"mediaprep/internal/model"
	"mediaprep/internal/stream"
)

const (
	Version1 = "v1"
	Version2 = "v2"
)

type Adapter interface {
	Version() string
	Marshal(doc *Document) ([]byte, error)
	Unmarshal(data []byte) (*Document, error)
}

func ForVersion(version string) (Adapter, error) {
	switch version {
	case Version1:
		return V1Adapter{}, nil
	case Version2:
		return V2Adapter{}, nil
	default:
		return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseManifest, "", -1,
			"unsupported manifest version %q", version)
	}
}

// ReadAny detects v1/v2 and adapts either on-disk shape to Document.
func ReadAny(data []byte) (*Document, string, error) {
	var head struct {
		ManifestVersion string `json:"manifest_version"`
		SchemaVersion   string `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, "", model.NewError(model.CodeInvalidMetadata, model.PhaseManifest, "", -1,
			"invalid manifest JSON: %v", err)
	}
	version := head.SchemaVersion
	if version == "" {
		version = head.ManifestVersion
	}
	adapter, err := ForVersion(version)
	if err != nil {
		return nil, "", err
	}
	doc, err := adapter.Unmarshal(data)
	if err != nil {
		return nil, "", err
	}
	return doc, version, nil
}

type V1Adapter struct{}

func (V1Adapter) Version() string { return Version1 }

type v1Wire struct {
	ManifestVersion string            `json:"manifest_version"`
	Root            string            `json:"root"`
	CreatedAt       string            `json:"created_at"`
	Summary         Summary           `json:"summary"`
	Deliverables    []Asset           `json:"deliverables"`
	Cuts            []Segment         `json:"cuts"`
	SilentRanges    []Silence         `json:"silent_ranges"`
	Exceptions      []model.Exception `json:"exceptions"`
	Pipeline        stream.Metrics    `json:"pipeline"`
}

func (V1Adapter) Marshal(doc *Document) ([]byte, error) {
	w := v1Wire{
		ManifestVersion: Version1, Root: doc.Root, CreatedAt: doc.CreatedAt,
		Summary: doc.Summary, Deliverables: doc.Assets, Cuts: doc.Segments,
		SilentRanges: doc.Silences, Exceptions: doc.Exceptions,
		Pipeline: doc.Pipeline,
	}
	return json.MarshalIndent(w, "", "  ")
}

func (V1Adapter) Unmarshal(data []byte) (*Document, error) {
	var w v1Wire
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseManifest, "", -1,
			"invalid v1 manifest: %v", err)
	}
	if w.ManifestVersion != Version1 {
		return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseManifest, "", -1,
			"not a v1 manifest")
	}
	return &Document{
		SchemaVersion: Version1, Root: w.Root, CreatedAt: w.CreatedAt, Summary: w.Summary,
		Assets: w.Deliverables, Segments: w.Cuts, Silences: w.SilentRanges,
		Exceptions: w.Exceptions, Pipeline: w.Pipeline,
	}, nil
}

type V2Adapter struct{}

func (V2Adapter) Version() string { return Version2 }

type v2Wire struct {
	SchemaVersion string `json:"schema_version"`
	Document
}

func (V2Adapter) Marshal(doc *Document) ([]byte, error) {
	doc.SchemaVersion = Version2
	return json.MarshalIndent(doc, "", "  ")
}

func (V2Adapter) Unmarshal(data []byte) (*Document, error) {
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseManifest, "", -1,
			"invalid v2 manifest: %v", err)
	}
	if doc.SchemaVersion != Version2 {
		return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseManifest, "", -1,
			"not a v2 manifest")
	}
	return &doc, nil
}
