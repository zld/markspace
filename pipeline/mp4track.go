package pipeline

import (
	"encoding/binary"

	gomp4 "github.com/abema/go-mp4"
)

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
