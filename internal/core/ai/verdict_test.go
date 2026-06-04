package ai

import "testing"

func TestParseVerdict(t *testing.T) {
	truthy := []string{"YES", "yes", "Yes.", "true", "1", "YES. The dialog is shown.", "  yes  ", "pass"}
	falsy := []string{"NO", "no", "No, nothing visible", "false", "0", "FAIL"}

	for _, in := range truthy {
		v, err := ParseVerdict(in)
		if err != nil || !v {
			t.Errorf("ParseVerdict(%q) = (%v,%v), want (true,nil)", in, v, err)
		}
	}
	for _, in := range falsy {
		v, err := ParseVerdict(in)
		if err != nil || v {
			t.Errorf("ParseVerdict(%q) = (%v,%v), want (false,nil)", in, v, err)
		}
	}
}

func TestParseVerdictUnparseable(t *testing.T) {
	for _, in := range []string{"maybe", "", "I am not sure either way yes or no"} {
		if _, err := ParseVerdict(in); err == nil {
			t.Errorf("ParseVerdict(%q) should error", in)
		}
	}
}

func TestMajority(t *testing.T) {
	if v, ok := majority([]bool{true, true, false}); !ok || !v {
		t.Errorf("majority(2T,1F) = (%v,%v)", v, ok)
	}
	if v, ok := majority([]bool{false, false, true}); !ok || v {
		t.Errorf("majority(1T,2F) = (%v,%v)", v, ok)
	}
	if _, ok := majority([]bool{true, false}); ok {
		t.Errorf("majority of a tie should report no winner")
	}
}

func TestExpectBool(t *testing.T) {
	if !expectBool("yes") || !expectBool("") || !expectBool("true") {
		t.Error("yes/empty/true should be true")
	}
	if expectBool("no") || expectBool("false") {
		t.Error("no/false should be false")
	}
}
