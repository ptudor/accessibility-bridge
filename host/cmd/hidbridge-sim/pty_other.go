//go:build !darwin && !linux

package main

import (
	"errors"
)

func openPTY() (*pty, string, error) {
	return nil, "", errors.New("pseudo-terminals are supported on macOS and Linux; use -stdio here")
}
