package ctags

// ParseError is a custom error type that represents ctags parsing errors.
// It distinguishes between fatal and non-fatal errors, which clients may
// want to handle differently.
type ParseError struct {
	// The error message.
	Message string

	// Whether the error is fatal. This corresponds to the 'fatal' flag in ctags error responses.
	Fatal bool

	// An optional inner error.
	Inner error
}

func (e *ParseError) Error() string {
	return e.Message
}

func (p *ParseError) Unwrap() error {
	return p.Inner
}

func newFatalParseError(msg string) *ParseError {
	return &ParseError{Message: msg, Fatal: true}
}

func newParseError(msg string) *ParseError {
	return &ParseError{Message: msg, Fatal: false}
}
