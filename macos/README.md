# wassup for macOS

Native SwiftUI setup and menu bar approvals for the Go CLI. Minimum macOS 14. Swift 6 language mode, no package dependencies, no Xcode project, no direct WhatsApp networking.

## Build and sign

From the repository root:

```sh
./macos/build.sh
```

This builds arm64 and Intel versions of both executables, assembles `macos/build/wassup.app`, signs the helper first and then the app, verifies the signatures, assesses Gatekeeper, and makes a signed UDZO DMG with an Applications shortcut.

The default signing identity is `Developer ID Application: tanishq aggarwal (422C63R2G8)`. Override with `SIGN_ID`. `VERSION` defaults to `git describe --tags --always`, then `0.1.0`. `BUILD_NUMBER` defaults to `1`. Non-numeric git versions are recorded in `WassupCLIVersion`; the public app version falls back to `0.1.0`.

```sh
VERSION=0.1.0 SIGN_ID=- TIMESTAMP=0 ./macos/build.sh
```

`TIMESTAMP=0` disables the signing timestamp. Ad-hoc signing also skips it. A Developer ID signature without notarization is expected to be rejected by Gatekeeper as an unnotarized app.

## Notarization

No upload happens unless `NOTARY_PROFILE` is set. Configure your own notarytool keychain profile first, then:

```sh
NOTARY_PROFILE=wassup-notary ./macos/build.sh
```

The script signs and submits the zipped app, staples it, creates a fresh DMG from the stapled app, signs and submits that DMG, then staples it. The current delivery was built without notarization or uploads.

## Snapshots and tests

```sh
cd macos
swift test
./build/wassup.app/Contents/MacOS/wassup --snapshot "$PWD/build/snapshots"
```

Snapshot mode renders all ten requested states at 2x using `ImageRenderer`, then exits. It never installs the helper, starts a service, reads the account, requests notifications, connects an AI app, or runs a CLI command. The toggle, scroll container, selectable text and app icons have drawable substitutes. Panels and buttons use the same flat gray fills in the live app and snapshots. The QR contains a fake, non-linking string. Animated elements render in their final state.

Tests cover structured CLI envelopes, stream framing, stderr draining, cancellation, timeout, service-status shapes, quoting, dates, stable installation, no-op installation, and replacement with a fixture service restart. Installer fixtures stay under `macos/.build/verification/` and do not use the real CLI.

For an isolated launch smoke test, use `WASSUP_HOME=/tmp/wassup-app-test`, run the app's executable directly, wait about eight seconds, then stop that app process and only that temporary home's background process. Do not click through setup. Changing WASSUP_HOME does not isolate the Go CLI's launchd job label or AI app configuration paths. Do not test service installation or connections against a real user account.

## Runtime layout

```text
macos/
  Package.swift
  build.sh
  wassup.entitlements
  Sources/WassupApp/
    WassupApp.swift          App lifecycle and setup window
    CLI.swift                JSON values, one-shot calls, streams, environment
    BinaryInstaller.swift    Version comparison and atomic stable helper install
    AppModel.swift           Setup, account, approvals, event and polling tasks
    Notifications.swift      Notification categories and shared approval actions
    Logo.swift               Native bubble, animated cutout and success mark
    Styles.swift             Colors, materials, buttons, motion and copy feedback
    SnapshotRenderer.swift   Isolated mock visual QA
    Setup/                   Welcome, linking, service, apps, safety and done
    MenuBar/                 Status, approval cards and footer
  Tests/WassupAppTests/
  build/
    wassup.app/
      Contents/MacOS/wassup
      Contents/Helpers/wassup
      Contents/Resources/    App icon and 1x/2x menu bar template images
    wassup-VERSION.dmg
    snapshots/
```

At launch the app reads the bundled CLI's version from `WassupCLIVersion` in Info.plist and installs it atomically at `$WASSUP_HOME/bin/wassup`, or `~/.wassup/bin/wassup`. All CLI invocations, including version queries, use the stable path. The bundled helper is never executed in place. Notification actions received during startup wait for the stable helper and are checked against the originating home. A changed installed version restarts an already-running background process. Login is cancelled when the user goes back or closes setup. Quitting the app terminates its event listener and child calls, but leaves the CLI's background process running.

The CLI receives the original environment, including an explicit `WASSUP_HOME`. Its PATH starts with the stable bin directory, adds the login-shell PATH once with a three-second timeout, then the documented Homebrew, local and system paths. Terminal setup receives that same PATH and home override. The app's completed-setup preference is separate for explicit custom homes.

## What needs a linked-account check

Real QR and phone linking, service installation, app configuration, sending/rejecting real drafts, notification delivery/actions, Terminal automation and the privileged symlink flow require live account or macOS permission testing. They are implemented but were not exercised on the user's linked account. Snapshots verify rendering, not animation timing or interactive behavior.

Known CLI limitations to improve without changing the app's transport:

- `connect --json` should report `connected` for every target. The app accepts it if present and tracks successful connections in its current session. Existing configuration cannot currently be verified by this command.
- The existing Codex connector can report success for a pre-existing config entry without verifying or replacing its executable path. A stable-path migration needs CLI support.
- launchd job names are account-based and do not include `WASSUP_HOME`. A separate home alone must not be used to test install/uninstall on a Mac with a real service.
- A service helper installing a launch agent for the menu app itself would allow notifications immediately after login before the user opens wassup. The requested toggle installs the CLI service only.
