package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

type metricCollector struct {
	device    nvml.Device
	cpuTempIn string
	lastBusy  uint64
	lastTotal uint64
	shutdown  bool
}

func newMetricCollector() (*metricCollector, error) {
	if ret := nvml.Init(); ret != nvml.SUCCESS {
		return nil, fmt.Errorf("nvml init: %w", ret)
	}

	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return nil, errors.Join(fmt.Errorf("nvml device count: %w", ret), shutdownErr())
	}
	if count != 1 {
		return nil, errors.Join(fmt.Errorf("expected exactly 1 nvml device, found %d", count), shutdownErr())
	}

	device, ret := nvml.DeviceGetHandleByIndex(0)
	if ret != nvml.SUCCESS {
		return nil, errors.Join(fmt.Errorf("nvml device handle: %w", ret), shutdownErr())
	}

	input, err := findCpuTempInput()
	if err != nil {
		return nil, errors.Join(err, shutdownErr())
	}

	busy, total, err := readProcStat()
	if err != nil {
		return nil, errors.Join(err, shutdownErr())
	}

	return &metricCollector{device: device, cpuTempIn: input, lastBusy: busy, lastTotal: total}, nil
}

func shutdownErr() error {
	if ret := nvml.Shutdown(); ret != nvml.SUCCESS {
		return fmt.Errorf("nvml shutdown: %w", ret)
	}
	return nil
}

func (c *metricCollector) sample() (metrics, bool, error) {
	cpuTemp, err := readCpuTemp(c.cpuTempIn)
	if err != nil {
		return metrics{}, false, err
	}

	gpuTemp, ret := c.device.GetTemperature(nvml.TEMPERATURE_GPU)
	if ret != nvml.SUCCESS {
		return metrics{}, false, fmt.Errorf("gpu temperature: %w", ret)
	}

	util, ret := c.device.GetUtilizationRates()
	if ret != nvml.SUCCESS {
		return metrics{}, false, fmt.Errorf("gpu utilization: %w", ret)
	}

	busy, total, err := readProcStat()
	if err != nil {
		return metrics{}, false, err
	}
	utilC, ok := cpuUtilization(c.lastBusy, c.lastTotal, busy, total)
	c.lastBusy, c.lastTotal = busy, total

	m := metrics{
		cpuTempC: cpuTemp,
		gpuTempC: int(gpuTemp),
		cpuUtil:  utilC,
		gpuUtil:  int(util.Gpu),
	}
	return m, ok, nil
}

func cpuUtilization(previousBusy, previousTotal, busy, total uint64) (int, bool) {
	if busy < previousBusy || total < previousTotal {
		return 0, false
	}
	busyDelta := busy - previousBusy
	totalDelta := total - previousTotal
	if totalDelta == 0 || busyDelta > totalDelta {
		return 0, false
	}
	return int(busyDelta * 100 / totalDelta), true
}

func (c *metricCollector) close() error {
	if c.shutdown {
		return nil
	}
	c.shutdown = true
	if ret := nvml.Shutdown(); ret != nvml.SUCCESS {
		return fmt.Errorf("nvml shutdown: %w", ret)
	}
	return nil
}

func readProcStat() (busy, total uint64, err error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, fmt.Errorf("read /proc/stat: %w", err)
	}
	return parseProcStat(data)
}

func parseProcStat(data []byte) (busy, total uint64, err error) {
	line, _, _ := strings.Cut(string(data), "\n")
	fields := strings.Fields(line)
	if len(fields) < 9 || fields[0] != "cpu" {
		return 0, 0, errors.New("malformed /proc/stat first line")
	}
	var idle uint64
	for i, f := range fields[1:9] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("parse /proc/stat field %d: %w", i+1, err)
		}
		total += v
		if i == 3 || i == 4 {
			idle += v
		}
	}
	return total - idle, total, nil
}

func findCpuTempInput() (string, error) {
	dirs, err := filepath.Glob("/sys/class/hwmon/hwmon*")
	if err != nil {
		return "", fmt.Errorf("list hwmon: %w", err)
	}
	var found bool
	var input string
	for _, dir := range dirs {
		name, err := readSysFile(filepath.Join(dir, "name"))
		if err != nil || name != "k10temp" {
			continue
		}
		labels, err := filepath.Glob(filepath.Join(dir, "temp*_label"))
		if err != nil {
			return "", fmt.Errorf("list temp labels in %s: %w", dir, err)
		}
		for _, label := range labels {
			v, err := readSysFile(label)
			if err != nil || v != "Tctl" {
				continue
			}
			if found {
				return "", errors.New("ambiguous k10temp Tctl sources")
			}
			found = true
			input = strings.TrimSuffix(label, "_label") + "_input"
		}
	}
	if !found {
		return "", errors.New("k10temp Tctl sensor not found")
	}
	return input, nil
}

func readCpuTemp(input string) (int, error) {
	raw, err := readSysFile(input)
	if err != nil {
		return 0, err
	}
	milli, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", input, err)
	}
	return celsiusFromMilli(milli), nil
}

func celsiusFromMilli(milli int64) int {
	whole := milli / 1000
	rem := milli % 1000
	if rem < 0 {
		whole--
	}
	return int(whole)
}

func readSysFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}
