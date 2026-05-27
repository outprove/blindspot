#!/bin/sh
set -eu

mkdir -p /app/pb_data

exec /app/main serve --http="0.0.0.0:${PORT:-8080}"
