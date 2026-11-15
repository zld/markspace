// Package transport guards and binds loopback-only UDP endpoints so the
// tool never touches LAN or public addresses.
package transport

import (
	"fmt"
	"net"
)

// ValidateLoopback rejects any address that is not 127.0.0.1 or ::1.
// The input may be a bare IP or a host:port pair.
func ValidateLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("address %q is not a valid IP literal", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("address %s is not a loopback address (only 127.0.0.1 and ::1 are allowed)", ip)
	}
	return nil
}

// IsLoopbackCandidate reports whether a "host:port" candidate address is a
// loopback endpoint. Unparseable candidates count as non-loopback.
func IsLoopbackCandidate(addr string) bool {
	return ValidateLoopback(addr) == nil
}

// BindLoopbackUDP binds a UDP socket on the given loopback IP and port.
// Port 0 selects a random free port. It refuses non-loopback binds.
func BindLoopbackUDP(ip string, port int) (*net.UDPConn, error) {
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("port %d out of range [0,65535]", port)
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return nil, fmt.Errorf("bind address %q is not a valid IP literal", ip)
	}
	if !parsed.IsLoopback() {
		return nil, fmt.Errorf("bind address %s is not loopback; refusing to bind", ip)
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: parsed, Port: port})
	if err != nil {
		return nil, fmt.Errorf("bind %s:%d: %w", ip, port, err)
	}
	return conn, nil
}
