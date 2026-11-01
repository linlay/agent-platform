package types

type FileAccess struct {
	Path      string
	Write     bool
	Recursive bool
	// Ref points into the analyzer's argument vector (excluding argv[0]).
	Ref  ValueRef
	Kind OpKind
	Role Role
	// Derived paths cannot acquire literal device provenance.
	Derived               bool
	Placement             Placement
	Child                 string
	RelativeToDestination bool
	Group                 int
}

// GitCheck names the repository state an analyzed Git invocation depends on.
// The caller must verify it before treating the invocation as non-executing.
type GitCheck string

const (
	GitCheckNone GitCheck = ""
	// GitCheckRead requires that effective configuration defines no program
	// that read commands run implicitly (fsmonitor, diff drivers, filters, gpg).
	GitCheckRead GitCheck = "read"
	// GitCheckWrite additionally requires that no repository hook is active.
	GitCheckWrite GitCheck = "write"
	// GitCheckNetwork additionally requires standard credential helpers and no
	// custom SSH or askpass programs.
	GitCheckNetwork GitCheck = "network"
)

// ValueRef records a decoded byte range without inferring origin from values.
type ValueRef struct {
	Index, Start, End int
	Known             bool
}

func ArgumentRef(index int, value string) ValueRef {
	return ValueRef{Index: index, End: len(value), Known: true}
}

type OpKind string

const (
	Read           OpKind = "read"
	Output         OpKind = "output"
	Create         OpKind = "create"
	Delete         OpKind = "delete"
	Rename         OpKind = "rename"
	Chmod          OpKind = "chmod"
	Link           OpKind = "link"
	Exec           OpKind = "exec"
	RemoteMutation OpKind = "remote_mutation"
	RepoWrite      OpKind = "repo_write"
	Discard        OpKind = "discard"
	// UnknownWrite preserves a conservative write requirement without claiming
	// that an unmodeled operation opens a byte output stream.
	UnknownWrite OpKind = "unknown_write"
)

type Origin string

const (
	Argument      Origin = "argument"
	ShellRedirect Origin = "shell_redirect"
	Implicit      Origin = "implicit"
)

type Role string

const (
	Source      Role = "source"
	Destination Role = "destination"
	Replaced    Role = "replaced"
	LinkTarget  Role = "link_target"
)

type Placement string

const (
	Exact           Placement = ""
	IntoDirectory   Placement = "into_directory"
	FileOrDirectory Placement = "file_or_directory"
	ParentDirectory Placement = "parent_directory"
)

type Target struct {
	Path                  string
	Role                  Role
	Word                  SourceWord
	Placement             Placement
	Child                 string
	RelativeToDestination bool
}
type Op struct {
	Kind      OpKind
	Targets   []Target
	Recursive bool
	Arg       int
	Rule      string
	Origin    Origin
}

func (o Op) ReadsTarget(t Target) bool {
	return o.Kind == Read || (o.Kind == Create || o.Kind == Rename) && t.Role == Source || o.Kind == Link && t.Role == LinkTarget
}

func (o Op) WritesTarget(t Target) bool {
	if o.Kind == Create && t.Role == Source {
		return false
	}
	return o.Writes()
}

func (o Op) Writes() bool {
	return o.Kind != Read && o.Kind != Exec && o.Kind != RemoteMutation && o.Kind != Discard && o.Kind != RepoWrite
}

type UnknownKind string

const (
	UnknownSyntax   UnknownKind = "syntax"
	UnknownWrapper  UnknownKind = "wrapper"
	UnknownOption   UnknownKind = "option"
	UnknownBehavior UnknownKind = "behavior"
	UnknownDialect  UnknownKind = "dialect"
	UnknownPath     UnknownKind = "path"
)

type Unknown struct {
	Kind UnknownKind
	Arg  int
	Rule string
}
type DepKind string

const (
	GitConfig   DepKind = "git_config"
	ConfigFile  DepKind = "config_file"
	Script      DepKind = "script"
	Environment DepKind = "environment"
)

type Dep struct {
	Kind     DepKind
	Check    string
	Path     string
	Variable string
}

// Effects contains facts only. Identity and policy decisions belong to callers.
type Effects struct {
	Ops      []Op
	Unknowns []Unknown
	Deps     []Dep
	SSHAgent bool
}

func (e Effects) Has(kind OpKind) bool {
	for _, o := range e.Ops {
		if o.Kind == kind {
			return true
		}
	}
	return false
}
func (e Effects) Unknown(kind UnknownKind) bool {
	for _, u := range e.Unknowns {
		if u.Kind == kind {
			return true
		}
	}
	return false
}
