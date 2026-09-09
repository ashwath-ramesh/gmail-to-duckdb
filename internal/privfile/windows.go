//go:build windows

package privfile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func createPrivateTemp(path string) (*os.File, error) {
	sa, err := privateSA(false)
	if err != nil {
		return nil, err
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_WRITE,
		0,
		sa,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

type fileRenameInfoEx struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

func replaceFile(from, to string) error {
	src, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(
		src,
		windows.DELETE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	to, err = filepath.Abs(to)
	if err != nil {
		return err
	}
	dest, err := windows.UTF16FromString(to)
	if err != nil {
		return err
	}
	nameBytes := (len(dest) - 1) * 2
	n := int(unsafe.Offsetof(fileRenameInfoEx{}.FileName)) + len(dest)*2
	words := make([]uint64, (n+7)/8)
	info := (*fileRenameInfoEx)(unsafe.Pointer(&words[0]))
	info.Flags = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	info.FileNameLength = uint32(nameBytes)
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&info.FileName[0])), len(dest)), dest)
	return windows.SetFileInformationByHandle(h, windows.FileRenameInfoEx, (*byte)(unsafe.Pointer(&words[0])), uint32(n))
}

func syncDir(string) error {
	return nil
}

func readPrivate(path string) ([]byte, error) {
	h, err := openPath(path, windows.GENERIC_READ|windows.READ_CONTROL|windows.WRITE_DAC)
	if err != nil {
		return nil, err
	}
	owned := false
	defer func() {
		if !owned {
			_ = windows.CloseHandle(h)
		}
	}()
	if err := verifyOwnerRegularHandle(h); err != nil {
		return nil, err
	}
	if err := checkHandleACL(h); err != nil {
		if herr := hardenHandle(h); herr != nil {
			return nil, fmt.Errorf("tighten permissions: %w", herr)
		}
		if err := checkHandleACL(h); err != nil {
			return nil, err
		}
	}
	f := os.NewFile(uintptr(h), path)
	owned = true
	defer f.Close()
	return io.ReadAll(f)
}

func checkPrivate(path string) error {
	h, err := openPath(path, windows.GENERIC_READ|windows.READ_CONTROL)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	if err := verifyOwnerRegularHandle(h); err != nil {
		return err
	}
	return checkHandleACL(h)
}

func hardenPath(path string) error {
	h, err := openPath(path, windows.READ_CONTROL|windows.WRITE_DAC)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	if err := verifyOwnerRegularHandle(h); err != nil {
		return err
	}
	return hardenHandle(h)
}

func lockDown() {}

func checkDir(path string) error {
	h, err := openDir(path, windows.GENERIC_READ|windows.READ_CONTROL)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	if err := verifyOwnerDirHandle(h); err != nil {
		return err
	}
	return checkDirHandleACL(h)
}

func hardenDir(path string) error {
	h, err := openDir(path, windows.READ_CONTROL|windows.WRITE_DAC)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	if err := verifyOwnerDirHandle(h); err != nil {
		return err
	}
	return hardenDirHandle(h)
}

func openDir(path string, access uint32) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(
		p,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
}

func verifyOwnerDirHandle(h windows.Handle) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return fmt.Errorf("path is not a directory")
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("path is a symlink")
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return fmt.Errorf("directory owner is not the current user")
	}
	if err := ownerAllowed(owner); err != nil {
		return fmt.Errorf("directory owner is not the current user")
	}
	return nil
}

func hardenDirHandle(h windows.Handle) error {
	sa, err := privateSA(true)
	if err != nil {
		return err
	}
	dacl, _, err := sa.SecurityDescriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(
		h,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	)
}

func mkdirPrivate(dir string) error {
	dir = filepath.Clean(dir)
	if dir == "." || dir == "" {
		return nil
	}
	fi, err := os.Lstat(dir)
	if err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("not a directory")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(dir)
	if parent != dir {
		if err := mkdirPrivate(parent); err != nil {
			return err
		}
	}
	sa, err := privateSA(true)
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	if err := windows.CreateDirectory(p, sa); err != nil {
		if err == windows.ERROR_ALREADY_EXISTS {
			return nil
		}
		return err
	}
	return nil
}

func openPath(path string, access uint32) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(
		p,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
}

func verifyOwnerRegularHandle(h windows.Handle) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return fmt.Errorf("path is not a regular file")
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("path is a symlink")
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return fmt.Errorf("file owner is not the current user")
	}
	if err := ownerAllowed(owner); err != nil {
		return fmt.Errorf("file owner is not the current user")
	}
	return nil
}

func ownerAllowed(owner *windows.SID) error {
	user, err := currentUserSID()
	if err != nil {
		return err
	}
	if owner.Equals(user) {
		return nil
	}
	def, err := tokenOwnerSID()
	if err != nil {
		return err
	}
	if owner.Equals(def) {
		return nil
	}
	return fmt.Errorf("owner is not the current user or the process token owner")
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

func checkHandleACL(h windows.Handle) error {
	return inspectACL(h, false)
}

func checkDirHandleACL(h windows.Handle) error {
	return inspectACL(h, true)
}

func inspectACL(h windows.Handle, dir bool) error {
	user, system, def, err := aclPrincipalSIDs()
	if err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("file allows access to others")
	}
	seenAccess := false
	seenSystem := false
	fileInherit := false
	dirInherit := false
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("file allows access to others")
		}
		flags := ace.Header.AceFlags
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		allowed := sid.Equals(user) || sid.Equals(def) || sid.Equals(system)
		appliesToSelf := flags&windows.INHERIT_ONLY_ACE == 0
		toFiles := flags&windows.OBJECT_INHERIT_ACE != 0
		toDirs := flags&windows.CONTAINER_INHERIT_ACE != 0
		if dir && (toFiles || toDirs) && !allowed {
			return fmt.Errorf("directory grants inherited access to others")
		}
		if !dir && flags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if appliesToSelf && !allowed {
			return fmt.Errorf("file allows access to others")
		}
		switch {
		case sid.Equals(user), sid.Equals(def):
			if appliesToSelf {
				seenAccess = true
			}
			if toFiles && flags&windows.NO_PROPAGATE_INHERIT_ACE == 0 {
				fileInherit = true
			}
			if toDirs && flags&windows.NO_PROPAGATE_INHERIT_ACE == 0 {
				dirInherit = true
			}
		case sid.Equals(system):
			if appliesToSelf {
				seenSystem = true
			}
		}
	}
	if !seenAccess || !seenSystem {
		return fmt.Errorf("file allows access to others")
	}
	if dir && (!fileInherit || !dirInherit) {
		return fmt.Errorf("directory does not inherit current-user protection to files and subdirectories")
	}
	return nil
}

func aclPrincipalSIDs() (user, system, def *windows.SID, err error) {
	user, err = currentUserSID()
	if err != nil {
		return nil, nil, nil, err
	}
	system, err = windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, nil, nil, err
	}
	def, err = tokenOwnerSID()
	if err != nil {
		return nil, nil, nil, err
	}
	return user, system, def, nil
}

func hardenHandle(h windows.Handle) error {
	sa, err := privateSA(false)
	if err != nil {
		return err
	}
	dacl, _, err := sa.SecurityDescriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(
		h,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	)
}

func privateSA(dir bool) (*windows.SecurityAttributes, error) {
	user, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	inherit := ""
	if dir {
		inherit = "OICI"
	}
	sddl := "O:" + user.String() + "D:P(A;" + inherit + ";FA;;;SY)(A;" + inherit + ";FA;;;" + user.String() + ")"
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}, nil
}

func currentUserSID() (*windows.SID, error) {
	tok := windows.GetCurrentProcessToken()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return windows.StringToSid(tu.User.Sid.String())
}
