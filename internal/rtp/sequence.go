// Package rtp orders raw RTP datagrams by extended sequence number and
// identifies loss, identical-content duplicates and same-sequence conflicts.
package rtp

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"sort"

	"github.com/example/mediad/internal/model"
	"github.com/pion/rtp"
)

// Anomaly is one ordering problem observed in the input stream.
type Anomaly struct {
	Code     model.Code `json:"code"`
	Sequence uint16     `json:"sequence"`
	ExtSeq   uint64     `json:"extSeq"`
	Offset   int64      `json:"offset"`
	Detail   string     `json:"detail,omitempty"`
}

// Range describes a contiguous interval of lost extended sequence numbers.
type Range struct {
	From uint64 `json:"from"`
	To   uint64 `json:"to"`
}

// Result is the output of ordering: accepted packets in play order plus the
// full anomaly bookkeeping. Conflicted materials keep their accepted packets
// but carry Isolated=true so later stages refuse to process them.
type Result struct {
	Packets     []model.Packet `json:"-"`
	Lost        []Anomaly      `json:"lost"`
	Duplicates  []Anomaly      `json:"duplicates"`
	Conflicts   []Anomaly      `json:"conflicts"`
	LossRanges  []Range        `json:"lossRanges"`
	TotalInput  int            `json:"totalInput"`
	LostCount   int            `json:"lostCount"`
	DupCount    int            `json:"duplicateCount"`
	ConflictSeq []uint16       `json:"conflictSequences"`
	Isolated    bool           `json:"isolated"`
}

type received struct {
	pkt    *rtp.Packet
	offset int64
}

// Order unmarshals and orders RTP datagrams. Each entry in datagrams is one
// wire packet paired with its byte offset in the input source.
func Order(datagrams []struct {
	Data   []byte
	Offset int64
}) (*Result, error) {
	res := &Result{TotalInput: len(datagrams)}
	bySeq := make(map[uint64][]received)
	var first uint16
	haveFirst := false

	for _, dg := range datagrams {
		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(dg.Data); err != nil {
			return nil, model.NewError(model.CodeIllegalPacket, model.StageRTP,
				0, dg.Offset, "rtp unmarshal: "+err.Error())
		}
		if !haveFirst {
			first = pkt.SequenceNumber
			haveFirst = true
		}
		// Anchor every sequence on the first observed packet (extSeq 0) so
		// 16-bit wraps become a monotonic wrap-free counter.
		extSeq := uint64(int64(wrapDelta(first, pkt.SequenceNumber)))
		bySeq[extSeq] = append(bySeq[extSeq], received{pkt: pkt, offset: dg.Offset})
	}

	seqs := make([]uint64, 0, len(bySeq))
	for s := range bySeq {
		seqs = append(seqs, s)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })

	var prev uint64
	havePrev := false
	conflictSet := make(map[uint16]bool)

	for _, s := range seqs {
		group := bySeq[s]
		// Classify multiple receptions of the same sequence number.
		primary := group[0]
		for _, extra := range group[1:] {
			if bytes.Equal(primary.pkt.Payload, extra.pkt.Payload) &&
				primary.pkt.Timestamp == extra.pkt.Timestamp {
				res.Duplicates = append(res.Duplicates, Anomaly{
					Code: model.CodePacketDuplicate, Sequence: primary.pkt.SequenceNumber,
					ExtSeq: s, Offset: extra.offset, Detail: "identical payload re-reception",
				})
			} else {
				res.Conflicts = append(res.Conflicts, Anomaly{
					Code: model.CodeSequenceConflict, Sequence: primary.pkt.SequenceNumber,
					ExtSeq: s, Offset: extra.offset, Detail: "same sequence, different payload",
				})
				conflictSet[primary.pkt.SequenceNumber] = true
			}
		}
		// Gap since the previous distinct sequence means loss.
		if havePrev {
			for missing := prev + 1; missing < s; missing++ {
				res.Lost = append(res.Lost, Anomaly{
					Code: model.CodePacketLost, Sequence: first + uint16(missing),
					ExtSeq: missing, Offset: -1, Detail: "sequence gap",
				})
			}
		}
		prev = s
		havePrev = true
		res.Packets = append(res.Packets, model.Packet{
			Sequence: primary.pkt.SequenceNumber, ExtSeq: s,
			Offset: primary.offset, Timestamp: primary.pkt.Timestamp,
			Payload: append([]byte(nil), primary.pkt.Payload...),
		})
	}

	res.LossRanges = compressRanges(res.Lost)
	res.LostCount = len(res.Lost)
	res.DupCount = len(res.Duplicates)
	for seq := range conflictSet {
		res.ConflictSeq = append(res.ConflictSeq, seq)
	}
	sort.Slice(res.ConflictSeq, func(i, j int) bool { return res.ConflictSeq[i] < res.ConflictSeq[j] })
	res.Isolated = len(res.Conflicts) > 0
	return res, nil
}

// wrapDelta returns the signed shortest distance from a to b in 16-bit
// sequence-number space, so a 65535 -> 0 wrap reads as +1.
func wrapDelta(a, b uint16) int32 {
	d := int32(b) - int32(a)
	if d > (1<<15)-1 {
		d -= 1 << 16
	} else if d < -(1 << 15) {
		d += 1 << 16
	}
	return d
}

func compressRanges(lost []Anomaly) []Range {
	if len(lost) == 0 {
		return nil
	}
	sorted := make([]uint64, len(lost))
	for i, a := range lost {
		sorted[i] = a.ExtSeq
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var out []Range
	start, prev := sorted[0], sorted[0]
	for _, v := range sorted[1:] {
		if v == prev+1 {
			prev = v
			continue
		}
		out = append(out, Range{From: start, To: prev})
		start, prev = v, v
	}
	out = append(out, Range{From: start, To: prev})
	return out
}

// PayloadDigest returns a content digest over the accepted packet payloads
// in play order, used for cache and checkpoint verification.
func PayloadDigest(pkts []model.Packet) [32]byte {
	h := sha256.New()
	var buf [8]byte
	for _, p := range pkts {
		binary.BigEndian.PutUint64(buf[:], p.ExtSeq)
		h.Write(buf[:])
		h.Write(p.Payload)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
