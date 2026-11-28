package fixture

import (
	"sort"

	"avpipe/internal/model"
)

// Cases returns the named, fully deterministic fixture descriptors.
func Cases() map[string]model.CaseDesc {
	return map[string]model.CaseDesc{
		"normal":  normalCase(),
		"damaged": damagedCase(),
	}
}

// normalCase: 3 tracks, 6 clip boundaries total, 2 repairable discontinuities
// (one wrap, one source switch). Everything else is a clean boundary.
func normalCase() model.CaseDesc {
	return model.CaseDesc{
		Name:    "normal",
		Damaged: false,
		Tracks: []model.TrackDesc{
			{
				ID: 1, Name: "video-a", ClockRate: 90000, Samples: 8,
				StepTicks: 3000, StartTS: 0,
				Boundaries: []model.BoundarySpec{
					{AfterSeq: 3, Type: model.BWrap},
				},
			},
			{
				ID: 2, Name: "video-b", ClockRate: 90000, Samples: 8,
				StepTicks: 3000, StartTS: 12000,
				Boundaries: []model.BoundarySpec{
					{AfterSeq: 3, Type: model.BSwitch, ExpectedNS: switchExpectedNS(3000, 90000, 4)},
				},
			},
			{
				ID: 3, Name: "audio", ClockRate: 48000, Samples: 8,
				StepTicks: 480, StartTS: 0,
				Boundaries: []model.BoundarySpec{
					{AfterSeq: 2, Type: model.BNone},
					{AfterSeq: 5, Type: model.BNone},
					{AfterSeq: 6, Type: model.BNone},
					{AfterSeq: 4, Type: model.BNone},
				},
			},
		},
	}
}

// damagedCase: adds an illegal (unrepairable) boundary on track 2 and a
// truncated tail block on track 3, while keeping wrap and switch defects.
func damagedCase() model.CaseDesc {
	c := normalCase()
	c.Name = "damaged"
	c.Damaged = true
	for i := range c.Tracks {
		switch c.Tracks[i].ID {
		case 2:
			c.Tracks[i].Boundaries = append(c.Tracks[i].Boundaries,
				model.BoundarySpec{AfterSeq: 5, Type: model.BIllegal})
		case 3:
			c.Tracks[i].Boundaries = append(c.Tracks[i].Boundaries,
				model.BoundarySpec{AfterSeq: 7, Type: model.BTruncate})
		}
	}
	// Keep boundary lists ordered for stable output.
	for i := range c.Tracks {
		sort.Slice(c.Tracks[i].Boundaries, func(a, b int) bool {
			return c.Tracks[i].Boundaries[a].AfterSeq < c.Tracks[i].Boundaries[b].AfterSeq
		})
	}
	return c
}

// switchExpectedNS computes the descriptor-anchored fixed time for a source
// switch landing on sample seq with the given per-sample tick increment.
func switchExpectedNS(stepTicks, rate uint32, seq int) model.TimeNS {
	return model.TicksToNS(stepTicks*uint32(seq), rate)
}
