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

// Package fusemount implements a FUSE-based mount transport for kubefs
// using hanwen/go-fuse. This is the primary transport on Linux.
package fusemount

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"syscall"
	"time"

	gofuse "github.com/hanwen/go-fuse/v2/fuse"
	"github.com/hanwen/go-fuse/v2/fs"

	"github.com/cloudygreybeard/kubefs/internal/kubefs"
)

// Mount mounts the kubefs filesystem at mountpoint using kernel FUSE.
func Mount(kfs *kubefs.FS, mountpoint string, readOnly, debug bool, logger *log.Logger) (*gofuse.Server, error) {
	root := &dirNode{kfs: kfs, nodeID: kubefs.RootInodeID}

	opts := &fs.Options{
		MountOptions: gofuse.MountOptions{
			FsName:        "kubefs",
			Name:          "kubefs",
			DisableXAttrs: true,
			MaxReadAhead:  128 * 1024,
		},
		AttrTimeout:  &oneSecond,
		EntryTimeout: &oneSecond,
	}

	if readOnly {
		opts.Options = append(opts.Options, "ro")
	}

	if debug {
		opts.Debug = true
	}

	server, err := fs.Mount(mountpoint, root, opts)
	if err != nil {
		return nil, fmt.Errorf("fuse mount: %w", err)
	}

	logger.Printf("mounted on %s (pid %d, %s)", mountpoint, os.Getpid(), kfs.Verbs())
	return server, nil
}

var oneSecond = 1 * time.Second

// dirNode represents a directory in the FUSE tree.
type dirNode struct {
	fs.Inode
	kfs    *kubefs.FS
	nodeID kubefs.InodeID
}

var (
	_ fs.NodeLookuper  = (*dirNode)(nil)
	_ fs.NodeReaddirer = (*dirNode)(nil)
	_ fs.NodeGetattrer = (*dirNode)(nil)
	_ fs.NodeUnlinker  = (*dirNode)(nil)
	_ fs.NodeRmdirer   = (*dirNode)(nil)
	_ fs.NodeCreater   = (*dirNode)(nil)
	_ fs.NodeStatfser  = (*dirNode)(nil)
)

func (d *dirNode) Lookup(ctx context.Context, name string, out *gofuse.EntryOut) (*fs.Inode, syscall.Errno) {
	node, err := d.kfs.Lookup(ctx, d.nodeID, name)
	if err != nil {
		return nil, toErrno(err)
	}

	attr, _ := d.kfs.GetAttr(ctx, node.ID)
	fillEntryOut(node, attr, out)

	var child fs.InodeEmbedder
	if node.IsDir() {
		child = &dirNode{kfs: d.kfs, nodeID: node.ID}
	} else {
		child = &fileNode{kfs: d.kfs, nodeID: node.ID}
	}

	stable := fs.StableAttr{Mode: uint32(attr.Mode), Ino: node.ID}
	return d.NewInode(ctx, child, stable), 0
}

func (d *dirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	children, err := d.kfs.Children(ctx, d.nodeID)
	if err != nil {
		return nil, toErrno(err)
	}

	entries := make([]gofuse.DirEntry, 0, len(children))
	for _, child := range children {
		mode := uint32(child.Mode())
		entries = append(entries, gofuse.DirEntry{
			Name: child.Name,
			Ino:  child.ID,
			Mode: mode,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return fs.NewListDirStream(entries), 0
}

func (d *dirNode) Getattr(ctx context.Context, _ fs.FileHandle, out *gofuse.AttrOut) syscall.Errno {
	attr, err := d.kfs.GetAttr(ctx, d.nodeID)
	if err != nil {
		return toErrno(err)
	}
	fillAttrOut(d.nodeID, attr, &out.Attr)
	return 0
}

func (d *dirNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return toErrno(d.kfs.Remove(ctx, d.nodeID, name, false))
}

func (d *dirNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	return toErrno(d.kfs.Remove(ctx, d.nodeID, name, true))
}

func (d *dirNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *gofuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	node, handle, err := d.kfs.CreateAndOpen(d.nodeID, name)
	if err != nil {
		return nil, nil, 0, toErrno(err)
	}

	attr, _ := d.kfs.GetAttr(ctx, node.ID)
	fillEntryOut(node, attr, out)

	child := &fileNode{kfs: d.kfs, nodeID: node.ID}
	stable := fs.StableAttr{Mode: uint32(attr.Mode), Ino: node.ID}
	inode := d.NewInode(ctx, child, stable)

	fh := &fuseHandle{kfs: d.kfs, handle: handle, nodeID: node.ID}
	return inode, fh, 0, 0
}

func (d *dirNode) Statfs(ctx context.Context, out *gofuse.StatfsOut) syscall.Errno {
	s := d.kfs.StatFS()
	out.Bsize = s.BlockSize
	out.Blocks = s.Blocks
	out.Bfree = s.BlocksFree
	out.Bavail = s.BlocksAvailable
	out.Files = s.Inodes
	out.Ffree = s.InodesFree
	out.Frsize = s.BlockSize
	return 0
}

// fileNode represents a file in the FUSE tree.
type fileNode struct {
	fs.Inode
	kfs    *kubefs.FS
	nodeID kubefs.InodeID
}

var (
	_ fs.NodeOpener    = (*fileNode)(nil)
	_ fs.NodeGetattrer = (*fileNode)(nil)
	_ fs.NodeSetattrer = (*fileNode)(nil)
)

func (f *fileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	handle, err := f.kfs.OpenFile(ctx, f.nodeID)
	if err != nil {
		return nil, 0, toErrno(err)
	}
	return &fuseHandle{kfs: f.kfs, handle: handle, nodeID: f.nodeID}, 0, 0
}

func (f *fileNode) Getattr(ctx context.Context, _ fs.FileHandle, out *gofuse.AttrOut) syscall.Errno {
	attr, err := f.kfs.GetAttr(ctx, f.nodeID)
	if err != nil {
		return toErrno(err)
	}
	fillAttrOut(f.nodeID, attr, &out.Attr)
	return 0
}

func (f *fileNode) Setattr(ctx context.Context, _ fs.FileHandle, in *gofuse.SetAttrIn, out *gofuse.AttrOut) syscall.Errno {
	if sz, ok := in.GetSize(); ok {
		if err := f.kfs.Truncate(f.nodeID, sz); err != nil {
			return toErrno(err)
		}
	}
	attr, err := f.kfs.GetAttr(ctx, f.nodeID)
	if err != nil {
		return toErrno(err)
	}
	fillAttrOut(f.nodeID, attr, &out.Attr)
	return 0
}

// fuseHandle implements fs.FileHandle for read/write/flush/release.
type fuseHandle struct {
	kfs    *kubefs.FS
	handle kubefs.HandleID
	nodeID kubefs.InodeID
}

var (
	_ fs.FileReader   = (*fuseHandle)(nil)
	_ fs.FileWriter   = (*fuseHandle)(nil)
	_ fs.FileFlusher  = (*fuseHandle)(nil)
	_ fs.FileFsyncer  = (*fuseHandle)(nil)
	_ fs.FileReleaser = (*fuseHandle)(nil)
)

func (h *fuseHandle) Read(ctx context.Context, dest []byte, off int64) (gofuse.ReadResult, syscall.Errno) {
	buf := make([]byte, len(dest))
	n, err := h.kfs.ReadHandle(h.handle, off, buf)
	if err != nil {
		return nil, toErrno(err)
	}
	return gofuse.ReadResultData(buf[:n]), 0
}

func (h *fuseHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	n, err := h.kfs.WriteHandle(h.handle, off, data)
	if err != nil {
		return 0, toErrno(err)
	}
	return uint32(n), 0
}

func (h *fuseHandle) Flush(ctx context.Context) syscall.Errno {
	return toErrno(h.kfs.FlushHandle(ctx, h.handle))
}

func (h *fuseHandle) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	return toErrno(h.kfs.FlushHandle(ctx, h.handle))
}

func (h *fuseHandle) Release(ctx context.Context) syscall.Errno {
	h.kfs.ReleaseHandle(h.handle)
	return 0
}

func fillAttrOut(id kubefs.InodeID, attr kubefs.Attr, out *gofuse.Attr) {
	out.Ino = id
	out.Size = attr.Size
	out.Nlink = attr.Nlink
	out.Mode = uint32(attr.Mode)
	out.Uid = attr.Uid
	out.Gid = attr.Gid
	out.SetTimes(&attr.Atime, &attr.Mtime, &attr.Ctime)
}

func fillEntryOut(node *kubefs.Inode, attr kubefs.Attr, out *gofuse.EntryOut) {
	out.NodeId = node.ID
	out.SetAttrTimeout(5 * time.Second)
	out.SetEntryTimeout(5 * time.Second)
	fillAttrOut(node.ID, attr, &out.Attr)
}

func toErrno(err error) syscall.Errno {
	if err == nil {
		return 0
	}
	if errno, ok := err.(syscall.Errno); ok {
		return errno
	}
	return syscall.EIO
}
