.PHONY: build run clean

build:
	go build -o bin/cc-bot ./cmd/cc-bot

run:
	go run ./cmd/cc-bot

clean:
	rm -rf bin/
