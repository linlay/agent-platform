package hitl

import "testing"

func TestWorkspaceCheckerAllowsInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	result := CheckWorkspaceAccess(WorkspaceCheckInput{
		Command:       "cat ./src/main.ts",
		WorkspaceRoot: root,
	})
	if result.Intercepted {
		t.Fatalf("expected inside workspace command to pass, got %#v", result)
	}
}

func TestWorkspaceCheckerInterceptsOutsideCwd(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	result := CheckWorkspaceAccess(WorkspaceCheckInput{
		Command:       "pwd",
		Cwd:           outside,
		WorkspaceRoot: root,
	})
	if !result.Intercepted {
		t.Fatalf("expected outside cwd to be intercepted")
	}
	if result.Rule.ViewportType != "builtin" || result.Rule.ViewportKey != "approval" {
		t.Fatalf("expected builtin approval, got %#v", result.Rule)
	}
}

func TestWorkspaceCheckerInterceptsOutsidePathArg(t *testing.T) {
	root := t.TempDir()
	result := CheckWorkspaceAccess(WorkspaceCheckInput{
		Command:       "cat /etc/hosts",
		WorkspaceRoot: root,
	})
	if !result.Intercepted {
		t.Fatalf("expected absolute outside path to be intercepted")
	}
}

func TestWorkspaceCheckerInterceptsRelativeEscape(t *testing.T) {
	root := t.TempDir()
	result := CheckWorkspaceAccess(WorkspaceCheckInput{
		Command:       "cat ../secret.txt",
		Cwd:           root,
		WorkspaceRoot: root,
	})
	if !result.Intercepted {
		t.Fatalf("expected relative escape to be intercepted")
	}
}

func TestWorkspaceCheckerDefersComplexShellToExistingSecurity(t *testing.T) {
	root := t.TempDir()
	result := CheckWorkspaceAccess(WorkspaceCheckInput{
		Command:       "cat $(pwd)/secret.txt",
		WorkspaceRoot: root,
	})
	if result.Intercepted {
		t.Fatalf("expected complex shell to be left to bash security, got %#v", result)
	}
}

func TestWorkspaceCheckerIgnoresHeredocBodyPaths(t *testing.T) {
	root := t.TempDir()
	result := CheckWorkspaceAccess(WorkspaceCheckInput{
		Command:       "cat <<EOF\n/etc/hosts\nEOF",
		WorkspaceRoot: root,
	})
	if result.Intercepted {
		t.Fatalf("expected heredoc body path to pass, got %#v", result)
	}
}

func TestWorkspaceCheckerStillInterceptsHeredocOutputPath(t *testing.T) {
	root := t.TempDir()
	result := CheckWorkspaceAccess(WorkspaceCheckInput{
		Command:       "cat <<EOF > /etc/heredoc.out\nhello\nEOF",
		WorkspaceRoot: root,
	})
	if !result.Intercepted {
		t.Fatalf("expected heredoc output path to be intercepted")
	}
}
