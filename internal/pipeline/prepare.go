package pipeline

import (
	"errors"
	"io"
	"os"

	"github.com/pion/rtcp"

	"mediapipe/internal/audio"
	"mediapipe/internal/clock"
	"mediapipe/internal/model"
	"mediapipe/internal/report"
	"mediapipe/internal/stream"
)

// PreparedInput is the deterministic, reordered media timeline for one file.
type PreparedInput struct {
	Header     stream.FileHeader
	Packets    []model.Packet
	Health     stream.Health
	InputSHA   string
	InputBytes int64
}

// PrepareInput hashes and parses the framed stream, applies wrap-aware clock
// expansion, and emits packets through the bounded reorder buffer.
func PrepareInput(inputPath string, reorderWindow int, frameMs int64) (*PreparedInput, error) {
	if frameMs <= 0 {
		frameMs = 20
	}
	sha, size, err := report.FileSHA256(inputPath)
	if err != nil {
		return nil, model.NewError(model.ErrorArgument, -1, "stat input", err)
	}
	f, err := os.Open(inputPath)
	if err != nil {
		return nil, model.NewError(model.ErrorArgument, -1, "open input", err)
	}
	defer f.Close()

	reader, header, err := stream.NewReader(f)
	if err != nil {
		return nil, err
	}
	health := &stream.Health{}
	tl := clock.NewTimeline(header.ClockRate)
	reorder := stream.NewReorder(reorderWindow, health)
	var packets []model.Packet

	stamp := func(in []model.Packet) []model.Packet {
		for i := range in {
			in[i].MediaMs = tl.MediaTime(in[i].ExtTimestamp)
		}
		packets = append(packets, in...)
		return in
	}

	for {
		rec, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if model.IsClass(err, model.ErrorTruncated) {
				health.AddIssue(err)
				break
			}
			return nil, err
		}

		health.CountRecord(rec)
		if rec.CRCError != nil {
			_ = model.WithPacketOffset(rec.CRCError, int64(rec.Header.PacketOffset))
			health.AddIssue(rec.CRCError)
		}
		if rec.ParseError != nil {
			_ = model.WithPacketOffset(rec.ParseError, int64(rec.Header.PacketOffset))
			health.AddIssue(rec.ParseError)
			if rec.Header.Type == stream.RecordRTCP {
				tl.MarkStaleReport()
			}
		}

		switch rec.Header.Type {
		case stream.RecordRTCP:
			for _, rp := range rec.RTCPPackets {
				if sr, ok := rp.(*rtcp.SenderReport); ok {
					ext := tl.ExpandTimestamp(sr.RTPTime)
					tl.ObserveSenderReport(ext, sr)
				}
			}
		case stream.RecordRTP:
			if rec.RTPPacket == nil {
				continue
			}
			pkt := rec.RTPPacket
			extSeq := tl.ExpandSequence(pkt.SequenceNumber)
			extTS := tl.ExpandTimestamp(pkt.Timestamp)
			valid := !rec.Corrupt
			reason := ""
			if rec.Corrupt {
				reason = "frame_crc_or_parse_corrupt"
			}
			if valid {
				if _, _, derr := audio.DecodePayload(pkt.Payload); derr != nil {
					valid = false
					reason = derr.Error()
				}
			}
			mp := model.Packet{
				SourceOffset:  rec.PayloadOffset,
				RecordIndex:   int(rec.Header.PacketOffset),
				Sequence:      pkt.SequenceNumber,
				ExtSequence:   extSeq,
				Timestamp:     pkt.Timestamp,
				ExtTimestamp:  extTS,
				SSRC:          pkt.SSRC,
				Marker:        pkt.Marker,
				Payload:       pkt.Payload,
				Raw:           rec.Payload,
				FrameMs:       frameMs,
				PayloadValid:  valid,
				PayloadReason: reason,
			}
			stamp(reorder.Push(mp))
		}
	}

	stamp(reorder.Flush())
	if tl.AppliedReports == 0 {
		health.RTCPMissing = true
	}
	health.RTCPStaleReports = int64(tl.StaleReports)
	return &PreparedInput{
		Header:     header,
		Packets:    packets,
		Health:     health.Snapshot(),
		InputSHA:   sha,
		InputBytes: size,
	}, nil
}
