package usb

import "golang.org/x/sys/windows"

// UsbDkInstalled reports whether the UsbDk driver service exists.
func UsbDkInstalled() bool {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false
	}
	defer windows.CloseServiceHandle(scm)
	name, _ := windows.UTF16PtrFromString("UsbDk")
	svc, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false
	}
	windows.CloseServiceHandle(svc)
	return true
}
