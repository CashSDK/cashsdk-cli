//go:build windows

package config

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// Windows ignores Unix permission bits. Protect the DACL from inherited access
// and allow only this user and the operating system before writing credentials.
func makePrivate(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("credential path must not be a symbolic link")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	inheritance := ""
	if info.IsDir() {
		inheritance = "OICI"
	}
	sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf(
		"D:P(A;%s;FA;;;SY)(A;%s;FA;;;%s)", inheritance, inheritance, user.User.Sid.String()))
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}
