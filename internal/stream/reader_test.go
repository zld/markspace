package stream

import (
	"bytes"
	"testing"

	errs "mediad/internal/errors"
)

func sc3(b ...byte) []byte { return append([]byte{0, 0, 1}, b...) }

func h264NAL(typ byte, payload int) []byte {
	out := sc3((0x2 << 5) | typ) // nal_ref_idc=2
	for i := 0; i < payload; i++ {
		out = append(out, byte(i))
	}
	return out
}

func hevcNAL(typ byte, payload int) []byte {
	out := sc3(typ<<1, 0x01) // 2-byte NAL header
	for i := 0; i < payload; i++ {
		out = append(out, byte(i))
	}
	return out
}

func av1OBU(typ byte, payload int) []byte {
	out := sc3(typ << 3)
	for i := 0; i < payload; i++ {
		out = append(out, byte(i+1))
	}
	return out
}

func collect(t *testing.T, data []byte, codec Codec, cfg Config) ([]*AccessUnit, *Scanner) {
	t.Helper()
	s, err := NewScanner(bytes.NewReader(data), codec, cfg)
	if err != nil {
		t.Fatalf("NewScanner: %v", err)
	}
	var aus []*AccessUnit
	for {
		au, err := s.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if au == nil {
			break
		}
		aus = append(aus, au)
	}
	return aus, s
}

func TestH264AccessUnitFraming(t *testing.T) {
	var data []byte
	// Two AUD-delimited AUs: IDR then P; a third VCL-only AU without AUD.
	data = append(data, h264NAL(9, 1)...) // AUD
	data = append(data, h264NAL(7, 4)...) // SPS
	data = append(data, h264NAL(5, 8)...) // IDR
	data = append(data, h264NAL(9, 1)...) // AUD
	data = append(data, h264NAL(1, 8)...) // P slice
	data = append(data, h264NAL(1, 8)...) // next AU: VCL boundary

	aus, s := collect(t, data, H264, DefaultConfig())
	if len(aus) != 3 {
		t.Fatalf("got %d AUs, want 3", len(aus))
	}
	if !aus[0].Keyframe || aus[0].Corrupt {
		t.Fatalf("AU0 should be a clean keyframe: %+v", aus[0])
	}
	if aus[1].Keyframe {
		t.Fatal("AU1 must not be a keyframe")
	}
	if len(aus[0].NALs) != 3 {
		t.Fatalf("AU0 NAL count = %d, want 3", len(aus[0].NALs))
	}
	// Every emitted AU must carry half-open consistent byte ranges.
	for _, au := range aus {
		if au.End <= au.Start {
			t.Fatalf("AU %d has invalid range [%d,%d)", au.Index, au.Start, au.End)
		}
	}
	if len(s.Damages()) != 0 || s.TotalRead() != int64(len(data)) {
		t.Fatalf("unexpected damages=%d total=%d", len(s.Damages()), s.TotalRead())
	}
}

func TestHEVCAndAV1Framing(t *testing.T) {
	var h []byte
	h = append(h, hevcNAL(35, 0)...) // AUD
	h = append(h, hevcNAL(33, 4)...) // SPS
	h = append(h, hevcNAL(19, 8)...) // IDR_W_RADL
	h = append(h, hevcNAL(35, 0)...)
	h = append(h, hevcNAL(1, 8)...) // TRAIL_R
	aus, _ := collect(t, h, HEVC, DefaultConfig())
	if len(aus) != 2 || !aus[0].Keyframe || aus[1].Keyframe {
		t.Fatalf("HEVC framing wrong: %+v", aus)
	}

	var a []byte
	a = append(a, av1OBU(OBUTemporalDelim, 0)...)
	a = append(a, av1OBU(OBUSequenceHeader, 8)...) // keyframe marker
	a = append(a, av1OBU(OBUFrame, 8)...)
	a = append(a, av1OBU(OBUTemporalDelim, 0)...)
	a = append(a, av1OBU(OBUFrame, 8)...)
	aus, _ = collect(t, a, AV1, DefaultConfig())
	if len(aus) != 2 || !aus[0].Keyframe {
		t.Fatalf("AV1 framing wrong: %+v", aus)
	}
}

func TestBadAndTruncatedHeaders(t *testing.T) {
	// H.264 forbidden bit set.
	bad := append(sc3(0x80|5), 1, 2, 3)
	bad = append(bad, h264NAL(5, 4)...)
	aus, s := collect(t, bad, H264, DefaultConfig())
	if len(aus) != 1 || !aus[0].Corrupt {
		t.Fatalf("bad-header AU not flagged corrupt: %+v", aus)
	}
	if len(s.Damages()) != 1 || s.Damages()[0].Code != errs.CodeUnsupportedHeader {
		t.Fatalf("damage = %+v", s.Damages())
	}

	// HEVC single-byte header is incomplete (needs 2).
	short := sc3(19 << 1)
	aus, s = collect(t, short, HEVC, DefaultConfig())
	if len(aus) != 1 || !aus[0].NALs[0].BadHeader {
		t.Fatalf("incomplete HEVC header: %+v", aus)
	}

	// Input containing no start code at all is reported at offset zero.
	aus, s = collect(t, []byte{0xAA, 0xBB, 0xCC, 0x00}, H264, DefaultConfig())
	if len(aus) != 0 || len(s.Damages()) != 1 ||
		s.Damages()[0].Code != errs.CodeTruncatedStream || s.Damages()[0].Offset != 0 {
		t.Fatalf("start-code-less input not reported: aus=%+v dmg=%+v", aus, s.Damages())
	}

	// A NAL below its minimum payload length is marked truncated.
	shortNAL := h264NAL(5, 0) // header byte only, IDR requires >= 2
	aus, s = collect(t, shortNAL, H264, DefaultConfig())
	if len(aus) != 1 || !aus[0].NALs[0].Truncated || !aus[0].Corrupt {
		t.Fatalf("short NAL not flagged: %+v", aus)
	}
	found := false
	for _, d := range s.Damages() {
		if d.Code == errs.CodeTruncatedStream {
			found = true
		}
	}
	if !found {
		t.Fatalf("truncated damage missing: %+v", s.Damages())
	}
}

func TestOversizedAUIsCappedAndScanContinues(t *testing.T) {
	cfg := Config{BlockSize: 16, MaxAUBytes: 32, MaxNALBytes: 32}
	var data []byte
	data = append(data, h264NAL(5, 100)...) // oversized
	data = append(data, h264NAL(9, 1)...)
	data = append(data, h264NAL(1, 4)...) // clean second AU
	aus, s := collect(t, data, H264, cfg)
	if len(aus) != 2 {
		t.Fatalf("got %d AUs, want 2", len(aus))
	}
	if !aus[0].Corrupt || !aus[0].NALs[0].Oversized {
		t.Fatalf("oversized NAL not flagged: %+v", aus[0])
	}
	if len(aus[0].NALs[0].Data) > 32 {
		t.Fatalf("payload copy %d exceeds cap", len(aus[0].NALs[0].Data))
	}
	if aus[1].Corrupt {
		t.Fatalf("second AU should be clean: %+v", aus[1])
	}
	if s.TotalRead() != int64(len(data)) {
		t.Fatalf("scanner skipped bytes: %d vs %d", s.TotalRead(), len(data))
	}
}

func TestSmallBlockSizesStillFrame(t *testing.T) {
	// Every block size from 4..12 must produce identical AU counts.
	var data []byte
	for i := 0; i < 6; i++ {
		data = append(data, h264NAL(9, 1)...)
		data = append(data, h264NAL(5, 3)...)
	}
	want := 0
	for bs := 4; bs <= 12; bs++ {
		aus, s := collect(t, data, H264, Config{BlockSize: bs, MaxAUBytes: 1 << 20, MaxNALBytes: 1 << 20})
		if len(s.Damages()) != 0 {
			t.Fatalf("bs=%d damages: %+v", bs, s.Damages())
		}
		if want == 0 {
			want = len(aus)
		} else if len(aus) != want {
			t.Fatalf("bs=%d got %d AUs, want %d", bs, len(aus), want)
		}
	}
}

func TestParseCodecRejectsUnknown(t *testing.T) {
	if _, err := ParseCodec("vp9"); err == nil {
		t.Fatal("unknown codec accepted")
	}
	if c, err := ParseCodec("h265"); err != nil || c != HEVC {
		t.Fatalf("h265 alias: %v %v", c, err)
	}
}
