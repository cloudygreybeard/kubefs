#!/usr/bin/env bash
# kubefs demo script
# Runs real kubefs commands against an external Kubernetes API with a
# typing animation for terminal recording. Designed to run inside the
# demo container built from hack/demo/Containerfile.
#
# The API endpoint is set via KUBE_API_SERVER (default:
# http://mock-kubeapi:6443). Override it or mount a real kubeconfig to
# retarget the demo at a real cluster.

set -e

YELLOW='\033[1;33m'
BLUE='\033[0;34m'
GREEN='\033[0;32m'
NC='\033[0m'

TYPE_SPEED="${TYPE_SPEED:-0.020}"
KUBE_API_SERVER="${KUBE_API_SERVER:-http://mock-kubeapi:6443}"

type_command() {
    local cmd="$1"
    printf "${BLUE}\$ ${NC}"
    for (( i=0; i<${#cmd}; i++ )); do
        printf "%s" "${cmd:$i:1}"
        sleep "$TYPE_SPEED"
    done
    printf "\n"
}

run_cmd() {
    local cmd="$1"
    type_command "${cmd}"
    eval "${cmd}"
}

demo_pause() {
    sleep "${1:-1.5}"
}

comment() {
    printf "\n${YELLOW}# %s${NC}\n" "$1"
    demo_pause 0.8
}

# Regenerate kubeconfig when KUBE_API_SERVER differs from the baked-in
# default, or when no config exists. A volume-mounted kubeconfig for
# real cluster demos takes precedence (the baked-in server URL won't
# match, but the mounted file is already correct).
BAKED_SERVER="http://mock-kubeapi:6443"
if [[ ! -f "${HOME}/.kube/config" ]] || \
   [[ "${KUBE_API_SERVER}" != "${BAKED_SERVER}" ]]; then
    mkdir -p "${HOME}/.kube"
    cat > "${HOME}/.kube/config" <<KUBEEOF
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: ${KUBE_API_SERVER}
  name: demo
contexts:
- context:
    cluster: demo
    user: demo
  name: demo
current-context: demo
users:
- name: demo
  user: {}
KUBEEOF
fi

clear

comment "kubefs - mount Kubernetes objects as a filesystem"
demo_pause 2

# --- Version ---
comment "Check the version"
run_cmd "kubefs version"
demo_pause 2

# --- Mount (foreground, background via &) ---
comment "Mount the cluster filesystem (read-only by default)"
type_command "kubefs mount -f /mnt/k8s &"
kubefs mount -f /mnt/k8s &
KUBEFS_PID=$!
sleep 2
demo_pause 1

# --- Browse namespaces ---
comment "Browse namespaces"
run_cmd "ls /mnt/k8s/"
demo_pause 2

# --- Browse resource types ---
comment "Browse resource types in the default namespace"
run_cmd "ls /mnt/k8s/default/"
demo_pause 3

# --- Browse pods ---
comment "List pods"
run_cmd "ls /mnt/k8s/default/pods/"
demo_pause 2

# --- Show pod files ---
comment "Each object has yaml, json, and describe files"
run_cmd "ls /mnt/k8s/default/pods/nginx-7f8b6c5d9-xk2p4/"
demo_pause 2

# --- Cat describe ---
comment "View a pod description"
run_cmd "cat /mnt/k8s/default/pods/nginx-7f8b6c5d9-xk2p4/describe"
demo_pause 4

# --- ConfigMap data keys ---
comment "ConfigMap data keys are exposed as files"
run_cmd "ls /mnt/k8s/default/configmaps/app-config/data/"
demo_pause 2

# --- Read a data key ---
comment "Read a config value directly"
run_cmd "cat /mnt/k8s/default/configmaps/app-config/data/database.url"
demo_pause 2

# --- Secret data (base64 decoded) ---
comment "Secret data keys are exposed as files (subject to RBAC)"
run_cmd "ls /mnt/k8s/default/secrets/nginx-tls/data/"
demo_pause 2
comment "Certificate is automatically base64-decoded"
run_cmd "cat /mnt/k8s/default/secrets/nginx-tls/data/tls.crt"
demo_pause 3

# --- Show access control ---
comment "The filesystem is read-only by default"
comment "Attempting to write in read-only mode is denied"
type_command "echo 'new-value' > /mnt/k8s/default/configmaps/app-config/data/log.level"
echo 'new-value' > /mnt/k8s/default/configmaps/app-config/data/log.level 2>&1 || true
demo_pause 2

# --- Unmount, remount with update verb ---
comment "Unmount and remount with --enable-verbs update"
umount /mnt/k8s 2>/dev/null || true
wait "${KUBEFS_PID}" 2>/dev/null || true
sleep 1

type_command "kubefs mount -f --enable-verbs update /mnt/k8s &"
kubefs mount -f --enable-verbs update /mnt/k8s &
KUBEFS_PID=$!
sleep 2
demo_pause 1

# --- Write to a secret data key ---
comment "Generate a new TLS certificate with openssl"
run_cmd "openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 365 -subj '/CN=nginx.default.svc' -keyout /tmp/tls.key -out /tmp/tls.crt 2>/dev/null"
demo_pause 2

comment "Rotate the certificate by writing the file"
type_command "cp /tmp/tls.crt /mnt/k8s/default/secrets/nginx-tls/data/tls.crt"
cp /tmp/tls.crt /mnt/k8s/default/secrets/nginx-tls/data/tls.crt
demo_pause 2

comment "Read it back -- the update has been applied to the cluster"
run_cmd "cat /mnt/k8s/default/secrets/nginx-tls/data/tls.crt"
demo_pause 3

comment "Verify with openssl"
run_cmd "openssl x509 -in /mnt/k8s/default/secrets/nginx-tls/data/tls.crt -noout -subject -dates"
demo_pause 3

# --- sed on a deployment ---
comment "Standard UNIX tools work on Kubernetes objects"
comment "Show the current nginx deployment image tag"
run_cmd "grep 'image:' /mnt/k8s/default/deployments/nginx/yaml"
demo_pause 2

comment "Update the image tag with sed and write it back"
run_cmd "sed 's|nginx:1.27|nginx:1.28-alpine|' /mnt/k8s/default/deployments/nginx/yaml > /tmp/patched.yaml"
demo_pause 1
run_cmd "cp /tmp/patched.yaml /mnt/k8s/default/deployments/nginx/yaml"
demo_pause 2

comment "Confirm the change was applied to the cluster"
run_cmd "grep 'image:' /mnt/k8s/default/deployments/nginx/yaml"
demo_pause 3

# --- Unmount ---
comment "Unmount"
umount /mnt/k8s 2>/dev/null || true
wait "${KUBEFS_PID}" 2>/dev/null || true
demo_pause 1

comment "Done. See github.com/cloudygreybeard/kubefs for more."
demo_pause 3

if [[ -t 0 ]]; then
    read -n 1 -s -r
fi
