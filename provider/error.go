package provider

type AlreadyExistsError struct {
	Msg string
}

type DoesNotExistError struct {
	Msg string
}

func (error *AlreadyExistsError) Error() string {
	return error.Msg
}

func (error *DoesNotExistError) Error() string {
	return error.Msg
}

func NewAlreadyExistsError(text string) error {
	return &AlreadyExistsError{text}
}

func NewDoesNotExistError(text string) error {
	return &DoesNotExistError{text}
}
