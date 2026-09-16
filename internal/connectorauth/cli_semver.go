package connectorauth

import (
	"regexp"
	"strings"
)

var cliSemVer = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z.-]+))?(?:\+([0-9A-Za-z.-]+))?$`)

func validCLISemVer(value string) bool {
	m := cliSemVer.FindStringSubmatch(value)
	if m == nil {
		return false
	}
	for i := 4; i <= 5; i++ {
		if m[i] == "" {
			continue
		}
		for _, part := range strings.Split(m[i], ".") {
			if part == "" {
				return false
			}
			if i == 4 && digits(part) && len(part) > 1 && part[0] == '0' {
				return false
			}
		}
	}
	return true
}
func digits(s string) bool { return s != "" && strings.Trim(s, "0123456789") == "" }
func numberCompare(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}
func versionAtLeast(output, minimum string) bool {
	a := strings.TrimPrefix(strings.TrimSpace(output), "v")
	b := strings.TrimPrefix(strings.TrimSpace(minimum), "v")
	if !validCLISemVer(a) || !validCLISemVer(b) {
		return false
	}
	aa, bb := cliSemVer.FindStringSubmatch(a), cliSemVer.FindStringSubmatch(b)
	for i := 1; i <= 3; i++ {
		if c := numberCompare(aa[i], bb[i]); c != 0 {
			return c > 0
		}
	}
	if aa[4] == "" {
		return true
	}
	if bb[4] == "" {
		return false
	}
	ap, bp := strings.Split(aa[4], "."), strings.Split(bb[4], ".")
	for i := 0; i < len(ap) && i < len(bp); i++ {
		if ap[i] == bp[i] {
			continue
		}
		an, bn := digits(ap[i]), digits(bp[i])
		if an && bn {
			return numberCompare(ap[i], bp[i]) > 0
		}
		if an != bn {
			return !an
		}
		return strings.Compare(ap[i], bp[i]) > 0
	}
	return len(ap) >= len(bp)
}
