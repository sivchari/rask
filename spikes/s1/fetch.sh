#!/usr/bin/env bash
# fetch.sh downloads all linux/arm64 binaries needed by the S1 spike into
# ./work/. Safe to re-run; skips files that already exist.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"
mkdir -p work
cd work

K8S_VERSION="v1.33.13"
KINE_VERSION="v0.16.3"
CONTAINERD_VERSION="2.3.3"
RUNC_VERSION="v1.5.1"
CNI_PLUGINS_VERSION="v1.9.1"

fetch() {
	local url="$1"
	local out="$2"
	if [[ -s "$out" ]]; then
		echo "skip $out (exists)"
		return
	fi
	echo "fetch $out <- $url"
	curl -sSL --fail -o "$out" "$url"
}

for bin in kube-apiserver kube-controller-manager kube-scheduler kubelet kubectl; do
	fetch "https://dl.k8s.io/${K8S_VERSION}/bin/linux/arm64/${bin}" "${bin}"
	chmod +x "${bin}"
done

fetch "https://github.com/k3s-io/kine/releases/download/${KINE_VERSION}/kine-arm64" "kine"
chmod +x kine

fetch "https://github.com/opencontainers/runc/releases/download/${RUNC_VERSION}/runc.arm64" "runc"
chmod +x runc

fetch "https://github.com/containerd/containerd/releases/download/v${CONTAINERD_VERSION}/containerd-${CONTAINERD_VERSION}-linux-arm64.tar.gz" "containerd.tar.gz"
mkdir -p containerd-bin
tar -xzf containerd.tar.gz -C containerd-bin

fetch "https://github.com/containernetworking/plugins/releases/download/${CNI_PLUGINS_VERSION}/cni-plugins-linux-arm64-${CNI_PLUGINS_VERSION}.tgz" "cni-plugins.tgz"
mkdir -p cni-bin
tar -xzf cni-plugins.tgz -C cni-bin

echo "done. binaries in $(pwd)"
ls -la
