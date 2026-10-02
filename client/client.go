// Package client connects to a TPBot micro:bit over BLE from a host
// (Linux BlueZ, macOS, Windows) with tinygo.org/x/bluetooth.
package client

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mj41/tpbot-ble/proto"
	"tinygo.org/x/bluetooth"
)

// Match picks the car to connect to. Empty fields match any TPBot.
type Match struct {
	Name string // advertised name, e.g. "TPB-1a2b"
	Addr string // BLE address
}

func (m Match) ok(r bluetooth.ScanResult) bool {
	name := r.LocalName()
	switch {
	case m.Addr != "":
		return strings.EqualFold(r.Address.String(), m.Addr)
	case m.Name != "":
		return name == m.Name
	}
	return strings.HasPrefix(name, proto.NamePrefix)
}

type Bot struct {
	Name, Addr string

	dev              bluetooth.Device
	cmd, state, info bluetooth.DeviceCharacteristic

	mu       sync.Mutex
	lastNote time.Time
}

// Scan reports every TPBot seen until timeout.
func Scan(adapter *bluetooth.Adapter, timeout time.Duration, found func(name, addr string, rssi int16)) error {
	seen := map[string]bool{}
	t := time.AfterFunc(timeout, func() { adapter.StopScan() })
	defer t.Stop()
	return adapter.Scan(func(_ *bluetooth.Adapter, r bluetooth.ScanResult) {
		if strings.HasPrefix(r.LocalName(), proto.NamePrefix) && !seen[r.Address.String()] {
			seen[r.Address.String()] = true
			found(r.LocalName(), r.Address.String(), r.RSSI)
		}
	})
}

// Connect scans up to timeout for a matching car and connects to it.
func Connect(adapter *bluetooth.Adapter, m Match, timeout time.Duration) (*Bot, error) {
	var found *bluetooth.ScanResult
	t := time.AfterFunc(timeout, func() { adapter.StopScan() })
	err := adapter.Scan(func(a *bluetooth.Adapter, r bluetooth.ScanResult) {
		if m.ok(r) {
			found = &r
			a.StopScan()
		}
	})
	t.Stop()
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("no TPBot found in %s (is it powered and not connected elsewhere?)", timeout)
	}
	dev, err := adapter.Connect(found.Address, bluetooth.ConnectionParams{})
	if err != nil {
		return nil, err
	}
	b := &Bot{Name: found.LocalName(), Addr: found.Address.String(), dev: dev}
	if err := b.discover(); err != nil {
		dev.Disconnect()
		return nil, err
	}
	return b, nil
}

func (b *Bot) discover() error {
	svcU, _ := bluetooth.ParseUUID(proto.ServiceUUID)
	cmdU, _ := bluetooth.ParseUUID(proto.CmdUUID)
	stateU, _ := bluetooth.ParseUUID(proto.StateUUID)
	infoU, _ := bluetooth.ParseUUID(proto.InfoUUID)
	svcs, err := b.dev.DiscoverServices([]bluetooth.UUID{svcU})
	if err != nil || len(svcs) == 0 {
		return fmt.Errorf("service not found: %v", err)
	}
	chars, err := svcs[0].DiscoverCharacteristics([]bluetooth.UUID{cmdU, stateU, infoU})
	if err != nil || len(chars) != 3 {
		return fmt.Errorf("characteristics not found: %v", err)
	}
	for _, c := range chars {
		switch c.UUID() {
		case cmdU:
			b.cmd = c
		case stateU:
			b.state = c
		case infoU:
			b.info = c
		}
	}
	return nil
}

func (b *Bot) Close() error { return b.dev.Disconnect() }

// Send writes one command (see proto) without waiting for a response.
func (b *Bot) Send(p []byte) error {
	_, err := b.cmd.WriteWithoutResponse(p)
	return err
}

func (b *Bot) Info() (string, error) {
	buf := make([]byte, 64)
	n, err := b.info.Read(buf)
	if err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}

// Notify calls f for every state notification. Call it once.
func (b *Bot) Notify(f func(proto.State)) error {
	b.mu.Lock()
	b.lastNote = time.Now()
	b.mu.Unlock()
	return b.state.EnableNotifications(func(buf []byte) {
		b.mu.Lock()
		b.lastNote = time.Now()
		b.mu.Unlock()
		if s, ok := proto.DecodeState(buf); ok {
			f(s)
		}
	})
}

// Silent is how long the car has sent no notification. The firmware notifies
// at least every 200 ms, so a few hundred ms more means the link is gone.
func (b *Bot) Silent() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return time.Since(b.lastNote)
}

var ErrLost = errors.New("no state notifications: BLE link lost")
