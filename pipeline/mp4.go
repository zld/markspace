package pipeline

import (
	"encoding/binary"
	"io"

	gomp4 "github.com/abema/go-mp4"
)

// FragmentWriter emits a fragmented MP4 stream: ftyp/moov initialization
// followed by moof/mdat fragments. Container box boundaries (size/type
// headers and nesting) are managed by github.com/abema/go-mp4; the payloads
// are self-generated opaque media samples, so no codec-specific boxes are
// required beyond the structural skeleton.

// trunFlagsPresent requests data-offset, first-sample-flags, sample-duration
// and sample-size fields in every trun box.
const trunFlagsPresent uint32 = 0x000301

type FragmentWriter struct {
	w       *gomp4.Writer
	trackID uint32
}

// NewFragmentWriter opens a fragmented MP4 writer over an io.WriteSeeker.
func NewFragmentWriter(out io.WriteSeeker, trackID uint32) *FragmentWriter {
	if trackID == 0 {
		trackID = 1
	}
	return &FragmentWriter{w: gomp4.NewWriter(out), trackID: trackID}
}

// WriteInitSegment writes the ftyp and a minimal moov with one track and the
// mvex/trex boxes that declare the stream as fragmented.
func (f *FragmentWriter) WriteInitSegment(timescale uint32) error {
	// ftyp
	if _, err := f.w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	ftyp := make([]byte, 0, 16)
	ftyp = appendStr4(ftyp, "iso5")
	ftyp = binary.BigEndian.AppendUint32(ftyp, 512) // minor version
	ftyp = appendStr4(ftyp, "iso5")
	ftyp = appendStr4(ftyp, "dash")
	if _, err := f.w.Write(ftyp); err != nil {
		return err
	}
	if _, err := f.w.EndBox(); err != nil {
		return err
	}

	// moov > mvhd, trak, mvex > trex
	if _, err := f.w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMoov()}); err != nil {
		return err
	}
	if err := f.writeFullBox(gomp4.BoxTypeMvhd(), 0, func(b []byte) []byte {
		b = binary.BigEndian.AppendUint32(b, 0) // creation_time
		b = binary.BigEndian.AppendUint32(b, 0) // modification_time
		b = binary.BigEndian.AppendUint32(b, timescale)
		b = binary.BigEndian.AppendUint32(b, 0) // duration (unknown)
		b = append(b, 0x00, 0x01, 0x00, 0x00)   // rate 1.0
		b = append(b, 0x01, 0x00)               // volume 1.0
		b = append(b, make([]byte, 10)...)      // reserved
		b = append(b, identityMatrix()...)
		b = append(b, make([]byte, 24)...)      // pre-defined
		b = binary.BigEndian.AppendUint32(b, 2) // next_track_ID
		return b
	}); err != nil {
		return err
	}
	if err := f.writeTrack(timescale); err != nil {
		return err
	}
	if _, err := f.w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMvex()}); err != nil {
		return err
	}
	if err := f.writeFullBox(gomp4.BoxTypeTrex(), 0, func(b []byte) []byte {
		b = binary.BigEndian.AppendUint32(b, f.trackID)
		b = binary.BigEndian.AppendUint32(b, 1) // default_sample_description_index
		b = binary.BigEndian.AppendUint32(b, 0) // default_sample_duration
		b = binary.BigEndian.AppendUint32(b, 0) // default_sample_size
		b = binary.BigEndian.AppendUint32(b, 0) // default_sample_flags
		return b
	}); err != nil {
		return err
	}
	if _, err := f.w.EndBox(); err != nil { // mvex
		return err
	}
	if _, err := f.w.EndBox(); err != nil { // moov
		return err
	}
	return nil
}

// WriteFragment writes one moof/mdat pair. samples are concatenated into mdat
// while the trun box records each sample's size and duration.
func (f *FragmentWriter) WriteFragment(seq uint32, samples [][]byte, sampleDuration uint32) error {
	// moof > mfhd, traf > tfhd, trun
	if _, err := f.w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMoof()}); err != nil {
		return err
	}
	if err := f.writeFullBox(gomp4.BoxTypeMfhd(), 0, func(b []byte) []byte {
		return binary.BigEndian.AppendUint32(b, seq) // sequence_number
	}); err != nil {
		return err
	}
	if _, err := f.w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeTraf()}); err != nil {
		return err
	}
	// tfhd flags 0x020000 = default-base-is-moof
	if err := f.writeFullBox(gomp4.BoxTypeTfhd(), 0x020000, func(b []byte) []byte {
		return binary.BigEndian.AppendUint32(b, f.trackID)
	}); err != nil {
		return err
	}
	// trun flags: data-offset (0x1) + first-sample-flags (0x4) +
	// sample-duration (0x100) + sample-size (0x200)
	// Record the file position of the data_offset field for later patching.
	trunOffsetPos, err := f.writeTrun(samples, sampleDuration)
	if err != nil {
		return err
	}
	if _, err := f.w.EndBox(); err != nil { // traf
		return err
	}
	moofEnd, err := f.w.EndBox() // moof
	if err != nil {
		return err
	}
	// mdat with the concatenated sample bytes.
	mdatBox, err := f.w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMdat()})
	if err != nil {
		return err
	}
	for _, s := range samples {
		if _, err := f.w.Write(s); err != nil {
			return err
		}
	}
	if _, err := f.w.EndBox(); err != nil {
		return err
	}
	// Patch the trun data offset: distance from the moof box start to the
	// first mdat payload byte (moof size + mdat 8-byte header).
	dataOffset := uint32(moofEnd.Size) + uint32(mdatBox.HeaderSize)
	cur, err := f.w.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := f.w.Seek(trunOffsetPos, io.SeekStart); err != nil {
		return err
	}
	var ob [4]byte
	binary.BigEndian.PutUint32(ob[:], dataOffset)
	if _, err := f.w.Write(ob[:]); err != nil {
		return err
	}
	if _, err := f.w.Seek(cur, io.SeekStart); err != nil {
		return err
	}
	return nil
}

// writeTrun emits the trun box and returns the file position of its
// data_offset field for post-write patching.
func (f *FragmentWriter) writeTrun(samples [][]byte, sampleDuration uint32) (int64, error) {
	if _, err := f.w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeTrun()}); err != nil {
		return 0, err
	}
	// trun flags: data-offset + first-sample-flags + sample-duration + sample-size.
	head := fullBoxHeader(0, trunFlagsPresent)
	if _, err := f.w.Write(head); err != nil {
		return 0, err
	}
	if _, err := f.w.Write(binary.BigEndian.AppendUint32(nil, uint32(len(samples)))); err != nil {
		return 0, err
	}
	pos, err := f.w.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if _, err := f.w.Write(binary.BigEndian.AppendUint32(nil, 0)); err != nil { // data_offset
		return 0, err
	}
	if _, err := f.w.Write(binary.BigEndian.AppendUint32(nil, 0)); err != nil { // first_sample_flags
		return 0, err
	}
	for _, s := range samples {
		if _, err := f.w.Write(binary.BigEndian.AppendUint32(nil, sampleDuration)); err != nil {
			return 0, err
		}
		if _, err := f.w.Write(binary.BigEndian.AppendUint32(nil, uint32(len(s)))); err != nil {
			return 0, err
		}
	}
	if _, err := f.w.EndBox(); err != nil {
		return 0, err
	}
	return pos, nil
}

// writeTrack emits a minimal trak skeleton sufficient for fragment addressing.
func (f *FragmentWriter) writeTrack(timescale uint32) error {
	if _, err := f.w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if err := f.writeFullBox(gomp4.BoxTypeTkhd(), 0x000007, func(b []byte) []byte {
		b = binary.BigEndian.AppendUint32(b, 0) // creation
		b = binary.BigEndian.AppendUint32(b, 0) // modification
		b = binary.BigEndian.AppendUint32(b, f.trackID)
		b = binary.BigEndian.AppendUint32(b, 0) // reserved
		b = binary.BigEndian.AppendUint32(b, 0) // duration
		b = append(b, make([]byte, 8)...)       // reserved
		b = append(b, 0x00, 0x00)               // layer + alt group
		b = append(b, 0x00, 0x00)               // volume + reserved
		b = append(b, identityMatrix()...)
		b = binary.BigEndian.AppendUint32(b, 320<<16) // width 16.16
		b = binary.BigEndian.AppendUint32(b, 240<<16) // height 16.16
		return b
	}); err != nil {
		return err
	}
	if _, err := f.w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if err := f.writeFullBox(gomp4.BoxTypeMdhd(), 0, func(b []byte) []byte {
		b = binary.BigEndian.AppendUint32(b, 0)
		b = binary.BigEndian.AppendUint32(b, 0)
		b = binary.BigEndian.AppendUint32(b, timescale)
		b = binary.BigEndian.AppendUint32(b, 0)
		b = append(b, 0x55, 0xC4) // language 'und'
		b = binary.BigEndian.AppendUint16(b, 0)
		return b
	}); err != nil {
		return err
	}
	if err := f.writeFullBox(gomp4.BoxTypeHdlr(), 0, func(b []byte) []byte {
		b = binary.BigEndian.AppendUint32(b, 0) // pre_defined
		b = appendStr4(b, "soun")               // handler_type
		b = append(b, make([]byte, 12)...)      // reserved
		return append(b, []byte("SoundHandler\x00")...)
	}); err != nil {
		return err
	}
	if _, err := f.w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if _, err := f.w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeStbl()}); err != nil {
		return err
	}
	// Empty sample tables: all sample metadata lives in the fragments.
	for _, bt := range []gomp4.BoxType{gomp4.BoxTypeStts(), gomp4.BoxTypeStsc(),
		gomp4.BoxTypeStsz(), gomp4.BoxTypeStco()} {
		if err := f.writeEmptyTable(bt); err != nil {
			return err
		}
	}
	if _, err := f.w.EndBox(); err != nil { // stbl
		return err
	}
	if _, err := f.w.EndBox(); err != nil { // minf
		return err
	}
	if _, err := f.w.EndBox(); err != nil { // mdia
		return err
	}
	if _, err := f.w.EndBox(); err != nil { // trak
		return err
	}
	return nil
}

func (f *FragmentWriter) writeEmptyTable(bt gomp4.BoxType) error {
	return f.writeFullBox(bt, 0, func(b []byte) []byte {
		b = binary.BigEndian.AppendUint32(b, 0) // entry_count (stco/stts/stsc)
		if bt == gomp4.BoxTypeStsz() {
			b = binary.BigEndian.AppendUint32(b, 0) // sample_size
			b = binary.BigEndian.AppendUint32(b, 0) // sample_count
		}
		return b
	})
}

// writeFullBox opens a box and writes version(1)+flags(3) then the payload.
func (f *FragmentWriter) writeFullBox(bt gomp4.BoxType, flags uint32, payload func([]byte) []byte) error {
	if _, err := f.w.StartBox(&gomp4.BoxInfo{Type: bt}); err != nil {
		return err
	}
	if _, err := f.w.Write(fullBoxHeader(0, flags)); err != nil {
		return err
	}
	if _, err := f.w.Write(payload(nil)); err != nil {
		return err
	}
	if _, err := f.w.EndBox(); err != nil {
		return err
	}
	return nil
}

// fullBoxHeader encodes the version (1 byte) and 24-bit flags of an ISO box.
func fullBoxHeader(version byte, flags uint32) []byte {
	return []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
}

func appendStr4(b []byte, s string) []byte {
	pad := make([]byte, 4)
	copy(pad, s)
	return append(b, pad...)
}

func identityMatrix() []byte {
	m := make([]byte, 36)
	m[0], m[16], m[32] = 0x00, 0x01, 0x40
	return m
}
