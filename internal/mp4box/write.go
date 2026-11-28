// Package mp4box wraps github.com/abema/go-mp4 for the small subset avpipe
// needs: a flat MP4 container made of ftyp, a custom "avpi" JSON descriptor
// box and a moov/mvhd time box. It is deliberately not a general MP4 toolkit.
package mp4box

import (
	"encoding/json"
	"errors"
	"io"

	"avpipe/internal/model"
	"github.com/abema/go-mp4"
)

// Write serializes desc as the avpi box payload and lays down ftyp + avpi +
// moov(mvhd) into w. The mvhd timescale/duration are real MP4 time fields that
// the scanner parses to cross-check the descriptor timeline.
func Write(w io.WriteSeeker, desc model.CaseDesc, timescale, duration uint32) error {
	payload, err := json.Marshal(desc)
	if err != nil {
		return errors.New("marshal descriptor: " + err.Error())
	}
	mw := mp4.NewWriter(w)

	// ftyp: brand "avpi", minor version 0, one compatible brand.
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	brand := mp4.StrToBoxType(model.AVPIBox)
	ftyp := &mp4.Ftyp{MajorBrand: brand, MinorVersion: 0}
	ftyp.CompatibleBrands = []mp4.CompatibleBrandElem{{CompatibleBrand: brand}}
	if _, err := mp4.Marshal(mw, ftyp, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}

	// avpi: opaque JSON payload written as a custom box.
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType(model.AVPIBox)}); err != nil {
		return err
	}
	if _, err := mw.Write(payload); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}

	// moov > mvhd: a genuine time box with timescale and duration.
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return err
	}
	mvhd := &mp4.Mvhd{Timescale: timescale, DurationV0: duration, Rate: 0x00010000, Volume: 0x0100}
	if _, err := mp4.Marshal(mw, mvhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil { // mvhd
		return err
	}
	if _, err := mw.EndBox(); err != nil { // moov
		return err
	}
	return nil
}
