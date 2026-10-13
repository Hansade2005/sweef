package engine

// webrtc_net.go: the network handed to pion. It is the standard library
// network with one change: every unicast UDP socket pion opens is wrapped in a
// counter that feeds the browsing context's wire totals (wire.go), so WebRTC
// traffic shows up in the Data Saver meter next to HTTP traffic. Multicast
// sockets (mDNS) are returned untouched because the multicast code needs the
// concrete *net.UDPConn.

import (
	"net"

	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"
)

type rtcNet struct {
	transport.Net
	w *wireCounters
}

func newRTCNet(w *wireCounters) (transport.Net, error) {
	base, err := stdnet.NewNet()
	if err != nil {
		return nil, err
	}
	if w == nil {
		return base, nil
	}
	return &rtcNet{Net: base, w: w}, nil
}

func (n *rtcNet) ListenUDP(network string, laddr *net.UDPAddr) (transport.UDPConn, error) {
	c, err := n.Net.ListenUDP(network, laddr)
	if err != nil || c == nil || (laddr != nil && laddr.IP.IsMulticast()) {
		return c, err
	}
	return &countUDP{UDPConn: c, w: n.w}, nil
}

func (n *rtcNet) ListenPacket(network, address string) (net.PacketConn, error) {
	c, err := n.Net.ListenPacket(network, address)
	if err != nil || c == nil {
		return c, err
	}
	return &countPacket{PacketConn: c, w: n.w}, nil
}

func (n *rtcNet) DialUDP(network string, laddr, raddr *net.UDPAddr) (transport.UDPConn, error) {
	c, err := n.Net.DialUDP(network, laddr, raddr)
	if err != nil || c == nil {
		return c, err
	}
	return &countUDP{UDPConn: c, w: n.w}, nil
}

func (n *rtcNet) Dial(network, address string) (net.Conn, error) {
	c, err := n.Net.Dial(network, address)
	if err != nil || c == nil {
		return c, err
	}
	return &countConn{Conn: c, w: n.w}, nil
}

type countUDP struct {
	transport.UDPConn
	w *wireCounters
}

func (c *countUDP) Read(p []byte) (int, error) {
	n, err := c.UDPConn.Read(p)
	c.w.addIn(n)
	return n, err
}

func (c *countUDP) ReadFrom(p []byte) (int, net.Addr, error) {
	n, a, err := c.UDPConn.ReadFrom(p)
	c.w.addIn(n)
	return n, a, err
}

func (c *countUDP) ReadFromUDP(p []byte) (int, *net.UDPAddr, error) {
	n, a, err := c.UDPConn.ReadFromUDP(p)
	c.w.addIn(n)
	return n, a, err
}

func (c *countUDP) Write(p []byte) (int, error) {
	n, err := c.UDPConn.Write(p)
	c.w.addOut(n)
	return n, err
}

func (c *countUDP) WriteTo(p []byte, a net.Addr) (int, error) {
	n, err := c.UDPConn.WriteTo(p, a)
	c.w.addOut(n)
	return n, err
}

func (c *countUDP) WriteToUDP(p []byte, a *net.UDPAddr) (int, error) {
	n, err := c.UDPConn.WriteToUDP(p, a)
	c.w.addOut(n)
	return n, err
}

type countPacket struct {
	net.PacketConn
	w *wireCounters
}

func (c *countPacket) ReadFrom(p []byte) (int, net.Addr, error) {
	n, a, err := c.PacketConn.ReadFrom(p)
	c.w.addIn(n)
	return n, a, err
}

func (c *countPacket) WriteTo(p []byte, a net.Addr) (int, error) {
	n, err := c.PacketConn.WriteTo(p, a)
	c.w.addOut(n)
	return n, err
}
