package usb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Identity sent to the phone. The app's res/xml/accessory_filter.xml
// matches manufacturer and model, so these must not change.
const (
	AccessoryManufacturer = "OpenTether"
	AccessoryModel        = "OpenTether Desktop"
	accessoryDescription  = "Share this phone's internet connection with the computer"
	accessoryVersion      = "1"
	accessoryURI          = "https://github.com/krnelpan1c/OpenTether"
	accessorySerial       = "opentether-desktop"
)

const (
	googleVID       = 0x18D1
	pidAccessory    = 0x2D00
	pidAccessoryADB = 0x2D01

	aoaGetProtocol = 51
	aoaSendString  = 52
	aoaStart       = 53

	reqVendorIn  = 0xC0 // device-to-host, vendor, device
	reqVendorOut = 0x40 // host-to-device, vendor, device

	readBufferSize = 64 << 10 // a multiple of every USB max packet size
	transferPoll   = 500      // ms; lets Close interrupt blocking transfers
)

var (
	// ErrNoPhone means no attached device looks like an Android phone.
	ErrNoPhone = errors.New("no Android phone found on USB")
	// ErrNeedUsbDk means Windows' MTP driver owns the phone and the UsbDk
	// driver (which lets libusb reach it anyway) is not installed.
	ErrNeedUsbDk = errors.New("install the free UsbDk driver (https://github.com/daynix/UsbDk/releases) " +
		"so OpenTether can reach the phone without USB debugging")
	// ErrNoSwitch means a phone was told to switch to accessory mode but did
	// not reappear as an accessory.
	ErrNoSwitch = errors.New("the phone did not switch to accessory mode; make sure the OpenTether app is installed")
	// ErrOpenHung means the OS never finished handing the accessory to us.
	ErrOpenHung = errors.New("Windows did not hand the phone over to OpenTether (UsbDk timed out). " +
		"This happens while USB debugging is on: turn USB debugging off and replug the phone, " +
		"or use `opentether usb --via adb`")
)

// Vendors whose devices may be phones even when no MTP/PTP/ADB interface is
// visible (for example in "charging only" mode).
var androidVendors = map[uint16]bool{
	0x18D1: true, // Google
	0x04E8: true, // Samsung
	0x22B8: true, // Motorola
	0x2A70: true, // OnePlus
	0x2717: true, // Xiaomi
	0x12D1: true, // Huawei
	0x339B: true, // Honor
	0x0FCE: true, // Sony
	0x1004: true, // LG
	0x0BB4: true, // HTC
	0x22D9: true, // OPPO / realme
	0x2D95: true, // vivo
	0x0B05: true, // ASUS
	0x17EF: true, // Lenovo
	0x19D2: true, // ZTE
	0x2E04: true, // HMD / Nokia
	0x2AE5: true, // Fairphone
	0x1BBB: true, // TCL / Alcatel
	0x04DD: true, // Sharp
	0x0482: true, // Kyocera
	0x05C6: true, // Qualcomm reference designs
	0x0E8D: true, // MediaTek reference designs
}

func isAccessory(d deviceDescriptor) bool {
	return d.idVendor == googleVID && (d.idProduct == pidAccessory || d.idProduct == pidAccessoryADB)
}

// isPhone reports whether a device looks like an Android phone. Only these
// devices are ever opened: with UsbDk, opening a device detaches it from its
// Windows driver, so probing a keyboard would briefly disconnect it.
func (c *Context) isPhone(d device) bool {
	if d.desc.bDeviceClass == 0x09 { // hub
		return false
	}
	ifaces := c.interfaces(d.ptr)
	for _, itf := range ifaces {
		switch itf {
		case [3]uint8{0x06, 0x01, 0x01}, // PTP / MTP (still image class)
			[3]uint8{0xFF, 0xFF, 0x00}, // MTP (vendor-specific)
			[3]uint8{0xFF, 0x42, 0x01}: // ADB
			return true
		}
	}
	if !androidVendors[d.desc.idVendor] {
		return false
	}
	for _, itf := range ifaces {
		if itf[0] == 0x03 { // HID: a keyboard or mouse from a phone vendor
			return false
		}
	}
	return true
}

// DeviceInfo describes an attached USB device, for diagnostics.
type DeviceInfo struct {
	Bus, Address uint8
	Vendor       uint16
	Product      uint16
	// Phone is true when OpenTether would try to switch it to accessory mode.
	Phone bool
	// Accessory is true when it is already in accessory mode.
	Accessory bool
}

func (d DeviceInfo) String() string {
	return fmt.Sprintf("bus %d addr %d  %04x:%04x", d.Bus, d.Address, d.Vendor, d.Product)
}

// Devices lists attached USB devices without opening any of them.
func (c *Context) Devices() []DeviceInfo {
	devs, free := c.devices()
	defer free()
	out := make([]DeviceInfo, 0, len(devs))
	for _, d := range devs {
		out = append(out, DeviceInfo{
			Bus: d.bus, Address: d.addr,
			Vendor: d.desc.idVendor, Product: d.desc.idProduct,
			Phone:     c.isPhone(d),
			Accessory: isAccessory(d.desc),
		})
	}
	return out
}

// OpenAccessory returns a stream to the OpenTether app on a USB-connected
// phone, first switching the phone into accessory mode if needed.
func (c *Context) OpenAccessory(ctx context.Context) (*Accessory, error) {
	start := time.Now()
	if acc, err := c.openExistingAccessory(start); acc != nil || err != nil {
		return acc, err
	}
	switched, err := c.switchPhones()
	c.trace(start, "asked phones to enter accessory mode", "switched", switched, "err", err)
	if switched == 0 {
		return nil, err
	}
	// The phone drops off the bus and comes back as 18D1:2D00/2D01.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		if acc, err := c.openExistingAccessory(start); acc != nil || err != nil {
			return acc, err
		}
	}
	c.trace(start, "accessory did not appear")
	return nil, ErrNoSwitch
}

func (c *Context) openExistingAccessory(start time.Time) (*Accessory, error) {
	devs, free := c.devices()
	defer free()
	for _, d := range devs {
		if !isAccessory(d.desc) {
			continue
		}
		c.trace(start, "accessory present, opening", "pid", fmt.Sprintf("%04x", d.desc.idProduct))
		h, err := c.openTimeout(d)
		if err != nil {
			c.trace(start, "open failed", "err", err)
			return nil, err
		}
		c.trace(start, "accessory opened (UsbDk configures the device here)")
		c.l.setAutoDetachKernelDriver(h, 1) // Linux only; harmless elsewhere
		in, out, err := c.bulkEndpoints(d.ptr)
		if err != nil {
			c.l.close(h)
			return nil, err
		}
		if rc := c.l.claimInterface(h, 0); rc != 0 {
			c.l.close(h)
			return nil, c.l.err("claim accessory interface", rc)
		}
		c.trace(start, "accessory interface claimed", "in", fmt.Sprintf("%#x", in), "out", fmt.Sprintf("%#x", out))
		return &Accessory{c: c, h: h, in: in, out: out, buf: make([]byte, readBufferSize), opened: time.Now()}, nil
	}
	return nil, nil
}

// switchPhones asks every phone-like device to restart in accessory mode
// and returns how many accepted.
func (c *Context) switchPhones() (int, error) {
	devs, free := c.devices()
	defer free()
	switched, phones := 0, 0
	var firstErr error
	for _, d := range devs {
		if !c.isPhone(d) {
			continue
		}
		phones++
		if err := c.switchPhone(d); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		switched++
	}
	if switched == 0 && firstErr == nil {
		if phones == 0 {
			return 0, ErrNoPhone
		}
		firstErr = errors.New("the phone does not support USB accessory mode")
	}
	return switched, firstErr
}

func (c *Context) switchPhone(d device) error {
	var h uintptr
	if rc := c.l.open(d.ptr, &h); rc != 0 {
		return c.openError(c.l.err(fmt.Sprintf("open %04x:%04x", d.desc.idVendor, d.desc.idProduct), rc))
	}
	defer c.l.close(h)

	var ver [2]byte
	if rc := c.l.controlTransfer(h, reqVendorIn, aoaGetProtocol, 0, 0, &ver[0], 2, 1000); rc != 2 {
		return fmt.Errorf("%04x:%04x does not speak the accessory protocol", d.desc.idVendor, d.desc.idProduct)
	}
	if ver[0]|ver[1] == 0 {
		return errors.New("accessory protocol version 0")
	}
	for i, s := range []string{
		AccessoryManufacturer, AccessoryModel, accessoryDescription,
		accessoryVersion, accessoryURI, accessorySerial,
	} {
		b := append([]byte(s), 0)
		if rc := c.l.controlTransfer(h, reqVendorOut, aoaSendString, 0, uint16(i), &b[0], uint16(len(b)), 1000); rc < 0 {
			return c.l.err("send accessory identity", rc)
		}
	}
	if rc := c.l.controlTransfer(h, reqVendorOut, aoaStart, 0, 0, nil, 0, 1000); rc < 0 {
		return c.l.err("start accessory mode", rc)
	}
	return nil
}

// openError explains failures to open a phone on Windows, where the
// standard driver stack only lets libusb in through UsbDk (or through an
// interface that adb may already be holding).
func (c *Context) openError(err error) error {
	if runtime.GOOS != "windows" || c.UsbDk {
		return err
	}
	if !UsbDkInstalled() {
		return fmt.Errorf("%w (%v)", ErrNeedUsbDk, err)
	}
	var ue *Error
	if errors.As(err, &ue) && ue.Code == errAccess {
		return fmt.Errorf("UsbDk is installed but libusb could not use it (%v), and the phone is in use by "+
			"another program, usually the adb server (stop it with `adb kill-server`): %w", c.UsbDkErr, err)
	}
	return fmt.Errorf("UsbDk is installed but libusb could not use it (%v); "+
		"run `opentether devices --verbose` for libusb's log: %w", c.UsbDkErr, err)
}

// Accessory is an open USB accessory: two bulk endpoints to the phone. It
// stays open across connection attempts (see Session), because closing a
// device under UsbDk can reset it and knock the phone out of accessory mode.
type Accessory struct {
	c       *Context
	h       uintptr
	in, out uint8

	rmu, wmu sync.Mutex
	// Transfers hold closeMu for reading; Close takes it for writing so
	// the handle is only released once no transfer is using it.
	closeMu sync.RWMutex
	closed  atomic.Bool
	failed  atomic.Pointer[error]

	buf  []byte
	data []byte

	opened            time.Time
	sawRead, sawWrite atomic.Bool
}

// Err returns the error that made the accessory unusable (unplugged, I/O
// failure), or nil while it works.
func (a *Accessory) Err() error {
	if p := a.failed.Load(); p != nil {
		return *p
	}
	if a.closed.Load() {
		return io.ErrClosedPipe
	}
	return nil
}

func (a *Accessory) fail(err error) error {
	a.failed.CompareAndSwap(nil, &err)
	return err
}

// Session returns a byte stream over the accessory for one connection
// attempt. Closing the session ends its reads and writes but leaves the
// accessory open for the next attempt.
func (a *Accessory) Session() *Session { return &Session{a: a} }

// Session is one connection attempt's view of an Accessory. Each Read
// returns at most one USB transfer, as transfer framing expects.
type Session struct {
	a      *Accessory
	closed atomic.Bool
	// Deadlines as unix nanoseconds (0 = none). Transfers poll every
	// transferPoll ms, so a deadline takes effect within that interval;
	// quic-go relies on this to stop its reader when shutting down.
	readDeadline, writeDeadline atomic.Int64
}

func (s *Session) SetReadDeadline(t time.Time) error {
	s.readDeadline.Store(unixNano(t))
	return nil
}

func (s *Session) SetWriteDeadline(t time.Time) error {
	s.writeDeadline.Store(unixNano(t))
	return nil
}

func (s *Session) SetDeadline(t time.Time) error {
	_ = s.SetReadDeadline(t)
	return s.SetWriteDeadline(t)
}

func unixNano(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func expired(deadline *atomic.Int64) bool {
	d := deadline.Load()
	return d != 0 && time.Now().UnixNano() >= d
}

// Close ends this session; the accessory stays open.
func (s *Session) Close() error {
	s.closed.Store(true)
	return nil
}

// Read reads from the phone.
func (s *Session) Read(p []byte) (int, error) {
	a := s.a
	a.rmu.Lock()
	defer a.rmu.Unlock()
	for len(a.data) == 0 {
		switch {
		case s.closed.Load():
			return 0, io.EOF
		case expired(&s.readDeadline):
			return 0, os.ErrDeadlineExceeded
		}
		n, rc, ok := a.transfer(a.in, a.buf, transferPoll)
		if !ok {
			return 0, io.EOF
		}
		if n > 0 {
			if !a.sawRead.Swap(true) {
				a.c.trace(a.opened, "first data from the phone", "bytes", n)
			}
			a.data = a.buf[:n]
			break
		}
		if rc != errTimeout {
			return 0, a.fail(a.c.l.err("usb read", rc))
		}
	}
	n := copy(p, a.data)
	a.data = a.data[n:]
	return n, nil
}

// Write sends p to the phone as one bulk transfer.
func (s *Session) Write(p []byte) (int, error) {
	a := s.a
	a.wmu.Lock()
	defer a.wmu.Unlock()
	written := 0
	for written < len(p) {
		switch {
		case s.closed.Load():
			return written, io.ErrClosedPipe
		case expired(&s.writeDeadline):
			return written, os.ErrDeadlineExceeded
		}
		// Writes only time out while the phone app isn't reading yet, so a
		// longer poll keeps transfers from being cancelled needlessly.
		n, rc, ok := a.transfer(a.out, p[written:], 4*transferPoll)
		if !ok {
			return written, io.ErrClosedPipe
		}
		written += n
		if n > 0 && !a.sawWrite.Swap(true) {
			a.c.trace(a.opened, "first data accepted by the phone", "bytes", n)
		}
		if rc != 0 && rc != errTimeout {
			return written, a.fail(a.c.l.err("usb write", rc))
		}
	}
	return written, nil
}

func (a *Accessory) transfer(ep uint8, b []byte, timeout uint32) (n int, rc int32, ok bool) {
	a.closeMu.RLock()
	defer a.closeMu.RUnlock()
	if a.closed.Load() {
		return 0, 0, false
	}
	var got int32
	rc = a.c.l.bulkTransfer(a.h, ep, &b[0], int32(len(b)), &got, timeout)
	return int(got), rc, true
}

// Close releases the device. In-flight transfers finish within one poll
// interval.
func (a *Accessory) Close() error {
	if a.closed.Swap(true) {
		return nil
	}
	a.closeMu.Lock()
	defer a.closeMu.Unlock()
	a.c.l.releaseInterface(a.h, 0)
	a.c.l.close(a.h)
	return nil
}

// AccessoryPresent reports whether a phone is already in accessory mode.
func (c *Context) AccessoryPresent() bool {
	devs, free := c.devices()
	defer free()
	for _, d := range devs {
		if isAccessory(d.desc) {
			return true
		}
	}
	return false
}

// openTimeout opens a device but gives up after a while: under UsbDk the
// open can block forever when Windows won't release the device (seen with
// composite accessory+adb devices). A hung open leaks its goroutine, so the
// context refuses further opens; restarting the client clears it.
func (c *Context) openTimeout(d device) (uintptr, error) {
	if c.hung.Load() {
		return 0, ErrOpenHung
	}
	type result struct {
		h  uintptr
		rc int32
	}
	ch := make(chan result, 1)
	go func() {
		var h uintptr
		rc := c.l.open(d.ptr, &h)
		ch <- result{h, rc}
	}()
	select {
	case r := <-ch:
		if r.rc != 0 {
			return 0, c.openError(c.l.err("open accessory", r.rc))
		}
		return r.h, nil
	case <-time.After(8 * time.Second):
		c.hung.Store(true)
		return 0, ErrOpenHung
	}
}
