# shellcheck shell=bash
# NOTE: common.sh is prepended to this script at embed time.

echo "Prepare builder for distro: $DISTRO, arch: $TARGET_ARCH"

# If BUILDER_IMAGE is provided, use it directly
if [ -n "$BUILDER_IMAGE" ]; then
  echo "Using provided builder image: $BUILDER_IMAGE"
  echo -n "$BUILDER_IMAGE" > "$RESULT_PATH"
  exit 0
fi

# Determine registry and set up authentication
if [ -n "$CLUSTER_REGISTRY_ROUTE" ]; then
  echo "Using external registry route: $CLUSTER_REGISTRY_ROUTE"
fi
setup_cluster_auth "${CLUSTER_REGISTRY_ROUTE:-}"

# Include a short hash of the AIB image in the registry tag so that different
# AIB versions cache their builder images separately and don't overwrite each other.
AIB_HASH=$(echo -n "$AIB_IMAGE" | sha256sum | cut -c1-8)
TARGET_IMAGE="${REGISTRY}/${NAMESPACE}/aib-build:${DISTRO}-${TARGET_ARCH}-${AIB_HASH}"
echo "AIB image: $AIB_IMAGE (hash: $AIB_HASH)"

setup_container_config
setup_var_tmp

# Local target name for pushing to registry
LOCAL_TARGET="localhost/aib-build:${DISTRO}-${TARGET_ARCH}-${AIB_HASH}"

BUILDER_TOTAL=2

emit_progress "Checking builder cache" 0 "$BUILDER_TOTAL"

install_custom_ca_certs
setup_osbuild

load_custom_definitions "$(workspaces.manifest-config-workspace.path)/custom-definitions.env"

emit_progress "Preparing builder image" 1 "$BUILDER_TOTAL"
# Used by the embedded builder cache helper.
# shellcheck disable=SC2034
declare -a SKOPEO_INSPECT_TLS_ARGS=() SKOPEO_COPY_TLS_ARGS=()
refresh_builder_image "$TARGET_IMAGE" "$LOCAL_TARGET" "$REGISTRY_AUTH_FILE" false \
  --distro "$DISTRO" "${CUSTOM_DEFS_ARGS[@]}"

emit_progress "Builder ready" "$BUILDER_TOTAL" "$BUILDER_TOTAL"
echo "Builder image ready: $BUILDER_IMAGE"
echo -n "$BUILDER_IMAGE" > "$RESULT_PATH"
