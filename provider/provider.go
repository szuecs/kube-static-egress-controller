package provider

type Provider interface {
	Create([]string) error
	Update([]string) error
	Delete() error
	String() string
}
