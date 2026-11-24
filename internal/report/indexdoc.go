package report

import (
	"encoding/json"

	"mediaroute/internal/model"
)

// IndexDocument is the on-disk random-access index. It is a stable, human
// readable projection of the rebuilt index and is verified by the verify
// subcommand alongside the manifest.
type IndexDocument struct {
	Version    int                `json:"version"`
	Name       string             `json:"name"`
	TotalBytes uint64             `json:"total_bytes"`
	Entries    []model.IndexEntry `json:"entries"`
	Boundaries []model.ByteRange  `json:"boundaries"`
}

// BuildIndexDocument projects a rebuilt index into its serialisable form,
// sorting entries per track so repeated runs produce identical bytes.
func BuildIndexDocument(name string, ix *model.Index) (*IndexDocument, error) {
	if ix == nil {
		return nil, model.NewTaskError(model.CategoryCorruptIndex, Stage, "nil index")
	}
	return &IndexDocument{
		Version:    ManifestVersion,
		Name:       name,
		TotalBytes: ix.TotalBytes,
		Entries:    ix.Sorted(),
		Boundaries: append([]model.ByteRange(nil), ix.Boundaries...),
	}, nil
}

// MarshalIndex renders the index document as stable indented JSON.
func MarshalIndex(doc *IndexDocument) ([]byte, error) {
	if doc == nil {
		return nil, model.NewTaskError(model.CategoryInvalidConfig, Stage, "nil index document")
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, model.WrapTaskError(model.CategoryIO, Stage, "marshal index", err)
	}
	return append(b, '\n'), nil
}
