package engine

// webrtc_net.go: the network handed to pion. It is the standard library
// network with one change: every unicast UDP socket pion opens is wrapped in a
// counter that feeds the browsing context's wire totals (wire.go), so WebRTC
// traffic shows up in the Data Saver meter next to HTTP traffic. Multicast
// sockets (mDNS) are returned untouched because the multicast code needs the
// concrete *net.UDPConn.

import (
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"
)

// rtcLinkSim impairs the media this process sends, for tests: media datagrams (over 90 bytes, so
// not the small STUN and RTCP packets; audio shares the queue with video, as on a real path) are lost at random, dropped in bursts, or
// squeezed through a bottleneck of a given rate with a drop-tail queue (the queueing delay is applied by
// delivering the datagram later, so the sender is never blocked, as on a real path). It is off unless a test
// turns it on.
type rtcLinkSim struct {
	mu       sync.Mutex
	on       bool
	loss     float64       // probability of dropping a large datagram
	rate     float64       // bottleneck in bytes per second (0: unlimited)
	maxQueue time.Duration // queueing delay beyond which the bottleneck drops
	dropAll  time.Time     // drop every large datagram until then
	busy     time.Time
	rnd      *rand.Rand
	Dropped  int
	Passed   int
}

var (
	rtcSim   rtcLinkSim
	rtcSimOn atomic.Bool // fast path: no lock while the simulator is off
)

// admit decides whether a datagram of n bytes goes out and after how long (the bottleneck's queueing delay).
func (l *rtcLinkSim) admit(n int) (bool, time.Duration) {
	if n <= 90 || !rtcSimOn.Load() {
		return true, 0
	}
	l.mu.Lock()
	if !l.on {
		l.mu.Unlock()
		return true, 0
	}
	if l.rnd == nil {
		l.rnd = rand.New(rand.NewSource(7))
	}
	now := time.Now()
	if now.Before(l.dropAll) || (l.loss > 0 && l.rnd.Float64() < l.loss) {
		l.Dropped++
		l.mu.Unlock()
		return false, 0
	}
	var wait time.Duration
	if l.rate > 0 {
		if l.busy.Before(now) {
			l.busy = now
		}
		wait = l.busy.Sub(now)
		if l.maxQueue > 0 && wait > l.maxQueue {
			l.Dropped++
			l.mu.Unlock()
			return false, 0
		}
		l.busy = l.busy.Add(time.Duration(float64(n) / l.rate * float64(time.Second)))
	}
	l.Passed++
	l.mu.Unlock()
	return true, wait
}

// set configures the simulator (zero values turn a feature off) and switches it on.
func (l *rtcLinkSim) set(loss float64, rateBps float64, maxQueue time.Duration) {
	l.mu.Lock()
	l.on, l.loss, l.rate, l.maxQueue = true, loss, rateBps/8, maxQueue
	rtcSimOn.Store(true)
	l.busy = time.Time{}
	l.Dropped, l.Passed = 0, 0
	l.mu.Unlock()
}

func (l *rtcLinkSim) blackout(d time.Duration) {
	l.mu.Lock()
	l.on = true
	rtcSimOn.Store(true)
	l.dropAll = time.Now().Add(d)
	l.mu.Unlock()
}

func (l *rtcLinkSim) reset() {
	l.mu.Lock()
	l.on, l.loss, l.rate, l.dropAll = false, 0, 0, time.Time{}
	rtcSimOn.Store(false)
	l.mu.Unlock()
}

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
	if ok, d := rtcSim.admit(len(p)); !ok {
		return len(p), nil
	} else if d > 0 {
		q := append([]byte(nil), p...)
		time.AfterFunc(d, func() { n, _ := c.UDPConn.Write(q); c.w.addOut(n) })
		return len(p), nil
	}
	n, err := c.UDPConn.Write(p)
	c.w.addOut(n)
	return n, err
}

func (c *countUDP) WriteTo(p []byte, a net.Addr) (int, error) {
	if ok, d := rtcSim.admit(len(p)); !ok {
		return len(p), nil
	} else if d > 0 {
		q := append([]byte(nil), p...)
		time.AfterFunc(d, func() { n, _ := c.UDPConn.WriteTo(q, a); c.w.addOut(n) })
		return len(p), nil
	}
	n, err := c.UDPConn.WriteTo(p, a)
	c.w.addOut(n)
	return n, err
}

func (c *countUDP) WriteToUDP(p []byte, a *net.UDPAddr) (int, error) {
	if ok, d := rtcSim.admit(len(p)); !ok {
		return len(p), nil
	} else if d > 0 {
		q := append([]byte(nil), p...)
		time.AfterFunc(d, func() { n, _ := c.UDPConn.WriteToUDP(q, a); c.w.addOut(n) })
		return len(p), nil
	}
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
	if ok, d := rtcSim.admit(len(p)); !ok {
		return len(p), nil
	} else if d > 0 {
		q := append([]byte(nil), p...)
		time.AfterFunc(d, func() { n, _ := c.PacketConn.WriteTo(q, a); c.w.addOut(n) })
		return len(p), nil
	}
	n, err := c.PacketConn.WriteTo(p, a)
	c.w.addOut(n)
	return n, err
}
