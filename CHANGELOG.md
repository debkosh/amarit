# Changelog

## v0.10.3

- **A Client with no version is a development build.** It keeps the
  settings, so a settings page needs no made-up version or URL; `Check` and
  `Download` return `ErrDevelopment`, and appupdate runs no rounds and fails
  a check with kind `development`.
- **Android's `UpdateNotification` is one plain Java file** with its words in
  `res/values/amarit_notification.xml`, so a build without Gradle, such as
  Chromium's GN, compiles it as source. Its receiver is now
  `UpdateNotification$Receiver`, and its strings are `amarit_notification_*`.

## v0.10.2

- **`relaunch.WaitExited` on Linux waits on pidfds** (`pidfd_open` and
  `ppoll`, Linux 5.3 and later), told by the kernel instead of asking every
  50 ms, and without mistaking an unreaped child for a running one. An older
  kernel is polled as before. Asked for by Project B's Linux build.

## v0.10.1

- **`amarit wait N`** blocks until the running job N ends and exits with
  its code, so a script can start a job and later wait for it. The job's
  keeper answers the waiters connected when it ends; amarit still keeps
  nothing of a job afterwards.

## v0.10.0

The system's own way of showing an app's updater. Guide: docs/appupdate.md.

- **`macapp.Present(u, words)`**: a progress bar on the app's Dock icon
  while appupdate downloads and checks, and a notification with Relaunch
  when a release is ready. amarit installs a notification delegate only
  when the app has none; an app with one forwards amarit's category to
  `macapp.HandleNotificationResponse`. The app's own categories are kept.
- **Android `UpdateNotification`**: shows appupdate's State, as JSON, as
  the system's notification, with Cancel, Install and Try again sent back
  as commands. Its words are resources an app overrides.
- **`examples/macupdate`**: an app that updates itself from a release it
  serves slowly, to watch both on a Mac.

## v0.9.0

What Project B built around the Client for its Mac app, in amarit, so the
next app does not write it again. Guide: docs/appupdate.md.

- **`amarit/appupdate`**: the updater of an app a person uses. It draws
  nothing: a JSON-ready `State` and its changes go out, `Check`, `Retry`,
  `Cancel`, `Relaunch`, `SetChannel` and `SetAutoUpdate` come in, and the
  guide's table says which commands each phase takes. Failures carry a kind
  (offline, network, stalled, damaged, unchecked, refused, not-writable,
  busy, install), not words. Rounds on a timer, the lock and the staged
  release shared by every copy, a cancelled release left alone until asked,
  a refused one rejected.
- **`amarit/macapp`**: a macOS bundle. `Info`, `Unpack` (ditto, keeping
  the open-file limit for the child), `Verify` with the Security framework
  in process, telling a real refusal from a check that couldn't be made,
  `Stage`/`Staged`, `Swap` and `Rollback` with one `renamex_np(RENAME_SWAP)`.
  `macapp.Installer` puts them together, with `Copies` for an app running
  several copies and `Settle` for the rollback at start. Needs cgo.
- **`amarit/relaunch`**: `After` starts a program once this process has
  gone; `WaitExited` waits for other processes to end.
- **Client**: `Download` takes `OnProgress` and `StallAfter`; errors
  `ErrDamaged`, `ErrStalled`, `ErrRefused`, `ErrNotWritable`; `TryLock` and
  `Lock` on the state directory; `Installing`, `Started`, `Healthy` and
  `RolledBack`, the rollback count for a program that installs itself. A
  start fails when its process ends before Healthy, so copies starting
  together are not failures.

## v0.8.2

- **`amarit job [-name N] [-dir D] [-env-file F] [-log F] -- <cmd>`**: a
  command run once, off the shell, never started again by a daemon restart,
  an update or a reboot. Its keeper is not the daemon's, so neither updating
  the daemon nor replacing the amarit binary touches it. `amarit jobs` lists
  the running ones, `amarit logs` and `amarit stop` take a job's name, and
  `-log F` ends the caller's file with `amarit job N: exit 0`. amarit keeps
  nothing of a job once it ends. One-shot work no longer needs a unit with
  `restart: never`, which a daemon restart ran again before v0.8.1.

## v0.8.1

- **A daemon that restarts leaves exited units exited.** A unit its restart
  policy had left exited (`restart: never`, or `on-failure` after a clean
  exit) was started again by the next daemon: the exit was remembered only
  in memory, so an update or a reboot ran every finished one-shot unit once
  more. The new daemon reads the exits from the run state the old one
  wrote, so this release's own update replays nothing. A changed unit file
  still runs the unit again.

## v0.8.0

Release channels, as Project B built them by hand on top of amarit.

- **`amarit next-version [-pre dev] 0.3.8`** prints `0.3.9-dev.<UTC minute>`:
  the name of a rolling build, which semver orders after 0.3.8, after the
  rolling builds before it, and before 0.3.9. `0.3.8+g<sha>` could not do
  that on the desktop, where build metadata does not order.
- **`amarit version-code 0.3.9-dev.202611201415`** prints `3009`, an APK's
  `versionCode` from its version without the pre-release part, so the
  rolling builds of 0.3.9 and the release 0.3.9 share one code.
- **Android: an equal `versionCode` with an older name is not an update.**
  A phone on 0.3.9 that picks the dev channel is not taken back to a dev
  build before it. Long numeric pre-release parts, such as the minute, now
  order correctly.
- **`Client.Channel`, `SetChannel`, `AutoUpdate`, `SetAutoUpdate`.** The
  person's choices live in `settings.json` in the client's state directory,
  shared by every copy using it; `Check` reads the channel each time.
- **`amarit publish -github owner/repo -tag channels ...`**: `amarit release`
  against a GitHub release with a fixed tag. It creates the pre-release if
  missing, merges into its manifest (else the latest release's), uploads the
  version's assets on the tag, the files named and then the manifest, and
  deletes the files of the release `-prune` dropped.
- **`-prune` drops only the release the run's channel leaves**, unless
  another channel still points at it. It used to drop every release no
  channel pointed at, old stable releases included.
- **docs: moving the manifest's address.** Publish to both addresses until
  the copies on the old one have updated.
- **npm package.** `npm install -g amarit` or `npx amarit` downloads the
  pinned release to `~/.local/bin`, checked by sha256, published from
  GitHub Actions on each release.

## v0.7.1

- **`Client.Unreject()`** forgets the version a `Reject` took out, so the next
  `Check` offers it again. For a check a person asked for: automatic checks
  keep holding a refused release.

## v0.7.0

- **`amarit.Client`: check and download for programs that install
  themselves.** A macOS app bundle is several binaries, a library and
  resources, not one file to rename, so the in-place swap does not apply.
  `NewClient(stateDir, channel, options...)` gives such a program the same
  manifest check and the same size-and-digest-verified download, and leaves
  the install and the restart to it. `Reject(version)` records a version the
  program's own checks refused, so it is not offered again until a newer
  release. The first consumer is the Project B browser.

## v0.6.0

An Android app is the sixth consumer: the same manifest, read from Kotlin.

- **`android/`: the Android library.** `Amarit.start(this)` reads the
  project's `amarit.json` from the APK's assets, checks the manifest on a
  schedule (JobScheduler) or on demand, compares against the installed
  `versionCode`, downloads and verifies the APK, and hands it to
  PackageInstaller; the user taps Install. Platform APIs only, minSdk 26.
  A notification on its own channel and an in-app dialog are the default
  UI; `Updater` is the API for an app that wants its own. Served by JitPack
  as `com.github.sudiptadeb:amarit`, the AAR attached to releases.
- **Spec: Android versions.** A higher `version_code` is an update, a lower
  one never, an equal one with a different version string is a rebuild,
  which carries a rolling channel on one code. `version_code` is required
  on the `android` target.
- **`amarit release` for an APK.** The `android` target, `-version-code N`,
  per-run `-asset` and `-url` patterns, and `-prune` to forget releases no
  channel points at.
- The Go and Kotlin tests parse one fixture, `testdata/manifest.json`.

## v0.5.3

- **A unit file named like an instance is ignored, loudly.** `amarit run`
  never accepted a name with `@`, `/`, `\` or a space, but a hand-written
  `web@3.json` in the units directory was loaded and then read everywhere
  as instance 3 of `web`, sharing its status, log and stop marker. The
  daemon now skips such a file and logs a `WARNING: IGNORING unit file`
  line naming it, once per process.
- README: who runs as whom on each platform.

## v0.5.2

A `go install` build configures itself: one resolution of the
configuration, an embedded `amarit.json`, the module version as the
fallback.

- **`amarit.ProjectFile(data)`.** A program that cannot be built with
  linker flags (`go install`) embeds its `amarit.json` and hands it to
  Start; the same file `amarit stamp` puts in, by another door. A program
  with no file says the same values with the existing options.
- **One resolution.** Every layer meets in one function, later winning:
  the module version Go recorded in the binary, the stamped file, options
  in code in the order given, the environment, the command line.
  `--update-url` and `--update-key` are part of it now rather than read by
  the engine on the side. Nothing is derived from a module path or a
  hosting convention.
- **The version falls back to the module.** With no stamp and no
  `amarit.Version`, the version is `debug.ReadBuildInfo`'s main module
  version, so any `go install` build knows what it runs.
  `amarit.BuildVersion()` returns it for the program's own banner.
- **The daemon declares its own project in code.** `go install
  .../cmd/amarit@v0.5.2` now updates itself like a release build. The
  three facts are repeated from the root `amarit.json` (embed cannot reach
  it from `cmd/amarit`) and a test fails when they drift.
- Versions are compared, never bytes: a `go install` build of the released
  version is current and is not replaced by the release asset; an older one
  takes the next release once.

## v0.5.1

amarit uses amarit: the daemon keeps itself current.

- **A project may make auto-update its default.** `"auto_update": true` in
  `amarit.json` (or `amarit.AutoUpdate(true)` in code) turns the loop on for
  every install of that program; its users opt out with `--no-auto-update`
  or `AMARIT_AUTO_UPDATE=0`. Resolution follows the usual layers, later
  winning: project default or the daemon's `AMARIT_MANAGED=1`, then
  `AMARIT_AUTO_UPDATE=1|0`, then `--auto-update`/`--no-auto-update`. Nothing
  is detected. The loop logs that it is on.
- **The daemon updates itself.** amarit's own `amarit.json` declares
  auto-update, so a release build of `amarit daemon` checks amarit's
  releases hourly and execs the new version in place: same pid, launchd and
  systemd see nothing, the units stay its children and are adopted with
  their pids; three failed starts roll it back. `AMARIT_AUTO_UPDATE=0` in
  the job's environment or `amarit daemon --no-auto-update` turns it off.
  Only the daemon checks; a `go install` build has no stamp and never does.
  The startup line now carries the daemon's version.
- **A rolled-back version is not retried.** The rollback records the
  rejected version in the sidecar and later checks skip it until the channel
  points elsewhere; before, a bad release was downloaded again every
  interval.
- Fixed: after the daemon's in-place restart its units were still its
  children, and the adopted-process watcher polled `kill(pid, 0)`, which a
  zombie answers, so a unit that exited would have looked alive forever.
  The watcher reaps its own children and reports their real exit status.

## v0.5.0

Update a service from the machine it is built on: a local release
directory, one command that brings every unit onto it, rollback when the
restart fails.

- **`amarit update [-check] [-rolling] [unit|glob ...]`.** Runs each
  program's own `<binary> update` once per binary, however many units share
  it, then restarts the units whose binary changed; `-rolling` one instance
  at a time. No names means every unit whose program was built with amarit;
  `-check` only reports. If the restart fails, the previous binary is put
  back, the units are restarted, and the rejected binary is kept as
  `<bin>.amarit/bad`.
- **Local manifests.** `releases` in `amarit.json` (and `--update-url`) may
  be `file://…` or a plain path; an asset's `url` may be relative to the
  manifest, so `releases.json` can sit in a directory beside its assets.
  `release.url` is optional and defaults to the asset beside the manifest.
- **`<program> update --check`** says what is available and applies nothing.
- **One update at a time per binary.** Processes that share a binary (the
  instances of a unit) take turns on a lock in the sidecar; the later ones
  find the version in place and only restart into it. Before, the race could
  overwrite the previous binary kept for rollback.
- Fixed: `amarit stamp` printed the wrong linker variable, so a stamped
  build carried no configuration. Fixed: `-config` defaulted to
  `amarit.config`, and `release -out` to `amarit.json`, which would have
  overwritten the project file; they are `amarit.json` and `releases.json`.

## v0.4.0

The daemon grows up to a real service stack: a web server, identical workers,
a model server that owns a large child process.

- **Process-group stop.** A stop or restart sends SIGTERM to the unit's whole
  process group, waits the unit's `stop_timeout` (default `10s`), then
  SIGKILLs what is left. Children a crashed program leaves behind, and
  processes a new daemon adopted, are stopped the same way.
- **Resource limits.** `"limits": {"nofile": 65536}` raises the open-files
  limit for that program. The daemon starts it through `amarit __exec`, which
  sets the limit and execs the binary, so the pid stays the program's. The
  launchd job `amarit install` writes now raises its own soft limit to 65536.
- **Instances.** `"instances": 8` (or `amarit run -instances 8`) runs eight
  copies named `<name>@1` .. `<name>@8`, each with `AMARIT_INSTANCE=<i>` and
  `{instance}` expanded in args and env values. `stop`, `start`, `restart`
  and `logs` take the unit or one `<name>@<i>`.
- **`amarit restart <unit|glob>... [-rolling]`.** Restarts units by name or
  pattern through the daemon; the backoff is reset and the restart counter is
  not touched. `-rolling` does one at a time and waits for each to stay up.
- **Double-supervision guard.** `amarit run` and `amarit start` refuse a
  binary that a launchd job or systemd unit already runs, name it with the
  command that removes it, and take `-force` to override.
- **Value handoff.** `amarit.HandoffValues` carries small named strings
  across the in-place restart beside the descriptors; `amarit.InheritedValues`
  reads them once in the new process.
- **Log rotation while running.** A unit's log is rotated at 10 MB while the
  program runs (copy, then truncate in place), one previous file kept.
- Fixed: a unit with `"restart": "never"` was re-run every poll after it
  exited. Fixed: the Windows build of the CLI.

## Earlier

- v0.3.1: programs set their own update interval in `amarit.json`.
- v0.3.0: descriptor handoff across the restart; auto-update under the daemon.
- v0.2.1: `go install` instructions; version from module info.
- v0.2.0: renamed to amarit; flatter manifest; one-call API; the daemon.
- v0.1.2: `update` exits non-zero when the check or apply failed.
- v0.1.1: the one-shot `update` subcommand.
- v0.1.0: hash-tier updates, in-place exec, rollback; release CLI.
