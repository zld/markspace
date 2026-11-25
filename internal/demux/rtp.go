package demux

import (
	"encoding/binary"
	"os"

	"github.com/pion/rtp"

	"mediacore/internal/clock"
	"mediacore/internal/model"
)

// RTPTrackID is the single track a raw RTP dump is demuxed into.
const RTPTrackID = "rtp:0"

// rtpTimescale is the RTP clock rate assumed for dump files (90 kHz video).
const rtpTimescale = 90000

// DemuxRTP reads a length-prefixed RTP packet dump (uint16 BE length +
// packet bytes), extends 32-bit timestamps and 16-bit sequence numbers
// across wrap-around, and emits media events on the unified scale.
func DemuxRTP(path string, opts Options) ([]model.Track, []model.Event, error) {
	opts = opts.defaults()
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, model.NewError(model.ErrIO, model.StageDemux, "", -1, "open: %v", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, nil, model.NewError(model.ErrIO, model.StageDemux, "", -1, "stat: %v", err)
	}
	tracks := []model.Track{{ID: RTPTrackID, Kind: model.TrackVideo, Timescale: rtpTimescale, Handler: "rtp"}}
	var events []model.Event
	var prevTS, prevSeq int64 = -1, -1
	var off int64
	seq := 0
	for off < fi.Size() {
		if off+2 > fi.Size() {
			return nil, nil, model.NewError(model.ErrTruncated, model.StageDemux, RTPTrackID, off, "record length truncated")
		}
		var lb [2]byte
		if _, err := f.ReadAt(lb[:], off); err != nil {
			return nil, nil, model.NewError(model.ErrIO, model.StageDemux, RTPTrackID, off, "read: %v", err)
		}
		n := int64(binary.BigEndian.Uint16(lb[:]))
		if n == 0 || n > int64(opts.MaxSampleSize) {
			return nil, nil, model.NewError(model.ErrBadLength, model.StageDemux, RTPTrackID, off, "record length %d", n)
		}
		if off+2+n > fi.Size() {
			return nil, nil, model.NewError(model.ErrTruncated, model.StageDemux, RTPTrackID, off,
				"packet body beyond file size %d", fi.Size())
		}
		buf := make([]byte, n)
		if _, err := f.ReadAt(buf, off+2); err != nil {
			return nil, nil, model.NewError(model.ErrIO, model.StageDemux, RTPTrackID, off, "read: %v", err)
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(buf); err != nil {
			return nil, nil, model.NewError(model.ErrUnsupported, model.StageDemux, RTPTrackID, off, "rtp: %v", err)
		}
		prevTS = clock.ExtendRTP(prevTS, pkt.Timestamp)
		prevSeq = clock.ExtendRTPSeq(prevSeq, pkt.SequenceNumber)
		payload := make([]byte, len(pkt.Payload))
		copy(payload, pkt.Payload)
		events = append(events, model.Event{
			TrackID: RTPTrackID, Kind: model.EventMedia,
			Ticks:     clock.ConvertSigned(prevTS, rtpTimescale, opts.Timescale),
			Duration:  0,
			SrcOffset: off, Size: uint32(len(payload)),
			Seq: seq, Payload: payload,
		})
		seq++
		off += 2 + n
	}
	return tracks, events, nil
}
