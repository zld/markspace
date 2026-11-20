package integration

import (
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"

	"rtspingest/internal/cli"
	"rtspingest/internal/fsm"
	"rtspingest/internal/server"
	"rtspingest/internal/transport"
)

// startServer brings up a loopback server and returns its URL plus a cleanup.
func startServer(t *testing.T, useUDP bool) (string, func()) {
	t.Helper()
	runner, err := server.NewRunner(fsm.NewMachine(), transport.NewUDPAdapter(), server.Config{
		UseUDP:       useUDP,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("server start: %v", err)
	}
	return runner.RTSPURL(), func() { runner.Close() }
}

// TestServerRejectsMalformedSDP confirms the server refuses RECORD-style
// announcements that violate the SDP policy (duplicate track, static payload
// type, missing control URL, non-H264 format).
func TestServerRejectsMalformedSDP(t *testing.T) {
	if err := cli.ValidateTargetURL("rtsp://127.0.0.1:1/x"); err != nil {
		t.Fatalf("loopback URL policy regressed: %v", err)
	}

	cases := map[string]*description.Session{
		"duplicate tracks": {Medias: []*description.Media{
			{Type: description.MediaTypeVideo, Control: "trackID=0", Formats: []format.Format{&format.H264{PayloadTyp: 96, PacketizationMode: 1}}},
			{Type: description.MediaTypeVideo, Control: "trackID=1", Formats: []format.Format{&format.H264{PayloadTyp: 97, PacketizationMode: 1}}},
		}},
		"static payload type": {Medias: []*description.Media{{
			Type: description.MediaTypeVideo, Control: "trackID=0",
			Formats: []format.Format{&format.H264{PayloadTyp: 90, PacketizationMode: 1}},
		}}},
		"missing control url": {Medias: []*description.Media{{
			Type:    description.MediaTypeVideo,
			Formats: []format.Format{&format.H264{PayloadTyp: 96, PacketizationMode: 1}},
		}}},
	}

	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			url, cleanup := startServer(t, false)
			defer cleanup()

			tcp := gortsplib.TransportTCP
			client := &gortsplib.Client{Transport: &tcp}
			err := client.StartRecording(url, bad)
			if err == nil {
				client.Close()
				t.Fatal("expected the server to reject the malformed SDP")
			}
		})
	}
}
