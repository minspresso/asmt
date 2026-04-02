.PHONY: build clean run install uninstall

BINARY=serverstat

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(BINARY) .

build-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o $(BINARY) .

run:
	go run .

clean:
	rm -f $(BINARY)

install: build
	sudo bash install.sh

uninstall:
	sudo bash uninstall.sh
