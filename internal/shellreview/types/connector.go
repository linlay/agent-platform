package types

// ConnectorInvocation identifies the verified child that may receive connector credentials.
type ConnectorInvocation struct {
	ID, Program string
	Args        []string
}

// ConnectorCall is one plain call of a mounted connector's CLI. Prefix holds
// the words that start the entry as written (the program, or an interpreter
// and its script); Argv holds the arguments after it.
type ConnectorCall struct {
	ConnectorID string
	Prefix      []string
	Argv        []string
}
