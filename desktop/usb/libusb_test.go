package usb

import (
	"errors"
	"testing"
	"unsafe"
)

// The descriptor mirrors must match libusb's 64-bit C layouts exactly.
func TestStructLayouts(t *testing.T) {
	checks := []struct {
		name      string
		got, want uintptr
	}{
		{"sizeof(libusb_device_descriptor)", unsafe.Sizeof(deviceDescriptor{}), 18},
		{"device_descriptor.idVendor", unsafe.Offsetof(deviceDescriptor{}.idVendor), 8},
		{"device_descriptor.bNumConfigurations", unsafe.Offsetof(deviceDescriptor{}.bNumConfigurations), 17},
		{"config_descriptor.interface", unsafe.Offsetof(configDescriptor{}.iface), 16},
		{"config_descriptor.extra_length", unsafe.Offsetof(configDescriptor{}.extraLength), 32},
		{"sizeof(libusb_interface)", unsafe.Sizeof(libusbInterface{}), 16},
		{"interface.num_altsetting", unsafe.Offsetof(libusbInterface{}.numAltsetting), 8},
		{"interface_descriptor.endpoint", unsafe.Offsetof(interfaceDescriptor{}.endpoint), 16},
		{"sizeof(libusb_interface_descriptor)", unsafe.Sizeof(interfaceDescriptor{}), 40},
		{"endpoint_descriptor.wMaxPacketSize", unsafe.Offsetof(endpointDescriptor{}.wMaxPacketSize), 4},
		{"endpoint_descriptor.extra", unsafe.Offsetof(endpointDescriptor{}.extra), 16},
		{"sizeof(libusb_endpoint_descriptor)", unsafe.Sizeof(endpointDescriptor{}), 32},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestEnumerate loads the real libusb when available and lists devices. It
// never opens a device.
func TestEnumerate(t *testing.T) {
	ctx, err := Open()
	if errors.Is(err, ErrNoLibusb) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.Close()
	devs := ctx.Devices()
	for _, d := range devs {
		t.Logf("%s phone=%v accessory=%v", d, d.Phone, d.Accessory)
	}
	t.Logf("%d devices, UsbDk=%v", len(devs), ctx.UsbDk)
}
