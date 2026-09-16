package connectorauth

import (
	"github.com/dlclark/regexp2/v2"
	"time"
)

func compileCLIRegexp(pattern string) (*regexp2.Regexp, error) {
	re, err := regexp2.Compile(pattern, regexp2.ECMAScript)
	if err != nil {
		return nil, err
	}
	re.MatchTimeout = 100 * time.Millisecond
	return re, nil
}
