package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/krnelpan1c/OpenTether/core/session"
	"github.com/krnelpan1c/OpenTether/desktop/usb"
)

// AOALink reaches the phone through the Android Open Accessory protocol,
// which needs neither USB debugging nor adb.
type AOALink struct {
	Log *slog.Logger

	once sync.Once
	ctx  *usb.Context
	err  error
	// acc stays open across attempts: reopening it can reset the phone out
	// of accessory mode.
	acc *usb.Accessory
}

func (l *AOALink) Key() string   { return "" } // trusted physical link, not pinned
func (l *AOALink) Token() string { return "" }
func (l *AOALink) Pin() string   { return "" }

func (l *AOALink) Connect(ctx context.Context) (net.PacketConn, net.Addr, bool, error) {
	l.once.Do(func() {
		l.ctx, l.err = usb.Open()
		if l.ctx != nil {
			l.ctx.Log = l.Log
		}
	})
	if l.err != nil {
		return nil, nil, false, l.err
	}
	if l.acc != nil && l.acc.Err() != nil {
		l.acc.Close() // unplugged or failed; start over
		l.acc = nil
	}
	if l.acc == nil {
		wasAccessory := l.ctx.AccessoryPresent()
		acc, err := l.ctx.OpenAccessory(ctx)
		if err != nil {
			return nil, nil, false, err
		}
		if acc == nil {
			return nil, nil, false, usb.ErrNoPhone
		}
		if !wasAccessory && l.Log != nil {
			l.Log.Info("phone switched to USB accessory mode; accept the prompt on the phone if one appears")
		}
		l.acc = acc
	}
	pc := session.NewFramedLink(l.acc.Session(), session.LinkOptions{
		Name:            "usb-accessory",
		TransferFraming: true,
		ReadSize:        64 << 10,
		BatchWrites:     true,
		PadMultiple:     512,
	})
	return pc, pc.RemoteAddr(), true, nil
}

// DialHint explains a failed handshake: the accessory link is up, so the
// phone app is what isn't answering.
func (l *AOALink) DialHint(err error) error {
	return fmt.Errorf("the phone is in USB accessory mode but the OpenTether app has not answered: "+
		"open the app, turn on USB, and accept any prompt on the phone (%w)", err)
}

func (l *AOALink) Close() {
	if l.acc != nil {
		l.acc.Close()
		l.acc = nil
	}
	if l.ctx != nil {
		l.ctx.Close()
	}
}

// dialHinter lets a link explain a failed handshake in its own terms.
type dialHinter interface {
	DialHint(err error) error
}
