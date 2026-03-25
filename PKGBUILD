# Maintainer: nicholas-fedor <nick@nicholasfedor.com>
pkgname=cliamp
_basever=1.27.1
pkgver=1.27.1+main
pkgrel=1
pkgdesc="Retro terminal music player with Spotify, YouTube, Navidrome, and Plex support"
arch=("x86_64" "aarch64")
url="https://github.com/nicholas-fedor/cliamp"
license=("MIT")
depends=("alsa-lib" "flac" "libvorbis" "libogg")
makedepends=("go>=1.26.1" "git" "gcc")
optdepends=(
    "ffmpeg: support for aac, opus, and wma audio formats"
    "yt-dlp: YouTube, SoundCloud, Bandcamp, and Bilibili playback"
    "pipewire-alsa: audio output on PipeWire systems"
    "pulseaudio-alsa: audio output on PulseAudio systems"
)
source=("${pkgname}-${pkgver}.tar.gz::https://github.com/nicholas-fedor/${pkgname}/archive/refs/heads/main.tar.gz")
sha256sums=("SKIP")

# Locate the extracted source directory (named cliamp-<branch>).
_srcdir() {
    find "${srcdir}" -maxdepth 1 -mindepth 1 -type d -name 'cliamp-*' -print -quit
}

pkgver() {
    # Branch tarballs from GitHub use the branch name as the directory suffix
    # (e.g., cliamp-main). Append it to the base version.
    local _dir="$(_srcdir)"
    local _branch="${_dir##*-}"

    printf '%s+%s' "${_basever}" "${_branch}"
}

prepare() {
    cd "$(_srcdir)"

    mkdir -p "${srcdir}/go"
    export GOPATH="${srcdir}/go"
    export GO111MODULE=on

    go mod download
}

build() {
    cd "$(_srcdir)"

    local _branch
    _branch="$(basename "$(_srcdir)")"
    _branch="${_branch##*-}"

    export GOPATH="${srcdir}/go"
    export GO111MODULE=on
    export CGO_ENABLED=1
    export GOFLAGS="-trimpath"

    go build \
        -buildmode=pie \
        -ldflags "-s -w -X main.version=v${_basever}+${_branch} -linkmode=external" \
        -o "${pkgname}" \
        .
}

package() {
    cd "$(_srcdir)"

    install -Dm755 "${pkgname}" "${pkgdir}/usr/bin/${pkgname}"

    # Install license.
    install -Dm644 LICENSE "${pkgdir}/usr/share/licenses/${pkgname}/LICENSE"

    # Install example configuration.
    install -Dm644 config.toml.example "${pkgdir}/usr/share/doc/${pkgname}/config.toml.example"
}
