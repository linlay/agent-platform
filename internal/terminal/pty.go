package terminal

import "io"

type startPTYRequest struct {
	Shell   string
	Args    []string
	Managed bool
	CWD     string
	Cols    int
	Rows    int
	Env     []string
}

type ptyProcess interface {
	io.Reader
	io.Writer
	Resize(cols int, rows int) error
	Busy() bool
	Close() error
	Wait() (*int, error)
}
