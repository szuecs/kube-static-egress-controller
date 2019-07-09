package aws

import (
	"errors"
	"net"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/aws-sdk-go/service/cloudformation/cloudformationiface"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"github.com/szuecs/kube-static-egress-controller/provider"
)

type mockedReceiveMsgs struct {
	ec2iface.EC2API
	RespVpcs             ec2.DescribeVpcsOutput
	RespInternetGateways ec2.DescribeInternetGatewaysOutput
	RespRouteTables      ec2.DescribeRouteTablesOutput
}

func (m mockedReceiveMsgs) DescribeVpcs(in *ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error) {
	return &m.RespVpcs, nil
}

func (m mockedReceiveMsgs) DescribeInternetGateways(in *ec2.DescribeInternetGatewaysInput) (*ec2.DescribeInternetGatewaysOutput, error) {
	return &m.RespInternetGateways, nil
}

func (m mockedReceiveMsgs) DescribeRouteTables(in *ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error) {
	return &m.RespRouteTables, nil
}

func TestGenerateStackSpec(t *testing.T) {
	expectedVpcId := "vpc-1111"
	expectedInternetGatewayId := "igw-1111"
	expectedRouteTableId := "rtb-1111"

	_, netA, _ := net.ParseCIDR("213.95.138.236/32")
	natCidrBlocks := []string{"172.31.64.0/28"}
	availabilityZones := []string{"eu-central-1a"}
	destinationCidrBlocks := map[provider.Resource]map[string]*net.IPNet{
		provider.Resource{
			Name:      "x",
			Namespace: "y",
		}: map[string]*net.IPNet{
			netA.String(): netA,
		},
	}
	p := NewAWSProvider("cluster-x", "controller-x", true, "", natCidrBlocks, availabilityZones, false)
	fakeVpcsResp := ec2.DescribeVpcsOutput{
		Vpcs: []*ec2.Vpc{
			&ec2.Vpc{
				VpcId:     aws.String("vpc-1111"),
				IsDefault: aws.Bool(true),
			},
		},
	}
	fakeIgwResp := ec2.DescribeInternetGatewaysOutput{
		InternetGateways: []*ec2.InternetGateway{
			&ec2.InternetGateway{
				InternetGatewayId: aws.String("igw-1111")},
		},
	}
	fakeRouteTablesResp := ec2.DescribeRouteTablesOutput{
		RouteTables: []*ec2.RouteTable{
			&ec2.RouteTable{
				VpcId:        aws.String("vpc-1111"),
				Routes:       []*ec2.Route{},
				RouteTableId: aws.String("rtb-1111"),
				Tags: []*ec2.Tag{
					&ec2.Tag{
						Key:   aws.String(tagDefaultAZKeyRouteTableID),
						Value: aws.String("eu-central-1a"),
					},
				},
			},
		},
	}

	p.ec2 = mockedReceiveMsgs{
		RespVpcs:             fakeVpcsResp,
		RespInternetGateways: fakeIgwResp,
		RespRouteTables:      fakeRouteTablesResp,
	}
	stackSpec, err := p.generateStackSpec(destinationCidrBlocks)
	if err != nil {
		t.Error("Failed to generate CloudFormation stack")
	}
	if stackSpec.vpcID != expectedVpcId {
		t.Errorf("Expect: %s,\n but got %s", expectedVpcId, stackSpec.vpcID)
	}
	if stackSpec.internetGatewayID != expectedInternetGatewayId {
		t.Errorf("Expect: %s,\n but got %s", expectedInternetGatewayId, stackSpec.internetGatewayID)
	}
	if stackSpec.tableID["eu-central-1a"] != expectedRouteTableId {
		t.Errorf("Expect: %s,\n but got %s", expectedRouteTableId, stackSpec.tableID["eu-central-1a"])
	}
}

func TestGenerateTemplate(t *testing.T) {
	_, netA, _ := net.ParseCIDR("213.95.138.236/32")
	natCidrBlocks := []string{"172.31.64.0/28"}
	availabilityZones := []string{"eu-central-1a"}
	destinationCidrBlocks := map[provider.Resource]map[string]*net.IPNet{
		provider.Resource{
			Name:      "x",
			Namespace: "y",
		}: map[string]*net.IPNet{
			netA.String(): netA,
		},
	}
	p := NewAWSProvider("cluster-x", "controller-x", true, "", natCidrBlocks, availabilityZones, false)
	expect := `{"AWSTemplateFormatVersion":"2010-09-09","Description":"Static Egress Stack","Parameters":{"AZ1RouteTableIDParameter":{"Type":"String","Description":"Route Table ID Availability Zone 1"},"DestinationCidrBlock1":{"Type":"String","Default":"213.95.138.236/32","Description":"Destination CIDR Block 1"},"InternetGatewayIDParameter":{"Type":"String","Description":"Internet Gateway ID"},"VPCIDParameter":{"Type":"AWS::EC2::VPC::Id","Description":"VPC ID"}},"Resources":{"EIP1":{"Type":"AWS::EC2::EIP","Properties":{"Domain":"vpc"}},"NATGateway1":{"Type":"AWS::EC2::NatGateway","Properties":{"AllocationId":{"Fn::GetAtt":["EIP1","AllocationId"]},"SubnetId":{"Ref":"NATSubnet1"}}},"NATSubnet1":{"Type":"AWS::EC2::Subnet","Properties":{"AvailabilityZone":"eu-central-1a","CidrBlock":"172.31.64.0/28","Tags":[{"Key":"Name","Value":"nat-eu-central-1a"}],"VpcId":{"Ref":"VPCIDParameter"}}},"NATSubnetRoute1":{"Type":"AWS::EC2::Route","Properties":{"DestinationCidrBlock":"0.0.0.0/0","GatewayId":{"Ref":"InternetGatewayIDParameter"},"RouteTableId":{"Ref":"NATSubnetRouteTable1"}}},"NATSubnetRouteTable1":{"Type":"AWS::EC2::RouteTable","Properties":{"VpcId":{"Ref":"VPCIDParameter"},"Tags":[{"Key":"Name","Value":"nat-eu-central-1a"}]}},"NATSubnetRouteTableAssociation1":{"Type":"AWS::EC2::SubnetRouteTableAssociation","Properties":{"RouteTableId":{"Ref":"NATSubnetRouteTable1"},"SubnetId":{"Ref":"NATSubnet1"}}},"RouteToNAT1z213x95x138x236y32":{"Type":"AWS::EC2::Route","Properties":{"DestinationCidrBlock":{"Ref":"DestinationCidrBlock1"},"NatGatewayId":{"Ref":"NATGateway1"},"RouteTableId":{"Ref":"AZ1RouteTableIDParameter"}}}},"Outputs":{"EIP1":{"Description":"external IP of the NATGateway1","Value":{"Ref":"EIP1"}}}}`
	template := p.generateTemplate(destinationCidrBlocks)
	if template != expect {
		t.Errorf("Expect:\n %s,\n but got %s", expect, template)
	}

}

type mockCloudformation struct {
	cloudformationiface.CloudFormationAPI
	err   error
	stack *cloudformation.Stack
}

func (cf *mockCloudformation) DescribeStacks(input *cloudformation.DescribeStacksInput) (*cloudformation.DescribeStacksOutput, error) {
	if cf.stack != nil {
		return &cloudformation.DescribeStacksOutput{
			Stacks: []*cloudformation.Stack{cf.stack},
		}, nil
	}
	return &cloudformation.DescribeStacksOutput{
		Stacks: nil,
	}, cf.err
}

func (cf *mockCloudformation) DescribeStacksPages(input *cloudformation.DescribeStacksInput, fn func(*cloudformation.DescribeStacksOutput, bool) bool) error {
	if cf.stack != nil {
		fn(&cloudformation.DescribeStacksOutput{
			Stacks: []*cloudformation.Stack{cf.stack},
		}, true)
		return nil
	}
	return cf.err
}

func (cf *mockCloudformation) CreateStack(input *cloudformation.CreateStackInput) (*cloudformation.CreateStackOutput, error) {
	cf.stack = &cloudformation.Stack{
		StackStatus: aws.String(cloudformation.StackStatusCreateComplete),
		Tags:        input.Tags,
	}
	return &cloudformation.CreateStackOutput{
		StackId: aws.String(""),
	}, cf.err
}

func (cf *mockCloudformation) UpdateStack(input *cloudformation.UpdateStackInput) (*cloudformation.UpdateStackOutput, error) {
	cf.stack = &cloudformation.Stack{
		StackStatus: aws.String(cloudformation.StackStatusUpdateComplete),
		Tags:        input.Tags,
	}
	return &cloudformation.UpdateStackOutput{
		StackId: aws.String(""),
	}, cf.err
}

func (cf *mockCloudformation) UpdateTerminationProtection(*cloudformation.UpdateTerminationProtectionInput) (*cloudformation.UpdateTerminationProtectionOutput, error) {
	return nil, cf.err
}

func (cf *mockCloudformation) DeleteStack(*cloudformation.DeleteStackInput) (*cloudformation.DeleteStackOutput, error) {
	cf.stack = &cloudformation.Stack{
		StackStatus: aws.String(cloudformation.StackStatusDeleteComplete),
	}
	return nil, cf.err
}

type mockEC2 struct {
	ec2iface.EC2API
	err                            error
	describeInternetGatewaysOutput *ec2.DescribeInternetGatewaysOutput
	describeRouteTables            *ec2.DescribeRouteTablesOutput
}

func (ec2 *mockEC2) DescribeInternetGateways(*ec2.DescribeInternetGatewaysInput) (*ec2.DescribeInternetGatewaysOutput, error) {
	return ec2.describeInternetGatewaysOutput, ec2.err
}

func (ec2 *mockEC2) DescribeRouteTables(*ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error) {
	return ec2.describeRouteTables, ec2.err
}

func TestEnsure(tt *testing.T) {
	_, netA, _ := net.ParseCIDR("213.95.138.235/32")
	_, netB, _ := net.ParseCIDR("213.95.138.236/32")

	for _, tc := range []struct {
		msg     string
		cf      *mockCloudformation
		ec2     *mockEC2
		configs map[provider.Resource]map[string]*net.IPNet
		success bool
		stack   *cloudformation.Stack
	}{
		{
			msg: "DescribeStacks failing should result in error.",
			cf: &mockCloudformation{
				err: errors.New("failed"),
			},
			success: false,
			stack:   nil,
		},
		{
			msg:     "don't do anything if the stack doesn't exist and the config is empty",
			cf:      &mockCloudformation{},
			success: true,
			stack:   nil,
		},
		{
			msg: "create new stack if it doesn't already exists",
			cf:  &mockCloudformation{},
			ec2: &mockEC2{
				describeInternetGatewaysOutput: &ec2.DescribeInternetGatewaysOutput{
					InternetGateways: []*ec2.InternetGateway{
						{
							InternetGatewayId: aws.String(""),
						},
					},
				},
				describeRouteTables: &ec2.DescribeRouteTablesOutput{
					RouteTables: []*ec2.RouteTable{
						{
							RouteTableId: aws.String(""),
						},
					},
				},
			},
			configs: map[provider.Resource]map[string]*net.IPNet{
				provider.Resource{
					Name:      "a",
					Namespace: "x",
				}: map[string]*net.IPNet{
					netA.String(): netA,
				},
			},
			success: true,
			stack: &cloudformation.Stack{
				StackStatus: aws.String(cloudformation.StackStatusCreateComplete),
				Tags: append(configsToTags(map[provider.Resource]map[string]*net.IPNet{
					provider.Resource{
						Name:      "a",
						Namespace: "x",
					}: map[string]*net.IPNet{
						netA.String(): netA,
					},
				}), []*cloudformation.Tag{
					{
						Key:   aws.String(clusterIDTagPrefix + "cluster-x"),
						Value: aws.String(resourceLifecycleOwned),
					},
					{
						Key:   aws.String(kubernetesApplicationTagKey),
						Value: aws.String("controller-x"),
					},
				}...),
			},
		},
		{
			msg: "delete stack if there are no configs",
			cf: &mockCloudformation{
				stack: &cloudformation.Stack{
					StackStatus: aws.String(cloudformation.StackStatusCreateComplete),
					Tags: []*cloudformation.Tag{
						{
							Key:   aws.String(clusterIDTagPrefix + "cluster-x"),
							Value: aws.String(resourceLifecycleOwned),
						},
						{
							Key:   aws.String(kubernetesApplicationTagKey),
							Value: aws.String("controller-x"),
						},
					},
				},
			},
			ec2: &mockEC2{
				describeInternetGatewaysOutput: &ec2.DescribeInternetGatewaysOutput{
					InternetGateways: []*ec2.InternetGateway{
						{
							InternetGatewayId: aws.String(""),
						},
					},
				},
				describeRouteTables: &ec2.DescribeRouteTablesOutput{
					RouteTables: []*ec2.RouteTable{
						{
							RouteTableId: aws.String(""),
						},
					},
				},
			},
			configs: nil,
			success: true,
			stack: &cloudformation.Stack{
				StackStatus: aws.String(cloudformation.StackStatusDeleteComplete),
			},
		},
		{
			msg: "update stack if there are changes to the configs",
			cf: &mockCloudformation{
				stack: &cloudformation.Stack{
					StackStatus: aws.String(cloudformation.StackStatusCreateComplete),
					Tags: append(configsToTags(map[provider.Resource]map[string]*net.IPNet{
						provider.Resource{
							Name:      "a",
							Namespace: "x",
						}: map[string]*net.IPNet{
							netA.String(): netA,
						},
					}), []*cloudformation.Tag{
						{
							Key:   aws.String(clusterIDTagPrefix + "cluster-x"),
							Value: aws.String(resourceLifecycleOwned),
						},
						{
							Key:   aws.String(kubernetesApplicationTagKey),
							Value: aws.String("controller-x"),
						},
					}...),
				},
			},
			ec2: &mockEC2{
				describeInternetGatewaysOutput: &ec2.DescribeInternetGatewaysOutput{
					InternetGateways: []*ec2.InternetGateway{
						{
							InternetGatewayId: aws.String(""),
						},
					},
				},
				describeRouteTables: &ec2.DescribeRouteTablesOutput{
					RouteTables: []*ec2.RouteTable{
						{
							RouteTableId: aws.String(""),
						},
					},
				},
			},
			configs: map[provider.Resource]map[string]*net.IPNet{
				provider.Resource{
					Name:      "a",
					Namespace: "x",
				}: map[string]*net.IPNet{
					netA.String(): netA,
					netB.String(): netB,
				},
			},
			success: true,
			stack: &cloudformation.Stack{
				StackStatus: aws.String(cloudformation.StackStatusUpdateComplete),
				Tags: append(configsToTags(map[provider.Resource]map[string]*net.IPNet{
					provider.Resource{
						Name:      "a",
						Namespace: "x",
					}: map[string]*net.IPNet{
						netA.String(): netA,
						netB.String(): netB,
					},
				}), []*cloudformation.Tag{
					{
						Key:   aws.String(clusterIDTagPrefix + "cluster-x"),
						Value: aws.String(resourceLifecycleOwned),
					},
					{
						Key:   aws.String(kubernetesApplicationTagKey),
						Value: aws.String("controller-x"),
					},
				}...),
			},
		},
	} {
		tt.Run(tc.msg, func(t *testing.T) {
			provider := &AWSProvider{
				clusterID:    "cluster-x",
				controllerID: "controller-x",
				vpcID:        "x",
				natCidrBlocks: []string{
					"172.31.64.0/28",
					"172.31.64.16/28",
					"172.31.64.32/28",
				},
				availabilityZones: []string{
					"eu-central-1a",
					"eu-central-1b",
					"eu-central-1c",
				},
				cloudformation:             tc.cf,
				ec2:                        tc.ec2,
				stackTerminationProtection: true,
				logger:                     log.WithFields(log.Fields{"provider": ProviderName}),
			}

			err := provider.Ensure(tc.configs)
			if tc.success {
				require.NoError(t, err)
				require.Equal(t, tc.cf.stack, tc.stack)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestCloudformationHasTags(tt *testing.T) {
	for _, tc := range []struct {
		msg          string
		expectedTags map[string]string
		tags         []*cloudformation.Tag
		expected     bool
	}{
		{
			msg: "matching tags should be found",
			expectedTags: map[string]string{
				"foo": "bar",
			},
			tags: []*cloudformation.Tag{
				{
					Key:   aws.String("foo"),
					Value: aws.String("bar"),
				},
			},
			expected: true,
		},
		{
			msg: "too many expected tags should not be found",
			expectedTags: map[string]string{
				"foo": "bar",
				"foz": "baz",
			},
			tags: []*cloudformation.Tag{
				{
					Key:   aws.String("foo"),
					Value: aws.String("bar"),
				},
			},
			expected: false,
		},
		{
			msg: "non matching values should not be found",
			expectedTags: map[string]string{
				"foo": "baz",
			},
			tags: []*cloudformation.Tag{
				{
					Key:   aws.String("foo"),
					Value: aws.String("bar"),
				},
			},
			expected: false,
		},
	} {
		tt.Run(tc.msg, func(t *testing.T) {
			require.Equal(t, tc.expected, cloudformationHasTags(tc.expectedTags, tc.tags))
		})
	}
}
