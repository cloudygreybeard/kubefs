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
	"github.com/spf13/cobra"
)

// Build-time version information, set via ldflags.
var (
	// Version is the semantic version or "dev" for local builds.
	Version = "dev"
	// Commit is the short git commit hash.
	Commit = "unknown"
	// Date is the UTC build timestamp.
	Date = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "kubefs",
	Short: "Mount Kubernetes objects as a FUSE filesystem",
	Long: `kubefs exposes Kubernetes cluster resources as a local filesystem
using FUSE, allowing you to browse namespaces, resource types, and
objects as directories, and read or edit their YAML/JSON representations
as regular files.

On macOS this uses FUSE-T (kext-less, NFS-backed).
On Linux this uses kernel FUSE (/dev/fuse).`,
}

func init() {
	rootCmd.PersistentFlags().String("kubeconfig", "", "path to kubeconfig file (default: $KUBECONFIG or ~/.kube/config)")
	rootCmd.PersistentFlags().String("context", "", "kubernetes context to use")
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}
