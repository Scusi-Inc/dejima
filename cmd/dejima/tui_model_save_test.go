package main

import "testing"

// The API-key field accepts typing/paste immediately — no "press Enter to start
// editing" — and arrow keys navigate between fields without corrupting the key.
func TestModelEditorTypesKeyDirectly(t *testing.T) {
	m := tuiModel{modelEditor: &modelEditor{
		providers: []string{"openai"}, provSel: 0, field: 2, // focused on the API key
	}}
	for _, c := range []string{"s", "k", "-", "a", "b", "c", "1", "2", "3"} {
		m.modelEditorKey(key(c))
	}
	if m.modelEditor.keyInput != "sk-abc123" {
		t.Fatalf("keyInput = %q, want sk-abc123 (typed directly, no enter-to-edit)", m.modelEditor.keyInput)
	}
	// Down arrow moves to the Save action and must not append to the key.
	m.modelEditorKey(key("down"))
	if m.modelEditor.field != fieldSave {
		t.Errorf("down should move to the Save field (%d), got %d", fieldSave, m.modelEditor.field)
	}
	if m.modelEditor.keyInput != "sk-abc123" {
		t.Errorf("navigation altered the key: %q", m.modelEditor.keyInput)
	}
}

// Enter with a typed key kicks off the save (busy), rather than needing a
// separate keystroke to commit.
func TestModelEditorEnterSaves(t *testing.T) {
	m := tuiModel{modelEditor: &modelEditor{
		providers: []string{"openai"}, provSel: 0, field: 2,
		loadedProvider: "openai", loadedModel: "openai/gpt-5.5", model: "openai/gpt-5.5",
	}}
	m.modelEditorKey(key("x"))
	m.modelEditorKey(key("y"))
	m.modelEditorKey(key("z"))
	m.modelEditorKey(key("1"))
	m.modelEditorKey(key("2"))
	m.modelEditorKey(key("3"))
	m.modelEditorKey(key("4"))
	m.modelEditorKey(key("5")) // 8-char key
	m.modelEditorKey(key("enter"))
	if m.modelEditor == nil {
		t.Fatal("editor closed unexpectedly before the async key save")
	}
	if !m.modelEditor.busy {
		t.Error("Enter with a typed key should start the save (busy), got not-busy")
	}
}

// esc closes the whole editor from any field (no half-open key-entry mode to
// back out of first).
func TestModelEditorEscCloses(t *testing.T) {
	m := tuiModel{modelEditor: &modelEditor{providers: []string{"openai"}, field: 2}}
	m.modelEditorKey(key("a"))
	next, _ := m.modelEditorKey(key("esc"))
	if next.(tuiModel).modelEditor != nil {
		t.Error("esc should close the editor")
	}
}
