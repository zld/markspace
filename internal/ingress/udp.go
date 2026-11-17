package ingress

import (
	"errors"
	"net"
	"time"

	"jbcheck/internal/jitter"
	"jbcheck/internal/metrics"
	"jbcheck/internal/packet"
	"jbcheck/internal/sim"
)

// RunUDP paces events out over loopback UDP from a sender goroutine while
// the receive loop drives the shared controller in real time. It returns
// when the stream tail is reached or the socket goes quiet (counted as a
// timeout), and only fails hard on socket I/O errors.
func RunUDP(conn *net.UDPConn, events []sim.Event, ctrl *jitter.Controller, st *metrics.Stats) error {
	errCh := make(chan error, 1)
	go sendEvents(conn.LocalAddr().(*net.UDPAddr), events, errCh)

	start := time.Now()
	buf := make([]byte, 2048)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
			return err
		}
		n, raddr, err := conn.ReadFromUDP(buf)
		now := time.Since(start).Milliseconds()
		if err != nil {
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				// Check stream end before ticking so the tail is not
				// padded with concealment frames.
				switch {
				case ctrl.Tail(now):
					st.EndReason = "udp_idle_timeout"
					st.Timeouts++
				case st.Input == 0 && now > 5000:
					st.EndReason = "udp_no_data_timeout"
					st.Timeouts++
				default:
					ctrl.Tick(now)
					continue
				}
				break
			}
			return err
		}
		if !raddr.IP.IsLoopback() {
			st.BadPackets++
			continue
		}
		p, err := packet.Decode(buf[:n])
		if err != nil {
			st.BadPackets++
			continue
		}
		pcm, err := packet.DecodePCM(p.Payload)
		if err != nil {
			st.BadPackets++
			continue
		}
		ctrl.Add(p, pcm, now)
		ctrl.Tick(now)
	}
	if serr := <-errCh; serr != nil {
		return serr
	}
	if st.EndReason == "" {
		st.EndReason = "udp_complete"
	}
	return nil
}

// sendEvents transmits the trace in real time relative to its arrival
// timestamps, preserving the generated jitter, reorder and loss pattern.
func sendEvents(addr *net.UDPAddr, events []sim.Event, errCh chan<- error) {
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		errCh <- err
		return
	}
	defer conn.Close()
	start := time.Now()
	for _, ev := range events {
		if d := time.Duration(ev.ArrivalMs)*time.Millisecond - time.Since(start); d > 0 {
			time.Sleep(d)
		}
		if _, err := conn.Write(ev.Data); err != nil {
			errCh <- err
			return
		}
	}
	errCh <- nil
}
