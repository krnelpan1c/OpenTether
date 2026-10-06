// Package usb talks to Android phones over USB with libusb, loaded at run
// time so the desktop client still builds without cgo. It implements the
// host side of the Android Open Accessory (AOA) protocol, which lets the
// phone share its connection over USB without USB debugging.
package usb

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// ErrNoLibusb is returned when the libusb shared library cannot be loaded.
var ErrNoLibusb = errors.New("libusb is not installed")

// libusb error codes used below.
const (
	errIO       = -1
	errAccess   = -3
	errNoDevice = -4
	errTimeout  = -7
	errNotFound = -5
)

// libusb_option values.
const (
	optionLogLevel = 0
	optionUseUsbDk = 1
)

// Mirrors of libusb's descriptor structs (64-bit layouts; Go aligns the
// fields exactly as C does).
type deviceDescriptor struct {
	bLength            uint8
	bDescriptorType    uint8
	bcdUSB             uint16
	bDeviceClass       uint8
	bDeviceSubClass    uint8
	bDeviceProtocol    uint8
	bMaxPacketSize0    uint8
	idVendor           uint16
	idProduct          uint16
	bcdDevice          uint16
	iManufacturer      uint8
	iProduct           uint8
	iSerialNumber      uint8
	bNumConfigurations uint8
}

type configDescriptor struct {
	bLength             uint8
	bDescriptorType     uint8
	wTotalLength        uint16
	bNumInterfaces      uint8
	bConfigurationValue uint8
	iConfiguration      uint8
	bmAttributes        uint8
	maxPower            uint8
	iface               *libusbInterface
	extra               *byte
	extraLength         int32
}

type libusbInterface struct {
	altsetting    *interfaceDescriptor
	numAltsetting int32
}

type interfaceDescriptor struct {
	bLength            uint8
	bDescriptorType    uint8
	bInterfaceNumber   uint8
	bAlternateSetting  uint8
	bNumEndpoints      uint8
	bInterfaceClass    uint8
	bInterfaceSubClass uint8
	bInterfaceProtocol uint8
	iInterface         uint8
	endpoint           *endpointDescriptor
	extra              *byte
	extraLength        int32
}

type endpointDescriptor struct {
	bLength          uint8
	bDescriptorType  uint8
	bEndpointAddress uint8
	bmAttributes     uint8
	wMaxPacketSize   uint16
	bInterval        uint8
	bRefresh         uint8
	bSynchAddress    uint8
	extra            *byte
	extraLength      int32
}

// lib holds the bound libusb functions.
type lib struct {
	init                      func(ctx *uintptr) int32
	exit                      func(ctx uintptr)
	getDeviceList             func(ctx uintptr, list **uintptr) int
	freeDeviceList            func(list *uintptr, unref int32)
	getDeviceDescriptor       func(dev uintptr, desc *deviceDescriptor) int32
	getActiveConfigDescriptor func(dev uintptr, cfg **configDescriptor) int32
	freeConfigDescriptor      func(cfg *configDescriptor)
	getBusNumber              func(dev uintptr) uint8
	getDeviceAddress          func(dev uintptr) uint8
	open                      func(dev uintptr, handle *uintptr) int32
	close                     func(handle uintptr)
	controlTransfer           func(handle uintptr, reqType, req uint8, value, index uint16, data *byte, length uint16, timeout uint32) int32
	claimInterface            func(handle uintptr, iface int32) int32
	releaseInterface          func(handle uintptr, iface int32) int32
	setAutoDetachKernelDriver func(handle uintptr, enable int32) int32
	setOption                 func(ctx uintptr, option int32, value int32) int32
	bulkTransfer              func(handle uintptr, endpoint uint8, data *byte, length int32, transferred *int32, timeout uint32) int32
	errorName                 func(code int32) string
}

func bind(sym func(name string) (uintptr, error)) (*lib, error) {
	l := &lib{}
	required := []struct {
		fptr any
		name string
	}{
		{&l.init, "libusb_init"},
		{&l.exit, "libusb_exit"},
		{&l.getDeviceList, "libusb_get_device_list"},
		{&l.freeDeviceList, "libusb_free_device_list"},
		{&l.getDeviceDescriptor, "libusb_get_device_descriptor"},
		{&l.getActiveConfigDescriptor, "libusb_get_active_config_descriptor"},
		{&l.freeConfigDescriptor, "libusb_free_config_descriptor"},
		{&l.getBusNumber, "libusb_get_bus_number"},
		{&l.getDeviceAddress, "libusb_get_device_address"},
		{&l.open, "libusb_open"},
		{&l.close, "libusb_close"},
		{&l.controlTransfer, "libusb_control_transfer"},
		{&l.claimInterface, "libusb_claim_interface"},
		{&l.releaseInterface, "libusb_release_interface"},
		{&l.setAutoDetachKernelDriver, "libusb_set_auto_detach_kernel_driver"},
		{&l.bulkTransfer, "libusb_bulk_transfer"},
		{&l.errorName, "libusb_error_name"},
	}
	for _, f := range required {
		addr, err := sym(f.name)
		if err != nil || addr == 0 {
			return nil, fmt.Errorf("%w: missing %s", ErrNoLibusb, f.name)
		}
		purego.RegisterFunc(f.fptr, addr)
	}
	// libusb_set_option is variadic in C. It is only called on Windows, where
	// variadic integer arguments are passed exactly like fixed ones.
	if runtime.GOOS == "windows" {
		if addr, err := sym("libusb_set_option"); err == nil && addr != 0 {
			purego.RegisterFunc(&l.setOption, addr)
		}
	}
	return l, nil
}

var (
	loadOnce sync.Once
	loaded   *lib
	loadErr  error
)

func loadLib() (*lib, error) {
	loadOnce.Do(func() {
		sym, err := openLibrary()
		if err != nil {
			loadErr = err
			return
		}
		loaded, loadErr = bind(sym)
	})
	return loaded, loadErr
}

// Error is a libusb error code.
type Error struct {
	Op   string
	Code int32
	name string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Op, e.name) }

func (l *lib) err(op string, code int32) error {
	return &Error{Op: op, Code: code, name: l.errorName(code)}
}

// Context is an initialised libusb context.
type Context struct {
	l   *lib
	ctx uintptr
	// UsbDk reports whether the Windows UsbDk backend is active. Without it,
	// Windows can't reach a phone that is still bound to its MTP driver.
	UsbDk bool
	// UsbDkErr is why UsbDk could not be enabled, if it wasn't.
	UsbDkErr error
	// Log, when set, receives a timed trace of accessory setup at debug level.
	Log *slog.Logger

	hung atomic.Bool
}

func (c *Context) trace(start time.Time, msg string, args ...any) {
	if c.Log != nil {
		c.Log.Debug(msg, append([]any{"elapsed", time.Since(start).Round(time.Millisecond)}, args...)...)
	}
}

// Debug makes libusb print its own debug log to stderr (Windows only; on
// Linux and macOS set LIBUSB_DEBUG=4 instead). Set it before Open.
var Debug bool

// Open loads libusb and creates a context. On Windows it prefers the UsbDk
// backend when the UsbDk driver is installed.
func Open() (*Context, error) {
	l, err := loadLib()
	if err != nil {
		return nil, err
	}
	c := &Context{l: l}
	if Debug && l.setOption != nil {
		l.setOption(0, optionLogLevel, 4) // default for new contexts, so init is logged too
	}
	if rc := l.init(&c.ctx); rc != 0 {
		return nil, l.err("libusb_init", rc)
	}
	// Switch to UsbDk after init: libusb only detects UsbDk while
	// initialising its Windows backend, and libusb_init_context applies
	// options before that, so passing the option there always fails.
	if l.setOption != nil {
		if rc := l.setOption(c.ctx, optionUseUsbDk, 0); rc == 0 {
			c.UsbDk = true
		} else {
			c.UsbDkErr = l.err("enable UsbDk backend", rc)
		}
	}
	return c, nil
}

// Close releases the context. After a hung open it does nothing: libusb_exit
// would wait for that open forever, and the process is exiting anyway.
func (c *Context) Close() {
	if c.hung.Load() {
		return
	}
	c.l.exit(c.ctx)
}

// device is one entry from libusb_get_device_list.
type device struct {
	ptr  uintptr
	desc deviceDescriptor
	bus  uint8
	addr uint8
}

// devices returns the attached devices and a function that releases them.
func (c *Context) devices() ([]device, func()) {
	var list *uintptr
	n := c.l.getDeviceList(c.ctx, &list)
	if n <= 0 || list == nil {
		return nil, func() {}
	}
	ptrs := unsafe.Slice(list, n)
	devs := make([]device, 0, n)
	for _, p := range ptrs {
		d := device{ptr: p, bus: c.l.getBusNumber(p), addr: c.l.getDeviceAddress(p)}
		if c.l.getDeviceDescriptor(p, &d.desc) != 0 {
			continue
		}
		devs = append(devs, d)
	}
	return devs, func() { c.l.freeDeviceList(list, 1) }
}

// interfaces returns (class, subclass, protocol) for every interface of the
// device's active configuration, read from cached descriptors without
// opening the device.
func (c *Context) interfaces(dev uintptr) [][3]uint8 {
	var cfg *configDescriptor
	if c.l.getActiveConfigDescriptor(dev, &cfg) != 0 || cfg == nil {
		return nil
	}
	defer c.l.freeConfigDescriptor(cfg)
	var out [][3]uint8
	for _, itf := range unsafe.Slice(cfg.iface, cfg.bNumInterfaces) {
		for _, alt := range unsafe.Slice(itf.altsetting, itf.numAltsetting) {
			out = append(out, [3]uint8{alt.bInterfaceClass, alt.bInterfaceSubClass, alt.bInterfaceProtocol})
		}
	}
	return out
}

// bulkEndpoints finds the bulk IN and OUT endpoints of interface 0.
func (c *Context) bulkEndpoints(dev uintptr) (in, out uint8, err error) {
	var cfg *configDescriptor
	if rc := c.l.getActiveConfigDescriptor(dev, &cfg); rc != 0 || cfg == nil {
		return 0, 0, c.l.err("get config descriptor", rc)
	}
	defer c.l.freeConfigDescriptor(cfg)
	if cfg.bNumInterfaces == 0 {
		return 0, 0, errors.New("accessory has no interfaces")
	}
	itf := unsafe.Slice(cfg.iface, cfg.bNumInterfaces)[0]
	if itf.numAltsetting == 0 {
		return 0, 0, errors.New("accessory interface has no settings")
	}
	alt := unsafe.Slice(itf.altsetting, itf.numAltsetting)[0]
	for _, ep := range unsafe.Slice(alt.endpoint, alt.bNumEndpoints) {
		if ep.bmAttributes&0x03 != 0x02 { // bulk only
			continue
		}
		if ep.bEndpointAddress&0x80 != 0 {
			in = ep.bEndpointAddress
		} else {
			out = ep.bEndpointAddress
		}
	}
	if in == 0 || out == 0 {
		return 0, 0, errors.New("accessory has no bulk endpoints")
	}
	return in, out, nil
}
