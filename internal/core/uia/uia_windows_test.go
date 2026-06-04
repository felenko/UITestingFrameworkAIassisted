//go:build windows

package uia

import "testing"

func TestControlTypeID(t *testing.T) {
	cases := map[string]int32{
		"button":   50000,
		"edit":     50004,
		"textbox":  50004,
		"combobox": 50003,
		"checkbox": 50002,
		"menuitem": 50011,
		"window":   50032,
		"":         0,
		"nonsense": 0,
	}
	for in, want := range cases {
		if got := controlTypeID(in); got != want {
			t.Errorf("controlTypeID(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestQueryDescribe(t *testing.T) {
	q := Query{AutomationID: "btnSave", ControlType: "button"}
	if got := q.describe(); got == "" {
		t.Errorf("describe() should not be empty")
	}
}
