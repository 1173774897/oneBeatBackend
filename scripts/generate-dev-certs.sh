#!/bin/sh

set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
SERVER_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
CERT_DIR="$SERVER_DIR/certs"

mkdir -p "$CERT_DIR"

openssl req \
  -x509 \
  -newkey rsa:2048 \
  -sha256 \
  -days 365 \
  -nodes \
  -keyout "$CERT_DIR/dev-key.pem" \
  -out "$CERT_DIR/dev-cert.pem" \
  -subj "/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1"

chmod 600 "$CERT_DIR/dev-key.pem"
printf 'Development certificate created in %s\n' "$CERT_DIR"
