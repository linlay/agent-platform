package subprocess

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestConfigureBackgroundPreservesProcessAttributes(t *testing.T) {
	original := &syscall.SysProcAttr{CmdLine: "custom command line", CreationFlags: windows.CREATE_NEW_PROCESS_GROUP, NoInheritHandles: true}
	cmd := exec.Command("unused.exe")
	cmd.SysProcAttr = original
	canceled := false
	cmd.Cancel = func() error { canceled = true; return nil }
	ConfigureBackground(cmd)
	if !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags != windows.CREATE_NEW_PROCESS_GROUP|windows.CREATE_NO_WINDOW {
		t.Fatalf("background console flags missing: %#v", cmd.SysProcAttr)
	}
	if cmd.SysProcAttr.CmdLine != original.CmdLine || !cmd.SysProcAttr.NoInheritHandles {
		t.Fatalf("existing attributes changed: %#v", cmd.SysProcAttr)
	}
	if original.HideWindow || original.CreationFlags != windows.CREATE_NEW_PROCESS_GROUP {
		t.Fatal("mutated shared process attributes")
	}
	_ = cmd.Cancel()
	if !canceled {
		t.Fatal("cancellation callback changed")
	}
}

func TestWindowsBackgroundConsoleAndPipes(t *testing.T) {
	for _, exitCode := range []int{0, 7} {
		t.Run(strconv.Itoa(exitCode), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsBackgroundHelper$")
			cmd.Env = append(os.Environ(), "AP_SUBPROCESS_HELPER=1", "AP_SUBPROCESS_EXIT="+strconv.Itoa(exitCode))
			cmd.Stdin = strings.NewReader("connector input 中文\n")
			var stderr strings.Builder
			cmd.Stderr = &stderr
			ConfigureBackground(cmd)
			out, err := cmd.Output()
			if exitCode == 0 && err != nil {
				t.Fatal(err)
			}
			if exitCode != 0 {
				var exited *exec.ExitError
				if !errors.As(err, &exited) || exited.ExitCode() != exitCode {
					t.Fatalf("exit code changed: %v", err)
				}
			}
			var result struct {
				Console uintptr
				Input   string
			}
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatalf("output %q: %v", out, err)
			}
			if result.Console != 0 || result.Input != "connector input 中文\n" || stderr.String() != "connector diagnostic\n" {
				t.Fatalf("console/pipes changed: %#v, stderr=%q", result, stderr.String())
			}
		})
	}
}

func TestWindowsBackgroundHelper(t *testing.T) {
	if os.Getenv("AP_SUBPROCESS_HELPER") != "1" {
		return
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(9)
	}
	console, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		Console uintptr
		Input   string
	}{console, string(input)})
	fmt.Fprintln(os.Stderr, "connector diagnostic")
	exitCode, _ := strconv.Atoi(os.Getenv("AP_SUBPROCESS_EXIT"))
	os.Exit(exitCode)
}
