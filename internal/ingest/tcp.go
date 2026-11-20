package ingest

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"jitterlab/internal/wire"
)

// ServeTCP accepts one connection from ln and reads length-prefixed
// frames (uint16 big-endian total packet length, then the wire packet)
// until ctx is cancelled, the peer closes, or a framing error occurs.
// The listener and connection are closed on return.
func ServeTCP(ctx context.Context, ln *net.TCPListener, h Handler) error {
	defer ln.Close()
	type acceptRes struct {
		conn *net.TCPConn
		err  error
	}
	ch := make(chan acceptRes, 1)
	go func() {
		c, err := ln.AcceptTCP()
		ch <- acceptRes{c, err}
	}()
	var conn *net.TCPConn
	select {
	case <-ctx.Done():
		return nil
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		conn = r.conn
	}
	defer conn.Close()
	return readTCPFrames(ctx, conn, h)
}

// readTCPFrames handles split headers/payloads, invalid lengths and
// mid-frame EOF.
func readTCPFrames(ctx context.Context, conn *net.TCPConn, h Handler) error {
	hdr := make([]byte, 2)
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		_ = conn.SetReadDeadline(time.Now().Add(ioTimeout))
		if _, err := readFull(ctx, conn, hdr); err != nil {
			return classifyReadErr(ctx, err)
		}
		n := int(binary.BigEndian.Uint16(hdr))
		if n < wire.HeaderSize || n > wire.MaxPacket {
			h.OnMalformed()
			return fmt.Errorf("ingest: invalid frame length %d", n)
		}
		body := make([]byte, n)
		if _, err := readFull(ctx, conn, body); err != nil {
			return classifyReadErr(ctx, err)
		}
		pkt, err := wire.Decode(body)
		if err != nil {
			h.OnMalformed()
			continue
		}
		h.OnPacket(pkt)
	}
}

// readFull is io.ReadFull with a per-call deadline and ctx checks, so a
// header or payload split across many reads still reassembles.
func readFull(ctx context.Context, conn *net.TCPConn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		_ = conn.SetReadDeadline(time.Now().Add(ioTimeout))
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return total, err
		}
	}
	return total, nil
}

// classifyReadErr turns EOF (clean or mid-frame) and cancellation into
// a nil return: the stream simply ended.
func classifyReadErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil
	}
	return err
}
