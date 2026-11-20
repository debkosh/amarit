package main

import (
	"testing"
	"time"

	"github.com/sudiptadeb/amarit"
)

// A rolling build is named so semver orders it after the release it follows,
// after the rolling builds before it, and before the next release.
func TestRollingNameOrders(t *testing.T) {
	at := time.Date(2026, 11, 20, 14, 15, 59, 0, time.FixedZone("IST", 5*3600+1800))
	first, err := rollingName("v0.3.8", "dev", at)
	if err != nil {
		t.Fatal(err)
	}
	if first != "0.3.9-dev.202611200845" {
		t.Fatalf("got %s; the minute is UTC", first)
	}
	later, _ := rollingName("0.3.8", "dev", at.Add(time.Hour))
	for _, pair := range [][2]string{{"0.3.8", first}, {first, later}, {later, "0.3.9"}} {
		if c, err := amarit.CompareVersions(pair[0], pair[1]); err != nil || c != -1 {
			t.Fatalf("%s must sort before %s (got %d, %v)", pair[0], pair[1], c, err)
		}
	}
	for _, bad := range [][2]string{{"0.3.9-dev.1", "dev"}, {"0.3", "dev"}, {"0.3.8", ""}, {"0.3.8", "de.v"}, {"0.3.8", "a+b"}} {
		if _, err := rollingName(bad[0], bad[1], at); err == nil {
			t.Fatalf("rollingName(%q, %q) must be refused", bad[0], bad[1])
		}
	}
}
