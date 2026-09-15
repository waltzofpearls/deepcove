package cloudhv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// orphanGracePeriod is how long a leftover process gets to go on its own before it is killed.
const orphanGracePeriod = 2 * time.Second

// SweepOrphans stops the VMs a previous deepcove left running and removes the directories they used.
//
// Apple VF runs a VM inside this process, so one dies with it. Cloud Hypervisor does not: the VMM,
// virtiofsd and passt are separate processes, and an abrupt exit leaves them running with nobody left to
// stop them. They are found by the directory they were given, so only this node's are touched.
func (b Backend) SweepOrphans() (int, error) {
	dirs, err := ipcDirEntries(b.IPCDir)
	if err != nil || len(dirs) == 0 {
		return 0, err
	}

	pids := orphanPIDs(procDir, b.IPCDir)
	signalAll(pids, syscall.SIGTERM)
	if waitGone(pids, orphanGracePeriod) > 0 {
		signalAll(pids, syscall.SIGKILL)
	}

	var removeErr error
	for _, dir := range dirs {
		removeErr = errors.Join(removeErr, os.RemoveAll(dir))
	}
	return len(pids), removeErr
}

// procDir is where the kernel lists running processes. A variable so a test can hand over a fake one.
var procDir = "/proc"

// ipcDirEntries are the per VM directories left behind. Nothing of ours is running yet when this is
// asked, so every one of them belongs to a previous process.
func ipcDirEntries(ipcDir string) ([]string, error) {
	entries, err := os.ReadDir(ipcDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cloudhv: read %s: %w", ipcDir, err)
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, filepath.Join(ipcDir, entry.Name()))
		}
	}
	return dirs, nil
}

// orphanPIDs are the processes still holding a socket under ipcDir, which is how a VM of ours is
// recognised without recording a pid anywhere.
func orphanPIDs(proc, ipcDir string) []int {
	entries, err := os.ReadDir(proc)
	if err != nil {
		return nil
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join(proc, entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		if strings.Contains(string(cmdline), ipcDir+string(filepath.Separator)) {
			pids = append(pids, pid)
		}
	}
	return pids
}

func signalAll(pids []int, sig syscall.Signal) {
	for _, pid := range pids {
		_ = syscall.Kill(pid, sig)
	}
}

// waitGone returns how many are still alive once the grace runs out.
func waitGone(pids []int, grace time.Duration) int {
	deadline := time.Now().Add(grace)
	for {
		alive := 0
		for _, pid := range pids {
			if syscall.Kill(pid, 0) == nil {
				alive++
			}
		}
		if alive == 0 || time.Now().After(deadline) {
			return alive
		}
		time.Sleep(readyPoll)
	}
}
