package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	contracts "agent-platform/internal/contracts"
)

type rejectingToolOutputSink struct{}

func (rejectingToolOutputSink) EmitToolOutput(context.Context, contracts.ToolOutput) error {
	return errors.New("live stream closed")
}

func TestBashOutputCapturePreservesControlBytesInLiveAndFinalOutput(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "bash-output-*.log")
	if err != nil {
		t.Fatalf("create output file: %v", err)
	}
	defer file.Close()

	sink := &collectingToolOutputSink{chunks: make(chan contracts.ToolOutput, 8)}
	capture := newBashOutputCapture(context.Background(), file, sink, contracts.ToolOutputStdout)
	writes := []string{
		"progress=000%\rprogress=",
		"050%\b\b\b100%",
		"\x1b[2K\rcompleted\n",
	}
	for _, value := range writes {
		if _, err := capture.Write([]byte(value)); err != nil {
			t.Fatalf("write capture: %v", err)
		}
	}
	capture.Close()

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek output file: %v", err)
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	want := strings.Join(writes, "")
	if string(raw) != want {
		t.Fatalf("final capture %q, want %q", raw, want)
	}

	var live strings.Builder
	for len(sink.chunks) > 0 {
		chunk := <-sink.chunks
		if chunk.Stream != contracts.ToolOutputStdout {
			t.Fatalf("live stream %q, want stdout", chunk.Stream)
		}
		live.WriteString(chunk.Delta)
	}
	if live.String() != want {
		t.Fatalf("live output %q, want %q", live.String(), want)
	}
}

func TestBashOutputCaptureMakesChildStdoutNonSeekableWithoutLiveSink(t *testing.T) {
	if os.Getenv("AP_TEST_BASH_OUTPUT_PIPE_HELPER") == "1" {
		if _, err := os.Stdout.Seek(0, io.SeekCurrent); err == nil {
			fmt.Print("seekable")
		} else {
			fmt.Print("pipe")
		}
		os.Exit(0)
	}

	file, err := os.CreateTemp(t.TempDir(), "bash-output-*.log")
	if err != nil {
		t.Fatalf("create output file: %v", err)
	}
	defer file.Close()
	capture := newBashOutputCapture(context.Background(), file, nil, contracts.ToolOutputStdout)

	cmd := exec.Command(os.Args[0], "-test.run=^TestBashOutputCaptureMakesChildStdoutNonSeekableWithoutLiveSink$")
	cmd.Env = append(os.Environ(), "AP_TEST_BASH_OUTPUT_PIPE_HELPER=1")
	cmd.Stdout = capture
	if err := cmd.Run(); err != nil {
		t.Fatalf("run helper process: %v", err)
	}
	capture.Close()

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek output file: %v", err)
	}
	output, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if string(output) != "pipe" {
		t.Fatalf("child stdout mode %q, want pipe", output)
	}
}

func TestBashOutputCaptureReportsFileWriteFailure(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "bash-output-*.log")
	if err != nil {
		t.Fatalf("create output file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close output file: %v", err)
	}

	capture := newBashOutputCapture(context.Background(), file, nil, contracts.ToolOutputStdout)
	if _, err := capture.Write([]byte("lost")); err == nil {
		t.Fatal("expected closed output file write to fail")
	}
	if capture.Err() == nil {
		t.Fatal("expected capture to retain the file write error")
	}
}

func TestBashOutputCaptureLiveFailureDoesNotCorruptFinalOutput(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "bash-output-*.log")
	if err != nil {
		t.Fatalf("create output file: %v", err)
	}
	defer file.Close()

	capture := newBashOutputCapture(context.Background(), file, rejectingToolOutputSink{}, contracts.ToolOutputStdout)
	if _, err := capture.Write([]byte("final survives\n")); err != nil {
		t.Fatalf("live sink failure leaked into capture write: %v", err)
	}
	capture.Close()
	if err := capture.Err(); err != nil {
		t.Fatalf("live sink failure became capture failure: %v", err)
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek output file: %v", err)
	}
	output, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if string(output) != "final survives\n" {
		t.Fatalf("final output %q, want preserved bytes", output)
	}
}
