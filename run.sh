#!/usr/bin/env bash
set -euo pipefail

# Keep the live bot's stdout/stderr bounded. rotatelogs rotates at 10 MiB and
# retains seven files (about 70 MiB total), while the hard link preserves the
# stable path used by local monitoring commands.
log_dir="${BBGO_LOG_DIR:-/tmp}"
mkdir -p "${log_dir}"

# The live Binance session must use the current direct egress IP. Do not let a
# stale local SOCKS/WARP proxy setting route API requests to an unavailable
# 127.0.0.1:1080 tunnel.
unset HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy

exec go run ./cmd/bbgo --dotenv .env run --config config/gammacapture.yaml 2>&1 \
  | /usr/sbin/rotatelogs -l -f -n 7 -L "${log_dir}/gammacapture-live.log" \
      "${log_dir}/gammacapture-live" 10M
