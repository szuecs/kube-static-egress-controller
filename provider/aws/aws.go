package aws

import (
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/aws-sdk-go/service/cloudformation/cloudformationiface"
	cf "github.com/crewjam/go-cloudformation"
	"github.com/linki/instrumented_http"
)

const ProviderName = "AWS"

type AwsProvider struct {
	natCidrBlocks     []string
	availabilityZones []string
	cloudformation    cloudformationiface.CloudFormationAPI
}

func NewAwsProvider(natCidrBlocks, availabilityZones []string) *AwsProvider {
	p := defaultConfigProvider()
	return &AwsProvider{
		natCidrBlocks:     natCidrBlocks,
		availabilityZones: availabilityZones,
		cloudformation:    cloudformation.New(p),
	}
}

func (p AwsProvider) String() string {
	return ProviderName
}

func (p *AwsProvider) Execute(nets []string) error {
	return nil
}

func generateTemplate() {
	natCidrBlocks := []string{"172.31.64.0/28", "172.31.64.16/28", "172.31.64.32/28"}
	availabilityZones := []string{"eu-central-1a", "eu-central-1b", "eu-central-1c"}
	destinationCidrBlocks := []string{"188.113.88.193/32", "8.8.8.8.8/32", "10.0.0.0/16"}

	template := cf.NewTemplate()
	template.Parameters["VPCIDParameter"] = &cf.Parameter{
		Description: "VPC ID",
		Type:        "AWS::EC2::VPC::Id",
	}
	template.Parameters["InternetGatewayIDParameter"] = &cf.Parameter{
		Description: "Internet Gateway ID",
		Type:        "String",
	}
	template.Parameters["AZ1RouteTableIDParameter"] = &cf.Parameter{
		Description: "Route Table ID Availability Zone 1",
		Type:        "String",
	}
	template.Parameters["AZ2RouteTableIDParameter"] = &cf.Parameter{
		Description: "Route Table ID Availability Zone 2",
		Type:        "String",
	}
	template.Parameters["AZ3RouteTableIDParameter"] = &cf.Parameter{
		Description: "Route Table ID Availability Zone 3",
		Type:        "String",
	}

	for i, destinationCidrBlock := range destinationCidrBlocks {
		template.Parameters[fmt.Sprintf("DestinationCidrBlock%d", i+1)] = &cf.Parameter{
			Description: fmt.Sprintf("Destination CIDR Block %d", i+1),
			Type:        "String",
			Default:     destinationCidrBlock,
		}
	}

	for i := 1; i <= len(availabilityZones); i++ {
		template.AddResource(fmt.Sprintf("NATGateway%d", i), &cf.EC2NatGateway{
			SubnetId: cf.Ref(
				fmt.Sprintf("NATSubnet%d", i)).String(),
			AllocationId: cf.GetAtt(
				fmt.Sprintf("EIP%d", i), "AllocationId"),
		})
		template.AddResource(fmt.Sprintf("EIP%d", i), &cf.EC2EIP{
			Domain: cf.String("vpc"),
		})
		template.AddResource(fmt.Sprintf("NATSubnet%d", i), &cf.EC2Subnet{
			CidrBlock:        cf.String(natCidrBlocks[i-1]),
			AvailabilityZone: cf.String(availabilityZones[i-1]),
			VpcId:            cf.Ref("VPCIDParameter").String(),
			Tags: []cf.ResourceTag{
				cf.ResourceTag{
					Key: cf.String("Name"),
					Value: cf.String(
						fmt.Sprintf("nat-%s", availabilityZones[i-1])),
				},
			},
		})
		template.AddResource(fmt.Sprintf("NATSubnetRoute%d", i), &cf.EC2Route{
			RouteTableId: cf.Ref(
				fmt.Sprintf("NATSubnetRouteTable%d", i)).String(),
			DestinationCidrBlock: cf.String("0.0.0.0/0"),
			GatewayId:            cf.Ref("InternetGatewayIDParameter").String(),
		})
		template.AddResource(fmt.Sprintf("NATSubnetRouteTableAssociation%d", i), &cf.EC2SubnetRouteTableAssociation{
			RouteTableId: cf.Ref(
				fmt.Sprintf("NATSubnetRouteTable%d", i)).String(),
			SubnetId: cf.Ref(
				fmt.Sprintf("NATSubnet%d", i)).String(),
		})
		template.AddResource(fmt.Sprintf("NATSubnetRouteTable%d", i), &cf.EC2RouteTable{
			VpcId: cf.Ref("VPCIDParameter").String(),
		})
		for j := range destinationCidrBlocks {
			template.AddResource(fmt.Sprintf("RouteToNAT%d", j+1), &cf.EC2Route{
				RouteTableId: cf.Ref(
					fmt.Sprintf("AZ%dRouteTableIDParameter", i)).String(),
				DestinationCidrBlock: cf.Ref(
					fmt.Sprintf("DestinationCidrBlock%d", j+1)).String(),
				NatGatewayId: cf.Ref(
					fmt.Sprintf("NATGateway%d", i)).String(),
			})
		}
	}
	stack, _ := json.Marshal(template)
	p := new(AwsProvider)
	p.createCFStack(string(stack))
}

const stackName = "egress-static-nat"

func (p *AwsProvider) deleteCFStack() error {
	params := &cloudformation.DeleteStackInput{StackName: aws.String(stackName)}
	_, err := p.cloudformation.DeleteStack(params)
	return err
}

func (p *AwsProvider) updateCFStack() error {
	template := templateYAML
	if spec.customTemplate != "" {
		template = spec.customTemplate
	}
	params := &cloudformation.UpdateStackInput{
		StackName: aws.String(spec.name),
		Parameters: []*cloudformation.Parameter{
			cfParam(parameterVPCIDParameter, spec.vpcID),
			cfParam(parameterInternetGatewayIDParameter, spec.internetGatewayID),
			cfParam(parameterAZ1RouteTableIDParameter, spec.routeTableIDAZ1),
			cfParam(parameterAZ2RouteTableIDParameter, spec.routeTableIDAZ2),
			cfParam(parameterAZ3RouteTableIDParameter, spec.routeTableIDAZ3),
		},
		Tags: []*cloudformation.Tag{
			cfTag(kubernetesCreatorTag, kubernetesCreatorValue),
			cfTag(clusterIDTagPrefix+spec.clusterID, resourceLifecycleOwned),
		},
		TemplateBody: aws.String(template),
	}
	if spec.certificateARN != "" {
		params.Tags = append(params.Tags, cfTag(certificateARNTag, spec.certificateARN))
	}
	resp, err := p.cloudformation.UpdateStack(params)
	if err != nil {
		return spec.name, err
	}

	return aws.StringValue(resp.StackId), nil
}
func (p *AwsProvider) createCFStack(stack string) error {
	stackSpec := &cloudformation.CreateStackInput{
		StackName:        aws.String(stackName),
		TimeoutInMinutes: aws.Int64(5),
		TemplateBody:     aws.String(stack),
	}
	//cfg := defaultConfigProvider()
	//c := cloudformation.New(cfg)
	fmt.Println(stackSpec)
	//c.CreateStack(stackSpec)
	return nil
	template := templateYAML
	if spec.customTemplate != "" {
		template = spec.customTemplate
	}
	params := &cloudformation.CreateStackInput{
		StackName: aws.String(spec.name),
		OnFailure: aws.String(cloudformation.OnFailureDelete),
		Parameters: []*cloudformation.Parameter{
			cfParam(parameterVPCIDParameter, spec.vpcID),
			cfParam(parameterInternetGatewayIDParameter, spec.internetGatewayID),
			cfParam(parameterAZ1RouteTableIDParameter, spec.routeTableIDAZ1),
			cfParam(parameterAZ2RouteTableIDParameter, spec.routeTableIDAZ2),
			cfParam(parameterAZ3RouteTableIDParameter, spec.routeTableIDAZ3),
		},
		Tags: []*cloudformation.Tag{
			cfTag(kubernetesCreatorTag, kubernetesCreatorValue),
			cfTag(clusterIDTag, spec.clusterID),
		},
		TemplateBody:     aws.String(template),
		TimeoutInMinutes: aws.Int64(int64(spec.timeoutInMinutes)),
	}
	resp, err := svc.CreateStack(params)
	if err != nil {
		return spec.name, err
	}

	return aws.StringValue(resp.StackId), nil
}

func defaultConfigProvider() client.ConfigProvider {
	cfg := aws.NewConfig().WithMaxRetries(3)
	cfg = cfg.WithHTTPClient(instrumented_http.NewClient(cfg.HTTPClient, nil))
	opts := session.Options{
		SharedConfigState: session.SharedConfigEnable,
		Config:            *cfg,
	}
	return session.Must(session.NewSessionWithOptions(opts))
}
