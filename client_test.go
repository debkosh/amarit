package amarit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, manifest func(string) string, asset []byte, running string) *Client {
	t.Helper()
	srv := serve(t, manifest, asset)
	c, err := NewClient(t.TempDir(), "stable", Project("demo"), Version(running), Releases(srv.URL+"/releases.json"))
	if err != nil {
		t.Fatal(err)
	}
	c.u.client = srv.Client()
	return c
}

func TestClientNeedsProjectVersionAndState(t *testing.T) {
	if _, err := NewClient(t.TempDir(), "", Project("demo"), Version("1.0.0")); err == nil || !strings.Contains(err.Error(), "releases") {
		t.Fatalf("want a releases error, got %v", err)
	}
	if _, err := NewClient(t.TempDir(), "", Project("demo"), Releases("http://x/releases.json")); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("want a version error, got %v", err)
	}
	if _, err := NewClient("", "", Project("demo"), Version("1.0.0"), Releases("http://x/releases.json")); err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("want a state error, got %v", err)
	}
}

func TestClientDownloadsAVerifiedAsset(t *testing.T) {
	c := testClient(t, manifestFor("0.6.0", "0.5.0", ""), assetBody, "0.5.0")
	d, err := c.Check(context.Background())
	if err != nil || !d.Update || d.Version != "0.6.0" {
		t.Fatalf("check: %v %+v", err, d)
	}
	dir := t.TempDir()
	p, err := c.Download(context.Background(), d, dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p) != dir || !strings.HasPrefix(filepath.Base(p), "demo-0.6.0-") {
		t.Fatalf("downloaded to %s", p)
	}
	if got, _ := os.ReadFile(p); string(got) != string(assetBody) {
		t.Fatal("downloaded bytes differ")
	}
	if err := c.u.Apply(context.Background(), d); err == nil || !strings.Contains(err.Error(), "downloads only") {
		t.Fatalf("a client must not swap, got %v", err)
	}
}

func TestClientRefusesABadDigestAndLeavesNothing(t *testing.T) {
	tampered := []byte("#!/bin/sh\necho evil\n")
	c := testClient(t, manifestFor("0.6.0", "0.5.0", ""), tampered, "0.5.0")
	d, _ := c.Check(context.Background())
	d.Asset.Size = int64(len(tampered)) // so only the hash fails
	dir := t.TempDir()
	if _, err := c.Download(context.Background(), d, dir); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("want a digest error, got %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("files left: %v", left)
	}
}

func TestClientRejectHoldsUntilANewerRelease(t *testing.T) {
	c := testClient(t, manifestFor("0.6.0", "0.5.0", ""), assetBody, "0.5.0")
	if err := c.Reject("0.6.0"); err != nil {
		t.Fatal(err)
	}
	d, err := c.Check(context.Background())
	if err != nil || d.Update || !strings.Contains(d.Reason, "rolled back") {
		t.Fatalf("check after reject: %v %+v", err, d)
	}
	c2 := testClient(t, manifestFor("0.7.0", "0.5.0", ""), assetBody, "0.5.0")
	c2.u.state = c.u.state
	if d, err := c2.Check(context.Background()); err != nil || !d.Update {
		t.Fatalf("a newer release should be offered: %v %+v", err, d)
	}
}

func TestClientUnrejectOffersTheVersionAgain(t *testing.T) {
	c := testClient(t, manifestFor("0.6.0", "0.5.0", ""), assetBody, "0.5.0")
	if err := c.Reject("0.6.0"); err != nil {
		t.Fatal(err)
	}
	if err := c.Unreject(); err != nil {
		t.Fatal(err)
	}
	if d, err := c.Check(context.Background()); err != nil || !d.Update {
		t.Fatalf("an unrejected release should be offered: %v %+v", err, d)
	}
}

// Two copies sharing a state directory: a channel picked in one is followed
// by the other at its next check, and so is the automatic-updates switch.
func TestClientSettingsAreShared(t *testing.T) {
	both := func(assetURL string) string {
		return fmt.Sprintf(`{"spec":1,"project":"demo","published":"2026-10-09T00:00:00Z",
		  "channels":{"stable":"0.5.0","dev":"0.5.1-dev.202611201415"},
		  "releases":{"0.5.0":{"assets":{}},"0.5.1-dev.202611201415":{"assets":{"%s/%s":{"url":"%s","sha256":"%s","size":%d}}}}}`,
			runtime.GOOS, runtime.GOARCH, assetURL, assetSHA, assetSize)
	}
	srv := serve(t, both, assetBody)
	state := t.TempDir()
	open := func() *Client {
		c, err := NewClient(state, "stable", Project("demo"), Version("0.5.0"), Releases(srv.URL+"/releases.json"), AutoUpdate(true))
		if err != nil {
			t.Fatal(err)
		}
		c.u.client = srv.Client()
		return c
	}
	one, two := open(), open()
	if two.Channel() != "stable" || !two.AutoUpdate() {
		t.Fatalf("defaults: %s %v", two.Channel(), two.AutoUpdate())
	}
	if d, err := two.Check(context.Background()); err != nil || d.Update {
		t.Fatalf("stable has nothing newer: %v %+v", err, d)
	}
	if err := one.SetChannel("dev"); err != nil {
		t.Fatal(err)
	}
	if err := one.SetAutoUpdate(false); err != nil {
		t.Fatal(err)
	}
	if two.Channel() != "dev" || two.AutoUpdate() {
		t.Fatalf("after the other copy chose: %s %v", two.Channel(), two.AutoUpdate())
	}
	if d, err := two.Check(context.Background()); err != nil || !d.Update || d.Version != "0.5.1-dev.202611201415" {
		t.Fatalf("dev build should be offered: %v %+v", err, d)
	}
	if err := one.SetChannel("de v"); err == nil {
		t.Fatal("a channel with a space must be refused")
	}
	if left, _ := filepath.Glob(filepath.Join(state, "settings-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestClientDownloadReportsProgressAndNamesADamagedOne(t *testing.T) {
	c := testClient(t, manifestFor("0.6.0", "0.5.0", ""), assetBody, "0.5.0")
	d, _ := c.Check(context.Background())
	var done, total int64
	if _, err := c.Download(context.Background(), d, t.TempDir(), OnProgress(func(n, of int64) { done, total = n, of })); err != nil {
		t.Fatal(err)
	}
	if done != assetSize || total != assetSize {
		t.Fatalf("progress ended at %d of %d, want %d", done, total, assetSize)
	}
	d.Asset.SHA256 = strings.Repeat("0", 64)
	if _, err := c.Download(context.Background(), d, t.TempDir()); !errors.Is(err, ErrDamaged) {
		t.Fatalf("a wrong sum: %v, want ErrDamaged", err)
	}
}

// A download that stops arriving is given up with ErrStalled; one its
// caller cancels with a cause returns that cause.
func TestClientDownloadStallAndCancel(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/releases.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, manifestFor("0.6.0", "0.5.0", "")(srv.URL+"/asset"))
	})
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(assetSize))
		w.Write(assetBody[:4])
		w.(http.Flusher).Flush()
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := NewClient(t.TempDir(), "stable", Project("demo"), Version("0.5.0"), Releases(srv.URL+"/releases.json"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := c.Download(context.Background(), d, dir, StallAfter(200*time.Millisecond)); !errors.Is(err, ErrStalled) {
		t.Fatalf("a stalled download: %v, want ErrStalled", err)
	}
	errMine := errors.New("the person cancelled")
	ctx, cancel := context.WithCancelCause(context.Background())
	time.AfterFunc(100*time.Millisecond, func() { cancel(errMine) })
	if _, err := c.Download(ctx, d, dir); !errors.Is(err, errMine) {
		t.Fatalf("a cancelled download: %v, want the cause", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("files left: %v", left)
	}
}

func TestClientLockIsOneCopyAtATime(t *testing.T) {
	dir := t.TempDir()
	a, _ := NewClient(dir, "", Project("demo"), Version("1.0.0"), Releases("http://x/releases.json"))
	b, _ := NewClient(dir, "", Project("demo"), Version("1.0.0"), Releases("http://x/releases.json"))
	unlock, ok, err := a.TryLock()
	if err != nil || !ok {
		t.Fatalf("first lock: %v %v", ok, err)
	}
	if _, ok, _ := b.TryLock(); ok {
		t.Fatal("a second copy took a held lock")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := b.Lock(ctx); err == nil {
		t.Fatal("Lock returned while another copy held it")
	}
	unlock()
	unlock2, err := b.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	unlock2()
}

// A release that never comes up healthy is rolled back after three failed
// starts, and copies starting at once are not failures.
func TestClientRollbackCountsStartsThatEnded(t *testing.T) {
	dir := t.TempDir()
	old, _ := NewClient(dir, "", Project("demo"), Version("1.0.0"), Releases("http://x/releases.json"))
	if err := old.Installing("1.1.0"); err != nil {
		t.Fatal(err)
	}
	next, _ := NewClient(dir, "", Project("demo"), Version("1.1.0"), Releases("http://x/releases.json"))
	// Copies starting together: this process, several times, while alive.
	for range 5 {
		if back, err := next.Started(); err != nil || back {
			t.Fatalf("live copies asked for a rollback: %v %v", back, err)
		}
	}
	// Three starts whose processes ended without Healthy.
	st := next.u.loadState()
	st.Pending = []int{deadPID(t), deadPID(t), deadPID(t)}
	next.u.saveState(st)
	back, err := next.Started()
	if err != nil || !back {
		t.Fatalf("three failed starts: rollback %v %v", back, err)
	}
	if err := next.RolledBack(); err != nil {
		t.Fatal(err)
	}
	if got := old.u.loadState(); got.Rejected != "1.1.0" || got.Healthy != "1.0.0" || got.Attempt != "" {
		t.Fatalf("state after the rollback: %+v", got)
	}
	if back, _ := old.Started(); back {
		t.Fatal("the version rolled back to asked for a rollback")
	}

	// Healthy ends the counting.
	old.Installing("1.2.0")
	healthy, _ := NewClient(dir, "", Project("demo"), Version("1.2.0"), Releases("http://x/releases.json"))
	healthy.Started()
	if err := healthy.Healthy(); err != nil {
		t.Fatal(err)
	}
	if got := healthy.u.loadState(); got.Attempt != "" || got.Healthy != "1.2.0" {
		t.Fatalf("state after Healthy: %+v", got)
	}
}

// deadPID is the pid of a process that has ended.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}
