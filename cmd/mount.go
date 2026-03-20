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
	"runtime"
	"syscall"
	"time"

	"github.com/jacobsa/fuse"
	"github.com/jacobsa/fuse/fuseutil"
	"github.com/spf13/cobra"

	kubefs "github.com/cloudygreybeard/kubefs/internal/fs"
	"github.com/cloudygreybeard/kubefs/internal/k8s"
)

const (
	daemonEnv = "_KUBEFS_DAEMON"
	// File descriptor number for the status pipe passed from parent to child.
	statusFDEnv = "_KUBEFS_STATUS_FD"
)

var (
	cacheTTL    time.Duration
	debug       bool
	foreground  bool
	enableVerbs []string
)

var mountCmd = &cobra.Command{
	Use:   "mount MOUNTPOINT",
	Short: "Mount the Kubernetes filesystem",
	Long: `Mount a FUSE filesystem that exposes Kubernetes objects as files.

On macOS, this uses FUSE-T (kext-less, NFS-backed).
On Linux, this uses kernel FUSE (/dev/fuse).

By default the filesystem is read-only. Use --enable-verbs to opt in
to mutating operations:

  kubefs mount /mnt/k8s                                        # read-only
  kubefs mount --enable-verbs update /mnt/k8s                  # update existing
  kubefs mount --enable-verbs create,update /mnt/k8s           # create + update
  kubefs mount --enable-verbs create,update,delete /mnt/k8s    # full CRUD

Valid verbs: create, update, delete. Read is always enabled.

The process backgrounds itself by default. Use -f / --foreground to
keep it in the foreground.

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
	mountCmd.Flags().BoolVar(&debug, "debug", false, "enable FUSE debug logging")
	mountCmd.Flags().BoolVarP(&foreground, "foreground", "f", false, "run in the foreground (default: daemonize)")
	mountCmd.Flags().StringSliceVar(&enableVerbs, "enable-verbs", nil, "mutating verbs to enable (create,update,delete)")
	rootCmd.AddCommand(mountCmd)
}

func runMount(cmd *cobra.Command, args []string) error {
	mountpoint := args[0]

	kubeconfig, _ := cmd.Flags().GetString("kubeconfig")
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

	// If not --foreground and not already the daemon child, re-exec in
	// the background and exit the parent.
	if !foreground && os.Getenv(daemonEnv) == "" {
		return daemonize(logger)
	}

	return serveFUSE(ctx, client, mountpoint, logger, verbs)
}

// daemonize re-executes the current process in the background. A pipe
// connects the child back to the parent so the parent can report
// whether the mount succeeded or relay the error to the user.
func daemonize(logger *log.Logger) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding executable path: %w", err)
	}

	// Pipe for the child to send mount status back to the parent.
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

	// Close the write end in the parent; only the child writes to it.
	statusW.Close()

	// Wait for the child to report mount status.
	buf := make([]byte, 1024)
	n, _ := statusR.Read(buf)
	statusR.Close()

	msg := string(buf[:n])
	if msg != "ok" {
		// Child failed to mount. Reap it so we don't leave a zombie.
		child.Wait()
		logFile.Close()
		if msg == "" {
			return fmt.Errorf("background process exited before mounting (log: %s)", logFile.Name())
		}
		return fmt.Errorf("%s", msg)
	}

	logger.Printf("mounted on %s (pid %d)", os.Args[len(os.Args)-1], child.Process.Pid)

	child.Process.Release()
	logFile.Close()
	return nil
}

func serveFUSE(ctx context.Context, client *k8s.Client, mountpoint string, logger *log.Logger, verbs kubefs.AllowedVerbs) error {
	fs := kubefs.NewKubeFS(client, logger, verbs)
	server := fuseutil.NewFileSystemServer(fs)

	cfg := &fuse.MountConfig{
		FSName:                    "kubefs",
		VolumeName:                "Kubernetes",
		DisableWritebackCaching:   true,
		DisableDefaultPermissions: true,
		ReadOnly:                  verbs.ReadOnly(),
		ErrorLogger:               logger,
	}

	if debug {
		cfg.DebugLogger = log.New(os.Stderr, "fuse: ", log.LstdFlags)
	}

	mfs, err := fuse.Mount(mountpoint, server, cfg)

	// Report mount status to parent if running as daemon child.
	reportMountStatus(err)

	if err != nil {
		return fmt.Errorf("mounting on %s: %w", mountpoint, err)
	}

	logger.Printf("mounted on %s (pid %d, %s)", mountpoint, os.Getpid(), verbs)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Printf("received %v, unmounting...", sig)
		if err := unmountDir(mountpoint); err != nil {
			logger.Printf("unmount failed: %v; forcing exit", err)
			os.Exit(1)
		}
		sig = <-sigCh
		logger.Printf("received %v again, forcing exit", sig)
		os.Exit(1)
	}()

	if err := mfs.Join(ctx); err != nil {
		return fmt.Errorf("join: %w", err)
	}

	logger.Println("unmounted")
	return nil
}

// reportMountStatus writes the mount outcome to the status pipe so the
// parent process can relay success or the error message to the user.
// In foreground mode (no pipe) this is a no-op.
func reportMountStatus(mountErr error) {
	fdStr := os.Getenv(statusFDEnv)
	if fdStr == "" {
		return
	}
	fd := 3
	fmt.Sscanf(fdStr, "%d", &fd)
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

// unmountDir unmounts the FUSE mount point. On macOS the umount(8)
// command is used because FUSE-T's NFS layer rejects syscall.Unmount.
// On Linux the library's fusermount-based unmount is used.
func unmountDir(dir string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("umount", dir).Run()
	}
	return fuse.Unmount(dir)
}
