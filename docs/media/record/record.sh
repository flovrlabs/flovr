#!/bin/sh
# Makes the recordings and pictures in docs/media again, from the examples
# and their recorded answers, so that no model is called:
#
#   sh docs/media/record/record.sh
#
# It needs Go, Node 22 or later, Google Chrome (CHROME names another
# browser of the same kind), ffmpeg, vhs and git. It works in a folder of its
# own, which it removes, and serves on 127.0.0.1:18085 and :18765
# (RECORD_PORT and RECORD_SINK_PORT change them).
set -eu

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
media="$repo/docs/media"
port=${RECORD_PORT:-18085}
sink=${RECORD_SINK_PORT:-18765}

for tool in go node git ffmpeg vhs; do
	command -v "$tool" >/dev/null 2>&1 || { echo "record.sh: needs $tool" >&2; exit 1; }
done

work=$(mktemp -d)
server=
cleanup() {
	if [ -n "$server" ]; then kill "$server" 2>/dev/null || true; fi
	rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 1' INT TERM

echo "Building flovr"
mkdir -p "$work/bin"
(cd "$repo" && go build -o "$work/bin/flovr" ./cmd/flovr)

# A copy of the examples in a repository of its own, so that the page's
# Changes has a commit to compare with, and the examples here stay as they are.
site="$work/site"
mkdir -p "$site"
cp -R "$repo/examples" "$site/"
find "$site" -name .DS_Store -delete
printf '.flovr/\n' > "$site/.gitignore"
git -C "$site" init -q -b main
git -C "$site" add -A
git -C "$site" -c user.name=flovr -c user.email=flovr@example.invalid commit -q -m Examples

key=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
echo "Serving the examples with their recorded answers"
(cd "$site" && exec env -u OPENROUTER_API_KEY -u OPENAI_API_KEY -u ANTHROPIC_API_KEY \
	FLOVR_PAGE_KEY="$key" PAGER_URL="http://127.0.0.1:$sink/pager" BILLING_URL="http://127.0.0.1:$sink/billing" \
	"$work/bin/flovr" serve examples --edit --replay --market "" --addr "127.0.0.1:$port") >"$work/serve.log" 2>&1 &
server=$!

echo "Recording the page"
RECORD_URL="http://127.0.0.1:$port" FLOVR_PAGE_KEY="$key" RECORD_SITE="$site" RECORD_WORK="$work" RECORD_SINK_PORT="$sink" \
	node "$here/record.mjs"

echo "Recording the terminal"
(cd "$work" && RECORD_SITE="$site" RECORD_BIN="$work/bin" vhs "$here/terminal.tape" >/dev/null)

echo "Encoding"
# A quarter faster than it was recorded, 15 frames a second, at half the
# size Chrome drew it.
ffmpeg -loglevel error -y -f concat -safe 0 -i "$work/frames/frames.txt" \
	-vf "setpts=PTS/1.25,fps=15,scale=1120:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=160:stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=5:diff_mode=rectangle" \
	"$media/demo.gif"
for name in canvas editor; do
	ffmpeg -loglevel error -y -i "$work/$name.png" -vf "scale=1680:-1:flags=lanczos" "$media/$name.png"
done
cp "$work/terminal.gif" "$media/terminal.gif"
echo "Written: docs/media/demo.gif, terminal.gif, canvas.png and editor.png"
