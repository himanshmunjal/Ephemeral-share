package handlers

import (
	"strings"
	"testing"
)

func TestRandomCodeUsesUnambiguousAlphabet(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		code, err := randomCode(8)
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != 8 {
			t.Fatalf("length %d", len(code))
		}
		for _, r := range code {
			if !strings.ContainsRune(crockfordAlphabet, r) {
				t.Fatalf("character %q outside the alphabet", r)
			}
			if strings.ContainsRune("ILOU", r) {
				t.Fatalf("ambiguous character %q in code", r)
			}
		}
		seen[code] = true
	}
	if len(seen) < 1990 {
		t.Errorf("only %d unique codes in 2000 draws — suspiciously low entropy", len(seen))
	}
}
