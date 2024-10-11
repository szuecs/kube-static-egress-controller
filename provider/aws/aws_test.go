package aws

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"github.com/szuecs/kube-static-egress-controller/provider"
)

const (
	clusterIDTagPrefix = "kubernetes.io/cluster/"
)

type mockedReceiveMsgs struct {
	ec2API
	RespVpcs             ec2.DescribeVpcsOutput
	RespInternetGateways ec2.DescribeInternetGatewaysOutput
	RespRouteTables      ec2.DescribeRouteTablesOutput
}

func (m mockedReceiveMsgs) DescribeVpcs(_ context.Context, in *ec2.DescribeVpcsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return &m.RespVpcs, nil
}

func (m mockedReceiveMsgs) DescribeInternetGateways(_ context.Context, in *ec2.DescribeInternetGatewaysInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInternetGatewaysOutput, error) {
	return &m.RespInternetGateways, nil
}

func (m mockedReceiveMsgs) DescribeRouteTables(_ context.Context, in *ec2.DescribeRouteTablesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	return &m.RespRouteTables, nil
}

func TestGenerateStackSpec(t *testing.T) {
	expectedVpcId := "vpc-1111"
	expectedInternetGatewayId := "igw-1111"
	expectedRouteTableId := "rtb-1111"

	_, netA, _ := net.ParseCIDR("213.95.138.236/32")
	natCidrBlocks := []string{"172.31.64.0/28"}
	availabilityZones := []string{"eu-central-1a"}
	additionalStackTags := map[string]string{
		"foo": "bar",
	}
	expectedTags := []cftypes.Tag{
		{
			Key:   aws.String("foo"),
			Value: aws.String("bar"),
		},
		{
			Key:   aws.String(clusterIDTagPrefix + "cluster-x"),
			Value: aws.String(resourceLifecycleOwned),
		},
		{
			Key:   aws.String(kubernetesApplicationTagKey),
			Value: aws.String("controller-x"),
		},
	}

	destinationCidrBlocks := map[provider.Resource]map[string]*net.IPNet{
		{
			Name:      "x",
			Namespace: "y",
		}: {
			netA.String(): netA,
		},
	}

	fakeVpcsResp := ec2.DescribeVpcsOutput{
		Vpcs: []ec2types.Vpc{
			{
				VpcId:     aws.String("vpc-1111"),
				IsDefault: aws.Bool(true),
			},
		},
	}
	fakeIgwResp := ec2.DescribeInternetGatewaysOutput{
		InternetGateways: []ec2types.InternetGateway{
			{
				InternetGatewayId: aws.String("igw-1111")},
		},
	}
	fakeRouteTablesResp := ec2.DescribeRouteTablesOutput{
		RouteTables: []ec2types.RouteTable{
			{
				VpcId:        aws.String("vpc-1111"),
				Routes:       []ec2types.Route{},
				RouteTableId: aws.String("rtb-1111"),
				Tags: []ec2types.Tag{
					{
						Key:   aws.String(tagDefaultAZKeyRouteTableID),
						Value: aws.String("eu-central-1a"),
					},
				},
			},
		},
	}

	p := &AWSProvider{
		clusterID:          "cluster-x",
		clusterIDTagPrefix: clusterIDTagPrefix,
		controllerID:       "controller-x",
		dry:                true,
		vpcID:              "",
		natCidrBlocks:      natCidrBlocks,
		availabilityZones:  availabilityZones,
		ec2: mockedReceiveMsgs{
			RespVpcs:             fakeVpcsResp,
			RespInternetGateways: fakeIgwResp,
			RespRouteTables:      fakeRouteTablesResp,
		},
		stackTerminationProtection: false,
		additionalStackTags:        additionalStackTags,
		logger:                     log.WithFields(log.Fields{"provider": ProviderName}),
	}

	stackSpec, err := p.generateStackSpec(context.Background(), destinationCidrBlocks)
	if err != nil {
		t.Error("Failed to generate CloudFormation stack")
	}
	if stackSpec.vpcID != expectedVpcId {
		t.Errorf("Expect: %s,\n but got %s", expectedVpcId, stackSpec.vpcID)
	}
	if stackSpec.internetGatewayID != expectedInternetGatewayId {
		t.Errorf("Expect: %s,\n but got %s", expectedInternetGatewayId, stackSpec.internetGatewayID)
	}
	if stackSpec.tableID["AZ1RouteTableIDParameter"] != expectedRouteTableId {
		t.Errorf(
			"Expect: %s,\n but got %s",
			expectedRouteTableId,
			stackSpec.tableID["AZ1RouteTableIDParameter"],
		)
	}
	// sort tags to ensure stable comparison
	sort.Slice(stackSpec.tags, func(i, j int) bool {
		return aws.ToString(stackSpec.tags[i].Key) < aws.ToString(stackSpec.tags[j].Key)
	})
	require.EqualValues(t, expectedTags, stackSpec.tags)
}

func TestGenerateTemplate(t *testing.T) {
	_, netA, _ := net.ParseCIDR("213.95.138.236/32")
	natCidrBlocks := []string{"172.31.64.0/28"}
	availabilityZones := []string{"eu-central-1a"}
	destinationCidrBlocks := map[provider.Resource]map[string]*net.IPNet{
		{
			Name:      "x",
			Namespace: "y",
		}: {
			netA.String(): netA,
		},
	}
	p := &AWSProvider{
		clusterID:                  "cluster-x",
		clusterIDTagPrefix:         clusterIDTagPrefix,
		controllerID:               "controller-x",
		dry:                        true,
		vpcID:                      "",
		natCidrBlocks:              natCidrBlocks,
		availabilityZones:          availabilityZones,
		stackTerminationProtection: false,
		additionalStackTags:        nil,
		logger:                     log.WithFields(log.Fields{"provider": ProviderName}),
	}

	expect := `{"AWSTemplateFormatVersion":"2010-09-09","Description":"Static Egress Stack","Parameters":{"AZ1RouteTableIDParameter":{"Type":"String","Description":"Route Table ID No 1"},"InternetGatewayIDParameter":{"Type":"String","Description":"Internet Gateway ID"},"VPCIDParameter":{"Type":"AWS::EC2::VPC::Id","Description":"VPC ID"}},"Resources":{"EIP1":{"Type":"AWS::EC2::EIP","Properties":{"Domain":"vpc"}},"NATGateway1":{"Type":"AWS::EC2::NatGateway","Properties":{"AllocationId":{"Fn::GetAtt":["EIP1","AllocationId"]},"SubnetId":{"Ref":"NATSubnet1"}}},"NATSubnet1":{"Type":"AWS::EC2::Subnet","Properties":{"AvailabilityZone":"eu-central-1a","CidrBlock":"172.31.64.0/28","Tags":[{"Key":"Name","Value":"nat-eu-central-1a"}],"VpcId":{"Ref":"VPCIDParameter"}}},"NATSubnetRoute1":{"Type":"AWS::EC2::Route","Properties":{"DestinationCidrBlock":"0.0.0.0/0","GatewayId":{"Ref":"InternetGatewayIDParameter"},"RouteTableId":{"Ref":"NATSubnetRouteTable1"}}},"NATSubnetRouteTable1":{"Type":"AWS::EC2::RouteTable","Properties":{"Tags":[{"Key":"Name","Value":"nat-eu-central-1a"}],"VpcId":{"Ref":"VPCIDParameter"}}},"NATSubnetRouteTableAssociation1":{"Type":"AWS::EC2::SubnetRouteTableAssociation","Properties":{"RouteTableId":{"Ref":"NATSubnetRouteTable1"},"SubnetId":{"Ref":"NATSubnet1"}}},"RouteToNAT1z213x95x138x236y32":{"Type":"AWS::EC2::Route","Properties":{"DestinationCidrBlock":"213.95.138.236/32","NatGatewayId":{"Ref":"NATGateway1"},"RouteTableId":{"Ref":"AZ1RouteTableIDParameter"}}}},"Outputs":{"EIP1":{"Description":"external IP of the NATGateway1","Value":{"Ref":"EIP1"}}}}`
	template := p.generateTemplate(
		destinationCidrBlocks,
		[]string{"AZ1RouteTableIDParameter"},
		map[string]int{"AZ1RouteTableIDParameter": 0},
	)
	if template != expect {
		t.Errorf("Expect:\n %s,\n but got:\n %s", expect, template)
	}

}

type mockCloudformation struct {
	err          error
	stack        cftypes.Stack
	templateBody string
	templateURL  string
}

func (cf *mockCloudformation) DescribeStacks(_ context.Context, input *cloudformation.DescribeStacksInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	if cf.stack.StackName != nil {
		return &cloudformation.DescribeStacksOutput{
			Stacks: []cftypes.Stack{cf.stack},
		}, nil
	}
	return &cloudformation.DescribeStacksOutput{
		Stacks: nil,
	}, cf.err
}

func (cf *mockCloudformation) GetTemplate(_ context.Context, input *cloudformation.GetTemplateInput, optFns ...func(*cloudformation.Options)) (*cloudformation.GetTemplateOutput, error) {
	if cf.templateBody != "" {
		return &cloudformation.GetTemplateOutput{
			TemplateBody: aws.String(cf.templateBody),
		}, nil
	}
	return &cloudformation.GetTemplateOutput{
		TemplateBody: nil,
	}, cf.err
}

func (cf *mockCloudformation) CreateStack(_ context.Context, input *cloudformation.CreateStackInput, optFns ...func(*cloudformation.Options)) (*cloudformation.CreateStackOutput, error) {
	cf.stack = cftypes.Stack{
		StackName:   aws.String("stack"),
		StackStatus: cftypes.StackStatusCreateComplete,
		Tags:        input.Tags,
	}
	cf.templateBody = aws.ToString(input.TemplateBody)
	cf.templateURL = aws.ToString(input.TemplateURL)
	return &cloudformation.CreateStackOutput{
		StackId: aws.String(""),
	}, cf.err
}

func (cf *mockCloudformation) UpdateStack(_ context.Context, input *cloudformation.UpdateStackInput, optFns ...func(*cloudformation.Options)) (*cloudformation.UpdateStackOutput, error) {
	cf.stack = cftypes.Stack{
		StackName:   aws.String("stack"),
		StackStatus: cftypes.StackStatusUpdateComplete,
		Tags:        input.Tags,
	}
	cf.templateBody = aws.ToString(input.TemplateBody)
	cf.templateURL = aws.ToString(input.TemplateURL)
	return &cloudformation.UpdateStackOutput{
		StackId: aws.String(""),
	}, cf.err
}

func (cf *mockCloudformation) UpdateTerminationProtection(context.Context, *cloudformation.UpdateTerminationProtectionInput, ...func(*cloudformation.Options)) (*cloudformation.UpdateTerminationProtectionOutput, error) {
	return nil, cf.err
}

func (cf *mockCloudformation) DeleteStack(context.Context, *cloudformation.DeleteStackInput, ...func(*cloudformation.Options)) (*cloudformation.DeleteStackOutput, error) {
	cf.stack = cftypes.Stack{
		StackName:   aws.String("stack"),
		StackStatus: cftypes.StackStatusDeleteComplete,
	}
	return nil, cf.err
}

type mockEC2 struct {
	ec2API
	err                            error
	describeInternetGatewaysOutput *ec2.DescribeInternetGatewaysOutput
	describeRouteTables            *ec2.DescribeRouteTablesOutput
}

func (ec2 *mockEC2) DescribeInternetGateways(context.Context, *ec2.DescribeInternetGatewaysInput, ...func(*ec2.Options)) (*ec2.DescribeInternetGatewaysOutput, error) {
	return ec2.describeInternetGatewaysOutput, ec2.err
}

func (ec2 *mockEC2) DescribeRouteTables(context.Context, *ec2.DescribeRouteTablesInput, ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	return ec2.describeRouteTables, ec2.err
}

type mockS3UploaderAPI struct {
	err error
}

func (s3 *mockS3UploaderAPI) Upload(ctx context.Context, input *s3.PutObjectInput, opts ...func(*manager.Uploader)) (*manager.UploadOutput, error) {
	return &manager.UploadOutput{Location: aws.ToString(input.Bucket) + "/" + aws.ToString(input.Key)}, s3.err
}

func TestEnsure(tt *testing.T) {
	_, netA, _ := net.ParseCIDR("213.95.138.235/32")
	_, netB, _ := net.ParseCIDR("213.95.138.236/32")

	for _, tc := range []struct {
		msg                       string
		cf                        *mockCloudformation
		ec2                       *mockEC2
		configs                   map[provider.Resource]map[string]*net.IPNet
		success                   bool
		expectedStack             cftypes.Stack
		expectedCIDRsFromTemplate map[string]struct{}
	}{
		{
			msg: "DescribeStacks failing should result in error.",
			cf: &mockCloudformation{
				err: errors.New("failed"),
			},
			success: false,
		},
		{
			msg:     "don't do anything if the stack doesn't exist and the config is empty",
			cf:      &mockCloudformation{},
			success: true,
		},
		{
			msg: "create new stack if it doesn't already exists",
			cf:  &mockCloudformation{},
			ec2: &mockEC2{
				describeInternetGatewaysOutput: &ec2.DescribeInternetGatewaysOutput{
					InternetGateways: []ec2types.InternetGateway{
						{
							InternetGatewayId: aws.String(""),
						},
					},
				},
				describeRouteTables: &ec2.DescribeRouteTablesOutput{
					RouteTables: []ec2types.RouteTable{
						{
							RouteTableId: aws.String(""),
						},
					},
				},
			},
			configs: map[provider.Resource]map[string]*net.IPNet{
				{
					Name:      "a",
					Namespace: "x",
				}: {
					netA.String(): netA,
				},
			},
			success: true,
			expectedStack: cftypes.Stack{
				StackName:   aws.String("stack"),
				StackStatus: cftypes.StackStatusCreateComplete,
				Tags: []cftypes.Tag{
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
		{
			msg: "delete stack if there are no configs",
			cf: &mockCloudformation{
				stack: cftypes.Stack{
					StackName:   aws.String("stack"),
					StackStatus: cftypes.StackStatusCreateComplete,
					Tags: []cftypes.Tag{
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
					InternetGateways: []ec2types.InternetGateway{
						{
							InternetGatewayId: aws.String(""),
						},
					},
				},
				describeRouteTables: &ec2.DescribeRouteTablesOutput{
					RouteTables: []ec2types.RouteTable{
						{
							RouteTableId: aws.String(""),
						},
					},
				},
			},
			configs: nil,
			success: true,
			expectedStack: cftypes.Stack{
				StackName:   aws.String("stack"),
				StackStatus: cftypes.StackStatusDeleteComplete,
			},
		},
		{
			msg: "update stack if there are changes to the configs",
			cf: &mockCloudformation{
				stack: cftypes.Stack{
					StackName:   aws.String("stack"),
					StackStatus: cftypes.StackStatusCreateComplete,
					Tags: []cftypes.Tag{
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
				templateBody: fmt.Sprintf(`{"AWSTemplateFormatVersion":"2010-09-09","Description":"Static Egress Stack","Parameters":{"AZ1RouteTableIDParameter":{"Type":"String","Description":"Route Table ID Availability Zone 1"},"InternetGatewayIDParameter":{"Type":"String","Description":"Internet Gateway ID"},"VPCIDParameter":{"Type":"AWS::EC2::VPC::Id","Description":"VPC ID"}},"Resources":{"EIP1":{"Type":"AWS::EC2::EIP","Properties":{"Domain":"vpc"}},"NATGateway1":{"Type":"AWS::EC2::NatGateway","Properties":{"AllocationId":{"Fn::GetAtt":["EIP1","AllocationId"]},"SubnetId":{"Ref":"NATSubnet1"}}},"NATSubnet1":{"Type":"AWS::EC2::Subnet","Properties":{"AvailabilityZone":"eu-central-1a","CidrBlock":"172.31.64.0/28","Tags":[{"Key":"Name","Value":"nat-eu-central-1a"}],"VpcId":{"Ref":"VPCIDParameter"}}},"NATSubnetRoute1":{"Type":"AWS::EC2::Route","Properties":{"DestinationCidrBlock":"0.0.0.0/0","GatewayId":{"Ref":"InternetGatewayIDParameter"},"RouteTableId":{"Ref":"NATSubnetRouteTable1"}}},"NATSubnetRouteTable1":{"Type":"AWS::EC2::RouteTable","Properties":{"VpcId":{"Ref":"VPCIDParameter"},"Tags":[{"Key":"Name","Value":"nat-eu-central-1a"}]}},"NATSubnetRouteTableAssociation1":{"Type":"AWS::EC2::SubnetRouteTableAssociation","Properties":{"RouteTableId":{"Ref":"NATSubnetRouteTable1"},"SubnetId":{"Ref":"NATSubnet1"}}},"RouteToNAT1z213x95x138x235y32":{"Type":"AWS::EC2::Route","Properties":{"DestinationCidrBlock":"%s","NatGatewayId":{"Ref":"NATGateway1"},"RouteTableId":{"Ref":"AZ1RouteTableIDParameter"}}}},"Outputs":{"EIP1":{"Description":"external IP of the NATGateway1","Value":{"Ref":"EIP1"}}}}`, netA.String()),
			},
			ec2: &mockEC2{
				describeInternetGatewaysOutput: &ec2.DescribeInternetGatewaysOutput{
					InternetGateways: []ec2types.InternetGateway{
						{
							InternetGatewayId: aws.String(""),
						},
					},
				},
				describeRouteTables: &ec2.DescribeRouteTablesOutput{
					RouteTables: []ec2types.RouteTable{
						{
							RouteTableId: aws.String("foo"),
							Tags: []ec2types.Tag{
								{
									Key:   aws.String("AvailabilityZone"),
									Value: aws.String("eu-central-1a"),
								},
							},
						},
					},
				},
			},
			configs: map[provider.Resource]map[string]*net.IPNet{
				{
					Name:      "a",
					Namespace: "x",
				}: {
					netA.String(): netA,
					netB.String(): netB,
				},
			},
			success: true,
			expectedStack: cftypes.Stack{
				StackName:   aws.String("stack"),
				StackStatus: cftypes.StackStatusUpdateComplete,
				Tags: []cftypes.Tag{
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
			expectedCIDRsFromTemplate: map[string]struct{}{
				netA.String(): {},
				netB.String(): {},
			},
		},
		{
			msg: "correctly update 'old' stack if there are changes to the configs",
			cf: &mockCloudformation{
				stack: cftypes.Stack{
					StackName:   aws.String("stack"),
					StackStatus: cftypes.StackStatusCreateComplete,
					Tags: []cftypes.Tag{
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
				templateBody: fmt.Sprintf(`{"AWSTemplateFormatVersion":"2010-09-09","Description":"Static Egress Stack","Parameters":{"AZ1RouteTableIDParameter":{"Type":"String","Description":"Route Table ID Availability Zone 1"},"DestinationCidrBlock1":{"Type":"String","Default":"%s","Description":"Destination CIDR Block 1"},"InternetGatewayIDParameter":{"Type":"String","Description":"Internet Gateway ID"},"VPCIDParameter":{"Type":"AWS::EC2::VPC::Id","Description":"VPC ID"}},"Resources":{"EIP1":{"Type":"AWS::EC2::EIP","Properties":{"Domain":"vpc"}},"NATGateway1":{"Type":"AWS::EC2::NatGateway","Properties":{"AllocationId":{"Fn::GetAtt":["EIP1","AllocationId"]},"SubnetId":{"Ref":"NATSubnet1"}}},"NATSubnet1":{"Type":"AWS::EC2::Subnet","Properties":{"AvailabilityZone":"eu-central-1a","CidrBlock":"172.31.64.0/28","Tags":[{"Key":"Name","Value":"nat-eu-central-1a"}],"VpcId":{"Ref":"VPCIDParameter"}}},"NATSubnetRoute1":{"Type":"AWS::EC2::Route","Properties":{"DestinationCidrBlock":"0.0.0.0/0","GatewayId":{"Ref":"InternetGatewayIDParameter"},"RouteTableId":{"Ref":"NATSubnetRouteTable1"}}},"NATSubnetRouteTable1":{"Type":"AWS::EC2::RouteTable","Properties":{"VpcId":{"Ref":"VPCIDParameter"},"Tags":[{"Key":"Name","Value":"nat-eu-central-1a"}]}},"NATSubnetRouteTableAssociation1":{"Type":"AWS::EC2::SubnetRouteTableAssociation","Properties":{"RouteTableId":{"Ref":"NATSubnetRouteTable1"},"SubnetId":{"Ref":"NATSubnet1"}}},"RouteToNAT1z213x95x138x236y32":{"Type":"AWS::EC2::Route","Properties":{"DestinationCidrBlock":{"Ref":"DestinationCidrBlock1"},"NatGatewayId":{"Ref":"NATGateway1"},"RouteTableId":{"Ref":"AZ1RouteTableIDParameter"}}}},"Outputs":{"EIP1":{"Description":"external IP of the NATGateway1","Value":{"Ref":"EIP1"}}}}`, netA.String()),
			},
			ec2: &mockEC2{
				describeInternetGatewaysOutput: &ec2.DescribeInternetGatewaysOutput{
					InternetGateways: []ec2types.InternetGateway{
						{
							InternetGatewayId: aws.String(""),
						},
					},
				},
				describeRouteTables: &ec2.DescribeRouteTablesOutput{
					RouteTables: []ec2types.RouteTable{
						{
							RouteTableId: aws.String("foo"),
							Tags: []ec2types.Tag{{
								Key:   aws.String("AvailabilityZone"),
								Value: aws.String("eu-central-1a"),
							}},
						},
					},
				},
			},
			configs: map[provider.Resource]map[string]*net.IPNet{
				{
					Name:      "a",
					Namespace: "x",
				}: {
					netA.String(): netA,
					netB.String(): netB,
				},
			},
			success: true,
			expectedStack: cftypes.Stack{
				StackName:   aws.String("stack"),
				StackStatus: cftypes.StackStatusUpdateComplete,
				Tags: []cftypes.Tag{
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
			expectedCIDRsFromTemplate: map[string]struct{}{
				netA.String(): {},
				netB.String(): {},
			},
		},
	} {
		tt.Run(tc.msg, func(t *testing.T) {
			provider := &AWSProvider{
				clusterIDTagPrefix: clusterIDTagPrefix,
				clusterID:          "cluster-x",
				controllerID:       "controller-x",
				vpcID:              "x",
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

			err := provider.Ensure(context.Background(), tc.configs)
			if tc.success {
				require.NoError(t, err)
				if tc.cf.stack.StackName != nil && len(tc.cf.stack.Tags) > 0 {
					// sort tags to ensure stable comparison
					sort.Slice(tc.cf.stack.Tags, func(i, j int) bool {
						return aws.ToString(tc.cf.stack.Tags[i].Key) < aws.ToString(tc.cf.stack.Tags[j].Key)
					})
				}
				require.Equal(t, tc.expectedStack, tc.cf.stack)
				if len(tc.expectedCIDRsFromTemplate) > 0 {
					cidrs := getCIDRsFromTemplate(tc.cf.templateBody)
					require.NotNil(t, cidrs)
					require.Equal(t, tc.expectedCIDRsFromTemplate, cidrs)
				}
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestCloudformationS3TemplateUpload(t *testing.T) {
	for _, tc := range []struct {
		msg              string
		cfTemplateBucket string
		s3UploaderErr    error
	}{
		{
			msg:              "when cfTemplateBucket is set it should upload",
			cfTemplateBucket: "bucket",
		},
		{
			msg:              "Fail on upload error when cfTemplateBucket is set",
			cfTemplateBucket: "bucket",
			s3UploaderErr:    errors.New("failed"),
		},
	} {
		t.Run(tc.msg, func(t *testing.T) {
			cloudformationAPI := &mockCloudformation{}

			provider := &AWSProvider{
				vpcID:            "x",
				cloudformation:   cloudformationAPI,
				s3Uploader:       &mockS3UploaderAPI{err: tc.s3UploaderErr},
				cfTemplateBucket: tc.cfTemplateBucket,
				logger:           log.WithFields(log.Fields{"provider": ProviderName}),
			}

			err := provider.createCFStack(context.Background(), &stackSpec{name: "stack", template: "<template>"})
			if tc.s3UploaderErr != nil {
				require.Error(t, err)
				return
			} else {
				require.NoError(t, err)
			}

			if tc.cfTemplateBucket != "" {
				require.NotEqual(t, cloudformationAPI.templateURL, "")
				require.Equal(t, cloudformationAPI.templateBody, "")
			} else {
				require.Equal(t, cloudformationAPI.templateURL, "")
				require.NotEqual(t, cloudformationAPI.templateBody, "")
			}

			err = provider.updateCFStack(context.Background(), &stackSpec{name: "stack", template: "<template>"})
			if tc.s3UploaderErr != nil {
				require.Error(t, err)
				return
			} else {
				require.NoError(t, err)
			}

			if tc.cfTemplateBucket != "" {
				require.NotEqual(t, cloudformationAPI.templateURL, "")
				require.Equal(t, cloudformationAPI.templateBody, "")
			} else {
				require.Equal(t, cloudformationAPI.templateURL, "")
				require.NotEqual(t, cloudformationAPI.templateBody, "")
			}
		})
	}
}

func TestCloudformationHasTags(tt *testing.T) {
	for _, tc := range []struct {
		msg          string
		expectedTags map[string]string
		tags         []cftypes.Tag
		expected     bool
	}{
		{
			msg: "matching tags should be found",
			expectedTags: map[string]string{
				"foo": "bar",
			},
			tags: []cftypes.Tag{
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
			tags: []cftypes.Tag{
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
			tags: []cftypes.Tag{
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
