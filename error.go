package ctags

type ParseError struct {
	Message string
	File    string
	Fatal   bool
}

func (e *ParseError) Error() string {
	return e.Message
}

func NewFatalError(msg string, file string) *ParseError {
	return &ParseError{
		Message: msg, File: file,
		Fatal: true,
	}
}

func NewError(msg string, file string) *ParseError {
	return &ParseError{Message: msg, File: file,
		Fatal: false,
	}
}
