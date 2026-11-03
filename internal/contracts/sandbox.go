package contracts

import "errors"

// ErrSandboxCommandNotStarted is evidence from the executor that the user
// command was never dispatched. RPC/transport failures after dispatch must not
// carry it, since replay could execute the command twice.
var ErrSandboxCommandNotStarted = errors.New("sandbox command not started")
