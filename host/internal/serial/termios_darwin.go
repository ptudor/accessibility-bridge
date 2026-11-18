//go:build darwin

package serial

import (
	"errors"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// BSD termios.h flow-control bits; x/sys exposes only their union CRTSCTS.
const (
	cctsOflow = 0x00010000 // CTS gates output
	crtsIflow = 0x00020000 // RTS signals input room; the cable RTS# output is unused
)

// sys/fcntl.h flags for TIOCFLUSH.
const (
	fread  = 1
	fwrite = 2
)

// iossiospeed is IOKit's _IOW('T', 2, speed_t) with a 64-bit speed_t. It
// sets a rate outside the termios table, which the FTDI driver needs only if
// it refuses 1 000 000 through tcsetattr; a pty has no use for it.
const iossiospeed = 0x80085402

func configure(fd int) error {
	t, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return err
	}
	raw(t)
	t.Cflag |= cctsOflow
	t.Ispeed, t.Ospeed = Baud, Baud
	if err = unix.IoctlSetTermios(fd, unix.TIOCSETA, t); err == nil {
		return nil
	}
	if !errors.Is(err, unix.EINVAL) {
		return err
	}
	t.Ispeed, t.Ospeed = unix.B38400, unix.B38400
	if err = unix.IoctlSetTermios(fd, unix.TIOCSETA, t); err != nil {
		return err
	}
	// IOSSIOSPEED copies a speed_t (uint64 on Darwin), whereas
	// IoctlSetPointerInt points at only an int32.
	speed := uint64(Baud)
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), iossiospeed, uintptr(unsafe.Pointer(&speed)))
	if errno != 0 {
		return errno
	}
	return nil
}

func raw(t *unix.Termios) {
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.CSTOPB | crtsIflow
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL
	t.Cc[unix.VMIN] = 0
	t.Cc[unix.VTIME] = 0
}

func flush(fd int) error {
	return unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, fread|fwrite)
}
