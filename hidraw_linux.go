package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var errDeviceNotFound = errors.New("1STPLAYER display not found")

var errDeviceBusy = errors.New("display is already in use")

const (
	hidBusUSB  = 0x03
	hidVendor  = 0x5131
	hidProduct = 0x2007

	hidDescriptorSize = 34

	iocRead = 2

	hidIOCGRDESC     = iocRead<<30 | ((4 + 4096) << 16) | ('H' << 8) | 0x02
	hidIOCGRDESCSIZE = iocRead<<30 | (4 << 16) | ('H' << 8) | 0x01
	hidIOCGRAWINFO   = iocRead<<30 | (8 << 16) | ('H' << 8) | 0x03
)

type hidrawReportDescriptor struct {
	size  uint32
	value [4096]byte
}

type hidrawDevinfo struct {
	busType uint32
	vendor  int16
	product int16
}

type hidrawDevice struct {
	path string
	f    *os.File
}

var wantDescriptor = mustDescriptor("0600ff0901a101090215002600ff750895408106090215002600ff750895409106c0")

func mustDescriptor(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != hidDescriptorSize {
		panic(fmt.Sprintf("bad built-in report descriptor: %d bytes", len(b)))
	}
	return b
}

func openHidrawDevice() (*hidrawDevice, error) {
	uevents, err := filepath.Glob("/sys/class/hidraw/hidraw*/device/uevent")
	if err != nil {
		return nil, fmt.Errorf("scan hidraw devices: %w", err)
	}
	wantID := fmt.Sprintf("HID_ID=%04X:%08X:%08X", hidBusUSB, hidVendor, hidProduct)

	var candidates []string
	for _, uevent := range uevents {
		data, err := os.ReadFile(uevent)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", uevent, err)
		}
		if !hasLine(data, wantID) {
			continue
		}
		hidraw := filepath.Base(filepath.Dir(filepath.Dir(uevent)))
		candidates = append(candidates, "/dev/"+hidraw)
	}

	switch len(candidates) {
	case 0:
		return nil, errDeviceNotFound
	case 1:
	default:
		return nil, fmt.Errorf("multiple 1STPLAYER hidraw candidates: %s", strings.Join(candidates, ", "))
	}

	path := candidates[0]
	sysDescriptor := filepath.Join("/sys/class/hidraw", filepath.Base(path), "device", "report_descriptor")
	descriptor, err := os.ReadFile(sysDescriptor)
	if err != nil {
		return nil, deviceOpenError(fmt.Errorf("read descriptor %s: %w", sysDescriptor, err), nil)
	}
	if string(descriptor) != string(wantDescriptor) {
		return nil, fmt.Errorf("%s: unexpected report descriptor", path)
	}

	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return nil, deviceOpenError(fmt.Errorf("open %s: %w", path, err), nil)
	}
	if err := lockHidrawFile(f); err != nil {
		return nil, deviceOpenError(fmt.Errorf("lock %s: %w", path, err), f.Close())
	}
	d := &hidrawDevice{path: path, f: f}
	if err := d.validate(); err != nil {
		return nil, deviceOpenError(err, d.f.Close())
	}
	return d, nil
}

func isDeviceDisappearance(err error) bool {
	if err == nil {
		return false
	}
	var multi interface{ Unwrap() []error }
	if errors.As(err, &multi) {
		return false
	}
	return errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, syscall.ENODEV) ||
		errors.Is(err, syscall.ENXIO)
}

func deviceOpenError(err error, closeErr error) error {
	if closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if isDeviceDisappearance(err) {
		return fmt.Errorf("%w: %w", errDeviceNotFound, err)
	}
	return err
}

func lockHidrawFile(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("%w: %w", errDeviceBusy, err)
		}
		return fmt.Errorf("flock: %w", err)
	}
	return nil
}

func (d *hidrawDevice) validate() error {
	var info hidrawDevinfo
	if err := ioctl(d.f.Fd(), hidIOCGRAWINFO, unsafe.Pointer(&info)); err != nil {
		return fmt.Errorf("query %s: %w", d.path, err)
	}
	if info.busType != hidBusUSB || uint16(info.vendor) != hidVendor || uint16(info.product) != hidProduct {
		return fmt.Errorf("%s: identity mismatch: bus=%d vendor=%#x product=%#x",
			d.path, info.busType, info.vendor, info.product)
	}
	var size int32
	if err := ioctl(d.f.Fd(), hidIOCGRDESCSIZE, unsafe.Pointer(&size)); err != nil {
		return fmt.Errorf("query descriptor size %s: %w", d.path, err)
	}
	if size != hidDescriptorSize {
		return fmt.Errorf("%s: unexpected report descriptor size on fd: %d", d.path, size)
	}
	var desc hidrawReportDescriptor
	desc.size = hidDescriptorSize
	if err := ioctl(d.f.Fd(), hidIOCGRDESC, unsafe.Pointer(&desc)); err != nil {
		return fmt.Errorf("read descriptor %s: %w", d.path, err)
	}
	if desc.size != hidDescriptorSize || string(desc.value[:hidDescriptorSize]) != string(wantDescriptor) {
		return fmt.Errorf("%s: unexpected report descriptor on fd", d.path)
	}
	return nil
}

func (d *hidrawDevice) writePayload(payload [64]byte) error {
	report := [65]byte{0}
	copy(report[1:], payload[:])
	n, err := d.f.Write(report[:])
	if err != nil {
		return fmt.Errorf("write %s: %w", d.path, err)
	}
	if n != len(report) {
		return fmt.Errorf("write %s: short write %d of %d bytes", d.path, n, len(report))
	}
	return nil
}

func (d *hidrawDevice) close() error {
	if err := d.f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", d.path, err)
	}
	return nil
}

func hasLine(data []byte, line string) bool {
	for _, l := range strings.Split(string(data), "\n") {
		if l == line {
			return true
		}
	}
	return false
}

func ioctl(fd uintptr, request uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}
