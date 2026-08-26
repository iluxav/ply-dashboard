VERSION ?= 0.1.0

.PHONY: build css run test img clean

css:
	./bin/tailwindcss -i web/input.css -o web/assets/app.css --minify

build: css
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o ply-dashboard .

test:
	go vet ./... && go test ./...

# dev loop against the host's own ply state
run: build
	PORT=7070 ./ply-dashboard

img: build
	ply build .

clean:
	rm -f ply-dashboard dashboard-*.img
