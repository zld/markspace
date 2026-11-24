package adapter_test

import (
	"bytes"
	"testing"

	"medpipe/internal/adapter"
	"medpipe/internal/adapter/mp4v1"
	"medpipe/internal/clock"
)

func TestRegistryRejectsUnsupportedMajorVersion(t *testing.T) {
	_, err := adapter.Get(404)
	if err == nil {
		t.Fatal("expected unsupported parser major error")
	}
	if code := clock.CodeOf(err); code != clock.CodeUnsupportedParserMajor {
		t.Fatalf("code=%s want %s", code, clock.CodeUnsupportedParserMajor)
	}
}

func TestMP4V1DecoderClassifiesMissingFieldsAndIllegalLength(t *testing.T) {
	d := mp4v1.Decoder{}

	mdatOnly := []byte{0, 0, 0, 12, 'm', 'd', 'a', 't', 1, 2, 3, 4}
	_, err := d.Decode(bytes.NewReader(mdatOnly), &clock.Location{ByteOffset: 7})
	if code := clock.CodeOf(err); code != clock.CodeMissingField {
		t.Fatalf("mdat-only code=%s err=%v want MISSING_FIELD", code, err)
	}

	illegal := []byte{0, 0, 0, 100, 'f', 't', 'y', 'p', 'x'}
	_, err = d.Decode(bytes.NewReader(illegal), &clock.Location{ByteOffset: 9})
	if code := clock.CodeOf(err); code != clock.CodeInvalidPacket {
		t.Fatalf("illegal length code=%s err=%v want INVALID_PACKET", code, err)
	}
	if ce, ok := clock.AsError(err); !ok || ce.Location == nil || ce.Location.ByteOffset != 9 {
		t.Fatalf("adapter error is not locatable: %+v", err)
	}

	data, muxErr := mp4v1.EncodeSegment([]byte(`{"hello":"segment"}`))
	if muxErr != nil {
		t.Fatal(muxErr)
	}
	doc, err := d.Decode(bytes.NewReader(data), nil)
	if err != nil {
		t.Fatalf("encoded MP4 should decode via go-mp4: %v", err)
	}
	if doc.ParserMajor != 1 || len(doc.Boxes) < 2 {
		t.Fatalf("unexpected document: %+v", doc)
	}
}
