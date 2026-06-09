package terminal

import "io"

type startPTYRequest struct {
	Shell string
	CWD   string
	Cols  int
	Rows  int
	Env   []string
}

type ptyProcess interface {
	io.Reader
	io.Writer
	Resize(cols int, rows int) error
	Close() error
	Wait() (*int, error)
}
