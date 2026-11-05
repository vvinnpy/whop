package connector

import (
	"context"
	"io"
	"time"
)

// FileEntry is a protocol-neutral directory entry.
type FileEntry struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	Mode      uint32    `json:"mode"`
	Modified  time.Time `json:"modified"`
	Directory bool      `json:"directory"`
	Symlink   bool      `json:"symlink,omitempty"`
}

// FileTransfer is one remote file-system session. Implementations must be
// closed by the caller.
type FileTransfer interface {
	ReadDir(ctx context.Context, path string) ([]FileEntry, error)
	Stat(ctx context.Context, path string) (FileEntry, error)
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	Create(ctx context.Context, path string) (io.WriteCloser, error)
	Mkdir(ctx context.Context, path string) error
	Remove(ctx context.Context, path string) error
	RemoveAll(ctx context.Context, path string) error
	Rename(ctx context.Context, oldPath, newPath string) error
	Close() error
}
