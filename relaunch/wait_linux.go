package relaunch

import (
	"errors"
	"syscall"
	"time"
	"unsafe"
)

// sysPidfdOpen is pidfd_open(2), the same number on every architecture
// since Linux 5.3; the syscall package has no wrapper for it.
const sysPidfdOpen = 434

// WaitExited returns once every process of pids has ended. Each pid gets a
// pidfd, which becomes readable when the process ends; one already gone is
// ESRCH. A kernel before 5.3, without pidfds, is polled instead.
func WaitExited(pids []int) error {
	var fds []pollFd
	for _, pid := range pids {
		fd, _, errno := syscall.Syscall(sysPidfdOpen, uintptr(pid), 0, 0)
		switch errno {
		case 0:
			fds = append(fds, pollFd{fd: int32(fd), events: pollIn})
		case syscall.ESRCH:
		case syscall.ENOSYS:
			for _, f := range fds {
				syscall.Close(int(f.fd))
			}
			return pollExited(pids)
		default:
			return errno
		}
	}
	defer func() {
		for _, f := range fds {
			syscall.Close(int(f.fd))
		}
	}()
	for len(fds) > 0 {
		_, _, errno := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&fds[0])), uintptr(len(fds)), 0, 0, 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return errno
		}
		// Keep the ones still running.
		running := fds[:0]
		for _, f := range fds {
			if f.revents == 0 {
				running = append(running, f)
			} else {
				syscall.Close(int(f.fd))
			}
		}
		fds = running
	}
	return nil
}

const pollIn = 0x1

type pollFd struct {
	fd      int32
	events  int16
	revents int16
}

// pollExited waits by asking every 50 ms; EPERM is a process of another user,
// still there.
func pollExited(pids []int) error {
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
