# Maintainer: nicholas-fedor <nick@nicholasfedor.com>
pkgname=cliamp
_basever=1.27.1
pkgver=1.27.1
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
source=("git+https://github.com/nicholas-fedor/${pkgname}.git#branch=main")
sha256sums=('SKIP')

# Locate the checked-out source directory.
_srcdir() {
    find "${srcdir}" -maxdepth 1 -mindepth 1 -type d -name "${pkgname}" -print -quit
}

pkgver() {
    cd "$(_srcdir)"

    # Produce a version like: 1.27.1.r284.g1aaeac4
    # (base version + commit count + abbreviated hash).
    # Arch pkgver disallows hyphens, so use dots as separators.
    local _rev _sha
    _rev="$(git rev-list --count HEAD)"
    _sha="$(git rev-parse --short HEAD)"

    printf '%s.r%s.g%s' "${_basever}" "${_rev}" "${_sha}"
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

    local _rev _sha _version
    _rev="$(git rev-list --count HEAD)"
    _sha="$(git rev-parse --short HEAD)"
    _version="v${_basever}.r${_rev}.g${_sha}"

    export GOPATH="${srcdir}/go"
    export GO111MODULE=on
    export CGO_ENABLED=1
    export GOFLAGS="-trimpath"

    go build \
        -buildmode=pie \
        -ldflags "-s -w -X main.version=${_version} -linkmode=external" \
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
