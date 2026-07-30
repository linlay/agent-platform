package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
)

type auditFinding struct {
	AgentKey   string `json:"agentKey,omitempty"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Severity   string `json:"severity"`
	SourcePath string `json:"sourcePath,omitempty"`
}

func main() {
	configDir := flag.String("config-dir", "", "configuration root containing configs/")
	jsonOutput := flag.Bool("json", false, "emit JSON")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments:", strings.Join(flag.Args(), " "))
		os.Exit(1)
	}

	findings, err := audit(*configDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "workspace/chat audit failed:", err)
		os.Exit(1)
	}
	blocking := blockingFindingCount(findings)
	if *jsonOutput {
		payload := map[string]any{"ok": blocking == 0, "blockingCount": blocking, "findings": findings}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(payload); err != nil {
			fmt.Fprintln(os.Stderr, "encode audit result:", err)
			os.Exit(1)
		}
	} else if len(findings) == 0 {
		fmt.Println("Workspace/Chat configuration audit passed.")
	} else {
		fmt.Printf("Workspace/Chat configuration audit found %d blocking and %d informational finding(s):\n", blocking, len(findings)-blocking)
		for _, finding := range findings {
			owner := finding.SourcePath
			if finding.AgentKey != "" {
				owner += " [" + finding.AgentKey + "]"
			}
			fmt.Printf("- [%s] %s: %s: %s\n", normalizedSeverity(finding.Severity), owner, finding.Code, finding.Message)
		}
	}
	if blocking != 0 {
		os.Exit(2)
	}
}

func audit(configDir string) ([]auditFinding, error) {
	options := config.LoadOptions{
		ConfigDir:                             configDir,
		IgnoreRemovedWorkingDirectoryForAudit: true,
	}
	cfg, err := config.Load(options)
	if err != nil {
		return nil, err
	}
	findings, err := auditRemovedWorkingDirectory(configDir)
	if err != nil {
		return nil, err
	}
	catalogFindings, err := catalog.AuditWorkspaceChatConfig(cfg)
	if err != nil {
		return nil, err
	}
	for _, finding := range catalogFindings {
		findings = append(findings, auditFinding{
			AgentKey:   finding.AgentKey,
			Code:       finding.Code,
			Message:    finding.Message,
			Severity:   finding.Severity,
			SourcePath: finding.SourcePath,
		})
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].SourcePath != findings[j].SourcePath {
			return findings[i].SourcePath < findings[j].SourcePath
		}
		if findings[i].AgentKey != findings[j].AgentKey {
			return findings[i].AgentKey < findings[j].AgentKey
		}
		return findings[i].Code < findings[j].Code
	})
	return findings, nil
}

func auditRemovedWorkingDirectory(configDir string) ([]auditFinding, error) {
	root := strings.TrimSpace(configDir)
	if root == "" {
		root = filepath.Dir(config.ConfigFile("configs/tools.yml"))
		root = filepath.Dir(root)
	} else {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		root = abs
	}
	path := filepath.Join(root, "configs", "tools.yml")
	tree, err := config.LoadYAMLTree(path)
	if err != nil {
		return nil, err
	}
	values, _ := tree.(map[string]any)
	findings := make([]auditFinding, 0)
	for _, section := range []string{"access-policy", "bash", "file-tools"} {
		sectionValues, _ := values[section].(map[string]any)
		if _, ok := sectionValues["working-directory"]; !ok {
			continue
		}
		findings = append(findings, auditFinding{
			Code:       "removed_working_directory",
			Message:    section + ".working-directory was removed; relative paths are always workspace-relative",
			Severity:   "error",
			SourcePath: filepath.Clean(path),
		})
	}
	return findings, nil
}

func normalizedSeverity(severity string) string {
	if strings.EqualFold(strings.TrimSpace(severity), "info") {
		return "info"
	}
	return "error"
}

func blockingFindingCount(findings []auditFinding) int {
	count := 0
	for _, finding := range findings {
		if normalizedSeverity(finding.Severity) != "info" {
			count++
		}
	}
	return count
}
