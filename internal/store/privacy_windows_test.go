//go:build windows

package store

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/ashwath-ramesh/gmail-to-duckdb/internal/privfile"
	"golang.org/x/sys/windows"
)

func makePermissiveFile(path string) error {
	return grantWorld(path, false)
}

func makePermissiveDir(path string) error {
	return grantWorld(path, true)
}

func dbParent(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "db")
	if err := privfile.MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestOpenRejectsUnsafeParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "custom")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := makePermissiveDir(parent); err != nil {
		t.Fatal(err)
	}
	before := snapshotDir(t, parent)
	path := filepath.Join(parent, "mail.duckdb")
	db, err := Open(path)
	if err == nil {
		_ = db.Close()
		t.Fatal("expected unsafe parent reject")
	}
	if db != nil {
		t.Fatal("db on failure")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "private") {
		t.Fatalf("want actionable parent error, got %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("must not create db in unsafe parent")
	}
	if snapshotDir(t, parent) != before {
		t.Fatal("custom parent changed")
	}
}

func assertDirPrivate(t *testing.T, dir string) {
	t.Helper()
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		t.Fatalf("not a directory: %s", info.Mode())
	}
	if err := assertOwnerSystemACL(dir); err != nil {
		t.Fatal(err)
	}
}

func snapshotDir(t *testing.T, dir string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return sd.String()
}

func TestOpenRejectsInheritOnlyWorldParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "custom")
	if err := privfile.MkdirPrivate(parent); err != nil {
		t.Fatal(err)
	}
	if err := grantPrivateSelfWorldInheritOnly(parent); err != nil {
		t.Fatal(err)
	}
	before := snapshotDir(t, parent)
	path := filepath.Join(parent, "mail.duckdb")
	db, err := Open(path)
	if err == nil {
		_ = db.Close()
		t.Fatal("expected inherit-only world parent reject")
	}
	if db != nil {
		t.Fatal("db on failure")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("must not create db when parent inheritance is unsafe")
	}
	if snapshotDir(t, parent) != before {
		t.Fatal("custom parent changed")
	}
}

func TestOpenRejectsNonInheritingParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "custom")
	if err := privfile.MkdirPrivate(parent); err != nil {
		t.Fatal(err)
	}
	if err := grantSelfOnlyPrivate(parent); err != nil {
		t.Fatal(err)
	}
	before := snapshotDir(t, parent)
	path := filepath.Join(parent, "mail.duckdb")
	db, err := Open(path)
	if err == nil {
		_ = db.Close()
		t.Fatal("expected non-inheriting parent reject")
	}
	if db != nil {
		t.Fatal("db on failure")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("must not create db when parent does not inherit")
	}
	if snapshotDir(t, parent) != before {
		t.Fatal("custom parent changed")
	}
}

func TestOpenWindowsDBUsesPrivateACL(t *testing.T) {
	path := filepath.Join(privateDir(t), "mail.duckdb")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := privfile.Check(path); err != nil {
		t.Fatal(err)
	}
	if err := assertOwnerSystemACL(path); err != nil {
		t.Fatal(err)
	}
	assertDirPrivate(t, path+".tmp")
}

func grantSelfOnlyPrivate(dir string) error {
	return grantUserInherit(dir, 0)
}

func grantPrivateSelfWorldInheritOnly(dir string) error {
	user, err := currentUserSID()
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	world, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		return err
	}
	var pinner runtime.Pinner
	defer pinner.Unpin()
	pinner.Pin(user)
	pinner.Pin(system)
	pinner.Pin(world)
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(user),
			},
		},
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(system),
			},
		},
		{
			AccessPermissions: windows.GENERIC_READ,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT | windows.INHERIT_ONLY_ACE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(world),
			},
		},
	}, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func grantUserInherit(dir string, inherit uint32) error {
	user, err := currentUserSID()
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	var pinner runtime.Pinner
	defer pinner.Unpin()
	pinner.Pin(user)
	pinner.Pin(system)
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inherit,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(user),
			},
		},
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inherit,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(system),
			},
		},
	}, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func grantWorld(path string, dir bool) error {
	world, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		return err
	}
	user, err := currentUserSID()
	if err != nil {
		return err
	}
	var pinner runtime.Pinner
	defer pinner.Unpin()
	pinner.Pin(world)
	pinner.Pin(user)
	inherit := uint32(0)
	if dir {
		inherit = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	entries := []windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inherit,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(user),
			},
		},
		{
			AccessPermissions: windows.GENERIC_READ | windows.GENERIC_WRITE,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inherit,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(world),
			},
		},
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func currentUserSID() (*windows.SID, error) {
	tok := windows.GetCurrentProcessToken()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return windows.StringToSid(tu.User.Sid.String())
}

func tokenOwnerSID() (*windows.SID, error) {
	tok := windows.GetCurrentProcessToken()
	var n uint32
	err := windows.GetTokenInformation(tok, windows.TokenOwner, nil, 0, &n)
	if n == 0 {
		return nil, err
	}
	buf := make([]byte, n)
	if err := windows.GetTokenInformation(tok, windows.TokenOwner, &buf[0], uint32(len(buf)), &n); err != nil {
		return nil, err
	}
	type tokenOwner struct {
		Owner *windows.SID
	}
	to := (*tokenOwner)(unsafe.Pointer(&buf[0]))
	if to.Owner == nil {
		return nil, fmt.Errorf("token owner unavailable")
	}
	return windows.StringToSid(to.Owner.String())
}

func assertOwnerSystemACL(path string) error {
	user, err := currentUserSID()
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	def, err := tokenOwnerSID()
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner == nil {
		return fmt.Errorf("owner is not current user")
	}
	if !owner.Equals(user) && !owner.Equals(def) {
		return fmt.Errorf("owner is not current user")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("missing dacl")
	}
	seenAccess := false
	seenSystem := false
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch {
		case sid.Equals(user), sid.Equals(def):
			seenAccess = true
		case sid.Equals(system):
			seenSystem = true
		default:
			return fmt.Errorf("unexpected trustee %s", sid)
		}
	}
	if !seenAccess || !seenSystem {
		return fmt.Errorf("missing current-user or SYSTEM ACE")
	}
	return nil
}
