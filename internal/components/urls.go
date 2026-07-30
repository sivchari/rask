package components

import "fmt"

// k8sBinaryURL and k8sChecksumURL build the download and checksum URLs for
// a dl.k8s.io release binary. dl.k8s.io's checksum files contain a bare
// hex digest with no filename to match against.
func k8sBinaryURL(k8sVersion string, arch Arch, bin string) string {
	return fmt.Sprintf("https://dl.k8s.io/%s/bin/linux/%s/%s", k8sVersion, arch, bin)
}

func k8sChecksumURL(k8sVersion string, arch Arch, bin string) string {
	return k8sBinaryURL(k8sVersion, arch, bin) + ".sha256"
}

// kineFilename, kineURL and kineChecksumURL build the download and checksum
// URLs for a k3s-io/kine release asset. kine's sha256sum-<arch>.txt lists
// every architecture's asset by filename.
func kineFilename(arch Arch) string {
	return "kine-" + string(arch)
}

func kineURL(arch Arch) string {
	return fmt.Sprintf("https://github.com/k3s-io/kine/releases/download/%s/%s", KineVersion, kineFilename(arch))
}

func kineChecksumURL(arch Arch) string {
	return fmt.Sprintf("https://github.com/k3s-io/kine/releases/download/%s/sha256sum-%s.txt", KineVersion, arch)
}

// runcFilename, runcURL and runcChecksumURL build the download and checksum
// URLs for an opencontainers/runc release asset. runc's runc.sha256sum is a
// single PGP-clearsigned file listing every architecture's asset.
func runcFilename(arch Arch) string {
	return "runc." + string(arch)
}

func runcURL(arch Arch) string {
	return fmt.Sprintf("https://github.com/opencontainers/runc/releases/download/%s/%s", RuncVersion, runcFilename(arch))
}

func runcChecksumURL() string {
	return fmt.Sprintf("https://github.com/opencontainers/runc/releases/download/%s/runc.sha256sum", RuncVersion)
}

// containerdArchive, containerdURL and containerdChecksumURL build the
// download and checksum URLs for a containerd/containerd release tarball.
func containerdArchive(arch Arch) string {
	return fmt.Sprintf("containerd-%s-linux-%s.tar.gz", ContainerdVersion, arch)
}

func containerdURL(arch Arch) string {
	return fmt.Sprintf("https://github.com/containerd/containerd/releases/download/v%s/%s", ContainerdVersion, containerdArchive(arch))
}

func containerdChecksumURL(arch Arch) string {
	return containerdURL(arch) + ".sha256sum"
}

// cniArchive, cniURL and cniChecksumURL build the download and checksum
// URLs for a containernetworking/plugins release tarball.
func cniArchive(arch Arch) string {
	return fmt.Sprintf("cni-plugins-linux-%s-%s.tgz", arch, CNIPluginsVersion)
}

func cniURL(arch Arch) string {
	return fmt.Sprintf("https://github.com/containernetworking/plugins/releases/download/%s/%s", CNIPluginsVersion, cniArchive(arch))
}

func cniChecksumURL(arch Arch) string {
	return cniURL(arch) + ".sha256"
}
