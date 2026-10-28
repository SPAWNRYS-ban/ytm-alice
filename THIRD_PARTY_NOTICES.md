# Dependencies

The Go executable includes github.com/kkdai/youtube/v2 (MIT), its Goja JavaScript
runtime and dependencies, and the Go standard library. Exact module versions and
checksums are recorded in go.mod and go.sum; `go version -m bin/ytm-alice` also
reports the embedded dependency versions. Upstream source and license texts:

- https://github.com/kkdai/youtube (MIT)
- https://github.com/dop251/goja (MIT)
- https://github.com/bitly/go-simplejson (MIT)
- https://github.com/dlclark/regexp2 (MIT)
- https://github.com/go-sourcemap/sourcemap (BSD-2-Clause)
- https://github.com/google/pprof (BSD-3-Clause)
- https://go.googlesource.com/text (BSD-3-Clause)
- https://go.dev/LICENSE (BSD-3-Clause)

The native player is dynamically linked to GStreamer/GLib already present on the
Station. No Yandex firmware libraries, device identities, account files,
certificates, cookies or private keys are included in this package.

MPRIS / D-Bus: github.com/godbus/dbus/v5 (BSD-2-Clause), golang.org/x/sys (BSD-3-Clause).
Лицензии приложены в licenses/.

## Bundled media integration

This single YAM package includes bt-proxy by chkzz, distributed through
Marker284/yandex-station-extended, revision
c8c743b10c4f540c0a690eb7cb0cf5a92b7f336b, under CC BY 4.0.
https://github.com/Marker284/yandex-station-extended/tree/c8c743b10c4f540c0a690eb7cb0cf5a92b7f336b/contrib/bt-proxy

The upstream source, build script and lock file are included in media/upstream/;
the attribution and license are in media/README.md and media/UPSTREAM-LICENSE.
The MIT media wrapper has been adapted to load its bus configuration from the
ytm-alice package directory. No endorsement by the upstream authors is implied.
The ytm-alice code and wrapper remain MIT; the combined package is declared as
MIT AND CC-BY-4.0. Other dependencies retain the licenses listed above.

The Rust media executable's locked dependency license texts are collected from
Cargo registry sources into licenses/rust/ during the release build. Its index
records crate names, versions and SPDX expressions. The release source archive
contains the same notices. Cargo.lock records checksums of the upstream sources.

## Standard libraries and native runtime

The static Go service includes the Go standard library (BSD-3-Clause).
The static Rust media executable includes the Rust standard library
(Apache-2.0 OR MIT), compiler-builtins and musl libc. Their notices and license
texts are retained in licenses/runtime/, alongside the Cargo dependency texts.
Sources: https://github.com/rust-lang/rust/tree/1.99.0,
https://github.com/rust-lang/compiler-builtins and https://musl.libc.org/.
