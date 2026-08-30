# 07 — In-app auto-update

AUK ships as a signed, notarized DMG from GitHub Releases. Wails v2 has no
built-in updater (v3's bsdiff updater is a future port — see roadmap 6.6), so
this is our own. It is intentionally small and paranoid: a paid app has to be
able to update itself, and an updater that installs code has to *prove* what it
installs is ours before it runs it.

Everything lives in `internal/updater/` (pure, unit-tested logic + the
macOS-specific exec edges behind an interface), exposed to the UI by
`app_update.go` (Wails bindings) and driven by `frontend/src/lib/updater.ts` +
`frontend/src/components/UpdateBanner.tsx`.

---

## 1. The feed — where "what's the latest?" comes from

The source of "is there a newer release?" is behind a `Feed` interface
(`feed.go`):

```go
type Feed interface {
    Latest(ctx context.Context) (Release, error)
}
```

`Release` is a source-agnostic struct (`Version`, `Notes`, `URL`, `AssetName`,
`SizeBytes`, `SHA256`). Nothing downstream — version compare, download, verify,
UI — knows or cares where it came from.

**v1 implementation: `GitHubFeed`.** It calls the public GitHub REST endpoint

```
GET https://api.github.com/repos/sandeepshekhar26/auk/releases/latest
```

unauthenticated (no token, no infrastructure of ours), maps `tag_name` →
version (stripping the leading `v`), picks the `AUK-*.dmg` asset for the URL +
size, and lifts a SHA-256 out of the release body if one is published there.
GitHub's `/releases/latest` already excludes drafts and pre-releases, which is
exactly what a stable-channel updater wants.

Network hygiene: a 5-second-ish caller-set timeout, a 1 MiB response cap, a
`User-Agent` (GitHub rejects requests without one), and — critically — a
rate-limit (HTTP 403/429) or offline failure is returned as an ordinary error
that the caller folds into "unknown", never a panic and never a red banner. A
background launch check that can't reach GitHub simply shows nothing.

**Why an abstraction and not just GitHub calls inline:** the honest weakness of
the GitHub feed is that the "what's latest / what's its hash" answer is served
by the same host that serves the download. A future **signed appcast** — a
small JSON document we host and sign with an offline key, whose signature the
app verifies before trusting a word of it — drops in as a second `Feed`
implementation with no change to any other file. GitHub Releases is the v1
source; the abstraction is what keeps that a *choice* rather than a weld. (See
§6.)

---

## 2. The verification chain — the security core

This is the whole reason the updater is written carefully. It mirrors the rigor
of the existing k6 self-heal (`internal/perf/download.go`: bounded reads,
checksum-before-use, atomic install) and adds the check that actually matters
for installing an *executable*. `install.go`, top to bottom, trusts nothing
until every step passes:

1. **Bounded, timeout'd download.** The DMG streams to a temp dir under a
   256 MiB cap (the real DMG is ~42 MB). A redirect to something enormous can't
   fill the disk; a hung connection can't wedge the UI.

2. **SHA-256, *if* the release published one.** `release.sh` prints a
   `SHA-256  <hash>` line and the release body carries it (parsed by
   `parseSHA256FromBody`, tolerant of `SHA-256`/`SHA256`/`sha256`, an optional
   colon, or a bare `shasum` line). This catches a corrupt or truncated
   transfer. **It is deliberately not the trust anchor.** An attacker who could
   serve a malicious DMG could serve a matching hash in notes they also
   control — a checksum published next to the file it "verifies" proves
   integrity, not authenticity. So a missing hash is fine; a *present* one is a
   bonus integrity gate, not the guarantee.

3. **Code-signature integrity** — `codesign --verify --deep --strict`. The
   bundle's signature is intact and covers its contents.

4. **Team ID == `V8SAC4GCQQ`** — parsed from `codesign -dvvv`'s
   `TeamIdentifier=` line. **This is the real anti-tamper guarantee.** Apple's
   notary service refuses to notarize anything not signed by a valid Developer
   ID, and the private key for *our* Developer ID never leaves the signing
   machine. So an attacker cannot produce a bundle that is both notarized *and*
   carries our Team ID. Checking the Team ID is therefore strictly stronger
   than checking a checksum an attacker in the download path could also swap.

5. **Notarization / Gatekeeper acceptance** — `spctl --assess --type exec`.
   We require **both** an `accepted` verdict **and** a `Notarized Developer ID`
   source. A merely ad-hoc-signed or Developer-ID-signed-but-*not*-notarized
   app is rejected. This confirms Apple stapled a valid notarization ticket.

Any failure at any step → reject, `hdiutil detach`, install nothing, and return
an error naming the step that failed.

**What this rejects:** a tampered/re-packed DMG (checksum, if present, or the
deep signature check); an app signed by *anyone else's* Developer ID, even a
validly notarized one (Team-ID check — this is the attack the check exists to
stop); an unsigned or ad-hoc app; a Developer-ID app that was never notarized;
and a truncated/oversized download. These are covered by focused unit tests
(`install_test.go`: `TestVerifyAppBundle_RejectsWrongTeamID`,
`_RejectsNotNotarized`, `_RejectsBadSignature`, plus the accept path).

The mount/verify happens on the DMG's `.app`, the verified `.app` is copied to
a staging dir with `ditto` (which preserves the extended attributes the
signature depends on — a plain `cp -R` would strip them and break the very
signature we just verified), and the **staged copy is re-verified** — it's the
exact bits Install will swap in. The DMG is always detached, on every path.

---

## 3. The install flow — what's automatic, what's guided

The target is a real one-click **"Restart to update"**. It works like this:

1. `DownloadAndVerifyUpdate` (binding) runs §2 and leaves a verified `AUK.app`
   plus the verified DMG in
   `~/Library/Application Support/AUK/pending-update/`.
2. `InstallUpdate` (binding) **re-verifies the staged bundle one more time**
   (closing any gap between download and install — the staging dir is
   user-writable), then, if the current app's install location is writable:
   - writes a small detached `/bin/sh` helper into the staging dir and starts
     it in its **own session** (so quitting AUK doesn't take it down),
   - returns `{relaunching:true}` to the UI, and
   - quits AUK ~600 ms later (giving the webview time to paint "Restarting…").
3. The helper waits for AUK's PID to exit (so the running bundle is never
   modified underfoot), moves the old bundle aside **atomically**, `ditto`s the
   staged bundle into place (preserving the signature), relaunches with `open`,
   and cleans up. On any failure it rolls the old bundle back, so the user is
   never left with no app.

**Guided fallback.** If the app can't replace itself automatically — it lives
somewhere unwritable (e.g. a read-only location, or `/Applications` without
permission), or it's a dev build not running from a `.app` — `InstallUpdate`
instead opens the verified DMG in Finder and returns `{guided:true, message}`.
The banner shows the message; the user drags AUK to Applications as usual. The
bits were still fully verified first; only the copy is manual.

**Honest status of what shipped:** the one-click download → verify → swap →
relaunch path *is* implemented, not stubbed. The parts to treat with
appropriate caution on real hardware are the two OS-level steps that can't be
exercised in a unit test without a live signed bundle and a mount: the
`hdiutil attach`/`spctl` assessment of a *real* notarized DMG, and the
quit-swap-relaunch handoff. Those are factored behind the `runner` interface
and validated in the pure layer (Team-ID parse, spctl-verdict parse, mount-point
parse, bundle-root/app-in-volume selection); the live exec of them wants one
manual pass against an actual signed `AUK-x.y.z.dmg` before the first paid
release leans on it. The guided fallback is always available as the safe floor.

---

## 4. Dev builds never nag

The current running version is read at runtime from the bundle, not compiled in
(`current.go`), so it can't drift from what `release.sh` actually stamped:

Resolution order — (1) `Contents/Info.plist`'s `CFBundleShortVersionString`,
located by walking up from `os.Executable()` to the `.app`; (2) an `-ldflags`
`buildVersion` override; (3) the `AUK_VERSION` env var; (4) `""` = dev/unknown.

A real release has step 1 stamped (`release.sh` → `wails.json`
`info.productVersion` → the plist template). Two dev cases, both handled so
nothing is ever falsely nagged:

- **`wails dev`**: Wails defaults an unset `productVersion` to **`1.0.0`**
  (`internal/project` in the Wails module), so the bundle reports `1.0.0`.
  `1.0.0` outranks every real `0.x` release, so `IsNewer` returns false —
  no update offered.
- **An unversioned / `0.0.0-dev` build** (`release.sh`'s no-tag fallback):
  `IsDevVersion` treats an empty, unparseable, or `0.0.0` version as dev, and
  `CheckForUpdate` returns `available:false, isDevBuild:true`.

The frontend trusts the backend's `available` / `isDevBuild` flags rather than
sniffing `import.meta.env`, so the "never nag in dev" rule has a single source
of truth. Version comparison is full SemVer precedence (`version.go`):
pre-releases sort *before* their release (`0.4.0-dev.3 < 0.4.0`), so a local
dev build that derives from an upcoming release is never told an older release
is "newer" than it. Build metadata is ignored.

> Note on the task's original assumption: it guessed a `wails dev` bundle would
> report a version *older* than the latest release (so a check would show an
> update). In fact Wails stamps `1.0.0`, which is *newer* than `0.3.0` — so a
> dev check reports **no** update. Either way the safe outcome holds: dev never
> nags.

---

## 5. The "check on launch" preference

Auto-check-on-launch is **opt-out**, default on (a paid app keeps its buyers
reachable unless they deliberately turn it off). It is persisted **without
touching the shared settings schema** (`settings.go` / `store.ts` /
`settings.yaml` are owned elsewhere): the updater keeps its own tiny JSON file
next to the other AUK app-support state,

```
~/Library/Application Support/AUK/updater.json   → {"autoCheck": true}
```

read/written by `GetUpdatePref` / `SetUpdatePref` (`pref.go`). A missing,
unreadable, or malformed file means the default (on). This keeps the preference
authoritative in one place that both the Go binding and — through the binding —
the frontend read, with zero coupling to the settings owner's files.

The launch check is fire-and-forget: `initUpdateCheck()` reads the pref and, if
on, runs a silent check that never blocks launch and never surfaces an error.

---

## 6. Future: a signed appcast

The one property GitHub Releases doesn't give us is a *tamper-evident answer to
"what's the latest"* — the feed and the download share a host and a trust
boundary. The Team-ID/notarization check in §2 already means a compromised feed
can't get malicious code *installed* (a swapped DMG fails verification), but it
could still lie about a version or point at a valid-but-wrong asset.

The migration path, already accommodated by the `Feed` abstraction:

1. Host a small `appcast.json` (version, notes, DMG URL, size, SHA-256) on our
   own domain.
2. Sign it with an offline Ed25519 key; ship the public key in the app.
3. Add a `SignedAppcastFeed` implementing `Feed` that verifies the signature
   before returning a `Release`.

No change to version compare, download, verification, bindings, or UI — only
which `Feed` `DefaultService` constructs. Until then, GitHub Releases + the
Developer-ID/notarization gate is the v1 trust model, and it is a strong one:
the worst a hostile feed can do is deny or misdirect, never install.

---

## File map

| Concern | File |
|---|---|
| SemVer parse/compare, dev detection | `internal/updater/version.go` |
| Current version from the bundle plist | `internal/updater/current.go` |
| Feed interface + GitHub impl + notes/SHA parsing | `internal/updater/feed.go` |
| Download, verify (checksum + codesign/Team-ID/notarize), mount, stage, swap helper | `internal/updater/install.go` |
| Orchestration (Check / DownloadAndStage / Install) | `internal/updater/updater.go` |
| Opt-out preference (own file) | `internal/updater/pref.go` |
| Wails bindings | `app_update.go` |
| Typed wrappers + reactive state | `frontend/src/lib/updater.ts` |
| Banner + Settings row | `frontend/src/components/UpdateBanner.tsx` |
| Tests (30, network-free) | `internal/updater/*_test.go` + `testdata/` |
