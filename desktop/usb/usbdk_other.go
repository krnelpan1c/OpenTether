//go:build !windows

package usb

// UsbDkInstalled reports whether the UsbDk driver service exists; UsbDk is
// Windows-only.
func UsbDkInstalled() bool { return false }
