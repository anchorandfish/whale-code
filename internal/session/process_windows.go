//go:build windows

package session

import (
	"fmt"
	"os/exec"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// getProcessStartTimeOS returns the process start time on Windows.
//
// Primary path: the GetProcessTimes syscall, which is always available on
// every supported Windows version (unlike wmic, which is deprecated and may
// be absent on Windows 11 24H2+). We return the raw CreationTime FILETIME as
// a 64-bit value (100-nanosecond intervals since 1601-01-01). This is stable
// for a process's lifetime and differs across process instances sharing a PID.
//
// Fallback path: if OpenProcess/GetProcessTimes fails (e.g. insufficient
// access rights to the target process), we fall back to the wmic command and
// parse its CreationDate output. This is best-effort; callers treat 0 as
// "unknown" and degrade gracefully.
func getProcessStartTimeOS(pid int) (int64, error) {
	const processQueryLimitedInfo = 0x1000 // PROCESS_QUERY_LIMITED_INFORMATION

	handle, err := windows.OpenProcess(processQueryLimitedInfo, false, uint32(pid))
	if err == nil {
		defer windows.CloseHandle(handle)
		var creation, exit, kernel, user windows.Filetime
		if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err == nil {
			// Combine the FILETIME into a 64-bit value. This is monotonic and
			// unique per process instance; we return it as int64 for
			// comparison with the value recorded in the lock file.
			ft := uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime)
			if ft != 0 {
				return int64(ft), nil
			}
		}
	}

	// Fallback: wmic. Note wmic is deprecated on Windows 11 24H2+ and may be
	// absent; this path is best-effort only.
	cmd := exec.Command("wmic", "process", fmt.Sprintf("where ProcessId=%d", pid),
		"get", "CreationDate", "/format:value")
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("GetProcessTimes failed and wmic fallback unavailable for pid %d: %w", pid, err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "CreationDate=") {
			val := strings.TrimPrefix(line, "CreationDate=")
			if v := parseWmicTime(val); v != 0 {
				return v, nil
			}
		}
	}
	return 0, fmt.Errorf("could not determine start time for pid %d", pid)
}

// Ensure unsafe is referenced even if future refactors drop its direct use
// (kept for consistency with the other platform files' pointer arithmetic).
var _ = unsafe.Sizeof(0)
