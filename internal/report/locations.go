package report

import "mediarc/internal/model"

// LocationsFromFaults converts the unified fault model into printable,
// stable error locations.
func LocationsFromFaults(faults []model.Fault) []Location {
	out := make([]Location, 0, len(faults))
	for _, f := range faults {
		out = append(out, Location{
			Kind:        string(f.Kind),
			MaterialID:  f.MaterialID,
			RecordIndex: f.RecordIndex,
			Offset:      f.Offset,
			Recoverable: f.Recoverable,
			Recovered:   f.Recovered,
		})
	}
	return out
}
