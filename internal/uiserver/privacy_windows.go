package uiserver

import (
	"errors"
	"os"
	"regexp"

	"golang.org/x/sys/windows"
)

func userSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}

// Unix mode bits do not enforce privacy on Windows. Protect the directory's
// DACL and inherit only the current user's full-control ACE into new files.
func protectDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("ui instance path must be a directory")
	}
	sid, err := userSID()
	if err != nil {
		return err
	}
	current, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := current.Owner()
	if err != nil {
		return err
	}
	if !owner.Equals(sid) {
		return errors.New("ui instance directory must belong to the current user")
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + sid.String() + ")")
	if err != nil {
		return err
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
func privateFile(path string, info os.FileInfo) bool {
	if !info.Mode().IsRegular() {
		return false
	}
	sid, err := userSID()
	if err != nil {
		return false
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	owner, _, err := descriptor.Owner()
	if err != nil || !owner.Equals(sid) {
		return false
	}
	// Query the DACL separately so the SDDL contains exactly one allowed ACE;
	// inherited ACE flags may differ between the lock and renamed temp file.
	descriptor, err = windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	return regexp.MustCompile(`^D:[A-Z]*\(A;[A-Z]*;FA;;;` + regexp.QuoteMeta(sid.String()) + `\)$`).MatchString(descriptor.String())
}
