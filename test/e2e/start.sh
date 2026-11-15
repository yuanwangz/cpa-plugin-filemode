#!/usr/bin/env bash
# Start the mock upstream and a local CPA with the plugin. Usage: start.sh <cpa-binary> <plugin.so> <workdir>
set -euo pipefail
CPA_BIN=${1:?cpa binary}; SO=${2:?plugin .so}; DIR=${3:?workdir}
MOCK_PORT=${MOCK_PORT:-18080}; CPA_PORT=${CPA_PORT:-18317}
HERE=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$DIR/plugins" "$DIR/auths"
cp "$SO" "$DIR/plugins/filemode.so"
sed -e "s#__DIR__#$DIR#g" -e "s#__CPA_PORT__#$CPA_PORT#g" -e "s#__MOCK_PORT__#$MOCK_PORT#g" "$HERE/config.yaml.tmpl" > "$DIR/config.yaml"
: > "$DIR/mock.jsonl"
for p in $MOCK_PORT $CPA_PORT; do if curl -s --noproxy "*" -o /dev/null "http://127.0.0.1:$p/" 2>/dev/null; then echo "port $p already in use; stop the old run first"; exit 1; fi; done
# setsid + exec so the pid files hold the real server pids (stop.sh kills them)
MOCK_LOG="$DIR/mock.jsonl" setsid bash -c 'echo $$ > "$0/mock.pid"; exec python3 "$1" "$2"' "$DIR" "$HERE/mock_upstream.py" "$MOCK_PORT" > "$DIR/mock.out" 2>&1 < /dev/null &
(cd "$DIR" && NO_PROXY=127.0.0.1,localhost no_proxy=127.0.0.1,localhost setsid bash -c 'echo $$ > "$0/cpa.pid"; exec "$1" -config "$0/config.yaml" -local-model -no-browser' "$DIR" "$CPA_BIN" > "$DIR/cpa.out" 2>&1 < /dev/null &)
for i in $(seq 1 60); do
  if [ "$(curl -s --noproxy "*" -o /dev/null -w "%{http_code}" "http://127.0.0.1:$CPA_PORT/v1/models" -H "Authorization: Bearer test-key")" = "200" ]; then echo "CPA up"; exit 0; fi
  sleep 0.5
done
echo "CPA did not start"; tail -50 "$DIR/cpa.out"; exit 1
