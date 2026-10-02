//go:build tinygo

package main

import (
	"device/nrf"
	"machine"
	"unsafe"
)

// The HC-SR04 echo pulse is timed in hardware, so BLE interrupts cannot
// stretch it. TIMER1 runs at 1 MHz. GPIOTE channel 7 fires on both edges of
// the echo pin. PPI channel 0 captures the first edge into CC[0] and then
// disables itself (group 0). PPI channel 1 captures every edge into CC[1],
// so after the falling edge CC[1]-CC[0] is the pulse width.
// The S113 SoftDevice keeps PPI channels 17-31, groups 4-5 and TIMER0
// (nrf_soc.h, NRF_SOC_SD_PPI_*_SD_ENABLED_MSK).
const (
	sonarTrig     = machine.P16
	sonarEcho     = machine.P15
	gpioteCh      = 7
	ppiFirst      = 0
	ppiEvery      = 1
	ppiGroup      = 0
	echoMaxUs     = 30000 // HC-SR04 holds echo ~38 ms without an object
	sonarSettleMs = 45
)

var timer = nrf.TIMER1

func regAddr(r *uint32) uint32 { return uint32(uintptr(unsafe.Pointer(r))) }

func sonarInit() {
	sonarTrig.Configure(machine.PinConfig{Mode: machine.PinOutput})
	sonarTrig.Low()

	timer.TASKS_STOP.Set(1)
	timer.MODE.Set(nrf.TIMER_MODE_MODE_Timer)
	timer.BITMODE.Set(nrf.TIMER_BITMODE_BITMODE_32Bit)
	timer.PRESCALER.Set(4) // 16 MHz / 2^4 = 1 MHz
	timer.TASKS_CLEAR.Set(1)
	timer.TASKS_START.Set(1)

	pin := uint32(sonarEcho)
	nrf.GPIOTE.CONFIG[gpioteCh].Set(nrf.GPIOTE_CONFIG_MODE_Event<<nrf.GPIOTE_CONFIG_MODE_Pos |
		(pin&31)<<nrf.GPIOTE_CONFIG_PSEL_Pos |
		(pin>>5)<<nrf.GPIOTE_CONFIG_PORT_Pos |
		nrf.GPIOTE_CONFIG_POLARITY_Toggle<<nrf.GPIOTE_CONFIG_POLARITY_Pos)

	ev := regAddr(&nrf.GPIOTE.EVENTS_IN[gpioteCh].Reg)
	nrf.PPI.CH[ppiFirst].EEP.Set(ev)
	nrf.PPI.CH[ppiFirst].TEP.Set(regAddr(&timer.TASKS_CAPTURE[0].Reg))
	nrf.PPI.FORK[ppiFirst].TEP.Set(regAddr(&nrf.PPI.TASKS_CHG[ppiGroup].DIS.Reg))
	nrf.PPI.CH[ppiEvery].EEP.Set(ev)
	nrf.PPI.CH[ppiEvery].TEP.Set(regAddr(&timer.TASKS_CAPTURE[1].Reg))
	nrf.PPI.CHG[ppiGroup].Set(1 << ppiFirst)
}

func now() uint32 {
	timer.TASKS_CAPTURE[2].Set(1)
	return timer.CC[2].Get()
}

// sonarTrigger arms the capture and sends the 10 µs trigger pulse.
func sonarTrigger() {
	nrf.PPI.CHENCLR.Set(1<<ppiFirst | 1<<ppiEvery)
	timer.CC[0].Set(0)
	timer.CC[1].Set(0)
	nrf.GPIOTE.EVENTS_IN[gpioteCh].Set(0)
	nrf.PPI.CHENSET.Set(1<<ppiFirst | 1<<ppiEvery)

	sonarTrig.High()
	t := now()
	for now()-t < 10 {
	}
	sonarTrig.Low()
}

// sonarResult reads the echo after sonarSettleMs. 0 means no complete echo.
func sonarResult() uint16 {
	firstSeen := nrf.PPI.CHEN.Get()&(1<<ppiFirst) == 0
	nrf.PPI.CHENCLR.Set(1<<ppiFirst | 1<<ppiEvery)
	if !firstSeen || sonarEcho.Get() {
		return 0
	}
	d := timer.CC[1].Get() - timer.CC[0].Get()
	if d == 0 || d > echoMaxUs {
		return 0
	}
	return uint16(d)
}
