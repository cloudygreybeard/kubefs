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

package fs

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jacobsa/fuse"
	"github.com/jacobsa/fuse/fuseops"
	"github.com/jacobsa/fuse/fuseutil"

	"github.com/cloudygreybeard/kubefs/internal/k8s"
)

// KubeFS implements fuseutil.FileSystem, exposing Kubernetes objects
// as a virtual directory tree.
type KubeFS struct {
	fuseutil.NotImplementedFileSystem

	client  *k8s.Client
	content *ContentProvider
	inodes  *InodeTable
	logger  *log.Logger
	verbs   AllowedVerbs

	// File handle management.
	handleMu   sync.Mutex
	nextHandle fuseops.HandleID
	// openFiles tracks writable file buffers keyed by handle ID.
	openFiles map[fuseops.HandleID]*openFile

	// contentCache stores fetched file content keyed by inode ID so that
	// GetInodeAttributes can report the real size and ReadFile can serve
	// it without a second API call.
	contentMu    sync.Mutex
	contentCache map[fuseops.InodeID]*cachedContent
}

type cachedContent struct {
	data      []byte
	fetchedAt time.Time
}

type openFile struct {
	node    *Inode
	buf     []byte
	dirty   bool
	pending bool // true when the file was created via CreateFile (new object)
}

// NewKubeFS creates a new Kubernetes FUSE filesystem.
func NewKubeFS(client *k8s.Client, logger *log.Logger, verbs AllowedVerbs) *KubeFS {
	return &KubeFS{
		client:       client,
		content:      NewContentProvider(client),
		inodes:       NewInodeTable(),
		logger:       logger,
		verbs:        verbs,
		nextHandle:   1,
		openFiles:    make(map[fuseops.HandleID]*openFile),
		contentCache: make(map[fuseops.InodeID]*cachedContent),
	}
}

func (fs *KubeFS) allocHandle() fuseops.HandleID {
	fs.handleMu.Lock()
	defer fs.handleMu.Unlock()
	h := fs.nextHandle
	fs.nextHandle++
	return h
}

func (fs *KubeFS) StatFS(_ context.Context, op *fuseops.StatFSOp) error {
	op.BlockSize = 4096
	op.IoSize = 65536
	op.Blocks = 0
	op.BlocksFree = 0
	op.BlocksAvailable = 0
	op.Inodes = 0
	op.InodesFree = 0
	return nil
}

func (fs *KubeFS) LookUpInode(ctx context.Context, op *fuseops.LookUpInodeOp) error {
	parent := fs.inodes.Get(op.Parent)
	if parent == nil {
		return fuse.ENOENT
	}

	if err := fs.ensureChildren(ctx, parent); err != nil {
		return err
	}

	child := fs.inodes.LookupChild(op.Parent, op.Name)
	if child == nil {
		return fuse.ENOENT
	}

	op.Entry = fs.childEntry(ctx, child)
	return nil
}

func (fs *KubeFS) GetInodeAttributes(ctx context.Context, op *fuseops.GetInodeAttributesOp) error {
	node := fs.inodes.Get(op.Inode)
	if node == nil {
		return fuse.ENOENT
	}

	size := uint64(0)
	if node.Type == NodeFile || node.Type == NodeDataFile {
		size = uint64(len(fs.fetchContent(ctx, node)))
	}

	op.Attributes = fs.inodeAttrs(node, size)
	op.AttributesExpiration = time.Now().Add(5 * time.Second)
	return nil
}

func (fs *KubeFS) SetInodeAttributes(_ context.Context, op *fuseops.SetInodeAttributesOp) error {
	if fs.verbs.ReadOnly() {
		return syscall.EROFS
	}

	node := fs.inodes.Get(op.Inode)
	if node == nil {
		return fuse.ENOENT
	}

	size := uint64(0)
	if op.Size != nil {
		size = *op.Size

		fs.handleMu.Lock()
		for _, of := range fs.openFiles {
			if of.node == node {
				newLen := int(size)
				if newLen < len(of.buf) {
					of.buf = of.buf[:newLen]
				}
			}
		}
		fs.handleMu.Unlock()
	}

	op.Attributes = fs.inodeAttrs(node, size)
	op.AttributesExpiration = time.Now().Add(5 * time.Second)
	return nil
}

func (fs *KubeFS) ForgetInode(_ context.Context, _ *fuseops.ForgetInodeOp) error {
	return nil
}

func (fs *KubeFS) OpenDir(ctx context.Context, op *fuseops.OpenDirOp) error {
	node := fs.inodes.Get(op.Inode)
	if node == nil {
		return fuse.ENOENT
	}
	if !node.IsDir() {
		return fuse.ENOTDIR
	}

	if err := fs.ensureChildren(ctx, node); err != nil {
		return err
	}

	op.Handle = fs.allocHandle()
	return nil
}

func (fs *KubeFS) ReadDir(_ context.Context, op *fuseops.ReadDirOp) error {
	node := fs.inodes.Get(op.Inode)
	if node == nil {
		return fuse.ENOENT
	}

	children := fs.inodes.Children(op.Inode)
	sort.Slice(children, func(i, j int) bool { return children[i].Name < children[j].Name })

	for i := int(op.Offset); i < len(children); i++ {
		child := children[i]
		dt := fuseutil.DT_Directory
		if !child.IsDir() {
			dt = fuseutil.DT_File
		}

		entry := fuseutil.Dirent{
			Offset: fuseops.DirOffset(i + 1),
			Inode:  child.ID,
			Name:   child.Name,
			Type:   dt,
		}

		n := fuseutil.WriteDirent(op.Dst[op.BytesRead:], entry)
		if n == 0 {
			break
		}
		op.BytesRead += n
	}

	return nil
}

func (fs *KubeFS) ReleaseDirHandle(_ context.Context, _ *fuseops.ReleaseDirHandleOp) error {
	return nil
}

func (fs *KubeFS) RmDir(ctx context.Context, op *fuseops.RmDirOp) error {
	if !fs.verbs.Delete {
		return syscall.EPERM
	}

	child := fs.inodes.LookupChild(op.Parent, op.Name)
	if child == nil {
		return fuse.ENOENT
	}
	if child.Type != NodeObject {
		return syscall.EPERM
	}

	ri := k8s.ResourceInfo{
		Group:    child.ResourceGroup,
		Version:  child.ResourceVersion,
		Resource: child.ResourceType,
	}

	if err := fs.client.DeleteObject(ctx, child.Namespace, ri, child.ObjectName); err != nil {
		fs.logger.Printf("RmDir: deleting %s/%s: %v", child.ResourceType, child.ObjectName, err)
		return fuse.EIO
	}

	fs.inodes.RemoveChild(op.Parent, op.Name)
	return nil
}

// Unlink removes a file. Only supported for pending-create files when
// the user abandons a create, and for object directories via RmDir.
func (fs *KubeFS) Unlink(_ context.Context, op *fuseops.UnlinkOp) error {
	child := fs.inodes.LookupChild(op.Parent, op.Name)
	if child == nil {
		return fuse.ENOENT
	}

	parent := fs.inodes.Get(op.Parent)
	if parent != nil && parent.Type == NodeResourceType {
		fs.inodes.RemoveChild(op.Parent, op.Name)
		return nil
	}

	return syscall.EPERM
}

func (fs *KubeFS) CreateFile(_ context.Context, op *fuseops.CreateFileOp) error {
	if !fs.verbs.Create {
		return syscall.EROFS
	}

	parent := fs.inodes.Get(op.Parent)
	if parent == nil {
		return fuse.ENOENT
	}
	if parent.Type != NodeResourceType {
		return syscall.EPERM
	}

	name := op.Name
	if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".json") {
		return syscall.EPERM
	}

	child := fs.inodes.AddChild(op.Parent, &Inode{
		Name:            name,
		Type:            NodeFile,
		Namespace:       parent.Namespace,
		ResourceType:    parent.ResourceType,
		FileName:        name,
		ResourceGroup:   parent.ResourceGroup,
		ResourceVersion: parent.ResourceVersion,
		CreationTime:    time.Now(),
	})

	handle := fs.allocHandle()
	fs.handleMu.Lock()
	fs.openFiles[handle] = &openFile{
		node:    child,
		buf:     nil,
		pending: true,
	}
	fs.handleMu.Unlock()

	op.Handle = handle
	op.Entry = fs.childEntry(context.Background(), child)
	return nil
}

func (fs *KubeFS) OpenFile(ctx context.Context, op *fuseops.OpenFileOp) error {
	node := fs.inodes.Get(op.Inode)
	if node == nil {
		return fuse.ENOENT
	}

	handle := fs.allocHandle()
	data := fs.fetchContent(ctx, node)

	fs.handleMu.Lock()
	fs.openFiles[handle] = &openFile{
		node: node,
		buf:  append([]byte(nil), data...),
	}
	fs.handleMu.Unlock()

	op.Handle = handle
	op.KeepPageCache = true
	return nil
}

func (fs *KubeFS) ReadFile(_ context.Context, op *fuseops.ReadFileOp) error {
	fs.handleMu.Lock()
	of, ok := fs.openFiles[op.Handle]
	fs.handleMu.Unlock()

	if !ok {
		return fuse.EIO
	}

	if op.Offset >= int64(len(of.buf)) {
		op.BytesRead = 0
		return nil
	}

	end := op.Offset + int64(len(op.Dst))
	if end > int64(len(of.buf)) {
		end = int64(len(of.buf))
	}

	op.BytesRead = copy(op.Dst, of.buf[op.Offset:end])
	return nil
}

func (fs *KubeFS) WriteFile(_ context.Context, op *fuseops.WriteFileOp) error {
	fs.handleMu.Lock()
	of, ok := fs.openFiles[op.Handle]
	fs.handleMu.Unlock()

	if !ok {
		return fuse.EIO
	}

	if of.pending {
		if !fs.verbs.Create {
			return syscall.EROFS
		}
	} else if !fs.verbs.Update {
		return syscall.EROFS
	}

	switch of.node.Type {
	case NodeDataFile:
		// all data files are writable
	case NodeFile:
		if of.node.FileName != "yaml" && of.node.FileName != "json" {
			return syscall.EPERM
		}
	default:
		return syscall.EPERM
	}

	end := int(op.Offset) + len(op.Data)
	if end > len(of.buf) {
		grown := make([]byte, end)
		copy(grown, of.buf)
		of.buf = grown
	}
	copy(of.buf[op.Offset:], op.Data)
	of.dirty = true

	return nil
}

func (fs *KubeFS) FlushFile(ctx context.Context, op *fuseops.FlushFileOp) error {
	fs.handleMu.Lock()
	of, ok := fs.openFiles[op.Handle]
	fs.handleMu.Unlock()

	if !ok {
		return nil
	}

	if !of.dirty {
		return nil
	}

	if of.pending {
		if !fs.verbs.Create {
			return syscall.EROFS
		}
		if err := fs.content.CreateContent(ctx, of.node, of.buf); err != nil {
			fs.logger.Printf("FlushFile: error creating object: %v", err)
			return fuse.EIO
		}
	} else {
		if !fs.verbs.Update {
			return syscall.EROFS
		}
		if err := fs.content.ApplyContent(ctx, of.node, of.buf); err != nil {
			fs.logger.Printf("FlushFile: error applying content: %v", err)
			return fuse.EIO
		}
	}

	of.dirty = false
	of.pending = false

	fs.contentMu.Lock()
	delete(fs.contentCache, of.node.ID)
	fs.contentMu.Unlock()

	return nil
}

func (fs *KubeFS) SyncFile(ctx context.Context, op *fuseops.SyncFileOp) error {
	return fs.FlushFile(ctx, &fuseops.FlushFileOp{
		Inode:  op.Inode,
		Handle: op.Handle,
	})
}

func (fs *KubeFS) ReleaseFileHandle(_ context.Context, op *fuseops.ReleaseFileHandleOp) error {
	fs.handleMu.Lock()
	delete(fs.openFiles, op.Handle)
	fs.handleMu.Unlock()
	return nil
}

func (fs *KubeFS) Destroy() {
	fs.logger.Println("filesystem unmounted")
}

// ensureChildren populates the child inodes for a directory node by
// querying the Kubernetes API. It is idempotent; existing children
// whose names still appear in the API response are preserved.
func (fs *KubeFS) ensureChildren(ctx context.Context, node *Inode) error {
	switch node.Type {
	case NodeRoot:
		return fs.populateNamespaces(ctx, node)
	case NodeNamespace:
		return fs.populateResourceTypes(ctx, node)
	case NodeResourceType:
		return fs.populateObjects(ctx, node)
	case NodeObject:
		return fs.populateActionFiles(node)
	case NodeDataDir:
		return fs.populateDataKeys(ctx, node)
	default:
		return nil
	}
}

func (fs *KubeFS) populateNamespaces(ctx context.Context, root *Inode) error {
	namespaces, err := fs.client.ListNamespaces(ctx)
	if err != nil {
		fs.logger.Printf("listing namespaces: %v", err)
		return fuse.EIO
	}

	keep := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		keep[ns] = true
		fs.inodes.AddChild(root.ID, &Inode{
			Name:         ns,
			Type:         NodeNamespace,
			Namespace:    ns,
			CreationTime: time.Now(),
		})
	}
	fs.inodes.RemoveChildren(root.ID, keep)
	return nil
}

func (fs *KubeFS) populateResourceTypes(ctx context.Context, nsNode *Inode) error {
	resources, err := fs.client.DiscoverResources(ctx)
	if err != nil {
		fs.logger.Printf("discovering resources: %v", err)
		return fuse.EIO
	}

	keep := make(map[string]bool, len(resources))
	for name, ri := range resources {
		keep[name] = true
		fs.inodes.AddChild(nsNode.ID, &Inode{
			Name:            name,
			Type:            NodeResourceType,
			Namespace:       nsNode.Namespace,
			ResourceType:    name,
			ResourceGroup:   ri.Group,
			ResourceVersion: ri.Version,
			CreationTime:    time.Now(),
		})
	}
	fs.inodes.RemoveChildren(nsNode.ID, keep)
	return nil
}

func (fs *KubeFS) populateObjects(ctx context.Context, rtNode *Inode) error {
	ri := k8s.ResourceInfo{
		Group:    rtNode.ResourceGroup,
		Version:  rtNode.ResourceVersion,
		Resource: rtNode.ResourceType,
	}

	objects, err := fs.client.ListObjects(ctx, rtNode.Namespace, ri)
	if err != nil {
		fs.logger.Printf("listing %s in %s: %v", rtNode.ResourceType, rtNode.Namespace, err)
		return fuse.EIO
	}

	keep := make(map[string]bool, len(objects))
	for _, obj := range objects {
		keep[obj.Name] = true
		fs.inodes.AddChild(rtNode.ID, &Inode{
			Name:            obj.Name,
			Type:            NodeObject,
			Namespace:       rtNode.Namespace,
			ResourceType:    rtNode.ResourceType,
			ObjectName:      obj.Name,
			ResourceGroup:   rtNode.ResourceGroup,
			ResourceVersion: rtNode.ResourceVersion,
			CreationTime:    obj.CreationTimestamp,
		})
	}
	fs.inodes.RemoveChildren(rtNode.ID, keep)
	return nil
}

func (fs *KubeFS) populateActionFiles(objNode *Inode) error {
	actions := ActionFiles(objNode.ResourceType)
	keep := make(map[string]bool, len(actions))
	for _, action := range actions {
		keep[action] = true
		fs.inodes.AddChild(objNode.ID, &Inode{
			Name:            action,
			Type:            NodeFile,
			Namespace:       objNode.Namespace,
			ResourceType:    objNode.ResourceType,
			ObjectName:      objNode.ObjectName,
			FileName:        action,
			ResourceGroup:   objNode.ResourceGroup,
			ResourceVersion: objNode.ResourceVersion,
			CreationTime:    objNode.CreationTime,
		})
	}

	if HasDataDir(objNode.ResourceType) {
		keep["data"] = true
		fs.inodes.AddChild(objNode.ID, &Inode{
			Name:            "data",
			Type:            NodeDataDir,
			Namespace:       objNode.Namespace,
			ResourceType:    objNode.ResourceType,
			ObjectName:      objNode.ObjectName,
			ResourceGroup:   objNode.ResourceGroup,
			ResourceVersion: objNode.ResourceVersion,
			CreationTime:    objNode.CreationTime,
		})
	}

	fs.inodes.RemoveChildren(objNode.ID, keep)
	return nil
}

func (fs *KubeFS) populateDataKeys(ctx context.Context, dataDirNode *Inode) error {
	ri := k8s.ResourceInfo{
		Group:    dataDirNode.ResourceGroup,
		Version:  dataDirNode.ResourceVersion,
		Resource: dataDirNode.ResourceType,
	}

	dataMap, err := fs.client.GetObjectData(ctx, dataDirNode.Namespace, ri, dataDirNode.ObjectName)
	if err != nil {
		fs.logger.Printf("getting data for %s/%s: %v", dataDirNode.ResourceType, dataDirNode.ObjectName, err)
		return fuse.EIO
	}

	keep := make(map[string]bool, len(dataMap))
	for key := range dataMap {
		keep[key] = true
		fs.inodes.AddChild(dataDirNode.ID, &Inode{
			Name:            key,
			Type:            NodeDataFile,
			Namespace:       dataDirNode.Namespace,
			ResourceType:    dataDirNode.ResourceType,
			ObjectName:      dataDirNode.ObjectName,
			FileName:        key,
			ResourceGroup:   dataDirNode.ResourceGroup,
			ResourceVersion: dataDirNode.ResourceVersion,
			CreationTime:    dataDirNode.CreationTime,
		})
	}
	fs.inodes.RemoveChildren(dataDirNode.ID, keep)
	return nil
}

func (fs *KubeFS) childEntry(ctx context.Context, node *Inode) fuseops.ChildInodeEntry {
	size := uint64(0)
	if node.Type == NodeFile || node.Type == NodeDataFile {
		size = uint64(len(fs.fetchContent(ctx, node)))
	}
	return fuseops.ChildInodeEntry{
		Child:                node.ID,
		Attributes:           fs.inodeAttrs(node, size),
		AttributesExpiration: time.Now().Add(5 * time.Second),
		EntryExpiration:      time.Now().Add(5 * time.Second),
	}
}

const contentTTL = 30 * time.Second

// fetchContent returns cached file content, fetching from the API if stale.
func (fs *KubeFS) fetchContent(ctx context.Context, node *Inode) []byte {
	fs.contentMu.Lock()
	cc, ok := fs.contentCache[node.ID]
	if ok && time.Since(cc.fetchedAt) < contentTTL {
		fs.contentMu.Unlock()
		return cc.data
	}
	fs.contentMu.Unlock()

	data, err := fs.content.GetContent(ctx, node)
	if err != nil {
		fs.logger.Printf("fetchContent: %s/%s/%s/%s: %v",
			node.Namespace, node.ResourceType, node.ObjectName, node.FileName, err)
		data = []byte(fmt.Sprintf("Error: %v\n", err))
	}

	fs.contentMu.Lock()
	fs.contentCache[node.ID] = &cachedContent{data: data, fetchedAt: time.Now()}
	fs.contentMu.Unlock()

	return data
}

func (fs *KubeFS) inodeAttrs(node *Inode, size uint64) fuseops.InodeAttributes {
	now := time.Now()
	nlink := uint32(1)
	if node.IsDir() {
		nlink = 2
	}
	uid := uint32(os.Getuid())
	gid := uint32(os.Getgid())

	return fuseops.InodeAttributes{
		Size:  size,
		Nlink: nlink,
		Mode:  node.ModeWithVerbs(fs.verbs.Update),
		Atime: now,
		Mtime: node.CreationTime,
		Ctime: node.CreationTime,
		Uid:   uid,
		Gid:   gid,
	}
}
