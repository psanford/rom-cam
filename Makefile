rom-cam-rpi: $(wildcard *.go) go.mod go.sum
	GOOS=linux GOARCH=arm GOARM=7 go build -o $@
