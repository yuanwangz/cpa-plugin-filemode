#!/usr/bin/env bash
DIR=${1:?workdir}
for f in cpa mock; do [ -f "$DIR/$f.pid" ] && kill "$(cat "$DIR/$f.pid")" 2>/dev/null; rm -f "$DIR/$f.pid"; done
true
