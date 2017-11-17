package aws

import (
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/aws-sdk-go/service/cloudformation/cloudformationiface"
	cft "github.com/crewjam/go-cloudformation"
	"github.com/linki/instrumented_http"
	log "github.com/sirupsen/logrus"
)

const (
	ProviderName                        = "aws"
	stackName                           = "egress-static-nat"
	parameterVPCIDParameter             = "VPCIDParameter"
	parameterInternetGatewayIDParameter = "InternetGatewayIDParameter"
	parameterAZ1RouteTableIDParameter   = "AZ1RouteTableIDParameter"
	parameterAZ2RouteTableIDParameter   = "AZ2RouteTableIDParameter"
	parameterAZ3RouteTableIDParameter   = "AZ3RouteTableIDParameter"
)

type AwsProvider struct {
	dry               bool
	natCidrBlocks     []string
	availabilityZones []string
	cloudformation    cloudformationiface.CloudFormationAPI
}

func NewAwsProvider(dry bool, natCidrBlocks, availabilityZones []string) *AwsProvider {
	p := defaultConfigProvider()
	return &AwsProvider{
		dry:               dry,
		natCidrBlocks:     natCidrBlocks,
		availabilityZones: availabilityZones,
		cloudformation:    cloudformation.New(p),
	}
}

func (p AwsProvider) String() string {
	return ProviderName
}

func (p *AwsProvider) Upsert(nets []string) error {
	log.Infof("%s Upsert(%v)", ProviderName, nets)
	if !p.dry {
		spec := &stackSpec{
			template: p.generateTemplate(nets),
		}
		stackID, err := p.createCFStack(nets, spec)
		if err != nil {
			return fmt.Errorf("Failed to create CF stack: %v", err)
		}
		log.Infof("%s: Created CF Stack %s", p, stackID)

		stackID, err = p.updateCFStack(nets, spec)
		if err != nil {
			return fmt.Errorf("Failed to update CF stack: %v", err)
		}
		log.Infof("%s: Updated CF Stack %s", p, stackID)
	}
	return nil
}

func (p *AwsProvider) Delete() error {
	log.Infof("%s Delete()", ProviderName)
	if !p.dry {
		p.deleteCFStack()
	}
	return nil
}

var parameterAZRouteTableIDParameter = []string{
	"AZ1RouteTableIDParameter",
	"AZ2RouteTableIDParameter",
	"AZ3RouteTableIDParameter",
}

type stackSpec struct {
	name              string
	vpcID             string
	internetGatewayID string
	routeTableIDAZ1   string
	routeTableIDAZ2   string
	routeTableIDAZ3   string
	timeoutInMinutes  uint
	template          string
}

func (p *AwsProvider) generateTemplate(nets []string) string {
	template := cft.NewTemplate()
	template.Parameters["VPCIDParameter"] = &cft.Parameter{
		Description: "VPC ID",
		Type:        "AWS::EC2::VPC::Id",
	}
	template.Parameters["InternetGatewayIDParameter"] = &cft.Parameter{
		Description: "Internet Gateway ID",
		Type:        "String",
	}
	template.Parameters["AZ1RouteTableIDParameter"] = &cft.Parameter{
		Description: "Route Table ID Availability Zone 1",
		Type:        "String",
	}
	template.Parameters["AZ2RouteTableIDParameter"] = &cft.Parameter{
		Description: "Route Table ID Availability Zone 2",
		Type:        "String",
	}
	template.Parameters["AZ3RouteTableIDParameter"] = &cft.Parameter{
		Description: "Route Table ID Availability Zone 3",
		Type:        "String",
	}

	for i, net := range nets {
		template.Parameters[fmt.Sprintf("DestinationCidrBlock%d", i+1)] = &cft.Parameter{
			Description: fmt.Sprintf("Destination CIDR Block %d", i+1),
			Type:        "String",
			Default:     net,
		}
	}

	for i := 1; i <= len(p.availabilityZones); i++ {
		template.AddResource(fmt.Sprintf("NATGateway%d", i), &cft.EC2NatGateway{
			SubnetId: cft.Ref(
				fmt.Sprintf("NATSubnet%d", i)).String(),
			AllocationId: cft.GetAtt(
				fmt.Sprintf("EIP%d", i), "AllocationId"),
		})
		template.AddResource(fmt.Sprintf("EIP%d", i), &cft.EC2EIP{
			Domain: cft.String("vpc"),
		})
		template.AddResource(fmt.Sprintf("NATSubnet%d", i), &cft.EC2Subnet{
			CidrBlock:        cft.String(p.natCidrBlocks[i-1]),
			AvailabilityZone: cft.String(p.availabilityZones[i-1]),
			VpcId:            cft.Ref("VPCIDParameter").String(),
			Tags: []cft.ResourceTag{
				cft.ResourceTag{
					Key: cft.String("Name"),
					Value: cft.String(
						fmt.Sprintf("nat-%s", p.availabilityZones[i-1])),
				},
			},
		})
		template.AddResource(fmt.Sprintf("NATSubnetRoute%d", i), &cft.EC2Route{
			RouteTableId: cft.Ref(
				fmt.Sprintf("NATSubnetRouteTable%d", i)).String(),
			DestinationCidrBlock: cft.String("0.0.0.0/0"),
			GatewayId:            cft.Ref("InternetGatewayIDParameter").String(),
		})
		template.AddResource(fmt.Sprintf("NATSubnetRouteTableAssociation%d", i), &cft.EC2SubnetRouteTableAssociation{
			RouteTableId: cft.Ref(
				fmt.Sprintf("NATSubnetRouteTable%d", i)).String(),
			SubnetId: cft.Ref(
				fmt.Sprintf("NATSubnet%d", i)).String(),
		})
		template.AddResource(fmt.Sprintf("NATSubnetRouteTable%d", i), &cft.EC2RouteTable{
			VpcId: cft.Ref("VPCIDParameter").String(),
		})
		for j := range nets {
			template.AddResource(fmt.Sprintf("RouteToNAT%d", j+1), &cft.EC2Route{
				RouteTableId: cft.Ref(
					fmt.Sprintf("AZ%dRouteTableIDParameter", i)).String(),
				DestinationCidrBlock: cft.Ref(
					fmt.Sprintf("DestinationCidrBlock%d", j+1)).String(),
				NatGatewayId: cft.Ref(
					fmt.Sprintf("NATGateway%d", i)).String(),
			})
		}
	}
	stack, _ := json.Marshal(template)
	return string(stack)
}

func (p *AwsProvider) deleteCFStack() error {
	params := &cloudformation.DeleteStackInput{StackName: aws.String(stackName)}
	_, err := p.cloudformation.DeleteStack(params)
	return err
}

func (p *AwsProvider) updateCFStack(nets []string, spec *stackSpec) (string, error) {
	params := &cloudformation.UpdateStackInput{
		StackName: aws.String(stackName),
		Parameters: []*cloudformation.Parameter{
			cfParam(parameterVPCIDParameter, spec.vpcID),
			cfParam(parameterInternetGatewayIDParameter, spec.internetGatewayID),
		},
		TemplateBody: aws.String(p.generateTemplate(nets)),
	}
	for i := range p.availabilityZones {
		params.Parameters = append(params.Parameters,
			cfParam(parameterAZRouteTableIDParameter[i], spec.routeTableIDAZ1))
	}
	resp, err := p.cloudformation.UpdateStack(params)
	if err != nil {
		return spec.name, err
	}

	return aws.StringValue(resp.StackId), nil
}

func (p *AwsProvider) createCFStack(nets []string, spec *stackSpec) (string, error) {
	params := &cloudformation.CreateStackInput{
		StackName: aws.String(spec.name),
		OnFailure: aws.String(cloudformation.OnFailureDelete),
		Parameters: []*cloudformation.Parameter{
			cfParam(parameterVPCIDParameter, spec.vpcID),
			cfParam(parameterInternetGatewayIDParameter, spec.internetGatewayID),
		},
		TemplateBody:     aws.String(spec.template),
		TimeoutInMinutes: aws.Int64(int64(spec.timeoutInMinutes)),
	}
	for i := range p.availabilityZones {
		params.Parameters = append(params.Parameters,
			cfParam(parameterAZRouteTableIDParameter[i], spec.routeTableIDAZ1))
	}
	resp, err := p.cloudformation.CreateStack(params)
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

func cfParam(key, value string) *cloudformation.Parameter {
	return &cloudformation.Parameter{
		ParameterKey:   aws.String(key),
		ParameterValue: aws.String(value),
	}
}
