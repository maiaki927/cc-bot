.PHONY: build run clean

build:
	go build -o bin/cc-bot .

run:
	go run .

clean:
	rm -rf bin/
