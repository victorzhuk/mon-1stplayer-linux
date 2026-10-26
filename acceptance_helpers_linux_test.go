package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	uhidEventSize      = 4376
	uhidDestroy        = 1
	uhidStart          = 2
	uhidStop           = 3
	uhidOpen           = 4
	uhidClose          = 5
	uhidOutput         = 6
	uhidGetReport      = 9
	uhidGetReportReply = 10
	uhidCreate2        = 11
	uhidSetReport      = 13
	uhidSetReportReply = 14

	uhidOutputReportType = 1
)

type accOutput struct {
	size int
	data []byte
}

type accDaemon struct {
	cmd      *exec.Cmd
	stdout   bytes.Buffer
	stderr   bytes.Buffer
	done     chan struct{}
	waitErr  error
	killOnce sync.Once
}

func startAccProcess(cmd *exec.Cmd) (*accDaemon, error) {
	d := &accDaemon{cmd: cmd}
	cmd.Stdout = &d.stdout
	cmd.Stderr = &d.stderr
	cmd.WaitDelay = time.Second
	d.done = make(chan struct{})
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		d.waitErr = cmd.Wait()
		close(d.done)
	}()
	return d, nil
}

func (d *accDaemon) kill(budget time.Duration) error {
	var sigErr error
	d.killOnce.Do(func() {
		if d.cmd.Process == nil {
			sigErr = errors.New("process was never started")
			return
		}
		if err := d.cmd.Process.Signal(syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
			sigErr = err
		}
	})
	if sigErr != nil {
		return sigErr
	}
	select {
	case <-d.done:
		return nil
	case <-time.After(budget):
		return fmt.Errorf("process did not exit within %s after signal", budget)
	}
}

func (d *accDaemon) waitExit(budget time.Duration) (int, error) {
	select {
	case <-d.done:
	case <-time.After(budget):
		if err := d.kill(2 * time.Second); err != nil {
			return -1, fmt.Errorf("process did not exit within %s and could not be reaped: %w", budget, err)
		}
	}
	if d.cmd.ProcessState == nil {
		return -1, errors.New("process exit state missing")
	}
	return d.cmd.ProcessState.ExitCode(), d.waitErr
}

func decodeAccOutput(event []byte) (accOutput, error) {
	if len(event) != uhidEventSize {
		return accOutput{}, fmt.Errorf("uhid event storage length %d, want %d", len(event), uhidEventSize)
	}
	if got := binary.LittleEndian.Uint32(event[0:4]); got != uhidOutput {
		return accOutput{}, fmt.Errorf("uhid event type %d, want UHID_OUTPUT(%d)", got, uhidOutput)
	}
	if rtype := event[4102]; rtype != uhidOutputReportType {
		return accOutput{}, fmt.Errorf("uhid output report type %d, want %d", rtype, uhidOutputReportType)
	}
	size := int(binary.LittleEndian.Uint16(event[4100:4102]))
	if size > 4096 {
		return accOutput{}, fmt.Errorf("uhid output size %d exceeds 4096", size)
	}
	data := make([]byte, size)
	copy(data, event[4:4+size])
	return accOutput{size: size, data: data}, nil
}

const accHelperMarker = "MON1STPLAYER_TEST_HELPER_PROCESS"

func accHelperCommand(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.timeout=60s")
	cmd.Env = append(os.Environ(), accHelperMarker+"="+mode)
	return cmd
}

func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(accHelperMarker)
	if mode == "" {
		return
	}
	switch mode {
	case "success":
		fmt.Println("helper output")
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "helper failure")
		os.Exit(3)
	case "writer":
		for {
			fmt.Fprintln(os.Stderr, "helper still writing")
			time.Sleep(10 * time.Millisecond)
		}
	default:
		os.Exit(4)
	}
}

func TestStartAccProcessSuccess(t *testing.T) {
	d, err := startAccProcess(accHelperCommand(t, "success"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	code, waitErr := d.waitExit(10 * time.Second)
	if code != 0 || waitErr != nil {
		t.Fatalf("exit code %d err %v, want 0 nil", code, waitErr)
	}
	if got := d.stdout.String(); !strings.Contains(got, "helper output") {
		t.Fatalf("stdout %q missing helper output", got)
	}
	if err := d.kill(time.Second); err != nil {
		t.Fatalf("cleanup after exit: %v", err)
	}
}

func TestStartAccProcessNonzeroExit(t *testing.T) {
	d, err := startAccProcess(accHelperCommand(t, "fail"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	code, waitErr := d.waitExit(10 * time.Second)
	if code != 3 {
		t.Fatalf("exit code %d, want 3", code)
	}
	var ee *exec.ExitError
	if !errors.As(waitErr, &ee) {
		t.Fatalf("wait error %v, want *exec.ExitError", waitErr)
	}
	if !strings.Contains(d.stderr.String(), "helper failure") {
		t.Fatalf("stderr %q missing helper failure", d.stderr.String())
	}
}

func TestStartAccProcessStartFailure(t *testing.T) {
	_, err := startAccProcess(exec.Command("/nonexistent/mon1st-helper"))
	if err == nil {
		t.Fatal("start of nonexistent binary succeeded, want error")
	}
}

func TestStartAccProcessKillOnTimeout(t *testing.T) {
	d, err := startAccProcess(accHelperCommand(t, "writer"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	code, waitErr := d.waitExit(500 * time.Millisecond)
	if code != -1 {
		t.Fatalf("exit code %d, want -1 after SIGKILL", code)
	}
	var ee *exec.ExitError
	if !errors.As(waitErr, &ee) {
		t.Fatalf("wait error %v, want *exec.ExitError", waitErr)
	}
	if d.cmd.ProcessState == nil {
		t.Fatal("process state missing after kill")
	}
}

func TestStartAccProcessRepeatedCleanup(t *testing.T) {
	d, err := startAccProcess(accHelperCommand(t, "success"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, werr := d.waitExit(10 * time.Second); werr != nil {
		t.Fatalf("wait: %v", werr)
	}
	for range 3 {
		if err := d.kill(2 * time.Second); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}
}

func TestDecodeAccOutput(t *testing.T) {
	newEvent := func(size int, eventType uint32, rtype byte) []byte {
		ev := make([]byte, uhidEventSize)
		binary.LittleEndian.PutUint32(ev[0:4], eventType)
		for i := 0; i < size && i < 4096; i++ {
			ev[4+i] = byte(i % 251)
		}
		binary.LittleEndian.PutUint16(ev[4100:4102], uint16(size))
		ev[4102] = rtype
		return ev
	}
	valid := func(size int) (accOutput, error) {
		return decodeAccOutput(newEvent(size, uhidOutput, uhidOutputReportType))
	}

	for _, size := range []int{0, 1, 65, 4096} {
		out, err := valid(size)
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if out.size != size || len(out.data) != size {
			t.Fatalf("size %d: decoded size %d data %d", size, out.size, len(out.data))
		}
	}
	for _, size := range []int{4097, 65535} {
		if _, err := valid(size); err == nil {
			t.Fatalf("size %d accepted, want rejection", size)
		}
	}
	ev := newEvent(65, uhidOutput, uhidOutputReportType)
	out, err := decodeAccOutput(ev)
	if err != nil {
		t.Fatalf("valid event: %v", err)
	}
	ev[4] = 0xff
	if out.data[0] != 0 {
		t.Fatal("decoded data aliases the event buffer, want a copy")
	}
	if _, err := decodeAccOutput(newEvent(65, uhidStart, uhidOutputReportType)); err == nil {
		t.Fatal("non-output event type accepted, want rejection")
	}
	if _, err := decodeAccOutput(newEvent(65, uhidOutput, 2)); err == nil {
		t.Fatal("report type 2 accepted, want rejection")
	}
	if _, err := decodeAccOutput(ev[:100]); err == nil {
		t.Fatal("short input accepted, want rejection")
	}
}

func TestUhidConstantsMatchKernelABI(t *testing.T) {
	kernel := map[string]uint32{
		"UHID_DESTROY":          uhidDestroy,
		"UHID_START":            uhidStart,
		"UHID_STOP":             uhidStop,
		"UHID_OPEN":             uhidOpen,
		"UHID_CLOSE":            uhidClose,
		"UHID_OUTPUT":           uhidOutput,
		"UHID_GET_REPORT":       uhidGetReport,
		"UHID_GET_REPORT_REPLY": uhidGetReportReply,
		"UHID_CREATE2":          uhidCreate2,
		"UHID_SET_REPORT":       uhidSetReport,
		"UHID_SET_REPORT_REPLY": uhidSetReportReply,
	}
	want := map[string]uint32{
		"UHID_DESTROY":          1,
		"UHID_START":            2,
		"UHID_STOP":             3,
		"UHID_OPEN":             4,
		"UHID_CLOSE":            5,
		"UHID_OUTPUT":           6,
		"UHID_GET_REPORT":       9,
		"UHID_GET_REPORT_REPLY": 10,
		"UHID_CREATE2":          11,
		"UHID_SET_REPORT":       13,
		"UHID_SET_REPORT_REPLY": 14,
	}
	if len(kernel) != len(want) {
		t.Fatalf("constant count %d, want %d", len(kernel), len(want))
	}
	for name, value := range want {
		if kernel[name] != value {
			t.Errorf("%s = %d, want %d (kernel uhid.h value)", name, kernel[name], value)
		}
	}
	if uhidEventSize != 4376 || uhidOutputReportType != 1 {
		t.Errorf("uhidEventSize = %d, uhidOutputReportType = %d, want 4376 and 1", uhidEventSize, uhidOutputReportType)
	}
}
