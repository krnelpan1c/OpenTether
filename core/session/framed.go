// Package session holds the transport-agnostic pieces shared by the phone
// relay and the desktop client: QUIC configuration, TLS identities and the
// adapters that let QUIC run over byte-stream links such as adb forward.
package session

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// streamAddr is the net.Addr reported for stream-based links.
type streamAddr string

func (a streamAddr) Network() string { return "opentether-stream" }
func (a streamAddr) String() string  { return string(a) }

// LinkOptions tunes a framed link for the transport underneath it.
type LinkOptions struct {
	// Name identifies the link in addresses and logs.
	Name string
	// TransferFraming treats every read from the link as one transfer that
	// holds whole frames (USB bulk links with BatchWrites on both ends), and
	// parses frames per transfer so one damaged transfer can't corrupt the
	// rest of the stream.
	TransferFraming bool
	// ReadSize is the buffer used for each transfer read. USB accessory
	// endpoints need reads sized to whole transfers (16 KB on the phone).
	ReadSize int
	// BatchWrites coalesces queued packets into large writes. Each write on
	// a USB link is one bulk transfer, so batching is what makes USB fast.
	BatchWrites bool
	// PadMultiple, when set, keeps every write from being an exact multiple
	// of this many bytes by appending an empty frame. A USB transfer that
	// ends on a max-packet boundary without a zero-length packet leaves the
	// receiver waiting for more data.
	PadMultiple int
}

// FramedPacketConn adapts a reliable byte stream (an adb-forwarded socket, a
// USB accessory bulk pipe, ...) into a net.PacketConn so QUIC can run on top.
// Each packet is written as a 2-byte big-endian length followed by the packet.
// Zero-length frames are padding and are skipped.
//
// The link is point-to-point, so every packet comes "from" the peer and
// WriteTo ignores its address argument.
type FramedPacketConn struct {
	rwc io.ReadWriteCloser
	r   *bufio.Reader

	wmu   sync.Mutex
	wbuf  []byte
	batch *batchWriter

	// Transfer framing state; only touched by the single reader.
	chunkBuf, chunk []byte
	dropped         atomic.Int64

	local, remote net.Addr
}

// NewFramedPacketConn wraps a network connection. The returned PacketConn
// owns conn.
func NewFramedPacketConn(conn net.Conn) *FramedPacketConn {
	c := NewFramedLink(conn, LinkOptions{})
	c.local = streamAddr("local:" + conn.LocalAddr().String())
	c.remote = streamAddr("peer:" + conn.RemoteAddr().String())
	return c
}

// NewFramedLink wraps any byte stream. The returned PacketConn owns rwc.
func NewFramedLink(rwc io.ReadWriteCloser, opts LinkOptions) *FramedPacketConn {
	name := opts.Name
	if name == "" {
		name = "link"
	}
	c := &FramedPacketConn{
		rwc:    rwc,
		r:      bufio.NewReaderSize(rwc, 256<<10),
		wbuf:   make([]byte, 0, 2+0xFFFF),
		local:  streamAddr("local:" + name),
		remote: streamAddr("peer:" + name),
	}
	if opts.TransferFraming {
		size := opts.ReadSize
		if size == 0 {
			size = 64 << 10
		}
		c.chunkBuf = make([]byte, size)
	}
	if opts.BatchWrites {
		c.batch = newBatchWriter(rwc, 16000, opts.PadMultiple)
	}
	return c
}

// RemoteAddr returns the address that ReadFrom reports for every packet.
func (c *FramedPacketConn) RemoteAddr() net.Addr { return c.remote }

// ReadFrom reads one packet. Packets longer than p are truncated, matching
// UDP semantics.
func (c *FramedPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if c.chunkBuf != nil {
		return c.readFromTransfer(p)
	}
	for {
		var hdr [2]byte
		if _, err := io.ReadFull(c.r, hdr[:]); err != nil {
			return 0, nil, err
		}
		size := int(binary.BigEndian.Uint16(hdr[:]))
		if size == 0 {
			continue // padding
		}
		n := min(size, len(p))
		if _, err := io.ReadFull(c.r, p[:n]); err != nil {
			return 0, nil, err
		}
		if size > n {
			if _, err := c.r.Discard(size - n); err != nil {
				return 0, nil, err
			}
		}
		return n, c.remote, nil
	}
}

// readFromTransfer parses frames within one transfer at a time. Every write
// on a USB link is one transfer holding whole frames, so a transfer that was
// lost or cut short only costs the packets inside it (QUIC resends them)
// instead of desynchronising every frame after it.
func (c *FramedPacketConn) readFromTransfer(p []byte) (int, net.Addr, error) {
	for {
		for len(c.chunk) >= 2 {
			size := int(binary.BigEndian.Uint16(c.chunk))
			if size == 0 {
				c.chunk = c.chunk[2:] // padding
				continue
			}
			if 2+size > len(c.chunk) {
				c.dropped.Add(1) // truncated transfer
				c.chunk = nil
				break
			}
			n := copy(p, c.chunk[2:2+size])
			c.chunk = c.chunk[2+size:]
			return n, c.remote, nil
		}
		c.chunk = nil
		n, err := c.rwc.Read(c.chunkBuf)
		if n > 0 {
			c.chunk = c.chunkBuf[:n]
			continue
		}
		if err != nil {
			return 0, nil, err
		}
	}
}

// Dropped reports how many frames were discarded because their transfer
// was truncated (transfer framing only).
func (c *FramedPacketConn) Dropped() int64 { return c.dropped.Load() }

// WriteTo writes one packet to the peer.
func (c *FramedPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(p) > 0xFFFF {
		return 0, &net.OpError{Op: "write", Net: "opentether-stream", Err: errPacketTooLarge}
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.wbuf = binary.BigEndian.AppendUint16(c.wbuf[:0], uint16(len(p)))
	c.wbuf = append(c.wbuf, p...)
	var err error
	if c.batch != nil {
		err = c.batch.write(c.wbuf)
	} else {
		_, err = c.rwc.Write(c.wbuf)
	}
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close closes the link.
func (c *FramedPacketConn) Close() error {
	if c.batch != nil {
		c.batch.close()
	}
	return c.rwc.Close()
}

func (c *FramedPacketConn) LocalAddr() net.Addr { return c.local }

type deadliner interface {
	SetDeadline(time.Time) error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

// Deadlines are forwarded when the underlying stream supports them; links
// without deadlines (USB) are unblocked by closing them instead.
func (c *FramedPacketConn) SetDeadline(t time.Time) error {
	if d, ok := c.rwc.(deadliner); ok {
		return d.SetDeadline(t)
	}
	return nil
}

func (c *FramedPacketConn) SetReadDeadline(t time.Time) error {
	if d, ok := c.rwc.(deadliner); ok {
		return d.SetReadDeadline(t)
	}
	return nil
}

func (c *FramedPacketConn) SetWriteDeadline(t time.Time) error {
	if d, ok := c.rwc.(deadliner); ok {
		return d.SetWriteDeadline(t)
	}
	return nil
}

type packetError string

func (e packetError) Error() string { return string(e) }

const errPacketTooLarge = packetError("packet too large for framed link")

// batchWriter queues frames and writes them in as few large writes as
// possible from a single goroutine. While one write is in flight new frames
// accumulate, so batching adds no latency when the link is idle.
type batchWriter struct {
	w   io.Writer
	max int
	pad int

	mu      sync.Mutex
	cond    *sync.Cond
	pending []byte
	spare   []byte
	err     error
	closed  bool
}

func newBatchWriter(w io.Writer, max, pad int) *batchWriter {
	b := &batchWriter{w: w, max: max, pad: pad, pending: make([]byte, 0, max+2), spare: make([]byte, 0, max+2)}
	b.cond = sync.NewCond(&b.mu)
	go b.loop()
	return b
}

func (b *batchWriter) write(frame []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.err == nil && !b.closed && len(b.pending) > 0 && len(b.pending)+len(frame) > b.max {
		b.cond.Wait()
	}
	if b.err != nil {
		return b.err
	}
	if b.closed {
		return io.ErrClosedPipe
	}
	b.pending = append(b.pending, frame...)
	b.cond.Broadcast()
	return nil
}

func (b *batchWriter) loop() {
	for {
		b.mu.Lock()
		for len(b.pending) == 0 && !b.closed {
			b.cond.Wait()
		}
		if b.closed {
			b.mu.Unlock()
			return
		}
		out := b.pending
		b.pending, b.spare = b.spare[:0], nil
		b.cond.Broadcast()
		b.mu.Unlock()

		if b.pad > 0 && len(out)%b.pad == 0 {
			out = append(out, 0, 0) // empty frame
		}
		_, err := b.w.Write(out)

		b.mu.Lock()
		b.spare = out[:0]
		if err != nil {
			b.err = err
			b.cond.Broadcast()
			b.mu.Unlock()
			return
		}
		b.mu.Unlock()
	}
}

func (b *batchWriter) close() {
	b.mu.Lock()
	b.closed = true
	b.cond.Broadcast()
	b.mu.Unlock()
}
