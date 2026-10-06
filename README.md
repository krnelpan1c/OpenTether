# OpenTether

A free, open-source alternative to PdaNet+. OpenTether shares your Android phone's mobile data with a computer over **USB** or **Wi-Fi Direct**. The computer's traffic leaves the phone through the OpenTether app's own sockets, so it doesn't go through Android's tethering/hotspot stack.

Unlike proxy-based tools, the desktop client creates a **virtual network adapter** and carries **all** IP traffic, including **TCP, UDP, DNS, IPv6 and QUIC**. That should fix apps such as Steam, game launchers and voice chat, which fail when only TCP gets through a proxy. See [PLAN.md](PLAN.md) for the reasoning.

> **Status:** early development (Phase 1). It works end-to-end in tests, but hasn't been tested on real phones and carriers yet.
>
> You are responsible for following your carrier's terms of service.

## How it works

```
 Desktop                                                     Android phone
 ┌──────────────────────────────────────┐                   ┌──────────────────────────────┐
 │ apps → OS → virtual adapter (Wintun) │                   │ OpenTether app (foreground   │
 │        → userspace TCP/IP (gVisor)   │  QUIC over USB    │ service) → Go relay core     │
 │        → QUIC session ───────────────┼── or Wi-Fi Direct ┼→ opens real sockets bound to │
 │                                      │                   │   the mobile-data network    │
 └──────────────────────────────────────┘                   └──────────────────────────────┘
```

- **TCP**: the desktop ends each connection in gVisor's netstack. The phone opens a matching socket, and the data flows over a QUIC stream.
- **UDP**: each local socket becomes one *flow*. The phone sends from one UDP socket per flow and accepts replies from any address, which gives games endpoint-independent NAT behaviour on our side. Large datagrams use a reliable overflow stream.
- **DNS**: queries to the virtual resolver (`172.19.0.2`) go to Android's `DnsResolver` on the upstream network, so Private DNS is respected.
- **Security**: TLS 1.3 inside QUIC. The phone's certificate is pinned by fingerprint. Wi-Fi Direct sessions also need a pairing token from the QR code. USB (adb) sessions are trusted because USB debugging authorization already authenticates the link.

## Repository layout

| Path | What it is |
|---|---|
| `core/proto` | Wire protocol: stream types, address and datagram encoding |
| `core/session` | QUIC configuration, TLS identities, framed packet link (QUIC over a byte stream) |
| `core/relay` | Phone side: turns sessions into sockets |
| `core/client` | Desktop side of a session |
| `core/netstack` | Desktop userspace network stack (TUN ↔ client) |
| `core/pairing` | `opentether://pair` links shown as QR codes |
| `mobile` | gomobile API the Android app calls |
| `desktop/` | adb transport, OS network configuration (Windows/Linux/macOS), config store, runner |
| `cmd/opentether` | Desktop CLI |
| `cmd/opentether-relay` | Phone relay running on a PC, for development |
| `android/` | Kotlin + Jetpack Compose app (Material 3 Expressive) |

## Building

Requirements: Go 1.26+, a JDK 17+, the Android SDK (API 36) and NDK. Android Studio provides the SDK, NDK and JDK.

```sh
# Go tests
go test ./...

# Android: build the Go core into an AAR, then the app
scripts/build-android-core.sh          # or scripts\build-android-core.ps1 on Windows
cd android && ./gradlew :app:assembleDebug

# Desktop client (+ wintun.dll on Windows)
scripts/build-desktop.sh               # or scripts\build-desktop.ps1
```

## Using it

**USB**

USB uses adb, so **USB debugging must be on**.

1. On the phone, turn on USB debugging (Settings → Developer options), open OpenTether and turn on **USB**.
2. Connect the phone with a cable.
3. On the computer, from an Administrator terminal (or with `sudo`), run `opentether usb`. Accept the USB debugging prompt on the phone if it appears.

`opentether devices` lists the phones adb can see.

*Experimental:* `opentether usb --experimental-aoa` uses Android Open Accessory mode instead, which doesn't need developer options. It's hidden from `--help` because it isn't reliable on Windows yet. It needs [UsbDk](https://github.com/daynix/UsbDk/releases) and USB debugging off, and in testing it could leave the phone's USB connection stuck until a replug or reboot. Linux needs the `libusb-1.0-0` package and macOS needs `brew install libusb`.

**Wi-Fi Direct**

1. On the phone, turn on **Wi-Fi Direct**. A network name, password and QR code appear.
2. Copy the pairing command from the phone and run it on the computer: `opentether wifi --pair "opentether://pair?..."`. The computer joins the phone's network by itself and switches back to your previous Wi-Fi when you stop. Pass `--no-join` to join manually. After the first time, `opentether wifi` is enough.

Add `--stats` to see live throughput. `opentether forget` clears paired phones.

**Devices without the client (proxy mode)**: phones, tablets, consoles and TVs can't run the desktop client.

1. On the host phone, turn on **Wi-Fi Direct** and the **Devices without the app** switch.
2. On the other device, join the `DIRECT-OT-…` network.
3. Edit that network, open **Advanced options → Proxy → Manual**, and enter `192.168.49.1` port `8080`. You can use **Proxy Auto-Config** with `http://192.168.49.1:8080/proxy.pac` instead.

The proxy speaks HTTP (including CONNECT) and SOCKS5 with UDP ASSOCIATE. Like PdaNet's Wi-Fi mode, it only helps apps that use proxy settings.

**Developing without a phone**: run `opentether-relay` on any machine and point the client at it with `opentether usb --endpoint 127.0.0.1:47101`.

## License

To be decided before outside contributions are accepted (GPL-3.0 or Apache-2.0; see PLAN.md §6).
