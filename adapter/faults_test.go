package adapter

import (
	"bytes"
	"testing"
)

// TestFiveFaultClasses owns the required fault-fixture matrix: truncation,
// illegal length, unknown version/type, out-of-order and timestamp wraparound.
func TestFiveFaultClasses(t *testing.T) {
	t.Run("truncation", func(t *testing.T) {
		mw := NewMetadataWriter()
		mw.WriteRecord(1, 0, kindChapter, 0, "ch", []byte("c"))
		if _, err := metaA().Parse(bytes.NewReader(mw.Bytes()[:5]), Options{}); err == nil {
			t.Fatal("truncated metadata must fail")
		}
		data, err := EncodeMP4(MP4Spec{Timescale: 25, Delta: 1, Sizes: []uint32{16}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mp4A().Parse(bytes.NewReader(data[:len(data)-2]), Options{}); err == nil {
			t.Fatal("truncated mp4 must fail")
		}
	})
	t.Run("illegal_length", func(t *testing.T) {
		mw := NewMetadataWriter()
		mw.WriteRecord(1, 0, kindChapter, 0, "ch", []byte("c"))
		bad := append([]byte(nil), mw.Bytes()[:8]...)
		bad = append(bad, 0, 0, 0, 9, 1, 0, 0, 0) // length 9 < 20 minimum
		if _, err := metaA().Parse(bytes.NewReader(bad), Options{}); err == nil {
			t.Fatal("illegal record length must fail")
		}
	})
	t.Run("unknown_version_and_type", func(t *testing.T) {
		reg := Default()
		if _, err := reg.Get("mp4", Version{Major: 99, Minor: 0}); err == nil {
			t.Fatal("unknown major version must be rejected")
		}
		mw := NewMetadataWriter()
		mw.WriteRecord(1, 0, 130, 0, "weird", []byte("x")) // known-range but unmapped kind
		if _, err := metaA().Parse(bytes.NewReader(mw.Bytes()), Options{}); err == nil {
			t.Fatal("unmapped kind code must be rejected")
		}
	})
	t.Run("out_of_order", func(t *testing.T) {
		w := NewRTPWriter()
		_ = w.WritePacket(NewRTPPacket(1, 5, 0, false, 16))
		_ = w.WritePacket(NewRTPPacket(1, 3, 0, false, 16))
		if _, err := rtpA().Parse(bytes.NewReader(w.Bytes()), Options{}); err == nil {
			t.Fatal("unrecoverable reordering (3 after 5) must fail")
		}
	})
	t.Run("timestamp_wraparound", func(t *testing.T) {
		w := NewRTPWriter()
		_ = w.WritePacket(NewRTPPacket(1, 0, u32sub(0, 3600), false, 16))
		_ = w.WritePacket(NewRTPPacket(1, 1, 0, false, 16))
		res, err := rtpA().Parse(bytes.NewReader(w.Bytes()), Options{Timescale: 90000})
		if err != nil {
			t.Fatal(err)
		}
		if err := normalizeTracks(res.Tracks); err != nil {
			t.Fatalf("wraparound must normalize: %v", err)
		}
		if res.Tracks[0].Events[1].TimeNS <= res.Tracks[0].Events[0].TimeNS {
			t.Fatal("unwrapped time must be monotonic after wrap")
		}
	})
}
