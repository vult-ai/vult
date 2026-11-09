#!/usr/bin/env bash
# Runs the checks a local change needs, and only those.
#
#   scripts/check-changes.sh            checks what changed since HEAD
#   scripts/check-changes.sh --base main    checks what changed since main
#   scripts/check-changes.sh --files web/src/main.tsx ledger/store.go
#                                       checks the named files
#   scripts/check-changes.sh --full     every check, as before a commit
#
# Web changes: typecheck, the design checks for styles and components, and the
# unit tests that load a changed file or the whole app. Go changes: vet and the tests of the
# changed packages, without the race detector. --full runs vet, the whole Go
# suite with the race detector, the web build (with all its checks) and every
# web unit test.
#
# Go tests need the disposable PostgreSQL described in CONTRIBUTING.md:
# VULT_TEST_ISOLATED=1 and VULT_TEST_DSN_FILE. Without them Go tests fail; they
# are never skipped.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
base="HEAD"
full=0
files=()
while [ $# -gt 0 ]; do
  case "$1" in
    --full) full=1 ;;
    --base) base="$2"; shift ;;
    --files) shift; while [ $# -gt 0 ] && [ "${1#--}" = "$1" ]; do files+=("$1"); shift; done; continue ;;
    -h|--help) sed -n '2,19p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

step() {
  local name="$1"; shift
  local start=$SECONDS
  printf '\n== %s\n' "$name"
  "$@"
  printf -- '-- %s: %ss\n' "$name" "$((SECONDS - start))"
}

if [ "$full" = 1 ]; then
  step "go vet" go vet ./...
  step "go tests (race)" go test -race -count=1 ./...
  step "web build and checks" npm --prefix web run -s build
  step "web unit tests" npm --prefix web run -s test:ui
  echo
  echo "All checks passed. The browser journey test needs the preview running; see CONTRIBUTING.md."
  exit 0
fi

if [ "${#files[@]}" -gt 0 ]; then
  changed=$(printf '%s\n' "${files[@]}" | sort -u)
else
  changed=$( { git diff --name-only "$base" -- ; git ls-files --others --exclude-standard; } | sort -u )
fi
if [ -z "$changed" ]; then
  echo "Nothing changed since $base."
  exit 0
fi

web_all=0
web_ts=0
web_style=0
web_tests=()
go_pkgs=()

add_test() {
  local file="$1" t
  for t in "${web_tests[@]+"${web_tests[@]}"}"; do [ "$t" = "$file" ] && return; done
  web_tests+=("$file")
}
# Unit tests that load a file, directly or through a test helper.
tests_for() {
  local stem="$1" match helper
  for match in $(grep -l -- "$stem" web/unit/*.test.mjs 2>/dev/null || true); do add_test "${match#web/}"; done
  for helper in $(grep -l -- "$stem" web/unit/helpers/* 2>/dev/null || true); do
    for match in $(grep -l -- "helpers/$(basename "${helper%.*}")" web/unit/*.test.mjs 2>/dev/null || true); do add_test "${match#web/}"; done
  done
}

while IFS= read -r file; do
  [ -e "$file" ] || continue
  case "$file" in
    web/package.json|web/package-lock.json|web/vite.config.ts|web/tsconfig*.json|web/unit/helpers/dom.mjs|web/unit/helpers/application.mjs)
      web_all=1 ;;
    web/unit/*.test.mjs) add_test "${file#web/}" ;;
    web/unit/helpers/*) tests_for "helpers/$(basename "${file%.*}")" ;;
    web/scripts/*) web_style=1 ;;
    web/src/*.css|web/src/*/*.css|web/src/*/*/*.css|web/src/*/*/*/*.css) web_style=1 ;;
    web/src/*)
      web_ts=1
      web_style=1
      stem="${file#web/}"
      tests_for "${stem%.*}"
      # Tests that load the whole app cover every component without naming it.
      for match in $(grep -l "loadApplication\|src/main.tsx" web/unit/*.test.mjs 2>/dev/null || true); do add_test "${match#web/}"; done
      ;;
    ledger/migrations/*) go_pkgs+=("./ledger/" "./access/" "./server/") ;;
    go.mod|go.sum) go_pkgs+=("./...") ;;
    *.go) go_pkgs+=("./$(dirname "$file")/") ;;
  esac
done <<< "$changed"

ran=0
if [ "$web_ts" = 1 ] || [ "$web_all" = 1 ]; then
  step "web typecheck" npm --prefix web run -s typecheck
  ran=1
fi
if [ "$web_style" = 1 ]; then
  step "web design checks" npm --prefix web run -s design:check
  ran=1
fi
if [ "$web_all" = 1 ]; then
  step "web unit tests (all)" npm --prefix web run -s test:ui
  ran=1
elif [ "${#web_tests[@]}" -gt 0 ]; then
  step "web unit tests (${#web_tests[@]} files)" bash -c 'cd web && node --test "$@"' _ "${web_tests[@]}"
  ran=1
fi

if [ "${#go_pkgs[@]}" -gt 0 ]; then
  pkgs=$(printf '%s\n' "${go_pkgs[@]}" | sort -u)
  # A deleted package directory has nothing left to check.
  existing=()
  while IFS= read -r p; do
    if [ "$p" = "./..." ] || [ -d "$p" ]; then existing+=("$p"); fi
  done <<< "$pkgs"
  if [ "${#existing[@]}" -gt 0 ]; then
    step "go vet" go vet "${existing[@]}"
    step "go tests" go test -count=1 "${existing[@]}"
    ran=1
  fi
fi

if [ "$ran" = 0 ]; then
  echo "No checks apply to these changes."
else
  echo
  echo "Changed-file checks passed. Run scripts/check-changes.sh --full before committing."
fi
