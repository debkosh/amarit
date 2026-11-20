//go:build unix && !darwin && !linux

package relaunch

import (
	"errors"
	"syscall"
	"time"
)

// WaitExited returns once every process of pids has ended.
func WaitExited(pids []int) error {
	for _, pid := range pids {
		for {
			err := syscall.Kill(pid, 0)
			if err != nil && !errors.Is(err, syscall.EPERM) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	return nil
}
