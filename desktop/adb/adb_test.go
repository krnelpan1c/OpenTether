package adb

import "testing"

func TestParseDevices(t *testing.T) {
	out := `* daemon not running; starting now at tcp:5037
* daemon started successfully
List of devices attached
R5CT1234ABC            device usb:1-1 product:e3qxxx model:SM_S928B device:e3q transport_id:1
emulator-5554          unauthorized transport_id:2

`
	devs := parseDevices(out)
	if len(devs) != 2 {
		t.Fatalf("got %d devices", len(devs))
	}
	if devs[0].Serial != "R5CT1234ABC" || devs[0].State != "device" || devs[0].Model != "SM S928B" {
		t.Fatalf("bad first device %+v", devs[0])
	}
	if devs[1].State != "unauthorized" {
		t.Fatalf("bad second device %+v", devs[1])
	}
}
