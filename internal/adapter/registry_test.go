package adapter

import (
	"errors"
	"testing"

	"medcheck/internal/clock"
)

func TestRegistryRejectsUnknownVersion(t *testing.T) {
	r := NewDefaultRegistry()
	if _, err := r.Get("aac-v9"); err == nil {
		t.Fatal("expected unknown version error")
	} else {
		var me clock.MediaError
		if !errors.As(err, &me) || me.Code != clock.CodeUnknownAdapterVersion {
			t.Fatalf("err=%T %v", err, err)
		}
	}
	if _, err := r.GetAudio(VersionAACV1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetVideo(VersionH264V1); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryRejectsDuplicateVersion(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(NewAACV1()); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(NewAACV1()); err == nil {
		t.Fatal("duplicate registration must fail")
	}
}
