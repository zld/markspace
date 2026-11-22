package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"

	"github.com/abema/go-mp4"

	"mediadist/timeline"
)

// MaxFragmentSamples bounds a single fMP4 fragment so malformed input
// cannot drive unbounded allocation.
const MaxFragmentSamples = 4096

// FragmentSpec describes one fMP4 (CMAF-style) fragment to emit.
type FragmentSpec struct {
	TrackID        uint32
	SequenceNumber uint32
	// BaseDecodeTime is in the track's timescale ticks.
	BaseDecodeTime uint64
	Samples        []FragmentSample
}

// FragmentSample is one sample inside a fragment.
type FragmentSample struct {
	Duration uint32
	Payload  []byte
}

// Fragment is the serialized output plus its content hash.
type Fragment struct {
	Data        []byte
	SHA256      string
	SampleCount int
	Size        int
}

// flags24 packs a 24-bit ISO box flag value into FullBox.Flags.
func flags24(v uint32) [3]byte {
	return [3]byte{byte(v >> 16), byte(v >> 8), byte(v)}
}

// BuildFragment serializes an ftyp + moof + mdat fragment with go-mp4.
// The moof carries mfhd/tfhd/tfdt/trun; mdat carries the exact sample
// payloads. Nothing is synthesized for missing samples.
func BuildFragment(spec FragmentSpec) (*Fragment, error) {
	if spec.TrackID == 0 {
		return nil, timeline.NewError("FRAGMENT_INVALID", "track id must be non-zero", nil)
	}
	if len(spec.Samples) == 0 || len(spec.Samples) > MaxFragmentSamples {
		return nil, timeline.NewError("FRAGMENT_INVALID", "sample count out of range",
			map[string]any{"samples": len(spec.Samples), "limit": MaxFragmentSamples})
	}
	var payload bytes.Buffer
	entries := make([]mp4.TrunEntry, 0, len(spec.Samples))
	for _, s := range spec.Samples {
		if s.Duration == 0 || len(s.Payload) == 0 {
			return nil, timeline.NewError("FRAGMENT_INVALID",
				"sample requires positive duration and payload", nil)
		}
		payload.Write(s.Payload)
		entries = append(entries, mp4.TrunEntry{
			SampleDuration: s.Duration,
			SampleSize:     uint32(len(s.Payload)),
		})
	}

	// Measure the moof size in a throwaway seekable buffer so the final
	// fragment contains exactly one moof with a correct trun DataOffset.
	var probe bytesReadSeeker
	moofSize, err := writeMoof(mp4.NewWriter(&probe), spec, entries, 0)
	if err != nil {
		return nil, err
	}
	dataOffset := int32(moofSize) + 8 // whole moof plus 8-byte mdat header

	var out bytesReadSeeker
	w := mp4.NewWriter(&out)
	if err := writeFtyp(w); err != nil {
		return nil, err
	}
	if _, err := writeMoof(w, spec, entries, dataOffset); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return nil, err
	}
	if _, err := w.Write(payload.Bytes()); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	data := out.Bytes()
	sum := sha256.Sum256(data)
	return &Fragment{
		Data: data, SHA256: hex.EncodeToString(sum[:]),
		SampleCount: len(spec.Samples), Size: len(data),
	}, nil
}

// writeMoof appends one complete moof box (nested boxes closed in
// reverse order) and returns its serialized size.
func writeMoof(w *mp4.Writer, spec FragmentSpec, entries []mp4.TrunEntry, dataOffset int32) (int64, error) {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoof()}); err != nil {
		return 0, err
	}
	mfhd := &mp4.Mfhd{SequenceNumber: spec.SequenceNumber}
	if err := marshalBox(w, mp4.BoxTypeMfhd(), mfhd); err != nil {
		return 0, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTraf()}); err != nil {
		return 0, err
	}
	tfhd := &mp4.Tfhd{
		FullBox: mp4.FullBox{Flags: flags24(mp4.TfhdDefaultBaseIsMoof)},
		TrackID: spec.TrackID,
	}
	if err := marshalBox(w, mp4.BoxTypeTfhd(), tfhd); err != nil {
		return 0, err
	}
	tfdt := &mp4.Tfdt{
		FullBox:               mp4.FullBox{Version: 1},
		BaseMediaDecodeTimeV1: spec.BaseDecodeTime,
	}
	if err := marshalBox(w, mp4.BoxTypeTfdt(), tfdt); err != nil {
		return 0, err
	}
	// ISO/IEC 14496-12 trun flags: data-offset 0x000001,
	// sample-duration 0x000100, sample-size 0x000200.
	trun := &mp4.Trun{
		FullBox:     mp4.FullBox{Flags: flags24(0x000001 | 0x000100 | 0x000200)},
		SampleCount: uint32(len(entries)),
		DataOffset:  dataOffset,
		Entries:     entries,
	}
	if err := marshalBox(w, mp4.BoxTypeTrun(), trun); err != nil {
		return 0, err
	}
	if _, err := w.EndBox(); err != nil { // traf
		return 0, err
	}
	closed, err := w.EndBox() // moof
	if err != nil {
		return 0, err
	}
	return int64(closed.Size), nil
}

func writeFtyp(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	ftyp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'d', 'a', 's', 'h'}},
		},
	}
	if _, err := mp4.Marshal(w, ftyp, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func marshalBox(w *mp4.Writer, boxType mp4.BoxType, v mp4.IImmutableBox) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: boxType}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, v, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

// VerifyFragment parses the fragment and confirms the structural boxes
// and sample count, giving callers a checkable fragment manifest entry.
func VerifyFragment(data []byte) (map[string]int, error) {
	found := map[string]int{}
	var trunCount uint32
	_, err := mp4.ReadBoxStructure(bytes.NewReader(data), func(h *mp4.ReadHandle) (interface{}, error) {
		name := h.BoxInfo.Type.String()
		found[name]++
		switch name {
		case "moof", "traf":
			if _, err := h.Expand(); err != nil {
				return nil, err
			}
		case "trun":
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			if trun, ok := box.(*mp4.Trun); ok {
				trunCount = trun.SampleCount
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, timeline.NewError("FRAGMENT_CORRUPT",
			"fragment structure unreadable", map[string]any{"error": err.Error()})
	}
	for _, want := range []string{"ftyp", "moof", "mdat", "tfdt", "trun"} {
		if found[want] == 0 {
			return nil, timeline.NewError("FRAGMENT_CORRUPT",
				"fragment missing required box", map[string]any{"box": want})
		}
	}
	found["samples"] = int(trunCount)
	return found, nil
}
