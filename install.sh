#!/bin/sh
# Installs the flowmaker binary of a release, for Linux or macOS:
#
#   curl -fsSL https://raw.githubusercontent.com/simonyosb/flowmaker/main/install.sh | sh
#
# It downloads the archive for this system from the GitHub release, checks it
# against the release's checksums.txt, and puts flowmaker in ~/.local/bin.
# Nothing else is changed, and nothing needs sudo.
#
#   FLOWMAKER_VERSION      the release to install, such as v0.1.0 (default: the latest)
#   FLOWMAKER_INSTALL_DIR  where to put the binary (default: ~/.local/bin)
#   FLOWMAKER_REPO_URL     where the releases are (default: the GitHub repository)

set -eu

repo="${FLOWMAKER_REPO_URL:-https://github.com/simonyosb/flowmaker}"

say() { printf '%s\n' "$*"; }
fail() { printf 'flowmaker install: %s\n' "$*" >&2; exit 1; }

# fetch writes what is at the URL $1 to the file $2.
fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --proto '=https,file' --proto-redir '=https' --tlsv1.2 -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1"
	else
		fail "needs curl or wget to download the release"
	fi
}

# latest prints the tag of the latest release, which GitHub redirects to.
latest() {
	if command -v curl >/dev/null 2>&1; then
		url=$(curl -fsSLI --proto '=https' --proto-redir '=https' --tlsv1.2 -o /dev/null -w '%{url_effective}' "$repo/releases/latest") || url=
	else
		url=$(wget -q -S --spider "$repo/releases/latest" 2>&1 | sed -n 's/^ *[Ll]ocation: *//p' | tr -d '\r' | tail -n 1)
	fi
	case "$url" in
		*/releases/tag/*) printf '%s\n' "${url##*/}" ;;
		*) fail "could not tell the latest release of $repo; set FLOWMAKER_VERSION, such as v0.1.0" ;;
	esac
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d ' ' -f 1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d ' ' -f 1
	else
		fail "needs sha256sum or shasum to check the download"
	fi
}

main() {
	case "$(uname -s)" in
		Linux) os=linux ;;
		Darwin) os=darwin ;;
		MINGW* | MSYS* | CYGWIN*) fail "on Windows, download the zip archive from $repo/releases" ;;
		*) fail "there is no release for $(uname -s); build it with Go: go install github.com/simonyosb/flowmaker/cmd/flowmaker@latest" ;;
	esac
	case "$(uname -m)" in
		x86_64 | amd64) arch=amd64 ;;
		arm64 | aarch64) arch=arm64 ;;
		*) fail "there is no release for $(uname -m); build it with Go: go install github.com/simonyosb/flowmaker/cmd/flowmaker@latest" ;;
	esac
	# A shell under Rosetta says x86_64 on a Mac that can run arm64.
	if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n hw.optional.arm64 2>/dev/null)" = 1 ]; then
		arch=arm64
	fi

	tag="${FLOWMAKER_VERSION:-}"
	if [ -z "$tag" ]; then
		tag=$(latest)
	fi
	case "$tag" in
		v*) ;;
		*) tag="v$tag" ;;
	esac
	archive="flowmaker_${tag#v}_${os}_${arch}.tar.gz"
	dir="${FLOWMAKER_INSTALL_DIR:-$HOME/.local/bin}"

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 1' INT TERM

	say "Downloading flowmaker $tag for $os/$arch"
	fetch "$repo/releases/download/$tag/$archive" "$tmp/$archive" || fail "could not download $archive of release $tag"
	fetch "$repo/releases/download/$tag/checksums.txt" "$tmp/checksums.txt" || fail "could not download the checksums of release $tag"

	want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
	[ -n "$want" ] || fail "checksums.txt of release $tag does not list $archive"
	[ "$(sha256 "$tmp/$archive")" = "$want" ] || fail "$archive does not match its checksum; nothing was installed"

	tar -xzf "$tmp/$archive" -C "$tmp" flowmaker
	mkdir -p "$dir"
	# Moved into place in one step, so a flowmaker that is running is not
	# changed under it.
	cp "$tmp/flowmaker" "$dir/.flowmaker.new"
	chmod 755 "$dir/.flowmaker.new"
	mv -f "$dir/.flowmaker.new" "$dir/flowmaker"

	say "Installed $("$dir/flowmaker" version) in $dir"
	case ":$PATH:" in
		*":$dir:"*) ;;
		*)
			say ""
			say "$dir is not on your PATH. Add it, for example in ~/.profile or ~/.zshrc:"
			say ""
			say "  export PATH=\"$dir:\$PATH\""
			;;
	esac
	say ""
	say "Try it without a key, on the examples:"
	say ""
	say "  git clone https://github.com/simonyosb/flowmaker && cd flowmaker"
	say "  flowmaker test examples/triage-ticket --replay"
}

# Everything runs from here, so a download cut short runs nothing.
main "$@"
