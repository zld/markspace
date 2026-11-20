# rtsp-ingest developer targets. The build output always goes to /tmp and is
# never committed.

.PHONY: vendor test build demo clean

# Refresh vendored dependencies and immediately restore the local gortsplib
# modification, since a bare 'go mod vendor' would overwrite it.
vendor:
	go mod vendor
	go run -mod=vendor ./tools/apply-vendor-patch

test:
	go test -mod=vendor ./...

build:
	go build -mod=vendor -o /tmp/rtsp-ingest ./cmd/rtsp-ingest

demo: build
	/tmp/rtsp-ingest demo --transport both --seed 17 --json

clean:
	rm -f /tmp/rtsp-ingest
