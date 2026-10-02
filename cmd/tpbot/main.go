// Command tpbot drives a TPBot micro:bit over BLE from a Linux laptop (BlueZ).
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mj41/tpbot-ble/client"
	"github.com/mj41/tpbot-ble/proto"
	"golang.org/x/term"
	"tinygo.org/x/bluetooth"
)

const usage = `usage: tpbot [-name TPB-xxxx] [-addr MAC] [-v] <command> [args]

commands:
  scan                    list TPBots nearby (5 s)
  info                    firmware version
  watch                   print every state notification
  keys                    drive with arrows/WASD, space stops, l lights, q quits
  drive L R [-for 1s]     wheels -100..100, then stop
  stop
  lights R G B            headlights 0..255
  servo PORT ANGLE        port 1..4, angle 0..180
  sonar HZ                0 (off)..20
  watchdog MS             100..10000 (until the micro:bit restarts)
  board both|v1|v2        which TPBot frame format the micro:bit sends
  i2c HEX...              debug: raw bytes to the TPBot controller (I2C 0x10)
  raw HEX...              send command bytes as is, e.g. raw 01 28 28
`

var (
	adapter  = bluetooth.DefaultAdapter
	nameFlag = flag.String("name", "", "connect to this advertised name (default: first TPB-*)")
	addrFlag = flag.String("addr", "", "connect to this address")
	rawWait  = flag.Duration("wait", time.Second, "raw: stay connected this long")
	verbose  = flag.Bool("v", false, "print state notifications to stderr")
)

func main() {
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := adapter.Enable(); err != nil {
		fail("enable bluetooth: %v", err)
	}
	if err := run(args[0], args[1:]); err != nil {
		fail("%v", err)
	}
}

func fail(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "tpbot: "+f+"\n", a...)
	os.Exit(1)
}

func run(cmd string, args []string) error {
	if cmd == "scan" {
		return scan()
	}
	b, err := client.Connect(adapter, client.Match{Name: *nameFlag, Addr: *addrFlag}, 10*time.Second)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "connected to %s %s\n", b.Name, b.Addr)
	defer b.Close()
	if *verbose && cmd != "watch" && cmd != "keys" {
		b.Notify(func(s proto.State) { fmt.Fprintln(os.Stderr, formatState(s)) })
	}

	switch cmd {
	case "info":
		v, err := b.Info()
		if err != nil {
			return err
		}
		fmt.Println(v)
		return nil
	case "watch":
		b.Notify(func(s proto.State) { fmt.Println(formatState(s)) })
		select {}
	case "keys":
		return keys(b)
	case "drive":
		fs := flag.NewFlagSet("drive", flag.ExitOnError)
		dur := fs.Duration("for", time.Second, "how long to drive")
		l, r, err := twoInts(args, -100, 100)
		if err != nil {
			return err
		}
		fs.Parse(args[2:])
		end := time.Now().Add(*dur)
		for time.Now().Before(end) {
			if err := b.Send(proto.Drive(int8(l), int8(r))); err != nil {
				return err
			}
			time.Sleep(100 * time.Millisecond)
		}
		return b.Send(proto.Stop())
	case "stop":
		return b.Send(proto.Stop())
	case "lights":
		if len(args) != 3 {
			return errors.New("lights R G B")
		}
		var c [3]uint8
		for i := range c {
			v, err := strconv.ParseUint(args[i], 10, 8)
			if err != nil {
				return err
			}
			c[i] = uint8(v)
		}
		return b.Send(proto.Headlights(c[0], c[1], c[2]))
	case "servo":
		p, a, err := twoInts(args, 0, 180)
		if err != nil {
			return err
		}
		return b.Send(proto.Servo(uint8(p), uint8(a)))
	case "sonar":
		v, err := oneInt(args, 0, proto.SonarMaxHz)
		if err != nil {
			return err
		}
		return b.Send(proto.Sonar(uint8(v)))
	case "board":
		m := map[string]uint8{"both": proto.BoardBoth, "v1": proto.BoardV1, "v2": proto.BoardV2}
		v, ok := m[strings.Join(args, "")]
		if !ok {
			return errors.New("board both|v1|v2")
		}
		return b.Send(proto.Board(v))
	case "i2c":
		args = append([]string{"7f"}, args...)
		fallthrough
	case "raw":
		var p []byte
		for _, a := range args {
			v, err := strconv.ParseUint(a, 16, 8)
			if err != nil {
				return err
			}
			p = append(p, byte(v))
		}
		if err := b.Send(p); err != nil {
			return err
		}
		time.Sleep(*rawWait)
		return nil
	case "watchdog":
		v, err := oneInt(args, proto.WatchdogMinMs, proto.WatchdogMaxMs)
		if err != nil {
			return err
		}
		return b.Send(proto.Watchdog(uint16(v)))
	}
	return fmt.Errorf("unknown command %q", cmd)
}

func oneInt(args []string, lo, hi int) (int, error) {
	if len(args) < 1 {
		return 0, errors.New("missing value")
	}
	v, err := strconv.Atoi(args[0])
	if err != nil || v < lo || v > hi {
		return 0, fmt.Errorf("%q: want %d..%d", args[0], lo, hi)
	}
	return v, nil
}

func twoInts(args []string, lo, hi int) (int, int, error) {
	if len(args) < 2 {
		return 0, 0, errors.New("missing values")
	}
	a, err := oneInt(args[0:1], lo, hi)
	if err != nil {
		return 0, 0, err
	}
	b, err := oneInt(args[1:2], lo, hi)
	return a, b, err
}

func scan() error {
	return client.Scan(adapter, 5*time.Second, func(name, addr string, rssi int16) {
		fmt.Printf("%s  %s  %d dBm\n", name, addr, rssi)
	})
}

func formatState(s proto.State) string {
	bit := func(m uint8) int {
		if s.Inputs&m != 0 {
			return 1
		}
		return 0
	}
	sonar := "-"
	if s.EchoUs != 0 {
		sonar = fmt.Sprintf("%dus (%.1f cm)", s.EchoUs, float64(s.EchoUs)/58)
	}
	wd := ""
	if s.Flags&proto.FlagWatchdogStop != 0 {
		wd = " watchdog-stop"
	}
	return fmt.Sprintf("#%-3d t=%-7d line L=%d R=%d  btn A=%d B=%d  motors %4d %4d  sonar %s  i2c-err %d board %d%s",
		s.Seq, s.Ms, bit(proto.InLineLeft), bit(proto.InLineRight),
		bit(proto.InButtonA), bit(proto.InButtonB), s.Left, s.Right, sonar, s.I2CErr, s.Board, wd)
}

// keys drives while a key repeats: the terminal's auto-repeat keeps the
// target alive, and the car stops 600 ms after the last key.
func keys(b *client.Bot) error {
	old, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(os.Stdin.Fd()), old)

	var mu sync.Mutex
	var l, r int8
	var last time.Time
	speed := int8(60)
	b.Notify(func(s proto.State) { fmt.Printf("\r%s\x1b[K", formatState(s)) })
	fmt.Print("arrows/WASD drive, space stop, +/- speed, l lights, q quit\r\n")

	go func() {
		for range time.Tick(100 * time.Millisecond) {
			mu.Lock()
			if time.Since(last) > 600*time.Millisecond {
				l, r = 0, 0
			}
			cl, cr := l, r
			mu.Unlock()
			b.Send(proto.Drive(cl, cr))
		}
	}()

	lightsOn := false
	buf := make([]byte, 8)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return err
		}
		k := string(buf[:n])
		mu.Lock()
		last = time.Now()
		switch k {
		case "w", "\x1b[A":
			l, r = speed, speed
		case "s", "\x1b[B":
			l, r = -speed, -speed
		case "a", "\x1b[D":
			l, r = -speed/2, speed/2
		case "d", "\x1b[C":
			l, r = speed/2, -speed/2
		case " ":
			l, r = 0, 0
		case "+":
			speed = min(speed+10, 100)
		case "-":
			speed = max(speed-10, 20)
		case "l":
			lightsOn = !lightsOn
			if lightsOn {
				b.Send(proto.Headlights(255, 255, 255))
			} else {
				b.Send(proto.Headlights(0, 0, 0))
			}
		case "q", "\x03":
			mu.Unlock()
			b.Send(proto.Stop())
			fmt.Print("\r\n")
			return nil
		}
		mu.Unlock()
	}
}
