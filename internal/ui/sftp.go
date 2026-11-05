package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	coreconnector "whop/internal/connector"
	"whop/internal/sshconfig"
)

// SFTPOptions configures the dual-pane SFTP browser.
type SFTPOptions struct {
	Host       sshconfig.Host
	LocalPath  string
	RemotePath string
	OpenRemote func(context.Context, sshconfig.Host) (coreconnector.FileTransfer, error)
}

// SFTPModel is a keyboard-first local/remote file browser.
type SFTPModel struct {
	host       sshconfig.Host
	localPath  string
	remotePath string
	openRemote func(context.Context, sshconfig.Host) (coreconnector.FileTransfer, error)
	transfer   coreconnector.FileTransfer
	ctx        context.Context
	cancel     context.CancelFunc

	width  int
	height int
	active int
	busy   bool
	loaded bool
	cursor [2]int
	local  []coreconnector.FileEntry
	remote []coreconnector.FileEntry
	status string
	err    error
}

type sftpPane int

const (
	sftpLocal sftpPane = iota
	sftpRemote
)

type sftpLoadedMsg struct {
	local  []coreconnector.FileEntry
	remote []coreconnector.FileEntry
	err    error
}

type sftpTransferDoneMsg struct {
	action string
	err    error
}

// NewSFTP creates a browser. LocalPath defaults to the user's home directory;
// RemotePath defaults to the SFTP server's home directory.
func NewSFTP(opts SFTPOptions) *SFTPModel {
	localPath := opts.LocalPath
	if strings.TrimSpace(localPath) == "" {
		if home, err := os.UserHomeDir(); err == nil {
			localPath = home
		} else {
			localPath = "."
		}
	}
	remotePath := opts.RemotePath
	if strings.TrimSpace(remotePath) == "" {
		remotePath = "."
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &SFTPModel{
		host:       opts.Host,
		localPath:  filepath.Clean(localPath),
		remotePath: path.Clean(remotePath),
		openRemote: opts.OpenRemote,
		ctx:        ctx,
		cancel:     cancel,
		width:      100,
		height:     30,
		status:     "loading local and remote directories...",
	}
}

// Init implements tea.Model.
func (m *SFTPModel) Init() tea.Cmd {
	return m.load()
}

// Close releases the SFTP session and cancels any in-flight operations.
func (m *SFTPModel) Close() error {
	m.cancel()
	if m.transfer != nil {
		return m.transfer.Close()
	}
	return nil
}

// Update implements tea.Model.
func (m *SFTPModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case sftpLoadedMsg:
		m.loaded = true
		m.busy = false
		if msg.err != nil {
			m.err = msg.err
			m.status = "file browser failed"
			return m, nil
		}
		m.local = msg.local
		m.remote = msg.remote
		m.err = nil
		m.status = ""
		m.clampCursor()
		return m, nil
	case sftpTransferDoneMsg:
		m.busy = false
		if msg.err != nil {
			m.err = msg.err
			m.status = msg.action + " failed"
			return m, nil
		}
		m.err = nil
		m.status = msg.action + " completed"
		return m, m.load()
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *SFTPModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q", "esc":
		m.cancel()
		return m, tea.Quit
	case "tab":
		m.active = (m.active + 1) % 2
		return m, nil
	case "j", "down", "ctrl+n":
		m.move(1)
		return m, nil
	case "k", "up", "ctrl+p":
		m.move(-1)
		return m, nil
	case "g":
		m.cursor[m.active] = 0
		return m, nil
	case "G":
		entries := m.entries(m.active)
		if len(entries) > 0 {
			m.cursor[m.active] = len(entries) - 1
		}
		return m, nil
	case "enter", "right":
		return m.openSelected()
	case "backspace", "left":
		return m.parentDirectory()
	case "r":
		if !m.busy {
			m.busy = true
			m.status = "refreshing..."
			return m, m.load()
		}
	case "u":
		return m.startUpload()
	case "d":
		return m.startDownload()
	}
	return m, nil
}

func (m *SFTPModel) entries(pane int) []coreconnector.FileEntry {
	if pane == int(sftpLocal) {
		return m.local
	}
	return m.remote
}

func (m *SFTPModel) selected(pane int) (coreconnector.FileEntry, bool) {
	entries := m.entries(pane)
	if len(entries) == 0 {
		return coreconnector.FileEntry{}, false
	}
	index := m.cursor[pane]
	if index < 0 || index >= len(entries) {
		return coreconnector.FileEntry{}, false
	}
	return entries[index], true
}

func (m *SFTPModel) move(delta int) {
	entries := m.entries(m.active)
	if len(entries) == 0 {
		m.cursor[m.active] = 0
		return
	}
	next := m.cursor[m.active] + delta
	if next < 0 {
		next = 0
	}
	if next >= len(entries) {
		next = len(entries) - 1
	}
	m.cursor[m.active] = next
}

func (m *SFTPModel) clampCursor() {
	for pane := 0; pane < 2; pane++ {
		entries := m.entries(pane)
		if len(entries) == 0 {
			m.cursor[pane] = 0
			continue
		}
		if m.cursor[pane] >= len(entries) {
			m.cursor[pane] = len(entries) - 1
		}
	}
}

func (m *SFTPModel) openSelected() (tea.Model, tea.Cmd) {
	entry, ok := m.selected(m.active)
	if !ok || !entry.Directory || m.busy {
		return m, nil
	}
	if m.active == int(sftpLocal) {
		m.localPath = filepath.Clean(entry.Path)
	} else {
		m.remotePath = path.Clean(entry.Path)
	}
	m.cursor[m.active] = 0
	m.busy = true
	m.status = "opening " + entry.Name + "..."
	return m, m.load()
}

func (m *SFTPModel) parentDirectory() (tea.Model, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	if m.active == int(sftpLocal) {
		parent := filepath.Dir(m.localPath)
		if parent == m.localPath {
			return m, nil
		}
		m.localPath = parent
	} else {
		parent := path.Dir(m.remotePath)
		if parent == m.remotePath {
			return m, nil
		}
		m.remotePath = parent
	}
	m.cursor[m.active] = 0
	m.busy = true
	m.status = "loading parent directory..."
	return m, m.load()
}

func (m *SFTPModel) load() tea.Cmd {
	return func() tea.Msg {
		local, err := readLocalDir(m.localPath)
		if err != nil {
			return sftpLoadedMsg{err: fmt.Errorf("local %s: %w", m.localPath, err)}
		}
		if m.transfer == nil {
			if m.openRemote == nil {
				return sftpLoadedMsg{err: fmt.Errorf("remote SFTP is not configured")}
			}
			m.transfer, err = m.openRemote(m.ctx, m.host)
			if err != nil {
				return sftpLoadedMsg{err: fmt.Errorf("open remote SFTP: %w", err)}
			}
		}
		remote, err := m.transfer.ReadDir(m.ctx, m.remotePath)
		if err != nil {
			return sftpLoadedMsg{err: err}
		}
		sortEntries(local)
		sortEntries(remote)
		return sftpLoadedMsg{local: local, remote: remote}
	}
}

func (m *SFTPModel) startUpload() (tea.Model, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	entry, ok := m.selected(int(sftpLocal))
	if !ok || entry.Directory {
		m.status = "select a local file to upload"
		return m, nil
	}
	m.busy = true
	m.status = "uploading " + entry.Name + "..."
	localPath := entry.Path
	remotePath := path.Join(m.remotePath, entry.Name)
	transfer := m.transfer
	return m, func() tea.Msg {
		return sftpTransferDoneMsg{action: "upload " + entry.Name, err: uploadFile(m.ctx, localPath, remotePath, transfer)}
	}
}

func (m *SFTPModel) startDownload() (tea.Model, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	entry, ok := m.selected(int(sftpRemote))
	if !ok || entry.Directory {
		m.status = "select a remote file to download"
		return m, nil
	}
	m.busy = true
	m.status = "downloading " + entry.Name + "..."
	remotePath := entry.Path
	localPath := filepath.Join(m.localPath, entry.Name)
	transfer := m.transfer
	return m, func() tea.Msg {
		return sftpTransferDoneMsg{action: "download " + entry.Name, err: downloadFile(m.ctx, remotePath, localPath, transfer)}
	}
}

// View implements tea.Model.
func (m *SFTPModel) View() string {
	width := max(40, m.width)
	header := styleTitle.Render(" whop SFTP ") + styleDim.Render(" "+m.host.Alias)
	if m.busy {
		header += "  " + styleAccent.Render("busy")
	}
	leftWidth := (width - 3) / 2
	rightWidth := width - leftWidth - 3
	local := m.paneLines(sftpLocal, leftWidth)
	remote := m.paneLines(sftpRemote, rightWidth)
	rows := max(len(local), len(remote))
	body := make([]string, 0, rows)
	for i := 0; i < rows; i++ {
		left, right := "", ""
		if i < len(local) {
			left = local[i]
		}
		if i < len(remote) {
			right = remote[i]
		}
		body = append(body, padTo(left, leftWidth)+" | "+right)
	}

	footer := styleDim.Render(" tab: switch  j/k: move  enter: open  backspace: parent  u: upload  d: download  r: refresh  q: quit")
	if m.status != "" {
		footer += "\n" + styleDim.Render(" "+m.status)
	}
	if m.err != nil {
		footer += "\n" + styleError.Render(" error: "+m.err.Error())
	}
	lines := []string{header, "", strings.Join(body, "\n"), "", ansi.Truncate(footer, width, "~")}
	return strings.Join(lines, "\n")
}

func (m *SFTPModel) paneLines(pane sftpPane, width int) []string {
	title := "LOCAL"
	current := m.localPath
	entries := m.local
	if pane == sftpRemote {
		title = "REMOTE"
		current = m.remotePath
		entries = m.remote
	}
	label := title + " / " + displayConfigPath(current)
	if pane == sftpRemote {
		label = title + " " + current
	}
	lines := []string{ansi.Truncate(styleAccent.Render(" "+label), width, "~")}
	if pane == sftpPane(m.active) {
		lines[0] = styleSelected.Render(padTo(" "+label, width))
	}
	available := max(1, m.height-7)
	start := 0
	if m.cursor[pane] >= available {
		start = m.cursor[pane] - available + 1
	}
	end := min(len(entries), start+available)
	for i := start; i < end; i++ {
		entry := entries[i]
		name := entry.Name
		if entry.Directory {
			name += "/"
		}
		size := fileSize(entry.Size)
		if entry.Directory {
			size = "-"
		}
		line := "  " + fit(name, max(1, width-10)) + " " + fit(size, 7)
		if i == m.cursor[pane] && pane == sftpPane(m.active) {
			line = styleSelected.Render(padTo(line, width))
		}
		lines = append(lines, line)
	}
	if len(entries) == 0 {
		lines = append(lines, styleDim.Render("  (empty)"))
	}
	return lines
}

func readLocalDir(dir string) ([]coreconnector.FileEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	result := make([]coreconnector.FileEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		result = append(result, coreconnector.FileEntry{
			Name:      info.Name(),
			Path:      filepath.Join(dir, info.Name()),
			Size:      info.Size(),
			Mode:      uint32(info.Mode().Perm()),
			Modified:  info.ModTime().UTC(),
			Directory: info.IsDir(),
			Symlink:   info.Mode()&os.ModeSymlink != 0,
		})
	}
	return result, nil
}

func sortEntries(entries []coreconnector.FileEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Directory != entries[j].Directory {
			return entries[i].Directory
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
}

func uploadFile(ctx context.Context, localPath, remotePath string, transfer coreconnector.FileTransfer) error {
	if transfer == nil {
		return fmt.Errorf("remote SFTP is not connected")
	}
	if _, err := transfer.Stat(ctx, remotePath); err == nil {
		return fmt.Errorf("remote file already exists: %s", path.Base(remotePath))
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check remote destination: %w", err)
	}
	source, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open local file: %w", err)
	}
	defer source.Close()
	destination, err := transfer.Create(ctx, remotePath)
	if err != nil {
		return err
	}
	if _, err := copyWithContext(ctx, destination, source); err != nil {
		_ = destination.Close()
		_ = transfer.Remove(ctx, remotePath)
		return fmt.Errorf("upload %s: %w", path.Base(remotePath), err)
	}
	if err := destination.Close(); err != nil {
		_ = transfer.Remove(ctx, remotePath)
		return fmt.Errorf("close remote file: %w", err)
	}
	return nil
}

func downloadFile(ctx context.Context, remotePath, localPath string, transfer coreconnector.FileTransfer) error {
	if transfer == nil {
		return fmt.Errorf("remote SFTP is not connected")
	}
	source, err := transfer.Open(ctx, remotePath)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(localPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create local file: %w", err)
	}
	if _, err := copyWithContext(ctx, destination, source); err != nil {
		_ = destination.Close()
		_ = os.Remove(localPath)
		return fmt.Errorf("download %s: %w", path.Base(remotePath), err)
	}
	if err := destination.Close(); err != nil {
		_ = os.Remove(localPath)
		return fmt.Errorf("close local file: %w", err)
	}
	return nil
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buffer := make([]byte, 32*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			written, writeErr := dst.Write(buffer[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

func fileSize(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	if size < 1024*1024 {
		return fmt.Sprintf("%.1f K", float64(size)/1024)
	}
	if size < 1024*1024*1024 {
		return fmt.Sprintf("%.1f M", float64(size)/(1024*1024))
	}
	return fmt.Sprintf("%.1f G", float64(size)/(1024*1024*1024))
}
