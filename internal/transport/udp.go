package transport

import (
	"fmt"
	"net"
	"sync"

	"github.com/bluenviron/gortsplib/v4"
)

// minUDPPort / maxUDPPort hard-bound the configurable range so a bad
// flag can never request privileged or out-of-range sockets.
const (
	minUDPPort = 1024
	maxUDPPort = 65535
)

// udpPortAllocator owns every client UDP socket. gortsplib opens two
// listeners per media (RTP then RTCP); the allocator hands out an
// even/odd consecutive pair from the configured range by keeping one
// socket reserved between the two calls. All sockets bind to the
// loopback address only.
type udpPortAllocator struct {
	minPort  int
	maxPort  int
	mu       sync.Mutex
	reserved *net.UDPConn
	tried    map[int]bool
}

func newUDPPortAllocator(minPort, maxPort int) (*udpPortAllocator, error) {
	if minPort == 0 && maxPort == 0 {
		// Conservative default window above the privileged range.
		minPort, maxPort = 30000, 30100
	}
	if minPort < minUDPPort || maxPort > maxUDPPort || minPort > maxPort {
		return nil, fmt.Errorf(
			"transport: UDP port range %d-%d invalid (must be within %d-%d)",
			minPort, maxPort, minUDPPort, maxUDPPort)
	}
	return &udpPortAllocator{
		minPort: minPort,
		maxPort: maxPort,
		tried:   make(map[int]bool),
	}, nil
}

// listenPacket implements gortsplib.Client.ListenPacket.
func (a *udpPortAllocator) listenPacket(network, address string) (net.PacketConn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("transport: invalid UDP address %q: %w", address, err)
	}
	if host != "" && host != "127.0.0.1" && host != "localhost" {
		return nil, fmt.Errorf("transport: refusing non-loopback UDP bind on %q", host)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.reserved != nil {
		conn := a.reserved
		a.reserved = nil
		return conn, nil
	}

	for port := a.minPort; port <= a.maxPort; port++ {
		if a.tried[port] {
			continue
		}
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
		if err != nil {
			a.tried[port] = true
			continue
		}
		a.tried[port] = true
		// Reserve the consecutive port for the RTCP listener.
		if port+1 <= a.maxPort && !a.tried[port+1] {
			rtcpConn, err := net.ListenUDP("udp",
				&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port + 1})
			if err == nil {
				a.reserved = rtcpConn
			}
		}
		return conn, nil
	}
	return nil, ErrPortExhausted
}

// close releases any socket still held in reserve.
func (a *udpPortAllocator) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.reserved != nil {
		a.reserved.Close()
		a.reserved = nil
	}
}

// udpAdapter wires a gortsplib client to the loopback UDP allocator.
type udpAdapter struct {
	client    *gortsplib.Client
	allocator *udpPortAllocator
	closed    bool
	started   bool
	mu        sync.Mutex
}

func newUDPAdapter(cfg Config) (*udpAdapter, error) {
	alloc, err := newUDPPortAllocator(cfg.UDPMinPort, cfg.UDPMaxPort)
	if err != nil {
		return nil, err
	}
	client := newBaseClient(cfg)
	transport := gortsplib.TransportUDP
	client.Transport = &transport
	client.ListenPacket = alloc.listenPacket

	return &udpAdapter{client: client, allocator: alloc}, nil
}

func (a *udpAdapter) Mode() Mode                { return ModeUDP }
func (a *udpAdapter) Client() *gortsplib.Client { return a.client }
func (a *udpAdapter) OnRTP(h RawHandler)        { a.client.OnPacketRTPAny(wireCallback(h)) }

func (a *udpAdapter) MarkStarted() {
	a.mu.Lock()
	a.started = true
	a.mu.Unlock()
}

func (a *udpAdapter) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	if a.started {
		// Closes the RTP/RTCP listeners gortsplib created via
		// ListenPacket, then release any socket still in reserve.
		a.client.Close()
	}
	a.allocator.close()
	return nil
}
