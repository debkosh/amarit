package main

import (
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// now is time.Now; a variable so a test can fix the minute.
var now = time.Now

// nextVersion prints the name of a rolling build after a release: the next
// patch with a pre-release tag of the channel and the UTC minute,
// 0.3.9-dev.202611201415 after 0.3.8. Semver puts it after 0.3.8, after the
// rolling builds before it, and before 0.3.9, so a desktop client on the
// channel takes each one and then the release itself. Build metadata
// (0.3.8+g6c3c4d4) cannot do that: semver ignores it for order.
func nextVersion(args []string) error {
	fs := flag.NewFlagSet("next-version", flag.ContinueOnError)
	pre := fs.String("pre", "dev", "the pre-release tag, normally the channel's name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: amarit next-version [-pre dev] <released version>")
	}
	v, err := rollingName(fs.Arg(0), *pre, now())
	if err != nil {
		return err
	}
	fmt.Println(v)
	return nil
}

func rollingName(released, pre string, t time.Time) (string, error) {
	n, err := releaseNumbers(released)
	if err != nil {
		return "", err
	}
	if pre == "" || !versionChars(pre) {
		return "", fmt.Errorf("-pre %q must be one identifier of letters, digits and hyphens", pre)
	}
	return fmt.Sprintf("%d.%d.%d-%s.%s", n[0], n[1], n[2]+1, pre, t.UTC().Format("200601021504")), nil
}

// releaseNumbers parses a plain release version, major.minor.patch with an
// optional leading v, and nothing after it.
func releaseNumbers(v string) ([3]int, error) {
	var n [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return n, fmt.Errorf("version %q: want a release, major.minor.patch", v)
	}
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 || p[0] == '+' || (len(p) > 1 && p[0] == '0') {
			return n, fmt.Errorf("version %q: %q is not a valid number", v, p)
		}
		n[i] = x
	}
	return n, nil
}

func versionChars(s string) bool {
	for _, r := range s {
		if !(r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}
