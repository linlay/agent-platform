package hitl

type Checker interface {
	Check(command string, chatLevel int) InterceptResult
}
