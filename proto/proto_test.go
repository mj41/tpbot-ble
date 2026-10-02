package proto

import "testing"

func TestStateRoundTrip(t *testing.T) {
	in := State{Seq: 7, Ms: 0x01020304, EchoUs: 1160, Inputs: InLineLeft | InButtonB, Left: -100, Right: 55, Flags: FlagWatchdogStop, I2CErr: 3, Board: BoardV2}
	var b [StateLen]byte
	in.Encode(&b)
	out, ok := DecodeState(b[:])
	if !ok || out != in {
		t.Fatalf("got %+v ok=%v, want %+v", out, ok, in)
	}
	if _, ok := DecodeState(b[:StateLen-1]); ok {
		t.Fatal("short buffer accepted")
	}
}
