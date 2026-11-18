//go:build freebsd

package serial

import "golang.org/x/sys/unix"

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

func configure(fd int) error {
	t, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return err
	}
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.CSTOPB | crtsIflow
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL | cctsOflow
	t.Ispeed, t.Ospeed = Baud, Baud
	t.Cc[unix.VMIN] = 0
	t.Cc[unix.VTIME] = 0
	return unix.IoctlSetTermios(fd, unix.TIOCSETA, t)
}

func flush(fd int) error {
	return unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, fread|fwrite)
}
