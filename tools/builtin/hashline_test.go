package builtin

import "testing"

func TestHashLineGoldens(t *testing.T) {
	tests := []struct {
		content string
		line    int
		want    string
	}{
		{"", 1, "VJ"},
		{"a", 2, "SB"},
		{"function hello() {", 10, "SZ"},
		{"hello", 1, "SN"},
		{"world", 2, "VV"},
		{"  indented", 3, "QM"},
	}
	for _, test := range tests {
		if got := HashLine(test.content, test.line); got != test.want {
			t.Errorf("HashLine(%q, %d) = %q; want %q", test.content, test.line, got, test.want)
		}
	}
}

func TestParseAnchorIsStrict(t *testing.T) {
	anchor, ok := ParseAnchor("10#SZ")
	if !ok || anchor.Line != 10 || anchor.Hash != "SZ" {
		t.Fatalf("ParseAnchor returned %+v, %v", anchor, ok)
	}
	for _, invalid := range []string{"0#SZ", "10#sz", "10#SZ:", "10#AB", "#SZ"} {
		if _, ok := ParseAnchor(invalid); ok {
			t.Errorf("ParseAnchor(%q) unexpectedly succeeded", invalid)
		}
	}
}
