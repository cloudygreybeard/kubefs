# kubefs

Mount Kubernetes cluster resources as a local filesystem.

![kubefs Demo](docs/kubefs-demo.svg)

Browse namespaces, resource types, and objects as directories. Read or edit
their YAML/JSON representations as regular files. Saving a modified file
applies the change to the cluster.

## Prerequisites

### Linux

Install FUSE 3:

```bash
# Debian/Ubuntu
sudo apt install fuse3

# Fedora/RHEL
sudo dnf install fuse3
```

On Linux systems without FUSE, kubefs can also use its embedded NFS
server as a fallback: `kubefs mount --transport=nfs /mnt/k8s`.

### macOS

No additional dependencies. kubefs embeds an NFS server and mounts via
macOS's built-in NFS client.

## Installation

### Homebrew (macOS/Linux)

```bash
brew install cloudygreybeard/tap/kubefs
```

### Go install

```bash
go install github.com/cloudygreybeard/kubefs@latest
```

### From source

```bash
git clone https://github.com/cloudygreybeard/kubefs
cd kubefs
make install
```

## Usage

```bash
mkdir -p /tmp/k8s
kubefs mount /tmp/k8s
```

The process backgrounds itself by default. Use `-f` to stay in the foreground:

```bash
kubefs mount -f /tmp/k8s
```

Then browse the filesystem:

```bash
ls /tmp/k8s/
# default  kube-system  kube-public  ...

ls /tmp/k8s/default/pods/
# coredns-5dd5756b68-abcde  ...

cat /tmp/k8s/default/pods/coredns-5dd5756b68-abcde/yaml

cat /tmp/k8s/default/pods/coredns-5dd5756b68-abcde/describe

cat /tmp/k8s/default/pods/coredns-5dd5756b68-abcde/logs
```

ConfigMap and Secret data keys are exposed as individual files under a
`data/` subdirectory. Secret values are automatically base64-decoded:

```bash
cat /tmp/k8s/default/configmaps/my-config/data/app.conf

cat /tmp/k8s/default/secrets/my-secret/data/password
```

Unmount:

```bash
umount /tmp/k8s
```

## Access control

The filesystem is **read-only by default**. Mutating operations must be
explicitly enabled with `--enable-verbs`:

```bash
# Read-only (default, safe for browsing)
kubefs mount /tmp/k8s

# Enable updating existing objects
kubefs mount --enable-verbs update /tmp/k8s

# Enable creating new objects and updating existing ones
kubefs mount --enable-verbs create,update /tmp/k8s

# Full CRUD
kubefs mount --enable-verbs create,update,delete /tmp/k8s
```

Valid verbs: `create`, `update`, `delete`. Read is always enabled.

When verbs are enabled, the corresponding filesystem operations become
available:

- **update**: Edit `yaml`, `json`, or `data/KEY` files and save to apply
  changes to the cluster.
- **create**: Create a `.yaml` or `.json` file under a resource type
  directory (e.g. `/default/configmaps/new-config.yaml`), write valid
  Kubernetes object content, and save to create the object.
- **delete**: Remove an object directory with `rmdir` to delete the object
  from the cluster.

### Rationale

The read-only default is a safety measure. kubefs exposes the entire
cluster as a local filesystem, and standard Unix tools like `rm`, `mv`,
and editors with auto-save can trigger destructive operations. Making
writes opt-in prevents accidental mutations.

The verb-based model follows Kubernetes RBAC vocabulary (`create`,
`update`, `delete`) that cluster operators already know. Each verb is
an independent capability: you can enable `create` without `delete`, or
`update` without `create`.

For fine-grained access control beyond what `--enable-verbs` provides
(e.g. restricting writes to specific namespaces or resource types), scope
the kubeconfig's RBAC. kubefs faithfully surfaces whatever the API
server permits.

### Flags

```
kubefs mount MOUNTPOINT [flags]

Flags:
  -f, --foreground             run in the foreground (default: daemonize)
  --kubeconfig string          path to kubeconfig (default: $KUBECONFIG or ~/.kube/config)
  --context string             kubernetes context to use
  --cache-ttl duration         cache TTL for API responses (default: 30s)
  --enable-verbs stringSlice   mutating verbs to enable (create,update,delete)
  --debug                      enable FUSE debug logging
```

## A note on imperative changes

When write verbs are enabled, kubefs permits imperative, ad-hoc
modifications to live cluster resources using standard Unix tools. This
is a deliberate design choice, not an oversight.

Modern Kubernetes best practice favours declarative workflows: GitOps
pipelines, pull-request-driven reviews, and controller reconciliation
loops. These remain the recommended approach for routine configuration
management. kubefs is not a substitute for them.

There are situations, however, where imperative access is the pragmatic
choice. An incident responder scaling a deployment at 3 a.m. should not
be blocked on a CI pipeline. An SRE inspecting and patching a
misconfigured secret during an outage needs the shortest path between
diagnosis and remediation. A developer iterating on a CRD in a throwaway
namespace gains little from a full apply cycle.

Every experienced operator keeps imperative tools on the belt --
`kubectl edit`, `kubectl patch`, `kubectl delete`— and reaches for
them when the situation calls for speed over ceremony. kubefs extends
that same capability to the broader Unix toolkit: `grep`, `sed`, `diff`,
`cp`, any editor or script that operates on files, and AI agents or MCP
servers that interact with the filesystem.

The access control model (read-only by default, verbs explicitly
opted-in, scoped by Kubernetes RBAC) provides the guardrails. Standard
cautions apply when the underlying kubeconfig carries broadly scoped
write permissions— the same cautions that apply to `kubectl` or any
other client operating under that identity. kubefs trusts operators to
choose the right tool for the task at hand.

## Filesystem hierarchy

```
MOUNTPOINT/
  NAMESPACE/
    RESOURCE_TYPE/
      OBJECT_NAME/
        yaml         read; write with --enable-verbs update
        json         read; write with --enable-verbs update
        describe     read-only
        logs         read-only, pods only
        data/        configmaps and secrets only
          KEY        read; write with --enable-verbs update
```

Resource types are discovered dynamically from the cluster API, including CRDs.

## How it works

On Linux, kubefs uses kernel FUSE (`/dev/fuse`) via
[hanwen/go-fuse](https://github.com/hanwen/go-fuse) — the standard,
well-supported path for userspace filesystems.

On macOS, kubefs takes a simpler approach: it embeds a lightweight
NFSv3 server (via [willscott/go-nfs](https://github.com/willscott/go-nfs))
and mounts it through macOS's built-in NFS client. This avoids any
external dependencies and works out of the box on all recent macOS
versions. As the macOS userspace filesystem ecosystem continues to
mature — particularly Apple's FSKit framework — kubefs may adopt
native FUSE support on macOS in a future release.

The transport is selected automatically but can be overridden:

```bash
kubefs mount /mnt/k8s                      # auto-detect (FUSE on Linux, NFS on macOS)
kubefs mount /mnt/k8s --transport=fuse     # force kernel FUSE
kubefs mount /mnt/k8s --transport=nfs      # force embedded NFS server
```

When using the NFS transport, kubefs binds to `127.0.0.1` on a random
high port. The listener is localhost-only and not reachable from the
network. The NFS server exposes only what the configured kubeconfig's
RBAC permits.

## Cross-compilation

The project uses pure Go with no cgo dependencies, so cross-compilation works
out of the box:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o kubefs .
```

## Development

```bash
make build    # Build the binary
make test     # Run tests
make lint     # Run linter
make clean    # Remove build artifacts
```

## Demo and end-to-end testing

The demo infrastructure doubles as an end-to-end test suite of sorts.
Two separate container images— `mock-kubeapi` and `kubefs-demo`— are
connected via a shared container network, exercising the same code paths
that run against a real Kubernetes cluster.

The demo script performs real FUSE mounts, reads objects through the
Kubernetes API client, decodes base64-encoded secret data, enforces
access control (verifying that writes are rejected in read-only mode),
rotates a TLS certificate by writing through the filesystem, and edits
a deployment's image tag with `sed`. Every operation flows through the
full stack: Go binary, FUSE layer, Kubernetes dynamic client, HTTP
transport, and API response handling.

This design has caught real bugs that unit tests would not surface --
for example, a file truncation issue that only manifests when a shorter
file overwrites a longer one through the FUSE write path, exactly the
kind of interaction that occurs naturally when standard Unix tools
operate on the filesystem.

```bash
make demo-build   # Build both container images
make demo-run     # Run the demo interactively (no recording)
make demo         # Record the demo animation with asciinema
```

### mock-kubeapi

The `mock-kubeapi` image is a standalone, reusable mock Kubernetes API
server. It serves canned responses for namespace, pod, deployment,
configmap, secret, and service resources, supports PUT for object
updates, and generates TLS credentials at runtime to avoid storing key
material in source code.

```bash
podman run -p 6443:6443 mock-kubeapi
```

It is useful beyond the demo: local development without a real cluster,
CI integration tests, or as a lightweight fake API for any tool that
speaks the Kubernetes REST interface.

### Retargeting

The demo container accepts an external API endpoint. To run against a
real cluster instead of the mock:

```bash
podman run --rm -it --privileged \
  -v ~/.kube/config:/home/demo/.kube/config:ro \
  kubefs-demo
```

See `hack/demo/` for the Containerfiles, mock API server, and demo
script.

## License

Apache 2.0. See [LICENSE](LICENSE).
