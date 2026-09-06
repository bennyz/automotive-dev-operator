# CentOS Automotive Suite Operator

An operator for building automotive OS images on OpenShift. This operator provides a cloud-native way to create automotive OS images using the automotive-image-builder (AIB) project, with support for both traditional AIB manifests and modern bootc container builds.

## Description

The CentOS Automotive Suite Operator enables automotive OS image building through:

- **ImageBuild Custom Resource**: Declaratively define and trigger automotive OS image builds
- **Multiple Build Modes**: Support for traditional AIB manifests and bootc container builds
- **CLI Tool (caib)**: Command-line interface for creating and monitoring builds
- **Artifact Management**: Serve built images via OpenShift Routes or push to OCI registries
- **Tekton Integration**: Uses OpenShift Pipelines (Tekton) for scalable build execution

## Getting Started

### Prerequisites

**For OpenShift Installation (Recommended):**
- OpenShift 4.18+ cluster
- OpenShift Pipelines Operator (Tekton) installed
- Cluster admin permissions (for initial installation)

**For Development:**
- Go 1.22.0+
- Podman or Docker
- OpenShift CLI (`oc`) or kubectl
- Operator SDK v1.42.0+ (for development)

## Installation

### Option 1: OpenShift OperatorHub (Recommended)

The easiest way to install on OpenShift is through OperatorHub:

1. Open the OpenShift Console
2. Navigate to **Operators** > **OperatorHub**
3. Search for "CentOS Automotive Suite"
4. Click **Install** and follow the prompts

After installation, create an `OperatorConfig` to enable components:

```sh
oc apply -f config/samples/automotive_v1_operatorconfig.yaml
```

### Option 2: OLM Catalog (Local Testing)

For local development and testing with a custom-built operator:

```sh
# Build, push, and install the operator via OLM catalog and keep the existing config
./hack/deploy-catalog.sh -y --keep-config

# Create OperatorConfig to configure the operator
oc apply -f config/samples/automotive_v1_operatorconfig.yaml
```

### Installing the CLI (caib)

The `caib` CLI lets you create, monitor, and download builds from the command line.

**Install the latest release:**

```sh
curl -fsSL https://raw.githubusercontent.com/centos-automotive-suite/automotive-dev-operator/main/hack/install-caib.sh | bash
```

Or install a specific version:

```sh
curl -fsSL https://raw.githubusercontent.com/centos-automotive-suite/automotive-dev-operator/main/hack/install-caib.sh | bash -s -- v0.1.0
```

**Configure the API endpoint:**

```sh
export CAIB_SERVER=https://build-api-automotive-dev-operator.apps.<cluster-domain>
```

The CLI auto-detects authentication from your `oc login` session. You can also set `CAIB_TOKEN` explicitly.


For usage, build examples, Jumpstarter flashing, and the full command reference, see [`cmd/caib/README.md`](cmd/caib/README.md).

## Uninstallation

### For OperatorHub Installation

1. In OpenShift Console, go to **Operators** > **Installed Operators**
2. Find "CentOS Automotive Suite" and click the options menu
3. Select **Uninstall Operator**

### For OLM Catalog Installation

```sh
./hack/deploy-catalog.sh uninstall -y
```

## Components

### CLI Tool

The `caib` CLI provides command-line access to build operations. Key command groups:

- **`caib image`** — build, flash, list, show, download, delete, and manage OS images
- **`caib workspace`** — create developer workspaces with cross-compilation toolchains, sync code, and deploy to boards
- **`caib container`** — build container images on-cluster using Shipwright
- **`caib catalog`** — publish and manage an image catalog for discovery and deployment

See [Installing the CLI](#installing-the-cli-caib) for setup and [`cmd/caib/README.md`](cmd/caib/README.md) for the full command reference.

## Architecture

### Dependencies

- **OpenShift Pipelines Operator**: Required for Tekton pipeline execution
- **OpenShift 4.18+**: Minimum supported OpenShift version
- **Container Registry**: For storing built images (--internal-registry)

## Development

### Local Development

1. **Clone and setup:**

```sh
git clone https://github.com/centos-automotive-suite/automotive-dev-operator.git
cd automotive-dev-operator
```

2. **Install dependencies:**

```sh
# Unit tests
make test

# Linting
make lint

# Formatting
make fmt
```

### Building

```sh
# Build all binaries
make build

# Build specific components
make build-caib           # CLI tool

# Build container images
make docker-build
```

## Release Information

This project publishes versioned releases with:
- Multi-architecture container images (amd64, arm64)
- `caib` CLI binaries for Linux
- OLM bundles for OperatorHub distribution

For the latest release, visit: https://github.com/centos-automotive-suite/automotive-dev-operator/releases

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
