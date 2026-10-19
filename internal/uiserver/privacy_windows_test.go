package uiserver

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPrivateDACL(t *testing.T) {
	// LocalSystem is rendered as SY in SDDL rather than its numeric SID.
	sid, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		sddl string
		want bool
	}{
		{"private", "D:P(A;;FA;;;SY)", true},
		{"inherited", "D:AI(A;ID;FA;;;SY)", true},
		{"other sections", "O:SYG:SYD:P(A;;FA;;;SY)S:AI", true},
		{"null ACL", "D:NO_ACCESS_CONTROL", false},
		{"empty ACL", "D:P", false},
		{"other user", "D:P(A;;FA;;;BA)", false},
		{"extra reader", "D:P(A;;FA;;;SY)(A;;FR;;;WD)", false},
		{"read only", "D:P(A;;FR;;;SY)", false},
		{"deny", "D:P(D;;FA;;;SY)", false},
		{"inherit only", "D:P(A;OIIO;FA;;;SY)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			descriptor, err := windows.SecurityDescriptorFromString(tc.sddl)
			if err != nil {
				t.Fatal(err)
			}
			if got := privateDACL(descriptor, sid); got != tc.want {
				t.Fatalf("privateDACL(%s) = %v, want %v", descriptor, got, tc.want)
			}
		})
	}
}

func TestPrivateFileInheritsProtectedDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := protectDirectory(directory); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "lock")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !privateFile(path, info) {
		t.Fatal("rejected file inheriting the current-user-only DACL")
	}
}
