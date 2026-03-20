#!/usr/bin/env bash
# Record the kubefs demo as animated GIF and SVG.
#
# Assumes the container images (mock-kubeapi and kubefs-demo) have
# already been built. Run `make demo-build` first, or use `make demo`
# which handles the dependency automatically.
#
# The two containers are connected via a shared container network so
# the demo container can reach the mock API at http://mock-kubeapi:6443.
#
# The demo container requires --privileged (or --device /dev/fuse
# --cap-add SYS_ADMIN) because kubefs mounts a FUSE filesystem.
#
# Prerequisites:
#   podman (or docker)
#   brew install asciinema agg
#   npm install -g svg-term-cli
#
# Usage:
#   make demo          # build + record (recommended)
#   ./hack/record-demo.sh   # record only (images must exist)

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DOCS_DIR="${PROJECT_ROOT}/docs"
CAST_FILE="${DOCS_DIR}/kubefs-demo.cast"
SVG_FILE="${DOCS_DIR}/kubefs-demo.svg"
GIF_FILE="${DOCS_DIR}/kubefs-demo.gif"

MOCK_IMAGE="mock-kubeapi"
DEMO_IMAGE="kubefs-demo"
NETWORK_NAME="kubefs-demo-net"

CONTAINER_RUNTIME="podman"
if ! command -v podman >/dev/null 2>&1; then
    if command -v docker >/dev/null 2>&1; then
        CONTAINER_RUNTIME="docker"
    else
        echo "Error: podman or docker is required."
        exit 1
    fi
fi

for tool in asciinema; do
    if ! command -v "${tool}" >/dev/null 2>&1; then
        echo "Error: ${tool} is not installed."
        echo "  Install with: brew install ${tool}"
        exit 1
    fi
done

mkdir -p "${DOCS_DIR}"

purge_network() {
    ${CONTAINER_RUNTIME} rm -f mock-kubeapi-demo 2>/dev/null || true
    # Remove any container still attached to the network (orphans from
    # interrupted runs, demo containers with random names, etc.)
    for cid in $(${CONTAINER_RUNTIME} ps -aq --filter network="${NETWORK_NAME}" 2>/dev/null); do
        ${CONTAINER_RUNTIME} rm -f "${cid}" 2>/dev/null || true
    done
    ${CONTAINER_RUNTIME} network rm "${NETWORK_NAME}" 2>/dev/null || true
}

cleanup() {
    echo "Cleaning up..."
    purge_network
}
trap cleanup EXIT

# --- Clean up any leftovers from previous runs ---

purge_network

# --- Create network ---

echo "Creating container network ${NETWORK_NAME}..."
${CONTAINER_RUNTIME} network create "${NETWORK_NAME}"

# --- Start mock API ---

echo "Starting mock-kubeapi..."
${CONTAINER_RUNTIME} run -d \
    --name mock-kubeapi-demo \
    --network "${NETWORK_NAME}" \
    --network-alias mock-kubeapi \
    "${MOCK_IMAGE}"

sleep 1

echo ""
echo "Recording demo..."
echo "  The demo mounts a FUSE filesystem against the mock-kubeapi container."
echo "  The demo container runs with --privileged for FUSE access."
echo ""

if [[ -t 0 ]]; then
    echo "Press Enter to start recording..."
    read -r
fi

asciinema rec "${CAST_FILE}" \
    --command "${CONTAINER_RUNTIME} run --rm -it --privileged --network ${NETWORK_NAME} -e TYPE_SPEED=${TYPE_SPEED:-0.020} ${DEMO_IMAGE}" \
    --idle-time-limit 3 \
    --overwrite \
    --output-format asciicast-v2 \
    --cols 90 \
    --rows 30

echo ""

if command -v agg >/dev/null 2>&1; then
    echo "Generating GIF with agg..."
    agg "${CAST_FILE}" "${GIF_FILE}" \
        --cols 90 \
        --rows 30 \
        --font-size 16
fi

if command -v svg-term >/dev/null 2>&1; then
    echo "Generating SVG with svg-term..."
    svg-term \
        --in "${CAST_FILE}" \
        --out "${SVG_FILE}" \
        --window \
        --width 90 \
        --height 30 \
        --padding 10
fi

echo ""
echo "Recording complete."
echo "  Cast: ${CAST_FILE} ($(du -h "${CAST_FILE}" | cut -f1))"
[[ -f "${GIF_FILE}" ]] && echo "  GIF:  ${GIF_FILE} ($(du -h "${GIF_FILE}" | cut -f1))"
[[ -f "${SVG_FILE}" ]] && echo "  SVG:  ${SVG_FILE} ($(du -h "${SVG_FILE}" | cut -f1))"
echo ""
echo "To include in README.md:"
[[ -f "${GIF_FILE}" ]] && echo "  ![kubefs Demo](docs/kubefs-demo.gif)"
[[ -f "${SVG_FILE}" ]] && echo "  ![kubefs Demo](docs/kubefs-demo.svg)"
