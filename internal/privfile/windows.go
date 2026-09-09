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

func replaceFile(from, to string) error {
	src, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	dst, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(src, dst, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
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
		windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE,
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
	user, err := currentUserSID()
	if err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(user) {
		return fmt.Errorf("file owner is not the current user")
	}
	return nil
}

func checkHandleACL(h windows.Handle) error {
	user, err := currentUserSID()
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
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
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("file allows access to others")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid.Equals(user) || sid.Equals(system) {
			continue
		}
		return fmt.Errorf("file allows access to others")
	}
	return nil
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
