package adminsource

import (
	"strings"
	"testing"
)

func TestControlYAMLTextRoundTrip(t *testing.T) {
	t.Setenv("CONTROL_YAML_SECRET", "expanded-secret")
	for _, tc := range []struct{ name, env string }{
		{"quoted", "    TOKEN: 'a: # secret' # keep this comment\n    OTHER: \"${CONTROL_YAML_SECRET}\"\n"},
		{"multiline", "    TOKEN: |- # explanation\n      first-secret\n\n      # also secret scalar data\n      second-secret\n      # trailing secret data\n\n    OTHER: >\n      one\n      two\n"},
		{"empty-block", "    TOKEN: | # empty\n    OTHER: >-\n"},
		{"comments-only", "    TOKEN: |\n      # only-secret-data\n    OTHER: literal\n"},
		{"flow", "  env: {TOKEN: 'secret: value', OTHER: \"${CONTROL_YAML_SECRET}\"} # env comment\n"},
	} {
		for _, newline := range []string{"\n", "\r\n"} {
			t.Run(tc.name+newline, func(t *testing.T) {
				env := "  env:\n" + tc.env
				if tc.name == "flow" {
					env = tc.env
				}
				content := "# user header\nkey: demo\ngreetings:\n- hello\n- hello\n\nname: 'Keep quotes' # description\nruntimeConfig:\n" + env + "  timeout: 10 # keep order\n# footer\n"
				content = strings.ReplaceAll(content, "\n", newline)
				safe, paths, err := redactAgentEnvironment(content)
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{"only-secret-data", "first-secret", "second-secret", "secret scalar data", "trailing secret data", "${CONTROL_YAML_SECRET}", "expanded-secret", "secret: value"} {
					if strings.Contains(safe, secret) {
						t.Fatalf("secret leaked: %q", safe)
					}
				}
				restored, err := preserveAgentEnvironment(content, safe, paths)
				if err != nil {
					t.Fatal(err)
				}
				if restored != content {
					t.Fatalf("changed original formatting\nwant %q\ngot  %q", content, restored)
				}
				edited := strings.Replace(safe, "'Keep quotes'", "'New name'", 1)
				restored, err = preserveAgentEnvironment(content, edited, paths)
				if err != nil {
					t.Fatal(err)
				}
				if restored != strings.Replace(content, "'Keep quotes'", "'New name'", 1) {
					t.Fatal("unrelated bytes changed")
				}
			})
		}
	}
}

func TestControlYAMLFlowParentsAndMissingPlaceholder(t *testing.T) {
	for _, text := range []string{
		"runtimeConfig: {env: {TOKEN: 'secret', OTHER: 'value'}, timeout: 10} # tail\n",
		"runtimeConfig:\n  env:\n    TOKEN: 'secret'\n    OTHER: 'value'\n",
	} {
		safe, paths, err := redactAgentEnvironment(text)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := preserveAgentEnvironment(text, safe, paths)
		if err != nil || restored != text {
			t.Fatalf("%q %v", restored, err)
		}
	}
	old := "runtimeConfig:\n  env:\n    TOKEN: '${ENV_TOKEN:raw}'\n"
	for _, candidate := range []string{"runtimeConfig:\n  env: {} # keep\n", "runtimeConfig:\n  env:\n    NEW: fresh\n"} {
		got, err := preserveAgentEnvironment(old, candidate, []string{"runtimeConfig.env.TOKEN"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "'${ENV_TOKEN:raw}'") {
			t.Fatalf("lost raw value: %q", got)
		}
	}
}

func TestControlYAMLAmbiguityFailsClosed(t *testing.T) {
	for _, text := range []string{
		"runtimeConfig:\n  env:\n    TOKEN: first\n    TOKEN: second\n",
		"runtimeConfig:\n  env: {TOKEN: first, TOKEN: second}\n",
		"runtimeConfig:\n  env: {TOKEN: first}\nruntimeConfig:\n  env: {TOKEN: second}\n",
	} {
		if _, _, err := redactAgentEnvironment(text); err == nil {
			t.Fatalf("accepted duplicate: %s", text)
		}
	}
}
