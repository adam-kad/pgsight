//go:build windows

package main

import (
	"io"
	"os"
)

func openTTY() (io.ReadCloser, error) {
	return os.OpenFile("CONIN$", os.O_RDWR, 0)
}
