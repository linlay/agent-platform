package connectorauth

import (
	"fmt"
	"time"

	"github.com/dlclark/regexp2/v2"
)

// CLI declaration patterns use ECMAScript semantics, including lookaround and
// backreferences. Bound matching prevents a package's pattern from blocking a
// management request on adversarial or unexpectedly large command output.
func cliPattern(pattern string) (*regexp2.Regexp, error) {
	re, err := regexp2.Compile(pattern, regexp2.ECMAScript)
	if err != nil {
		return nil, fmt.Errorf("invalid CLI pattern")
	}
	re.MatchTimeout = 250 * time.Millisecond
	return re, nil
}
