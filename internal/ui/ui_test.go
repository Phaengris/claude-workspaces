package ui_test

import (
	"bytes"
	"testing"

	"github.com/Phaengris/claude-workspaces/internal/ui"
)

func TestPrintJSON(t *testing.T) {
	var b bytes.Buffer
	if err := ui.PrintJSON(&b, map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"a\": 1\n}\n"; b.String() != want {
		t.Errorf("got %q want %q", b.String(), want)
	}
}

func TestTable(t *testing.T) {
	var b bytes.Buffer
	ui.Table(&b, [][]string{{"a", "bb"}, {"ccc", "d"}})
	if want := "a    bb\nccc  d\n"; b.String() != want {
		t.Errorf("got %q want %q", b.String(), want)
	}
}
