package fixture

import (
	"github.com/abema/go-mp4"
)

const movieTimescale = 48000

type trackPlan struct {
	id        uint32
	entryType string
	timescale uint32
}

var plannableTracks = []trackPlan{
	{id: 1, entryType: "Opus", timescale: 48000},
	{id: 2, entryType: "Opus", timescale: 48000},
	{id: 3, entryType: "sbtt", timescale: 48000},
}

func marshalBox(w *mp4.Writer, box mp4.IImmutableBox) error {
	_, err := mp4.Marshal(w, box, mp4.Context{})
	return err
}

func withBox(w *mp4.Writer, typ mp4.BoxType, body func() error) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
		return err
	}
	if body != nil {
		if err := body(); err != nil {
			return err
		}
	}
	_, err := w.EndBox()
	return err
}

func writeFtyp(w *mp4.Writer) error {
	return withBox(w, mp4.BoxTypeFtyp(), func() error {
		ftyp := &mp4.Ftyp{MajorBrand: [4]byte{'i', 's', 'o', '5'}, MinorVersion: 512}
		ftyp.AddCompatibleBrand([4]byte{'i', 's', 'o', '5'})
		ftyp.AddCompatibleBrand([4]byte{'i', 's', 'o', '6'})
		ftyp.AddCompatibleBrand([4]byte{'m', 'p', '4', '1'})
		return marshalBox(w, ftyp)
	})
}

func identityMatrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}

func writeMoov(w *mp4.Writer, duration uint64) error {
	return withBox(w, mp4.BoxTypeMoov(), func() error {
		if err := withBox(w, mp4.BoxTypeMvhd(), func() error {
			return marshalBox(w, &mp4.Mvhd{
				FullBox:     mp4.FullBox{Version: 0},
				Timescale:   movieTimescale,
				DurationV0:  uint32(duration),
				Rate:        0x00010000,
				Volume:      0x0100,
				Matrix:      identityMatrix(),
				NextTrackID: uint32(len(plannableTracks) + 1),
			})
		}); err != nil {
			return err
		}
		for _, tp := range plannableTracks {
			if err := writeTrak(w, tp); err != nil {
				return err
			}
		}
		return withBox(w, mp4.BoxTypeMvex(), func() error {
			for _, tp := range plannableTracks {
				if err := withBox(w, mp4.BoxTypeTrex(), func() error {
					return marshalBox(w, &mp4.Trex{
						FullBox:                       mp4.FullBox{Version: 0},
						TrackID:                       tp.id,
						DefaultSampleDescriptionIndex: 1,
					})
				}); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

func writeTrak(w *mp4.Writer, tp trackPlan) error {
	handler := [4]byte{'s', 'o', 'u', 'n'}
	name := "SoundHandler"
	if tp.entryType == "sbtt" {
		handler = [4]byte{'s', 'u', 'b', 't'}
		name = "SubtitleHandler"
	}
	return withBox(w, mp4.BoxTypeTrak(), func() error {
		if err := withBox(w, mp4.BoxTypeTkhd(), func() error {
			return marshalBox(w, &mp4.Tkhd{
				FullBox: mp4.FullBox{Version: 0, Flags: [3]byte{0, 0, 0x07}},
				TrackID: tp.id,
				Matrix:  identityMatrix(),
				Volume:  0x0100,
				Width:   0,
				Height:  0,
			})
		}); err != nil {
			return err
		}
		return withBox(w, mp4.BoxTypeMdia(), func() error {
			if err := withBox(w, mp4.BoxTypeMdhd(), func() error {
				return marshalBox(w, &mp4.Mdhd{
					FullBox:   mp4.FullBox{Version: 0},
					Timescale: tp.timescale,
					Language:  [3]byte{'u', 'n', 'd'},
				})
			}); err != nil {
				return err
			}
			if err := withBox(w, mp4.BoxTypeHdlr(), func() error {
				return marshalBox(w, &mp4.Hdlr{
					FullBox:     mp4.FullBox{Version: 0},
					PreDefined:  0,
					HandlerType: handler,
					Name:        name,
				})
			}); err != nil {
				return err
			}
			return withBox(w, mp4.BoxTypeMinf(), func() error {
				return withBox(w, mp4.BoxTypeStbl(), func() error {
					if err := writeStsd(w, tp); err != nil {
						return err
					}
					if err := withBox(w, mp4.BoxTypeStts(), func() error {
						return marshalBox(w, &mp4.Stts{FullBox: mp4.FullBox{Version: 0}})
					}); err != nil {
						return err
					}
					if err := withBox(w, mp4.BoxTypeStsc(), func() error {
						return marshalBox(w, &mp4.Stsc{FullBox: mp4.FullBox{Version: 0}})
					}); err != nil {
						return err
					}
					if err := withBox(w, mp4.BoxTypeStsz(), func() error {
						return marshalBox(w, &mp4.Stsz{FullBox: mp4.FullBox{Version: 0}})
					}); err != nil {
						return err
					}
					return withBox(w, mp4.BoxTypeStco(), func() error {
						return marshalBox(w, &mp4.Stco{FullBox: mp4.FullBox{Version: 0}})
					})
				})
			})
		})
	})
}

// writeStsd writes a single raw sample entry. Rendering the entry by hand
// keeps the fixture independent from the library's encoder and makes the
// malformed cases explicit.
func writeStsd(w *mp4.Writer, tp trackPlan) error {
	return withBox(w, mp4.BoxTypeStsd(), func() error {
		if err := marshalBox(w, &mp4.Stsd{FullBox: mp4.FullBox{Version: 0}, EntryCount: 1}); err != nil {
			return err
		}
		typ := mp4.StrToBoxType(tp.entryType)
		if _, err := w.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
			return err
		}
		entry := make([]byte, 0, 28)
		entry = append(entry, make([]byte, 6)...) // reserved
		entry = append(entry, 0, 1)               // data_reference_index
		entry = append(entry, 0, 0)               // sound version
		entry = append(entry, make([]byte, 6)...) // reserved
		entry = append(entry, 0, 2)               // channel count
		entry = append(entry, 0, 16)              // sample size
		entry = append(entry, 0, 0, 0, 0)         // pre-defined + reserved
		entry = append(entry, byte(movieTimescale>>16), byte(movieTimescale>>8))
		if _, err := w.Write(entry); err != nil {
			return err
		}
		if tp.entryType == "Opus" {
			if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDOps()}); err != nil {
				return err
			}
			ops := []byte{0, 2, 0x01, 0x38, 0, 0, 0xbb, 0x80, 0, 0, 0}
			if _, err := w.Write(ops); err != nil {
				return err
			}
			if _, err := w.EndBox(); err != nil {
				return err
			}
		}
		_, err := w.EndBox()
		return err
	})
}
