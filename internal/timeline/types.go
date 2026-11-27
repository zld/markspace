package timeline

import "regress/internal/model"

// SourceStream is the transport-side projection the timeline needs: one
// metadata-bearing stream with its events. Ingest types are converted into it
// at the wiring layer so this package stays free of transport dependencies.
type SourceStream struct {
	TrackID string
	Kind    model.TrackKind
	Events  []model.Event
}
