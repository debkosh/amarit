package amarit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
