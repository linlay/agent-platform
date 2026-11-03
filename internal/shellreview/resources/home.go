package resources

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// accountHome queries account metadata using a fixed system utility and a
// minimal environment. It never executes the analyzed program or reads HOME
// from run.env. Unknown/unsupported account services remain unresolved.
func accountHome() (string, error) {
	if os.Getuid() != os.Geteuid() {
		return "", errors.New("account principal is ambiguous")
	}
	program, args := accountHomeCommand(strconv.Itoa(os.Getuid()))
	if program == "" {
		return "", errors.New("account home lookup unsupported")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	cmd.WaitDelay = 100 * time.Millisecond
	var output boundedProbeOutput
	output.limit = 16384
	cmd.Stdout = &output
	err := cmd.Run()
	if err != nil {
		if accountNotFound(err) {
			return "", nil
		}
		return "", errors.New("account home lookup failed")
	}
	if output.Len() > 16384 {
		return "", errors.New("account response exceeds limit")
	}
	return parseAccountHome(output.String())
}
func parsePasswdHome(raw string) (string, error) {
	fields := strings.Split(strings.TrimSpace(raw), ":")
	if len(fields) != 7 || !strings.HasPrefix(fields[5], "/") {
		return "", errors.New("invalid account home response")
	}
	return fields[5], nil
}

// Returning a short write stops subprocess output once its review budget is full.
type boundedProbeOutput struct {
	buffer bytes.Buffer
	limit  int
}

func (b *boundedProbeOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		return 0, io.ErrShortBuffer
	}
	return b.buffer.Write(p)
}

func (b *boundedProbeOutput) Len() int       { return b.buffer.Len() }
func (b *boundedProbeOutput) String() string { return b.buffer.String() }
