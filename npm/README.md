# amarit

amarit: keep it alive, keep it current. Self-update from a release manifest, in-place or zero-downtime restart, health-gated rollback, and keep-alive under launchd, systemd or nothing.

```sh
npm install -g amarit      # or run it once: npx amarit ...
amarit run <command>
```

The first run downloads the amarit release this package names from GitHub, checks it against the
sha256 pinned in the package, and installs it to `~/.local/bin/amarit`, where the
[install script](https://github.com/debkosh/amarit/blob/main/install.sh) puts it. From then on
the binary there (or in `/usr/local/bin`) is the one that runs, and it keeps itself current.

Everything else: https://github.com/debkosh/amarit
