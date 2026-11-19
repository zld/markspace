package demux

import "tsinspect/internal/transport"

// buffer stores a packet that cannot yet be attributed to the program. The
// cache is hard-bounded: when it fills, the window restarts and the dropped
// head is reported, so a stream that never delivers a table cannot exhaust
// memory.
func (s *Slicer) buffer(pkt *transport.Packet) {
	if len(s.pending) >= s.maxBuffer {
		s.stats.BufferOverflows++
		s.stats.BufferedDropped += int64(len(s.pending))
		s.stats.NoTableDropped += int64(len(s.pending))
		s.pending = s.pending[:0]
	}
	s.pending = append(s.pending, pkt)
}

func (s *Slicer) dropPending() {
	s.stats.BufferedDropped += int64(len(s.pending))
	s.pending = nil
}

// flushPending writes cached packets that belong to the now-known program.
func (s *Slicer) flushPending() {
	pending := s.pending
	s.pending = nil
	for _, pkt := range pending {
		if pkt.PID != s.pmtPID && !s.streamPIDs[pkt.PID] {
			continue
		}
		if pkt.TEI {
			continue
		}
		// The cached window predates any continuity judgement, so a repeated
		// counter inside it is indistinguishable from a real duplicate; the
		// conservative choice is to keep packet order as received.

		if s.writeErr != nil {
			return
		}
		before := s.stats.PacketsWritten
		s.emit(pkt)
		if s.stats.PacketsWritten > before {
			s.stats.BufferedFlushed++
		}
	}
}
