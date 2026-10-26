package main

import (
	"math"
	"testing"
)

func TestParseProcStat(t *testing.T) {
	t.Run("valid line excludes guest and subtracts idle plus iowait", func(t *testing.T) {
		data := []byte("cpu 100 1 10 1000 5 2 1 1 500 300\ncpu0 1 1 1 1 1 1 1 1 0 0\n")
		busy, total, err := parseProcStat(data)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if total != 1120 {
			t.Errorf("total = %d, want 1120 (guest fields excluded)", total)
		}
		if busy != 115 {
			t.Errorf("busy = %d, want 115 (idle 1000 + iowait 5 subtracted)", busy)
		}
	})

	t.Run("short line returns error", func(t *testing.T) {
		if _, _, err := parseProcStat([]byte("cpu 1 2 3\n")); err == nil {
			t.Errorf("err = nil, want error for short first line")
		}
	})

	t.Run("empty input returns error", func(t *testing.T) {
		if _, _, err := parseProcStat([]byte("")); err == nil {
			t.Errorf("err = nil, want error for empty input")
		}
	})

	t.Run("wrong prefix returns error", func(t *testing.T) {
		if _, _, err := parseProcStat([]byte("mem 1 2 3 4 5 6 7 8 9\n")); err == nil {
			t.Errorf("err = nil, want error for missing cpu prefix")
		}
	})

	t.Run("invalid integer returns error", func(t *testing.T) {
		if _, _, err := parseProcStat([]byte("cpu 1 x 3 4 5 6 7 8 9\n")); err == nil {
			t.Errorf("err = nil, want error for invalid integer field")
		}
	})
}

func TestCPUUtilization(t *testing.T) {
	cases := []struct {
		name                string
		prevBusy, prevTotal uint64
		busy, total         uint64
		wantPct             int
		wantReady           bool
	}{
		{"normal delta", 100, 200, 150, 300, 50, true},
		{"all idle delta", 0, 100, 0, 200, 0, true},
		{"floor rounding", 0, 0, 1, 3, 33, true},
		{"zero total delta not ready", 50, 100, 50, 100, 0, false},
		{"decreasing busy not ready", 100, 200, 90, 300, 0, false},
		{"decreasing total not ready", 50, 100, 60, 90, 0, false},
		{"busy delta above total delta not ready", 10, 100, 60, 140, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pct, ready := cpuUtilization(tc.prevBusy, tc.prevTotal, tc.busy, tc.total)
			if ready != tc.wantReady {
				t.Errorf("ready = %v, want %v", ready, tc.wantReady)
			}
			if ready && pct != tc.wantPct {
				t.Errorf("pct = %d, want %d", pct, tc.wantPct)
			}
		})
	}
}

func TestCelsiusFromMilli(t *testing.T) {
	cases := []struct {
		milli int64
		want  int
	}{
		{41999, 41},
		{41000, 41},
		{0, 0},
		{-1, -1},
		{-999, -1},
		{-1000, -1},
		{-1001, -2},
		{math.MinInt64, -9223372036854776},
		{math.MinInt64 + 999, -9223372036854775},
		{math.MaxInt64, 9223372036854775},
	}
	for _, tc := range cases {
		if got := celsiusFromMilli(tc.milli); got != tc.want {
			t.Errorf("celsiusFromMilli(%d) = %d, want %d", tc.milli, got, tc.want)
		}
	}
}
