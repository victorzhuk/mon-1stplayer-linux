package main

type displayMode uint8

const (
	displayCelsius displayMode = iota
	displayFahrenheit
	displayUtilization
)

type metrics struct {
	cpuTempC int
	gpuTempC int
	cpuUtil  int
	gpuUtil  int
}

// Segment tables per digit 0..9: bit i set means live-block index 2*i receives
// the group mask. Rows are hundreds, tens, units.
var (
	cpuDigitSeg = [3][10]uint8{
		{0x77, 0x42, 0x3b, 0x6b, 0x4e, 0x6d, 0x7d, 0x43, 0x7f, 0x6f},
		{0x6f, 0x06, 0x5d, 0x5e, 0x36, 0x7a, 0x7b, 0x46, 0x7f, 0x7e},
		{0x77, 0x24, 0x7a, 0x6e, 0x2d, 0x4f, 0x5f, 0x26, 0x7f, 0x6f},
	}
	gpuDigitSeg = [3][10]uint8{
		{0x7d, 0x09, 0x37, 0x3b, 0x4b, 0x7a, 0x7e, 0x29, 0x7f, 0x7b},
		{0x77, 0x42, 0x3e, 0x5e, 0x4b, 0x5d, 0x7d, 0x46, 0x7f, 0x5f},
		{0x77, 0x22, 0x3d, 0x3e, 0x6a, 0x5e, 0x5f, 0x32, 0x7f, 0x7e},
	}
)

type barSegment struct {
	index uint8
	mask  uint8
}

// Bar segments in level order 1..10.
var (
	cpuBarSeg = [10]barSegment{
		{6, 0x40},
		{8, 0x40},
		{10, 0x40},
		{12, 0x40},
		{1, 0x01},
		{5, 0x01},
		{3, 0x01},
		{2, 0x40},
		{4, 0x40},
		{0, 0x40},
	}
	gpuBarSeg = [10]barSegment{
		{9, 0x01},
		{11, 0x01},
		{0, 0x80},
		{2, 0x80},
		{4, 0x80},
		{7, 0x01},
		{12, 0x80},
		{10, 0x80},
		{8, 0x80},
		{6, 0x80},
	}
)

func drawNumber(live []byte, value int, seg [3][10]uint8, masks [3]uint8) {
	if value < 0 {
		return
	}
	if value > 999 {
		value = 999
	}
	for pos, threshold := range [3]int{100, 10, 0} {
		if value < threshold {
			continue
		}
		digit := value
		switch pos {
		case 0:
			digit /= 100
		case 1:
			digit = digit / 10 % 10
		default:
			digit %= 10
		}
		segments := seg[pos][digit]
		for i := uint(0); i < 7; i++ {
			if segments&(1<<i) != 0 {
				live[2*i] |= masks[pos]
			}
		}
	}
}

func drawBar(live []byte, value int, seg [10]barSegment) {
	if value <= 0 {
		return
	}
	levels := 10
	if value <= 90 {
		levels = (value + 9) / 10
	}
	for _, s := range seg[:levels] {
		live[s.index] |= s.mask
	}
}

func fahrenheit(c int) int {
	tenths := 18*c + 320
	f := tenths / 10
	if tenths%10 < 0 {
		f--
	}
	return f
}

func encodePayload(sequence byte, sample metrics, mode displayMode) [64]byte {
	var payload [64]byte
	payload[0] = sequence
	payload[2] = 0x02
	fixed := [...]byte{
		0xfd, 0x08, 0x08, 0x08, 0xfd, 0x08, 0xdd, 0x0f,
		0xd0, 0xdf, 0x8f, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
	copy(payload[24:40], fixed[:])

	live := payload[40:56]

	var cpuVal, gpuVal, cpuBar, gpuBar int
	switch mode {
	case displayCelsius:
		cpuVal, gpuVal = sample.cpuTempC, sample.gpuTempC
		cpuBar, gpuBar = cpuVal, gpuVal
	case displayFahrenheit:
		cpuVal, gpuVal = fahrenheit(sample.cpuTempC), fahrenheit(sample.gpuTempC)
		cpuBar, gpuBar = sample.cpuTempC, sample.gpuTempC
	case displayUtilization:
		cpuVal, gpuVal = sample.cpuUtil, sample.gpuUtil
		cpuBar, gpuBar = cpuVal, gpuVal
	}

	live[1] |= 0x04
	live[11] |= 0x04
	switch mode {
	case displayCelsius:
		live[5] |= 0x02
		live[7] |= 0x02
	case displayFahrenheit:
		live[3] |= 0x02
		live[9] |= 0x02
	case displayUtilization:
		live[1] |= 0x02
		live[3] |= 0x04
		live[5] |= 0x04
		live[11] |= 0x02
		live[7] |= 0x04
		live[9] |= 0x04
	}

	drawNumber(live, cpuVal, cpuDigitSeg, [3]uint8{0x01, 0x02, 0x04})
	drawNumber(live, gpuVal, gpuDigitSeg, [3]uint8{0x08, 0x10, 0x20})
	drawBar(live, cpuBar, cpuBarSeg)
	drawBar(live, gpuBar, gpuBarSeg)

	return payload
}

func closePayload() [64]byte {
	var payload [64]byte
	payload[1] = 0x0f
	return payload
}
