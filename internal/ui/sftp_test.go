package ui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

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

	model.remotePath = "/home"
	cmd = updateSFTP(t, model, runeKey("h"))
	if cmd == nil {
		t.Fatal("h parent navigation did not load")
	}
	updateSFTP(t, model, cmd())
	if model.remotePath != "/" {
		t.Fatalf("remote path after h = %q", model.remotePath)
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
	return ansi.Strip(value)
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

func selectSFTPEntry(t *testing.T, model *SFTPModel, pane sftpPane, name string) {
	t.Helper()
	for index, entry := range model.entries(int(pane)) {
		if entry.Name == name {
			model.cursor[int(pane)] = index
			return
		}
	}
	t.Fatalf("%s entry %q not found in %#v", paneName(pane), name, model.entries(int(pane)))
}

func paneName(pane sftpPane) string {
	if pane == sftpRemote {
		return "remote"
	}
	return "local"
}

func TestSFTPSearchFiltersOnlyActivePane(t *testing.T) {
	model, remote, _ := newSFTPTestModel(t)
	remote.files["/home/notes.log"] = []byte("notes")
	remote.dirs["/home"]["notes.log"] = coreconnector.FileEntry{Name: "notes.log", Path: "/home/notes.log", Size: 5}
	reload := model.load()
	updateSFTP(t, model, reload())

	updateSFTP(t, model, runeKey("/"))
	updateSFTP(t, model, runeKey("upload"))
	updateSFTP(t, model, tea.KeyMsg{Type: tea.KeyTab})
	if model.searching {
		t.Fatal("tab did not leave search mode")
	}
	if model.active != int(sftpRemote) {
		t.Fatalf("tab did not switch pane, active=%d", model.active)
	}
	if got := model.queries[int(sftpLocal)]; got != "upload" {
		t.Fatalf("local query = %q", got)
	}
	if len(model.local) != 1 || model.local[0].Name != "upload.txt" {
		t.Fatalf("filtered local entries = %#v", model.local)
	}
	if len(model.remote) != 2 {
		t.Fatalf("remote pane was unexpectedly filtered: %#v", model.remote)
	}

	updateSFTP(t, model, runeKey("/"))
	updateSFTP(t, model, runeKey("download"))
	updateSFTP(t, model, runeKey("x"))
	if got := model.queries[int(sftpRemote)]; got != "downloadx" {
		t.Fatalf("remote query = %q", got)
	}
	if len(model.remote) != 0 {
		t.Fatalf("remote filtered entries = %#v", model.remote)
	}
	if len(model.local) != 1 || model.local[0].Name != "upload.txt" {
		t.Fatalf("local pane changed while searching remote: %#v", model.local)
	}

	updateSFTP(t, model, tea.KeyMsg{Type: tea.KeyBackspace})
	if len(model.remote) != 1 || model.remote[0].Name != "download.txt" {
		t.Fatalf("remote backspace filter = %#v", model.remote)
	}
	updateSFTP(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.searching {
		t.Fatal("esc did not leave search mode")
	}
	if got := model.queries[int(sftpLocal)]; got != "upload" {
		t.Fatalf("local query lost after pane switch: %q", got)
	}
	_ = model.Close()
}

func TestSFTPSearchModeSwallowsCommands(t *testing.T) {
	model, remote, _ := newSFTPTestModel(t)
	updateSFTP(t, model, runeKey("/"))
	if cmd := updateSFTP(t, model, runeKey("udq?")); cmd != nil {
		t.Fatalf("typing commands in search mode returned a command: %T", cmd())
	}
	if got := model.queries[int(sftpLocal)]; got != "udq?" {
		t.Fatalf("query = %q", got)
	}
	if !model.searching {
		t.Fatal("search mode exited while typing")
	}
	if model.busy {
		t.Fatal("typing triggered a transfer")
	}
	if len(remote.files) != 1 {
		t.Fatalf("remote files changed: %#v", remote.files)
	}

	updateSFTP(t, model, tea.KeyMsg{Type: tea.KeyCtrlU})
	if got := model.queries[int(sftpLocal)]; got != "" {
		t.Fatalf("ctrl+u did not clear query: %q", got)
	}
	if !model.searching {
		t.Fatal("ctrl+u should stay in search mode")
	}
	_ = model.Close()
}

func TestSFTPPageKeysMoveOnlyActivePane(t *testing.T) {
	model, remote, local := newSFTPTestModel(t)
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("local-%02d.txt", i)
		if err := os.WriteFile(filepath.Join(local, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 6; i++ {
		name := fmt.Sprintf("remote-%02d.txt", i)
		remotePath := path.Join("/home", name)
		remote.files[remotePath] = []byte(name)
		remote.dirs["/home"][name] = coreconnector.FileEntry{Name: name, Path: remotePath, Size: int64(len(name))}
	}
	model.height = 10
	reload := model.load()
	updateSFTP(t, model, reload())

	model.cursor[int(sftpLocal)] = 0
	model.cursor[int(sftpRemote)] = 0
	pageSize := model.entryPageSize()
	if pageSize < 2 {
		t.Fatalf("page size too small: %d", pageSize)
	}
	if len(model.local) <= pageSize {
		t.Fatalf("test needs more local entries than one page: entries=%d page=%d", len(model.local), pageSize)
	}

	updateSFTP(t, model, tea.KeyMsg{Type: tea.KeyCtrlD})
	if model.cursor[int(sftpLocal)] != pageSize {
		t.Fatalf("local cursor after page down = %d, want %d", model.cursor[int(sftpLocal)], pageSize)
	}
	if model.cursor[int(sftpRemote)] != 0 {
		t.Fatalf("remote cursor moved while local pane active: %d", model.cursor[int(sftpRemote)])
	}
	updateSFTP(t, model, tea.KeyMsg{Type: tea.KeyCtrlU})
	if model.cursor[int(sftpLocal)] != 0 {
		t.Fatalf("local cursor after page up = %d", model.cursor[int(sftpLocal)])
	}

	updateSFTP(t, model, tea.KeyMsg{Type: tea.KeyTab})
	updateSFTP(t, model, tea.KeyMsg{Type: tea.KeyCtrlD})
	if model.cursor[int(sftpRemote)] != min(pageSize, len(model.remote)-1) {
		t.Fatalf("remote cursor after page down = %d", model.cursor[int(sftpRemote)])
	}
	if model.cursor[int(sftpLocal)] != 0 {
		t.Fatalf("local cursor moved while remote pane active: %d", model.cursor[int(sftpLocal)])
	}
	_ = model.Close()
}

func TestSFTPDirectoryNavigationClearsOnlyActiveSearch(t *testing.T) {
	model, _, local := newSFTPTestModel(t)
	if err := os.Mkdir(filepath.Join(local, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	reload := model.load()
	updateSFTP(t, model, reload())

	updateSFTP(t, model, runeKey("/"))
	updateSFTP(t, model, runeKey("sub"))
	updateSFTP(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	updateSFTP(t, model, tea.KeyMsg{Type: tea.KeyTab})
	updateSFTP(t, model, runeKey("/"))
	updateSFTP(t, model, runeKey("download"))
	updateSFTP(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	updateSFTP(t, model, tea.KeyMsg{Type: tea.KeyTab})
	selectSFTPEntry(t, model, sftpLocal, "sub")

	cmd := updateSFTP(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("opening directory did not load")
	}
	updateSFTP(t, model, cmd())
	if model.localPath != filepath.Join(local, "sub") {
		t.Fatalf("local path = %q", model.localPath)
	}
	if got := model.queries[int(sftpLocal)]; got != "" {
		t.Fatalf("local query after navigation = %q", got)
	}
	if got := model.queries[int(sftpRemote)]; got != "download" {
		t.Fatalf("remote query after local navigation = %q", got)
	}
	if len(model.remote) != 1 || model.remote[0].Name != "download.txt" {
		t.Fatalf("remote filter lost after local navigation: %#v", model.remote)
	}
	_ = model.Close()
}

func TestSFTPTransferUsesFilteredSelection(t *testing.T) {
	model, remote, local := newSFTPTestModel(t)
	if err := os.WriteFile(filepath.Join(local, "other.txt"), []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	reload := model.load()
	updateSFTP(t, model, reload())
	updateSFTP(t, model, runeKey("/"))
	updateSFTP(t, model, runeKey("upload"))
	updateSFTP(t, model, tea.KeyMsg{Type: tea.KeyEsc})

	cmd := updateSFTP(t, model, runeKey("u"))
	if cmd == nil {
		t.Fatal("upload command missing")
	}
	updateSFTP(t, model, cmd())
	if got := string(remote.files["/home/upload.txt"]); got != "from local" {
		t.Fatalf("uploaded = %q", got)
	}
	if _, ok := remote.files["/home/other.txt"]; ok {
		t.Fatal("unselected file was uploaded")
	}
	_ = model.Close()
}

func TestSFTPFooterAndHelpShowShortcuts(t *testing.T) {
	model, _, _ := newSFTPTestModel(t)
	view := ansiStrip(model.View())
	for _, want := range []string{"tab: switch", "ctrl+d/u: page", "/: search", "backspace: parent", "u/d: upload/download", "?: help"} {
		if !strings.Contains(view, want) {
			t.Fatalf("browse footer missing %q:\n%s", want, view)
		}
	}

	updateSFTP(t, model, runeKey("/"))
	view = ansiStrip(model.View())
	for _, want := range []string{"type: search active pane", "tab/esc: apply", "ctrl+u: clear"} {
		if !strings.Contains(view, want) {
			t.Fatalf("search footer missing %q:\n%s", want, view)
		}
	}
	updateSFTP(t, model, tea.KeyMsg{Type: tea.KeyEsc})

	updateSFTP(t, model, runeKey("?"))
	view = ansiStrip(model.View())
	for _, want := range []string{"NAVIGATION", "SEARCH", "TRANSFER", "ctrl+d/u", "backspace/left/h", "upload selected local file", "download selected remote file", "close help"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help view missing %q:\n%s", want, view)
		}
	}
	updateSFTP(t, model, runeKey("q"))
	if model.help {
		t.Fatal("q did not close help")
	}
	_ = model.Close()
}

func TestSFTPViewFitsNarrowTerminal(t *testing.T) {
	model, _, _ := newSFTPTestModel(t)
	model.width = 40
	model.height = 20
	view := ansiStrip(model.View())
	for _, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > 40 {
			t.Fatalf("line width %d exceeds terminal width:\n%s", width, view)
		}
	}
	_ = model.Close()
}
