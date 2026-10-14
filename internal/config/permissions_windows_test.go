//go:build windows

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func assertPrivateDACL(t *testing.T, path string, directory bool) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("DACL permits inherited access: %v", err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	flags := ""
	if directory {
		flags = "OICI"
	}
	sddl := sd.String()
	expected, err := windows.SecurityDescriptorFromString("D:P(A;" + flags + ";FA;;;SY)(A;" + flags + ";FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	// Windows abbreviates well-known SIDs, including the runner's local administrator.
	_, actualACEs, _ := strings.Cut(sddl, "(")
	_, expectedACEs, _ := strings.Cut(expected.String(), "(")
	if strings.Count(sddl, "(") != 2 || actualACEs != expectedACEs {
		t.Fatalf("unexpected credential access: %s", sddl)
	}
}

func TestWindowsCredentialAccess(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for i := 0; i < 2; i++ {
		if err := WithLock(func() error { return (&Config{Token: "csk_mcp_fixture"}).Save() }); err != nil {
			t.Fatal(err)
		}
		assertPrivateDACL(t, filepath.Dir(Path()), true)
		assertPrivateDACL(t, Path(), false)
		assertPrivateDACL(t, Path()+".lock", false)
	}
	// A legacy file with broad permissions must be restricted on load as well.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(Path(), windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	assertPrivateDACL(t, Path(), false)
	path := filepath.Join(t.TempDir(), "client.json")
	if err := os.WriteFile(path, []byte("old"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivateFile(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	assertPrivateDACL(t, path, false)
}
