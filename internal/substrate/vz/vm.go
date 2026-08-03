//go:build darwin

package vz

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"time"

	cvz "github.com/Code-Hex/vz/v3"

	"github.com/sivchari/rask/internal/guestinit"
)

// Default VM sizing. Not yet exposed as a create-time flag: S2's
// measurements found no boot-latency difference between 1/1GiB and
// 2/2GiB, so these are chosen for headroom to actually run a Kubernetes
// control plane and node, not for boot speed.
const (
	defaultCPUCount   = 2
	defaultMemoryMiB  = 4096
	defaultDataDiskGB = 30
)

// vmConfig is everything buildVirtualMachineConfiguration needs to build one
// cluster's vz.VirtualMachineConfiguration.
type vmConfig struct {
	kernelImagePath  string
	initramfsPath    string
	diskPath         string
	machineID        *cvz.GenericMachineIdentifier
	clusterName      string
	bootTimeUnixNano int64
	net              *clusterNetwork
}

// buildVirtualMachineConfiguration wires together every vz device rask's
// guest needs: the boot loader (kernel Image + concatenated initrd, per
// the M0 s2 spike's confirmed "arm64 requires an uncompressed kernel Image,
// no decompression on the vz side" finding), a virtio-console serial port
// for boot markers, the required entropy device (the M0 s3 spike found
// DHCP/getrandom hangs without it), the cluster's virtio-net device
// (network.go), the virtio-blk data disk, Rosetta's directory share (gated
// on host availability), and the persisted machine identifier (for future
// save/restore, per the M0 s5 spike).
func buildVirtualMachineConfiguration(cfg vmConfig) (*cvz.VirtualMachineConfiguration, *consolePipes, error) {
	// panic=0: a panicking guest halts instead of reboot-looping. panic=-1
	// (instant reboot) turned every failed boot into an infinite CPU-burning
	// loop; leftover VMs from failed creates froze the whole host.
	cmdline := fmt.Sprintf("console=hvc0 reboot=t panic=0 %s",
		guestinit.FormatBootParams(guestinit.BootParams{ClusterName: cfg.clusterName, BootTimeUnixNano: cfg.bootTimeUnixNano}))

	bootLoader, err := cvz.NewLinuxBootLoader(cfg.kernelImagePath, cvz.WithCommandLine(cmdline), cvz.WithInitrd(cfg.initramfsPath))
	if err != nil {
		return nil, nil, fmt.Errorf("vz: boot loader: %w", err)
	}

	config, err := cvz.NewVirtualMachineConfiguration(bootLoader, defaultCPUCount, defaultMemoryMiB*1024*1024)
	if err != nil {
		return nil, nil, fmt.Errorf("vz: vm configuration: %w", err)
	}

	console, err := attachConsole(config)
	if err != nil {
		return nil, nil, err
	}

	entropyConfig, err := cvz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, nil, fmt.Errorf("vz: entropy device: %w", err)
	}

	config.SetEntropyDevicesVirtualMachineConfiguration([]*cvz.VirtioEntropyDeviceConfiguration{entropyConfig})

	netDeviceConfig, err := cfg.net.networkDeviceConfig()
	if err != nil {
		return nil, nil, err
	}

	config.SetNetworkDevicesVirtualMachineConfiguration([]*cvz.VirtioNetworkDeviceConfiguration{netDeviceConfig})

	diskConfig, err := buildDiskDeviceConfiguration(cfg.diskPath)
	if err != nil {
		return nil, nil, err
	}

	config.SetStorageDevicesVirtualMachineConfiguration([]cvz.StorageDeviceConfiguration{diskConfig})

	// Rosetta's directory share is deliberately NOT attached — see
	// attachRosetta's doc comment for why (a suspected correlation with a
	// real host crash during E2E testing on this exact machine). Not even
	// LinuxRosettaDirectoryShareAvailability() is called. This means only
	// arm64 guest images work for now; amd64-via-Rosetta is out of scope
	// until this is revisited.

	platform, err := cvz.NewGenericPlatformConfiguration(cvz.WithGenericMachineIdentifier(cfg.machineID))
	if err != nil {
		return nil, nil, fmt.Errorf("vz: platform configuration: %w", err)
	}

	config.SetPlatformVirtualMachineConfiguration(platform)

	if ok, err := config.Validate(); !ok || err != nil {
		return nil, nil, fmt.Errorf("vz: invalid vm configuration: valid=%v err=%w", ok, err)
	}

	return config, console, nil
}

// consolePipes are the host-side ends of the VM's serial console. A single
// background goroutine (started by attachConsole) scans fromGuestR and
// publishes each line to lines — every consumer (boot-failure detection,
// --verbose logging) reads from that one channel rather than each
// independently scanning the same underlying pipe, which would race for
// bytes.
type consolePipes struct {
	toGuestW   *os.File // host writes here to send guest input (unused today; kept for the read/write pair's correct orientation, see attachConsole's doc comment)
	fromGuestR *os.File // host reads here to receive guest output
	lines      chan string
	closers    []io.Closer
}

func (c *consolePipes) Close() {
	for _, cl := range c.closers {
		_ = cl.Close()
	}
}

// Lines returns every console line, in order; closed once the console
// itself closes (the VM stopped).
func (c *consolePipes) Lines() <-chan string {
	return c.lines
}

// startReading launches the single goroutine that scans console output.
func (c *consolePipes) startReading() {
	go func() {
		defer close(c.lines)

		scanner := bufio.NewScanner(c.fromGuestR)
		for scanner.Scan() {
			// Non-blocking: console output is diagnostic, not a
			// control channel worth ever blocking the guest's
			// writer over if no one is currently reading Lines().
			select {
			case c.lines <- scanner.Text():
			default:
			}
		}
	}()
}

// attachConsole wires a VirtioConsoleDeviceSerialPortConfiguration backed
// by an os.Pipe pair. NewFileHandleSerialPortAttachment(read, write)'s
// parameter naming is inverted from intuition (a "vz API surprise" found
// during the M0 s2 spike): "read" is what the framework reads from to send
// bytes INTO the guest, "write" is what the framework writes guest output
// INTO — so the host must read from the "write" pipe to see console output.
func attachConsole(config *cvz.VirtualMachineConfiguration) (*consolePipes, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("vz: console input pipe: %w", err)
	}

	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("vz: console output pipe: %w", err)
	}

	attachment, err := cvz.NewFileHandleSerialPortAttachment(inR, outW)
	if err != nil {
		return nil, fmt.Errorf("vz: serial port attachment: %w", err)
	}

	serialConfig, err := cvz.NewVirtioConsoleDeviceSerialPortConfiguration(attachment)
	if err != nil {
		return nil, fmt.Errorf("vz: serial port configuration: %w", err)
	}

	config.SetSerialPortsVirtualMachineConfiguration([]*cvz.VirtioConsoleDeviceSerialPortConfiguration{serialConfig})

	console := &consolePipes{toGuestW: inW, fromGuestR: outR, lines: make(chan string, 256), closers: []io.Closer{inR, inW, outR, outW}}
	console.startReading()

	return console, nil
}

// buildDiskDeviceConfiguration attaches diskPath (the per-cluster data
// disk, created by createDataDisk) as a virtio-blk device — the guest's
// only block device (guestlayout.DataDiskDevice), since the root
// filesystem is the concatenated initrd/tmpfs, not a disk image.
func buildDiskDeviceConfiguration(diskPath string) (*cvz.VirtioBlockDeviceConfiguration, error) {
	attachment, err := cvz.NewDiskImageStorageDeviceAttachment(diskPath, false)
	if err != nil {
		return nil, fmt.Errorf("vz: disk attachment %s: %w", diskPath, err)
	}

	diskConfig, err := cvz.NewVirtioBlockDeviceConfiguration(attachment)
	if err != nil {
		return nil, fmt.Errorf("vz: disk device configuration: %w", err)
	}

	return diskConfig, nil
}

// attachRosetta attaches the host's Rosetta directory share (tag
// "rosetta", matching cmd/rask-init's mountRosettaAndRegisterBinfmt), if
// Rosetta is installed and available on this host.
//
// NOT CALLED right now (see the disabledFeatures var below): during E2E
// testing on this exact machine, the host suffered repeated
// system-wide crashes while a VM with Rosetta attached was running,
// closely enough correlated in time to be a real suspect. Disabled at the
// user's explicit direction until the actual cause is confirmed one way
// or the other; kept here (not deleted) for when that investigation
// happens. No enable flag exists — re-enabling requires an explicit code
// change (calling this from buildVirtualMachineConfiguration again), not
// a runtime toggle.
func attachRosetta(config *cvz.VirtualMachineConfiguration) error {
	availability := cvz.LinuxRosettaDirectoryShareAvailability()
	if availability != cvz.LinuxRosettaAvailabilityInstalled {
		return fmt.Errorf("rosetta not available: %s", availability)
	}

	share, err := cvz.NewLinuxRosettaDirectoryShare()
	if err != nil {
		return fmt.Errorf("rosetta directory share: %w", err)
	}

	fsConfig, err := cvz.NewVirtioFileSystemDeviceConfiguration("rosetta")
	if err != nil {
		return fmt.Errorf("rosetta fs device configuration: %w", err)
	}

	fsConfig.SetDirectoryShare(share)
	config.SetDirectorySharingDevicesVirtualMachineConfiguration([]cvz.DirectorySharingDeviceConfiguration{fsConfig})

	return nil
}

// disabledFeatures keeps attachRosetta reachable (so it compiles and isn't
// flagged as dead code) without actually calling it from anywhere at
// runtime — see attachRosetta's doc comment for why it's disabled.
var disabledFeatures = struct {
	attachRosetta func(*cvz.VirtualMachineConfiguration) error
}{
	attachRosetta: attachRosetta,
}

// stopVM force-stops vm and waits briefly for it to reach a terminal
// state.
func stopVM(vm *cvz.VirtualMachine) {
	if vm.CanStop() {
		_ = vm.Stop()
	} else {
		_, _ = vm.RequestStop()
	}

	deadline := time.After(5 * time.Second)

	for {
		select {
		case st := <-vm.StateChangedNotify():
			if st == cvz.VirtualMachineStateStopped || st == cvz.VirtualMachineStateError {
				return
			}
		case <-deadline:
			return
		}
	}
}
