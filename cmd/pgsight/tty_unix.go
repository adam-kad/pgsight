//go:build !windows

package main

import (
	"io"
	"os"
)

func openTTY() (io.ReadCloser, error) {
	return os.Open("/dev/tty")
}
