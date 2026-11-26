package main

import (
	"os"

	"replaypipe/internal/fixtures"
	"replaypipe/internal/index"
	"replaypipe/internal/metadata"
)

// roundTripRate re-parses each source and compares canonical digests against
// the first parse. A source that cannot be re-read counts as inconsistent.
func roundTripRate(set *fixtures.Set, es *eventSet, media *index.Media) (int, int) {
	type src struct {
		first []metadata.Event
		again func() ([]metadata.Event, error)
	}
	opts := metadata.ParseOptions{MediaEndNS: media.DurationNS()}
	mediaEnd := media.DurationNS()
	sources := []src{
		{es.SRT, func() ([]metadata.Event, error) {
			f, err := os.Open(set.SRT)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			ev, e := metadata.ParseSRT(f, opts)
			if e != nil {
				return nil, e
			}
			return ev, nil
		}},
		{es.VTT, func() ([]metadata.Event, error) {
			f, err := os.Open(set.VTT)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			ev, e := metadata.ParseWebVTT(f, opts)
			if e != nil {
				return nil, e
			}
			return ev, nil
		}},
		{es.Chapters, func() ([]metadata.Event, error) {
			f, err := os.Open(set.Chapters)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			ev, e := metadata.ParseChapters(f, opts)
			if e != nil {
				return nil, e
			}
			return ev, nil
		}},
		{es.ID3, func() ([]metadata.Event, error) {
			data, err := os.ReadFile(set.ID3)
			if err != nil {
				return nil, err
			}
			ev, e := metadata.ParseID3(data, metadata.ParseOptions{MediaEndNS: mediaEnd, TrackID: 3})
			if e != nil {
				return nil, e
			}
			return ev, nil
		}},
		{es.SEI, func() ([]metadata.Event, error) {
			data, err := os.ReadFile(set.SEI)
			if err != nil {
				return nil, err
			}
			ev, e := metadata.ParseSEI(data, metadata.ParseOptions{MediaEndNS: mediaEnd, TrackID: 1})
			if e != nil {
				return nil, e
			}
			return ev, nil
		}},
		{es.RTP, func() ([]metadata.Event, error) {
			data, err := os.ReadFile(set.RTP)
			if err != nil {
				return nil, err
			}
			sess, e := metadata.ParseRTP(data, fixtures.Timescale, metadata.ParseOptions{MediaEndNS: mediaEnd, TrackID: 1})
			if e != nil {
				return nil, e
			}
			return sess.Events, nil
		}},
	}
	consistent, total := 0, 0
	for _, s := range sources {
		again, err := s.again()
		total += len(s.first)
		if err != nil {
			continue
		}
		if len(again) != len(s.first) {
			continue
		}
		for i := range again {
			if again[i].DigestHex() == s.first[i].DigestHex() {
				consistent++
			}
		}
	}
	return consistent, total
}
