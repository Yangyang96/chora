package apppreview

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type cappedLog struct {
	mu        sync.Mutex
	file      *os.File
	capacity  int64
	written   int64
	truncated bool
}

func openCappedLog(path string, capacity int64) (*cappedLog, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > capacity {
		_ = file.Close()
		return nil, errors.New("app preview log ownership or bounds are invalid")
	}
	return &cappedLog{file: file, capacity: capacity, written: info.Size(), truncated: info.Size() == capacity}, nil
}

func (log *cappedLog) Write(data []byte) (int, error) {
	log.mu.Lock()
	defer log.mu.Unlock()
	remaining := log.capacity - log.written
	if remaining <= 0 {
		log.truncated = true
		return len(data), nil
	}
	write := data
	if int64(len(write)) > remaining {
		write = write[:remaining]
		log.truncated = true
	}
	n, err := log.file.Write(write)
	log.written += int64(n)
	if err == nil && n == len(write) {
		return len(data), nil
	}
	return n, err
}

func (log *cappedLog) Close() error {
	log.mu.Lock()
	defer log.mu.Unlock()
	return log.file.Close()
}

func (manager *Manager) startHost(record manifest) (View, error) {
	if err := manager.checkPort(record.View.Config.Port); err != nil {
		return manager.failStart(record, errors.New("local app preview port is already in use"))
	}
	log, err := openCappedLog(manager.logPath(record.View.Key), manager.logCap)
	if err != nil {
		return manager.failStart(record, err)
	}
	working := record.View.Config.WorkingDirectory
	if working == "" {
		working = "."
	}
	command := exec.Command("/bin/sh", "-lc", record.View.Config.Command)
	command.Dir = filepath.Join(record.View.Target.Root, working)
	command.Env = append(os.Environ(), "HOST=127.0.0.1", "PORT="+strconv.Itoa(record.View.Config.Port))
	command.Stdout, command.Stderr = log, log
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		_ = log.Close()
		return manager.failStart(record, fmt.Errorf("start local app preview: %w", err))
	}
	pgid, birth, err := observeProcess(command.Process.Pid)
	if err != nil || pgid != command.Process.Pid {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
		_ = log.Close()
		return manager.failStart(record, errors.New("prove local app preview process ownership"))
	}
	record.PID, record.PGID, record.Birth = command.Process.Pid, pgid, birth
	record.View.State = StateRunning
	record.View.URL = "http://127.0.0.1:" + strconv.Itoa(record.View.Config.Port)
	if err := manager.persist(record); err != nil {
		_ = stopGroup(pgid, time.Second)
		_, _ = command.Process.Wait()
		_ = log.Close()
		return View{}, err
	}
	child := &hostChild{done: make(chan struct{})}
	manager.children[record.View.Key] = child
	go manager.waitHost(record.View.Key, record.Nonce, command, log, child)
	return manager.withLogSize(record.View), nil
}

func checkLoopbackPort(port int) error {
	listener, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return err
	}
	return listener.Close()
}

func (manager *Manager) waitHost(key, nonce string, command *exec.Cmd, log *cappedLog, child *hostChild) {
	err := command.Wait()
	_ = log.Close()
	close(child.done)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.children[key] == child {
		delete(manager.children, key)
	}
	record, loadErr := manager.load(key)
	if loadErr != nil || record.Nonce != nonce {
		return
	}
	code := -1
	if command.ProcessState != nil {
		code = command.ProcessState.ExitCode()
	}
	record.View.ExitCode = &code
	record.View.LogTruncated = log.truncated
	if record.View.State == StateStopped && !record.View.CleanupRequired {
		record.View.URL = ""
		_ = manager.persist(record)
		return
	}
	alive, groupErr := groupAlive(record.PGID)
	if groupErr != nil || alive {
		record.View.State = StateRecoveryRequired
		record.View.CleanupRequired = true
		record.View.Reason = "preview leader exited while process-group ownership remains"
	} else if code == 0 {
		record.View.State = StateStopped
		record.View.Reason = ""
	} else {
		record.View.State = StateFailed
		record.View.Reason = fmt.Sprintf("preview command exited with status %d", code)
		if err != nil && code < 0 {
			record.View.Reason = "preview command exited without a status"
		}
	}
	_ = manager.persist(record)
}

func (manager *Manager) inspectHost(record manifest) manifest {
	pgid, birth, err := observeProcess(record.PID)
	if err == nil && pgid == record.PGID && birth == record.Birth {
		record.View.State = StateRunning
		return record
	}
	alive, groupErr := groupAlive(record.PGID)
	if groupErr != nil || alive {
		record.View.State = StateRecoveryRequired
		record.View.CleanupRequired = true
		if err == nil {
			record.View.Reason = "local preview process identity no longer matches its owner"
		} else {
			record.View.Reason = "local preview leader is gone but its process group cannot be proven absent"
		}
		return record
	}
	// An owned child is finalized by waitHost only after exec.Wait has drained
	// its output and committed exit/log metadata. Process disappearance alone
	// must not publish a terminal state ahead of that commit.
	if manager.children[record.View.Key] != nil {
		return record
	}
	record.View.State = StateStopped
	record.View.CleanupRequired = false
	record.View.Reason = ""
	return record
}

func (manager *Manager) stopHost(record *manifest) error {
	pgid, birth, err := observeProcess(record.PID)
	if errors.Is(err, os.ErrProcessDone) {
		alive, groupErr := groupAlive(record.PGID)
		if groupErr != nil {
			return groupErr
		}
		if alive {
			return errors.New("preview leader is gone; refusing to signal an unowned process group")
		}
		record.View.State, record.View.CleanupRequired, record.View.URL = StateStopped, false, ""
		return nil
	}
	if err != nil || pgid != record.PGID || birth != record.Birth {
		return errors.New("local preview process identity does not match durable ownership")
	}
	if err := stopGroup(record.PGID, 2*time.Second); err != nil {
		return err
	}
	record.View.State, record.View.CleanupRequired, record.View.Reason, record.View.URL = StateStopped, false, "", ""
	return nil
}

func (manager *Manager) failStart(record manifest, err error) (View, error) {
	record.View.State = StateFailed
	record.View.Reason = err.Error()
	record.View.CleanupRequired = false
	if persistErr := manager.persist(record); persistErr != nil {
		return View{}, errors.Join(err, persistErr)
	}
	return manager.withLogSize(record.View), err
}
