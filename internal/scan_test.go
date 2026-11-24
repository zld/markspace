package medpipe

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// Test-local thin wrappers around the production capture builder keep the
// scanner tests terse while sharing the real wire-format implementation.
type captureBuilder = CaptureBuilder

func newCapture() *captureBuilder { return NewCaptureBuilder() }

func h264KeyFrame() []byte { return []byte{0x65, 0xAA, 0xBB} } // type 5 IDR
func h264PFrame() []byte   { return []byte{0x41, 0xCC} }       // type 1 non-IDR
func h265KeyFrame() []byte { return []byte{(20 << 1) | 0, 0x01, 0xDD} }
func h265PFrame() []byte   { return []byte{(1 << 1), 0x01, 0xEE} }

func scanOpts(codec Codec) ScanOptions {
	return ScanOptions{Track: "v", Codec: codec, Kind: KindVideo, ClockRate: 90000, ExpectedDelta: 3000}
}

// scanBytes runs a full capture scan including magic verification.
func scanBytes(t *testing.T, raw []byte, opts ScanOptions) (ScanResult, error) {
	t.Helper()
	s, errs := NewCaptureScanner(bytes.NewReader(raw), opts)
	if len(errs) > 0 {
		return ScanResult{Errors: errs}, errs[0]
	}
	return s.Scan()
}

func TestScanH264FramesAndKeyframes(t *testing.T) {
	cb := newCapture().
		Packet(0, 0, true, h264KeyFrame()).
		Packet(1, 3000, true, h264PFrame()).
		Packet(2, 6000, true, h264PFrame())
	res, err := scanBytes(t, cb.Bytes(), scanOpts(CodecH264))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Units) != 3 {
		t.Fatalf("want 3 units, got %d", len(res.Units))
	}
	if !res.Units[0].Frame.Keyframe || res.Units[1].Frame.Keyframe {
		t.Fatal("keyframe classification wrong")
	}
	if res.Units[0].Frame.ByteOffset != 4 { // first record starts after magic
		t.Fatalf("first unit offset = %d", res.Units[0].Frame.ByteOffset)
	}
}

func TestScanH265AndAV1Classification(t *testing.T) {
	cb := newCapture().
		Packet(0, 0, true, h265KeyFrame()).
		Packet(1, 3000, true, h265PFrame())
	res, err := scanBytes(t, cb.Bytes(), scanOpts(CodecH265))
	if err != nil || !res.Units[0].Frame.Keyframe || res.Units[1].Frame.Keyframe {
		t.Fatalf("h265 classification failed: %v %+v", err, res.Units)
	}
	// AV1 OBU frame header byte: show_existing=0, frame_type=0 => key.
	keyOBU := []byte{0x30, 0x00, 0x11} // type 6 frame, payload starts 0x00
	pOBU := []byte{0x30, 0x02, 0x22}   // frame_type=1
	cb = newCapture().
		Packet(0, 0, true, keyOBU).
		Packet(1, 3000, true, pOBU)
	res, err = scanBytes(t, cb.Bytes(), scanOpts(CodecAV1))
	if err != nil || !res.Units[0].Frame.Keyframe || res.Units[1].Frame.Keyframe {
		t.Fatalf("av1 classification failed: %v %+v", err, res.Units)
	}
}

func TestScanFragmentedUnit(t *testing.T) {
	// One access unit split across two RTP packets, marker on the last.
	cb := newCapture().
		Packet(0, 0, false, []byte{0x65, 0xAA}).
		Packet(1, 0, true, []byte{0xBB, 0xCC}).
		Packet(2, 3000, true, h264PFrame())
	res, err := scanBytes(t, cb.Bytes(), scanOpts(CodecH264))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Units) != 2 {
		t.Fatalf("want 2 reassembled units, got %d", len(res.Units))
	}
	if !bytes.Equal(res.Units[0].Frame.RawPayload, []byte{0x65, 0xAA, 0xBB, 0xCC}) {
		t.Fatalf("reassembly wrong: % x", res.Units[0].Frame.RawPayload)
	}
}

func TestScanTruncatedTail(t *testing.T) {
	cb := newCapture().
		Packet(0, 0, true, h264KeyFrame()).
		Packet(1, 3000, true, h264PFrame()).
		Truncate(4) // cut inside the final record
	res, err := scanBytes(t, cb.Bytes(), scanOpts(CodecH264))
	if err != nil {
		t.Fatalf("truncated tail must be recoverable, got %v", err)
	}
	if len(res.Units) != 1 {
		t.Fatalf("truncated unit must not be indexed, got %d units", len(res.Units))
	}
	if len(res.Errors) != 1 || res.Errors[0].Code != CodeTruncatedUnit || !res.Errors[0].OffsetSet {
		t.Fatalf("want one anchored truncation error, got %+v", res.Errors)
	}
}

func TestScanBadMagic(t *testing.T) {
	bad := []byte{'X', 'X', 'X', 'X', 0, 0}
	if errs := VerifyMagic(bytes.NewReader(bad), "v"); len(errs) != 1 || errs[0].Kind != KindFatal {
		t.Fatalf("want fatal magic error, got %+v", errs)
	}
}

func TestScanEmptyStream(t *testing.T) {
	cb := newCapture()
	res, err := scanBytes(t, cb.Bytes(), scanOpts(CodecH264))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Units) != 0 || len(res.Errors) != 1 || res.Errors[0].Code != CodeEmptyStream {
		t.Fatalf("empty stream handling wrong: %+v", res)
	}
}

func TestScanMetadataAttachedToNextFrame(t *testing.T) {
	// Metadata and the coded frame share one RTP timestamp: the marker on
	// the metadata packet must not close the access unit.
	meta := append([]byte{h264NALSEI}, buildMetaPayload(FrameMeta{ColorPrimaries: 9, Quality: 80})...)
	cb := newCapture().
		Packet(0, 0, true, meta).
		Packet(1, 0, true, h264KeyFrame())
	res, err := scanBytes(t, cb.Bytes(), scanOpts(CodecH264))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Units) != 1 {
		t.Fatalf("metadata must not be its own index entry, got %d", len(res.Units))
	}
	f := res.Units[0].Frame
	if !f.HasMeta || f.Meta.ColorPrimaries != 9 || f.Meta.Quality != 80 {
		t.Fatalf("metadata not attached: %+v", f.Meta)
	}
}

func TestScanTimestampRollbackAnchored(t *testing.T) {
	pf := h264PFrame()
	// Known delta: rollback is repaired and output stays monotonic.
	cb := newCapture().
		Packet(0, 0, true, h264KeyFrame()).
		Packet(1, 3000, true, pf).
		Packet(2, 2000, true, pf) // backwards
	res, err := scanBytes(t, cb.Bytes(), scanOpts(CodecH264))
	if err != nil || len(res.Units) != 3 {
		t.Fatalf("seeded-delta rollback should repair, got err=%v units=%d", err, len(res.Units))
	}
	for i := 1; i < len(res.Units); i++ {
		if res.Units[i].Sample.UnifiedUS <= res.Units[i-1].Sample.UnifiedUS {
			t.Fatal("repaired rollback not monotonic")
		}
	}
	// Ambiguity: with no seeded delta, consecutive duplicate timestamps
	// leave no learnable nominal delta and must fail fatally with the byte
	// offset attached.
	opts := scanOpts(CodecH264)
	opts.ExpectedDelta = 0
	cb2 := newCapture().
		Packet(0, 0, true, h264KeyFrame()).
		Packet(1, 3000, true, pf).
		Packet(2, 3000, true, pf) // duplicate before any delta was learned
	_, err = scanBytes(t, cb2.Bytes(), opts)
	var pe *PipelineError
	if !errors.As(err, &pe) || pe.Code != CodeTimestampRepair || !pe.OffsetSet {
		t.Fatalf("want anchored fatal timestamp error, got %v", err)
	}
}

func TestVerifyMagicShortFile(t *testing.T) {
	errs := VerifyMagic(io.LimitReader(bytes.NewReader([]byte{1, 2}), 2), "v")
	if len(errs) != 1 || errs[0].Code != CodeInvalidRTP {
		t.Fatalf("want invalid-capture error, got %+v", errs)
	}
}
