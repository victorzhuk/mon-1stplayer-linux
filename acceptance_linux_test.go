//go:build acceptance

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	accDaemonUser  = "mon-1stplayer"
	accDetachedEnv = "MON1STPLAYER_ACCEPTANCE_DETACHED"

	accSetupBudget    = 60 * time.Second
	accScenarioBudget = 15 * time.Second
	accCleanupBudget  = 60 * time.Second
	accOverallBudget  = 420 * time.Second
	accNodeWait       = 10 * time.Second

	accBinEnv = "MON1STPLAYER_ACCEPTANCE_BIN"
)

var accFixedBlock = []byte{
	0xfd, 0x08, 0x08, 0x08, 0xfd, 0x08, 0xdd, 0x0f,
	0xd0, 0xdf, 0x8f, 0x00, 0x00, 0x00, 0x00, 0x00,
}

var (
	accRunStart   = time.Now()
	accFixtureSeq int
)

type accFixture struct {
	t       *testing.T
	fd      int
	uniq    string
	outputs chan accOutput
	errs    chan error
	done    chan struct{}
	stop    chan struct{}
	devNode string
	desc    []byte

	destroyOnce   sync.Once
	shutdownOnce  sync.Once
	destroyResult error
}

func (d *accDaemon) sigterm() {
	_ = d.cmd.Process.Signal(syscall.SIGTERM)
}

func acceptanceDaemonPath() (string, error) {
	p := os.Getenv(accBinEnv)
	if p == "" {
		return "", fmt.Errorf("%s is required; no fallback to an installed daemon binary", accBinEnv)
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%s must be an absolute path, got %q", accBinEnv, p)
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("%s: %w", accBinEnv, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s %s is not a regular file", accBinEnv, p)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%s %s is not executable", accBinEnv, p)
	}
	return p, nil
}

func requireAcceptanceEnvironment(t *testing.T) {
	t.Helper()
	if os.Getenv(accDetachedEnv) != "1" {
		t.Fatalf("refused: %s=1 is required for the acceptance suite", accDetachedEnv)
	}
	if os.Geteuid() != 0 {
		t.Fatal("refused: acceptance suite must run as root for /dev/uhid")
	}
	if n := accCountRealCandidates(); n > 0 {
		t.Fatalf("refused: %d real 5131:2007 candidate(s) present in sysfs; physical display must be disconnected", n)
	}
	if _, err := user.Lookup(accDaemonUser); err != nil {
		t.Fatalf("refused: dedicated account %q missing: %v", accDaemonUser, err)
	}
	if _, err := acceptanceDaemonPath(); err != nil {
		t.Fatalf("refused: %v", err)
	}
	svc := exec.Command("systemctl", "is-active", "--quiet", "mon-1stplayer.service")
	err := svc.Run()
	var ee *exec.ExitError
	if err == nil {
		t.Fatal("refused: mon-1stplayer.service is active; stop it before running acceptance scenarios")
	}
	if !errors.As(err, &ee) {
		t.Fatalf("refused: service state query failed: %v", err)
	}
	if time.Since(accRunStart) > accOverallBudget {
		t.Fatalf("refused: overall acceptance budget of %s exceeded", accOverallBudget)
	}
}

func accCountRealCandidates() int {
	entries, err := filepath.Glob("/sys/class/hidraw/hidraw*/device/uevent")
	if err != nil {
		return len(entries) + 1
	}
	n := 0
	for _, p := range entries {
		data, err := os.ReadFile(p)
		if err != nil {
			n++
			continue
		}
		if hasLine(data, "HID_ID=0003:00005131:00002007") {
			n++
		}
	}
	return n
}

func accDescriptor() []byte {
	return mustDescriptor("0600ff0901a101090215002600ff750895408106090215002600ff750895409106c0")
}

func newAccFixture(t *testing.T, descriptor []byte) *accFixture {
	t.Helper()
	fd, err := syscall.Open("/dev/uhid", syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open /dev/uhid: %v", err)
	}
	accFixtureSeq++
	uniq := fmt.Sprintf("mon1st-acc-%d-%d", os.Getpid(), accFixtureSeq)

	ev := make([]byte, uhidEventSize)
	binary.LittleEndian.PutUint32(ev[0:4], uhidCreate2)
	copy(ev[4:132], "HID 5131:2007")
	copy(ev[196:260], uniq)
	binary.LittleEndian.PutUint16(ev[260:262], uint16(len(descriptor)))
	binary.LittleEndian.PutUint16(ev[262:264], 0x0003)
	binary.LittleEndian.PutUint32(ev[264:268], 0x5131)
	binary.LittleEndian.PutUint32(ev[268:272], 0x2007)
	copy(ev[280:280+len(descriptor)], descriptor)

	if err := uhidWrite(fd, ev); err != nil {
		_ = syscall.Close(fd)
		t.Fatalf("UHID_CREATE2: %v", err)
	}

	fx := &accFixture{
		t:       t,
		fd:      fd,
		uniq:    uniq,
		outputs: make(chan accOutput, 256),
		errs:    make(chan error, 8),
		done:    make(chan struct{}),
		stop:    make(chan struct{}),
		desc:    descriptor,
	}
	t.Cleanup(func() {
		if err := fx.destroy(); err != nil {
			t.Fatalf("fixture %s cleanup: %v", fx.uniq, err)
		}
	})
	go fx.readLoop()

	fx.devNode = fx.awaitHidrawNode()
	fx.grantNode()
	return fx
}

func uhidWrite(fd int, ev []byte) error {
	deadline := time.Now().Add(time.Second)
	for {
		n, err := syscall.Write(fd, ev)
		if err == nil {
			if n != len(ev) {
				return fmt.Errorf("short uhid write %d of %d bytes", n, len(ev))
			}
			return nil
		}
		if !errors.Is(err, syscall.EAGAIN) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (fx *accFixture) readLoop() {
	defer close(fx.done)
	defer close(fx.outputs)
	defer fx.readerShutdown()

	buf := make([]byte, uhidEventSize)
	for {
		n, err := syscall.Read(fx.fd, buf)
		select {
		case <-fx.stop:
			return
		default:
		}
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if errors.Is(err, syscall.EAGAIN) {
				timer := time.NewTimer(10 * time.Millisecond)
				select {
				case <-fx.stop:
					timer.Stop()
					return
				case <-timer.C:
				}
				continue
			}
			fx.publishErr(fmt.Errorf("read /dev/uhid: %w", err))
			return
		}
		if n == 0 {
			continue
		}
		full := make([]byte, uhidEventSize)
		copy(full, buf[:n])
		switch binary.LittleEndian.Uint32(full[0:4]) {
		case uhidStart:
			if binary.LittleEndian.Uint64(full[4:12]) != 0 {
				fx.publishErr(errors.New("uhid device reports numbered reports; descriptor parsed unexpectedly"))
				return
			}
		case uhidOutput:
			out, derr := decodeAccOutput(full)
			if derr != nil {
				fx.publishErr(derr)
				return
			}
			select {
			case fx.outputs <- out:
			default:
				fx.publishErr(errors.New("output channel full; capture overflow"))
				return
			}
		case uhidGetReport:
			reply := make([]byte, uhidEventSize)
			binary.LittleEndian.PutUint32(reply[0:4], uhidGetReportReply)
			copy(reply[4:8], full[4:8])
			binary.LittleEndian.PutUint16(reply[8:10], uint16(syscall.EIO))
			if werr := uhidWrite(fx.fd, reply); werr != nil {
				fx.publishErr(fmt.Errorf("GET_REPORT reply: %w", werr))
				return
			}
		case uhidSetReport:
			reply := make([]byte, uhidEventSize)
			binary.LittleEndian.PutUint32(reply[0:4], uhidSetReportReply)
			copy(reply[4:8], full[4:8])
			binary.LittleEndian.PutUint16(reply[8:10], uint16(syscall.EIO))
			if werr := uhidWrite(fx.fd, reply); werr != nil {
				fx.publishErr(fmt.Errorf("SET_REPORT reply: %w", werr))
				return
			}
		case uhidStop, uhidOpen, uhidClose:
		}
	}
}

func (fx *accFixture) publishErr(err error) {
	select {
	case fx.errs <- err:
	default:
	}
}

func (fx *accFixture) readerShutdown() {
	fx.shutdownOnce.Do(func() {
		ev := make([]byte, uhidEventSize)
		binary.LittleEndian.PutUint32(ev[0:4], uhidDestroy)
		_ = uhidWrite(fx.fd, ev)
		_ = syscall.Close(fx.fd)
	})
}

func (fx *accFixture) matchUevent(data string) bool {
	return hasLine([]byte(data), "HID_ID=0003:00005131:00002007") &&
		hasLine([]byte(data), "HID_UNIQ="+fx.uniq)
}

func (fx *accFixture) identityHeld() bool {
	data, err := os.ReadFile(filepath.Join("/sys/class/hidraw", filepath.Base(fx.devNode), "device", "uevent"))
	if err != nil || !fx.matchUevent(string(data)) {
		return false
	}
	desc, err := os.ReadFile(filepath.Join("/sys/class/hidraw", filepath.Base(fx.devNode), "device", "report_descriptor"))
	if err != nil {
		return false
	}
	return bytes.Equal(bytes.TrimRight(desc, "\x00"), fx.desc)
}

func (fx *accFixture) awaitHidrawNode() string {
	fx.t.Helper()
	deadline := time.Now().Add(accNodeWait)
	for time.Now().Before(deadline) {
		entries, _ := filepath.Glob("/sys/class/hidraw/hidraw*")
		for _, dir := range entries {
			data, err := os.ReadFile(filepath.Join(dir, "device", "uevent"))
			if err != nil || !fx.matchUevent(string(data)) {
				continue
			}
			desc, err := os.ReadFile(filepath.Join(dir, "device", "report_descriptor"))
			if err != nil || !bytes.Equal(bytes.TrimRight(desc, "\x00"), fx.desc) {
				continue
			}
			return "/dev/" + filepath.Base(dir)
		}
		time.Sleep(50 * time.Millisecond)
	}
	fx.t.Fatalf("fixture %s: hidraw node did not appear within %s", fx.uniq, accNodeWait)
	return ""
}

func (fx *accFixture) grantNode() {
	fx.t.Helper()
	u, err := user.Lookup(accDaemonUser)
	if err != nil {
		fx.t.Fatalf("lookup %s: %v", accDaemonUser, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		fx.t.Fatalf("invalid uid %q for %s: %v", u.Uid, accDaemonUser, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		fx.t.Fatalf("invalid gid %q for %s: %v", u.Gid, accDaemonUser, err)
	}
	if err := syscall.Chown(fx.devNode, uid, gid); err != nil {
		fx.t.Fatalf("chown %s: %v", fx.devNode, err)
	}
	if !fx.identityHeld() {
		fx.t.Fatalf("fixture %s: identity lost at %s before permission change", fx.uniq, fx.devNode)
	}
	if err := os.Chmod(fx.devNode, 0o660); err != nil {
		fx.t.Fatalf("chmod %s: %v", fx.devNode, err)
	}
}

func (fx *accFixture) destroy() error {
	fx.destroyOnce.Do(func() {
		fx.destroyResult = fx.stopReaderAndVerify()
	})
	return fx.destroyResult
}

func (fx *accFixture) stopReaderAndVerify() error {
	select {
	case <-fx.stop:
	default:
		close(fx.stop)
	}
	select {
	case <-fx.done:
	case <-time.After(accCleanupBudget):
		return fmt.Errorf("fixture %s: reader did not stop within %s", fx.uniq, accCleanupBudget)
	}
	select {
	case err := <-fx.errs:
		return fmt.Errorf("fixture %s: %w", fx.uniq, err)
	default:
	}
	deadline := time.Now().Add(accCleanupBudget)
	for {
		gone := true
		entries, err := filepath.Glob("/sys/class/hidraw/hidraw*/device/uevent")
		if err != nil {
			return fmt.Errorf("fixture %s: cleanup scan failed: %w", fx.uniq, err)
		}
		for _, p := range entries {
			data, err := os.ReadFile(p)
			if err != nil {
				return fmt.Errorf("fixture %s: cleanup read %s: %w", fx.uniq, p, err)
			}
			if fx.matchUevent(string(data)) {
				gone = false
				break
			}
		}
		if gone {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("fixture %s: hidraw node still present after %s", fx.uniq, accCleanupBudget)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (fx *accFixture) drain() int {
	n := 0
	for {
		select {
		case _, ok := <-fx.outputs:
			if !ok {
				return n
			}
			n++
		default:
			return n
		}
	}
}

func (fx *accFixture) waitReports(n int, budget time.Duration) [][]byte {
	fx.t.Helper()
	var got [][]byte
	deadline := time.NewTimer(budget)
	defer deadline.Stop()
	for len(got) < n {
		select {
		case out, ok := <-fx.outputs:
			if !ok {
				fx.t.Fatalf("fixture %s: output channel closed after %d/%d reports", fx.uniq, len(got), n)
			}
			got = append(got, out.data)
		case err := <-fx.errs:
			fx.t.Fatalf("fixture %s: %v", fx.uniq, err)
		case <-deadline.C:
			fx.t.Fatalf("fixture %s: got %d/%d reports within %s", fx.uniq, len(got), n, budget)
		}
	}
	return got
}

func startAccDaemon(t *testing.T, args ...string) *accDaemon {
	return startAccDaemonWithEnv(t, nil, args...)
}

var accOperationalEnvKeys = []string{
	"MON1STPLAYER_SHOW",
	"MON1STPLAYER_UNIT",
	"MON1STPLAYER_AUTO_INTERVAL",
	"MON1STPLAYER_FRAME_INTERVAL",
	"MON1STPLAYER_RETRY_DELAY",
}

func startAccDaemonWithEnv(t *testing.T, env []string, args ...string) *accDaemon {
	t.Helper()
	u, err := user.Lookup(accDaemonUser)
	if err != nil {
		t.Fatalf("lookup %s: %v", accDaemonUser, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		t.Fatalf("invalid uid %q for %s: %v", u.Uid, accDaemonUser, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		t.Fatalf("invalid gid %q for %s: %v", u.Gid, accDaemonUser, err)
	}
	bin, err := acceptanceDaemonPath()
	if err != nil {
		t.Fatalf("acceptance daemon path: %v", err)
	}
	drop := make(map[string]bool, len(accOperationalEnvKeys))
	for _, k := range accOperationalEnvKeys {
		drop[k] = true
	}
	var childEnv []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if drop[key] {
			continue
		}
		childEnv = append(childEnv, kv)
	}
	childEnv = append(childEnv, env...)
	cmd := exec.Command(bin, args...)
	cmd.Env = childEnv
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)},
	}
	d, err := startAccProcess(cmd)
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	t.Cleanup(func() {
		if kerr := d.kill(accCleanupBudget); kerr != nil {
			t.Fatalf("daemon cleanup: %v", kerr)
		}
	})
	return d
}

func accWirePayload(t *testing.T, data []byte) []byte {
	t.Helper()
	if len(data) != 65 {
		t.Fatalf("captured output has %d bytes, want 65-byte hidraw buffer", len(data))
	}
	if data[0] != 0 {
		t.Fatalf("captured output report-ID prefix is 0x%02x, want 0x00", data[0])
	}
	return data[1:65]
}

func accAssertRefresh(t *testing.T, p []byte) {
	t.Helper()
	if p[1] != 0x00 {
		t.Fatalf("command byte 0x%02x, want 0x00", p[1])
	}
	if p[2] != 0x02 {
		t.Fatalf("page byte 0x%02x, want 0x02", p[2])
	}
	for i := 4; i < 24; i++ {
		if p[i] != 0 {
			t.Fatalf("payload offset %d is 0x%02x, want 0", i, p[i])
		}
	}
	if !bytes.Equal(p[24:40], accFixedBlock) {
		t.Fatalf("fixed block mismatch: % x", p[24:40])
	}
	for i := 56; i < 64; i++ {
		if p[i] != 0 {
			t.Fatalf("payload offset %d is 0x%02x, want 0", i, p[i])
		}
	}
}

func accAssertClose(t *testing.T, p []byte) {
	t.Helper()
	if p[0] != 0 || p[1] != 0x0f {
		t.Fatalf("close payload header % x, want 00 0f", p[:2])
	}
	for i := 2; i < 64; i++ {
		if p[i] != 0 {
			t.Fatalf("close payload offset %d is 0x%02x, want 0", i, p[i])
		}
	}
}

func accAssertModeBits(t *testing.T, p []byte, want map[int]byte, forbid map[int]byte) {
	t.Helper()
	for i, mask := range want {
		if p[40+i]&mask != mask {
			t.Fatalf("mode bits at live offset %d are 0x%02x, want 0x%02x set", i, p[40+i], mask)
		}
	}
	for i, mask := range forbid {
		if p[40+i]&mask != 0 {
			t.Fatalf("mode bits at live offset %d are 0x%02x, want 0x%02x clear", i, p[40+i], mask)
		}
	}
}

var (
	accUtilizationBits = map[int]byte{1: 0x06, 3: 0x04, 5: 0x04, 7: 0x04, 9: 0x04}
	accFahrenheitBits  = map[int]byte{3: 0x02, 9: 0x02}
)

func TestAcceptanceMainJourney(t *testing.T) {
	requireAcceptanceEnvironment(t)
	fx := newAccFixture(t, accDescriptor())
	startAccDaemon(t)

	start := time.Now()
	raw := fx.waitReports(10, accScenarioBudget)
	span := time.Since(start)
	if span > 3*time.Second {
		t.Fatalf("ten reports took %s, want about 2s", span)
	}
	var prev byte
	for i, r := range raw {
		p := accWirePayload(t, r)
		accAssertRefresh(t, p)
		if i > 0 && p[0] != prev+1 {
			t.Fatalf("sequence jumped: %d after %d", p[0], prev)
		}
		prev = p[0]
	}
}

func TestAcceptanceAmbiguityRefusal(t *testing.T) {
	requireAcceptanceEnvironment(t)
	fa := newAccFixture(t, accDescriptor())
	fb := newAccFixture(t, accDescriptor())
	d := startAccDaemon(t, "-validate-frame")
	code, werr := d.waitExit(accScenarioBudget)
	if code != 2 || werr == nil {
		t.Fatalf("ambiguous candidates: exit %d err %v, want 2; stderr: %s", code, werr, d.stderr.String())
	}
	if n := fa.drain() + fb.drain(); n != 0 {
		t.Fatalf("ambiguous candidates produced %d output reports, want 0", n)
	}
}

func TestAcceptanceWrongDescriptorRefusal(t *testing.T) {
	requireAcceptanceEnvironment(t)
	desc := accDescriptor()
	desc[4] = 0x02
	fx := newAccFixture(t, desc)
	d := startAccDaemon(t, "-validate-frame")
	code, werr := d.waitExit(accScenarioBudget)
	if code != 2 || werr == nil {
		t.Fatalf("wrong descriptor: exit %d err %v, want 2; stderr: %s", code, werr, d.stderr.String())
	}
	if n := fx.drain(); n != 0 {
		t.Fatalf("wrong descriptor produced %d output reports, want 0", n)
	}
}

func TestAcceptanceReconnect(t *testing.T) {
	requireAcceptanceEnvironment(t)
	fx := newAccFixture(t, accDescriptor())
	startAccDaemon(t)

	raw := fx.waitReports(10, accScenarioBudget)
	var lastOld byte
	for i, r := range raw {
		p := accWirePayload(t, r)
		accAssertRefresh(t, p)
		if i > 0 && p[0] != lastOld+1 {
			t.Fatalf("initial sequence jumped: %d after %d", p[0], lastOld)
		}
		lastOld = p[0]
	}
	if lastOld == 0 {
		t.Fatal("pre-disconnect sequence is zero, want nonzero")
	}

	if err := fx.destroy(); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	time.Sleep(2 * time.Second)

	repl := newAccFixture(t, accDescriptor())
	raw2 := repl.waitReports(4, accScenarioBudget)
	first := accWirePayload(t, raw2[0])
	delta := uint8(first[0] - lastOld)
	if delta < 1 || delta > 127 {
		t.Fatalf("reconnect sequence delta %d from %d, want within 1..127", delta, lastOld)
	}
	for i := 1; i < len(raw2); i++ {
		p := accWirePayload(t, raw2[i])
		accAssertRefresh(t, p)
		if p[0] != accWirePayload(t, raw2[i-1])[0]+1 {
			t.Fatalf("replacement sequence jumped: %d after %d", p[0], accWirePayload(t, raw2[i-1])[0])
		}
	}
}

func TestAcceptanceShutdown(t *testing.T) {
	requireAcceptanceEnvironment(t)
	fx := newAccFixture(t, accDescriptor())
	d := startAccDaemon(t)

	raw := fx.waitReports(1, accScenarioBudget)
	accAssertRefresh(t, accWirePayload(t, raw[0]))

	d.sigterm()
	code, werr := d.waitExit(accScenarioBudget)
	if code != 0 || werr != nil {
		t.Fatalf("shutdown exit %d err %v, want 0; stderr: %s", code, werr, d.stderr.String())
	}
	closeDeadline := time.Now().Add(accScenarioBudget)
	for {
		p := accWirePayload(t, fx.waitReports(1, time.Until(closeDeadline))[0])
		if p[1] == 0x0f {
			accAssertClose(t, p)
			break
		}
		accAssertRefresh(t, p)
	}
	if n := fx.drain(); n != 0 {
		t.Fatalf("shutdown produced %d unexpected trailing reports, want 0", n)
	}
}

func TestAcceptanceDeviceBusy(t *testing.T) {
	requireAcceptanceEnvironment(t)
	fx := newAccFixture(t, accDescriptor())
	mon := startAccDaemon(t)

	raw := fx.waitReports(3, accScenarioBudget)
	if p := accWirePayload(t, raw[2]); p[1] != 0x00 {
		t.Fatalf("monitor frame is not a refresh before the busy attempt: % x", p[:2])
	}

	diag := startAccDaemon(t, "-validate-frame")
	code, werr := diag.waitExit(accScenarioBudget)
	if code != 2 || werr == nil {
		t.Fatalf("busy diagnostic: exit %d err %v, want 2", code, werr)
	}
	if !strings.Contains(diag.stderr.String(), "display is already in use") {
		t.Fatalf("busy diagnostic stderr %q missing %q", diag.stderr.String(), "display is already in use")
	}

	busyWindow := fx.waitReports(2, time.Second)
	for _, r := range busyWindow {
		accAssertRefresh(t, accWirePayload(t, r))
	}

	mon.sigterm()
	mcode, mwerr := mon.waitExit(accScenarioBudget)
	if mcode != 0 || mwerr != nil {
		t.Fatalf("monitor shutdown exit %d err %v, want 0", mcode, mwerr)
	}

	fresh := startAccDaemon(t, "-validate-frame")
	fcode, fwerr := fresh.waitExit(accScenarioBudget)
	if fcode != 0 || fwerr != nil {
		t.Fatalf("fresh diagnostic: exit %d err %v, want 0; stderr: %s", fcode, fwerr, fresh.stderr.String())
	}
}

func TestAcceptanceLongDescriptorRefusal(t *testing.T) {
	requireAcceptanceEnvironment(t)
	desc := append(append([]byte{}, accDescriptor()...), 0x05, 0xff)
	fx := newAccFixture(t, desc)

	f, err := os.OpenFile(fx.devNode, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", fx.devNode, err)
	}
	defer func() { _ = f.Close() }()
	dev := &hidrawDevice{path: fx.devNode, f: f}
	err = dev.validate()
	if err == nil {
		t.Fatalf("validate accepted a %d-byte descriptor, want refusal on descriptor length", len(desc))
	}
	if !strings.Contains(err.Error(), "descriptor size") {
		t.Fatalf("validate error %q is not a descriptor-length refusal", err.Error())
	}
	if n := fx.drain(); n != 0 {
		t.Fatalf("refused device produced %d output reports, want 0", n)
	}
}

func TestAcceptanceEnvironmentMode(t *testing.T) {
	requireAcceptanceEnvironment(t)

	t.Run("utilization", func(t *testing.T) {
		fx := newAccFixture(t, accDescriptor())
		startAccDaemonWithEnv(t, []string{"MON1STPLAYER_SHOW=utilization"})
		raw := fx.waitReports(2, accScenarioBudget)
		p := accWirePayload(t, raw[1])
		accAssertModeBits(t, p, accUtilizationBits, map[int]byte{5: 0x02, 7: 0x02, 3: 0x02, 9: 0x02})
	})

	t.Run("fahrenheit", func(t *testing.T) {
		fx := newAccFixture(t, accDescriptor())
		startAccDaemonWithEnv(t, []string{"MON1STPLAYER_SHOW=temperature", "MON1STPLAYER_UNIT=f"})
		raw := fx.waitReports(2, accScenarioBudget)
		p := accWirePayload(t, raw[1])
		accAssertModeBits(t, p, accFahrenheitBits, map[int]byte{5: 0x02, 7: 0x02, 1: 0x02})
	})

	t.Run("cli override", func(t *testing.T) {
		fx := newAccFixture(t, accDescriptor())
		startAccDaemonWithEnv(t, []string{"MON1STPLAYER_SHOW=temperature"}, "-show", "utilization")
		raw := fx.waitReports(2, accScenarioBudget)
		p := accWirePayload(t, raw[1])
		accAssertModeBits(t, p, accUtilizationBits, map[int]byte{3: 0x02, 9: 0x02, 5: 0x02, 7: 0x02})
	})
}

func TestAcceptanceDiagnosticDeadline(t *testing.T) {
	requireAcceptanceEnvironment(t)
	fx := newAccFixture(t, accDescriptor())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err := runDiagnostic(ctx, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("diagnostic error %v, want context deadline exceeded", err)
	}

	raw := fx.waitReports(2, accScenarioBudget)
	accAssertRefresh(t, accWirePayload(t, raw[0]))
	accAssertClose(t, accWirePayload(t, raw[1]))
	time.Sleep(300 * time.Millisecond)
	if n := fx.drain(); n != 0 {
		t.Fatalf("diagnostic deadline produced %d extra reports after the close frame, want 0", n)
	}
}
