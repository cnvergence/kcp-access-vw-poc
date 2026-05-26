#!/usr/bin/env bash
# Thin wrapper — delegates to hack/kind/Makefile.
# Use `make -C hack/kind setup` directly or `make kind-setup` from repo root.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec make setup
