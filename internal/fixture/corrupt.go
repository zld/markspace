package fixture

import (
	"errors"
	"os"

	"github.com/abema/go-mp4"
)

// WriteTruncatedMP4 first builds a valid MP4 then cuts it so the final
// mdat payload extends past the file end. The parser must reject it during
// the scan stage and report the sample's byte offset.
func WriteTruncatedMP4(spec MP4Spec, outPath string) error {
	validPath := spec.Path
	if err := WriteMP4(spec); err != nil {
		return err
	}
	data, err := os.ReadFile(validPath)
	if err != nil {
		return err
	}
	// Drop enough tail bytes that at least the last sample is incomplete.
	last := spec.Payload[len(spec.Payload)-1]
	cut := len(data) - (len(last)/2 + 1)
	if cut <= 0 {
		return errors.New("fixture: payload too small to truncate meaningfully")
	}
	return os.WriteFile(outPath, data[:cut], 0o644)
}

// WriteZeroTimescaleMP4 emits an MP4 whose mdhd timescale is zero. The
// normal writer refuses this, so the boxes are marshalled explicitly.
func WriteZeroTimescaleMP4(path string, payload [][]byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := mp4.NewWriter(f)

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand:       [4]byte{'i', 's', 'o', 'm'},
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}}},
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	// Timescale deliberately left at zero to exercise the rejection path.
	if _, err := mp4.Marshal(w, &mp4.Mdhd{DurationV0: 100}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // mdhd
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStts()}); err != nil {
		return err
	}
	durs := make([]mp4.SttsEntry, len(payload))
	for i := range durs {
		durs[i] = mp4.SttsEntry{SampleCount: 1, SampleDelta: 10}
	}
	if _, err := mp4.Marshal(w, &mp4.Stts{EntryCount: uint32(len(durs)), Entries: durs},
		mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // stts
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsc()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsc{
		EntryCount: 1,
		Entries:    []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: uint32(len(payload))}},
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // stsc
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsz()}); err != nil {
		return err
	}
	sizes := make([]uint32, len(payload))
	for i, p := range payload {
		sizes[i] = uint32(len(p))
	}
	if _, err := mp4.Marshal(w, &mp4.Stsz{SampleCount: uint32(len(sizes)), EntrySize: sizes},
		mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // stsz
		return err
	}
	if _, err := w.EndBox(); err != nil { // stbl
		return err
	}
	if _, err := w.EndBox(); err != nil { // minf
		return err
	}
	if _, err := w.EndBox(); err != nil { // trak
		return err
	}
	if _, err := w.EndBox(); err != nil { // moov
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return err
	}
	for _, p := range payload {
		if _, err := w.Write(p); err != nil {
			return err
		}
	}
	_, err = w.EndBox()
	return err
}
