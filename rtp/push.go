package rtp

import (
	"fmt"

	"mediarc/model"
)

// Push admits one arrival and returns everything it made deliverable. A
// returned error means the packet itself was rejected (late, duplicate or
// invalid); frames freed by that rejection are still returned.
func (w *Window) Push(a Arrival) (Release, error) {
	if a.Packet == nil {
		w.mu.Lock()
		w.stats.Arrived++
		w.stats.Invalid++
		w.mu.Unlock()
		return Release{}, model.New(model.CodeInvalidRTP, stream, "nil packet")
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	w.stats.Arrived++
	seq := w.unwrap(a.Packet.SequenceNumber)
	// Duplicate detection wins over lateness: replaying an already delivered
	// packet must be a no-op with a stable duplicate code, not a late error.
	if _, dup := w.seen[seq]; dup {
		w.stats.Duplicates++
		return Release{}, model.New(model.CodeDuplicatePacket, stream,
			fmt.Sprintf("sequence %d was already delivered", seq))
	}
	if w.started && seq < w.floor {
		w.stats.Late++
		return Release{}, model.New(model.CodeLatePacket, stream,
			fmt.Sprintf("sequence %d is behind window floor %d", seq, w.floor))
	}

	release := Release{}
	frame, errDetail, jumpDetail := w.place(a.Packet, seq, a.ReceivedNS)
	if frame == nil {
		w.stats.Invalid++
		// The position is real but unusable; keep the timeline continuous by
		// declaring it lost instead of shifting later packets.
		if !w.started {
			w.started = true
			w.floor = seq
			w.highest = seq
		} else if seq > w.highest {
			w.highest = seq
		}
		if seq >= w.floor {
			w.gapPending[seq] = Gap{StartSeq: uint64(seq), Count: 1, DurationNS: w.cfg.PacketDurationNS}
		}
		w.bumpResident()
		w.drainLocked(&release)
		return release, model.New(model.CodeInvalidRTP, stream, errDetail)
	}
	if jumpDetail != "" {
		release.Issues = append(release.Issues, model.New(model.CodeTimestampJump, stream, jumpDetail))
	}

	if !w.started {
		w.started = true
		w.floor = seq
		w.highest = seq
	}
	if seq > w.highest {
		w.highest = seq
	}
	w.rememberLocked(seq)
	w.pending[seq] = frame
	if frame.OutOfOrder {
		w.stats.Reordered++
	} else {
		w.stats.Accepted++
	}
	w.drainLocked(&release)
	w.bumpResident()
	return release, nil
}

// rememberLocked records a delivered sequence number, pruning the oldest when
// the bounded history is full.
func (w *Window) rememberLocked(seq int64) {
	if _, ok := w.seen[seq]; ok {
		return
	}
	w.seen[seq] = struct{}{}
	w.seenOrder = append(w.seenOrder, seq)
	limit := w.cfg.duplicateMemory()
	for len(w.seenOrder) > limit {
		oldest := w.seenOrder[0]
		w.seenOrder = w.seenOrder[1:]
		delete(w.seen, oldest)
	}
}

func (w *Window) bumpResident() {
	if n := len(w.pending) + len(w.gapPending); n > w.maxResident {
		w.maxResident = n
	}
}
