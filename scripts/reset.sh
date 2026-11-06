#!/usr/bin/env bash
# Clears local 春日影 LX data so the app opens as on first use.
#
#   scripts/reset.sh              development build
#   scripts/reset.sh --release    installed macOS build
#   scripts/reset.sh --songs      also clears cached audio
set -euo pipefail

name="春日影 LX"
id="com.cxer1001.haruhikage.sources"
release=0 songs=0
for arg in "$@"; do
	case "$arg" in
	--release) release=1 ;;
	--songs) songs=1 ;;
	-h | --help)
		sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "unknown option: $arg" >&2
		exit 2
		;;
	esac
done
if [[ $release == 0 ]]; then
	name+=" Dev"
	id+=".dev"
fi

if [[ $(uname) != Darwin ]]; then
	echo "only macOS for now" >&2
	exit 1
fi
data="$HOME/Library/Application Support/$name"
cache="$HOME/Library/Caches/$name"

if pgrep -f "/$name.app/Contents/MacOS/" >/dev/null; then
	echo "quitting $name…"
	osascript -e "tell application id \"$id\" to quit" >/dev/null 2>&1 || true
	for _ in $(seq 50); do
		pgrep -f "/$name.app/Contents/MacOS/" >/dev/null || break
		sleep 0.1
	done
	if pgrep -f "/$name.app/Contents/MacOS/" >/dev/null; then
		echo "$name is still running: quit it and try again" >&2
		exit 1
	fi
fi

if [[ -d $data ]]; then
	rm -rf "$data"
	echo "removed ${data/#$HOME/~}"
fi
if [[ $songs == 1 && -d $cache ]]; then
	rm -rf "$cache"
	echo "removed ${cache/#$HOME/~}"
fi
echo "$name will open as on first use"
