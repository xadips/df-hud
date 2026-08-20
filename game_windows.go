//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsProcessScanner struct {
	exeName string
	selfPID uint32
}

func newProcessScanner(exeName string) processScanner {
	if exeName == "" {
		exeName = defaultGameProcess
	}
	return &windowsProcessScanner{exeName: exeName, selfPID: uint32(os.Getpid())}
}

type windowsProcess struct {
	pid       uint32
	exeName   string
	startedAt time.Time
}

func (s *windowsProcessScanner) scan() (GameState, error) {
	processes, err := enumerateWindowsProcesses(true)
	if err != nil {
		return GameState{}, err
	}
	best := oldestMatchingWindowsProcess(processes, s.exeName, s.selfPID)
	if best.pid == 0 {
		return GameState{}, nil
	}
	return GameState{Running: true, PID: int(best.pid), StartedAt: best.startedAt}, nil
}

func oldestMatchingWindowsProcess(processes []windowsProcess, exeName string, selfPID uint32) windowsProcess {
	var best windowsProcess
	for _, process := range processes {
		if process.pid == selfPID || process.startedAt.IsZero() ||
			!strings.EqualFold(baseName(process.exeName), exeName) {
			continue
		}
		if best.pid == 0 || process.startedAt.Before(best.startedAt) {
			best = process
		}
	}
	return best
}

// enumerateWindowsProcesses uses Toolhelp for names and GetProcessTimes for the
// kernel-recorded creation instant. Processes can disappear or deny a handle
// between those calls; those entries are skipped rather than failing the scan.
func enumerateWindowsProcesses(withStartTime bool) ([]windowsProcess, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, err
	}

	var out []windowsProcess
	for {
		process := windowsProcess{
			pid:     entry.ProcessID,
			exeName: windows.UTF16ToString(entry.ExeFile[:]),
		}
		if !withStartTime {
			out = append(out, process)
		} else if started, err := windowsProcessStartTime(entry.ProcessID); err == nil {
			process.startedAt = started
			out = append(out, process)
		}

		entry.Size = uint32(unsafe.Sizeof(windows.ProcessEntry32{}))
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			break
		}
	}
	return out, nil
}

func windowsProcessStartTime(pid uint32) (time.Time, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return time.Time{}, err
	}
	defer windows.CloseHandle(handle)

	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, creation.Nanoseconds()), nil
}

func processScanDescription(exeName string) string {
	return fmt.Sprintf("looking for a process whose executable name is %q", exeName)
}

func processScanError(err error) string {
	return fmt.Sprintf("could not enumerate Windows processes: %v", err)
}

func similarProcesses(needle string) []string {
	processes, err := enumerateWindowsProcesses(false)
	if err != nil {
		return nil
	}
	needle = strings.ToLower(needle)
	self := uint32(os.Getpid())
	var out []string
	for _, process := range processes {
		if process.pid == self || !strings.Contains(strings.ToLower(process.exeName), needle) {
			continue
		}
		out = append(out, fmt.Sprintf("pid %d: %s", process.pid, process.exeName))
		if len(out) >= 8 {
			break
		}
	}
	return out
}
