#!/usr/bin/env bash
# One-shot e2e: start mock + CPA, run the checks, stop everything.
# Usage: run.sh <cpa-binary> <plugin.so> <workdir>
HERE=$(cd "$(dirname "$0")" && pwd)
DIR=${3:?workdir}
rm -rf "$DIR"; mkdir -p "$DIR"
trap '"$HERE/stop.sh" "$DIR"' EXIT
"$HERE/start.sh" "$1" "$2" "$DIR" || exit 1
sleep "${SETTLE:-2}"
python3 "$HERE/e2e_test.py" "$DIR"
