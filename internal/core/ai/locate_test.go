package ai

import "testing"

func TestParsePoint(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		x, y  int
		found bool
		isErr bool
	}{
		{"plain", "POINT: 123,456", 123, 456, true, false},
		{"spaces", "point: 123 , 456", 123, 456, true, false},
		{"parens", "POINT: (88, 12)", 88, 12, true, false},
		{"after prose", "The Save button is in the toolbar.\nPOINT: 412,88", 412, 88, true, false},
		{"markdown", "**POINT: 10,20**", 10, 20, true, false},
		{"x separator", "POINT: 640x480", 640, 480, true, false},
		{"not found", "I examined the image.\nNOT FOUND", 0, 0, false, false},
		{"not found prose", "The element is NOT FOUND in this screenshot.", 0, 0, false, false},
		{"last wins", "POINT: 1,1\nActually correcting myself:\nPOINT: 200,300", 200, 300, true, false},
		{"empty", "", 0, 0, false, true},
		{"garbage", "I cannot help with that.", 0, 0, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x, y, found, err := ParsePoint(c.raw)
			if c.isErr {
				if err == nil {
					t.Fatalf("want error, got x=%d y=%d found=%v", x, y, found)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if found != c.found || x != c.x || y != c.y {
				t.Fatalf("got (%d,%d,found=%v), want (%d,%d,found=%v)", x, y, found, c.x, c.y, c.found)
			}
		})
	}
}
