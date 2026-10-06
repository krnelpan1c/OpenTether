package usb

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// openLibrary loads libusb-1.0.dll from next to opentether.exe (shipped by
// scripts/build-desktop.ps1) or System32. The default search directories
// deliberately exclude the current directory.
func openLibrary() (func(string) (uintptr, error), error) {
	h, err := windows.LoadLibraryEx("libusb-1.0.dll", 0, windows.LOAD_LIBRARY_SEARCH_DEFAULT_DIRS)
	if err != nil {
		return nil, fmt.Errorf("%w (put libusb-1.0.dll next to opentether.exe): %v", ErrNoLibusb, err)
	}
	return func(name string) (uintptr, error) { return windows.GetProcAddress(h, name) }, nil
}
