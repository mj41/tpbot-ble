# tpbot-ble

BLE control for an ELECFREAKS TPBot car with a micro:bit V2. The micro:bit firmware is a BLE peripheral: it drives the car and reports the car's raw sensors. Its clients:

- `tpbot`, a Linux laptop tool (here);
- `tpbot-bridge` (here), which connects the car over the laptop's BLE to [sbot](https://github.com/mj41/sbot), an app server that manages a Stack-chan and the car together;
- **Stack-chan itself**, as an optional extension of its Embody Mode: the [StackChan firmware fork](https://github.com/mj41/StackChan/tree/embody-mj41) has a BLE central for the car, so everything runs on the robot's one Wi-Fi connection. The car stays optional: most robots have none.

Part of [home-w42-eu](https://github.com/mj41/home-w42-eu), a local first, privacy first platform for a home, where the micro:bit is one light client.

## Build and flash

Needs TinyGo ≥ 0.41 (Go 1.26 support) and Go.

```bash
make firmware   # build/tpbot-ble.hex = S113 SoftDevice + app
make flash      # copies it to the MICROBIT drive; the micro:bit restarts
make cli        # build/tpbot, build/tpbot-bridge
make test
```

The merged hex carries Nordic's S113 SoftDevice (from `tinygo.org/x/bluetooth`), so one copy to the drive flashes both. No OpenOCD is needed.

On the micro:bit, the center LED blinks while it advertises and stays on while a client is connected. Debug output (`println`) goes to the USB serial port at 115200 baud.

## Who may connect: the allowlist

The micro:bit accepts commands only from centrals on its **allowlist**, set at build
time: `ALLOW="AA:BB:…,CC:DD:…" make flash`, or one address per line in
`~/.config/tpbot-ble/allow` (not in git). An empty list means any central.

- A central that is not on the list gets **no commands taken and no state sent**, and
  is disconnected (at most every 10 s). The center LED blinks fast meanwhile.
- The serial port logs every central: `central 0A:1B:2C:3D:4E:52 allowed` / `refused`.
  That is how to find a new central's address: it shows up as refused.
- Our centrals: the laptop's adapter (`0A:1B:2C:3D:4E:60`) and Stack-chan
  (`0A:1B:2C:3D:4E:52`, its Wi-Fi MAC + 2).
- **Limit:** BLE addresses can be spoofed. LE Secure Connections bonding (which
  TinyGo's bluetooth package supports) is the stronger step, together with NimBLE
  bonding on Stack-chan. The disconnect uses `RemoveBond()`, the only peripheral-side
  disconnect in TinyGo's S113 port; no bonds are stored.

## Laptop tool

```bash
./build/tpbot scan
./build/tpbot info
./build/tpbot watch               # state notifications
./build/tpbot keys                # arrows/WASD, space, +/-, l, q
./build/tpbot drive 50 50 -for 1s
./build/tpbot lights 255 0 0
./build/tpbot servo 1 90
./build/tpbot sonar 20
./build/tpbot -v raw 01 28 d8     # one drive 40/-40 without repeat: the watchdog stops it
```

`-name TPB-xxxx` or `-addr MAC` picks one car; the default is the first `TPB-*` found. `-v` prints state notifications during any command. The micro:bit accepts one connection at a time.

## Bridge to sbot

`tpbot-bridge` connects the car (BLE) to [sbot](https://github.com/mj41/sbot) as a `robot` worker of the [device wire protocol](https://github.com/mj41/home-w42-eu/blob/main/docs/wire-protocol.md) (through the `wire` package of [stackchan-server](https://github.com/mj41/stackchan-server)), with the `car_*` commands and telemetry listed in sbot's readme, [Car capability](https://github.com/mj41/sbot#car-capability).

```bash
make cli
./build/tpbot-bridge -with stackchan-0a1b2c3d4e50    # -server ws://127.0.0.1:8780 by default
```

Or without cloning: `go install github.com/mj41/tpbot-ble/cmd/tpbot-bridge@latest`.

- The worker id is `tpbot-<suffix of the BLE name>`, e.g. `tpbot-1a2b`. `-with` links the car to a Stack-chan, so browsers paired with it can drive the car. The bridge also logs a pairing URL for the car alone.
- It forwards every state notification as telemetry (at most every 50 ms), and turns `car_*` commands into BLE commands.
- It stops the car when the server connection drops, and closes the server session when the BLE link goes quiet for 1.5 s. It reconnects both.
- It holds the micro:bit's only BLE connection: stop it before using `tpbot`.

## Protocol

One GATT service. The advertisement holds its UUID and the name `TPB-xxxx` (the last 4 hex digits of the chip's device ID).

| Characteristic | UUID | Access |
|---|---|---|
| service | `3d440001-87fb-42e7-9a68-7596af5168a0` | |
| command | `3d440002-…` | write, write without response |
| state | `3d440003-…` | read, notify |
| info | `3d440004-…` | read: firmware version text |

Commands (first byte is the opcode):

| Op | Command | Args |
|---|---|---|
| `0x01` | drive | int8 left, int8 right: -100..100 |
| `0x02` | stop | none |
| `0x03` | servo | uint8 port 1..4, uint8 angle 0..180 |
| `0x04` | headlights | uint8 r, g, b |
| `0x05` | watchdog | uint16 LE ms, 100..10000 (default 500) |
| `0x06` | sonar | uint8 Hz, 0 (off)..20 (default 10) |
| `0x07` | board | uint8 frame format: 0 both, 1 V1 (default), 2 V2 |
| `0x7F` | i2c | debug: raw bytes written to the TPBot controller (I2C `0x10`) |

**Watchdog:** the motors stop when no `drive` arrives within the watchdog time, and on every connect or disconnect. A client that wants to keep moving sends `drive` again, e.g. every 100 ms.

State (14 bytes, little endian, format 2), notified on every change, after every sonar measurement, and at least every 200 ms:

| Byte | Field |
|---|---|
| 0 | format = 2 |
| 1 | seq (uint8, wraps) |
| 2–5 | micro:bit uptime, ms |
| 6–7 | sonar echo pulse, µs; 0 = no echo or sonar off (cm ≈ µs / 58) |
| 8 | inputs: bit 0 line left (raw P13), bit 1 line right (raw P14), bit 2 button A pressed, bit 3 button B pressed |
| 9, 10 | left, right motor speed in effect (int8) |
| 11 | flags: bit 0 the watchdog stopped the motors (cleared by the next drive) |
| 12 | failed I2C writes to the TPBot controller since boot (uint8, wraps) |
| 13 | board mode (as `0x07`) |

The sonar measures only while a client is connected.

## Hardware notes

- **TPBot:** the board controller is at I2C `0x10` on the edge connector (P19/P20). The frames come from ELECFREAKS' MakeCode extension ([pxt-tpbot](https://github.com/elecfreaks/pxt-tpbot), `V1.ts`, `V2.ts`). The firmware sends V1 frames by default (see the next point). Line sensors are on P13/P14, sonar trigger on P16 and echo on P15.
- **V1 or V2 board:** ELECFREAKS' extension sends both frame formats. Our TPBot is a **V1**: a V2-only headlight command did nothing (seen through Stack-chan's camera, 2026-10-01). With both formats, its wheels kept turning after a stop, so the firmware sends V1 only by default. `tpbot board v2` or `tpbot-bridge -board v2` switches. The firmware also resends the motor values (stop included) every 200 ms, and leaves 2 ms between I2C frames.
- **Flashing:** copy with `dd ... oflag=direct` (as `make flash` does). With `cp` + `sync`, DAPLink often wrote `FAIL.TXT` "The transfer timed out", and then the micro:bit had no program.
- **Sonar timing** is done in hardware (TIMER1, GPIOTE channel 7, PPI channels 0–1, group 0), so BLE interrupts cannot change it. S113 keeps TIMER0, PPI channels 17–31 and groups 4–5.
- **BLE callbacks** run in the SoftDevice interrupt. They only queue commands; the main loop does the I2C.

## Status

Works on a micro:bit V2.2 in a TPBot V1 (2026-10-01/02), driven by `tpbot`, by `tpbot-bridge` and by Stack-chan: BLE, state notifications, the watchdog stop, headlights, the sonar (a steady ≈18 cm to Stack-chan), and the line sensors. Motors: drive and stop work with V1 frames. After a stop, a lifted wheel coasts for a moment, with no motor sound and no force.

- **Start the TPBot with one press of its power button** (LEDs breathe green: standby, driven by the micro:bit). A second press starts its own line-tracking mode (rainbow LEDs), which drives the wheels by itself. A double press switches it off. ([ELECFREAKS guide](https://shop.elecfreaks.com/blogs/tutorials/tpbot-creative-programming-guide))

- **Security:** an address allowlist (since 0.3.0, tested 2026-10-02: a refused laptop got no state and its drive was ignored). Bonding is still to do.
- **Not covered yet:** the micro:bit's own sensors (accelerometer, magnetometer, microphone, temperature, logo touch), the TPBot V2 encoder commands, and the TPBot color sensor.

## Related projects

- [sbot](https://github.com/mj41/sbot): the app server with the cockpit (camera, joystick, safety stop) that drives the car; the `car_*` capability.
- [StackChan fork, branch `embody-mj41`](https://github.com/mj41/StackChan/tree/embody-mj41): Stack-chan as the car's BLE central; enabling the car: step 8 of [SETUP.md](https://github.com/mj41/StackChan/blob/embody-mj41/firmware/main/apps/app_embody_mode/SETUP.md).
- [stackchan-server](https://github.com/mj41/stackchan-server): the `wire` package the bridge uses.
- [home-w42-eu](https://github.com/mj41/home-w42-eu): the platform this is part of. All the repos: [The repos today](https://github.com/mj41/home-w42-eu#the-repos-today).

## License

Apache License 2.0, see [LICENSE](LICENSE). The merged firmware hex includes Nordic Semiconductor's S113 SoftDevice (from `tinygo.org/x/bluetooth`) under Nordic's own license, which allows it to be used with Nordic chips such as the micro:bit's nRF52833.
