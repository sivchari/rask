// Command spike-s5 measures Virtualization.framework VM memory-snapshot
// save/restore: latency of SaveMachineStateToPath / RestoreMachineStateFromURL
// / Resume, guest clock behavior across the cycle, and whether the save file
// survives a restore. This retires the biggest open question for rask's v2
// "<1s warm resume" design. See RESULTS.md.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Code-Hex/vz/v3"
)

var tickRe = regexp.MustCompile(`RASK-S5-TICK seq=(\d+) mono_ms=(\d+) wall_unix_ms=(\d+)`)

type tick struct {
	seq    int
	monoMS int64
	wallMS int64
}

type guest struct {
	vm    *vz.VirtualMachine
	ticks chan tick
	boot  chan struct{}
}

func main() {
	runs := flag.Int("runs", 5, "save/restore cycles to measure")
	kernel := flag.String("kernel", "../s2/work/Image", "uncompressed arm64 kernel Image (reuses S2's)")
	initrd := flag.String("initrd", "work/initramfs.cpio", "initramfs with S5 rask-init")
	gap := flag.Duration("gap", 3*time.Second, "wall-clock gap between save and restore")
	flag.Parse()

	kernelPath, _ := filepath.Abs(*kernel)
	initrdPath, _ := filepath.Abs(*initrd)

	for i := 1; i <= *runs; i++ {
		fmt.Printf("=== cycle %d/%d ===\n", i, *runs)
		if err := cycle(kernelPath, initrdPath, *gap); err != nil {
			log.Fatalf("cycle %d: %v", i, err)
		}
	}
}

func cycle(kernelPath, initrdPath string, gap time.Duration) error {
	saveFile := filepath.Join("work", fmt.Sprintf("rask-s5-%d.vzsave", time.Now().UnixNano()))
	defer os.Remove(saveFile)

	// A restored VM must share the saved VM's machine identifier; a fresh
	// random identifier makes RestoreMachineStateFromURL fail with
	// "invalid argument". rask persists this per cluster.
	machineID, err := vz.NewGenericMachineIdentifier()
	if err != nil {
		return fmt.Errorf("machine id: %w", err)
	}

	// --- VM1: boot, observe ticks, pause, save ---
	g1, err := newGuest(kernelPath, initrdPath, machineID)
	if err != nil {
		return fmt.Errorf("vm1: %w", err)
	}
	if err := g1.vm.Start(); err != nil {
		return fmt.Errorf("vm1 start: %w", err)
	}
	select {
	case <-g1.boot:
	case <-time.After(10 * time.Second):
		return fmt.Errorf("vm1: no boot marker")
	}
	preSave, err := waitTick(g1.ticks, 5*time.Second)
	if err != nil {
		return fmt.Errorf("vm1 tick: %w", err)
	}

	if err := g1.vm.Pause(); err != nil {
		return fmt.Errorf("pause: %w", err)
	}
	tSave := time.Now()
	if err := g1.vm.SaveMachineStateToPath(saveFile); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	saveDur := time.Since(tSave)
	fi, _ := os.Stat(saveFile)
	if err := g1.vm.Stop(); err != nil {
		fmt.Printf("  (vm1 stop: %v)\n", err)
	}

	wallAtSave := time.Now()
	time.Sleep(gap)

	// --- VM2: fresh VM object, restore, resume ---
	g2, err := newGuest(kernelPath, initrdPath, machineID)
	if err != nil {
		return fmt.Errorf("vm2: %w", err)
	}
	tRestore := time.Now()
	if err := g2.vm.RestoreMachineStateFromURL(saveFile); err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	restoreDur := time.Since(tRestore)
	tResume := time.Now()
	if err := g2.vm.Resume(); err != nil {
		return fmt.Errorf("resume: %w", err)
	}
	resumeDur := time.Since(tResume)

	postRestore, err := waitTick(g2.ticks, 5*time.Second)
	if err != nil {
		return fmt.Errorf("vm2 tick after resume: %w", err)
	}
	firstTickDur := time.Since(tRestore)

	_, saveFileErr := os.Stat(saveFile)

	monoDelta := postRestore.monoMS - preSave.monoMS
	wallDeltaGuest := postRestore.wallMS - preSave.wallMS
	wallDeltaHost := time.Since(wallAtSave).Milliseconds() + saveDur.Milliseconds()

	fmt.Printf("  save=%s (file %dMiB)  restore=%s  resume=%s  first-tick-after-restore=%s\n",
		saveDur, fi.Size()/(1<<20), restoreDur, resumeDur, firstTickDur)
	fmt.Printf("  guest mono delta=%dms (must be small+forward)  guest wall delta=%dms (host-side elapsed=%dms)\n",
		monoDelta, wallDeltaGuest, wallDeltaHost)
	fmt.Printf("  save file after restore: %v\n", statText(saveFileErr))
	if monoDelta < 0 {
		fmt.Println("  WARNING: monotonic clock went BACKWARD across restore")
	}
	if err := g2.vm.Stop(); err != nil {
		fmt.Printf("  (vm2 stop: %v)\n", err)
	}
	return nil
}

func statText(err error) string {
	if err == nil {
		return "still exists"
	}
	return fmt.Sprintf("gone (%v)", err)
}

func waitTick(ticks chan tick, timeout time.Duration) (tick, error) {
	select {
	case t := <-ticks:
		return t, nil
	case <-time.After(timeout):
		return tick{}, fmt.Errorf("no tick within %s", timeout)
	}
}

func newGuest(kernelPath, initrdPath string, machineID *vz.GenericMachineIdentifier) (*guest, error) {
	bootLoader, err := vz.NewLinuxBootLoader(
		kernelPath,
		vz.WithCommandLine("console=hvc0 reboot=t panic=-1"),
		vz.WithInitrd(initrdPath),
	)
	if err != nil {
		return nil, fmt.Errorf("boot loader: %w", err)
	}
	config, err := vz.NewVirtualMachineConfiguration(bootLoader, 1, 1024*1024*1024)
	if err != nil {
		return nil, fmt.Errorf("vm config: %w", err)
	}
	platform, err := vz.NewGenericPlatformConfiguration(vz.WithGenericMachineIdentifier(machineID))
	if err != nil {
		return nil, fmt.Errorf("platform config: %w", err)
	}
	config.SetPlatformVirtualMachineConfiguration(platform)

	inR, _, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	attachment, err := vz.NewFileHandleSerialPortAttachment(inR, outW)
	if err != nil {
		return nil, fmt.Errorf("serial attachment: %w", err)
	}
	consoleConfig, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(attachment)
	if err != nil {
		return nil, fmt.Errorf("console config: %w", err)
	}
	config.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{consoleConfig})

	entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("entropy: %w", err)
	}
	config.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

	if ok, err := config.Validate(); !ok || err != nil {
		return nil, fmt.Errorf("config validate: ok=%v err=%w", ok, err)
	}
	if ok, err := config.ValidateSaveRestoreSupport(); !ok || err != nil {
		return nil, fmt.Errorf("save/restore support validate: ok=%v err=%w", ok, err)
	}
	vm, err := vz.NewVirtualMachine(config)
	if err != nil {
		return nil, fmt.Errorf("new vm: %w", err)
	}

	g := &guest{vm: vm, ticks: make(chan tick, 64), boot: make(chan struct{}, 1)}
	go func() {
		scanner := bufio.NewScanner(outR)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "RASK-S5-BOOT-COMPLETE") {
				select {
				case g.boot <- struct{}{}:
				default:
				}
			}
			if m := tickRe.FindStringSubmatch(line); m != nil {
				seq, _ := strconv.Atoi(m[1])
				mono, _ := strconv.ParseInt(m[2], 10, 64)
				wall, _ := strconv.ParseInt(m[3], 10, 64)
				select {
				case g.ticks <- tick{seq: seq, monoMS: mono, wallMS: wall}:
				default:
				}
			}
		}
	}()
	return g, nil
}
