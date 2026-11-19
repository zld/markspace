package transport

import (
	"errors"
	"net"
	"testing"
)

func TestParseMode(t *testing.T) {
	if m, err := ParseMode("tcp"); err != nil || m != ModeTCP {
		t.Fatalf("tcp parse: %v %v", m, err)
	}
	if m, err := ParseMode("udp"); err != nil || m != ModeUDP {
		t.Fatalf("udp parse: %v %v", m, err)
	}
	if _, err := ParseMode("sctp"); err == nil {
		t.Fatal("invalid mode must error")
	}
}

func TestUDPPortAllocatorRejectsBadRange(t *testing.T) {
	for _, tc := range [][2]int{{512, 600}, {2000, 1000}, {1000, 70000}} {
		if _, err := newUDPPortAllocator(tc[0], tc[1]); err == nil {
			t.Fatalf("range %v must be rejected", tc)
		}
	}
}

func TestUDPPortAllocatorBindsLoopbackPairs(t *testing.T) {
	a, err := newUDPPortAllocator(31000, 31040)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()

	rtp, err := a.listenPacket("udp", ":0")
	if err != nil {
		t.Fatalf("rtp bind: %v", err)
	}
	rtcp, err := a.listenPacket("udp", ":0")
	if err != nil {
		t.Fatalf("rtcp bind: %v", err)
	}

	for _, pc := range []net.PacketConn{rtp, rtcp} {
		addr := pc.LocalAddr().(*net.UDPAddr)
		if !addr.IP.IsLoopback() {
			t.Fatalf("socket bound to non-loopback IP %v", addr.IP)
		}
	}
	rtpPort := rtp.LocalAddr().(*net.UDPAddr).Port
	rtcpPort := rtcp.LocalAddr().(*net.UDPAddr).Port
	if rtcpPort != rtpPort+1 {
		t.Fatalf("ports %d/%d must be consecutive (even/odd)", rtpPort, rtcpPort)
	}
	if rtpPort%2 != 0 {
		t.Fatalf("RTP port %d must be even", rtpPort)
	}
	rtp.Close()
	rtcp.Close()
}

func TestUDPPortAllocatorRefusesNonLoopback(t *testing.T) {
	a, _ := newUDPPortAllocator(31050, 31060)
	defer a.close()
	if _, err := a.listenPacket("udp", "8.8.8.8:0"); err == nil {
		t.Fatal("non-loopback bind must be refused")
	}
}

func TestUDPPortAllocatorExhaustion(t *testing.T) {
	// Range of two ports: occupy both externally so allocation fails.
	blocker1, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 31100})
	if err != nil {
		t.Fatal(err)
	}
	blocker2, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 31101})
	if err != nil {
		t.Fatal(err)
	}
	defer blocker1.Close()
	defer blocker2.Close()

	a, err := newUDPPortAllocator(31100, 31101)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	if _, err := a.listenPacket("udp", ":0"); !errors.Is(err, ErrPortExhausted) {
		t.Fatalf("got %v, want ErrPortExhausted", err)
	}
}

func TestAdapterCloseIsIdempotent(t *testing.T) {
	tcp := newTCPAdapter(Config{Mode: ModeTCP})
	if err := tcp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tcp.Close(); err != nil {
		t.Fatalf("second TCP close: %v", err)
	}

	udp, err := newUDPAdapter(Config{Mode: ModeUDP, UDPMinPort: 31200, UDPMaxPort: 31220})
	if err != nil {
		t.Fatal(err)
	}
	if err := udp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := udp.Close(); err != nil {
		t.Fatalf("second UDP close: %v", err)
	}
}
