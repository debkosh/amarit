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
`SetChannel(name)`, `SetAutoUpdate(on)`; `Close()` when the app quits. A command sent in a phase that
does not take it does nothing, so a view only shows the buttons this table
gives its phase:

| Phase | What it means | Check | Cancel | Retry | Relaunch |
|---|---|---|---|---|---|
| `""` idle | nothing to show | yes | | | |
| `checking` | a check the person asked for | | yes | | |
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
| `development` | a build with no version, which never updates | no |
| `needs-user` | the update must be made outside the app: a Flatpak's that asks for new permissions, or one the person turned down | no |

Behaviour behind the table:

- **Rounds.** `Start` runs a round every `Interval` (an hour unless set),
  the first `FirstCheck` after start. A round offers a release another copy
  staged, then checks if automatic updates are on and no other copy holds
  the lock.
- **Cancel** shows nothing again, and automatic rounds leave that release
  alone until a person checks. It ends whatever work is current, a check
  that is about to download included, so a quick Retry then Cancel leaves
  nothing running.
- **Close** ends the rounds and any work, and returns once a download has
  stopped and its files are gone; the updater takes no command after it. An
  app calls it as it quits, and a test at its end. `Stop` ends only the
  rounds.
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

A development build makes its client with no version. It keeps the
settings, so a settings page works the same; `Start` runs no rounds, and
Check fails with `development`.

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

## A Flatpak: `amarit/flatpak`

A Flatpak updates through Flatpak's update portal, not amarit's manifest:
the repository is signed, Flatpak downloads only what changed, and `/app`
stays the deployment the app started from. `appupdate.Config.Source` takes
`github.com/sudiptadeb/amarit/flatpak`, a module of its own on
`godbus/dbus/v5`, and the shell sees the same State and sends the same
commands.

```go
src, err := flatpak.New(flatpak.Options{Copies: copies, Quit: app.Quit})
c, _ := amarit.NewClient(stateDir, "stable", amarit.ProjectFile(amaritJSON)) // the settings and the lock
u := appupdate.New(appupdate.Config{Client: c, Source: src, Dir: stateDir})
src.Present(u, flatpak.Words{})  // the ready notification with Relaunch
u.Start()
```

What differs from the manifest:

| | Flatpak |
|---|---|
| Finding an update | the portal's monitor polls the remote every 30 minutes and says when it heard of a newer commit; `Check` makes the update it heard of, or shows `current` |
| `version` | what `Options.VersionOf(ctx, commit)` says the commit is, else empty: the portal names commits. It runs on the updater's goroutine and may read the network |
| Downloading | the portal's `Update`, `done` of `total` from its progress |
| Updated outside the app (GNOME Software, `flatpak update`) | `ready` at once |
| A release that asks for new permissions | `failed`, `needs-user`: the person updates in the software centre |
| The first update | Flatpak asks the person once, "Allow … to update itself?"; `downloading` at 0 while the dialog waits; a refusal is `needs-user` |
| Relaunch | the portal's `Spawn` with the latest-version flag, for each copy with `Copies`, then `Quit`; the new copy starts at once and waits for the app's own single-instance lock. It runs with the running copy's sandbox |
| Channels | none: `SetChannel` is `appupdate.ErrUnsupported` |
| Rollback | Flatpak's (`flatpak update --commit`) |
| The ready notification | the Notification portal, which a sandboxed app must use; its Relaunch button sends Relaunch. No Dock progress on Linux |

`flatpak/examples/demo` is a Flatpak that updates itself this way, with
its README's steps for a local repository. For a quick test, run the
portal with a short poll: `/usr/libexec/flatpak-portal --replace
--no-idle-exit --poll-timeout=15`.

## The system's own way of showing it

An app may show the updater the way its platform does, beside or instead of
its own view. These presenters listen to the same State and send the same
commands as any view.

**Mac**: `macapp.Present(u, macapp.Words{})` puts a progress bar on the app's
Dock icon while it downloads and checks, and when a release is ready posts a
notification with a Relaunch button. `Words` overrides the English: `Ready`
(a format of the app's name and the version), `Body`, `Relaunch`. The app
needs an NSApplication and a bundle identifier, and asks for notifications
the first time.

amarit never replaces an app's notification delegate. When the app has none,
amarit installs its own. An app with one forwards the responses in amarit's
category:

```objc
// in the app's didReceiveNotificationResponse:
if ([response.notification.request.content.categoryIdentifier isEqualToString:@"amarit.update"]) {
  GoHandleAmaritResponse(response.actionIdentifier.UTF8String); // calls macapp.HandleNotificationResponse
  completionHandler();
  return;
}
```

amarit keeps the app's own notification categories beside its own.

**Android**: the library's `UpdateNotification`, one plain Java file,
shows the state the Go
updater makes, as its JSON, as the system's notification: progress with
Cancel while downloading, Install when ready, Try again after a failure a
retry can help.

```kotlin
UpdateNotification.commands = UpdateNotification.Commands { name -> goUpdater.command(name) } // "cancel", "retry", "relaunch"
// on every change of the Go updater's state:
UpdateNotification.show(context, stateJson)
```

Its words are the string resources in `res/values/amarit_notification.xml`,
which the app overrides with its own. The app asks for `POST_NOTIFICATIONS`
on Android 13 and later. A tap while the app's process is gone opens the app.

A build without Gradle, such as Chromium's GN, takes it as source:
`android/amarit/src/main/java/io/github/sudiptadeb/amarit/UpdateNotification.java`,
`android/amarit/src/main/res/values/amarit_notification.xml` (its resources
in the package `io.github.sudiptadeb.amarit`), and the receiver in the app's
manifest:

```xml
<receiver android:name="io.github.sudiptadeb.amarit.UpdateNotification$Receiver" android:exported="false" />
```

It needs only the platform's APIs, no Kotlin and no other library.

`examples/macupdate` is an app that updates itself from a release it serves
slowly, to see both on a Mac: `./build.sh`, then open `amarit demo.app`.

## Automatic updates are the project's to declare

`AutoUpdate` is off unless the project's amarit.json says
`"auto_update": true` (pass it with `amarit.ProjectFile(raw)` or the stamp),
and a person's `SetAutoUpdate` wins over both. With it off, rounds still
offer a release another copy staged, and a person's Check still checks.

## The Client underneath

For a program that wants the pieces without appupdate:

| Call | What it does |
|---|---|
| `c.Download(ctx, d, dir, amarit.OnProgress(f), amarit.StallAfter(30*time.Second))` | progress at most ten times a second; gives up with `ErrStalled` |
| `errors.Is(err, amarit.ErrDamaged)` | the size or sha256 is not the manifest's |
| `c.TryLock()`, `c.Lock(ctx)` | the state directory's lock, one copy at a time |
| `c.Installing(v)`, `c.Started()`, `c.Healthy()`, `c.RolledBack()` | the rollback count for a program that installs itself |
| `relaunch.After(exe, args)`, `relaunch.WaitExited(pids)` | start once this process has gone; wait for others to end |
