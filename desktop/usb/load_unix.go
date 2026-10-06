//go:build linux || darwin

package usb

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ebitengine/purego"
)

// openLibrary loads the system libusb (Linux: the libusb-1.0-0 package;
// macOS: `brew install libusb`), or a copy next to the executable.
func openLibrary() (func(string) (uintptr, error), error) {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(dir, "libusb-1.0.so.0"), filepath.Join(dir, "libusb-1.0.0.dylib"))
	}
	if runtime.GOOS == "darwin" {
		candidates = append(candidates,
			"/opt/homebrew/lib/libusb-1.0.0.dylib",
			"/usr/local/lib/libusb-1.0.0.dylib",
			"libusb-1.0.0.dylib")
	} else {
		candidates = append(candidates, "libusb-1.0.so.0", "libusb-1.0.so")
	}
	var lastErr error
	for _, path := range candidates {
		h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			lastErr = err
			continue
		}
		return func(name string) (uintptr, error) { return purego.Dlsym(h, name) }, nil
	}
	hint := "install the libusb-1.0-0 package"
	if runtime.GOOS == "darwin" {
		hint = "run: brew install libusb"
	}
	return nil, fmt.Errorf("%w (%s): %v", ErrNoLibusb, hint, lastErr)
}
