.PHONY: build clean run

BINARY=serverstat

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(BINARY) .

run:
	go run .

clean:
	rm -f $(BINARY)

install: build
	sudo mkdir -p /opt/serverstat
	sudo cp $(BINARY) /opt/serverstat/
	sudo cp config.yaml /opt/serverstat/
	sudo cp serverstat.service /etc/systemd/system/
	sudo systemctl daemon-reload
	@echo "Installed. Edit /opt/serverstat/config.yaml then run:"
	@echo "  sudo systemctl enable --now serverstat"
