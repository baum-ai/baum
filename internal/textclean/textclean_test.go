package textclean

import "testing"

func TestSafeText(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"clean", "hello, wörld", "hello, wörld"},
		{"nul", "a\x00b", "a�b"},
		{"invalid utf8", "a\xffb", "a�b"},
		{"both", "\x00\xff", "��"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SafeText(tt.in); got != tt.want {
				t.Errorf("SafeText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
