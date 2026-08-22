.PHONY: build test test-race vet check run doctor

build:
	go build -trimpath -o fleetctl ./cmd/fleetctl

test:
	go test -timeout 180s ./...

test-race:
	go test -race -timeout 240s ./...

vet:
	go vet ./...

check: vet test
	@test -z "$$(gofmt -l .)"

run: build
	./fleetctl up

doctor: build
	./fleetctl doctor
