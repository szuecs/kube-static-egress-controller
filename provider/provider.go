package provider

type Resource struct {
	Name      string
	Namespace string
}

type EgressConfig struct {
	Resource
	IPAddresses map[string]struct{}
}

type Provider interface {
	Ensure(configs map[Resource]map[string]struct{}) error
	String() string
}
