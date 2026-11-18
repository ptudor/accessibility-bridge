//go:build darwin

package main

import (
	"bytes"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openPTY opens /dev/ptmx, unlocks its slave and returns the master as a
// non-blocking file (so Close interrupts Read) and the slave's path. The
// slave is opened once and kept, raw, so the master never sees a hangup
// between hidbridgectl sessions.
func openPTY() (*pty, string, error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", fmt.Errorf("open /dev/ptmx: %w", err)
	}
	if err = unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		_ = unix.Close(fd)
		return nil, "", fmt.Errorf("grant pty: %w", err)
	}
	if err = unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		_ = unix.Close(fd)
		return nil, "", fmt.Errorf("unlock pty: %w", err)
	}
	var name [128]byte
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
		_ = unix.Close(fd)
		return nil, "", fmt.Errorf("pty name: %w", errno)
	}
	slave := string(name[:bytes.IndexByte(name[:], 0)])
	kept, err := keepSlave(slave)
	if err != nil {
		_ = unix.Close(fd)
		return nil, "", err
	}
	if err = unix.SetNonblock(fd, true); err != nil {
		_ = kept.Close()
		_ = unix.Close(fd)
		return nil, "", err
	}
	return &pty{File: os.NewFile(uintptr(fd), "/dev/ptmx"), slave: kept}, slave, nil
}

func keepSlave(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	t, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("termios %s: %w", path, err)
	}
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL
	if err = unix.IoctlSetTermios(fd, unix.TIOCSETA, t); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
