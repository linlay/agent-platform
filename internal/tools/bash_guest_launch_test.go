package tools

import (
	"agent-platform/internal/contracts"
	"agent-platform/internal/shellreview/dialect"
	"agent-platform/internal/shellreview/types"
)

// These fixtures emulate a verified Bash profile; they are not Container Hub
// launch evidence. Production login sh deliberately never sets this flag.
func fixtureBashLaunch(_ *contracts.ExecutionContext, command, cwd string, env map[string]string) (types.LaunchBinding, error) {
	binding := unverifiedSandboxLaunch(command, cwd, env)
	binding.Executable = "/verified-fixture/bash"
	binding.Template = []string{"--noprofile", "--norc", "-c", "{{command}}"}
	binding.Dialect = dialect.Bash
	binding.SyntaxSupported = true
	binding.Reason = "explicit Bash test fixture"
	return binding, nil
}
func (*scriptProbeSandbox) ReviewShellLaunch(c *contracts.ExecutionContext, s, d string, e map[string]string) (types.LaunchBinding, error) {
	return fixtureBashLaunch(c, s, d, e)
}
func (*skillProbeSandbox) ReviewShellLaunch(c *contracts.ExecutionContext, s, d string, e map[string]string) (types.LaunchBinding, error) {
	return fixtureBashLaunch(c, s, d, e)
}
func (*configProbeSandbox) ReviewShellLaunch(c *contracts.ExecutionContext, s, d string, e map[string]string) (types.LaunchBinding, error) {
	return fixtureBashLaunch(c, s, d, e)
}
func (*stubSandboxClient) ReviewShellLaunch(c *contracts.ExecutionContext, s, d string, e map[string]string) (types.LaunchBinding, error) {
	return fixtureBashLaunch(c, s, d, e)
}
