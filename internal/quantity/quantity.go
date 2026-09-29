// Package quantity parses Kubernetes resource quantities ("500m", "1.5",
// "256Mi", "1G", "2e3") into integers: millicores for CPU and bytes for memory.
package quantity

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

var suffixes = []struct {
	s string
	m float64
}{
	{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40}, {"Pi", 1 << 50}, {"Ei", 1 << 60},
	{"n", 1e-9}, {"u", 1e-6}, {"m", 1e-3},
	{"k", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12}, {"P", 1e15}, {"E", 1e18},
}

// Parse returns the value of a quantity as a float in base units.
func Parse(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty quantity")
	}
	mult := 1.0
	num := s
	for _, sf := range suffixes {
		if strings.HasSuffix(s, sf.s) {
			// "1e3" style exponent must not be confused with the "E" suffix.
			if sf.s == "E" && strings.ContainsAny(s[:len(s)-1], "eE") {
				continue
			}
			num, mult = strings.TrimSuffix(s, sf.s), sf.m
			break
		}
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || v < 0 || math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, fmt.Errorf("invalid quantity %q", s)
	}
	return v * mult, nil
}

// MilliCPU parses a CPU quantity into millicores.
func MilliCPU(s string) (int64, error) {
	v, err := Parse(s)
	if err != nil {
		return 0, err
	}
	return int64(math.Ceil(v * 1000)), nil
}

// Bytes parses a memory quantity into bytes.
func Bytes(s string) (int64, error) {
	v, err := Parse(s)
	if err != nil {
		return 0, err
	}
	return int64(math.Ceil(v)), nil
}

// FormatCPU renders millicores the way humans write them ("250m", "2").
func FormatCPU(m int64) string {
	if m%1000 == 0 {
		return strconv.FormatInt(m/1000, 10)
	}
	return strconv.FormatInt(m, 10) + "m"
}

// FormatBytes renders bytes using binary suffixes ("512Mi", "1.5Gi").
func FormatBytes(b int64) string {
	units := []string{"Ei", "Pi", "Ti", "Gi", "Mi", "Ki"}
	for i, u := range units {
		size := int64(1) << (10 * (6 - i))
		if b >= size {
			v := float64(b) / float64(size)
			if v == math.Trunc(v) {
				return fmt.Sprintf("%d%s", int64(v), u)
			}
			return strconv.FormatFloat(v, 'f', 1, 64) + u
		}
	}
	return strconv.FormatInt(b, 10)
}
