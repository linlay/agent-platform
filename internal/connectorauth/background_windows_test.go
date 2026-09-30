package connectorauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/subprocess"

	"golang.org/x/sys/windows"
)

type consoleProbe struct {
	Console  uintptr
	Input    string
	ChildPID uint32
}

func TestWindowsBackgroundCLIBatchPipesAndExitCode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cli with spaces")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	batch := filepath.Join(dir, "demo.cmd")
	script := "@echo off\r\n\"" + os.Args[0] + "\" -test.run=^TestWindowsCLIConsoleHelper$\r\nexit /b %ERRORLEVEL%\r\n"
	if err := os.WriteFile(batch, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	for _, exitCode := range []int{0, 7} {
		t.Run(strconv.Itoa(exitCode), func(t *testing.T) {
			env := append(os.Environ(), "AP_CONNECTORAUTH_CONSOLE_HELPER=exit", "AP_CONNECTORAUTH_EXIT="+strconv.Itoa(exitCode))
			cmd, err := cliExecutable(t.Context(), batch, []string{"status"}, dir, env)
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stdin = strings.NewReader("batch connector input 中文\n")
			out, err := cmd.Output()
			if exitCode == 0 && err != nil {
				t.Fatalf("batch output %q: %v", out, err)
			}
			if exitCode != 0 {
				var exited *exec.ExitError
				if !errors.As(err, &exited) || exited.ExitCode() != exitCode {
					t.Fatalf("batch exit code changed: %v", err)
				}
			}
			var probe consoleProbe
			if err := json.Unmarshal(out, &probe); err != nil {
				t.Fatalf("batch output %q: %v", out, err)
			}
			if probe.Console != 0 || probe.Input != "batch connector input 中文\n" {
				t.Fatalf("batch console or stdin/stdout changed: %#v", probe)
			}
		})
	}
}

func TestWindowsBackgroundCLICancelsProcessTree(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	env := append(os.Environ(), "AP_CONNECTORAUTH_CONSOLE_HELPER=wait")
	cmd, err := cliExecutable(ctx, os.Args[0], []string{"-test.run=^TestWindowsCLIConsoleHelper$"}, t.TempDir(), env)
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var probe consoleProbe
	if err := json.NewDecoder(stdout).Decode(&probe); err != nil {
		cancel()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	if probe.Console != 0 || probe.ChildPID == 0 {
		cancel()
		_ = cmd.Wait()
		t.Fatalf("invalid helper process: %#v", probe)
	}
	child, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, probe.ChildPID)
	if err != nil {
		cancel()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	defer func() {
		// Clean up by the original process handle if a cancellation regression
		// leaves the fixture alive; a PID-based kill could target a reused PID.
		if status, err := windows.WaitForSingleObject(child, 0); err == nil && status == uint32(windows.WAIT_TIMEOUT) {
			_ = windows.TerminateProcess(child, 1)
		}
		_ = windows.CloseHandle(child)
	}()
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("canceled command succeeded")
	}
	status, err := windows.WaitForSingleObject(child, 3000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("child process survived cancellation: status=%d, err=%v", status, err)
	}
}

func TestWindowsCLIConsoleHelper(t *testing.T) {
	mode := os.Getenv("AP_CONNECTORAUTH_CONSOLE_HELPER")
	if mode == "" {
		return
	}
	if mode == "child" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	console, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
	probe := consoleProbe{Console: console}
	if mode == "wait" {
		child := exec.Command(os.Args[0], "-test.run=^TestWindowsCLIConsoleHelper$")
		child.Env = append(os.Environ(), "AP_CONNECTORAUTH_CONSOLE_HELPER=child")
		subprocess.ConfigureBackground(child)
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(9)
		}
		probe.ChildPID = uint32(child.Process.Pid)
		_ = json.NewEncoder(os.Stdout).Encode(probe)
		_ = child.Wait()
		os.Exit(0)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(9)
	}
	probe.Input = string(input)
	_ = json.NewEncoder(os.Stdout).Encode(probe)
	exitCode, _ := strconv.Atoi(os.Getenv("AP_CONNECTORAUTH_EXIT"))
	os.Exit(exitCode)
}
