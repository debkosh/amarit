# An app's updater: appupdate, macapp, relaunch

`amarit.Client` checks the manifest and downloads a verified asset; a
program that installs itself does the rest. For an app a person uses, the
rest is the same every time: a timer, progress, Cancel and Retry, the
release's own checks, a staged release, the install and the restart, and a
rollback when the new release won't start. These packages are that rest,
each usable alone.

| Package | What it is |
|---|---|
| `amarit` (Client) | the manifest, the verified download with progress and a stall cut-off, typed errors, the lock every copy shares, the settings, the rollback count |
| `amarit/appupdate` | the updater: a State, its changes, and the person's commands; it draws nothing |
| `amarit/macapp` | a macOS bundle: read it, unpack it, check its signature, stage it, swap it in atomically, roll it back |
| `amarit/relaunch` | start a program once this process has gone; wait for other processes to end |

## appupdate draws nothing

The updater exposes what an interface needs and takes what a person does.
How it looks is the app's: a bubble, a settings row, a notification.

Out: `State`, from `u.State()` and on every change to `u.OnChange(f)`.
It is JSON as it is.

```json
{"phase": "downloading", "version": "0.3.9", "done": 1048576, "total": 52428800,
 "channel": "stable", "autoUpdate": true}
```

| Field | Meaning |
|---|---|
| `phase` | where the updater is, below |
| `version` | the release checked, downloading, ready or failed |
| `done`, `total` | bytes, while downloading |
| `kind` | why it failed, below |
| `error` | the error itself, for a log; the words a person reads are the app's |
| `channel`, `autoUpdate` | the settings every copy sharing the state follows |

In: `Check()`, `Retry()`, `Cancel()`, `Relaunch()`, and
`SetChannel(name)`, `SetAutoUpdate(on)`. A command sent in a phase that
does not take it does nothing, so a view only shows the buttons this table
gives its phase:

| Phase | What it means | Check | Cancel | Retry | Relaunch |
|---|---|---|---|---|---|
| `""` idle | nothing to show | yes | | | |
| `checking` | a check the person asked for | | | | |
| `current` | that check found nothing newer | yes | | | |
| `downloading` | `done` of `total` | | yes | yes | |
| `verifying` | the download is unpacked and checked | | yes | | |
| `ready` | `version` is staged | yes | | | yes |
| `failed` | `kind` says why | yes | | when retryable | |

The settings are taken in every phase. Automatic checks never show
`checking` or `current`, and a manifest they could not read is only logged:
the next round tries again.

| Kind | Why | Retryable |
|---|---|---|
| `offline` | the manifest couldn't be read | yes |
| `network` | the download failed on the way | yes |
| `stalled` | the download stopped arriving | yes |
| `damaged` | the download didn't match the manifest | yes |
| `unchecked` | the release's checks couldn't be made | yes |
| `install` | putting the release in place failed | yes |
| `refused` | the release's checks refused it: another version, a broken or foreign signature | no; Check tries it again |
| `not-writable` | the app can't be replaced where it is | no |
| `busy` | another copy's download is still stopping | no; Check again |

Behaviour behind the table:

- **Rounds.** `Start` runs a round every `Interval` (an hour unless set),
  the first `FirstCheck` after start. A round offers a release another copy
  staged, then checks if automatic updates are on and no other copy holds
  the lock.
- **Cancel** shows nothing again, and automatic rounds leave that release
  alone until a person checks.
- **Refused** releases are rejected on the install, so a bad release is
  downloaded once, not every round. A check a person asks for means "try
  it": it forgets the rejection and the cancel.
- **Relaunch** records the attempt with the client (`Installing`), then
  calls the installer. A bundle's ends the app; an APK's opens the system's
  installer and the release stays ready, so a person who backs out can ask
  again.
- **Listeners** are called in order, one change at a time, on a goroutine
  of the updater's. A listener posts the state to its own thread and may
  call the updater back.

```go
c, _ := amarit.NewClient(stateDir, "stable",
    amarit.Project("app"), amarit.Version(running), amarit.Releases(url))
u := appupdate.New(appupdate.Config{Client: c, Installer: inst, Dir: stateDir})
u.OnChange(func(s appupdate.State) { postToMain(func() { view.Show(s) }) })
u.Start()
// on a button: u.Check(), u.Cancel(), u.Retry(), u.Relaunch()
```

## Installers

An `Installer` is how a release becomes the installed app:

```go
type Installer interface {
    Usable() error                                            // ErrNotWritable: not-writable
    Prepare(ctx context.Context, file, version string) (string, error) // ErrRefused: refused
    Staged() (staged, version string)                         // a release another copy staged
    Install(staged string) error
}
```

`appupdate.File{Dir, Open}` is a release that is one file the system
installs, an APK: the download is staged as it is and `Open` hands it to
the package installer.

`macapp.Installer` is a bundle:

```go
inst := &macapp.Installer{
    App:         macapp.Running(),          // the installed .app
    StateDir:    stateDir,                  // stage/ and previous/, shared by every copy
    Requirement: macapp.SignedBy(leafSHA1), // empty: an unsigned app updates too
    Copies:      copies,                    // optional: the other running copies
    Quit:        app.Quit,
}
if back, _ := inst.Settle(c); back { app.Quit(); return } // early in main
// once a window is up:
c.Healthy()
```

- **Prepare** unpacks the zip with `ditto`, refuses a bundle whose
  `CFBundleShortVersionString` is not the manifest's version, checks the
  signature against `Requirement` in this process with the Security
  framework, and moves the bundle into the stage. Only a checked bundle
  reaches the stage, so every copy may offer what it finds there.
- **Install** asks `Copies.QuitOthers()` to end the other copies, exchanges
  the staged bundle with the installed one in one `renamex_np(RENAME_SWAP)`,
  so the app is never missing, keeps the old one in `previous/`, and has
  `Copies.StartAfter` (or `relaunch.After`) start the new release once this
  process has gone, then calls `Quit`.
- **Settle** counts this start. A start fails when its process ends before
  any copy calls `Healthy`, so copies starting together are not failures.
  After three failed starts it puts `previous/` back, rejects the release,
  and starts the previous one.

The signature check tells a real verdict from a check that couldn't be
made: unsigned, altered or foreign is `macapp.ErrBadSignature` or
`macapp.ErrWrongSigner`, both `amarit.ErrRefused`; a file that couldn't be
read is neither, and is tried again. `Unpack` gives the programs it starts
the app's open-file limit, which Go otherwise leaves at the 256 it found
under launchd.

macapp needs cgo, for the Security framework and `renamex_np`. These are
the system's own frameworks, not a dependency.

## The Client underneath

For a program that wants the pieces without appupdate:

| Call | What it does |
|---|---|
| `c.Download(ctx, d, dir, amarit.OnProgress(f), amarit.StallAfter(30*time.Second))` | progress at most ten times a second; gives up with `ErrStalled` |
| `errors.Is(err, amarit.ErrDamaged)` | the size or sha256 is not the manifest's |
| `c.TryLock()`, `c.Lock(ctx)` | the state directory's lock, one copy at a time |
| `c.Installing(v)`, `c.Started()`, `c.Healthy()`, `c.RolledBack()` | the rollback count for a program that installs itself |
| `relaunch.After(exe, args)`, `relaunch.WaitExited(pids)` | start once this process has gone; wait for others to end |
