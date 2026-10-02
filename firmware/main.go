//go:build tinygo

// Firmware for a micro:bit V2 in an ELECFREAKS TPBot: a BLE peripheral that
// drives the car and reports its raw sensors. Protocol: package proto.
package main

import (
	"device/nrf"
	"machine"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mj41/tpbot-ble/proto"
	"tinygo.org/x/bluetooth"
)

const version = "tpbot-ble 0.3.1"

// BLE callbacks run in the SoftDevice interrupt (SWI2): they may not allocate
// or use I2C. They only fill this queue, which the main loop drains.
const (
	qLen   = 8
	cmdMax = 20
)

type cmdSlot struct {
	n byte
	b [cmdMax]byte
}

var (
	cmdQ      [qLen]cmdSlot
	qHead     atomic.Uint32 // written by the interrupt
	qTail     atomic.Uint32 // written by the main loop
	connected atomic.Bool
	connEvent atomic.Uint32 // counts connects and disconnects
)

// Central allowlist, set at build time (make: ALLOW, or ~/.config/tpbot-ble/allow):
// comma-separated addresses such as "0A:1B:2C:3D:4E:60". Empty: any central may connect.
// Addresses can be spoofed; LE Secure Connections bonding is the stronger, later step.
var allowList string

var (
	allowed    []bluetooth.MAC // parsed once in main, read-only afterwards
	peer       [6]byte         // the connected central's address, set in onConnect
	peerOK     atomic.Bool     // that central is on the allowlist (or the list is empty)
	lastReject uint32          // ms; RemoveBond erases a flash page, so at most every 10 s
)

func onWrite(_ bluetooth.Connection, offset int, value []byte) {
	if !peerOK.Load() {
		return // not an allowed central: no command is taken
	}
	h := qHead.Load()
	if offset != 0 || len(value) == 0 || h-qTail.Load() >= qLen {
		return
	}
	s := &cmdQ[h%qLen]
	s.n = byte(copy(s.b[:], value))
	qHead.Store(h + 1)
}

func onConnect(d bluetooth.Device, c bool) {
	if c {
		peer = d.Address.MAC
		ok := len(allowed) == 0
		for _, a := range allowed {
			if a == d.Address.MAC {
				ok = true
			}
		}
		peerOK.Store(ok)
	} else {
		peerOK.Store(false)
	}
	connected.Store(c)
	connEvent.Add(1)
}

func macString(m [6]byte) string {
	const digits = "0123456789ABCDEF"
	b := make([]byte, 0, 17)
	for i := 5; i >= 0; i-- {
		b = append(b, digits[m[i]>>4], digits[m[i]&15])
		if i > 0 {
			b = append(b, ':')
		}
	}
	return string(b)
}

var (
	lineL   = machine.P13
	lineR   = machine.P14
	buttonA = machine.BUTTONA
	buttonB = machine.BUTTONB
)

func readInputs() uint8 {
	var in uint8
	if lineL.Get() {
		in |= proto.InLineLeft
	}
	if lineR.Get() {
		in |= proto.InLineRight
	}
	if !buttonA.Get() {
		in |= proto.InButtonA
	}
	if !buttonB.Get() {
		in |= proto.InButtonB
	}
	return in
}

// Center LED of the matrix: row 3 high and column 3 low light it, with no
// multiplexing needed for a single pixel.
func ledInit() {
	for _, p := range []machine.Pin{machine.LED_ROW_1, machine.LED_ROW_2, machine.LED_ROW_3, machine.LED_ROW_4, machine.LED_ROW_5} {
		p.Configure(machine.PinConfig{Mode: machine.PinOutput})
		p.Low()
	}
	for _, p := range []machine.Pin{machine.LED_COL_1, machine.LED_COL_2, machine.LED_COL_3, machine.LED_COL_4, machine.LED_COL_5} {
		p.Configure(machine.PinConfig{Mode: machine.PinOutput})
		p.High()
	}
	machine.LED_ROW_3.High()
}

func led(on bool) { machine.LED_COL_3.Set(!on) }

func hex4(v uint16) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[v>>12&15], digits[v>>8&15], digits[v>>4&15], digits[v&15]})
}

func must(what string, err error) {
	if err != nil {
		for {
			println("failed to", what+":", err.Error())
			time.Sleep(time.Second)
		}
	}
}

func main() {
	ledInit()
	for _, p := range []machine.Pin{lineL, lineR, buttonA, buttonB} {
		p.Configure(machine.PinConfig{Mode: machine.PinInput})
	}
	if err := tpbotInit(); err != nil {
		println("i2c:", err.Error())
	}
	motors(0, 0)
	sonarInit()

	name := proto.NamePrefix + hex4(uint16(nrf.FICR.DEVICEID[0].Get()))
	println(version, name)
	for _, f := range strings.Split(allowList, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		if m, err := bluetooth.ParseMAC(f); err == nil {
			allowed = append(allowed, m)
		} else {
			println("allowlist: bad address", f)
		}
	}
	if len(allowed) == 0 {
		println("allowlist: empty, any central may connect")
	} else {
		println("allowlist:", len(allowed), "centrals")
	}

	adapter := bluetooth.DefaultAdapter
	must("enable BLE", adapter.Enable())
	adapter.SetConnectHandler(onConnect)

	var stateChar, infoChar bluetooth.Characteristic
	svc, _ := bluetooth.ParseUUID(proto.ServiceUUID)
	cmdU, _ := bluetooth.ParseUUID(proto.CmdUUID)
	stateU, _ := bluetooth.ParseUUID(proto.StateUUID)
	infoU, _ := bluetooth.ParseUUID(proto.InfoUUID)
	var stateBuf [proto.StateLen]byte
	must("add service", adapter.AddService(&bluetooth.Service{
		UUID: svc,
		Characteristics: []bluetooth.CharacteristicConfig{
			{
				UUID:       cmdU,
				Flags:      bluetooth.CharacteristicWritePermission | bluetooth.CharacteristicWriteWithoutResponsePermission,
				WriteEvent: onWrite,
			},
			{
				Handle: &stateChar,
				UUID:   stateU,
				Value:  stateBuf[:],
				Flags:  bluetooth.CharacteristicReadPermission | bluetooth.CharacteristicNotifyPermission,
			},
			{
				Handle: &infoChar,
				UUID:   infoU,
				Value:  []byte(version),
				Flags:  bluetooth.CharacteristicReadPermission,
			},
		},
	}))

	adv := adapter.DefaultAdvertisement()
	must("configure advertising", adv.Configure(bluetooth.AdvertisementOptions{
		LocalName:    name,
		ServiceUUIDs: []bluetooth.UUID{svc},
		Interval:     bluetooth.NewDuration(100 * time.Millisecond),
	}))
	must("start advertising", adv.Start())

	var (
		st         = proto.State{Board: boardMode}
		start      = time.Now()
		watchdogMs = uint32(proto.WatchdogDefaultMs)
		sonarHz    = uint32(proto.SonarDefaultHz)
		lastDrive  uint32
		lastNotify uint32
		lastConn   uint32
		lastMotors uint32 // last time the motor values went to the board
		sonarAt    uint32 // when the running measurement was triggered
		sonarBusy  bool
		dirty      bool
	)
	ms := func() uint32 { return uint32(time.Since(start).Milliseconds()) }
	setMotors := func(l, r int8) {
		l, r = clamp100(l), clamp100(r)
		motors(l, r)
		lastMotors = ms()
		if l != st.Left || r != st.Right {
			dirty = true
		}
		st.Left, st.Right = l, r
	}

	for {
		t := ms()

		if e := connEvent.Load(); e != lastConn {
			lastConn = e
			setMotors(0, 0) // a new or lost central always starts from a stopped car
			dirty = true
			if connected.Load() {
				if peerOK.Load() {
					println("central", macString(peer), "allowed")
				} else {
					println("central", macString(peer), "refused")
				}
			}
		}
		if connected.Load() && !peerOK.Load() && (lastReject == 0 || t-lastReject > 10000) {
			lastReject = t
			adapter.RemoveBond() // the only peripheral-side disconnect in this BLE stack; no bonds are used
		}

		for qTail.Load() != qHead.Load() {
			s := &cmdQ[qTail.Load()%qLen]
			c := s.b[:s.n]
			switch {
			case c[0] == proto.OpDrive && len(c) >= 3:
				setMotors(int8(c[1]), int8(c[2]))
				lastDrive = t
				if st.Flags&proto.FlagWatchdogStop != 0 {
					st.Flags &^= proto.FlagWatchdogStop
					dirty = true
				}
			case c[0] == proto.OpStop:
				setMotors(0, 0)
			case c[0] == proto.OpServo && len(c) >= 3:
				servo(c[1], c[2])
			case c[0] == proto.OpHeadlights && len(c) >= 4:
				headlights(c[1], c[2], c[3])
			case c[0] == proto.OpWatchdog && len(c) >= 3:
				v := uint32(c[1]) | uint32(c[2])<<8
				if v >= proto.WatchdogMinMs && v <= proto.WatchdogMaxMs {
					watchdogMs = v
				}
			case c[0] == proto.OpBoard && len(c) >= 2 && c[1] <= proto.BoardV2:
				boardMode = c[1]
				st.Board = boardMode
				setMotors(st.Left, st.Right)
				dirty = true
			case c[0] == proto.OpI2C && len(c) >= 2:
				rawI2C(c[1:])
			case c[0] == proto.OpSonar && len(c) >= 2:
				if c[1] <= proto.SonarMaxHz {
					sonarHz = uint32(c[1])
				}
				if sonarHz == 0 {
					sonarBusy = false // a measurement still running must not report after "off"
					if st.EchoUs != 0 {
						st.EchoUs = 0
						dirty = true
					}
				}
			}
			qTail.Add(1)
		}

		if (st.Left != 0 || st.Right != 0) && t-lastDrive > watchdogMs {
			setMotors(0, 0)
			st.Flags |= proto.FlagWatchdogStop
			dirty = true
		}

		// Send the motor values again every 200 ms, stop included: a frame the
		// board missed cannot leave the wheels turning.
		if t-lastMotors >= 200 {
			setMotors(st.Left, st.Right)
		}
		if i2cErrors != st.I2CErr {
			st.I2CErr = i2cErrors
			dirty = true
		}

		if sonarBusy && t-sonarAt >= sonarSettleMs {
			sonarBusy = false
			st.EchoUs = sonarResult()
			dirty = true
		}
		if !sonarBusy && sonarHz > 0 && connected.Load() && t-sonarAt >= 1000/sonarHz {
			sonarTrigger()
			sonarAt, sonarBusy = t, true
		}

		if in := readInputs(); in != st.Inputs {
			st.Inputs = in
			dirty = true
		}

		// Notify on every change, and at least every 200 ms as a heartbeat.
		if dirty || t-lastNotify >= 200 {
			st.Seq++
			st.Ms = t
			st.Encode(&stateBuf)
			if peerOK.Load() || !connected.Load() {
				stateChar.Write(stateBuf[:]) // a refused central gets no state either
			}
			lastNotify, dirty = t, false
		}

		if connected.Load() && !peerOK.Load() {
			led(t%200 < 100) // a refused central: fast blink
		} else if connected.Load() {
			led(true)
		} else {
			led(t%1000 < 100)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
