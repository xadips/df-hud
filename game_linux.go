//go:build linux

package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// userHZ is the kernel clock-tick rate used by /proc/<pid>/stat. It is 100 on
// mainstream Linux builds; reading it dynamically would require cgo.
const userHZ = 100

// procScanner finds the game in /proc. procRoot is configurable for tests.
type procScanner struct {
	procRoot string
	exeName  string
	selfPID  int
}

func newProcScanner(exeName string) *procScanner {
	if exeName == "" {
		exeName = defaultGameProcess
	}
	return &procScanner{procRoot: "/proc", exeName: exeName, selfPID: os.Getpid()}
}

func newProcessScanner(exeName string) processScanner { return newProcScanner(exeName) }

// scan returns the oldest process whose argv[0] basename matches the game.
// Matching argv[0] only prevents an editor, grep, or df-hud argument from
// creating a phantom game session. Oldest wins when Proton creates helpers with
// the same executable name.
func (s *procScanner) scan() (GameState, error) {
	entries, err := os.ReadDir(s.procRoot)
	if err != nil {
		return GameState{}, err
	}
	boot, err := bootTime(s.procRoot)
	if err != nil {
		return GameState{}, err
	}

	var best GameState
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == s.selfPID {
			continue
		}
		argv0, err := processArgv0(s.procRoot, pid)
		if err != nil || argv0 == "" {
			continue
		}
		if !strings.EqualFold(baseName(argv0), s.exeName) {
			continue
		}
		started, err := processStartTime(s.procRoot, pid, boot)
		if err != nil {
			continue
		}
		if !best.Running || started.Before(best.StartedAt) {
			best = GameState{Running: true, PID: pid, StartedAt: started}
		}
	}
	return best, nil
}

func processArgv0(procRoot string, pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return "", err
	}
	argv0, _, _ := strings.Cut(string(data), "\x00")
	return argv0, nil
}

// processStartTime converts field 22 of /proc/<pid>/stat from ticks since boot
// to wall time.
func processStartTime(procRoot string, pid int, boot time.Time) (time.Time, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return time.Time{}, err
	}
	// comm may contain spaces and parentheses, so field parsing starts after the
	// final ')' rather than splitting the complete line.
	close := strings.LastIndex(string(data), ")")
	if close < 0 || close+2 >= len(data) {
		return time.Time{}, fmt.Errorf("proc: %d: malformed stat line", pid)
	}
	fields := strings.Fields(string(data[close+2:]))
	const startTimeIndex = 19 // remaining fields begin at field 3
	if len(fields) <= startTimeIndex {
		return time.Time{}, fmt.Errorf("proc: %d: stat has %d fields after comm, want more than %d",
			pid, len(fields), startTimeIndex)
	}
	ticks, err := strconv.ParseInt(fields[startTimeIndex], 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("proc: %d: start time %q: %w", pid, fields[startTimeIndex], err)
	}
	return boot.Add(time.Duration(ticks) * time.Second / userHZ), nil
}

func bootTime(procRoot string) (time.Time, error) {
	f, err := os.Open(filepath.Join(procRoot, "stat"))
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "btime ") {
			continue
		}
		secs, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("proc: btime %q: %w", line, err)
		}
		return time.Unix(secs, 0), nil
	}
	if err := scanner.Err(); err != nil {
		return time.Time{}, err
	}
	return time.Time{}, errors.New("proc: /proc/stat has no btime line")
}

func processScanDescription(exeName string) string {
	return fmt.Sprintf("looking for a process whose argv[0] basename is %q", exeName)
}

func processScanError(err error) string { return fmt.Sprintf("could not scan /proc: %v", err) }

// similarProcesses is intentionally broader than the scanner: diagnostics may
// search the entire command line to help discover a differently named binary.
func similarProcesses(needle string) []string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []string
	self, parent := os.Getpid(), os.Getppid()
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == self || pid == parent {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		line := strings.ReplaceAll(string(raw), "\x00", " ")
		lower := strings.ToLower(line)
		if !strings.Contains(lower, strings.ToLower(needle)) || strings.Contains(lower, "df-hud") {
			continue
		}
		if len(line) > 120 {
			line = line[:120] + "..."
		}
		out = append(out, fmt.Sprintf("pid %d: %s", pid, strings.TrimSpace(line)))
		if len(out) >= 8 {
			break
		}
	}
	return out
}
