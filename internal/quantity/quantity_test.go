package quantity

import "testing"

func TestParse(t *testing.T) {
	cpu := map[string]int64{"500m": 500, "1": 1000, "1.5": 1500, "0.1": 100, "2000m": 2000, "100u": 1}
	for in, want := range cpu {
		if got, err := MilliCPU(in); err != nil || got != want {
			t.Errorf("MilliCPU(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	mem := map[string]int64{"128Mi": 128 << 20, "1Gi": 1 << 30, "1G": 1e9, "1e3": 1000, "512": 512, "1.5Gi": 3 << 29, "2E": 2e18}
	for in, want := range mem {
		if got, err := Bytes(in); err != nil || got != want {
			t.Errorf("Bytes(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "-1", "1Zi"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
	if FormatCPU(1500) != "1500m" || FormatCPU(2000) != "2" || FormatBytes(512<<20) != "512Mi" || FormatBytes(3<<29) != "1.5Gi" {
		t.Error("format mismatch")
	}
}
