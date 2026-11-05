package ui

import (
	"bytes"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	coreconnector "whop/internal/connector"
	"whop/internal/sshconfig"
)

type fakeFileTransfer struct {
	dirs   map[string]map[string]coreconnector.FileEntry
	files  map[string][]byte
	closed bool
}

func newFakeFileTransfer() *fakeFileTransfer {
	return &fakeFileTransfer{dirs: map[string]map[string]coreconnector.FileEntry{".": {}}, files: map[string][]byte{}}
}

func (f *fakeFileTransfer) ReadDir(ctx context.Context, remotePath string) ([]coreconnector.FileEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries := f.dirs[path.Clean(remotePath)]
	result := make([]coreconnector.FileEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry)
	}
	sortEntries(result)
	return result, nil
}

func (f *fakeFileTransfer) Stat(ctx context.Context, remotePath string) (coreconnector.FileEntry, error) {
	if err := ctx.Err(); err != nil {
		return coreconnector.FileEntry{}, err
	}
	remotePath = path.Clean(remotePath)
	if data, ok := f.files[remotePath]; ok {
		return coreconnector.FileEntry{Name: path.Base(remotePath), Path: remotePath, Size: int64(len(data))}, nil
	}
	if _, ok := f.dirs[remotePath]; ok {
		return coreconnector.FileEntry{Name: path.Base(remotePath), Path: remotePath, Directory: true}, nil
	}
	return coreconnector.FileEntry{}, os.ErrNotExist
}

func (f *fakeFileTransfer) Open(ctx context.Context, remotePath string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, ok := f.files[path.Clean(remotePath)]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *fakeFileTransfer) Create(ctx context.Context, remotePath string) (io.WriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &fakeRemoteWriter{transfer: f, path: path.Clean(remotePath)}, nil
}

func (f *fakeFileTransfer) Mkdir(ctx context.Context, remotePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.dirs[path.Clean(remotePath)] = map[string]coreconnector.FileEntry{}
	return nil
}

func (f *fakeFileTransfer) Remove(ctx context.Context, remotePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	delete(f.files, path.Clean(remotePath))
	return nil
}

func (f *fakeFileTransfer) RemoveAll(ctx context.Context, remotePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	remotePath = path.Clean(remotePath)
	for key := range f.files {
		if key == remotePath || strings.HasPrefix(key, remotePath+"/") {
			delete(f.files, key)
		}
	}
	for key := range f.dirs {
		if key == remotePath || strings.HasPrefix(key, remotePath+"/") {
			delete(f.dirs, key)
		}
	}
	return nil
}

func (f *fakeFileTransfer) Rename(ctx context.Context, oldPath, newPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	oldPath, newPath = path.Clean(oldPath), path.Clean(newPath)
	data, ok := f.files[oldPath]
	if !ok {
		return os.ErrNotExist
	}
	delete(f.files, oldPath)
	f.files[newPath] = data
	return nil
}

func (f *fakeFileTransfer) Close() error {
	f.closed = true
	return nil
}

type fakeRemoteWriter struct {
	transfer *fakeFileTransfer
	path     string
	data     bytes.Buffer
	closed   bool
}

func (w *fakeRemoteWriter) Write(data []byte) (int, error) { return w.data.Write(data) }
func (w *fakeRemoteWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	w.transfer.files[w.path] = append([]byte(nil), w.data.Bytes()...)
	return nil
}

func newSFTPTestModel(t *testing.T) (*SFTPModel, *fakeFileTransfer, string) {
	t.Helper()
	local := t.TempDir()
	if err := os.WriteFile(filepath.Join(local, "upload.txt"), []byte("from local"), 0o600); err != nil {
		t.Fatal(err)
	}
	remote := newFakeFileTransfer()
	remote.files["/home/download.txt"] = []byte("from remote")
	remote.dirs["/home"] = map[string]coreconnector.FileEntry{
		"download.txt": {Name: "download.txt", Path: "/home/download.txt", Size: 11},
	}
	model := NewSFTP(SFTPOptions{
		Host:       sshconfig.Host{Alias: "prod-db"},
		LocalPath:  local,
		RemotePath: "/home",
		OpenRemote: func(context.Context, sshconfig.Host) (coreconnector.FileTransfer, error) {
			return remote, nil
		},
	})
	updateSFTP(t, model, model.Init()())
	return model, remote, local
}

func updateSFTP(t *testing.T, model *SFTPModel, msg tea.Msg) tea.Cmd {
	t.Helper()
	_, cmd := model.Update(msg)
	return cmd
}

func TestSFTPDualPaneLoadsBothSides(t *testing.T) {
	model, _, _ := newSFTPTestModel(t)
	view := ansiStrip(model.View())
	for _, want := range []string{"whop SFTP", "LOCAL", "REMOTE", "upload.txt", "download.txt"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if err := model.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSFTPUploadAndDownload(t *testing.T) {
	model, remote, local := newSFTPTestModel(t)
	cmd := updateSFTP(t, model, runeKey("u"))
	if cmd == nil {
		t.Fatal("upload command missing")
	}
	updateSFTP(t, model, cmd())
	if got := string(remote.files["/home/upload.txt"]); got != "from local" {
		t.Fatalf("uploaded = %q", got)
	}

	model.active = int(sftpRemote)
	for index, entry := range model.remote {
		if entry.Name == "download.txt" {
			model.cursor[int(sftpRemote)] = index
		}
	}
	cmd = updateSFTP(t, model, runeKey("d"))
	if cmd == nil {
		t.Fatal("download command missing")
	}
	updateSFTP(t, model, cmd())
	data, err := os.ReadFile(filepath.Join(local, "download.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "from remote" {
		t.Fatalf("downloaded = %q", data)
	}
	if err := model.Close(); err != nil {
		t.Fatal(err)
	}
	if !remote.closed {
		t.Fatal("remote transfer was not closed")
	}
}

func TestSFTPRefusesOverwrite(t *testing.T) {
	model, remote, _ := newSFTPTestModel(t)
	remote.files["/home/upload.txt"] = []byte("existing")
	cmd := updateSFTP(t, model, runeKey("u"))
	if cmd == nil {
		t.Fatal("upload command missing")
	}
	msg := cmd()
	if done, ok := msg.(sftpTransferDoneMsg); !ok || done.err == nil {
		t.Fatalf("expected overwrite error, got %#v", msg)
	}
	if got := string(remote.files["/home/upload.txt"]); got != "existing" {
		t.Fatalf("existing remote file changed: %q", got)
	}
	_ = model.Close()
}

func TestSFTPNavigationTabAndParent(t *testing.T) {
	model, _, _ := newSFTPTestModel(t)
	updateSFTP(t, model, tea.KeyMsg{Type: tea.KeyTab})
	if model.active != int(sftpRemote) {
		t.Fatal("tab did not switch pane")
	}
	cmd := updateSFTP(t, model, tea.KeyMsg{Type: tea.KeyBackspace})
	if cmd == nil {
		t.Fatal("parent navigation did not load")
	}
	updateSFTP(t, model, cmd())
	if model.remotePath != "/" {
		t.Fatalf("remote path = %q", model.remotePath)
	}
	_ = model.Close()
}

func TestCopyWithContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := copyWithContext(ctx, io.Discard, strings.NewReader("data"))
	if err == nil {
		t.Fatal("canceled copy succeeded")
	}
}

func ansiStrip(value string) string {
	return strings.NewReplacer("\x1b", "").Replace(value)
}

func TestSFTPMoveOpenDirectoryAndFormatting(t *testing.T) {
	model, remote, local := newSFTPTestModel(t)
	if err := os.Mkdir(filepath.Join(local, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	remote.dirs["/home/sub"] = map[string]coreconnector.FileEntry{}
	reload := model.load()
	updateSFTP(t, model, reload())
	model.cursor[int(sftpLocal)] = 0
	if len(model.local) == 0 || model.local[0].Name != "sub" || !model.local[0].Directory {
		t.Fatalf("local entries = %#v", model.local)
	}
	cmd := updateSFTP(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("opening a directory did not reload")
	}
	updateSFTP(t, model, cmd())
	if model.localPath != filepath.Join(local, "sub") {
		t.Fatalf("local path = %q", model.localPath)
	}
	model.move(-1)
	if model.cursor[int(sftpLocal)] != 0 {
		t.Fatal("cursor moved below zero")
	}
	model.move(100)
	if model.cursor[int(sftpLocal)] != max(0, len(model.local)-1) {
		t.Fatal("cursor moved past the end")
	}

	tests := map[int64]string{
		0:                  "0 B",
		1024:               "1.0 K",
		1024 * 1024:        "1.0 M",
		1024 * 1024 * 1024: "1.0 G",
	}
	for size, want := range tests {
		if got := fileSize(size); got != want {
			t.Fatalf("fileSize(%d) = %q, want %q", size, got, want)
		}
	}
	_ = model.Close()
}

func TestSortEntriesDirectoriesFirst(t *testing.T) {
	entries := []coreconnector.FileEntry{
		{Name: "zeta", Size: 1},
		{Name: "beta", Directory: true},
		{Name: "Alpha", Size: 2},
		{Name: "aardvark", Directory: true},
	}
	sortEntries(entries)
	got := []string{entries[0].Name, entries[1].Name, entries[2].Name, entries[3].Name}
	want := []string{"aardvark", "beta", "Alpha", "zeta"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("sorted = %#v, want %#v", got, want)
		}
	}
}
