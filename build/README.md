# Build Directory

The build directory is used to house all the build files and assets for your application. 

The structure is:

* bin - Output directory
* darwin - macOS specific files
* windows - Windows specific files
* sidecars - third-party binaries shipped alongside the app (k6); see `sidecars/README.md`

## Releasing (macOS)

`wails build` alone does **not** produce a shippable artifact: it emits an
unsigned `build/bin/AUK.app` with nothing in `Contents/Resources` but the
icon, so Gatekeeper blocks it on any other Mac and every load test fails with
"k6 is not available".

Use `scripts/release.sh` instead. It runs the whole pipeline:

1. `wails build -clean` (and verifies the binary actually landed — Wails has
   been seen to print "Done." without writing `Contents/MacOS/AUK`)
2. `scripts/bundle-k6.sh` stages the pinned, checksum-verified k6 into
   `Contents/Resources/bin/k6` together with `k6-LICENSE.txt` and
   `k6-NOTICE.txt` (AGPL-3.0 obligations — see `sidecars/README.md`)
3. codesigns the **nested k6 first**, then the app bundle with
   `darwin/entitlements.plist`. Order matters: the bundle signature seals
   everything inside it, so staging k6 afterwards would invalidate it.
   `--deep` is deliberately not used
4. notarizes + staples the app, builds a UDZO DMG with an `/Applications`
   symlink, then signs + notarizes + staples the DMG
5. verifies with `spctl -a`, `stapler validate`, and a k6 signature check

Configuration is all environment variables — no secrets in the repo:
`AUK_SIGN_IDENTITY`, `AUK_ASC_KEY_P8`, `AUK_ASC_KEY_ID`, `AUK_ASC_ISSUER`,
`AUK_VERSION`, `AUK_PLATFORM`, `AUK_K6_TARGET`, `AUK_SKIP_NOTARIZE`. The
Developer ID identity must already be in a keychain on the search list
(`security find-identity -v -p codesigning`); the script does not create one.

`wails` and `node` come from a version-managed toolchain — put them on `PATH`
before running the script.

### Architecture

The app builds arm64-only by default (`AUK_PLATFORM=darwin/arm64`), so an
arm64 k6 is bundled. `bundle-k6.sh` reads the architecture back off the built
binary with `lipo -archs` and refuses to guess for a universal binary — if the
app is ever built universal, the k6 story has to be revisited (upstream ships
per-arch k6 builds, not a universal one, so it would mean `lipo`-ing two
downloads together, which changes the "unmodified upstream binary" claim in
`k6-NOTICE.txt`).

## Mac

The `darwin` directory holds files specific to Mac builds.
These may be customised and used as part of the build. To return these files to the default state, simply delete them
and
build with `wails build`.

The directory contains the following files:

- `Info.plist` - the main plist file used for Mac builds. It is used when building using `wails build`.
- `Info.dev.plist` - same as the main plist file but used when building using `wails dev`.

## Windows

The `windows` directory contains the manifest and rc files used when building with `wails build`.
These may be customised for your application. To return these files to the default state, simply delete them and
build with `wails build`.

- `icon.ico` - The icon used for the application. This is used when building using `wails build`. If you wish to
  use a different icon, simply replace this file with your own. If it is missing, a new `icon.ico` file
  will be created using the `appicon.png` file in the build directory.
- `installer/*` - The files used to create the Windows installer. These are used when building using `wails build`.
- `info.json` - Application details used for Windows builds. The data here will be used by the Windows installer,
  as well as the application itself (right click the exe -> properties -> details)
- `wails.exe.manifest` - The main application manifest file.