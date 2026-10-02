TARGET   := microbit-v2-s113v7
SD_HEX    = $(shell go list -m -f '{{.Dir}}' tinygo.org/x/bluetooth)/s113_nrf52_7.0.1/s113_nrf52_7.0.1_softdevice.hex
# Centrals that may connect (comma-separated BLE addresses); empty = any.
ALLOW    ?= $(shell tr -s ' \n' ',' < $(HOME)/.config/tpbot-ble/allow 2>/dev/null | sed 's/,$$//')
MICROBIT ?= $(firstword $(wildcard /run/media/$(USER)/MICROBIT /media/$(USER)/MICROBIT))

.PHONY: all firmware cli flash test clean

all: firmware cli

# build/tpbot-ble.hex = S113 SoftDevice + app, so one copy to the MICROBIT drive flashes both.
firmware:
	@mkdir -p build
	tinygo build -target $(TARGET) -size short -ldflags="-X main.allowList=$(ALLOW)" -o build/app.hex ./firmware
	tr -d '\r' < $(SD_HEX) | grep -v '^:00000001FF' > build/tpbot-ble.hex
	cat build/app.hex >> build/tpbot-ble.hex

cli:
	@mkdir -p build
	go build -o build/tpbot-bridge ./cmd/tpbot-bridge
	@mkdir -p build
	go build -o build/tpbot ./cmd/tpbot

flash: firmware
	@test -n "$(MICROBIT)" || { echo "MICROBIT drive not mounted"; exit 1; }
	@# Direct, sequential writes: with cp + sync DAPLink often reports "The transfer timed out".
	dd if=build/tpbot-ble.hex of=$(MICROBIT)/tpbot-ble.hex bs=64k oflag=direct conv=fsync status=none
	@for i in $$(seq 30); do sleep 1; ls $(MICROBIT) >/dev/null 2>&1 && [ $$i -gt 5 ] && break; done; \
	if [ -f $(MICROBIT)/FAIL.TXT ]; then cat $(MICROBIT)/FAIL.TXT; echo "flash failed: run make flash again"; exit 1; fi
	@echo "flashed; the micro:bit restarted"

test:
	go vet ./proto ./client ./cmd/...
	go test -race ./proto ./client ./cmd/...

clean:
	rm -rf build
