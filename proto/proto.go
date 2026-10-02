// Package proto is the BLE protocol between the TPBot micro:bit firmware
// and its clients (the laptop tool, later Stack-chan). See readme.md.
package proto

const (
	ServiceUUID = "3d440001-87fb-42e7-9a68-7596af5168a0"
	CmdUUID     = "3d440002-87fb-42e7-9a68-7596af5168a0" // write, write without response
	StateUUID   = "3d440003-87fb-42e7-9a68-7596af5168a0" // read, notify
	InfoUUID    = "3d440004-87fb-42e7-9a68-7596af5168a0" // read

	// NamePrefix starts the advertised name, e.g. "TPB-1a2b".
	NamePrefix = "TPB-"
)

// Command opcodes: the first byte of a write to the command characteristic.
const (
	OpDrive      = 0x01 // int8 left, int8 right: -100..100
	OpStop       = 0x02 // none
	OpServo      = 0x03 // uint8 port 1..4, uint8 angle 0..180
	OpHeadlights = 0x04 // uint8 r, g, b
	OpWatchdog   = 0x05 // uint16 LE ms: WatchdogMinMs..WatchdogMaxMs
	OpSonar      = 0x06 // uint8 rate in Hz: 0 (off)..SonarMaxHz
	OpBoard      = 0x07 // uint8 BoardBoth, BoardV1 or BoardV2: which TPBot frame format to send
	OpI2C        = 0x7F // debug: raw bytes written to the TPBot controller (I2C 0x10)
)

// TPBot frame formats (OpBoard). See firmware/tpbot.go.
const (
	BoardBoth = 0
	BoardV1   = 1
	BoardV2   = 2
)

const (
	WatchdogDefaultMs = 500
	WatchdogMinMs     = 100
	WatchdogMaxMs     = 10000
	SonarDefaultHz    = 10
	SonarMaxHz        = 20
)

// State is the 14-byte value of the state characteristic.
const (
	StateLen    = 14
	StateFormat = 2
)

// Input bits in State.Inputs.
const (
	InLineLeft  = 1 << 0 // raw level of P13
	InLineRight = 1 << 1 // raw level of P14
	InButtonA   = 1 << 2 // pressed
	InButtonB   = 1 << 3 // pressed
)

// Flag bits in State.Flags.
const (
	FlagWatchdogStop = 1 << 0 // the watchdog stopped the motors; cleared by the next drive
)

type State struct {
	Seq    uint8
	Ms     uint32 // micro:bit uptime
	EchoUs uint16 // sonar echo pulse in µs, 0 = no echo or sonar off
	Inputs uint8
	Left   int8 // motor speed in effect
	Right  int8
	Flags  uint8
	I2CErr uint8 // failed I2C writes to the TPBot controller since boot (wraps)
	Board  uint8 // BoardBoth, BoardV1 or BoardV2
}

func (s *State) Encode(b *[StateLen]byte) {
	b[0] = StateFormat
	b[1] = s.Seq
	b[2] = byte(s.Ms)
	b[3] = byte(s.Ms >> 8)
	b[4] = byte(s.Ms >> 16)
	b[5] = byte(s.Ms >> 24)
	b[6] = byte(s.EchoUs)
	b[7] = byte(s.EchoUs >> 8)
	b[8] = s.Inputs
	b[9] = byte(s.Left)
	b[10] = byte(s.Right)
	b[11] = s.Flags
	b[12] = s.I2CErr
	b[13] = s.Board
}

func DecodeState(b []byte) (State, bool) {
	if len(b) < StateLen || b[0] != StateFormat {
		return State{}, false
	}
	return State{
		Seq:    b[1],
		Ms:     uint32(b[2]) | uint32(b[3])<<8 | uint32(b[4])<<16 | uint32(b[5])<<24,
		EchoUs: uint16(b[6]) | uint16(b[7])<<8,
		Inputs: b[8],
		Left:   int8(b[9]),
		Right:  int8(b[10]),
		Flags:  b[11],
		I2CErr: b[12],
		Board:  b[13],
	}, true
}

func Drive(left, right int8) []byte { return []byte{OpDrive, byte(left), byte(right)} }
func Stop() []byte                  { return []byte{OpStop} }
func Servo(port, angle uint8) []byte {
	return []byte{OpServo, port, angle}
}
func Headlights(r, g, b uint8) []byte { return []byte{OpHeadlights, r, g, b} }
func Watchdog(ms uint16) []byte       { return []byte{OpWatchdog, byte(ms), byte(ms >> 8)} }
func Sonar(hz uint8) []byte           { return []byte{OpSonar, hz} }
func Board(mode uint8) []byte         { return []byte{OpBoard, mode} }
func I2C(raw ...byte) []byte          { return append([]byte{OpI2C}, raw...) }
