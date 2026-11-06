package acp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/agentengine"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	dto "agent-platform/internal/contracts/agentengine"
	"agent-platform/internal/httpclient"
	"agent-platform/internal/llm"
	driver "agent-platform/internal/runtime/acp"
	"agent-platform/internal/runtime/query"
	"agent-platform/internal/runtime/runstate"
	"agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/terminal"
)

// Run only from Desktop's opt-in archive acceptance. Never enabled in ordinary go test.
// The bootstrap, records, snapshots and managed Node all come from the real installer.
func TestInstalledPluginsProductQuery(t *testing.T) {
	root := os.Getenv("ACP_ACCEPTANCE_ROOT")
	if root == "" || os.Getenv("ACP_LIVE_AUTH") != "1" {
		t.Skip("requires Desktop archive acceptance and explicit local-auth model calls")
	}
	runtimeRoot := filepath.Join(root, "platform")
	registry, err := agentengine.New(runtimeRoot, filepath.Join(root, "bootstrap.json"), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	data, err := os.ReadFile(filepath.Join(root, "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.Apply("snapshot", data); err != nil {
		t.Fatal(err)
	}
	var snapshot dto.EngineSnapshot
	if err = json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	for _, entry := range snapshot.Entries {
		t.Run(entry.PluginID, func(t *testing.T) {
			name := strings.TrimPrefix(entry.PluginID, "zenmind-agent-")
			state := filepath.Join(root, "runs", name)
			os.MkdirAll(state, 0700)
			workspace := filepath.Join(state, "workspace")
			os.MkdirAll(workspace, 0700)
			store, err := chat.NewFileStoreAtStartup(filepath.Join(state, "chats"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			_, _, err = store.EnsureChat("acceptance", "project", "")
			if err != nil {
				t.Fatal(err)
			}
			host, err := driver.New(registry, state, driver.Options{Proxy: httpclient.Config{Mode: "fixed", URL: "http://127.0.0.1:7890"}})
			if err != nil {
				t.Fatal(err)
			}
			defer host.Close()
			catalog := queryCatalog{def: catalog.AgentDefinition{Key: "project", Mode: "CODER", Engine: catalog.AgentEngineACP, Runtime: map[string]any{"engineId": entry.EngineID, "workspaceRoot": workspace}, Workspace: catalog.AgentWorkspaceConfig{Root: workspace}}}
			sessions := driver.NewSessions(host, store, catalog.AgentDefinition)
			defer sessions.Close()
			sessions.EnableAuthentication(terminal.NewManager())
			ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
			defer cancel()
			_, prepareErr := sessions.Prepare(ctx, "acceptance")
			t.Logf("installed snapshot initial preparation: ready=%v", prepareErr == nil)
			sessions.CloseChat("acceptance")
			profiles, err := os.ReadDir(filepath.Join(state, "agent-engine-profiles"))
			if err != nil || len(profiles) != 1 {
				t.Fatal("missing isolated profile", err)
			}
			profile := filepath.Join(state, "agent-engine-profiles", profiles[0].Name(), name)
			home, err := os.UserHomeDir()
			if err != nil {
				t.Fatal(err)
			}
			// Copy only authorized authentication state, never projects, MCP settings or session history.
			source := filepath.Join(home, ".codex", "auth.json")
			target := filepath.Join(profile, "auth.json")
			if name == "claude" {
				source = filepath.Join(home, ".claude", ".credentials.json")
				target = filepath.Join(profile, ".credentials.json")
			}
			if auth, err := os.ReadFile(source); err == nil {
				if err = os.WriteFile(target, auth, 0600); err != nil {
					t.Fatal(err)
				}
			} else if name == "codex" {
				t.Fatal("local Codex auth file unavailable")
			} else {
				// This workstation's Claude login is a configured gateway. Copy only
				// its credential + matching endpoint, never hooks, tools or other settings.
				data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
				var config struct {
					Env map[string]string `json:"env"`
				}
				if err != nil || json.Unmarshal(data, &config) != nil {
					t.Fatal("existing Claude credential settings unavailable")
				}
				env := map[string]string{}
				for _, key := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"} {
					if value := config.Env[key]; value != "" {
						env[key] = value
					}
				}
				if env["ANTHROPIC_AUTH_TOKEN"] == "" && env["ANTHROPIC_API_KEY"] == "" {
					t.Fatal("existing Claude authentication unavailable")
				}
				if err = os.WriteFile(filepath.Join(profile, "settings.json"), encoded(t, map[string]any{"env": env}), 0600); err != nil {
					t.Fatal(err)
				}
				t.Log("reusing authorized local Claude gateway configuration in isolated profile")
			}
			// Out-of-band test credential import requires a fresh host session manager;
			// product login uses StartAuthentication and its explicit recheck instead.
			sessions.Close()
			host, err = driver.New(registry, state, driver.Options{Proxy: httpclient.Config{Mode: "fixed", URL: "http://127.0.0.1:7890"}})
			if err != nil {
				t.Fatal(err)
			}
			defer host.Close()
			sessions = driver.NewSessions(host, store, catalog.AgentDefinition)
			defer sessions.Close()
			sessions.EnableAuthentication(terminal.NewManager())
			prepared, err := sessions.Prepare(ctx, "acceptance")
			if err != nil {
				t.Fatal("real adapter preparation", err)
			}
			if len(prepared.ConfigOptions) == 0 {
				t.Fatal("real session configuration unavailable")
			}
			// Confirm the external model before sending the first prompt.
			for _, raw := range prepared.ConfigOptions {
				var option struct {
					ID, Category string
					CurrentValue json.RawMessage
					Choices      []dto.ConfigChoice
				}
				json.Unmarshal(raw, &option)
				if option.Category != "model" {
					continue
				}
				value := option.CurrentValue
				for _, choice := range option.Choices {
					if choice.Value == "gpt-6.1-terra" {
						value = encoded(t, choice.Value)
					}
				}
				prepared, err = sessions.Configure(ctx, dto.ConfigUpdate{ChatID: "acceptance", PreparationID: prepared.PreparationID, ConfigRevision: prepared.ConfigRevision, Selections: []dto.ConfigSelection{{ConfigID: option.ID, Value: value}}})
				if err != nil {
					t.Fatal("real configuration confirmation", err)
				}
				t.Log("external model configured before first Query")
				break
			}
			runs := runstate.NewManager()
			service := query.NewService(query.Dependencies{Chats: store, Registry: catalog, Runs: runs, Sessions: session.New(session.Dependencies{Chats: store}), ACP: sessions, DeltaMappers: llm.DeltaMapperFactory{}})
			marker := "ACP-PRODUCT-" + name + "-7319"
			messages := []string{"Remember this marker for our next turn: " + marker + ". Reply with only the marker. Do not use tools.", "What was the marker I asked you to remember? Reply with only the exact marker. Do not use tools."}
			if os.Getenv("ACP_LIVE_INTERACTIONS") == "cancel" {
				messages = nil
			}
			for index, message := range messages {
				if index == 1 {
					if err = sessions.CloseChat("acceptance"); err != nil {
						t.Fatal(err)
					}
				}
				result, err := service.ExecuteQuery(ctx, runtimetypes.QueryCommand{ChatID: "acceptance", AgentKey: "project", Message: message, IncludeFullText: true}, runtimetypes.QueryHooks{})
				if err != nil {
					t.Fatalf("real Query turn %d: %v", index+1, err)
				}
				if result.FinishReason != "complete" || !strings.Contains(result.Content, marker) {
					t.Fatalf("real Query turn %d: finish=%s markerMatched=%v", index+1, result.FinishReason, strings.Contains(result.Content, marker))
				}
				saved, err := store.Summary("acceptance")
				if err != nil || saved.LastRunID != result.RunID || !strings.Contains(saved.LastRunContent, marker) {
					t.Fatal("real answer not persisted", err)
				}
				t.Logf("real Query turn %d complete, answer persisted, context marker matched", index+1)
			}
			if mode := os.Getenv("ACP_LIVE_INTERACTIONS"); mode == "1" || mode == "cancel" {
				realProductInteractions(t, ctx, name, workspace, sessions, service, runs)
			}
			if err = sessions.CloseChat("acceptance"); err != nil {
				t.Fatal(err)
			}
			if host.Stats().Processes != 0 {
				t.Fatal("adapter process remained after close")
			}
		})
	}
}

func realProductInteractions(t *testing.T, ctx context.Context, name, workspace string, sessions *driver.Sessions, service *query.Service, runs *runstate.Manager) {
	t.Helper()
	configure := func(id, value string) {
		prepared, err := sessions.Prepare(ctx, "acceptance")
		if err != nil {
			t.Fatal(err)
		}
		_, err = sessions.Configure(ctx, dto.ConfigUpdate{ChatID: "acceptance", PreparationID: prepared.PreparationID, ConfigRevision: prepared.ConfigRevision, Selections: []dto.ConfigSelection{{ConfigID: id, Value: encoded(t, value)}}})
		if err != nil {
			t.Fatal("real mode configuration", err)
		}
	}
	ctx = runtimetypes.WithIdentity(ctx, &contracts.AuthIdentity{Subject: "acceptance-owner"})
	phases := []string{"question", "approve", "cancel"}
	if os.Getenv("ACP_LIVE_INTERACTIONS") == "cancel" {
		phases = []string{"cancel"}
	}
	for _, phase := range phases {
		target := filepath.Join(workspace, "probe-"+phase+".txt")
		tool := "Write"
		if name == "codex" {
			tool = "apply_patch"
			mode := "default"
			if phase == "question" || phase == "cancel" {
				mode = "plan"
			}
			configure("collaboration_mode", mode)
			if phase == "approve" {
				configure("mode", "read-only")
			}
		} else {
			configure("mode", "default")
		}
		prompt := "Use " + tool + " to create only " + target + " containing exactly OK on its own line. This is a single-file approval test. Request required approval; never bypass permissions. Do not run shell commands or touch other files. After approval reply DONE."
		if phase == "question" || phase == "cancel" {
			tool = "AskUserQuestion"
			if name == "codex" {
				tool = "request_user_input"
			}
			prompt = "Protocol test: use " + tool + " to ask exactly one single-choice question Choose probe result, with options OK and SKIP. Do not use other tools or access files. After the answer reply only DONE."
		}
		handle, err := service.StartQuery(ctx, runtimetypes.QueryCommand{ChatID: "acceptance", AgentKey: "project", Message: prompt})
		if err != nil {
			t.Fatal(err)
		}
		sub, err := service.AttachRun(ctx, runtimetypes.RunRef{RunID: handle.RunID}, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
		asked := false
		terminal := ""
	drain:
		for {
			select {
			case event, ok := <-sub.Events:
				if !ok {
					break drain
				}
				if event.Type == "run.error" {
					t.Fatalf("real %s returned run.error", phase)
				}
				if event.Type == "run.complete" || event.Type == "run.cancel" {
					terminal = event.Type
				}
				if event.Type != "awaiting.ask" {
					continue
				}
				asked = true
				command := runtimetypes.SubmitCommand{RunRef: runtimetypes.RunRef{RunID: handle.RunID, ChatID: "acceptance", AgentKey: "project"}, AwaitingID: event.String("awaitingId"), SubmitID: "product-" + phase}
				if phase == "cancel" {
					if event.String("mode") != "question" {
						t.Fatal("cancel requires the requested question wait")
					}
					_, err = service.Interrupt(ctx, runtimetypes.InterruptCommand{RunRef: command.RunRef})
					if err != nil {
						t.Fatal(err)
					}
					continue
				}
				if phase == "question" {
					if event.String("mode") != "question" {
						t.Fatal("unexpected permission during question")
					}
					questions, _ := event.Value("questions").([]any)
					for _, raw := range questions {
						q := raw.(map[string]any)
						options, _ := q["options"].([]any)
						if len(options) == 0 {
							if required, _ := q["required"].(bool); required {
								t.Fatal("required real question has no choices")
							}
							continue
						}
						first := options[0].(map[string]any)
						command.Params = append(command.Params, encoded(t, map[string]any{"id": q["id"], "answer": first["id"]}))
					}
				} else {
					if event.String("mode") != "approval" {
						t.Fatal("unexpected question during edit")
					}

					// Inspect the same neutral approval details shown by the product UI.
					approvals, _ := event.Value("approvals").([]any)
					if len(approvals) != 1 {
						t.Fatal("missing real approval")
					}
					approval := approvals[0].(map[string]any)
					var request struct {
						Kind      string
						Locations []struct{ Path string }
						RawInput  struct {
							FilePath string `json:"file_path"`
						}
					}
					commandText, _ := approval["command"].(string)
					if json.Unmarshal([]byte(commandText), &request) != nil {
						t.Fatal("invalid approval description")
					}

					paths := []string{}
					for _, location := range request.Locations {
						paths = append(paths, location.Path)
					}
					if len(paths) == 0 && request.RawInput.FilePath != "" {
						paths = append(paths, request.RawInput.FilePath)
					}
					if request.Kind != "edit" || len(paths) == 0 {
						t.Fatal("permission is not a bounded file edit")
					}
					for _, candidate := range paths {
						if !filepath.IsAbs(candidate) {
							candidate = filepath.Join(workspace, candidate)
						}
						parent, _ := filepath.EvalSymlinks(filepath.Dir(candidate))
						expected, _ := filepath.EvalSymlinks(workspace)
						if parent != expected || filepath.Base(candidate) != filepath.Base(target) {
							t.Fatal("permission escaped disposable target")
						}
					}
					command.Params = []json.RawMessage{encoded(t, map[string]any{"id": approval["id"], "decision": "approve"})}
				}
				result, err := service.Submit(ctx, command)
				if err != nil || !result.Accepted {
					t.Fatal("real interaction submit failed", err)
				}
			case <-ctx.Done():
				t.Fatalf("real %s timed out", phase)
			}
		}
		sub.Close()
		if !asked {
			t.Fatalf("real %s did not request interaction", phase)
		}
		expected := "run.complete"
		if phase == "cancel" {
			expected = "run.cancel"
		}
		if terminal != expected {
			t.Fatalf("real %s terminal=%s", phase, terminal)
		}
		if phase == "approve" {
			data, err := os.ReadFile(target)
			if err != nil || strings.TrimSpace(string(data)) != "OK" {
				t.Fatal("approved file edit not applied", err)
			}
		}
		if phase == "cancel" {
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("cancelled edit created a file")
			}
		}
		t.Logf("real %s: host event, control/submit and terminal verified", phase)
	}
}
