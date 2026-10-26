package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	frameInterval = 200 * time.Millisecond
	retryDelay    = time.Second
	exitRuntime   = 1
	exitRefuse    = 2
)

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

type config struct {
	auto          bool
	mode          displayMode
	tempMode      displayMode
	autoInterval  time.Duration
	frameInterval time.Duration
	retryDelay    time.Duration
	validate      bool
}

var flagEnvBindings = []struct {
	flag string
	env  string
}{
	{"show", "MON1STPLAYER_SHOW"},
	{"unit", "MON1STPLAYER_UNIT"},
	{"auto-interval", "MON1STPLAYER_AUTO_INTERVAL"},
	{"frame-interval", "MON1STPLAYER_FRAME_INTERVAL"},
	{"retry-delay", "MON1STPLAYER_RETRY_DELAY"},
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "mon-1stplayer:", err)
		var ee *exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		os.Exit(exitRuntime)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cfg, err := parseFlags(args, stderr)
	if err != nil {
		return err
	}
	if cfg == nil {
		return nil
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.validate {
		return runDiagnostic(ctx, stdout)
	}
	return runMonitor(ctx, cfg, stderr)
}

func parseFlags(args []string, stderr io.Writer) (*config, error) {
	fs := flag.NewFlagSet("mon-1stplayer", flag.ContinueOnError)
	fs.SetOutput(stderr)
	show := fs.String("show", "temperature", "temperature|utilization|auto")
	unit := fs.String("unit", "c", "c|f")
	autoInterval := fs.Duration("auto-interval", 6*time.Second, "auto mode switch interval")
	frameDelay := fs.Duration("frame-interval", frameInterval, "frame write interval")
	retryWait := fs.Duration("retry-delay", retryDelay, "device retry delay")
	validate := fs.Bool("validate-frame", false, "send fixed frames and close")

	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: mon-1stplayer [flags]")
		fs.PrintDefaults()
		_, _ = fmt.Fprintln(stderr, "\nEach setting resolves as: explicit flag overrides environment, environment overrides default.")
		for _, b := range flagEnvBindings {
			_, _ = fmt.Fprintf(stderr, "  %s sets -%s (default shown above)\n", b.env, b.flag)
		}
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, nil
		}
		return nil, &exitError{exitRefuse, err}
	}
	if fs.NArg() > 0 {
		return nil, &exitError{exitRefuse, fmt.Errorf("unexpected argument %q", fs.Arg(0))}
	}
	effective := map[string]string{}
	for _, b := range flagEnvBindings {
		effective[b.flag] = "-" + b.flag
	}
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	for _, b := range flagEnvBindings {
		if explicit[b.flag] {
			continue
		}
		value, ok := os.LookupEnv(b.env)
		if !ok {
			continue
		}
		if value == "" {
			return nil, &exitError{exitRefuse, fmt.Errorf("%s must not be empty", b.env)}
		}
		if err := fs.Set(b.flag, value); err != nil {
			return nil, &exitError{exitRefuse, fmt.Errorf("%s: %w", b.env, err)}
		}
		effective[b.flag] = b.env
	}

	for _, check := range []struct {
		flag string
		d    time.Duration
	}{
		{"auto-interval", *autoInterval},
		{"frame-interval", *frameDelay},
		{"retry-delay", *retryWait},
	} {
		if check.d <= 0 {
			return nil, &exitError{exitRefuse, fmt.Errorf("%s must be positive", effective[check.flag])}
		}
	}

	tempMode := displayCelsius
	switch *unit {
	case "c":
	case "f":
		tempMode = displayFahrenheit
	default:
		return nil, &exitError{exitRefuse, fmt.Errorf("invalid %s %q", effective["unit"], *unit)}
	}

	cfg := &config{
		tempMode:      tempMode,
		autoInterval:  *autoInterval,
		frameInterval: *frameDelay,
		retryDelay:    *retryWait,
		validate:      *validate,
	}
	switch *show {
	case "temperature":
		cfg.mode = tempMode
	case "utilization":
		cfg.mode = displayUtilization
	case "auto":
		cfg.mode = tempMode
		cfg.auto = true
	default:
		return nil, &exitError{exitRefuse, fmt.Errorf("invalid %s %q", effective["show"], *show)}
	}
	return cfg, nil
}

func runMonitor(ctx context.Context, cfg *config, stderr io.Writer) (runErr error) {
	collector, err := newMetricCollector()
	if err != nil {
		return &exitError{exitRuntime, err}
	}
	defer func() {
		runErr = errors.Join(runErr, collector.close())
	}()

	lg := log.New(stderr, "mon-1stplayer: ", log.LstdFlags)
	ticker := time.NewTicker(cfg.frameInterval)
	defer ticker.Stop()

	var sequence byte
	mode := cfg.mode
	var nextSwitch time.Time
	waitingLogged := false

	for {
		dev, err := openHidrawDevice()
		if err != nil {
			if !errors.Is(err, errDeviceNotFound) {
				return &exitError{exitRefuse, err}
			}
			if !waitingLogged {
				lg.Print("waiting for display")
				waitingLogged = true
			}
			if sleepCtx(ctx, cfg.retryDelay) != nil {
				return nil
			}
			continue
		}
		waitingLogged = false
		if nextSwitch.IsZero() {
			nextSwitch = time.Now().Add(cfg.autoInterval)
		}
		lg.Printf("connected: %s", dev.path)

		writeFailed := false
		canceled := false
	inner:
		for {
			select {
			case <-ctx.Done():
				canceled = true
				break inner
			case now := <-ticker.C:
				if cfg.auto && !now.Before(nextSwitch) {
					if mode == displayUtilization {
						mode = cfg.tempMode
					} else {
						mode = displayUtilization
					}
					nextSwitch = now.Add(cfg.autoInterval)
				}
				sample, ready, serr := collector.sample()
				if serr != nil {
					runErr = &exitError{exitRuntime, errors.Join(stopActive(dev), fmt.Errorf("metrics: %w", serr))}
					return runErr
				}
				if !ready {
					continue
				}
				payload := encodePayload(sequence, sample, mode)
				sequence++
				if werr := dev.writePayload(payload); werr != nil {
					lg.Printf("write failed: %v", werr)
					writeFailed = true
					break inner
				}
			}
		}

		if canceled {
			runErr = errors.Join(runErr, stopActive(dev))
			return runErr
		}
		if writeFailed {
			if cerr := dev.close(); cerr != nil {
				lg.Printf("close failed: %v", cerr)
			}
			if sleepCtx(ctx, cfg.retryDelay) != nil {
				return nil
			}
			continue
		}
	}
}

func stopActive(dev *hidrawDevice) error {
	werr := dev.writePayload(closePayload())
	cerr := dev.close()
	return errors.Join(werr, cerr)
}

func runDiagnostic(ctx context.Context, stdout io.Writer) (diagErr error) {
	dev, err := openHidrawDevice()
	if err != nil {
		return &exitError{exitRefuse, err}
	}

	defer func() {
		diagErr = errors.Join(diagErr, dev.close())
	}()

	sample := metrics{cpuTempC: 41, gpuTempC: 62, cpuUtil: 23, gpuUtil: 84}
	send := func(sequence byte, mode displayMode, label string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "sending %s frame\n", label)
		return dev.writePayload(encodePayload(sequence, sample, mode))
	}
	wait := func() error {
		_, _ = fmt.Fprintln(stdout, "waiting 3s")
		return sleepCtx(ctx, 3*time.Second)
	}

	diagErr = send(0, displayCelsius, "celsius")
	if diagErr == nil {
		diagErr = wait()
	}
	if diagErr == nil {
		diagErr = send(1, displayUtilization, "utilization")
	}
	if diagErr == nil {
		diagErr = wait()
	}
	if diagErr != nil {
		if errors.Is(diagErr, context.Canceled) || errors.Is(diagErr, context.DeadlineExceeded) {
			if errors.Is(diagErr, context.Canceled) {
				_, _ = fmt.Fprintln(stdout, "cancelled; closing")
			}
			closeErr := dev.writePayload(closePayload())
			return diagnosticContextResult(diagErr, closeErr)
		}
		return &exitError{exitRuntime, diagErr}
	}

	_, _ = fmt.Fprintln(stdout, "sending close frame")
	if werr := dev.writePayload(closePayload()); werr != nil {
		return &exitError{exitRuntime, werr}
	}
	_, _ = fmt.Fprintln(stdout, "done")
	return nil
}

func diagnosticContextResult(cause, closeErr error) error {
	if errors.Is(cause, context.Canceled) {
		return closeErr
	}
	return &exitError{exitRuntime, errors.Join(cause, closeErr)}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
