BIN := bin/sc
export CGO_ENABLED := 0

.PHONY: build test race vet fmt smoke m1-compat accept-m2 clean

build:
	go build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/sc

test:
	go test ./...

race:
	CGO_ENABLED=1 go test -race ./...

vet:
	go vet ./...

fmt:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

smoke: build
	sudo ./scripts/smoke.sh

# M2 acceptance in the VM (plan section 12): touches only its own test paths.
accept-m2: build
	sudo ./scripts/accept-m2.sh

# The M1 binary must keep working on a store the M2 code has migrated.
m1-compat:
	./scripts/build-sc-m1.sh bin/sc-m1
	SC_M1_BIN=$(CURDIR)/bin/sc-m1 go test -count=1 -run TestM1Compat ./internal/store

clean:
	rm -rf bin
