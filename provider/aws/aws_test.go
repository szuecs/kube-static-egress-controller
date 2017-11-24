package aws

import (
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
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

	natCidrBlocks := []string{"172.31.64.0/28"}
	availabilityZones := []string{"eu-central-1a"}
	destinationCidrBlocks := []string{"213.95.138.236/32"}
	p := NewAwsProvider(true, natCidrBlocks, availabilityZones)
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
						Key:   aws.String(tagDefaultKeyRouteTableId),
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
	natCidrBlocks := []string{"172.31.64.0/28"}
	availabilityZones := []string{"eu-central-1a"}
	destinationCidrBlocks := []string{"213.95.138.236/32"}
	p := NewAwsProvider(true, natCidrBlocks, availabilityZones)
	expect := `{"AWSTemplateFormatVersion":"2010-09-09","Parameters":{"AZ1RouteTableIDParameter":{"Type":"String","Description":"Route Table ID Availability Zone 1"},"DestinationCidrBlock1":{"Type":"String","Default":"213.95.138.236/32","Description":"Destination CIDR Block 1"},"InternetGatewayIDParameter":{"Type":"String","Description":"Internet Gateway ID"},"VPCIDParameter":{"Type":"AWS::EC2::VPC::Id","Description":"VPC ID"}},"Resources":{"EIP1":{"Type":"AWS::EC2::EIP","Properties":{"Domain":"vpc"}},"NATGateway1":{"Type":"AWS::EC2::NatGateway","Properties":{"AllocationId":{"Fn::GetAtt":["EIP1","AllocationId"]},"SubnetId":{"Ref":"NATSubnet1"}}},"NATSubnet1":{"Type":"AWS::EC2::Subnet","Properties":{"AvailabilityZone":"eu-central-1a","CidrBlock":"172.31.64.0/28","Tags":[{"Key":"Name","Value":"nat-eu-central-1a"}],"VpcId":{"Ref":"VPCIDParameter"}}},"NATSubnetRoute1":{"Type":"AWS::EC2::Route","Properties":{"DestinationCidrBlock":"0.0.0.0/0","GatewayId":{"Ref":"InternetGatewayIDParameter"},"RouteTableId":{"Ref":"NATSubnetRouteTable1"}}},"NATSubnetRouteTable1":{"Type":"AWS::EC2::RouteTable","Properties":{"VpcId":{"Ref":"VPCIDParameter"}}},"NATSubnetRouteTableAssociation1":{"Type":"AWS::EC2::SubnetRouteTableAssociation","Properties":{"RouteTableId":{"Ref":"NATSubnetRouteTable1"},"SubnetId":{"Ref":"NATSubnet1"}}},"RouteToNAT1z213x95x138x236y32":{"Type":"AWS::EC2::Route","Properties":{"DestinationCidrBlock":{"Ref":"DestinationCidrBlock1"},"NatGatewayId":{"Ref":"NATGateway1"},"RouteTableId":{"Ref":"AZ1RouteTableIDParameter"}}}}}`
	template := p.generateTemplate(destinationCidrBlocks)
	if template != expect {
		t.Errorf("Expect:\n %s,\n but got %s", expect, template)
	}

}
