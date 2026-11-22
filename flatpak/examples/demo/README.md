# amarit/flatpak demo

A Flatpak that updates itself through the portal. In a Linux VM with
flatpak, flatpak-builder and org.freedesktop.Platform//26.08:

    GOOS=linux GOARCH=arm64 go build -ldflags "-X main.build=1" -o amarit-demo .
    flatpak-builder --user --force-clean --default-branch=stable --repo=repo build io.github.sudiptadeb.amarit.demo.yml
    flatpak build-update-repo --generate-static-deltas repo
    flatpak --user remote-add --no-gpg-verify amarit-demo "file://$PWD/repo"
    flatpak --user install -y amarit-demo io.github.sudiptadeb.amarit.demo
    flatpak run io.github.sudiptadeb.amarit.demo -auto-relaunch &

Then build again with `-X main.build=2` and the same two repository
commands. The portal's monitor hears of the new commit at its next poll, the
log in `~/.var/app/io.github.sudiptadeb.amarit.demo/data/amarit-demo.log`
shows downloading, ready and the relaunch, and the next line is build 2.
Add a permission to finish-args for a third commit, and the update fails as
needs-user.
