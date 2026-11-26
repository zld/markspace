package main

import "cmafrepair/pkg/media/fixture"

// bandwidthFor/resolutionFor/codecsFor look up playlist attributes from the
// fixture's fixed rendition ladder, with safe fallbacks for unknown names.
func bandwidthFor(rep string) int {
	for _, r := range fixture.Reps {
		if r.Name == rep {
			return r.Bandwidth
		}
	}
	return 500000
}

func resolutionFor(rep string) string {
	for _, r := range fixture.Reps {
		if r.Name == rep {
			return r.Resolution
		}
	}
	return ""
}

func codecsFor(rep string) string {
	for _, r := range fixture.Reps {
		if r.Name == rep {
			return r.Codecs
		}
	}
	return "avc1.4d401e"
}
