#!/bin/sh
# Runs every regression check. Live tests run only with PING_LIVE=1.
set -eu
cd "$(dirname "$0")/.."

unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
	echo "gofmt needed:" >&2
	echo "$unformatted" >&2
	exit 1
fi

go vet ./...
go test ./...

# Files stay small enough to read in one sitting.
limit=300
oversized=$(find . -name '*.go' -not -path './vendor/*' -exec wc -l {} + |
	awk -v max="$limit" '$2 != "total" && $1 > max { print $2 " (" $1 " lines)" }')
if [ -n "$oversized" ]; then
	echo "files over $limit lines:" >&2
	echo "$oversized" >&2
	exit 1
fi
echo "check passed"
