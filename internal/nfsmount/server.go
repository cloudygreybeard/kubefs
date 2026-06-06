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

package nfsmount

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"runtime"

	nfs "github.com/willscott/go-nfs"
	nfshelper "github.com/willscott/go-nfs/helpers"

	"github.com/cloudygreybeard/kubefs/internal/kubefs"
)

// Server manages the embedded NFSv3 server and macOS mount lifecycle.
type Server struct {
	fs         *kubefs.FS
	listener   net.Listener
	mountpoint string
	logger     *log.Logger
	cancel     context.CancelFunc
}

// Mount starts an NFSv3 server on localhost and mounts it at the given
// mount point using the operating system's built-in NFS client.
func Mount(ctx context.Context, kfs *kubefs.FS, mountpoint, addr string, logger *log.Logger) (*Server, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("starting NFS listener: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port
	logger.Printf("NFS server listening on 127.0.0.1:%d", port)

	ctx, cancel := context.WithCancel(ctx)

	billyFS := NewBillyFS(ctx, kfs)
	handler := nfshelper.NewNullAuthHandler(billyFS)
	cacheHelper := nfshelper.NewCachingHandler(handler, 1024)

	go func() {
		if err := nfs.Serve(listener, cacheHelper); err != nil {
			logger.Printf("NFS server error: %v", err)
		}
	}()

	if err := mountNFS(mountpoint, port); err != nil {
		cancel()
		listener.Close()
		return nil, fmt.Errorf("mounting NFS: %w", err)
	}

	return &Server{
		fs:         kfs,
		listener:   listener,
		mountpoint: mountpoint,
		logger:     logger,
		cancel:     cancel,
	}, nil
}

// Unmount unmounts the filesystem and stops the NFS server.
func (s *Server) Unmount() error {
	var mountErr error
	if runtime.GOOS == "darwin" {
		mountErr = exec.Command("umount", s.mountpoint).Run()
	} else {
		mountErr = exec.Command("umount", s.mountpoint).Run()
	}
	s.cancel()
	s.listener.Close()
	return mountErr
}

// Wait blocks until the context is cancelled.
func (s *Server) Wait(ctx context.Context) {
	<-ctx.Done()
}

func mountNFS(mountpoint string, port int) error {
	if err := os.MkdirAll(mountpoint, 0o755); err != nil {
		return fmt.Errorf("creating mount point: %w", err)
	}

	opts := fmt.Sprintf("port=%d,mountport=%d", port, port)

	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("mount", "-o", opts, "-t", "nfs", "localhost:/", mountpoint)
	} else {
		opts += ",nfsvers=3,noacl,tcp"
		cmd = exec.Command("mount", "-o", opts, "-t", "nfs", "localhost:/", mountpoint)
	}

	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mount command failed: %w", err)
	}
	return nil
}
