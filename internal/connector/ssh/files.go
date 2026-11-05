package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	coreconnector "whop/internal/connector"
	"whop/internal/sshconfig"
)

type fileTransfer struct {
	client *sftp.Client
	ssh    *ssh.Client
}

// OpenFileTransfer starts an SFTP subsystem session for host.
func (c *Connector) OpenFileTransfer(ctx context.Context, host sshconfig.Host) (coreconnector.FileTransfer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sshClient, err := c.Dial(ctx, host)
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = sshClient.Close()
		case <-done:
		}
	}()
	client, err := sftp.NewClient(sshClient)
	close(done)
	if err != nil {
		_ = sshClient.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("open SFTP subsystem on %s: %w", host.Alias, err)
	}
	if err := ctx.Err(); err != nil {
		_ = client.Close()
		_ = sshClient.Close()
		return nil, err
	}
	return &fileTransfer{client: client, ssh: sshClient}, nil
}

func (f *fileTransfer) ReadDir(ctx context.Context, remotePath string) ([]coreconnector.FileEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := f.client.ReadDirContext(ctx, remotePath)
	if err != nil {
		return nil, fmt.Errorf("sftp read directory %s: %w", remotePath, err)
	}
	result := make([]coreconnector.FileEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, fileEntry(remotePath, entry))
	}
	return result, nil
}

func (f *fileTransfer) Stat(ctx context.Context, remotePath string) (coreconnector.FileEntry, error) {
	if err := ctx.Err(); err != nil {
		return coreconnector.FileEntry{}, err
	}
	info, err := f.client.Stat(remotePath)
	if err != nil {
		return coreconnector.FileEntry{}, fmt.Errorf("sftp stat %s: %w", remotePath, err)
	}
	return fileEntry(remotePath, info), nil
}

func (f *fileTransfer) Open(ctx context.Context, remotePath string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := f.client.Open(remotePath)
	if err != nil {
		return nil, fmt.Errorf("sftp open %s: %w", remotePath, err)
	}
	return file, nil
}

func (f *fileTransfer) Create(ctx context.Context, remotePath string) (io.WriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := f.client.Create(remotePath)
	if err != nil {
		return nil, fmt.Errorf("sftp create %s: %w", remotePath, err)
	}
	return file, nil
}

func (f *fileTransfer) Mkdir(ctx context.Context, remotePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.client.Mkdir(remotePath); err != nil {
		return fmt.Errorf("sftp mkdir %s: %w", remotePath, err)
	}
	return nil
}

func (f *fileTransfer) Remove(ctx context.Context, remotePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.client.Remove(remotePath); err != nil {
		return fmt.Errorf("sftp remove %s: %w", remotePath, err)
	}
	return nil
}

func (f *fileTransfer) RemoveAll(ctx context.Context, remotePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.client.RemoveAll(remotePath); err != nil {
		return fmt.Errorf("sftp remove all %s: %w", remotePath, err)
	}
	return nil
}

func (f *fileTransfer) Rename(ctx context.Context, oldPath, newPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.client.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("sftp rename %s to %s: %w", oldPath, newPath, err)
	}
	return nil
}

func (f *fileTransfer) Close() error {
	var result error
	if err := f.client.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		result = err
	}
	if err := f.ssh.Close(); err != nil && !errors.Is(err, os.ErrClosed) && result == nil {
		result = err
	}
	return result
}

func fileEntry(base string, info os.FileInfo) coreconnector.FileEntry {
	return coreconnector.FileEntry{
		Name:      info.Name(),
		Path:      path.Join(base, info.Name()),
		Size:      info.Size(),
		Mode:      uint32(info.Mode().Perm()),
		Modified:  info.ModTime().UTC(),
		Directory: info.IsDir(),
		Symlink:   info.Mode()&os.ModeSymlink != 0,
	}
}

var _ coreconnector.FileTransfer = (*fileTransfer)(nil)
