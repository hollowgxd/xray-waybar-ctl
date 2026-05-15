BIN      := xray-waybar-ctl
PREFIX   ?= $(HOME)/.local
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -X main.version=$(VERSION)

.PHONY: build install uninstall test vet tidy clean run-status

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/xray-waybar-ctl

install: build
	install -Dm755 $(BIN) $(PREFIX)/bin/$(BIN)
	install -Dm644 configs/app.yaml.example $(PREFIX)/share/xray-waybar/app.yaml.example

uninstall:
	rm -f $(PREFIX)/bin/$(BIN)
	rm -f $(PREFIX)/share/xray-waybar/app.yaml.example

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -f $(BIN)

run-status: build
	./$(BIN) status
