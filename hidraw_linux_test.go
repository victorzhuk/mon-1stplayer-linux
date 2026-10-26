package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestIsDeviceDisappearance(t *testing.T) {
	joined := errors.Join(syscall.ENOENT, syscall.EACCES)
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain enoent", syscall.ENOENT, true},
		{"wrapped enoent", fmt.Errorf("read descriptor: %w", syscall.ENOENT), true},
		{"fs err not exist", fs.ErrNotExist, true},
		{"enodev", syscall.ENODEV, true},
		{"enxio", syscall.ENXIO, true},
		{"eacces", syscall.EACCES, false},
		{"eperm", syscall.EPERM, false},
		{"eio", syscall.EIO, false},
		{"joined enoent eacces", joined, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDeviceDisappearance(tc.err); got != tc.want {
				t.Fatalf("isDeviceDisappearance(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestDeviceOpenError(t *testing.T) {
	t.Run("disappearance with nil close error wraps retry sentinel", func(t *testing.T) {
		cause := fmt.Errorf("open %s: %w", "/dev/hidraw9", syscall.ENODEV)
		err := deviceOpenError(cause, nil)
		if !errors.Is(err, errDeviceNotFound) {
			t.Fatalf("err = %v, want errors.Is errDeviceNotFound", err)
		}
		if !errors.Is(err, syscall.ENODEV) {
			t.Fatalf("err = %v, want errors.Is ENODEV", err)
		}
	})
	t.Run("disappearance with close error joins without retry sentinel", func(t *testing.T) {
		cause := fmt.Errorf("open %s: %w", "/dev/hidraw9", syscall.ENOENT)
		closeErr := errors.New("close /dev/hidraw9: bad file descriptor")
		err := deviceOpenError(cause, closeErr)
		if errors.Is(err, errDeviceNotFound) {
			t.Fatalf("err = %v, must not satisfy errDeviceNotFound", err)
		}
		if !errors.Is(err, cause) || !errors.Is(err, closeErr) {
			t.Fatalf("err = %v, want joined cause and closeErr", err)
		}
	})
	t.Run("non-disappearance passes through unchanged", func(t *testing.T) {
		cause := fmt.Errorf("open %s: %w", "/dev/hidraw9", syscall.EACCES)
		err := deviceOpenError(cause, nil)
		if err != cause {
			t.Fatalf("err = %v, want original error", err)
		}
	})
}

func TestLockHidrawFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hidraw0")
	first, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	defer func() { _ = first.Close() }()

	if err := lockHidrawFile(first); err != nil {
		t.Fatalf("first lock: %v", err)
	}

	second, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open second: %v", err)
	}
	defer func() { _ = second.Close() }()

	err = lockHidrawFile(second)
	if !errors.Is(err, errDeviceBusy) {
		t.Fatalf("second lock err = %v, want errDeviceBusy", err)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	if err := lockHidrawFile(second); err != nil {
		t.Fatalf("lock after release: %v", err)
	}
}
