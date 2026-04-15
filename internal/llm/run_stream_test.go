package llm

import "testing"

func TestSanitizeQuestionFields(t *testing.T) {
	questions := []any{
		map[string]any{
			"question":            "Pick a plan",
			"type":                " select ",
			"header":              "Plan",
			"placeholder":         "ignored for select",
			"allowFreeText":       true,
			"freeTextPlaceholder": "Type your own plan",
			"multiSelect":         false,
			"options": []any{
				map[string]any{"label": "Weekend", "description": "2 days"},
			},
		},
		map[string]any{
			"question":            "Your name",
			"type":                "text",
			"header":              "Profile",
			"placeholder":         "Type your name",
			"allowFreeText":       true,
			"freeTextPlaceholder": "should be removed",
			"multiSelect":         true,
			"options":             []any{map[string]any{"label": "unused"}},
		},
		map[string]any{
			"question":            "How many people?",
			"type":                "number",
			"placeholder":         "3",
			"allowFreeText":       false,
			"freeTextPlaceholder": "should be removed",
			"multiSelect":         false,
			"options":             []any{map[string]any{"label": "unused"}},
		},
		map[string]any{
			"question":            "Secret",
			"type":                "password",
			"placeholder":         "Enter password",
			"allowFreeText":       false,
			"freeTextPlaceholder": "should be removed",
			"multiSelect":         false,
			"options":             []any{map[string]any{"label": "unused"}},
		},
	}

	sanitized := sanitizeQuestionFields(questions)

	selectQuestion := sanitized[0].(map[string]any)
	if _, ok := selectQuestion["allowFreeText"]; !ok {
		t.Fatal("expected select question to keep allowFreeText")
	}
	if _, ok := selectQuestion["freeTextPlaceholder"]; !ok {
		t.Fatal("expected select question to keep freeTextPlaceholder")
	}
	if _, ok := selectQuestion["multiSelect"]; !ok {
		t.Fatal("expected select question to keep multiSelect")
	}
	if _, ok := selectQuestion["options"]; !ok {
		t.Fatal("expected select question to keep options")
	}

	for _, idx := range []int{1, 2, 3} {
		question := sanitized[idx].(map[string]any)
		if _, ok := question["allowFreeText"]; ok {
			t.Fatalf("expected question %d to drop allowFreeText", idx)
		}
		if _, ok := question["freeTextPlaceholder"]; ok {
			t.Fatalf("expected question %d to drop freeTextPlaceholder", idx)
		}
		if _, ok := question["multiSelect"]; ok {
			t.Fatalf("expected question %d to drop multiSelect", idx)
		}
		if _, ok := question["options"]; ok {
			t.Fatalf("expected question %d to drop options", idx)
		}
		if question["question"] == nil {
			t.Fatalf("expected question %d to keep question text", idx)
		}
		if question["placeholder"] == nil {
			t.Fatalf("expected question %d to keep placeholder", idx)
		}
	}
}

func TestBuildAwaitQuestionsQuestionModeClonesBeforeSanitizing(t *testing.T) {
	payload := map[string]any{
		"mode": "question",
		"questions": []any{
			map[string]any{
				"question":            "How many people?",
				"type":                "number",
				"placeholder":         "3",
				"allowFreeText":       false,
				"freeTextPlaceholder": "should be removed",
				"multiSelect":         false,
				"options":             []any{map[string]any{"label": "unused"}},
			},
		},
	}

	got := buildAwaitQuestions(payload)
	if len(got) != 1 {
		t.Fatalf("expected 1 question, got %d", len(got))
	}

	original := payload["questions"].([]any)[0].(map[string]any)
	if _, ok := original["allowFreeText"]; !ok {
		t.Fatal("expected original payload to remain unchanged")
	}
	if _, ok := original["options"]; !ok {
		t.Fatal("expected original payload options to remain unchanged")
	}

	sanitized := got[0].(map[string]any)
	if _, ok := sanitized["allowFreeText"]; ok {
		t.Fatal("expected sanitized clone to drop allowFreeText")
	}
	if _, ok := sanitized["freeTextPlaceholder"]; ok {
		t.Fatal("expected sanitized clone to drop freeTextPlaceholder")
	}
	if _, ok := sanitized["multiSelect"]; ok {
		t.Fatal("expected sanitized clone to drop multiSelect")
	}
	if _, ok := sanitized["options"]; ok {
		t.Fatal("expected sanitized clone to drop options")
	}
	if sanitized["question"] != "How many people?" {
		t.Fatalf("expected question text to be preserved, got %#v", sanitized["question"])
	}
	if sanitized["placeholder"] != "3" {
		t.Fatalf("expected placeholder to be preserved, got %#v", sanitized["placeholder"])
	}
}
