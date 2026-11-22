package model

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorFormattingAndUnwrap(t *testing.T) {
	cause := errors.New("underlying")
	err := WrapError(TrackAudioMain, StageAudio, CodeAudioGapOverflow, true, cause, "gapof %d ms", 40)
	if got := err.Error(); got == "" || !contains(got, "track=a-main") || !contains(got, "stage=audio-recovery") || !contains(got, "recoverable=true") {
		t.Fatalf("unexpected message %q", got)
	}
	if !errors.Is(err, cause) {
		t.Fatal("wrapped cause not reachable with errors.Is")
	}
	pe, ok := AsError(err)
	if !ok || pe.Code != CodeAudioGapOverflow || !IsRecoverable(err) {
		t.Fatalf("classification lost: %+v %v", pe, ok)
	}
}

func TestErrorListOrdering(t *testing.T) {
	var l ErrorList
	l.Addf(TrackVideoMain, StageParse, CodeMalformedPacket, true, "first")
	l.Addf(TrackAudioMain, StageAudio, CodeAudioRate, false, "second")
	l.Addf(TrackSEI, StageMetadata, CodeMissingPayload, true, "third")
	if l.Len() != 3 {
		t.Fatalf("len = %d", l.Len())
	}
	rec := l.Recoverable()
	if len(rec) != 2 || rec[0].Message != "first" || rec[1].Message != "third" {
		t.Fatalf("recoverable order = %+v", rec)
	}
	if un := l.Unrecoverable(); un == nil || un.Message != "second" {
		t.Fatalf("unrecoverable = %+v", un)
	}
	msgs := l.Messages()
	if len(msgs) != 3 || !contains(msgs[0], "first") {
		t.Fatalf("messages = %v", msgs)
	}
}

func TestPlainErrorIsUnrecoverable(t *testing.T) {
	if IsRecoverable(errors.New("plain")) {
		t.Fatal("plain errors must be unrecoverable")
	}
	if !IsRecoverable(nil) {
		t.Fatal("nil error is trivially recoverable")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func ExampleError() {
	err := NewError(TrackEmsg, StageFanin, CodeBoundaryMismatch, false, "layer %d ends at %s", 2, "00:00:04.000")
	fmt.Println(err.Error())
	// Output: track=m-emsg stage=fanin code=segment-boundary-mismatch recoverable=false: layer 2 ends at 00:00:04.000
}
