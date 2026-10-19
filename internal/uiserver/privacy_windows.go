package uiserver

import (
	"errors"
	"os"
	"unsafe"

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
	return privateDACL(descriptor, sid)
}

func privateDACL(descriptor *windows.SECURITY_DESCRIPTOR, sid *windows.SID) bool {
	acl, _, err := descriptor.DACL()
	if err != nil || acl == nil || acl.AceCount != 1 {
		return false
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &ace); err != nil {
		return false
	}
	// Inspect the ACL itself: SDDL can abbreviate well-known SIDs and include
	// other security descriptor sections, even when only the DACL was requested.
	const fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
	return ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE &&
		ace.Header.AceFlags&windows.INHERIT_ONLY_ACE == 0 &&
		ace.Mask == fileAllAccess &&
		(*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(sid)
}
