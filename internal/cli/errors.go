package cli

import (
	"errors"
	"fmt"

	"github.com/theworkflowco/pp-substack/internal/substack"
)

type codedError struct {
	code  int
	cause error
}

func (err *codedError) Error() string {
	return err.cause.Error()
}

func (err *codedError) Unwrap() error {
	return err.cause
}

func usageError(message string) error {
	return &codedError{code: 2, cause: errors.New(message)}
}

func authError(message string) error {
	return &codedError{code: 3, cause: errors.New(message)}
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var coded *codedError
	if errors.As(err, &coded) {
		return coded.code
	}
	var httpErr *substack.HTTPError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.StatusCode == 401 || httpErr.StatusCode == 403:
			return 3
		case httpErr.StatusCode == 404:
			return 4
		case httpErr.StatusCode == 429 || httpErr.StatusCode >= 500:
			return 5
		}
	}
	return 7
}

func requiredFlag(name string) error {
	return usageError(fmt.Sprintf("--%s is required", name))
}
