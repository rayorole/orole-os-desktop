//go:build windows

package sandbox

import (
	"errors"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobObject is a Windows job with KILL_ON_JOB_CLOSE: when the daemon exits or crashes, Windows closes
// the handle and kills every QEMU in it, so VMs never outlive the daemon. The same pattern as
// internal/agents/job_windows.go, which is unexported there.
type jobObject struct {
	mu sync.Mutex
	h  windows.Handle
}

func newJobObject() (*jobObject, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return &jobObject{h: h}, nil
}

// assign puts the process with this pid (and its future children) into the job.
func (j *jobObject) assign(pid int) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.h == 0 {
		return errors.New("job closed")
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ph)
	return windows.AssignProcessToJobObject(j.h, ph)
}

// close releases the handle; with KILL_ON_JOB_CLOSE this kills every process still in the job.
func (j *jobObject) close() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.h != 0 {
		windows.CloseHandle(j.h)
		j.h = 0
	}
}
