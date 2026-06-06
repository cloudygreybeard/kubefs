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

// Package kubefs implements the core Kubernetes virtual filesystem,
// independent of any mount transport (FUSE, NFS, etc.).
package kubefs

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

	"github.com/cloudygreybeard/kubefs/internal/k8s"
)

const contentTTL = 30 * time.Second

// Attr holds filesystem attributes for a node.
type Attr struct {
	Size  uint64
	Nlink uint32
	Mode  os.FileMode
	Atime time.Time
	Mtime time.Time
	Ctime time.Time
	Uid   uint32
	Gid   uint32
}

// StatFS holds filesystem-level statistics.
type StatFS struct {
	BlockSize       uint32
	IoSize          uint32
	Blocks          uint64
	BlocksFree      uint64
	BlocksAvailable uint64
	Inodes          uint64
	InodesFree      uint64
}

type cachedContent struct {
	data      []byte
	fetchedAt time.Time
}

type openFile struct {
	node    *Inode
	buf     []byte
	dirty   bool
	pending bool
}

// FS is the transport-agnostic Kubernetes virtual filesystem.
type FS struct {
	client  *k8s.Client
	content *ContentProvider
	inodes  *InodeTable
	logger  *log.Logger
	verbs   AllowedVerbs

	handleMu   sync.Mutex
	nextHandle HandleID
	openFiles  map[HandleID]*openFile

	contentMu    sync.Mutex
	contentCache map[InodeID]*cachedContent
}

// New creates a new Kubernetes virtual filesystem.
func New(client *k8s.Client, logger *log.Logger, verbs AllowedVerbs) *FS {
	return &FS{
		client:       client,
		content:      NewContentProvider(client),
		inodes:       NewInodeTable(),
		logger:       logger,
		verbs:        verbs,
		nextHandle:   1,
		openFiles:    make(map[HandleID]*openFile),
		contentCache: make(map[InodeID]*cachedContent),
	}
}

// Verbs returns the configured access control verbs.
func (fs *FS) Verbs() AllowedVerbs { return fs.verbs }

func (fs *FS) allocHandle() HandleID {
	fs.handleMu.Lock()
	defer fs.handleMu.Unlock()
	h := fs.nextHandle
	fs.nextHandle++
	return h
}

// StatFS returns filesystem-level statistics.
func (fs *FS) StatFS() StatFS {
	return StatFS{
		BlockSize: 4096,
		IoSize:    65536,
	}
}

// Lookup finds a child node by name under the given parent.
func (fs *FS) Lookup(ctx context.Context, parentID InodeID, name string) (*Inode, error) {
	parent := fs.inodes.Get(parentID)
	if parent == nil {
		return nil, syscall.ENOENT
	}

	if err := fs.ensureChildren(ctx, parent); err != nil {
		return nil, err
	}

	child := fs.inodes.LookupChild(parentID, name)
	if child == nil {
		return nil, syscall.ENOENT
	}

	return child, nil
}

// GetInode returns a node by its ID.
func (fs *FS) GetInode(id InodeID) *Inode {
	return fs.inodes.Get(id)
}

// Children returns sorted child nodes for a directory.
func (fs *FS) Children(ctx context.Context, id InodeID) ([]*Inode, error) {
	node := fs.inodes.Get(id)
	if node == nil {
		return nil, syscall.ENOENT
	}
	if err := fs.ensureChildren(ctx, node); err != nil {
		return nil, err
	}
	children := fs.inodes.Children(id)
	sort.Slice(children, func(i, j int) bool { return children[i].Name < children[j].Name })
	return children, nil
}

// GetAttr returns filesystem attributes for a node.
func (fs *FS) GetAttr(ctx context.Context, id InodeID) (Attr, error) {
	node := fs.inodes.Get(id)
	if node == nil {
		return Attr{}, syscall.ENOENT
	}

	size := uint64(0)
	if node.Type == NodeFile || node.Type == NodeDataFile {
		size = uint64(len(fs.FetchContent(ctx, node)))
	}

	return fs.nodeAttr(node, size), nil
}

// Truncate resizes open file buffers for a node.
func (fs *FS) Truncate(id InodeID, size uint64) error {
	if fs.verbs.ReadOnly() {
		return syscall.EROFS
	}
	node := fs.inodes.Get(id)
	if node == nil {
		return syscall.ENOENT
	}

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
	return nil
}

// Remove deletes a Kubernetes object (directories) or abandons a
// pending create (files under resource type directories).
func (fs *FS) Remove(ctx context.Context, parentID InodeID, name string, isDir bool) error {
	child := fs.inodes.LookupChild(parentID, name)
	if child == nil {
		return syscall.ENOENT
	}

	if isDir {
		if !fs.verbs.Delete {
			return syscall.EPERM
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
			fs.logger.Printf("Remove: deleting %s/%s: %v", child.ResourceType, child.ObjectName, err)
			return syscall.EIO
		}

		fs.inodes.RemoveChild(parentID, name)
		return nil
	}

	parent := fs.inodes.Get(parentID)
	if parent != nil && parent.Type == NodeResourceType {
		fs.inodes.RemoveChild(parentID, name)
		return nil
	}

	return syscall.EPERM
}

// CreateAndOpen creates a new file under a resource type directory
// and returns an open handle for writing.
func (fs *FS) CreateAndOpen(parentID InodeID, name string) (*Inode, HandleID, error) {
	if !fs.verbs.Create {
		return nil, 0, syscall.EROFS
	}

	parent := fs.inodes.Get(parentID)
	if parent == nil {
		return nil, 0, syscall.ENOENT
	}
	if parent.Type != NodeResourceType {
		return nil, 0, syscall.EPERM
	}

	if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".json") {
		return nil, 0, syscall.EPERM
	}

	child := fs.inodes.AddChild(parentID, &Inode{
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

	return child, handle, nil
}

// OpenFile opens a file and returns a handle for reading and writing.
func (fs *FS) OpenFile(ctx context.Context, id InodeID) (HandleID, error) {
	node := fs.inodes.Get(id)
	if node == nil {
		return 0, syscall.ENOENT
	}

	handle := fs.allocHandle()
	data := fs.FetchContent(ctx, node)

	fs.handleMu.Lock()
	fs.openFiles[handle] = &openFile{
		node: node,
		buf:  append([]byte(nil), data...),
	}
	fs.handleMu.Unlock()

	return handle, nil
}

// ReadHandle reads data from an open file handle.
func (fs *FS) ReadHandle(handle HandleID, offset int64, buf []byte) (int, error) {
	fs.handleMu.Lock()
	of, ok := fs.openFiles[handle]
	fs.handleMu.Unlock()

	if !ok {
		return 0, syscall.EIO
	}

	if offset >= int64(len(of.buf)) {
		return 0, nil
	}

	end := offset + int64(len(buf))
	if end > int64(len(of.buf)) {
		end = int64(len(of.buf))
	}

	return copy(buf, of.buf[offset:end]), nil
}

// WriteHandle writes data to an open file handle.
func (fs *FS) WriteHandle(handle HandleID, offset int64, data []byte) (int, error) {
	fs.handleMu.Lock()
	of, ok := fs.openFiles[handle]
	fs.handleMu.Unlock()

	if !ok {
		return 0, syscall.EIO
	}

	if of.pending {
		if !fs.verbs.Create {
			return 0, syscall.EROFS
		}
	} else if !fs.verbs.Update {
		return 0, syscall.EROFS
	}

	switch of.node.Type {
	case NodeDataFile:
		// writable
	case NodeFile:
		if of.node.FileName != "yaml" && of.node.FileName != "json" {
			return 0, syscall.EPERM
		}
	default:
		return 0, syscall.EPERM
	}

	end := int(offset) + len(data)
	if end > len(of.buf) {
		grown := make([]byte, end)
		copy(grown, of.buf)
		of.buf = grown
	}
	copy(of.buf[offset:], data)
	of.dirty = true

	return len(data), nil
}

// FlushHandle writes dirty data back to the Kubernetes API.
func (fs *FS) FlushHandle(ctx context.Context, handle HandleID) error {
	fs.handleMu.Lock()
	of, ok := fs.openFiles[handle]
	fs.handleMu.Unlock()

	if !ok || !of.dirty {
		return nil
	}

	if of.pending {
		if !fs.verbs.Create {
			return syscall.EROFS
		}
		if err := fs.content.CreateContent(ctx, of.node, of.buf); err != nil {
			fs.logger.Printf("FlushHandle: error creating object: %v", err)
			return syscall.EIO
		}
	} else {
		if !fs.verbs.Update {
			return syscall.EROFS
		}
		if err := fs.content.ApplyContent(ctx, of.node, of.buf); err != nil {
			fs.logger.Printf("FlushHandle: error applying content: %v", err)
			return syscall.EIO
		}
	}

	of.dirty = false
	of.pending = false

	fs.contentMu.Lock()
	delete(fs.contentCache, of.node.ID)
	fs.contentMu.Unlock()

	return nil
}

// ReleaseHandle closes an open file handle.
func (fs *FS) ReleaseHandle(handle HandleID) {
	fs.handleMu.Lock()
	delete(fs.openFiles, handle)
	fs.handleMu.Unlock()
}

// FetchContent returns cached file content, fetching from the API if stale.
func (fs *FS) FetchContent(ctx context.Context, node *Inode) []byte {
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

func (fs *FS) nodeAttr(node *Inode, size uint64) Attr {
	now := time.Now()
	nlink := uint32(1)
	if node.IsDir() {
		nlink = 2
	}
	return Attr{
		Size:  size,
		Nlink: nlink,
		Mode:  node.ModeWithVerbs(fs.verbs.Update),
		Atime: now,
		Mtime: node.CreationTime,
		Ctime: node.CreationTime,
		Uid:   uint32(os.Getuid()),
		Gid:   uint32(os.Getgid()),
	}
}

func (fs *FS) ensureChildren(ctx context.Context, node *Inode) error {
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

func (fs *FS) populateNamespaces(ctx context.Context, root *Inode) error {
	namespaces, err := fs.client.ListNamespaces(ctx)
	if err != nil {
		fs.logger.Printf("listing namespaces: %v", err)
		return syscall.EIO
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

func (fs *FS) populateResourceTypes(ctx context.Context, nsNode *Inode) error {
	resources, err := fs.client.DiscoverResources(ctx)
	if err != nil {
		fs.logger.Printf("discovering resources: %v", err)
		return syscall.EIO
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

func (fs *FS) populateObjects(ctx context.Context, rtNode *Inode) error {
	ri := k8s.ResourceInfo{
		Group:    rtNode.ResourceGroup,
		Version:  rtNode.ResourceVersion,
		Resource: rtNode.ResourceType,
	}

	objects, err := fs.client.ListObjects(ctx, rtNode.Namespace, ri)
	if err != nil {
		fs.logger.Printf("listing %s in %s: %v", rtNode.ResourceType, rtNode.Namespace, err)
		return syscall.EIO
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

func (fs *FS) populateActionFiles(objNode *Inode) error {
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

func (fs *FS) populateDataKeys(ctx context.Context, dataDirNode *Inode) error {
	ri := k8s.ResourceInfo{
		Group:    dataDirNode.ResourceGroup,
		Version:  dataDirNode.ResourceVersion,
		Resource: dataDirNode.ResourceType,
	}

	dataMap, err := fs.client.GetObjectData(ctx, dataDirNode.Namespace, ri, dataDirNode.ObjectName)
	if err != nil {
		fs.logger.Printf("getting data for %s/%s: %v", dataDirNode.ResourceType, dataDirNode.ObjectName, err)
		return syscall.EIO
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
