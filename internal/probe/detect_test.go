package probe

import (
	"testing"

	"mediapipeline/internal/fault"
	"mediapipeline/internal/fixture"
)

func TestDetectFormats(t *testing.T) {
	valid := fixture.BuildValid(fixture.Options{})
	res, err := Detect(valid.Data)
	if err != nil || res.Format != FormatMP4 || res.Brand != "isom" {
		t.Fatalf("mp4: %+v %v", res, err)
	}
	ts := fixture.BuildTS()
	if res, err := Detect(ts.Data); err != nil || res.Format != FormatTS {
		t.Fatalf("ts: %+v %v", res, err)
	}
	rtp := fixture.BuildRTP()
	if res, err := Detect(rtp.Data); err != nil || res.Format != FormatRTP || !res.RTPFramed {
		t.Fatalf("rtp: %+v %v", res, err)
	}
}

func TestDetectUnknownIsAnError(t *testing.T) {
	_, err := Detect([]byte("plain text, definitely not media"))
	fe, ok := fault.As(err)
	if !ok || fe.Stage != fault.StageProbe || fe.Operation != fault.OpUnknownFormat {
		t.Fatalf("expected probe fault, got %v", err)
	}
	if _, err := Detect(nil); err == nil {
		t.Fatal("empty input must fail")
	}
}
