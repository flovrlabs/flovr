#!/bin/sh
# Runs the tests of flovr's own page in uitests/, each written as what a
# person would do on the page, against a server that serves a copy of the
# examples with their recorded answers:
#
#   sh uitests/run.sh            the decisions recorded in answers.jsonl, no model called
#   sh uitests/run.sh --record   asks the decision model again and keeps its answers; needs OPENROUTER_API_KEY, costs a little
#   sh uitests/run.sh --record flovr-page   one test alone, by the name of its folder
#
# It needs Go, git and Chrome or Chromium (CHROME names another browser of the
# kind), and serves on 127.0.0.1:18086 (UITEST_PORT changes it).
set -eu

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
port=${UITEST_PORT:-18086}
mode=${1:---replay}

work=$(mktemp -d)
server=
cleanup() {
	if [ -n "$server" ]; then kill "$server" 2>/dev/null || true; fi
	rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 1' INT TERM

(cd "$repo" && go build -o "$work/flovr" ./cmd/flovr)
site="$work/site"
mkdir -p "$site"
cp -R "$repo/examples" "$site/"
find "$site" -name .DS_Store -delete
git -C "$site" init -q -b main
git -C "$site" add -A
git -C "$site" -c user.name=flovr -c user.email=flovr@example.invalid commit -q -m Examples

key=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
(cd "$site" && exec env -u OPENROUTER_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY \
	FLOVR_PAGE_KEY="$key" PAGER_URL="http://127.0.0.1:9/pager" BILLING_URL="http://127.0.0.1:9/billing" \
	"$work/flovr" serve examples --edit --replay --market "" --addr "127.0.0.1:$port") >"$work/serve.log" 2>&1 &
server=$!
i=0
until curl -fs "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; do
	i=$((i + 1))
	if [ "$i" -gt 50 ]; then echo "run.sh: the server did not start" >&2; cat "$work/serve.log" >&2; exit 1; fi
	sleep 0.2
done

# One test alone, by the name of its folder, or all of them.
failed=0
for dir in "$here"/${2:-*}/; do
	FLOVR_PAGE="http://127.0.0.1:$port/?key=$key#/workflows" "$work/flovr" test "$mode" "$dir" || failed=1
done
exit $failed
