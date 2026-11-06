//go:build darwin

package processgroup

import (
	"golang.org/x/sys/unix"
)

// Trusted CLIs create separate process groups for background shell jobs. Keep
// their kernel identities while ancestry is observable, including after reparenting.
// This is lifecycle bookkeeping, not containment of deliberate daemon escapes.
type descendantTracker struct{ members map[int32]unix.Timeval }

func newDescendantTracker(pid int) (*descendantTracker, error) {
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return nil, err
	}
	return &descendantTracker{members: map[int32]unix.Timeval{int32(pid): p.Proc.P_starttime}}, nil
}

func (d *descendantTracker) capture() error {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return err
	}
	live := make(map[int32]unix.KinfoProc, len(processes))
	for _, p := range processes {
		live[p.Proc.P_pid] = p
	}
	for pid, start := range d.members {
		if p, ok := live[pid]; !ok || p.Proc.P_starttime != start {
			delete(d.members, pid)
		}
	}
	for {
		added := false
		for _, p := range processes {
			if _, known := d.members[p.Proc.P_pid]; known {
				continue
			}
			if _, owned := d.members[p.Eproc.Ppid]; owned {
				d.members[p.Proc.P_pid] = p.Proc.P_starttime
				added = true
			}
		}
		if !added {
			return nil
		}
	}
}

func (d *descendantTracker) signal(signal unix.Signal) error {
	for pid, start := range d.members {
		current, err := unix.SysctlKinfoProc("kern.proc.pid", int(pid))
		if err != nil {
			return err
		} // Refresh and retry; an unreadable PID is never signalled.
		if current.Proc.P_starttime != start {
			continue
		}
		if err := unix.Kill(int(pid), signal); err != nil && err != unix.ESRCH {
			return err
		}
	}
	return nil
}

// Freeze the known parents before taking another snapshot, so their ordinary
// children cannot fork between the last ancestry capture and termination.
func (d *descendantTracker) terminate() (bool, error) {
	if err := d.capture(); err != nil {
		return false, err
	}
	for len(d.members) > 0 {
		if err := d.signal(unix.SIGSTOP); err != nil {
			return false, err
		}
		before := make(map[int32]unix.Timeval, len(d.members))
		for pid, start := range d.members {
			before[pid] = start
		}
		if err := d.capture(); err != nil {
			return false, err
		}
		added := false
		for pid, start := range d.members {
			if previous, ok := before[pid]; !ok || previous != start {
				added = true
				break
			}
		}
		if added {
			continue
		}
		if err := d.signal(unix.SIGKILL); err != nil {
			return false, err
		}
		break
	}
	return len(d.members) == 0, nil
}
