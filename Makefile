BIN := bin/sc
export CGO_ENABLED := 0

.PHONY: build test race vet fmt smoke m1-compat clean

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

# The M1 binary must keep working on a store the M2 code has migrated.
m1-compat:
	./scripts/build-sc-m1.sh bin/sc-m1
	SC_M1_BIN=$(CURDIR)/bin/sc-m1 go test -count=1 -run TestM1Compat ./internal/store

clean:
	rm -rf bin
