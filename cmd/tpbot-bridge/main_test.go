package main

import (
	"bytes"
	"testing"

	"github.com/mj41/stackchan-server/wire"
	"github.com/mj41/tpbot-ble/proto"
)

func TestCarCommand(t *testing.T) {
	ok := []struct {
		cmd  string
		args map[string]any
		want []byte
	}{
		{"car_drive", map[string]any{"left": 50.0, "right": -100.0}, proto.Drive(50, -100)},
		{"car_stop", nil, proto.Stop()},
		{"car_servo", map[string]any{"port": 2.0, "angle": 90.0}, proto.Servo(2, 90)},
		{"car_headlights", map[string]any{"color": "#ff8000"}, proto.Headlights(255, 128, 0)},
		{"car_sonar", map[string]any{"hz": 0.0}, proto.Sonar(0)},
		{"car_watchdog", map[string]any{"ms": 300.0}, proto.Watchdog(300)},
	}
	for _, c := range ok {
		got, err := carCommand(wire.RobotCommandBody{Command: c.cmd, Args: c.args})
		if err != nil || !bytes.Equal(got, c.want) {
			t.Errorf("%s %v: got % x, %v; want % x", c.cmd, c.args, got, err, c.want)
		}
	}
	bad := []wire.RobotCommandBody{
		{Command: "car_drive", Args: map[string]any{"left": 101.0, "right": 0.0}},
		{Command: "car_drive", Args: map[string]any{"left": 1.0}},
		{Command: "car_servo", Args: map[string]any{"port": 5.0, "angle": 0.0}},
		{Command: "car_headlights", Args: map[string]any{"color": "red"}},
		{Command: "car_watchdog", Args: map[string]any{"ms": 0.0}},
		{Command: "nod"},
	}
	for _, c := range bad {
		if _, err := carCommand(c); err == nil {
			t.Errorf("%s %v: accepted", c.Command, c.Args)
		}
	}
}
