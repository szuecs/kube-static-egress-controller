package inmemory

const ProviderName = "inmemory"

type InMemoryProvider struct{}

func NewInMemoryProvider() *InMemoryProvider {
	return &InMemoryProvider{}
}

func (p InMemoryProvider) String() string {
	return ProviderName
}

func (p *InMemoryProvider) Execute(nets []string) error {
	return nil
}
