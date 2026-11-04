package store

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestMacroDefinitionRoundTripPreservesJSON(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	definition := []byte(` { "name": "deploy", "steps": [{"expect": "ready"}] } `)
	if err := db.SetMacroDefinition("deploy", definition); err != nil {
		t.Fatal(err)
	}

	got, found, err := db.MacroDefinition("deploy")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected macro definition to be found")
	}
	if !bytes.Equal(got, definition) {
		t.Fatalf("definition changed: got %q, want %q", got, definition)
	}
}

func TestMacroDefinitionOverwriteAndList(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.SetMacroDefinition("z-deploy", []byte(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := db.SetMacroDefinition("a-check", []byte(`{"command":"uptime"}`)); err != nil {
		t.Fatal(err)
	}
	if err := db.SetMacroDefinition("z-deploy", []byte(`{"version":2,"secret":"do-not-log"}`)); err != nil {
		t.Fatal(err)
	}

	got, found, err := db.MacroDefinition("z-deploy")
	if err != nil {
		t.Fatal(err)
	}
	if !found || string(got) != `{"version":2,"secret":"do-not-log"}` {
		t.Fatalf("definition not replaced: found=%v got=%q", found, got)
	}

	definitions, err := db.MacroDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 2 {
		t.Fatalf("definitions = %#v", definitions)
	}
	if string(definitions["a-check"]) != `{"command":"uptime"}` {
		t.Fatalf("unexpected a-check definition: %q", definitions["a-check"])
	}
	if string(definitions["z-deploy"]) != `{"version":2,"secret":"do-not-log"}` {
		t.Fatalf("unexpected z-deploy definition: %q", definitions["z-deploy"])
	}
}

func TestMacroDefinitionMissingAndDelete(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	got, found, err := db.MacroDefinition("missing")
	if err != nil {
		t.Fatal(err)
	}
	if found || got != nil {
		t.Fatalf("missing macro = %q, found=%v", got, found)
	}

	definitions, err := db.MacroDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	if definitions == nil || len(definitions) != 0 {
		t.Fatalf("empty definitions = %#v", definitions)
	}

	if err := db.SetMacroDefinition("disposable", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteMacroDefinition("disposable"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := db.MacroDefinition("disposable"); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("macro definition was not deleted")
	}
	if err := db.DeleteMacroDefinition("disposable"); err != nil {
		t.Fatalf("deleting missing macro failed: %v", err)
	}
}

func TestMacroDefinitionValidation(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, name := range []string{"", " ", "\t\n"} {
		if err := db.SetMacroDefinition(name, []byte(`{}`)); err == nil {
			t.Fatalf("expected empty name error for %q", name)
		}
		if _, _, err := db.MacroDefinition(name); err == nil {
			t.Fatalf("expected empty name error for %q", name)
		}
		if err := db.DeleteMacroDefinition(name); err == nil {
			t.Fatalf("expected empty name error for %q", name)
		}
	}
	for _, definition := range [][]byte{nil, {}, []byte(" \t\n")} {
		if err := db.SetMacroDefinition("empty", definition); err == nil {
			t.Fatalf("expected empty definition error for %q", definition)
		}
	}
}

func TestMacroDefinitionsPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetMacroDefinition("persistent", []byte(`{"steps":["connect","run"]}`)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	got, found, err := reopened.MacroDefinition("persistent")
	if err != nil {
		t.Fatal(err)
	}
	if !found || string(got) != `{"steps":["connect","run"]}` {
		t.Fatalf("definition did not persist: found=%v got=%q", found, got)
	}
	definitions, err := reopened.MacroDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 || string(definitions["persistent"]) != `{"steps":["connect","run"]}` {
		t.Fatalf("definitions did not persist: %#v", definitions)
	}
}
