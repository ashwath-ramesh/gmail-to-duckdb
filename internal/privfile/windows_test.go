//go:build windows

package privfile

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func assertPrivate(t *testing.T, path string) {
	t.Helper()
	if err := Check(path); err != nil {
		t.Fatal(err)
	}
	if err := assertOwnerSystemACL(path); err != nil {
		t.Fatal(err)
	}
}

func makePermissive(path string) error {
	return grantWorldRead(path)
}

func makeDirPermissive(path string) error {
	return grantWorldInherit(path)
}

func assertStillPermissive(t *testing.T, path string) {
	t.Helper()
	if err := Check(path); err == nil {
		t.Fatal("file became private")
	}
}

func makeDirNotCreatable(dir string) error {
	out, err := exec.Command("icacls", dir, "/deny", "Everyone:(WD,AD)").CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls deny: %v: %s", err, out)
	}
	return nil
}

func restoreDirCreatable(dir string) error {
	out, err := exec.Command("icacls", dir, "/remove:d", "Everyone").CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls restore: %v: %s", err, out)
	}
	return nil
}

func snapshotParent(t *testing.T, dir string) string {
	t.Helper()
	s, err := aclSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func assertParentUnchanged(t *testing.T, dir string, before string) {
	t.Helper()
	got := snapshotParent(t, dir)
	if got != before {
		t.Fatalf("parent ACL changed")
	}
}

func assertNewAppDirPrivate(t *testing.T, dir string) {
	t.Helper()
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("not a directory")
	}
	if err := CheckDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := assertProtectedDirACL(dir); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(dir, "child.secret")
	if err := os.WriteFile(child, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Check(child); err != nil {
		t.Fatalf("inherited child not private: %v", err)
	}
	if err := assertOwnerSystemACL(child); err != nil {
		t.Fatal(err)
	}
}

func TestCheckDirRejectsInheritOnlyWorld(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if err := MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}
	if err := grantPrivateSelfWorldInheritOnly(dir); err != nil {
		t.Fatal(err)
	}
	if err := CheckDir(dir); err == nil {
		t.Fatal("expected inherit-only world reject")
	}
}

func TestCheckDirRejectsNoInherit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if err := MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}
	if err := grantSelfOnlyPrivate(dir); err != nil {
		t.Fatal(err)
	}
	if err := CheckDir(dir); err == nil {
		t.Fatal("expected missing inherit reject")
	}
}

func TestCheckDirRejectsFileOnlyInherit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if err := MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}
	if err := grantUserInherit(dir, windows.OBJECT_INHERIT_ACE|windows.NO_PROPAGATE_INHERIT_ACE); err != nil {
		t.Fatal(err)
	}
	if err := CheckDir(dir); err == nil {
		t.Fatal("expected file-only inherit reject")
	}
}

func TestCheckDirRejectsNoPropagateInherit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if err := MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}
	if err := grantUserInherit(dir, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT|windows.NO_PROPAGATE_INHERIT_ACE); err != nil {
		t.Fatal(err)
	}
	if err := CheckDir(dir); err == nil {
		t.Fatal("expected no-propagate inherit reject")
	}
}

func TestCheckSucceedsWhileExclusiveWriteHandleHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.json")
	if err := Write(path, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	f, err := openAppWriteExclusive(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := Check(path); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceWhileReadHandleHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.json")
	old := []byte(`{"v":"old-complete","pad":"` + strings.Repeat("A", 64) + `"}`)
	new := []byte(`{"v":"new-complete","pad":"` + strings.Repeat("B", 64) + `"}`)
	if err := Write(path, old); err != nil {
		t.Fatal(err)
	}
	f, err := openAppRead(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := Write(path, new); err != nil {
		t.Fatal(err)
	}
	gotOld, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotOld, old) {
		t.Fatalf("held handle %s", gotOld)
	}
	gotNew, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotNew, new) {
		t.Fatalf("new read %s", gotNew)
	}
	assertPrivate(t, path)
}

func TestWriteUsesProtectedCurrentUserACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.json")
	if err := Write(path, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := assertOwnerSystemACL(path); err != nil {
		t.Fatal(err)
	}
}

func TestCheckRejectsInheritedWorldACL(t *testing.T) {
	dir := t.TempDir()
	if err := grantWorldInherit(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "secret.json")
	if err := os.WriteFile(path, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Check(path); err == nil {
		t.Fatal("inherited world ACL passed")
	}
	if err := Write(path, []byte(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	if err := Check(path); err != nil {
		t.Fatal(err)
	}
}

func grantSelfOnlyPrivate(dir string) error {
	return grantUserInherit(dir, 0)
}

func grantPrivateSelfWorldInheritOnly(dir string) error {
	user, system, world, err := dirACLSIDs()
	if err != nil {
		return err
	}
	var pinner runtime.Pinner
	defer pinner.Unpin()
	pinner.Pin(user)
	pinner.Pin(system)
	pinner.Pin(world)
	return setDirACL(dir, []windows.EXPLICIT_ACCESS{
		aceFor(user, windows.GENERIC_ALL, 0, windows.TRUSTEE_IS_USER),
		aceFor(system, windows.GENERIC_ALL, 0, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
		aceFor(world, windows.GENERIC_READ, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT|windows.INHERIT_ONLY_ACE, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
	})
}

func grantUserInherit(dir string, inherit uint32) error {
	user, system, _, err := dirACLSIDs()
	if err != nil {
		return err
	}
	var pinner runtime.Pinner
	defer pinner.Unpin()
	pinner.Pin(user)
	pinner.Pin(system)
	return setDirACL(dir, []windows.EXPLICIT_ACCESS{
		aceFor(user, windows.GENERIC_ALL, inherit, windows.TRUSTEE_IS_USER),
		aceFor(system, windows.GENERIC_ALL, inherit, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
	})
}

func dirACLSIDs() (user, system, world *windows.SID, err error) {
	user, err = currentUserSID()
	if err != nil {
		return nil, nil, nil, err
	}
	system, err = windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, nil, nil, err
	}
	world, err = windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		return nil, nil, nil, err
	}
	return user, system, world, nil
}

func aceFor(sid *windows.SID, access windows.ACCESS_MASK, inherit uint32, kind windows.TRUSTEE_TYPE) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: access,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inherit,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  kind,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}

func setDirACL(dir string, entries []windows.EXPLICIT_ACCESS) error {
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func grantWorldRead(path string) error {
	world, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		return err
	}
	var pinner runtime.Pinner
	defer pinner.Unpin()
	pinner.Pin(world)
	entries := []windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_READ | windows.GENERIC_WRITE,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(world),
		},
	}}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func grantWorldInherit(dir string) error {
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
	entries := []windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(user),
			},
		},
		{
			AccessPermissions: windows.GENERIC_READ | windows.GENERIC_WRITE,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
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
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func openAppRead(path string) (*os.File, error) {
	h, err := openPath(path, windows.GENERIC_READ|windows.READ_CONTROL)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

func openAppWriteExclusive(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

func aclSnapshot(path string) (string, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", err
	}
	return sd.String(), nil
}

func assertProtectedDirACL(path string) error {
	user, system, def, err := aclPrincipalSIDs()
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("dacl not protected")
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return fmt.Errorf("owner is not current user")
	}
	if err := ownerAllowed(owner); err != nil {
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

func assertOwnerSystemACL(path string) error {
	user, system, def, err := aclPrincipalSIDs()
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
	if err := ownerAllowed(owner); err != nil {
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
