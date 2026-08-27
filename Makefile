VERSION ?= 0.1.0

.PHONY: build css run mock test img clean release

# bump ply.toml, commit, tag, push — the tag ALWAYS matches the version, so
# the workflow's guard can't bite. `make release V=0.2.0` overrides the bump.
release:
	@set -eu; \
	test "$$(git rev-parse --abbrev-ref HEAD)" = main || { echo "release: not on main"; exit 1; }; \
	test -z "$$(git status --porcelain)" || { echo "release: working tree not clean — commit the feature work first"; exit 1; }; \
	git pull --ff-only; \
	CUR=$$(sed -n 's/^version = "\(.*\)"/\1/p' ply.toml | head -1); \
	V="$(V)"; \
	[ -n "$$V" ] || V=$$(echo "$$CUR" | awk -F. '{print $$1"."$$2"."$$3+1}'); \
	echo "$$V" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$$' || { echo "release: bad version \`$$V\`"; exit 1; }; \
	echo "release: $$CUR -> $$V"; \
	go vet ./... && go test ./...; \
	sed -i "s/^version = \".*\"/version = \"$$V\"/" ply.toml; \
	git add ply.toml; \
	git commit -m "v$$V"; \
	git push; \
	git tag "v$$V"; \
	git push origin "v$$V"; \
	echo "release: v$$V tagged — release.yml builds both images and attaches them"

css:
	./bin/tailwindcss -i web/input.css -o web/assets/app.css --minify

build: css
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o ply-dashboard .

test:
	go vet ./... && go test ./...

# dev loop against the host's own ply state
run: build
	PORT=7070 ./ply-dashboard

# design loop against fabricated state — no ply needed; login mock/mockmock
mock: build
	MOCK=true PORT=7077 ./ply-dashboard

img: build
	ply build .

clean:
	rm -f ply-dashboard dashboard-*.img
