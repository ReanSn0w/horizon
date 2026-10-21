package distribution

import "testing"

func TestVersionOrdering(t *testing.T) {
	ordered := []string{"v0.1.0-alpha", "v0.1.0-alpha.2", "v0.1.0-alpha.10", "v0.1.0-beta", "v0.1.0", "v0.2.0", "v1.0.0", "v100000000000000000000000.0.0"}
	for i, a := range ordered {
		for j, b := range ordered {
			c, err := CompareVersions(a, b)
			if err != nil || (i == j && c != 0) || (i < j && c >= 0) || (i > j && c <= 0) {
				t.Fatalf("%s %s: %d %v", a, b, c, err)
			}
		}
	}
	for _, tag := range []string{"dev", "v01.2.3", "v1.2.3-01", "v1.2.3/other"} {
		if ValidTag(tag) {
			t.Fatalf("accepted %s", tag)
		}
	}
}
