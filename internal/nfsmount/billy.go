// Copyright 2026 cloudygreybeard
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package nfsmount implements an NFS-based mount transport for kubefs.
// It embeds an NFSv3 server and mounts it via the operating system's
// built-in NFS client, requiring no FUSE installation.
package nfsmount

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	billy "github.com/go-git/go-billy/v5"
	nfsfile "github.com/willscott/go-nfs/file"

	"github.com/cloudygreybeard/kubefs/internal/kubefs"
)

// BillyFS adapts a kubefs.FS to the billy.Filesystem interface used by
// willscott/go-nfs.
type BillyFS struct {
	fs  *kubefs.FS
	ctx context.Context

	mu      sync.Mutex
	handles map[*billyFile]bool
}

// NewBillyFS creates a billy.Filesystem backed by a kubefs.FS.
func NewBillyFS(ctx context.Context, kfs *kubefs.FS) *BillyFS {
	return &BillyFS{
		fs:      kfs,
		ctx:     ctx,
		handles: make(map[*billyFile]bool),
	}
}

func (b *BillyFS) resolve(path string) (*kubefs.Inode, error) {
	path = filepath.Clean(path)
	if path == "." || path == "/" || path == "" {
		return b.fs.GetInode(kubefs.RootInodeID), nil
	}

	path = strings.TrimPrefix(path, "/")
	parts := strings.Split(path, "/")

	current := kubefs.RootInodeID
	for _, name := range parts {
		node, err := b.fs.Lookup(b.ctx, current, name)
		if err != nil {
			return nil, err
		}
		current = node.ID
	}
	return b.fs.GetInode(current), nil
}

func (b *BillyFS) parentAndName(path string) (kubefs.InodeID, string, error) {
	path = filepath.Clean(path)
	path = strings.TrimPrefix(path, "/")
	dir := filepath.Dir(path)
	name := filepath.Base(path)

	parent, err := b.resolve(dir)
	if err != nil {
		return 0, "", err
	}
	return parent.ID, name, nil
}

// Create creates a new file.
func (b *BillyFS) Create(filename string) (billy.File, error) {
	parentID, name, err := b.parentAndName(filename)
	if err != nil {
		return nil, err
	}

	node, handle, err := b.fs.CreateAndOpen(parentID, name)
	if err != nil {
		return nil, err
	}

	f := &billyFile{
		bfs:    b,
		node:   node,
		handle: handle,
		name:   filename,
	}
	b.mu.Lock()
	b.handles[f] = true
	b.mu.Unlock()
	return f, nil
}

// Open opens a file for reading.
func (b *BillyFS) Open(filename string) (billy.File, error) {
	return b.OpenFile(filename, os.O_RDONLY, 0)
}

// OpenFile opens a file with the given flags and permissions.
func (b *BillyFS) OpenFile(filename string, flag int, _ os.FileMode) (billy.File, error) {
	node, err := b.resolve(filename)
	if err != nil {
		return nil, err
	}

	if node.IsDir() {
		return &billyFile{bfs: b, node: node, name: filename, isDir: true}, nil
	}

	handle, err := b.fs.OpenFile(b.ctx, node.ID)
	if err != nil {
		return nil, err
	}

	f := &billyFile{
		bfs:    b,
		node:   node,
		handle: handle,
		name:   filename,
		flag:   flag,
	}
	b.mu.Lock()
	b.handles[f] = true
	b.mu.Unlock()
	return f, nil
}

// Stat returns file info for the given path.
func (b *BillyFS) Stat(filename string) (os.FileInfo, error) {
	node, err := b.resolve(filename)
	if err != nil {
		return nil, err
	}
	attr, err := b.fs.GetAttr(b.ctx, node.ID)
	if err != nil {
		return nil, err
	}
	return &billyFileInfo{node: node, attr: attr}, nil
}

// ReadDir returns directory entries.
func (b *BillyFS) ReadDir(path string) ([]os.FileInfo, error) {
	node, err := b.resolve(path)
	if err != nil {
		return nil, &os.PathError{Op: "readdir", Path: path, Err: err}
	}

	children, err := b.fs.Children(b.ctx, node.ID)
	if err != nil {
		return nil, err
	}

	infos := make([]os.FileInfo, 0, len(children))
	for _, child := range children {
		attr, err := b.fs.GetAttr(b.ctx, child.ID)
		if err != nil {
			continue
		}
		infos = append(infos, &billyFileInfo{node: child, attr: attr})
	}
	return infos, nil
}

// Remove removes a file or empty directory.
func (b *BillyFS) Remove(filename string) error {
	parentID, name, err := b.parentAndName(filename)
	if err != nil {
		return err
	}
	node, err := b.resolve(filename)
	if err != nil {
		return err
	}
	return b.fs.Remove(b.ctx, parentID, name, node.IsDir())
}

// Rename is not supported.
func (b *BillyFS) Rename(_, _ string) error { return syscall.EPERM }

// MkdirAll is not supported.
func (b *BillyFS) MkdirAll(_ string, _ os.FileMode) error { return syscall.EPERM }

// Lstat is the same as Stat (no symlinks).
func (b *BillyFS) Lstat(filename string) (os.FileInfo, error) { return b.Stat(filename) }

// Symlink is not supported.
func (b *BillyFS) Symlink(_, _ string) error { return syscall.EPERM }

// Readlink is not supported.
func (b *BillyFS) Readlink(_ string) (string, error) { return "", syscall.EPERM }

// Join joins path elements.
func (b *BillyFS) Join(elem ...string) string { return filepath.Join(elem...) }

// TempFile is not supported.
func (b *BillyFS) TempFile(_, _ string) (billy.File, error) { return nil, syscall.EPERM }

// Chroot is not supported.
func (b *BillyFS) Chroot(_ string) (billy.Filesystem, error) { return nil, syscall.EPERM }

// Root returns the root path.
func (b *BillyFS) Root() string { return "/" }

// billyFile implements billy.File.
type billyFile struct {
	bfs    *BillyFS
	node   *kubefs.Inode
	handle kubefs.HandleID
	name   string
	offset int64
	flag   int
	isDir  bool
	closed bool
}

func (f *billyFile) Name() string { return f.name }

func (f *billyFile) Read(p []byte) (int, error) {
	if f.isDir {
		return 0, fmt.Errorf("is a directory")
	}
	n, err := f.bfs.fs.ReadHandle(f.handle, f.offset, p)
	f.offset += int64(n)
	if n == 0 && err == nil {
		return 0, fmt.Errorf("EOF")
	}
	return n, err
}

func (f *billyFile) ReadAt(p []byte, off int64) (int, error) {
	if f.isDir {
		return 0, fmt.Errorf("is a directory")
	}
	return f.bfs.fs.ReadHandle(f.handle, off, p)
}

func (f *billyFile) Write(p []byte) (int, error) {
	if f.isDir {
		return 0, fmt.Errorf("is a directory")
	}
	n, err := f.bfs.fs.WriteHandle(f.handle, f.offset, p)
	f.offset += int64(n)
	return n, err
}

func (f *billyFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0:
		f.offset = offset
	case 1:
		f.offset += offset
	case 2:
		attr, err := f.bfs.fs.GetAttr(f.bfs.ctx, f.node.ID)
		if err != nil {
			return 0, err
		}
		f.offset = int64(attr.Size) + offset
	}
	return f.offset, nil
}

func (f *billyFile) Close() error {
	if f.closed || f.isDir {
		return nil
	}
	f.closed = true

	if err := f.bfs.fs.FlushHandle(f.bfs.ctx, f.handle); err != nil {
		return err
	}
	f.bfs.fs.ReleaseHandle(f.handle)

	f.bfs.mu.Lock()
	delete(f.bfs.handles, f)
	f.bfs.mu.Unlock()
	return nil
}

func (f *billyFile) Lock() error   { return nil }
func (f *billyFile) Unlock() error { return nil }

func (f *billyFile) Truncate(size int64) error {
	return f.bfs.fs.Truncate(f.node.ID, uint64(size))
}

// billyFileInfo implements os.FileInfo backed by kubefs node attributes.
type billyFileInfo struct {
	node *kubefs.Inode
	attr kubefs.Attr
}

func (fi *billyFileInfo) Name() string      { return fi.node.Name }
func (fi *billyFileInfo) Size() int64       { return int64(fi.attr.Size) }
func (fi *billyFileInfo) Mode() os.FileMode { return fi.attr.Mode }
func (fi *billyFileInfo) ModTime() time.Time { return fi.attr.Mtime }
func (fi *billyFileInfo) IsDir() bool       { return fi.node.IsDir() }

// Sys returns a *nfsfile.FileInfo so go-nfs can read uid/gid and
// fileid consistently across platforms.
func (fi *billyFileInfo) Sys() interface{} {
	return &nfsfile.FileInfo{
		Fileid: fi.node.ID,
		Nlink:  fi.attr.Nlink,
		UID:    fi.attr.Uid,
		GID:    fi.attr.Gid,
	}
}
