#!/bin/sh
set -e
quickshell --no-color -p "$TESTDIR/shell.qml" >"$TESTDIR/log" 2>&1 &
pid=$!
tries=0
until grep -q 'Configuration Loaded' "$TESTDIR/log"; do
    sleep .1
    tries=$((tries + 1))
    if [ "$tries" -gt 80 ]; then exit 1; fi
done
win=$(xdotool search --name 'AIQuota Hover Test' | head -1)
xdotool mousemove --window "$win" 50 250
sleep .2
xdotool mousemove --window "$win" 395 5
sleep 1
xdotool mousemove --window "$win" 50 250
sleep .3
xdotool mousemove --window "$win" 395 5
sleep 3.3
xdotool mousemove --window "$win" 390 30
sleep .08
xdotool mousemove --window "$win" 350 100
sleep .4
xdotool mousemove --window "$win" 50 250
sleep .4
wait "$pid"
