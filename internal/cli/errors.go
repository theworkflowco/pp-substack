package cli

import (
	"encoding/json"
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

func ErrorOutput(err error) ([]byte, bool) {
	var updateErr *substack.UpdateError
	if !errors.As(err, &updateErr) {
		return nil, false
	}
	envelope := struct {
		SchemaVersion      string `json:"schema_version"`
		Stage              string `json:"stage"`
		Code               string `json:"code"`
		MutationDispatched bool   `json:"mutation_dispatched"`
		Message            string `json:"message"`
	}{
		SchemaVersion:      "pp-substack-update-error-v1",
		Stage:              string(updateErr.Stage),
		Code:               updateErr.Code,
		MutationDispatched: updateErr.MutationDispatched,
		Message:            safeUpdateErrorMessage(updateErr.Stage),
	}
	encoded, marshalErr := json.Marshal(envelope)
	if marshalErr != nil {
		return nil, false
	}
	return encoded, true
}

func safeUpdateErrorMessage(stage substack.UpdateStage) string {
	switch stage {
	case substack.UpdateStagePreMutation:
		return "Substack draft update failed before mutation"
	case substack.UpdateStageMutationUnknown:
		return "Substack draft update result is unknown"
	case substack.UpdateStagePostMutationVerification:
		return "Substack draft update response could not be verified"
	default:
		return "Substack draft update failed"
	}
}

func requiredFlag(name string) error {
	return usageError(fmt.Sprintf("--%s is required", name))
}
