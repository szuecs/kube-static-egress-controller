package aws

const ProviderName = "AWS"

type AwsProvider struct {
	natCidrBlocks     []string
	availabilityZones []string
}

func NewAwsProvider(natCidrBlocks, availabilityZones []string) *AwsProvider {
	return &AwsProvider{
		natCidrBlocks:     natCidrBlocks,
		availabilityZones: availabilityZones,
	}
}

func (p AwsProvider) String() string {
	return ProviderName
}

func (p *AwsProvider) Execute(nets []string) error {
	return nil
}
