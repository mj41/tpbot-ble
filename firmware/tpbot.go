//go:build tinygo

package main

import (
	"machine"
	"time"

	"github.com/mj41/tpbot-ble/proto"
)

// The TPBot board controller listens on the edge connector I2C bus (P19/P20).
// Frames follow ELECFREAKS' MakeCode extension (github.com/elecfreaks/pxt-tpbot,
// V1.ts and V2.ts). The extension sends both formats, but a V1 board does not
// stop its motors reliably then (tested 2026-10-01), so V1 is the default and
// OpBoard picks another.
const tpbotAddr = 0x10

var bus = machine.I2C1

func tpbotInit() error {
	return bus.Configure(machine.I2CConfig{
		Frequency: 100 * machine.KHz,
		SCL:       machine.SCL1_PIN,
		SDA:       machine.SDA1_PIN,
	})
}

var (
	v1buf     [4]byte
	v2buf     [9]byte
	boardMode byte = proto.BoardV1
	i2cErrors byte
)

// tx writes one frame, with one retry. The pause between frames gives the
// board controller time to take the previous one.
func tx(b []byte) {
	if bus.Tx(tpbotAddr, b, nil) != nil {
		i2cErrors++
		time.Sleep(time.Millisecond)
		if bus.Tx(tpbotAddr, b, nil) != nil {
			i2cErrors++
		}
	}
	time.Sleep(2 * time.Millisecond)
}

func sendV1(a, b, c, d byte) {
	if boardMode == proto.BoardV2 {
		return
	}
	v1buf = [4]byte{a, b, c, d}
	tx(v1buf[:])
}

func sendV2(cmd byte, params ...byte) {
	if boardMode == proto.BoardV1 {
		return
	}
	v2buf[0], v2buf[1], v2buf[2], v2buf[3] = 0xFF, 0xF9, cmd, byte(len(params))
	n := copy(v2buf[4:], params)
	tx(v2buf[:4+n])
}

// rawI2C writes debug bytes to the board controller as they are.
func rawI2C(b []byte) { tx(b) }

func clamp100(v int8) int8 {
	if v > 100 {
		return 100
	}
	if v < -100 {
		return -100
	}
	return v
}

func abs8(v int8) byte {
	if v < 0 {
		return byte(-v)
	}
	return byte(v)
}

// motors sets both wheels, -100..100.
func motors(left, right int8) {
	left, right = clamp100(left), clamp100(right)
	var dir byte
	if left < 0 {
		dir |= 1
	}
	if right < 0 {
		dir |= 2
	}
	l, r := abs8(left), abs8(right)
	sendV1(0x01, l, r, dir)
	sendV2(0x10, l, r, dir)
}

// servo sets port 1..4 to angle 0..180.
func servo(port, angle byte) {
	if port < 1 || port > 4 {
		return
	}
	if angle > 180 {
		angle = 180
	}
	sendV1(0x10+port-1, angle, 0, 0)
	sendV2(0x20, port, angle)
}

func headlights(r, g, b byte) {
	sendV1(0x20, r, g, b)
	sendV2(0x30, r, g, b)
}
