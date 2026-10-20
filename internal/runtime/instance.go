package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const instanceLockName = "dmemd.lock"

// InstanceLock is an advisory exclusive lock on the user data directory. The
// kernel releases it automatically when the process exits or crashes, so a
// stale file never blocks a later start. It is independent of the HTTP port.
type InstanceLock struct {
	file *os.File
}

// AcquireInstance takes the per-data-directory process lock. A second live
// process using the same directory fails here before opening the database, even
// if it is configured with a different listening port.
func AcquireInstance(dataDir string) (*InstanceLock, error) {
	if dataDir == "" {
		return nil, errors.New("数据目录为空，无法建立单实例锁")
	}
	if err := EnsurePrivateDir(dataDir); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, instanceLockName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开实例锁 %s: %w", path, err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("设置实例锁权限 %s: %w", path, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("数据目录 %s 已被另一个 dmemd 实例使用", dataDir)
		}
		return nil, fmt.Errorf("锁定数据目录 %s: %w", dataDir, err)
	}
	if err := file.Truncate(0); err == nil {
		_, _ = file.Seek(0, 0)
		_, _ = fmt.Fprintf(file, "pid=%d\n", os.Getpid())
		_ = file.Sync()
	}
	return &InstanceLock{file: file}, nil
}

// Close releases the process lock. It is safe to call more than once.
func (l *InstanceLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	return errors.Join(unlockErr, closeErr)
}
