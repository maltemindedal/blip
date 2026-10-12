#!/usr/bin/env bash
# Drive Blip the way two WebSocket clients do and keep the evidence.
# Usage: verify.sh <doctor|build|test|lint|launch|health|relay|stop|cleanup|all>
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(git -C "$HERE" rev-parse --show-toplevel)"
RUN_ID="${BLIP_VERIFY_RUN:-$(date +%Y%m%d-%H%M%S)}"
ART="${BLIP_VERIFY_ARTIFACTS:-/tmp/blip-verify/$RUN_ID}"
WORK="$ART/work"
BIN="$WORK/blip"
mkdir -p "$ART" "$WORK"
cd "$ROOT" || exit 1

FAILED=0
SKIPPED=0

run() {
  local name="$1"; shift
  printf '%q ' "$@" > "$ART/$name.cmd"; echo >> "$ART/$name.cmd"
  "$@" > "$ART/$name.stdout" 2> "$ART/$name.stderr"
  local rc=$?
  echo "$rc" > "$ART/$name.exit"
  echo "[$name] exit=$rc  ($ART/$name.*)"
  return "$rc"
}

expect() {
  local desc="$1" file="$2" pattern="$3"
  if grep -qE -- "$pattern" "$file"; then echo "  PASS $desc"; else echo "  FAIL $desc (no /$pattern/ in $file)"; FAILED=1; fi
}

port() { cat "$ART/server.port" 2>/dev/null; }

doctor() {
  echo "artifacts: $ART"
  echo "repo: $ROOT @ $(git rev-parse --short HEAD) ($(git rev-parse --abbrev-ref HEAD))"
  command -v go >/dev/null || { echo "  FAIL go not on PATH"; return 1; }
  local want; want="$(sed -n 's/^go //p' go.mod)"
  local have; have="$(go env GOVERSION)"
  echo "go: $have (go.mod needs $want; GOTOOLCHAIN=$(go env GOTOOLCHAIN))"
  [ "$have" = "go$want" ] || [ "$(printf '%s\n%s\n' "go$want" "$have" | sort -V | head -1)" = "go$want" ] && echo "  PASS go toolchain satisfies go.mod" || { echo "  FAIL go $have is older than go.mod's $want"; return 1; }
  command -v curl >/dev/null && echo "  PASS curl present" || { echo "  FAIL curl missing"; return 1; }
  local gl; gl="$(go env GOPATH)/bin/golangci-lint"
  [ -x "$gl" ] && echo "  PASS $gl present for lint" || echo "  WARN no golangci-lint in GOPATH/bin (make install-tools): lint will SKIP"
  if [ -s "$ART/server.pid" ] && kill -0 "$(cat "$ART/server.pid")" 2>/dev/null; then echo "  PASS server from this run is up: pid $(cat "$ART/server.pid") on port $(port)"; else echo "  INFO no server from this run is running"; fi
}

build() { run build go build -o "$BIN" ./cmd/server; }

test_go() {
  run test go test -race -shuffle=on ./... || FAILED=1
  expect "go test -race exits 0" "$ART/test.exit" "^0$"
  if grep -E "^(FAIL|--- FAIL)" "$ART/test.stdout" > "$ART/test.failures"; then echo "  FAIL $(wc -l < "$ART/test.failures") failing lines in test.stdout"; FAILED=1; fi
  echo "  $(grep -c '^ok ' "$ART/test.stdout") packages ok"
}

lint() {
  local gl; gl="$(go env GOPATH)/bin/golangci-lint"
  [ -x "$gl" ] || { echo "  SKIP lint: run make install-tools to build CI's pinned golangci-lint"; SKIPPED=1; return; }
  run lint "$gl" run --timeout=10m || FAILED=1
  expect "golangci-lint exits 0" "$ART/lint.exit" "^0$"
}

launch() {
  if [ -s "$ART/server.pid" ] && kill -0 "$(cat "$ART/server.pid")" 2>/dev/null; then echo "  FAIL a server from this run is already up; run stop first"; FAILED=1; return 1; fi
  [ -x "$BIN" ] || build || { FAILED=1; return 1; }
  local p; p="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
  echo "$p" > "$ART/server.port"
  (cd "$WORK" && SERVER_PORT="127.0.0.1:$p" ALLOWED_ORIGINS="http://localhost:$p" LOG_LEVEL=debug exec "$BIN" > "$ART/server.log" 2>&1) &
  echo $! > "$ART/server.pid"
  local i
  for i in $(seq 1 50); do
    curl -fsS "http://127.0.0.1:$p/" > /dev/null 2>&1 && break
    sleep 0.1
  done
  if grep -q 'msg="server listening"' "$ART/server.log" && curl -fsS "http://127.0.0.1:$p/" > /dev/null 2>&1; then
    echo "  PASS server ready on 127.0.0.1:$p (pid $(cat "$ART/server.pid"), log says server listening, / answers 200)"
  else
    echo "  FAIL server not ready (see $ART/server.log)"; FAILED=1; return 1
  fi
}

health() {
  [ -s "$ART/server.port" ] || { echo "  FAIL no server; run launch first"; FAILED=1; return; }
  run health curl -sS -o "$ART/health.body" -w '%{http_code}\n' "http://127.0.0.1:$(port)/"
  expect "GET / returns 200" "$ART/health.stdout" "^200$"
  run testpage curl -sS -o "$ART/testpage.html" -w '%{http_code} %{content_type}\n' "http://127.0.0.1:$(port)/test"
  expect "GET /test serves HTML" "$ART/testpage.stdout" "^200 text/html"
}

relay() {
  [ -s "$ART/server.port" ] || { echo "  FAIL no server; run launch first"; FAILED=1; return; }
  run relay go run ./.agents/skills/verify-blip/wsclient -addr "127.0.0.1:$(port)" -origin "http://localhost:$(port)"
  expect "handshake without Origin is refused with 403" "$ART/relay.stdout" "^no-origin handshake status: 403$"
  expect "two clients connect with the allowed Origin" "$ART/relay.stdout" "^both clients connected$"
  expect "receiver gets the normalized, escaped message" "$ART/relay.stdout" '^receiver got: \{"content":"\\u003chi\\u003e \\u0026 bye"\}$'
  expect "sender does not receive its own message" "$ART/relay.stdout" "^sender got nothing \(expected\)$"
  expect "server log shows both clients registered" "$ART/server.log" 'msg="client registered".*total_clients=2'
}

stop() {
  [ -s "$ART/server.pid" ] || return 0
  local pid; pid="$(cat "$ART/server.pid")"
  kill "$pid" 2>/dev/null
  local i; for i in $(seq 1 100); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
  kill -0 "$pid" 2>/dev/null && kill -9 "$pid" 2>/dev/null
  rm -f "$ART/server.pid"
  echo "  stopped pid $pid"
}

cleanup() {
  stop
  rm -rf "$WORK"
  echo "cleanup done; evidence kept in $ART"
}

STEP="${1:-}"
case "$STEP" in
  doctor) doctor || FAILED=1 ;;
  build) build || FAILED=1 ;;
  test) test_go ;;
  lint) lint ;;
  launch) launch ;;
  health) health ;;
  relay) relay ;;
  stop) stop ;;
  cleanup) cleanup ;;
  all)
    if doctor; then test_go; lint; launch && { health; relay; }; else FAILED=1; echo "  doctor failed: not driving this checkout"; fi
    cleanup ;;
  *) sed -n '2,3p' "$0"; exit 2 ;;
esac
if [ "$FAILED" != 0 ]; then echo "RESULT: FAIL ($STEP)"; elif [ "$SKIPPED" != 0 ]; then echo "RESULT: PASS with SKIP ($STEP): skipped steps are unverified"; else echo "RESULT: PASS ($STEP)"; fi
exit "$FAILED"
