package apperrors

import "errors"

type Error struct {
	code        Code
	message     string
	cause       error
	category    Category
	scope       Scope
	status      int
	retryable   bool
	hasCategory bool
	hasScope    bool
	hasStatus   bool
	hasRetry    bool
	diagnostics map[string]any
}

type Option func(*options)

type options struct {
	category    Category
	scope       Scope
	status      int
	retryable   bool
	hasCategory bool
	hasScope    bool
	hasStatus   bool
	hasRetry    bool
	diagnostics map[string]any
}

func WithCategory(category Category) Option {
	return func(opts *options) {
		opts.category = category
		opts.hasCategory = true
	}
}

func WithScope(scope Scope) Option {
	return func(opts *options) {
		opts.scope = scope
		opts.hasScope = true
	}
}

func WithStatus(status int) Option {
	return func(opts *options) {
		opts.status = status
		opts.hasStatus = true
	}
}

func WithRetryable(retryable bool) Option {
	return func(opts *options) {
		opts.retryable = retryable
		opts.hasRetry = true
	}
}

func WithDiagnostics(diagnostics map[string]any) Option {
	return func(opts *options) {
		if len(diagnostics) == 0 {
			return
		}
		opts.diagnostics = cloneMap(diagnostics)
	}
}

func WithDiagnostic(key string, value any) Option {
	return func(opts *options) {
		if key == "" || value == nil {
			return
		}
		if opts.diagnostics == nil {
			opts.diagnostics = map[string]any{}
		}
		opts.diagnostics[key] = value
	}
}

func New(code Code, message string, opts ...Option) *Error {
	applied := applyOptions(opts)
	return &Error{
		code:        code,
		message:     message,
		category:    applied.category,
		scope:       applied.scope,
		status:      applied.status,
		retryable:   applied.retryable,
		hasCategory: applied.hasCategory,
		hasScope:    applied.hasScope,
		hasStatus:   applied.hasStatus,
		hasRetry:    applied.hasRetry,
		diagnostics: cloneMap(applied.diagnostics),
	}
}

func Wrap(code Code, err error, opts ...Option) error {
	if err == nil {
		return New(code, "", opts...)
	}
	appErr := New(code, err.Error(), opts...)
	appErr.cause = err
	return appErr
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.message != "" {
		return e.message
	}
	if e.cause != nil {
		return e.cause.Error()
	}
	return string(e.code)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *Error) Code() Code {
	if e == nil {
		return ""
	}
	return e.code
}

func (e *Error) Payload() map[string]any {
	if e == nil {
		return nil
	}
	opts := []Option{
		WithDiagnostics(e.diagnostics),
	}
	if e.hasCategory {
		opts = append(opts, WithCategory(e.category))
	}
	if e.hasScope {
		opts = append(opts, WithScope(e.scope))
	}
	if e.hasStatus {
		opts = append(opts, WithStatus(e.status))
	}
	if e.hasRetry {
		opts = append(opts, WithRetryable(e.retryable))
	}
	return Payload(e.code, e.Error(), opts...)
}

func Payload(code Code, message string, opts ...Option) map[string]any {
	applied := applyOptions(opts)
	definition, known := Lookup(code)
	if !known {
		definition = Definition{
			Code:               code,
			Category:           CategorySystem,
			Scope:              ScopeSystem,
			HTTPStatus:         500,
			Retryable:          false,
			UserSafeMessageKey: string(code),
		}
	}
	category := definition.Category
	if applied.hasCategory {
		category = applied.category
	}
	scope := definition.Scope
	if applied.hasScope {
		scope = applied.scope
	}
	status := definition.HTTPStatus
	if applied.hasStatus {
		status = applied.status
	}
	retryable := definition.Retryable
	if applied.hasRetry {
		retryable = applied.retryable
	}
	if message == "" {
		message = string(code)
	}
	payload := map[string]any{
		"category":           string(category),
		"code":               string(code),
		"message":            message,
		"retryable":          retryable,
		"scope":              string(scope),
		"status":             status,
		"userSafeMessageKey": definition.UserSafeMessageKey,
	}
	if len(applied.diagnostics) > 0 {
		payload["diagnostics"] = cloneMap(applied.diagnostics)
	}
	return payload
}

func FromCode(code Code, message string, opts ...Option) map[string]any {
	return Payload(code, message, opts...)
}

func FromError(err error, fallback Code, opts ...Option) map[string]any {
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr.Payload()
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	return Payload(fallback, message, opts...)
}

func applyOptions(opts []Option) options {
	var applied options
	for _, opt := range opts {
		if opt != nil {
			opt(&applied)
		}
	}
	return applied
}

func cloneMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
