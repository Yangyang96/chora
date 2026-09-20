# macOS application

**English** | [简体中文](macos-application.zh-CN.md)

The Apple Silicon application implementation packages Chora, its browser UI and
pinned Node.js/Pi dependencies. It targets macOS 13 or later. A public signed,
notarized installer is not yet available; source setup in the [README](../README.md)
remains the public Alpha entry. An ad-hoc development build does not establish
Gatekeeper, notarization or distribution acceptance.

## First launch and authentication

For a qualified distribution, drag `Chora.app` into Applications and open it.
The menu bar application prepares its managed runtime, offers fresh data or
migration, starts a loopback Workbench and opens the default browser. Go, Node.js,
npm and a Chora checkout are not needed on the installed machine. Task repositories
still need Git and any tools required by their own checks or applications.
Docker is optional and used only for explicitly prepared isolated execution.
Local execution retains its No Sandbox boundary.

Choose **Set Up Model Authentication…** in the Chora menu. The native dialog uses
Pi's own provider authentication methods, including API keys and browser/device
flows where the provider offers them. Pi owns credential storage and refresh;
Chora does not copy credentials into its database or operation logs. Existing Pi
configuration is reused. Return to Workbench and refresh Runtime readiness before
starting a task. Provider authentication and network availability still apply.
The [first-task guide](../README.md#your-first-task) describes the browser workflow.

The app stores its data, immutable managed runtimes and operation logs under
`~/Library/Application Support/Chora`. Runtime versions come from the application
bundle and are integrity-checked; Chora does not silently update Node.js or Pi.
Pi retains its native configuration location. A maintainer can set
`CHORA_DESKTOP_SUPPORT_DIR` to an absolute disposable directory when testing;
`PI_CODING_AGENT_DIR` independently selects a disposable Pi configuration.

## Lifetime and recovery

Closing the browser leaves Chora running. **Open Workbench** reopens it. **Quit
Chora…** asks before stopping tasks and previews, waits for cleanup and preserves
task state. An uncertain cleanup reports an error and retains recovery state;
it is not reported as a clean stop. Relaunch to reconcile state and inspect logs.
Sleep or logout does not guarantee continued execution or automatic task restart.

**Back Up Data…** and **Restore Backup…** require idle work, stop the service and
operate on the complete Chora data root. A restore preserves the prior data in a
safety backup. Backups contain sensitive history and must be stored privately.
They preserve Task worktrees inside the data root, but do not roll back original
repositories, external worktrees or Pi credentials. Restore to the same data
location so recorded absolute paths remain valid. See also
[Workbench maintenance](workbench-maintenance.md).

On first launch, **Migrate Existing Data…** checks the migration ledger and data
integrity, rejects unsupported/newer schemas, requires the old service to stop,
and makes a backup before adopting the existing location. It deliberately keeps
absolute worktree references in place. Failed inspection or backup leaves the
original installation available. Do not run the source and application services
against the same data directory simultaneously.

## Updates and uninstall

**Check for Updates…** looks for published macOS assets only when requested.
Download a candidate, then use **Install Downloaded Update…**. Installation requires
idle work, matching application and signing-team identities, a newer version,
valid signatures, a stapled notarization ticket and Gatekeeper acceptance. The
copied candidate is checked again before replacement. Chora backs up data and
retains the previous application. Reopen from Applications after installation.
If startup fails, restore the previous application and the matching data backup;
a database written by a newer version cannot be assumed readable by an older one.

Quit Chora and move the application to Trash to uninstall. Data, Pi configuration,
managed runtimes and Task worktrees remain available for reinstallation. There is
no automatic destructive data-removal step.

## Building a local candidate

Build on Apple Silicon macOS with Xcode command-line tools, Python 3.12 or later,
and the [development toolchain](../CONTRIBUTING.md#development-and-verification).
Install source dependencies with `npm ci` and `go mod download all`, then use a
fresh output directory outside the checkout:

```sh
python3 tools/macos-package/build.py \
  --output /private/tmp/chora-macos-candidate \
  --version 0.1.0-alpha.2 --build-number 2 --dmg
```

The builder pins archive checksums and the Pi dependency lockfile, bundles the
optional Linux arm64 isolation helper, records source identity and an npm SBOM,
and verifies the resulting code signature. Default signing is ad-hoc and suitable
only for local development. Build outputs must stay outside the public repository.

`--identity 'Developer ID Application: …'` requires a clean committed checkout.
It enables hardened-runtime distribution signing but does **not** notarize or
publish the result. A formal candidate still requires notarization and stapling,
Gatekeeper testing on a clean machine, first-task and signed-update/recovery
acceptance, and matching published source and dependency notices. Developer ID
credentials and package publication are separate prerequisites. Do not bypass
Gatekeeper or describe an ad-hoc build as a supported public installer.
