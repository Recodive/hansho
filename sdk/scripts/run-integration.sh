#!/bin/sh
# Builds the real hub, a stub relay and a fake device, starts the first two on
# 127.0.0.1, then runs each SDK's integration suite against them: pairing,
# channels, templates, sounds, messages at every level, a page and its ack,
# Live Activities, feeds and signed webhooks.
#
#   ./sdk/scripts/run-integration.sh
#   HANSHO_HUB_BIN=/path/to/hub ./sdk/scripts/run-integration.sh   # use an existing hub binary
#
# Everything is local: no external ingress, no real relay.
set -eu
here="$(cd "$(dirname "$0")" && pwd)"
repo="$here/../.."
out="$(mktemp -d)"
pids=""

cleanup() {
  status=$?
  for p in $pids; do kill "$p" 2>/dev/null || true; done
  if [ "$status" -ne 0 ]; then
    echo "--- hub log (tail) ---" >&2
    tail -n 40 "$out/hub.log" 2>/dev/null >&2 || true
  fi
  rm -rf "$out"
  exit "$status"
}
trap cleanup EXIT INT TERM

# Waits until FILE contains a line starting with PREFIX and prints the rest of that line.
wait_line() {
  file=$1 prefix=$2 i=0
  while [ $i -lt 150 ]; do
    line=$(sed -n "s/^$prefix//p" "$file" 2>/dev/null | head -n 1)
    if [ -n "$line" ]; then printf '%s\n' "$line"; return 0; fi
    i=$((i + 1)); sleep 0.1
  done
  echo "timed out waiting for '$prefix' in $file" >&2
  return 1
}

if [ -z "${HANSHO_HUB_BIN:-}" ]; then
  (cd "$repo/hub" && go build -o "$out/hub" ./cmd/hub)
  HANSHO_HUB_BIN="$out/hub"
fi
(cd "$here/stubrelay" && go build -o "$out/stubrelay" main.go)
(cd "$here/fakedevice" && go build -o "$out/fakedevice" main.go)
HANSHO_FAKEDEVICE_BIN="$out/fakedevice"

# Stub relay: learns its address from the first stdout line.
"$out/stubrelay" 127.0.0.1:0 >"$out/relay.log" 2>&1 &
pids="$pids $!"
relay_addr=$(wait_line "$out/relay.log" "listening ")

# The webhook receiver lives inside each SDK test process; the hub needs its address up front.
webhook_port=$("$HANSHO_FAKEDEVICE_BIN" freeport)
HANSHO_WEBHOOK_SECRET="whsec_integration_$$"
HANSHO_WEBHOOK_ADDR="127.0.0.1:$webhook_port"

for v in $(env | sed -n 's/^\(HUB_[A-Za-z0-9_]*\)=.*/\1/p'); do unset "$v"; done
export HUB_ADDR="127.0.0.1:0"
export HUB_PUBLIC_URL="https://hub.test.invalid"
export HUB_DISPLAY_NAME="SDK Integration Hub"
export HUB_DB_PATH="$out/hub.db"
export HUB_KEY_PATH="$out/hub.key"
export HUB_RELAY_URL="http://$relay_addr"
export HUB_ID="hub_integration"
export HUB_MASTER_KEY="$(head -c 32 /dev/urandom | base64)"
export HUB_WEBHOOK_URL="http://$HANSHO_WEBHOOK_ADDR/hook"
export HUB_WEBHOOK_SECRET="$HANSHO_WEBHOOK_SECRET"

HANSHO_API_KEY=$("$HANSHO_HUB_BIN" apikey create integration)
"$HANSHO_HUB_BIN" serve >"$out/hub.log" 2>&1 &
pids="$pids $!"
hub_addr=$(wait_line "$out/hub.log" "hansho hub listening on " | cut -d' ' -f1)
HANSHO_HUB_URL="http://$hub_addr"
i=0
until curl -fsS "$HANSHO_HUB_URL/health" >/dev/null 2>&1; do
  i=$((i + 1)); [ $i -lt 100 ] || { echo "hub did not become healthy" >&2; exit 1; }
  sleep 0.1
done

export HANSHO_HUB_BIN HANSHO_HUB_URL HANSHO_API_KEY HANSHO_WEBHOOK_SECRET HANSHO_WEBHOOK_ADDR HANSHO_FAKEDEVICE_BIN
echo "hub $HANSHO_HUB_URL  relay http://$relay_addr  webhook receiver $HANSHO_WEBHOOK_ADDR"

echo "=== Go SDK ==="
(cd "$here/../go" && go test -tags integration -run Integration -count=1 -v ./...)

echo "=== Node SDK ==="
cd "$here/../node"
[ -d node_modules ] || npm ci --no-audit --no-fund
node --test test/integration.test.ts

echo "=== Python SDK ==="
(cd "$here/../python" && python3 -m unittest -v tests.test_integration)

echo "=== Ruby SDK ==="
(cd "$here/../ruby" && bundle install --quiet && bundle exec ruby -Itest test/integration_test.rb -v)

echo "=== PHP SDK ==="
(cd "$here/../php" && { [ -d vendor ] || composer install --no-interaction --no-progress --quiet; } && vendor/bin/phpunit --testsuite integration --testdox)

echo "=== Java SDK ==="
(cd "$here/../java" && mvn -B -q verify -Dtest=IntegrationTest)

echo "=== .NET SDK ==="
(cd "$here/../dotnet" && dotnet test tests/Hansho.Tests -f net10.0 -c Release --filter "Category=Integration" -nodeReuse:false)

echo "=== Rust SDK ==="
(cd "$here/../rust" && cargo test --test integration -- --nocapture)

echo "integration: OK"
