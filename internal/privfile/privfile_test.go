package privfile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestWriteRelativePathFromWorkingDir(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := Write("secret.json", []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	assertPrivate(t, filepath.Join(dir, "secret.json"))
	got, err := os.ReadFile("secret.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"v":1}` {
		t.Fatalf("%s", got)
	}
	if err := Write("secret.json", []byte(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(filepath.Join(dir, "secret.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"v":2}` {
		t.Fatalf("%s", got)
	}
}

func TestWriteReplacesPermissiveFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(path, []byte(`{"v":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := makePermissive(path); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte(`{"v":"new"}`)); err != nil {
		t.Fatal(err)
	}
	assertPrivate(t, path)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"v":"new"}` {
		t.Fatalf("%s", b)
	}
}

func TestWriteRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	path := filepath.Join(dir, "secret.json")
	keep := []byte(`{"v":"keep"}`)
	if err := os.WriteFile(target, keep, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		skipIfNoSymlink(t, err)
	}
	if err := Write(path, []byte(`{"v":"new"}`)); err == nil {
		t.Fatal("expected symlink reject")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(keep) {
		t.Fatalf("target changed: %s", got)
	}
}

func TestWriteRejectsNonRegular(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte(`{"v":1}`)); err == nil {
		t.Fatal("expected non-regular reject")
	}
	assertNoTemp(t, dir)
}

func TestReadRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	path := filepath.Join(dir, "secret.json")
	if err := os.WriteFile(target, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		skipIfNoSymlink(t, err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("expected symlink reject")
	}
}

func TestReadAcceptsInheritedPrivateChild(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "app")
	if err := MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "secret.json")
	want := []byte(`{"v":1}`)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s", got)
	}
	assertPrivate(t, path)
}

func TestReadTightensPermissiveFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.json")
	want := []byte(`{"v":1}`)
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := makePermissive(path); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s", got)
	}
	assertPrivate(t, path)
}

func TestCheckDoesNotModifyPermissiveFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(path, []byte(`{"v":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := makePermissive(path); err != nil {
		t.Fatal(err)
	}
	if err := Check(path); err == nil {
		t.Fatal("expected permissive reject")
	}
	assertStillPermissive(t, path)
}

func TestFailedWriteLeavesPriorAndCleansTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.json")
	priorCanary := "CANARY_PRIOR_PAYLOAD_7f3a"
	nextCanary := "CANARY_REPLACEMENT_PAYLOAD_9c1d"
	old := []byte(`{"v":"` + priorCanary + `","pad":"` + strings.Repeat("A", 32) + `"}`)
	if err := Write(path, old); err != nil {
		t.Fatal(err)
	}
	if err := makeDirNotCreatable(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restoreDirCreatable(dir) })
	err := Write(path, []byte(`{"v":"`+nextCanary+`"}`))
	if err == nil {
		t.Fatal("expected replace failure")
	}
	if strings.Contains(err.Error(), priorCanary) || strings.Contains(err.Error(), nextCanary) {
		t.Fatalf("error leaked payload: %v", err)
	}
	if err := restoreDirCreatable(dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(old) {
		t.Fatalf("prior data lost: %s", got)
	}
	assertNoTemp(t, dir)
}

func TestAtomicReadersSeeWholeJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.json")
	old := []byte(`{"v":1,"pad":"` + strings.Repeat("A", 8192) + `"}`)
	new := []byte(`{"v":2,"pad":"` + strings.Repeat("B", 8192) + `"}`)
	if err := Write(path, old); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errc := make(chan error, 1)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 80; j++ {
				b, err := Read(path)
				if err != nil {
					report(errc, err)
					return
				}
				if !bytes.Equal(b, old) && !bytes.Equal(b, new) {
					report(errc, fmt.Errorf("unexpected payload len %d", len(b)))
					return
				}
			}
		}()
	}
	for i := 0; i < 40; i++ {
		if err := Write(path, new); err != nil {
			t.Fatal(err)
		}
		if err := Write(path, old); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	select {
	case err := <-errc:
		t.Fatal(err)
	default:
	}
}

func TestCheckDirRejectsPermissive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := makeDirPermissive(dir); err != nil {
		t.Fatal(err)
	}
	if err := CheckDir(dir); err == nil {
		t.Fatal("expected permissive reject")
	}
}

func TestHardenDirMakesPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := makeDirPermissive(dir); err != nil {
		t.Fatal(err)
	}
	if err := HardenDir(dir); err != nil {
		t.Fatal(err)
	}
	assertNewAppDirPrivate(t, dir)
}

func TestHardenDirRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	path := filepath.Join(root, "link")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		skipIfNoSymlink(t, err)
	}
	if err := HardenDir(path); err == nil {
		t.Fatal("expected symlink reject")
	}
	if err := CheckDir(path); err == nil {
		t.Fatal("expected symlink reject")
	}
	assertNewAppDirPrivate(t, target)
}

func TestHardenDirDoesNotChmodParent(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := makeDirPermissive(parent); err != nil {
		t.Fatal(err)
	}
	before := snapshotParent(t, parent)
	child := filepath.Join(parent, "app")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := makeDirPermissive(child); err != nil {
		t.Fatal(err)
	}
	if err := HardenDir(child); err != nil {
		t.Fatal(err)
	}
	assertParentUnchanged(t, parent, before)
	assertNewAppDirPrivate(t, child)
}

func TestMkdirPrivateDoesNotChmodParent(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	before := snapshotParent(t, parent)
	child := filepath.Join(parent, "app")
	if err := MkdirPrivate(child); err != nil {
		t.Fatal(err)
	}
	assertParentUnchanged(t, parent, before)
	assertNewAppDirPrivate(t, child)
}

func report(errc chan<- error, err error) {
	select {
	case errc <- err:
	default:
	}
}

func skipIfNoSymlink(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if runtime.GOOS == "windows" {
		t.Skipf("symlink: %v", err)
	}
	t.Fatal(err)
}

func assertNoTemp(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Fatalf("temp left: %s", e.Name())
		}
	}
}
