package applevf

import (
	"testing"

	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
)

// bootVM must reject an invalid spec before touching any platform machinery, on every OS. On macOS this
// stops short of a real boot (no kernel needed); on Linux bootVM is the not-supported stub. Either way an
// empty spec is an error.
func TestBootVMRejectsInvalidSpec(t *testing.T) {
	if _, err := bootVM(vm.Spec{}, nil); err == nil {
		t.Error("bootVM(empty spec) should error")
	}
}

// A well formed spec pointing at a nonexistent kernel must also error rather than boot: on macOS the boot
// loader fails to open the kernel, on Linux it is unsupported. This exercises bootVM past validation
// without needing a real kernel artifact or the virtualization entitlement.
func TestBootVMMissingKernelErrors(t *testing.T) {
	spec := vm.Spec{
		KernelPath:  filepathJoinNonexistent(t),
		Cmdline:     vm.DefaultCmdline().String(),
		BaseImage:   filepathJoinNonexistent(t),
		CPUCount:    1,
		MemoryBytes: 256 << 20,
	}
	if _, err := bootVM(spec, nil); err == nil {
		t.Error("bootVM with a missing kernel should error, not boot")
	}
}

func filepathJoinNonexistent(t *testing.T) string {
	t.Helper()
	return t.TempDir() + "/does-not-exist"
}
