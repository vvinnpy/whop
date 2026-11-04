package secrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

// fastParams keeps tests quick while still exercising the real Argon2id path.
var fastParams = Params{Time: 1, Memory: 64, Threads: 1}

func createTestVault(t *testing.T, path, password string) *Vault {
	t.Helper()
	v, err := CreateWithParams(path, password, fastParams)
	if err != nil {
		t.Fatalf("CreateWithParams: %v", err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return v
}

func TestCreateOpenSaveAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault")
	v := createTestVault(t, path, "correct horse battery staple")

	if err := v.Set("db/password", "p@ssw0rd"); err != nil {
		t.Fatalf("Set db/password: %v", err)
	}
	if err := v.Set("中文/密码", "带空格的值"); err != nil {
		t.Fatalf("Set unicode: %v", err)
	}
	if got, ok, err := v.Get("db/password"); err != nil || !ok || got != "p@ssw0rd" {
		t.Fatalf("Get before Save = %q, %v, %v", got, ok, err)
	}
	if err := v.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := v.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat vault: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("vault mode = %#o, want 0600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile vault: %v", err)
	}
	for _, plaintext := range []string{"correct horse battery staple", "p@ssw0rd", "带空格的值"} {
		if bytes.Contains(raw, []byte(plaintext)) {
			t.Fatalf("vault contains plaintext %q", plaintext)
		}
	}

	reopened, err := Open(path, "correct horse battery staple")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reopened.Close()
	if got, ok, err := reopened.Get("db/password"); err != nil || !ok || got != "p@ssw0rd" {
		t.Fatalf("Get after reopen = %q, %v, %v", got, ok, err)
	}
	if got, ok, err := reopened.Get("中文/密码"); err != nil || !ok || got != "带空格的值" {
		t.Fatalf("Get unicode after reopen = %q, %v, %v", got, ok, err)
	}
	names, err := reopened.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	wantNames := []string{"db/password", "中文/密码"}
	if strings.Join(names, "\x00") != strings.Join(wantNames, "\x00") {
		t.Fatalf("List = %#v, want %#v", names, wantNames)
	}
}

func TestWrongPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault")
	v := createTestVault(t, path, "right-password")
	if err := v.Set("x", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(path, "wrong-password"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("Open wrong password error = %v, want ErrInvalidPassword", err)
	}
}

func TestSetDeleteSaveAndList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault")
	v := createTestVault(t, path, "password")
	if err := v.Set("b", "two"); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("a", "one"); err != nil {
		t.Fatal(err)
	}
	names, err := v.List()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(names, ","), "a,b"; got != want {
		t.Fatalf("List = %q, want %q", got, want)
	}
	if err := v.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := v.Get("a"); err != nil || ok {
		t.Fatalf("Get deleted = %v, %v", ok, err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, "password")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, ok, err := reopened.Get("a"); err != nil || ok {
		t.Fatalf("Get deleted after reopen = %v, %v", ok, err)
	}
	if got, ok, err := reopened.Get("b"); err != nil || !ok || got != "two" {
		t.Fatalf("Get b after reopen = %q, %v, %v", got, ok, err)
	}
}

func TestCreateRefusesExistingVault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault")
	first := createTestVault(t, path, "first")
	if err := first.Set("x", "one"); err != nil {
		t.Fatal(err)
	}
	if err := first.Save(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := CreateWithParams(path, "second", fastParams); err == nil {
		t.Fatal("CreateWithParams overwrote an existing vault")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed Create changed the existing vault")
	}
}

func TestInvalidInputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault")
	if _, err := CreateWithParams("", "password", fastParams); err == nil {
		t.Error("Create accepted empty path")
	}
	if _, err := CreateWithParams(path, "", fastParams); err == nil {
		t.Error("Create accepted empty password")
	}
	if _, err := CreateWithParams(path, "password", Params{}); err == nil {
		t.Error("Create accepted invalid KDF params")
	}
	if _, err := CreateWithParams(path, "password", Params{Time: 1, Memory: 7, Threads: 1}); err == nil {
		t.Error("Create accepted undersized KDF memory")
	}
	if _, err := Open("", "password"); err == nil {
		t.Error("Open accepted empty path")
	}
	if _, err := Open(path, ""); err == nil {
		t.Error("Open accepted empty password")
	}

	v := createTestVault(t, path, "password")
	if err := v.Set("", "value"); err == nil {
		t.Error("Set accepted empty name")
	}
	if err := v.Set(" \t", "value"); err == nil {
		t.Error("Set accepted whitespace-only name")
	}
	if err := v.Set("name", ""); err == nil {
		t.Error("Set accepted empty secret")
	}
	if err := v.Delete(""); err == nil {
		t.Error("Delete accepted empty name")
	}
	if _, _, err := v.Get(""); err == nil {
		t.Error("Get accepted empty name")
	}
	if err := v.Delete("missing"); err != nil {
		t.Errorf("Delete missing name: %v", err)
	}
}

func TestCloseRejectsOperations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault")
	v := createTestVault(t, path, "password")
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	checks := []struct {
		name string
		err  error
	}{
		{"Get", func() error { _, _, err := v.Get("x"); return err }()},
		{"List", func() error { _, err := v.List(); return err }()},
		{"Set", v.Set("x", "y")},
		{"Delete", v.Delete("x")},
		{"Save", v.Save()},
	}
	for _, check := range checks {
		if !errors.Is(check.err, ErrLocked) {
			t.Errorf("%s after Close = %v, want ErrLocked", check.name, check.err)
		}
	}
}

func TestCorruptFiles(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]byte) []byte
		want   error
	}{
		{
			name: "bad magic",
			mutate: func(data []byte) []byte {
				data[0] ^= 0xff
				return data
			},
			want: ErrInvalidVault,
		},
		{
			name: "bad version",
			mutate: func(data []byte) []byte {
				data[8] = 0
				data[9] = 2
				return data
			},
			want: ErrInvalidVault,
		},
		{
			name: "bad reserved byte",
			mutate: func(data []byte) []byte {
				data[19] = 1
				return data
			},
			want: ErrInvalidVault,
		},
		{
			name: "bad salt length",
			mutate: func(data []byte) []byte {
				data[20] = 1
				return data
			},
			want: ErrInvalidVault,
		},
		{
			name: "truncated header",
			mutate: func(data []byte) []byte {
				return data[:headerSize-1]
			},
			want: ErrInvalidVault,
		},
		{
			name: "truncated ciphertext",
			mutate: func(data []byte) []byte {
				return data[:headerSize+1]
			},
			want: ErrInvalidVault,
		},
		{
			name: "tampered ciphertext",
			mutate: func(data []byte) []byte {
				data[len(data)-1] ^= 0xff
				return data
			},
			want: ErrInvalidPassword,
		},
		{
			name: "tampered authenticated header",
			mutate: func(data []byte) []byte {
				data[13] = 2
				return data
			},
			want: ErrInvalidPassword,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "vault")
			v := createTestVault(t, path, "password")
			if err := v.Set("secret", "value"); err != nil {
				t.Fatal(err)
			}
			if err := v.Save(); err != nil {
				t.Fatal(err)
			}
			if err := v.Close(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = tt.mutate(data)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(path, "password"); !errors.Is(err, tt.want) {
				t.Fatalf("Open error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestSpecialCharacters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault")
	v := createTestVault(t, path, "päss 中文\npassword")
	name := "服务器/数据库\nprimary"
	secret := "pa ss\t中文\n\"quotes\" \\ slash"
	if err := v.Set(name, secret); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, "päss 中文\npassword")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, ok, err := reopened.Get(name); err != nil || !ok || got != secret {
		t.Fatalf("Get special value = %q, %v, %v", got, ok, err)
	}
}

func TestSaveUsesAtomicFileAndNoTemporaryFilesRemain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault")
	v := createTestVault(t, path, "password")
	if err := v.Set("x", "one"); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("x", "two"); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	v.Close()

	matches, err := filepath.Glob(filepath.Join(dir, ".whop-vault-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary vault files remain: %v", matches)
	}
	reopened, err := Open(path, "password")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, ok, err := reopened.Get("x"); err != nil || !ok || got != "two" {
		t.Fatalf("Get after atomic saves = %q, %v, %v", got, ok, err)
	}
}

func TestSaveWaitsForFileLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault")
	v := createTestVault(t, path, "password")
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}

	lock := flock.New(path+".lock", flock.SetPermissions(0o600))
	if err := lock.Lock(); err != nil {
		t.Fatalf("acquire test lock: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- v.Save()
	}()
	select {
	case err := <-done:
		_ = lock.Unlock()
		t.Fatalf("Save completed while lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := lock.Unlock(); err != nil {
		t.Fatalf("release test lock: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Save after lock release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Save did not finish after lock release")
	}
}

func TestConcurrentSaveDoesNotProduceCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault")
	v := createTestVault(t, path, "password")
	if err := v.Set("x", "zero"); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := v.Set("x", string(rune('a'+i))); err != nil {
				errs <- err
				return
			}
			errs <- v.Save()
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Save: %v", err)
		}
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, "password")
	if err != nil {
		t.Fatalf("Open after concurrent saves: %v", err)
	}
	defer reopened.Close()
	if _, ok, err := reopened.Get("x"); err != nil || !ok {
		t.Fatalf("Get after concurrent saves = %v, %v", ok, err)
	}
}

func TestDefaultPath(t *testing.T) {
	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if path == "" || !strings.HasSuffix(path, filepath.Join("whop", "secrets.vault")) {
		t.Fatalf("DefaultPath = %q", path)
	}
}

func TestDefaultParamsMeetBaseline(t *testing.T) {
	if defaultParams.Time != 3 || defaultParams.Memory != 64*1024 || defaultParams.Threads != 2 {
		t.Fatalf("default KDF params = %+v", defaultParams)
	}
}
