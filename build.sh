#!/bin/sh
set -e
mkdir -p dist
for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
  os=${t%/*}; arch=${t#*/}; ext=""; [ "$os" = windows ] && ext=".exe"
  GOOS=$os GOARCH=$arch CGO_ENABLED=0 go build -ldflags "-s -w" -o "dist/prism-$os-$arch$ext" .
done
ls dist
