package distribution

import (
	"fmt"
	"strings"
)

func numericCompare(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}
func numeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// CompareVersions compares the supported SemVer tags without integer overflow.
func CompareVersions(a, b string) (int, error) {
	if !ValidTag(a) || !ValidTag(b) {
		return 0, fmt.Errorf("release tags required")
	}
	av, ap, _ := strings.Cut(strings.TrimPrefix(a, "v"), "-")
	bv, bp, _ := strings.Cut(strings.TrimPrefix(b, "v"), "-")
	aa, bb := strings.Split(av, "."), strings.Split(bv, ".")
	for i := range aa {
		if c := numericCompare(aa[i], bb[i]); c != 0 {
			return c, nil
		}
	}
	if ap == bp {
		return 0, nil
	}
	if ap == "" {
		return 1, nil
	}
	if bp == "" {
		return -1, nil
	}
	aa, bb = strings.Split(ap, "."), strings.Split(bp, ".")
	for i := 0; i < len(aa) && i < len(bb); i++ {
		an, bn := numeric(aa[i]), numeric(bb[i])
		var c int
		switch {
		case an && bn:
			c = numericCompare(aa[i], bb[i])
		case an:
			c = -1
		case bn:
			c = 1
		default:
			c = strings.Compare(aa[i], bb[i])
		}
		if c != 0 {
			return c, nil
		}
	}
	if len(aa) < len(bb) {
		return -1, nil
	}
	return 1, nil
}
func NoDowngrade(current, target string) error {
	c, err := CompareVersions(target, current)
	if err != nil {
		return err
	}
	if c < 0 {
		return fmt.Errorf("downgrade from %s to %s is not supported; user data may be incompatible", current, target)
	}
	return nil
}
