// Package macapp updates a macOS application bundle: a signed folder of
// binaries, libraries and resources that the in-place updater, which renames
// one file, cannot replace. It reads what a bundle says it is, unpacks the
// downloaded archive, checks the signature in this process, stages the
// checked bundle where every copy of the app can see it, swaps it in with one
// atomic exchange, and puts the previous one back when the new one does not
// start. Installer puts these together for package appupdate.
package macapp
