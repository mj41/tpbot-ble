// Command tpbot-bridge connects a TPBot micro:bit (BLE) to an Embody Mode
// server such as sbot (WebSocket, stackchan-server wire protocol). The car
// registers as a "robot" worker with the car_* commands and telemetry.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mj41/stackchan-server/wire"
	"github.com/mj41/tpbot-ble/client"
	"github.com/mj41/tpbot-ble/proto"
	"tinygo.org/x/bluetooth"
)

// The car capability: the same names whether the car hangs off this bridge
// or, later, off a Stack-chan. Documented in readme.md ("Car capability").
var (
	carCommands     = []string{"car_drive", "car_stop", "car_servo", "car_headlights", "car_sonar", "car_watchdog"}
	carMeasurements = []string{"car_echo_us", "car_line_l", "car_line_r", "car_btn_a", "car_btn_b",
		"car_left", "car_right", "car_watchdog_stop", "car_uptime_ms", "car_i2c_errors", "car_board"}
)

const (
	bleSilence      = 1500 * time.Millisecond
	telemetryMinGap = 50 * time.Millisecond
	heartbeatPeriod = 30 * time.Second
	readWait        = 60 * time.Second // the server pings every 5 s
)

var log = slog.New(slog.NewTextHandler(os.Stderr, nil))

func main() {
	home, _ := os.UserHomeDir()
	server := flag.String("server", "ws://127.0.0.1:8780", "server base URL (ws:// or wss://)")
	tokenFile := flag.String("token-file", filepath.Join(home, ".config/stackchan-server/robot-token"), "robot token file")
	id := flag.String("id", "", "worker id (default: tpbot-<suffix of the BLE name>, e.g. tpbot-1a2b)")
	with := flag.String("with", "", "id of the Stack-chan this car belongs to (Register label \"with\")")
	name := flag.String("name", "", "BLE name to connect to (default: first TPB-*)")
	addr := flag.String("addr", "", "BLE address to connect to")
	board := flag.String("board", "v1", "TPBot frame format: v1, v2 or both (see tpbot-ble readme)")
	flag.Parse()

	boardMode, ok := map[string]uint8{"both": proto.BoardBoth, "v1": proto.BoardV1, "v2": proto.BoardV2}[*board]
	if !ok {
		log.Error("-board must be v1, v2 or both")
		os.Exit(2)
	}
	tok, err := os.ReadFile(*tokenFile)
	if err != nil {
		log.Error("read token", "err", err)
		os.Exit(1)
	}
	token := strings.TrimSpace(string(tok))
	adapter := bluetooth.DefaultAdapter
	if err := adapter.Enable(); err != nil {
		log.Error("enable bluetooth", "err", err)
		os.Exit(1)
	}

	// On Ctrl-C or SIGTERM: stop the car and release the BLE link. BlueZ keeps a link
	// open after its client exits, and the micro:bit then advertises to nobody else.
	var current atomic.Pointer[client.Bot]
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		s := <-sig
		if bot := current.Load(); bot != nil {
			bot.Send(proto.Stop())
			bot.Close()
			log.Info("BLE released", "signal", s.String())
			time.Sleep(300 * time.Millisecond) // let BlueZ send the disconnect
		}
		os.Exit(0)
	}()

	for {
		bot, err := client.Connect(adapter, client.Match{Name: *name, Addr: *addr}, 10*time.Second)
		if err != nil {
			log.Warn("BLE connect", "err", err)
			time.Sleep(2 * time.Second)
			continue
		}
		workerID := *id
		if workerID == "" {
			workerID = "tpbot-" + strings.ToLower(strings.TrimPrefix(bot.Name, proto.NamePrefix))
		}
		log.Info("BLE connected", "car", bot.Name, "addr", bot.Addr, "worker", workerID, "board", *board)
		if err := bot.Send(proto.Board(boardMode)); err != nil {
			log.Warn("set board", "err", err)
		}
		b := &bridge{bot: bot, id: workerID, with: *with, url: strings.TrimSuffix(*server, "/") + wire.ConnectPath, token: token}
		current.Store(bot)
		err = b.run()
		current.Store(nil)
		bot.Send(proto.Stop())
		bot.Close()
		log.Warn("BLE session ended", "err", err)
		time.Sleep(time.Second)
	}
}

type bridge struct {
	bot       *client.Bot
	id, with  string
	url       string
	token     string
	firmware  string
	states    chan proto.State
	lastState proto.State
	pairURL   string
}

// run serves one BLE connection: it (re)dials the server until the BLE link is lost.
func (b *bridge) run() error {
	b.firmware, _ = b.bot.Info()
	b.states = make(chan proto.State, 1)
	err := b.bot.Notify(func(s proto.State) {
		select { // keep only the newest state
		case <-b.states:
		default:
		}
		b.states <- s
	})
	if err != nil {
		return err
	}
	backoff := time.Second
	for {
		start := time.Now()
		err := b.session()
		b.bot.Send(proto.Stop()) // nobody drives a car without a server
		if errors.Is(err, client.ErrLost) {
			return err
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		log.Warn("server session ended", "err", err, "retry_in", backoff)
		if err := b.waitAlive(backoff); err != nil {
			return err
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (b *bridge) waitAlive(d time.Duration) error {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if b.bot.Silent() > bleSilence {
			return client.ErrLost
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

func (b *bridge) session() error {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+b.token)
	h.Set(wire.WorkerIDHeader, b.id)
	ws, resp, err := websocket.DefaultDialer.Dial(b.url, h)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial %s: %w (HTTP %s)", b.url, err, resp.Status)
		}
		return fmt.Errorf("dial %s: %w", b.url, err)
	}
	defer ws.Close()
	send := func(kind string, body any) error {
		f, err := wire.Marshal(kind, wire.Meta{}, body)
		if err != nil {
			return err
		}
		ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return ws.WriteMessage(websocket.TextMessage, f)
	}
	reg := wire.RegisterBody{
		Class: wire.ClassRobot,
		Capabilities: wire.RobotCapabilities{
			Model:        "tpbot-microbit",
			Firmware:     b.firmware,
			Commands:     carCommands,
			Measurements: carMeasurements,
		},
	}
	if b.with != "" {
		reg.Labels = map[string]string{"with": b.with}
	}
	if err := send(wire.KindRegister, reg); err != nil {
		return err
	}

	frames := make(chan wire.Frame, 16)
	errc := make(chan error, 1)
	go func() {
		ws.SetReadDeadline(time.Now().Add(readWait))
		ws.SetPingHandler(func(data string) error {
			ws.SetReadDeadline(time.Now().Add(readWait))
			return ws.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(10*time.Second))
		})
		for {
			kind, data, err := ws.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			ws.SetReadDeadline(time.Now().Add(readWait))
			if kind != websocket.TextMessage {
				continue
			}
			fs, err := wire.Parse(data)
			if err != nil {
				continue
			}
			for _, f := range fs {
				frames <- f
			}
		}
	}()

	heartbeat := time.NewTicker(heartbeatPeriod)
	defer heartbeat.Stop()
	alive := time.NewTicker(250 * time.Millisecond)
	defer alive.Stop()
	var lastSent time.Time
	var pending *proto.State
	flush := time.NewTimer(time.Hour)
	defer flush.Stop()

	sendState := func(s proto.State) error {
		lastSent = time.Now()
		return send(wire.KindRobotTelemetry, wire.RobotTelemetryBody{Measurements: measurements(s)})
	}

	for {
		var err error
		select {
		case err = <-errc:
			return err
		case f := <-frames:
			err = b.handleFrame(f, send)
		case s := <-b.states:
			b.lastState = s
			if gap := time.Since(lastSent); gap < telemetryMinGap {
				pending = &s
				flush.Reset(telemetryMinGap - gap)
				continue
			}
			pending = nil
			err = sendState(s)
		case <-flush.C:
			if pending != nil {
				err = sendState(*pending)
				pending = nil
			}
		case <-heartbeat.C:
			err = send(wire.KindHeartbeat, nil)
		case <-alive.C:
			if b.bot.Silent() > bleSilence {
				return client.ErrLost
			}
		}
		if err != nil {
			return err
		}
	}
}

func (b *bridge) handleFrame(f wire.Frame, send func(string, any) error) error {
	switch f.Kind {
	case wire.KindAccepted:
		log.Info("server accepted", "server", b.url, "worker", b.id, "with", b.with)
	case wire.KindRejected:
		var r wire.RejectedBody
		f.Decode(&r)
		return fmt.Errorf("server rejected: %s", r.Reason)
	case wire.KindPairCode:
		var p wire.PairCodeBody
		if f.Decode(&p) == nil && b.pairURL == "" {
			b.pairURL = p.URL
			log.Info("pair a browser with this car directly", "url", p.URL)
		}
	case wire.KindRobotCommand:
		var c wire.RobotCommandBody
		if err := f.Decode(&c); err != nil {
			return nil
		}
		if p, err := carCommand(c); err != nil {
			log.Warn("bad command", "command", c.Command, "err", err)
		} else if err := b.bot.Send(p); err != nil {
			log.Warn("BLE write", "command", c.Command, "err", err)
		}
	}
	return nil
}

// carCommand turns a car_* command into the BLE command bytes.
func carCommand(c wire.RobotCommandBody) ([]byte, error) {
	num := func(k string, lo, hi int) (int, error) {
		v, ok := c.Args[k].(float64)
		if !ok || v < float64(lo) || v > float64(hi) {
			return 0, fmt.Errorf("args.%s: want a number %d..%d", k, lo, hi)
		}
		return int(v), nil
	}
	switch c.Command {
	case "car_drive":
		l, err := num("left", -100, 100)
		if err != nil {
			return nil, err
		}
		r, err := num("right", -100, 100)
		if err != nil {
			return nil, err
		}
		return proto.Drive(int8(l), int8(r)), nil
	case "car_stop":
		return proto.Stop(), nil
	case "car_servo":
		p, err := num("port", 1, 4)
		if err != nil {
			return nil, err
		}
		a, err := num("angle", 0, 180)
		if err != nil {
			return nil, err
		}
		return proto.Servo(uint8(p), uint8(a)), nil
	case "car_headlights":
		col, _ := c.Args["color"].(string)
		v, err := strconv.ParseUint(strings.TrimPrefix(col, "#"), 16, 32)
		if len(col) != 7 || col[0] != '#' || err != nil {
			return nil, errors.New(`args.color: want "#rrggbb"`)
		}
		return proto.Headlights(uint8(v>>16), uint8(v>>8), uint8(v)), nil
	case "car_sonar":
		hz, err := num("hz", 0, proto.SonarMaxHz)
		if err != nil {
			return nil, err
		}
		return proto.Sonar(uint8(hz)), nil
	case "car_watchdog":
		ms, err := num("ms", proto.WatchdogMinMs, proto.WatchdogMaxMs)
		if err != nil {
			return nil, err
		}
		return proto.Watchdog(uint16(ms)), nil
	}
	return nil, fmt.Errorf("unknown command %q", c.Command)
}

func measurements(s proto.State) map[string]float64 {
	bit := func(v, m uint8) float64 {
		if v&m != 0 {
			return 1
		}
		return 0
	}
	return map[string]float64{
		"car_echo_us":       float64(s.EchoUs),
		"car_line_l":        bit(s.Inputs, proto.InLineLeft),
		"car_line_r":        bit(s.Inputs, proto.InLineRight),
		"car_btn_a":         bit(s.Inputs, proto.InButtonA),
		"car_btn_b":         bit(s.Inputs, proto.InButtonB),
		"car_left":          float64(s.Left),
		"car_right":         float64(s.Right),
		"car_watchdog_stop": bit(s.Flags, proto.FlagWatchdogStop),
		"car_uptime_ms":     float64(s.Ms),
		"car_i2c_errors":    float64(s.I2CErr),
		"car_board":         float64(s.Board),
	}
}
