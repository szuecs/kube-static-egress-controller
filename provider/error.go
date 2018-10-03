package provider

type AlreadyExistsError struct {
	msg string
}

type DoesNotExistError struct {
	msg string
}

func (error *AlreadyExistsError) Error() string {
	return error.msg
}

func (error *DoesNotExistError) Error() string {
	return error.msg
}

func NewAlreadyExistsError(msg string) error {
	return &AlreadyExistsError{msg}
}

func NewDoesNotExistError(msg string) error {
	return &DoesNotExistError{msg}
}
