package tools

func windowsShellUTF8Command(shellExecutable string, command string, goos string) string {
	if goos != "windows" {
		return command
	}
	switch normalizedShellBase(shellExecutable) {
	case "powershell", "pwsh":
		return "$OutputEncoding = New-Object System.Text.UTF8Encoding -ArgumentList $false; [Console]::OutputEncoding = $OutputEncoding; " + command
	case "cmd":
		return "chcp 65001 >NUL & " + command
	default:
		return command
	}
}
