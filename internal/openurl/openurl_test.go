package openurl

import (
	"reflect"
	"testing"
)

func TestCommand(t *testing.T) {
	const target = "http://127.0.0.1:1/"
	tests := []struct {
		goos string
		name string
		args []string
	}{
		{"linux", "xdg-open", []string{target}},
		{"darwin", "open", []string{target}},
		{"windows", "rundll32", []string{"url.dll,FileProtocolHandler", target}},
	}
	for _, tt := range tests {
		name, args, err := command(tt.goos, target)
		if err != nil || name != tt.name || !reflect.DeepEqual(args, tt.args) {
			t.Errorf("%s: got %q %q %v", tt.goos, name, args, err)
		}
	}
	if _, _, err := command("plan9", target); err == nil {
		t.Error("an unknown platform should fail")
	}
}

func TestOpenRefusesNonHTTP(t *testing.T) {
	for _, target := range []string{"file:///etc/passwd", "javascript:alert(1)", "calc.exe", ""} {
		if err := Open(target); err == nil {
			t.Errorf("Open(%q) should refuse", target)
		}
	}
}
