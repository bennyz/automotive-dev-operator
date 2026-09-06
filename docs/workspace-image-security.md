# Workspace Image Security

This guide covers how to restrict which container images can run in workspaces,
how to sign images with cosign, and how to configure the operator to verify
signatures.

## Image Allowlist

By default, only the configured toolchain image is permitted in workspaces.
To allow additional images, add them to the `allowedImages` list in the
OperatorConfig:

```yaml
apiVersion: automotive.sdv.cloud.redhat.com/v1alpha1
kind: OperatorConfig
metadata:
  name: config
spec:
  workspaces:
    allowedImages:
      - "quay.io/my-org/*"                          # prefix glob — matches any image under quay.io/my-org/
      - "registry.example.com/toolchain:v2.1"        # exact match
```

The toolchain image (`workspaces.toolchainImage` or the default) is always
implicitly allowed regardless of this list.

When `allowedImages` is empty and a user requests a non-toolchain image, the
request is rejected with HTTP 403 (API) or a reconciliation error (controller).

## Signing Images with Cosign

### Generate a keypair

```bash
cosign generate-key-pair
```

This creates `cosign.key` (private) and `cosign.pub` (public).

### Sign an image

```bash
cosign sign --key cosign.key quay.io/my-org/my-toolchain:v1.0
```

For images in a private registry, ensure you are authenticated:

```bash
podman login registry.example.com
cosign sign --key cosign.key registry.example.com/my-toolchain:v1.0
```

### Verify locally (optional)

```bash
cosign verify --key cosign.pub quay.io/my-org/my-toolchain:v1.0
```

## Enabling Signature Verification

### 1. Store the public key in a ConfigMap

```bash
kubectl create configmap workspace-cosign-key \
  --from-file=cosign.pub=cosign.pub \
  -n <operator-namespace>
```

### 2. Configure the OperatorConfig

```yaml
apiVersion: automotive.sdv.cloud.redhat.com/v1alpha1
kind: OperatorConfig
metadata:
  name: config
spec:
  workspaces:
    imageVerify: true
    imageCosignKeyRef:
      name: workspace-cosign-key
      key: cosign.pub
    allowedImages:
      - "quay.io/my-org/*"
```

When `imageVerify` is true, every workspace image must pass cosign signature
verification before a pod is created. The operator tries v3 bundle verification
(OCI referrers) first, then falls back to legacy tag-based signatures.

## Private Registry Authentication

For images in private registries, configure pull secrets so both the pod and
signature verification can authenticate:

### Global (all workspaces)

```yaml
spec:
  workspaces:
    imagePullSecrets:
      - name: my-registry-secret
```

The pull secret must be a standard `kubernetes.io/dockerconfigjson` secret:

```bash
kubectl create secret docker-registry my-registry-secret \
  --docker-server=registry.example.com \
  --docker-username=user \
  --docker-password=pass \
  -n <operator-namespace>
```

The same credentials are used for both pulling the image into the pod and
verifying its cosign signature.

## Full Example

Sign an image, configure the operator, and create a workspace:

```bash
# Sign
cosign sign --key cosign.key registry.example.com/my-toolchain:v1.0

# Store public key
kubectl create configmap workspace-cosign-key \
  --from-file=cosign.pub=cosign.pub -n ado-system

# Create pull secret
kubectl create secret docker-registry reg-creds \
  --docker-server=registry.example.com \
  --docker-username=user --docker-password=pass -n ado-system

# Configure operator
kubectl apply -f - <<EOF
apiVersion: automotive.sdv.cloud.redhat.com/v1alpha1
kind: OperatorConfig
metadata:
  name: config
  namespace: ado-system
spec:
  workspaces:
    imageVerify: true
    imageCosignKeyRef:
      name: workspace-cosign-key
      key: cosign.pub
    allowedImages:
      - "registry.example.com/my-toolchain:*"
    imagePullSecrets:
      - name: reg-creds
EOF

# Create workspace with custom image
caib workspace create my-ws --toolchain-image registry.example.com/my-toolchain:v1.0
```
