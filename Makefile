BIN := bin/sc
export CGO_ENABLED := 0

.PHONY: build test race vet fmt smoke clean

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

clean:
	rm -rf bin
