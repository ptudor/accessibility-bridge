//go:build linux

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// openPTY opens /dev/ptmx, unlocks its slave and returns the master as a
// non-blocking file (so Close interrupts Read) and the slave's path. The
// slave is opened once and kept, raw, so the master never reads EIO between
// hidbridgectl sessions.
func openPTY() (*pty, string, error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", fmt.Errorf("open /dev/ptmx: %w", err)
	}
	if err = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		_ = unix.Close(fd)
		return nil, "", fmt.Errorf("unlock pty: %w", err)
	}
	n, err := unix.IoctlGetUint32(fd, unix.TIOCGPTN)
	if err != nil {
		_ = unix.Close(fd)
		return nil, "", fmt.Errorf("pty number: %w", err)
	}
	slave := fmt.Sprintf("/dev/pts/%d", n)
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
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("termios %s: %w", path, err)
	}
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL
	if err = unix.IoctlSetTermios(fd, unix.TCSETS, t); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
