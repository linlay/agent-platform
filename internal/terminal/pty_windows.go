//go:build windows

package terminal

func startPTY(req startPTYRequest) (ptyProcess, error) {
	return nil, ErrUnsupported
}
