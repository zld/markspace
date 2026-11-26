package adapter

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/pion/rtp"

	"continuity/internal/core"
	"continuity/internal/fixture"
)

func TestRegistryVersioning(t *testing.T) {
	if err := RegisterV1("fake2", fakeParser{version: 2}); err == nil {
		t.Fatal("expected version rejection")
	}
	if err := RegisterV1("fake1", fakeParser{version: 1}); err != nil {
		t.Fatal(err)
	}
	if err := RegisterV1("fake1", fakeParser{version: 1}); err == nil {
		t.Fatal("expected duplicate rejection")
	}
	if _, ok := Get("fake1"); !ok {
		t.Fatal("fake1 not registered")
	}
}

type fakeParser struct{ version int }

func (f fakeParser) Version() int { return f.version }

func (f fakeParser) ParseSegment(r io.ReadSeeker, source string) (*core.SegmentInfo, error) {
	return nil, nil
}

func TestMP4ParseSegment(t *testing.T) {
	data, err := fixture.BuildSegment(fixture.SegmentSpec{
		TrackID:   3,
		BaseTime:  1<<33 - 100,
		Durations: []uint32{1500, 1503, 1497},
		Payload:   []byte("payload-bytes"),
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := (MP4Parser{}).ParseSegment(bytes.NewReader(data), "seg.m4s")
	if err != nil {
		t.Fatal(err)
	}
	if info.TrackID != 3 || info.BaseTime != 1<<33-100 {
		t.Fatalf("info: %+v", info)
	}
	if len(info.Samples) != 3 || info.Samples[1].Duration != 1503 {
		t.Fatalf("samples: %+v", info.Samples)
	}
	if info.MoofOffset != 24 {
		t.Fatalf("moof offset=%d, want 24", info.MoofOffset)
	}
	if info.Size != int64(len(data)) {
		t.Fatalf("size=%d", info.Size)
	}
	var zero [32]byte
	if info.Fingerprint == zero {
		t.Fatal("fingerprint not computed")
	}
}

func TestMP4TruncatedMoof(t *testing.T) {
	// ftyp (24 bytes) + moof header declaring 200 bytes, file ends early.
	data, err := fixture.BuildSegment(fixture.SegmentSpec{TrackID: 1, Durations: []uint32{1500}})
	if err != nil {
		t.Fatal(err)
	}
	ftyp := data[:24]
	moofHdr := make([]byte, 8)
	binary.BigEndian.PutUint32(moofHdr[0:4], 200)
	copy(moofHdr[4:8], "moof")
	broken := append(append(append([]byte{}, ftyp...), moofHdr...), 0, 0, 0, 0)
	_, err = (MP4Parser{}).ParseSegment(bytes.NewReader(broken), "seg2.m4s")
	var ce *core.Error
	if !errors.As(err, &ce) {
		t.Fatalf("want core.Error, got %v", err)
	}
	if ce.Code != core.CodeTruncatedBox || ce.Offset != 24 {
		t.Fatalf("err: %+v", ce)
	}
}

func TestRTPParseEvents(t *testing.T) {
	var buf bytes.Buffer
	for i := 0; i < 3; i++ {
		pkt := rtp.Packet{
			Header:  rtp.Header{Version: 2, SequenceNumber: uint16(100 + i), Timestamp: uint32(5000 + i*3000), SSRC: 42},
			Payload: []byte{byte(i)},
		}
		raw, err := pkt.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		var hdr [10]byte
		binary.BigEndian.PutUint64(hdr[0:8], uint64(90000+i*100))
		binary.BigEndian.PutUint16(hdr[8:10], uint16(len(raw)))
		buf.Write(hdr[:])
		buf.Write(raw)
	}
	all := append([]byte(nil), buf.Bytes()...)
	evs, err := RTPParser{}.ParseEvents(bytes.NewReader(all), "events.rtp")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || evs[1].RTPTime != 8000 || evs[2].Seq != 102 || evs[0].SSRC != 42 {
		t.Fatalf("events: %+v", evs)
	}
	// truncated tail
	trunc := all[:len(all)-3]
	if _, err := (RTPParser{}).ParseEvents(bytes.NewReader(trunc), "events.rtp"); err == nil {
		t.Fatal("expected truncation error")
	}
}
