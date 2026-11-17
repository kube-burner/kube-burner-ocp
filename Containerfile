# kube-burner-ocp runner image.
#
# Downloads a released kube-burner-ocp binary and runs it. No build stage:
# the project already publishes per-architecture binaries, so compiling one
# here would only risk producing something different.
#
#   make image IMAGE=quay.io/<org>/kube-burner-ocp:latest
#
# The release defaults to the upstream project's latest. Point it elsewhere
# to pick up a workload that has not landed upstream yet:
#
#   make image RELEASE_REPO=<you>/kube-burner-ocp RELEASE_VERSION=v1.14.0
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

ARG RELEASE_REPO=kube-burner/kube-burner-ocp
ARG RELEASE_VERSION=latest
ARG TARGETARCH

RUN microdnf install -y tar gzip ca-certificates \
 && microdnf clean all

# Release assets are named kube-burner-ocp-V<version>-linux-<x86_64|arm64>.tar.gz:
# a capital V, and x86_64 rather than amd64.
RUN set -eux; \
    case "${TARGETARCH:-$(uname -m)}" in \
      amd64|x86_64)  asset_arch=x86_64 ;; \
      arm64|aarch64) asset_arch=arm64  ;; \
      *) echo "unsupported architecture: ${TARGETARCH:-$(uname -m)}" >&2; exit 1 ;; \
    esac; \
    tag="${RELEASE_VERSION}"; \
    if [ "${tag}" = "latest" ]; then \
      tag="$(curl -sfL "https://api.github.com/repos/${RELEASE_REPO}/releases/latest" \
             | grep -m1 '"tag_name"' | cut -d'"' -f4)"; \
    fi; \
    [ -n "${tag}" ] || { echo "could not resolve a release tag for ${RELEASE_REPO}" >&2; exit 1; }; \
    url="https://github.com/${RELEASE_REPO}/releases/download/${tag}/kube-burner-ocp-V${tag#v}-linux-${asset_arch}.tar.gz"; \
    echo "downloading ${url}"; \
    curl -sfL "${url}" -o /tmp/kube-burner-ocp.tar.gz; \
    tar -xzf /tmp/kube-burner-ocp.tar.gz -C /usr/local/bin kube-burner-ocp; \
    rm -f /tmp/kube-burner-ocp.tar.gz; \
    chmod 0755 /usr/local/bin/kube-burner-ocp; \
    kube-burner-ocp version

COPY hack/container/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh \
 && mkdir -p /workspace && chmod 0777 /workspace

# kube-burner writes collected metrics relative to the working directory, and
# an arbitrary OpenShift UID has to be able to write there.
WORKDIR /workspace

# kube-burner only falls back to the in-cluster config when it finds no
# kubeconfig: it checks $KUBECONFIG, then $HOME/.kube/config. HOME is pinned
# to a directory that has neither, so the pod's ServiceAccount token is what
# gets used. Setting KUBECONFIG on the pod overrides this deliberately.
ENV HOME=/workspace

# Numeric so Kubernetes `runAsNonRoot: true` can verify it without a lookup.
USER 1001

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
