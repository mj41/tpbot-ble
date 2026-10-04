# tpbot-ble

micro:bit V2 BLE firmware (TinyGo) for an ELECFREAKS TPBot car, plus a Go laptop tool. Read `readme.md` for the protocol and the plan.

- Use TinyGo from `~/.local/bin/tinygo` (upstream 0.42 in `~/.local/opt/tinygo`). Fedora's `/usr/bin/tinygo` 0.39 does not support Go 1.26.
- `make firmware flash` builds `build/tpbot-ble.hex` (S113 SoftDevice + app) and copies it to the MICROBIT drive.
- Firmware files have `//go:build tinygo`. gopls without TinyGo reports missing `machine` imports. That is expected.
- BLE write and connect callbacks run in the SoftDevice interrupt: no heap allocation, no I2C, no println there.
- Keep `proto` free of host-only packages: the firmware and the clients both use it.
- Related: Stackchan Embody Mode (`../s-w42-eu-raw`; the author's private notes `../s-w42-eu-mj-priv/AGENTS.md`). The car support there must stay optional.
