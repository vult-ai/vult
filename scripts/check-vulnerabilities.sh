#!/usr/bin/env bash
# Scans dependencies for known vulnerabilities. Run it before every release.
#
#   scripts/check-vulnerabilities.sh
#
# Go: govulncheck reports vulnerabilities in the standard library and the
# required modules that this code actually calls. Any finding fails the check;
# the usual fix is a newer Go toolchain or module version.
#
# Web: npm audit fails on any high or critical advisory in web/package-lock.json.
#
# Both read public advisory databases, so the check needs network access.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

# Pinned so two runs of the same commit use the same scanner.
govulncheck="golang.org/x/vuln/cmd/govulncheck@v1.9.0"

status=0
printf '\n== govulncheck\n'
go run "$govulncheck" ./... || status=1

printf '\n== npm audit (web)\n'
(cd web && npm audit --audit-level=high) || status=1

echo
if [ "$status" = 0 ]; then
  echo "No known vulnerabilities found."
else
  echo "Known vulnerabilities found. Fix them before releasing." >&2
fi
exit "$status"
