package types

// OptionRole describes the local effect of an option value.
type OptionRole int

const (
	OptionNone          OptionRole = iota // flag without a value
	OptionValue                           // value is not a local resource
	OptionRead                            // value is a file that is read
	OptionWrite                           // value is a file that is written
	OptionReadDir                         // value is a directory that is read
	OptionWriteDir                        // value is a directory that is written
	OptionExec                            // value is a program or command to run
	OptionRemote                          // flag causes a remote mutation
	OptionRemoteData                      // value is sent to a remote endpoint (may be @file)
	OptionTwoValues                       // option consumes two non-resource values
	OptionNameFile                        // option consumes a name and a file that is read
	OptionRecursive                       // flag makes operands recursive
	OptionOptionalValue                   // --long[=value]; never consumes the next argument
)

// OptionSpec is the grammar used to separate options, their values and file
// operands. It never decides permissions: it only reports local effects.
type OptionSpec struct {
	ShortFlags string                // letters without values
	ShortArgs  map[byte]OptionRole   // letters taking a value
	Long       map[string]OptionRole // long options by name, without leading --
	// NumericShort accepts -NUM forms (head -20, tail -200).
	NumericShort bool
}

type ParsedOptions struct {
	Positional     []string
	PositionalRefs []ValueRef
	Files          []FileAccess
	Incomplete     bool
	Exec           bool
	Remote         bool
	Recursive      bool
	Flags          map[string]bool
	Values         map[string][]string
	ValueRefs      map[string][]ValueRef
}

func (p *ParsedOptions) Has(name string) bool { return p.Flags[name] }
