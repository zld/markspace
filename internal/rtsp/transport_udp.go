package rtsp

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"rtspdiag/internal/config"
)

// runUDP performs RTSP signaling over a raw TCP control connection and reads
// RTP from a self-managed UDP socket. It deliberately bypasses gortsplib's
// client-side UDP reorder buffer, which would absorb every packet following a
// loss and hide the very gaps this tool exists to measure; the project-owned
// parser and reorderer own the data plane instead. The same Session state
// machine drives the method transitions as the TCP/interleaved adapter.
func (a *Adapter) runUDP(ctx context.Context) error {
	parsed, err := url.Parse(a.cfg.URL)
	if err != nil {
		return fmt.Errorf("rtsp: parse url: %w", err)
	}
	control, err := net.DialTimeout("tcp", parsed.Host, a.cfg.ReadTimeout)
	if err != nil {
		return fmt.Errorf("rtsp: dial control: %w", err)
	}
	defer control.Close()
	reader := bufio.NewReaderSize(control, 64*1024)

	c := &rawClient{conn: control, rd: reader, url: a.cfg.URL}
	if _, err := c.request("OPTIONS", a.cfg.URL, ""); err != nil {
		return fmt.Errorf("rtsp: options: %w", err)
	}
	if err := a.session.Apply(MethodOptions); err != nil {
		return err
	}

	if _, err := c.request("DESCRIBE", a.cfg.URL, "Accept: application/sdp"); err != nil {
		return fmt.Errorf("rtsp: describe: %w", err)
	}
	if err := a.session.Apply(MethodDescribe); err != nil {
		return err
	}

	// Self-managed, consecutive RTP/RTCP UDP ports on loopback.
	rtpConn, rtcpConn, rtpPort, rtcpPort, err := bindUDPPair()
	if err != nil {
		return err
	}
	defer rtpConn.Close()
	defer rtcpConn.Close()

	th := fmt.Sprintf("Transport: RTP/AVP;unicast;client_port=%d-%d", rtpPort, rtcpPort)
	setupHdr, err := c.request("SETUP", a.cfg.URL+"/trackID=0", th)
	if err != nil {
		return fmt.Errorf("rtsp: setup: %w", err)
	}
	if err := a.session.Apply(MethodSetup); err != nil {
		return err
	}
	sessionID := extractSession(setupHdr)

	if _, err := c.request("PLAY", a.cfg.URL, "Session: "+sessionID); err != nil {
		return fmt.Errorf("rtsp: play: %w", err)
	}
	if err := a.session.Apply(MethodPlay); err != nil {
		return err
	}

	if len(a.cfg.InjectMalformed) > 0 {
		_ = a.cfg.Pipeline.Ingest(a.cfg.InjectMalformed, time.Now())
	}

	// Data plane: read raw RTP datagrams straight into the project pipeline.
	// It returns on the short idle grace after the fixture stream ends (the
	// mock server disconnects the control connection once all packets are out).
	recvErr := a.receiveUDP(ctx, rtpConn)

	// Best-effort TEARDOWN over the control channel; a closed control
	// connection (server disconnect) must not mask the receive outcome.
	_, _ = c.request("TEARDOWN", a.cfg.URL, "Session: "+sessionID)
	_ = a.session.Apply(MethodTeardown)
	return recvErr
}

// receiveUDP reads datagrams until the context ends, the socket is closed by
// the control-connection watcher (server disconnect), or the idle read
// timeout elapses with no data after the first packet arrived.
func (a *Adapter) receiveUDP(ctx context.Context, rtpConn net.PacketConn) error {
	buf := make([]byte, 65535)
	gotOne := false
	// Idle grace is short once data flows: the fixture ends with a disconnect,
	// and the control watcher normally unblocks us before this timer fires.
	idleGrace := 500 * time.Millisecond
	for {
		idle := idleGrace
		if !gotOne {
			idle = 2 * time.Second // initial server/stream startup grace
		}
		_ = rtpConn.SetReadDeadline(time.Now().Add(idle))
		n, _, err := rtpConn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if gotOne {
					return nil // no more data within the grace window
				}
				continue
			}
			// Socket closed by the disconnect watcher or torn down.
			return nil
		}
		gotOne = true
		raw := make([]byte, n)
		copy(raw, buf[:n])
		a.mu.Lock()
		a.gotData = true
		a.mu.Unlock()
		_ = a.cfg.Pipeline.Ingest(raw, time.Now())
	}
}

// rawClient is a minimal RTSP/1.0 request/response helper over one TCP conn.
type rawClient struct {
	conn net.Conn
	rd   *bufio.Reader
	url  string
	cseq int
}

func (c *rawClient) request(method, target string, extra string) (string, error) {
	c.cseq++
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %s RTSP/1.0\r\n", method, target)
	fmt.Fprintf(&sb, "CSeq: %d\r\n", c.cseq)
	if extra != "" {
		fmt.Fprintf(&sb, "%s\r\n", extra)
	}
	sb.WriteString("\r\n")
	if _, err := c.conn.Write([]byte(sb.String())); err != nil {
		return "", err
	}
	return c.readResponse()
}

func (c *rawClient) readResponse() (string, error) {
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var sb strings.Builder
	contentLength := 0
	for {
		line, err := c.rd.ReadString('\n')
		if err != nil {
			return "", err
		}
		sb.WriteString(line)
		trim := strings.TrimRight(line, "\r\n")
		lower := strings.ToLower(trim)
		if strings.HasPrefix(lower, "content-length:") {
			if v, perr := strconv.Atoi(strings.TrimSpace(trim[len("content-length:"):])); perr == nil {
				contentLength = v
			}
		}
		if trim == "" {
			break // end of headers
		}
	}
	if contentLength > 0 {
		body := make([]byte, contentLength)
		if _, err := readFull(c.rd, body); err != nil {
			return "", err
		}
		sb.Write(body)
	}
	resp := sb.String()
	if !strings.Contains(resp, " 200 ") {
		return resp, fmt.Errorf("non-200 response: %s", firstLine(resp))
	}
	return resp, nil
}

func readFull(rd *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := rd.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimRight(s[:i], "\r")
	}
	return s
}

func extractSession(headers string) string {
	for _, line := range strings.Split(headers, "\n") {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "session:") {
			v := strings.TrimSpace(line[len("Session:"):])
			// Strip any session parameters after a semicolon.
			if i := strings.IndexByte(v, ';'); i >= 0 {
				v = v[:i]
			}
			return strings.TrimSpace(v)
		}
	}
	return "rtspdiag"
}

// bindUDPPair allocates two consecutive loopback UDP ports, RTP on an even
// port and RTCP on the following odd port.
func bindUDPPair() (net.PacketConn, net.PacketConn, int, int, error) {
	for attempt := 0; attempt < 32; attempt++ {
		probe, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			return nil, nil, 0, 0, fmt.Errorf("rtsp: reserve udp: %w", err)
		}
		rtpPort := probe.LocalAddr().(*net.UDPAddr).Port
		_ = probe.Close()
		if rtpPort%2 != 0 {
			rtpPort++
		}
		rtpConn, err := net.ListenPacket("udp",
			net.JoinHostPort("127.0.0.1", strconv.Itoa(rtpPort)))
		if err != nil {
			continue
		}
		rtcpConn, err := net.ListenPacket("udp",
			net.JoinHostPort("127.0.0.1", strconv.Itoa(rtpPort+1)))
		if err != nil {
			_ = rtpConn.Close()
			continue
		}
		return rtpConn, rtcpConn, rtpPort, rtpPort + 1, nil
	}
	return nil, nil, 0, 0, fmt.Errorf("rtsp: could not bind consecutive UDP ports")
}

var _ = config.TransportUDP
