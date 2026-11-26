package fixture

import (
	"encoding/json"
	"os"
)

type eventLine struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	AtNS int64  `json:"at_ns"`
}

func writeEvents(path string) error {
	events := []eventLine{
		{ID: "fault-open-near", Kind: "playback_stall", AtNS: 1_960_000_000},
		{ID: "fault-open", Kind: "playback_stall", AtNS: 2_000_000_000},
		{ID: "fault-late", Kind: "decode_gap", AtNS: 7_000_000_000},
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}
