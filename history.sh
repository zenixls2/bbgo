#!/usr/bin/env bash
go run ./cmd/gammacapture-data \
  -output data/gammacapture \
  -symbol SOLJPY \
  -from 2026-07-01 \
  -to 2026-07-16
