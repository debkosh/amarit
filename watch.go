package amarit

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"time"
)

var (
	// ErrDamaged is a download whose size or sha256 is not the manifest's.
	ErrDamaged = errors.New("the download does not match the manifest")
	// ErrStalled is a download that stopped arriving for longer than
	// StallAfter allows: a connection that died without saying so, as one
	// does across a laptop's sleep.
	ErrStalled = errors.New("the download stopped arriving")
	// ErrRefused is a release the program's own checks refused: a bundle of
	// another version, or a signature that is broken or someone else's. A
	// program's checks wrap it; a check that could not be made does not.
	ErrRefused = errors.New("the release was refused")
	// ErrNotWritable is an installed program that cannot be replaced where
	// it is, such as a macOS app still on its disk image.
	ErrNotWritable = errors.New("the installed program can't be replaced where it is")
	// ErrDevelopment is a Client made with no version: a development build,
	// which keeps the settings and never updates.
	ErrDevelopment = errors.New("a development build doesn't update")
)

// DownloadOption tunes one Client.Download.
type DownloadOption func(*watch)

// OnProgress hears how much of the asset has arrived, in bytes, at most ten
// times a second and once at the end.
func OnProgress(f func(done, total int64)) DownloadOption {
	return func(w *watch) { w.progress = f }
}

// StallAfter gives up on a download that receives nothing for d, with
// ErrStalled. The clock is the monotonic one, which stops while the machine
// sleeps.
func StallAfter(d time.Duration) DownloadOption {
	return func(w *watch) { w.stall = d }
}

type watch struct {
	progress func(done, total int64)
	stall    time.Duration
}

func newWatch(opts []DownloadOption) *watch {
	if len(opts) == 0 {
		return nil
	}
	w := &watch{}
	for _, o := range opts {
		o(w)
	}
	return w
}

// reader counts what passes through r, reports it, and, with a stall limit,
// cancels the download once nothing has passed for that long.
func (w *watch) reader(ctx context.Context, r io.Reader, total int64, cancel context.CancelCauseFunc) io.Reader {
	cr := &countingReader{r: r, w: w, total: total, start: time.Now()}
	if w.stall > 0 {
		go func() {
			tick := time.NewTicker(min(w.stall/4, time.Second))
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
				}
				if cr.quiet() >= w.stall {
					cancel(ErrStalled)
					return
				}
			}
		}()
	}
	return cr
}

type countingReader struct {
	r        io.Reader
	w        *watch
	total    int64
	done     int64
	reported time.Time
	// start is when the download began, and heard how long after it bytes
	// last arrived: an offset, so the stall watcher reads it without a lock
	// and the clock stays the monotonic one.
	start time.Time
	heard atomic.Int64
}

func (c *countingReader) quiet() time.Duration {
	return time.Since(c.start) - time.Duration(c.heard.Load())
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.done += int64(n)
		c.heard.Store(int64(time.Since(c.start)))
		if c.w.progress != nil && time.Since(c.reported) >= 100*time.Millisecond {
			c.reported = time.Now()
			c.w.progress(c.done, c.total)
		}
	}
	if err == io.EOF && c.w.progress != nil {
		c.w.progress(c.done, c.total)
	}
	return n, err
}
