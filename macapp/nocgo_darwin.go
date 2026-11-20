//go:build !cgo

package macapp

// The signature check and the atomic swap call macOS through cgo.
var _ = macappNeedsCgoOnMacOS
