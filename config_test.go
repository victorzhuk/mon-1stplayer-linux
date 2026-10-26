package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, b := range flagEnvBindings {
		t.Setenv(b.env, "")
		if err := os.Unsetenv(b.env); err != nil {
			t.Fatalf("unset %s: %v", b.env, err)
		}
	}
}

func TestParseFlagsEnvPrecedence(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		env   map[string]string
		check func(t *testing.T, cfg *config, err error)
	}{
		{
			name: "built-in defaults",
			check: func(t *testing.T, cfg *config, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if cfg.auto || cfg.mode != displayCelsius || cfg.tempMode != displayCelsius {
					t.Fatalf("mode settings = %+v, want celsius fixed", cfg)
				}
				if cfg.autoInterval != 6*time.Second {
					t.Errorf("autoInterval = %s, want 6s", cfg.autoInterval)
				}
				if cfg.frameInterval != 200*time.Millisecond {
					t.Errorf("frameInterval = %s, want 200ms", cfg.frameInterval)
				}
				if cfg.retryDelay != time.Second {
					t.Errorf("retryDelay = %s, want 1s", cfg.retryDelay)
				}
				if cfg.validate {
					t.Error("validate = true, want false")
				}
			},
		},
		{
			name: "show from environment",
			env:  map[string]string{"MON1STPLAYER_SHOW": "utilization"},
			check: func(t *testing.T, cfg *config, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if cfg.mode != displayUtilization {
					t.Errorf("mode = %v, want utilization", cfg.mode)
				}
			},
		},
		{
			name: "unit from environment",
			env:  map[string]string{"MON1STPLAYER_UNIT": "f"},
			check: func(t *testing.T, cfg *config, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if cfg.tempMode != displayFahrenheit || cfg.mode != displayFahrenheit {
					t.Errorf("tempMode = %v, want fahrenheit", cfg.tempMode)
				}
			},
		},
		{
			name: "auto-interval from environment",
			env:  map[string]string{"MON1STPLAYER_AUTO_INTERVAL": "2s"},
			check: func(t *testing.T, cfg *config, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if cfg.autoInterval != 2*time.Second {
					t.Errorf("autoInterval = %s, want 2s", cfg.autoInterval)
				}
			},
		},
		{
			name: "frame-interval from environment",
			env:  map[string]string{"MON1STPLAYER_FRAME_INTERVAL": "750ms"},
			check: func(t *testing.T, cfg *config, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if cfg.frameInterval != 750*time.Millisecond {
					t.Errorf("frameInterval = %s, want 750ms", cfg.frameInterval)
				}
			},
		},
		{
			name: "retry-delay from environment",
			env:  map[string]string{"MON1STPLAYER_RETRY_DELAY": "3s"},
			check: func(t *testing.T, cfg *config, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if cfg.retryDelay != 3*time.Second {
					t.Errorf("retryDelay = %s, want 3s", cfg.retryDelay)
				}
			},
		},
		{
			name: "cli overrides malformed environment",
			args: []string{"-show", "temperature"},
			env:  map[string]string{"MON1STPLAYER_SHOW": "bogus"},
			check: func(t *testing.T, cfg *config, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if cfg.mode != displayCelsius {
					t.Errorf("mode = %v, want temperature from CLI", cfg.mode)
				}
			},
		},
		{
			name: "invalid show from environment names key",
			env:  map[string]string{"MON1STPLAYER_SHOW": "bogus"},
			check: func(t *testing.T, cfg *config, err error) {
				if err == nil {
					t.Fatal("err = nil, want refusal")
				}
				if !strings.Contains(err.Error(), "MON1STPLAYER_SHOW") {
					t.Errorf("error %q does not name MON1STPLAYER_SHOW", err)
				}
			},
		},
		{
			name: "invalid show from cli names flag",
			args: []string{"-show", "bogus"},
			check: func(t *testing.T, cfg *config, err error) {
				if err == nil {
					t.Fatal("err = nil, want refusal")
				}
				if !strings.Contains(err.Error(), "-show") {
					t.Errorf("error %q does not name -show", err)
				}
			},
		},
		{
			name: "invalid unit from environment names key",
			env:  map[string]string{"MON1STPLAYER_UNIT": "kelvin"},
			check: func(t *testing.T, cfg *config, err error) {
				if err == nil {
					t.Fatal("err = nil, want refusal")
				}
				if !strings.Contains(err.Error(), "MON1STPLAYER_UNIT") {
					t.Errorf("error %q does not name MON1STPLAYER_UNIT", err)
				}
			},
		},
		{
			name: "inactive setting still validated",
			args: []string{"-show", "utilization"},
			env:  map[string]string{"MON1STPLAYER_FRAME_INTERVAL": "0s"},
			check: func(t *testing.T, cfg *config, err error) {
				if err == nil {
					t.Fatal("err = nil, want refusal")
				}
				if !strings.Contains(err.Error(), "MON1STPLAYER_FRAME_INTERVAL") {
					t.Errorf("error %q does not name MON1STPLAYER_FRAME_INTERVAL", err)
				}
			},
		},
		{
			name: "present empty value refuses",
			env:  map[string]string{"MON1STPLAYER_UNIT": ""},
			check: func(t *testing.T, cfg *config, err error) {
				if err == nil {
					t.Fatal("err = nil, want refusal")
				}
				if !strings.Contains(err.Error(), "MON1STPLAYER_UNIT") {
					t.Errorf("error %q does not name MON1STPLAYER_UNIT", err)
				}
			},
		},
		{
			name: "malformed duration from environment",
			env:  map[string]string{"MON1STPLAYER_RETRY_DELAY": "soon"},
			check: func(t *testing.T, cfg *config, err error) {
				if err == nil {
					t.Fatal("err = nil, want refusal")
				}
				if !strings.Contains(err.Error(), "MON1STPLAYER_RETRY_DELAY") {
					t.Errorf("error %q does not name MON1STPLAYER_RETRY_DELAY", err)
				}
			},
		},
		{
			name: "nonpositive cli duration names flag",
			args: []string{"-auto-interval", "0"},
			check: func(t *testing.T, cfg *config, err error) {
				if err == nil {
					t.Fatal("err = nil, want refusal")
				}
				if !strings.Contains(err.Error(), "-auto-interval") {
					t.Errorf("error %q does not name -auto-interval", err)
				}
			},
		},
		{
			name: "negative retry-delay from environment",
			env:  map[string]string{"MON1STPLAYER_RETRY_DELAY": "-1s"},
			check: func(t *testing.T, cfg *config, err error) {
				if err == nil {
					t.Fatal("err = nil, want refusal")
				}
				if !strings.Contains(err.Error(), "MON1STPLAYER_RETRY_DELAY") {
					t.Errorf("error %q does not name MON1STPLAYER_RETRY_DELAY", err)
				}
			},
		},
		{
			name: "positional argument refuses",
			args: []string{"extra"},
			check: func(t *testing.T, cfg *config, err error) {
				if err == nil {
					t.Fatal("err = nil, want refusal")
				}
			},
		},
		{
			name: "unknown flag refuses",
			args: []string{"-bogus"},
			check: func(t *testing.T, cfg *config, err error) {
				if err == nil {
					t.Fatal("err = nil, want refusal")
				}
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			var stderr bytes.Buffer
			cfg, err := parseFlags(tt.args, &stderr)
			tt.check(t, cfg, err)
		})
	}
}

func TestParseFlagsHelpBeforeEnvironment(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("MON1STPLAYER_SHOW", "bogus")
	t.Setenv("MON1STPLAYER_FRAME_INTERVAL", "bogus")
	var stderr bytes.Buffer
	cfg, err := parseFlags([]string{"-h"}, &stderr)
	if err != nil {
		t.Fatalf("err = %v, want nil help result", err)
	}
	if cfg != nil {
		t.Fatalf("cfg = %+v, want nil on help", cfg)
	}
}
