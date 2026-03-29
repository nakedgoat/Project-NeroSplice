APP := matrix-migrator

.PHONY: tidy build test race run-init

tidy:
	go mod tidy

build:
	go build -o bin/$(APP) ./cmd/migrate

test:
	go test ./...

race:
	go test -race ./...

run-init:
	go run ./cmd/migrate init
