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

package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/cloudygreybeard/kubefs/internal/fusemount"
	"github.com/cloudygreybeard/kubefs/internal/k8s"
	"github.com/cloudygreybeard/kubefs/internal/kubefs"
	"github.com/cloudygreybeard/kubefs/internal/nfsmount"
)

const (
	daemonEnv   = "_KUBEFS_DAEMON"
	statusFDEnv = "_KUBEFS_STATUS_FD"
)

var (
	cacheTTL    time.Duration
	debug       bool
	foreground  bool
	enableVerbs []string
	transport   string
	nfsAddr     string
)

var mountCmd = &cobra.Command{
	Use:   "mount MOUNTPOINT",
	Short: "Mount the Kubernetes filesystem",
	Long: `Mount a filesystem that exposes Kubernetes objects as files.

On Linux, this uses kernel FUSE (/dev/fuse) by default.
On macOS, this uses an embedded NFS server (no FUSE required).

Use --transport to override the auto-detected transport:

  kubefs mount /mnt/k8s                          # auto-detect
  kubefs mount /mnt/k8s --transport=fuse         # force kernel FUSE
  kubefs mount /mnt/k8s --transport=nfs          # force NFS

By default the filesystem is read-only. Use --enable-verbs to opt in
to mutating operations:

  kubefs mount /mnt/k8s                                        # read-only
  kubefs mount --enable-verbs update /mnt/k8s                  # update existing
  kubefs mount --enable-verbs create,update /mnt/k8s           # create + update
  kubefs mount --enable-verbs create,update,delete /mnt/k8s    # full CRUD

The filesystem hierarchy is:

  MOUNTPOINT/
    NAMESPACE/
      RESOURCE_TYPE/
        OBJECT_NAME/
          yaml       (read; write with --enable-verbs update)
          json       (read; write with --enable-verbs update)
          describe   (read-only)
          logs       (read-only, pods only)
          data/      (configmaps and secrets)
            KEY      (read; write with --enable-verbs update)`,
	Args: cobra.ExactArgs(1),
	RunE: runMount,
}

func init() {
	mountCmd.Flags().DurationVar(&cacheTTL, "cache-ttl", 30*time.Second, "cache TTL for API responses")
	mountCmd.Flags().BoolVar(&debug, "debug", false, "enable debug logging")
	mountCmd.Flags().BoolVarP(&foreground, "foreground", "f", false, "run in the foreground (default: daemonize)")
	mountCmd.Flags().StringSliceVar(&enableVerbs, "enable-verbs", nil, "mutating verbs to enable (create,update,delete)")
	mountCmd.Flags().StringVar(&transport, "transport", "auto", "mount transport: auto, fuse, or nfs")
	mountCmd.Flags().StringVar(&nfsAddr, "nfs-addr", "127.0.0.1:0", "NFS server listen address (transport=nfs only)")

	rootCmd.AddCommand(mountCmd)
}

func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}

func resolveTransport() string {
	if transport != "auto" {
		return transport
	}
	if runtime.GOOS == "darwin" {
		return "nfs"
	}
	if _, err := os.Stat("/dev/fuse"); err == nil {
		return "fuse"
	}
	return "nfs"
}

func runMount(cmd *cobra.Command, args []string) error {
	mountpoint := expandTilde(args[0])

	kubeconfig, _ := cmd.Flags().GetString("kubeconfig")
	kubeconfig = expandTilde(kubeconfig)
	kubecontext, _ := cmd.Flags().GetString("context")

	verbs, err := kubefs.ParseVerbs(enableVerbs)
	if err != nil {
		return err
	}

	logger := log.New(os.Stderr, "kubefs: ", log.LstdFlags)

	client, err := k8s.NewClient(kubeconfig, kubecontext, cacheTTL)
	if err != nil {
		return fmt.Errorf("creating kubernetes client: %w", err)
	}

	ctx := context.Background()
	if _, err := client.ListNamespaces(ctx); err != nil {
		return fmt.Errorf("connecting to cluster: %w", err)
	}

	if !foreground && os.Getenv(daemonEnv) == "" {
		return daemonize(logger)
	}

	resolved := resolveTransport()
	logger.Printf("using %s transport", resolved)

	switch resolved {
	case "nfs":
		return serveNFS(ctx, client, mountpoint, logger, verbs)
	case "fuse":
		return serveFUSE(ctx, client, mountpoint, logger, verbs)
	default:
		return fmt.Errorf("unknown transport %q (valid: auto, fuse, nfs)", resolved)
	}
}

func serveNFS(ctx context.Context, client *k8s.Client, mountpoint string, logger *log.Logger, verbs kubefs.AllowedVerbs) error {
	kfs := kubefs.New(client, logger, verbs)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	srv, err := nfsmount.Mount(ctx, kfs, mountpoint, nfsAddr, logger)

	reportMountStatus(err)

	if err != nil {
		return fmt.Errorf("mounting on %s: %w", mountpoint, err)
	}

	logger.Printf("mounted on %s (pid %d, %s, nfs)", mountpoint, os.Getpid(), verbs)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Printf("received %v, unmounting...", sig)
		if err := srv.Unmount(); err != nil {
			logger.Printf("unmount failed: %v; forcing exit", err)
			os.Exit(1)
		}
		cancel()
	}()

	srv.Wait(ctx)
	logger.Println("unmounted")
	return nil
}

func serveFUSE(ctx context.Context, client *k8s.Client, mountpoint string, logger *log.Logger, verbs kubefs.AllowedVerbs) error {
	kfs := kubefs.New(client, logger, verbs)

	server, err := fusemount.Mount(kfs, mountpoint, verbs.ReadOnly(), debug, logger)

	reportMountStatus(err)

	if err != nil {
		return fmt.Errorf("mounting on %s: %w", mountpoint, err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Printf("received %v, unmounting...", sig)
		if err := server.Unmount(); err != nil {
			logger.Printf("unmount failed: %v; forcing exit", err)
			os.Exit(1)
		}
		sig = <-sigCh
		logger.Printf("received %v again, forcing exit", sig)
		os.Exit(1)
	}()

	server.Wait()
	logger.Println("unmounted")
	return nil
}

func daemonize(logger *log.Logger) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding executable path: %w", err)
	}

	statusR, statusW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("creating status pipe: %w", err)
	}

	child := exec.Command(exe, os.Args[1:]...)
	child.Env = append(os.Environ(), daemonEnv+"=1")
	child.ExtraFiles = []*os.File{statusW}
	child.Env = append(child.Env, fmt.Sprintf("%s=%d", statusFDEnv, 3))
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	logFile, err := os.CreateTemp("", "kubefs-*.log")
	if err != nil {
		statusR.Close()
		statusW.Close()
		return fmt.Errorf("creating log file: %w", err)
	}
	child.Stderr = logFile
	child.Stdout = logFile

	if err := child.Start(); err != nil {
		logFile.Close()
		statusR.Close()
		statusW.Close()
		return fmt.Errorf("starting background process: %w", err)
	}

	statusW.Close()

	buf := make([]byte, 1024)
	n, _ := statusR.Read(buf)
	statusR.Close()

	msg := string(buf[:n])
	if msg != "ok" {
		_ = child.Wait()
		logFile.Close()
		if msg == "" {
			return fmt.Errorf("background process exited before mounting (log: %s)", logFile.Name())
		}
		return fmt.Errorf("%s", msg)
	}

	logger.Printf("mounted on %s (pid %d)", os.Args[len(os.Args)-1], child.Process.Pid)

	_ = child.Process.Release()
	logFile.Close()
	return nil
}

func reportMountStatus(mountErr error) {
	fdStr := os.Getenv(statusFDEnv)
	if fdStr == "" {
		return
	}
	fd := 3
	_, _ = fmt.Sscanf(fdStr, "%d", &fd)
	w := os.NewFile(uintptr(fd), "status-pipe")
	if w == nil {
		return
	}
	defer w.Close()
	if mountErr != nil {
		fmt.Fprintf(w, "%v", mountErr)
	} else {
		fmt.Fprint(w, "ok")
	}
}
