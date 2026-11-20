package server

import (
	"testing"

	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
)

func validDesc() *description.Session {
	return &description.Session{
		Medias: []*description.Media{{
			Type:    description.MediaTypeVideo,
			Control: "trackID=0",
			Formats: []format.Format{&format.H264{
				PayloadTyp:        96,
				PacketizationMode: 1,
			}},
		}},
	}
}

func TestValidateDescriptionOK(t *testing.T) {
	if _, err := ValidateDescription(validDesc()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateDescriptionFailures(t *testing.T) {
	cases := []struct {
		name string
		desc *description.Session
	}{
		{"nil", nil},
		{"no track", &description.Session{}},
		{
			"duplicate tracks",
			&description.Session{Medias: []*description.Media{
				{Type: description.MediaTypeVideo, Control: "trackID=0", Formats: []format.Format{&format.H264{PayloadTyp: 96, PacketizationMode: 1}}},
				{Type: description.MediaTypeVideo, Control: "trackID=1", Formats: []format.Format{&format.H264{PayloadTyp: 97, PacketizationMode: 1}}},
			}},
		},
		{
			"audio track",
			&description.Session{Medias: []*description.Media{{
				Type: description.MediaTypeAudio, Control: "trackID=0",
				Formats: []format.Format{&format.H264{PayloadTyp: 96, PacketizationMode: 1}},
			}}},
		},
		{
			"missing control url",
			&description.Session{Medias: []*description.Media{{
				Type:    description.MediaTypeVideo,
				Formats: []format.Format{&format.H264{PayloadTyp: 96, PacketizationMode: 1}},
			}}},
		},
		{
			"static payload type",
			&description.Session{Medias: []*description.Media{{
				Type: description.MediaTypeVideo, Control: "trackID=0",
				Formats: []format.Format{&format.H264{PayloadTyp: 95, PacketizationMode: 1}},
			}}},
		},
		{
			"wrong packetization mode",
			&description.Session{Medias: []*description.Media{{
				Type: description.MediaTypeVideo, Control: "trackID=0",
				Formats: []format.Format{&format.H264{PayloadTyp: 96, PacketizationMode: 0}},
			}}},
		},
		{
			"no formats",
			&description.Session{Medias: []*description.Media{{
				Type: description.MediaTypeVideo, Control: "trackID=0",
			}}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ValidateDescription(tc.desc); err == nil {
				t.Fatal("expected validation error, got nil")
			}
		})
	}
}
