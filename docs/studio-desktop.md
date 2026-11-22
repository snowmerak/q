# Q Studio desktop

Q Studio adds a native window, tray, background execution and user-login startup
around the existing Studio engine. The app and browser share the Svelte build
and HTTP API. The shell's separate [Go module](../desktop/go.mod) pins Wails
v3.1.0; ordinary Go installation remains independent of desktop dependencies.

## Installation and controls

Windows packaging produces `Q-Studio-<version>-windows-<arch>-setup.exe`.
It installs for the current user under `%LOCALAPPDATA%\Programs\Q Studio`,
creates shortcuts, and checks per-user/per-machine WebView2 registration in both
registry views. An existing runtime skips Microsoft's bootstrapper. When the
runtime is missing, setup runs the bootstrapper and checks availability again;
an already-installed result does not block installation. Network access is
needed when installing a missing runtime. `/S` enables silent installation;
`/D=<directory>` chooses another location and must be the final argument.
Local builds are unsigned.

The portable ZIP contains both `Q Studio.exe` and `q-engine.exe`. Extract them together
to a stable directory. WebView2 must be available. The app resolves the bundled
engine beside itself, independently of `PATH`.

All three platforms support amd64 (Intel/AMD x64) and arm64:

| Platform | Installation artifact | Install |
| --- | --- | --- |
| Windows | `Q-Studio-<version>-windows-<arch>-setup.exe` | Run the installer |
| macOS | `Q-Studio-<version>-darwin-<arch>.dmg` | Open the image and drag **Q Studio** into **Applications** |
| Debian/Ubuntu | `Q-Studio-<version>-linux-<arch>.deb` | `sudo apt install ./Q-Studio-<version>-linux-<arch>.deb` |
| Fedora/RPM | `Q-Studio-<version>-linux-<arch>.rpm` | `sudo dnf install ./Q-Studio-<version>-linux-<arch>.rpm` |

The macOS image contains `Q Studio.app` with its engine inside the bundle.
Move it to its final location before enabling login startup. Signed, notarized
macOS distribution requires the distributor's Apple credentials. Published
macOS ZIPs from the signing-enabled workflow contain a signed, notarized
`Q Studio.app`, including its engine and an attached notarization ticket.
Local packages and manual runs with publication off contain an unsigned
`Q Studio.app` in the ZIP. A build without packaging produces the raw executable
pair instead.
Linux packages install the paired binaries in `/usr/lib/q-studio`,
an application launcher, an icon and the `/usr/bin/q-studio` symlink. Native
Wails v3.1 requires GTK4 and WebKitGTK 6.0; package managers resolve these
dependencies. Linux distributions without those libraries are not supported by
these packages. Installing or uninstalling the app preserves the existing `q`
CLI and user data. Plain Unix ZIPs require executable permissions and the same
runtime libraries; use the platform installation package when available.

Use the window's **Q Studio** menu or tray menu:

| Action or setting | Behavior |
| --- | --- |
| Open Q Studio | Show and focus the existing app window |
| Open in browser | Open the same engine in the system browser |
| Keep running when window closes | Hide the window while retaining the engine; initially on |
| Start at login | Register/remove the user's login item; initially off |
| Start hidden at login | Start without creating a WebView window; initially on |
| Quit Q Studio | Stop the app and its owned engine; leave a CLI-owned engine running |

Closing the window is different from **Quit Q Studio**. A manual second launch
shows the existing window; an automatic launch does not bring it to the front.
Linux without a StatusNotifier watcher disables hidden startup and close-to-tray
so the window remains accessible. Restart to detect newly available tray support.

## Build from source

From the repository root:

```powershell
task desktop:build
task desktop:package
```

Equivalent commands with an explicit version or NSIS path:

```powershell
go run ./scripts/desktopbuild -version 0.2.2
go run ./scripts/desktopbuild -installer -nsis C:\path\to\makensis.exe
```

The builder uses the host OS/architecture and `GOWORK=off`, putting paired
executables in `bin/studio/<os>-<arch>/` and a versioned ZIP in `bin/`.
The engine already embeds the frontend; rebuilding it uses the existing
`task studio:install`, `task studio:build`, and `task studio:check` commands.

The wrapper compiles the independent engine and desktop modules, then passes
the prebuilt pair to Wails v3.1.0 with `build --skip-build`. It installs a pinned
CLI in `bin/tools` if needed. Wails owns package assembly, platform metadata,
icons and companion placement. Windows lifecycle/WebView2 checks and Linux
installation paths are maintained templates under `desktop/packaging`.

After changing `desktop/assets/icon.png`, run `go -C desktop generate .` from
the repository root to refresh the Windows executable icons and browser
favicon, then run `task studio:build` to update the embedded frontend.

macOS staging prepares a 1024-pixel copy of the existing logo so Wails emits all
ICNS representations, including 512- and 1024-pixel sizes. The custom macOS
Info.plist explicitly selects `Contents/Resources/icons.icns` and omits
`CFBundleIconName`, preventing the bundled default asset catalog from taking
precedence. These inputs apply to unsigned local packages too, before signing
or notarization can seal the bundle.

This uses Wails' **experimental YAML packaging pipeline**, explicitly enabled
only for the packaging child process with `WAILS_EXP_USE_WAKE=1`. The GUI backend
uses Wails' native defaults. Windows needs NSIS (`makensis` on PATH or `-nsis`).
Windows packaging downloads Microsoft's evergreen WebView2 bootstrapper over
HTTPS and bundles it. Wails creates DMG/DEB/RPM files itself. People installing
the app do not need Go, Node.js, Wails or these build tools.

Run native builds on the target OS and architecture. Linux build hosts need
`build-essential pkg-config libgtk-4-dev libwebkitgtk-6.0-dev` on Ubuntu 24.04;
macOS needs Xcode command line tools. Targets are explicit and can be combined:

```powershell
go run ./scripts/desktopbuild -targets windows/amd64,windows/arm64 -installer
go run ./scripts/desktopbuild -targets linux/amd64 -installer -formats deb,rpm
go run ./scripts/desktopbuild -targets darwin/arm64 -installer
```

`-package-only` repackages an existing `bin/studio/<os>-<arch>` pair without
recompiling. `-purego` explicitly opts into Wails' experimental purego GUI
backend, enabling macOS/Linux cross-builds; it is not used by default or by CI.
Cross-built packages still need testing on the target system. DMG assembly
requires a macOS or Linux host: Wails v3.1's Windows DMG writer loses executable
permissions, so the wrapper rejects that combination. Linux-based packaging
of a cross-built macOS pair preserves those permissions.

## GitHub Actions and releases

The [Q Studio desktop release workflow](../.github/workflows/studio-desktop.yml)
requires Windows and Ubuntu builds on amd64 and arm64. macOS on both
architectures is optional: publishing runs skip Macs without Apple signing
credentials, and Mac build/signing failures do not block Windows/Linux releases.
Development-only manual runs still attempt all six targets without credentials.
Each runner builds the frontend from its lockfile before compiling and packaging
the app. Windows uses a checksum-verified portable NSIS compiler, including
on ARM64. macOS ARM64 uses an Intel macOS runner and Apple's ARM64 C/Objective-C
compiler with the native Wails backend, avoiding hosted ARM64 capacity queues.
That target checks Mach-O architecture and runs cross-target vet; its engine
cannot be executed on the Intel runner. The other five targets execute their
assembled-engine tests on matching hardware.

After committing the desktop implementation and workflow, push a numeric
`v<major>.<minor>.<patch>` tag, for example:

```sh
git tag -a v0.2.2 -m 'Q Studio 0.2.2'
git push origin v0.2.2
```

All four Windows/Linux builds must produce their six installation files
(two EXEs, two DEBs and two RPMs) and four portable ZIPs. Successful signed
Mac targets add a DMG and app ZIP each; missing/incomplete Mac pairs are omitted.
The release job hashes only its verified artifact list and uploads that list
plus a SHA256 manifest. It creates the tag's GitHub Release with
generated notes, or attaches the files to an existing release while preserving
its notes and publication state. A rerun replaces assets with the same names.
If an existing release has assets missing from a rerun, publication stops to
avoid leaving those assets outside the new manifest; rerun with complete inputs.
The build source is pinned to the tag's commit; moving the tag during a run
causes release publication to fail. The workflow never creates Git tags.

For manual runs in **Actions → Q Studio desktop release → Run workflow**:

- Set `version` to a numeric `major.minor.patch` value.
- Leave `publish_release` off to build the selected workflow ref and retain only
  Actions artifacts.
- Turn `publish_release` on to build the existing `v<version>` tag and attach the
  packages to its release. That tag must contain the desktop build sources.

Only the release job receives `contents: write`; build jobs use read access.
The built-in `GITHUB_TOKEN` is sufficient for GitHub; no personal access token
is needed. macOS publication additionally requires the Apple credentials below.
Release helpers come from the workflow commit, while application sources stay
pinned to the selected tag. This also supports recovery of older source tags.

The [infrastructure retry workflow](../.github/workflows/studio-desktop-retry.yml)
watches completed release builds from the default branch. It retries recognized
runner-allocation, runner-disconnection and transient setup/download/upload
errors after 1, 3 and 10 minutes, with at most three additional attempts.
Successful jobs and their artifacts are retained; GitHub reruns unsuccessful
jobs and their dependents, including release publication. Build/test failures,
unknown errors and manual cancellations stop automatic retries. Setup network
errors must have a recognizable error in their check annotations; ordinary
log messages alone do not trigger retries. Existing manual reruns count toward
the same attempt limit. A controller waits up to 110 minutes for each rerun and
stops if its API request fails, without blindly replaying an ambiguous request.
Its Actions summary records the retry or stop reason.

This workflow needs to be present on the default branch. Its controller uses
`actions: write` and `checks: read`, runs only default-branch code, and accepts
only this repository's release workflow push/dispatch events. It never checks
out the failed run's source. No additional token or paid retry service is used.
Run `task ci:check` to test the policy locally without dispatching Actions.

### macOS signing and notarization

Configure these repository **Settings → Secrets and variables → Actions** secrets:

| Secret | Value |
| --- | --- |
| `APPLE_DEVELOPER_ID_CERTIFICATE_BASE64` | Base64 of a password-protected `.p12` export containing the **Developer ID Application** certificate and private key |
| `APPLE_DEVELOPER_ID_CERTIFICATE_PASSWORD` | Password for that `.p12` export |
| `APPLE_DEVELOPER_TEAM_ID` | The certificate's 10-character Apple developer team ID |
| `APPLE_NOTARY_KEY_BASE64` | Base64 of the App Store Connect team API key's `.p8` private key |
| `APPLE_NOTARY_KEY_ID` | The API key's 10-character key ID |
| `APPLE_NOTARY_ISSUER_ID` | The team API key's issuer UUID |

On a Mac, `base64 -i DeveloperID.p12 | pbcopy` and
`base64 -i AuthKey_KEYID.p8 | pbcopy` produce the corresponding secret values.
Use a team API key with access to Apple's notary service. An Apple Development
or Developer ID Installer certificate does not satisfy the app-signing check.

For publishing runs, both Mac targets check required credential configuration
before building. Native certificate validation happens during signing.
Missing credentials skip Mac targets. Invalid credentials, rejected notarization
or failed signature/ticket/Gatekeeper verification fail the affected Mac job and
omit its artifacts, while Windows/Linux remain required. Published Mac artifacts
never fall back to unsigned packages. Mac jobs allow 90 minutes;
the app and DMG notarization submissions each wait at most 15 minutes. The retry
controller allows 110 minutes per rerun within its six-hour overall limit.

The signing helper extracts the Wails-produced app into a private workspace,
signs its engine before sealing the app with hardened runtime and timestamps,
notarizes an app archive, and staples the app's ticket. It then creates and signs
the final DMG, notarizes it, and staples that ticket too. Both final containers
are checked: the DMG is mounted again and the published ZIP is extracted again,
with app signatures, tickets and Gatekeeper assessments checked before upload.
The artifact names and release SHA256 manifest remain unchanged.

Only the credential-check and signing steps receive Apple secrets. Signing uses
an isolated temporary keychain and restores the original keychain search list;
private key files and the keychain are cleaned on success or failure. Published
artifacts include neither those credentials nor the signing workspace.

`task ci:check` exercises credential rejection, signing/notarization failures,
artifact publication gating and cleanup using a native-command adapter. These
local tests do not establish real Apple signing or notarization. That requires
a macOS runner and the configured credentials. Existing v0.0.2/v0.0.3 releases
remain unsigned; this pipeline applies when the updated workflow is run.

References: [Apple distribution packaging](https://developer.apple.com/documentation/xcode/packaging-mac-software-for-distribution),
[Apple notarization workflow](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow),
[GitHub runner certificates](https://docs.github.com/en/actions/how-tos/deploy/deploy-to-third-party-platforms/sign-xcode-applications).

## Upgrades and removal

Windows upgrades/removal request normal quit and wait up to 20 seconds before
replacing files. Removal unregisters login startup and preserves `~/.q` and all
workspace data. Existing CLI installations in `GOBIN` are not overwritten.

## Ownership, storage and access

The app invokes its bundled `q-engine __studio_desktop`. A versioned JSON handshake uses inherited
stdin/stdout; diagnostics are separate. The parent keeps stdin open. Closing it
requests shutdown of an engine created by this invocation. On Windows, a
kill-on-close Job Object also contains the relay and its worker descendants.

Each user configuration directory has one Studio OS file lock. CLI and app
attachments authenticate the descriptor and verify protocol compatibility.
Explicit `--host`/`--port` requests must match the current owner. A CLI-owned
engine survives app quit. If it exits, the app can acquire ownership and reconnect.
Health failure reconnects with backoff; accepted agent turns are never silently
replayed. A stale lock file does not retain ownership.

| Location | Purpose |
| --- | --- |
| Existing `~/.q` and workspace stores | Shared application settings, providers and work |
| `~/.q/desktop/settings.json` | Window-close and hidden-login preferences |
| `~/.q/studio/ui-state.json` | Shared theme, text size, sidebar, last workspace and unsent session drafts |
| OS login registration | Actual enabled/disabled login startup state |
| `~/.q/desktop/desktop.log` | Desktop startup/engine diagnostics; one previous log retained after a 5 MiB startup rotation |
| `~/.q/logs/studio/` | Existing rotated structured Studio logs |
| `~/.q/studio/runtime.lock` | Live OS ownership lock |
| `~/.q/studio/runtime.json` | Private discovery credential and endpoint; removed at normal shutdown |

The runtime directory uses Unix permissions or a protected current-user/SYSTEM
Windows DACL. Do not share its descriptor in bug reports. Desktop-owned engines
bind to loopback and require per-process credentials. The shell's proxy supplies
them without exposing them to Svelte/preview JavaScript. Browser launch exchanges
a one-use link for an HttpOnly SameSite session; mutations check the origin.
Standalone web CLI access retains its existing authentication behavior. No Go
services are bound to the desktop frontend.

Display and navigation preferences and session drafts belong to the Studio
profile. The frontend loads them through `/api/v1/ui-state` before mounting,
so a new HTTP port or a different app/browser origin restores the same state.
Updates merge individual keys and atomically replace the private profile file;
failed saves show an alert and retry without replacing newer edits.
Browser localStorage is a fallback cache and a source for one-time migration.
Existing profile values and cleared drafts take precedence over legacy caches.
Browsers cannot read storage belonging to a different old origin: to migrate
those legacy values, open that original address with the updated server first.
Already-open windows load other windows' changes when refreshed.

Login startup uses Windows HKCU Run, a macOS LaunchAgent preserving
`--autostart`, or Linux XDG autostart. Starting the app does not overwrite an
OS-level disable. Before moving a portable installation, disable login startup;
enable it again from the final location.

Login apps may inherit a different environment from interactive terminals.
Configure usable Git, ACP, MCP and browser executable paths and ensure provider
environment variables are available to the login session.

## Background execution

The engine's existing [ticket schedules](ticket-schedules.md) continue with the
window hidden. Login startup turns on that engine; it does not add a scheduler.
Question/permission waits, capacity limits, missed-interval coalescing and
interrupted-run recovery retain their existing behavior. Quit, logout and OS
shutdown end the app lifetime. This is not a pre-login service or an execution
guarantee during sleep/offline periods.

## Verification

On Linux, an exit without a window or terminal output can be diagnosed in
`~/.q/desktop/desktop.log`. The v0.0.2/v0.0.3 shell used a home-directory hash as
the final D-Bus single-instance name component. A hash starting with a digit
made startup fail before a window was created. The corrected source prefixes
only those invalid Linux components with a letter, preserving working Linux
identities and all Windows/macOS identities.

```powershell
go test ./internal/studioprocess ./cmd/q
task desktop:check
```

Test the assembled engine protocol and real subprocess cleanup:

```powershell
$env:GOWORK = 'off'
$env:Q_STUDIO_TEST_ENGINE = (Resolve-Path './bin/studio/windows-arm64/q-engine.exe').Path
go -C desktop test ./...
```

The integration test isolates its home and Library/Memory ports. Startup tests
use private temporary registry keys and do not change the real login item.
Coverage includes browser admission/replay, cross-origin mutation rejection,
engine control, ownership, proxy credentials, settings and OS-disabled startup.
The engine test also calls Overview's registered-sessions API, which initializes
Library and the bundled browser skill. Checking only Studio's status endpoint
does not detect a malformed embedded skill. Embedded skill resources use LF in
Git, and validation, hashing, upload and installation canonicalize CRLF to LF
so Windows and Unix builds produce the same skill identity. The bundle version
advances for this correction to supersede earlier noncanonical installations.

Windows installer regression tests compile and execute the actual WebView2
admission routine in silent NSIS harnesses:

```powershell
$env:Q_STUDIO_NSIS = 'C:\path\to\makensis.exe'
go test ./scripts/desktopbuild -run TestWebViewInstallerAdmission -v
```

They verify that an existing machine/user runtime skips setup, a zero version
requires setup, and post-setup availability controls admission even when the
bootstrapper returns an already-installed code. An absent runtime is rejected
even with a zero bootstrapper result. Fixtures use private test registry keys
and temporary files without installing or modifying the real WebView2 runtime.

The implementation was built and exercised on Windows ARM64: real WebView
rendering, close-to-background survival, re-opening, subprocess protocol and
normal quit. Wails installers were generated for all six OS/architecture pairs:
two Windows EXEs, two macOS DMGs and four Linux DEB/RPM files. Package contents
and archive integrity were inspected, including Unix executable permissions.
Local macOS/Linux binaries were cross-built using the experimental purego
backend, with macOS images assembled by Wails under Linux. The real Linux
ARM64 engine integration tests also passed under WSL Debian. Native Linux ARM64
builds and actual window rendering were subsequently verified with Go 1.27.3,
GTK 4.18.6 and WebKitGTK 2.54.0 on Debian 13/WSLg. Both released ARM64 DEBs were
extracted and executed: valid profile IDs opened Overview, while digit-leading
IDs exited silently. The corrected native build opened the same failing
profile, reached Ready, reused its window on a second launch and shut down
normally through `--quit`. macOS UI execution has not been verified.
Native GitHub Actions builds
and assembled-engine tests passed for all six OS/architecture pairs in the
v0.0.1 release.
Native macOS behavior, Linux desktop environments outside WSLg (including
NVIDIA GB10), clean-account Windows installation/removal and an actual
logout/login cycle still need platform testing. Apple signing credentials must
be supplied separately; auto-updates are not included.
