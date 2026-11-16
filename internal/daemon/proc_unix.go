//go:build !windows

package daemon

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

func runtimeOS() string { return runtime.GOOS }

func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func terminate(pid int) { _ = syscall.Kill(pid, syscall.SIGTERM) }

func forceKill(pid int) { _ = syscall.Kill(pid, syscall.SIGKILL) }

// lockFile takes an exclusive, non-blocking lock that the OS releases if the process dies.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// Locked reports whether a daemon (or an in-process command) currently owns the session.
func Locked(path string) bool {
	unlock, err := lockFile(path)
	if err != nil {
		return true
	}
	unlock()
	return false
}
