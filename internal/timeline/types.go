package timeline

import "github.com/zld/mediarc/internal/model"

// Event is one sample on the shared corrected timeline.
type Event struct {
	Raw model.RawEvent `json:"raw"`

	UnwrappedTicks int64    `json:"unwrapped_ticks"`
	SourceNS       model.NS `json:"source_ns"`
	TimeNS         model.NS `json:"time_ns"`
	CorrectionNS   model.NS `json:"correction_ns"`
}

// Time returns the shared time and implements the timed-point interface used
// by watermark and cutting helpers.
func (e Event) Time() model.NS { return e.TimeNS }
