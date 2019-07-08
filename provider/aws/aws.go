package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/apparentlymart/go-cidr/cidr"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/aws-sdk-go/service/cloudformation/cloudformationiface"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
	cft "github.com/crewjam/go-cloudformation"
	"github.com/linki/instrumented_http"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/szuecs/kube-static-egress-controller/provider"
)

const (
	ProviderName                        = "aws"
	stackName                           = "egress-static-nat"
	parameterVPCIDParameter             = "VPCIDParameter"
	parameterInternetGatewayIDParameter = "InternetGatewayIDParameter"
	tagDefaultAZKeyRouteTableID         = "AvailabilityZone"
	tagDefaultTypeValueRouteTableID     = "dmz" // find route table by "Type" tag = "dmz"
	egressConfigTagPrefix               = "egress-config/"
	maxStackWaitTimeout                 = 15 * time.Minute
	stackStatusCheckInterval            = 15 * time.Second
)

var (
	errCreateFailed           = fmt.Errorf("wait for stack failed with %s", cloudformation.StackStatusCreateFailed)
	errRollbackComplete       = fmt.Errorf("wait for stack failed with %s", cloudformation.StackStatusRollbackComplete)
	errUpdateRollbackComplete = fmt.Errorf("wait for stack failed with %s", cloudformation.StackStatusUpdateRollbackComplete)
	errRollbackFailed         = fmt.Errorf("wait for stack failed with %s", cloudformation.StackStatusRollbackFailed)
	errUpdateRollbackFailed   = fmt.Errorf("wait for stack failed with %s", cloudformation.StackStatusUpdateRollbackFailed)
	errDeleteFailed           = fmt.Errorf("wait for stack failed with %s", cloudformation.StackStatusDeleteFailed)
	errTimeoutExceeded        = fmt.Errorf("wait for stack timeout exceeded")
)

type AWSProvider struct {
	dry                        bool
	vpcID                      string
	natCidrBlocks              []string
	availabilityZones          []string
	cloudformation             cloudformationiface.CloudFormationAPI
	ec2                        ec2iface.EC2API
	stackTerminationProtection bool
	logger                     *log.Entry
}

func NewAWSProvider(dry bool, vpcID string, natCidrBlocks, availabilityZones []string, stackTerminationProtection bool) *AWSProvider {
	// TODO: find vpcID at startup
	p := defaultConfigProvider()
	return &AWSProvider{
		dry:                        dry,
		vpcID:                      vpcID,
		natCidrBlocks:              natCidrBlocks,
		availabilityZones:          availabilityZones,
		cloudformation:             cloudformation.New(p),
		ec2:                        ec2.New(p),
		stackTerminationProtection: stackTerminationProtection,
		logger:                     log.WithFields(log.Fields{"provider": ProviderName}),
	}
}

func (p AWSProvider) String() string {
	return ProviderName
}

func generateRoutes(configs map[provider.Resource]map[string]*net.IPNet) []string {
	cidrs := make([]*net.IPNet, 0, len(configs))
	for _, rs := range configs {
		for _, ipnet := range rs {
			cidrs = append(cidrs, ipnet)
		}
	}

	sort.Slice(cidrs, func(i, j int) bool {
		countI := cidr.AddressCount(cidrs[i])
		countJ := cidr.AddressCount(cidrs[j])
		if countI == countJ {
			return cidrs[i].String() < cidrs[j].String()
		}
		return countI < countJ
	})

	newCIDRs := make([]string, 0, len(cidrs))
	i := 0
	for _, c := range cidrs {
		contained := false
		if i < len(cidrs)-1 {
			for _, block := range cidrs[i+1:] {
				if networkContained(c, block) {
					contained = true
					break
				}
			}
		}

		if !contained {
			newCIDRs = append(newCIDRs, c.String())
		}
		i++
	}

	return newCIDRs
}

func networkContained(subnet, CIDRBlock *net.IPNet) bool {
	first, last := cidr.AddressRange(subnet)
	return CIDRBlock.Contains(first) && CIDRBlock.Contains(last)
}

func (p *AWSProvider) Ensure(configs map[provider.Resource]map[string]*net.IPNet) error {
	params := &cloudformation.DescribeStacksInput{
		StackName: aws.String(stackName),
	}
	resp, err := p.cloudformation.DescribeStacks(params)
	if err != nil {
		if aerr, ok := err.(awserr.Error); !ok || aerr.Code() == cloudformation.ErrCodeStackInstanceNotFoundException {
			return err
		}
	}

	// don't do anything if the stack doesn't exist and the config is empty
	if len(configs) == 0 && len(resp.Stacks) == 0 {
		return nil
	}

	spec, err := p.generateStackSpec(configs)
	if err != nil {
		return errors.Wrap(err, "failed to generate stack spec")
	}

	// create new stack if it doesn't already exists
	if len(resp.Stacks) == 0 {
		p.logger.Infof("Creating CF stack with config: %v", configs)
		err := p.createCFStack(spec)
		if err != nil {
			return errors.Wrap(err, "failed to create CF stack")
		}
		p.logger.Infof("Created CF stack with config: %v", configs)
		return nil
	}

	if len(resp.Stacks) != 1 {
		return fmt.Errorf("found %d stacks, expected 1", len(resp.Stacks))
	}

	stack := resp.Stacks[0]

	if len(configs) == 0 {
		p.logger.Info("Deleting CF stack. No egress configs")
		err := p.deleteCFStack()
		if err != nil {
			return err
		}
		p.logger.Info("Deleted CF stack.")
		return nil
	}

	storedConfigs := parseTagsToEgressConfigStore(stack.Tags)

	// compare configs
	if configsEqual(storedConfigs, configs) {
		return nil
	}

	// update stack with new config
	p.logger.Infof("Updating CF stack with config: %v", configs)
	err = p.updateCFStack(spec)
	if err != nil {
		return errors.Wrap(err, "failed to update CF stack")
	}
	p.logger.Infof("Updated CF stack with config: %v", configs)
	return nil
}

func configsToTags(configs map[provider.Resource]map[string]*net.IPNet) []*cloudformation.Tag {
	tags := make([]*cloudformation.Tag, 0, len(configs))
	for config, ipAddresses := range configs {
		addresses := make([]string, 0, len(ipAddresses))
		for address := range ipAddresses {
			addresses = append(addresses, address)
		}
		sort.Strings(addresses)
		tag := &cloudformation.Tag{
			Key:   aws.String(egressConfigTagPrefix + "configmap/" + config.Namespace + "/" + config.Name),
			Value: aws.String(strings.Join(addresses, ",")),
		}
		tags = append(tags, tag)
	}
	return tags
}

func parseTagsToEgressConfigStore(tags []*cloudformation.Tag) map[provider.Resource]map[string]*net.IPNet {
	store := make(map[provider.Resource]map[string]*net.IPNet)
	for _, tag := range tags {
		cfg := parseTagAsEgressConfig(tag)
		if cfg != nil {
			store[cfg.Resource] = cfg.IPAddresses
		}
	}
	return store
}

// parseTagAsEgressConfig parses tags of the format
// egress-config/<resource-type>/<namespace>/<name>=<ipAddress>,
func parseTagAsEgressConfig(tag *cloudformation.Tag) *provider.EgressConfig {
	key := aws.StringValue(tag.Key)
	value := aws.StringValue(tag.Value)
	if strings.HasPrefix(key, egressConfigTagPrefix) {
		parts := strings.Split(key, "/")
		if len(parts) != 4 {
			return nil
		}
		ips := strings.Split(value, ",")
		ipsSet := make(map[string]*net.IPNet)
		for _, ip := range ips {
			_, ipnet, err := net.ParseCIDR(ip)
			if err != nil {
				continue
			}
			ipsSet[ipnet.String()] = ipnet
		}
		return &provider.EgressConfig{
			Resource: provider.Resource{
				Name:      parts[3],
				Namespace: parts[2],
			},
			IPAddresses: ipsSet,
		}
	}
	return nil
}

func configsEqual(a, b map[provider.Resource]map[string]*net.IPNet) bool {
	if len(a) != len(b) {
		return false
	}

	for aKey, aIPs := range a {
		bIPs, ok := b[aKey]
		if !ok {
			return false
		}
		if len(aIPs) != len(bIPs) {
			return false
		}
		for aIP := range aIPs {
			if _, ok := bIPs[aIP]; !ok {
				return false
			}
		}
	}
	return true
}

func (p *AWSProvider) generateStackSpec(configs map[provider.Resource]map[string]*net.IPNet) (*stackSpec, error) {
	spec := &stackSpec{
		template:                   p.generateTemplate(configs),
		tableID:                    make(map[string]string),
		timeoutInMinutes:           10,
		stackTerminationProtection: p.stackTerminationProtection,
		tags:                       configsToTags(configs),
	}

	vpcID, err := p.findVPC()
	if err != nil {
		return nil, err
	}
	spec.vpcID = vpcID

	// get assigned internet gateway
	igw, err := p.getInternetGatewayId(spec.vpcID)
	p.logger.Debugf("%s: igw(%d)", p, len(igw))
	if err != nil {
		return nil, err
	}

	if len(igw) == 0 {
		return nil, fmt.Errorf("no Internet Gateways found")
	}

	// get first internet gateway ID
	igwID := aws.StringValue(igw[0].InternetGatewayId)
	spec.internetGatewayID = igwID

	// get route tables
	rt, err := p.getRouteTables(spec.vpcID)
	p.logger.Debugf("%s: rt(%d)", p, len(rt))
	if err != nil {
		return nil, err
	}

	// adding route tables to spec
	for _, table := range rt {
		for _, tag := range table.Tags {
			if tagDefaultAZKeyRouteTableID == aws.StringValue(tag.Key) {
				// eu-central-1a -> rtb-b738aadc
				spec.tableID[aws.StringValue(tag.Value)] = aws.StringValue(table.RouteTableId)
			}
		}
	}
	return spec, nil
}

func (p *AWSProvider) findVPC() (string, error) {
	// provided by the user
	if p.vpcID != "" {
		return p.vpcID, nil
	}

	vpcs, err := p.getVpcID()
	p.logger.Debugf("%s: vpcs(%d)", p, len(vpcs))
	if err != nil {
		return "", err
	}

	if len(vpcs) == 1 {
		return aws.StringValue(vpcs[0].VpcId), nil
	}

	for _, vpc := range vpcs {
		if aws.BoolValue(vpc.IsDefault) {
			return aws.StringValue(vpc.VpcId), nil
		}
	}

	return "", fmt.Errorf("VPC not found")
}

type stackSpec struct {
	vpcID                      string
	internetGatewayID          string
	tableID                    map[string]string
	timeoutInMinutes           uint
	template                   string
	stackTerminationProtection bool
	tags                       []*cloudformation.Tag
}

func (p *AWSProvider) generateTemplate(configs map[provider.Resource]map[string]*net.IPNet) string {
	template := cft.NewTemplate()
	template.Description = "Static Egress Stack"
	template.Outputs = map[string]*cft.Output{}
	template.Parameters["VPCIDParameter"] = &cft.Parameter{
		Description: "VPC ID",
		Type:        "AWS::EC2::VPC::Id",
	}
	template.Parameters["InternetGatewayIDParameter"] = &cft.Parameter{
		Description: "Internet Gateway ID",
		Type:        "String",
	}

	nets := generateRoutes(configs)

	for i, net := range nets {
		template.Parameters[fmt.Sprintf("DestinationCidrBlock%d", i+1)] = &cft.Parameter{
			Description: fmt.Sprintf("Destination CIDR Block %d", i+1),
			Type:        "String",
			Default:     net,
		}
	}

	for i := 1; i <= len(p.availabilityZones); i++ {
		template.Parameters[fmt.Sprintf("AZ%dRouteTableIDParameter", i)] = &cft.Parameter{
			Description: fmt.Sprintf(
				"Route Table ID Availability Zone %d", i),
			Type: "String",
		}
		template.AddResource(fmt.Sprintf("NATGateway%d", i), &cft.EC2NatGateway{
			SubnetId: cft.Ref(
				fmt.Sprintf("NATSubnet%d", i)).String(),
			AllocationId: cft.GetAtt(
				fmt.Sprintf("EIP%d", i), "AllocationId"),
		})

		template.AddResource(fmt.Sprintf("EIP%d", i), &cft.EC2EIP{
			Domain: cft.String("vpc"),
		})
		template.Outputs[fmt.Sprintf("EIP%d", i)] = &cft.Output{
			Description: fmt.Sprintf("external IP of the NATGateway%d", i),
			Value:       cft.Ref(fmt.Sprintf("EIP%d", i)),
		}

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
			Tags: []cft.ResourceTag{
				cft.ResourceTag{
					Key: cft.String("Name"),
					Value: cft.String(
						fmt.Sprintf("nat-%s", p.availabilityZones[i-1])),
				},
			},
		})
	}

	for j, cidrEntry := range nets {
		cleanCidrEntry := strings.Replace(cidrEntry, "/", "y", -1)
		cleanCidrEntry = strings.Replace(cleanCidrEntry, ".", "x", -1)
		for i := 1; i <= len(p.availabilityZones); i++ {
			p.logger.Debugf("RouteToNAT%dz%s", i, cleanCidrEntry)
			template.AddResource(fmt.Sprintf("RouteToNAT%dz%s", i, cleanCidrEntry), &cft.EC2Route{
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

func isDoesNotExistsErr(err error) bool {
	if awsErr, ok := err.(awserr.Error); ok {
		if awsErr.Code() == "ValidationError" && strings.Contains(awsErr.Message(), "does not exist") {
			// we wanted to delete a stack and it does not exist (or was removed while we were waiting, we can hide the error)
			return true
		}
	}
	return false
}

func (p *AWSProvider) deleteCFStack() error {
	if p.dry {
		p.logger.Debugf("%s: Stack to delete: %s", p, stackName)
		return nil
	}

	if p.stackTerminationProtection {
		// make sure to disable stack termination protection
		termParams := &cloudformation.UpdateTerminationProtectionInput{
			StackName:                   aws.String(stackName),
			EnableTerminationProtection: aws.Bool(false),
		}

		_, err := p.cloudformation.UpdateTerminationProtection(termParams)
		if err != nil {
			return err
		}
	}

	params := &cloudformation.DeleteStackInput{StackName: aws.String(stackName)}
	_, err := p.cloudformation.DeleteStack(params)
	if err != nil {
		if isDoesNotExistsErr(err) {
			return nil
		}
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), maxStackWaitTimeout)
	defer cancel()

	err = p.waitForStack(ctx, stackStatusCheckInterval, stackName)
	if err != nil {
		if isDoesNotExistsErr(err) {
			return nil
		}
		return err
	}
	return nil
}

func (p *AWSProvider) updateCFStack(spec *stackSpec) error {
	params := &cloudformation.UpdateStackInput{
		StackName: aws.String(stackName),
		Parameters: []*cloudformation.Parameter{
			cfParam(parameterVPCIDParameter, spec.vpcID),
			cfParam(parameterInternetGatewayIDParameter, spec.internetGatewayID),
		},
		TemplateBody: aws.String(spec.template),
		Tags:         spec.tags,
	}
	for i, az := range p.availabilityZones {
		params.Parameters = append(params.Parameters,
			cfParam(
				fmt.Sprintf("AZ%dRouteTableIDParameter", i+1),
				spec.tableID[az]))
	}
	if !p.dry {
		// ensure the stack termination protection is set
		if spec.stackTerminationProtection {
			termParams := &cloudformation.UpdateTerminationProtectionInput{
				StackName:                   aws.String(stackName),
				EnableTerminationProtection: aws.Bool(spec.stackTerminationProtection),
			}

			_, err := p.cloudformation.UpdateTerminationProtection(termParams)
			if err != nil {
				return err
			}
		}

		_, err := p.cloudformation.UpdateStack(params)
		if err != nil {
			if awsErr, ok := err.(awserr.Error); ok {
				if awsErr.Code() == "AlreadyExistsException" {
					err = provider.NewAlreadyExistsError(fmt.Sprintf("%s AlreadyExists", stackName))
				}
			}
			return err
		}

		ctx, cancel := context.WithTimeout(context.Background(), maxStackWaitTimeout)
		defer cancel()
		return p.waitForStack(ctx, stackStatusCheckInterval, stackName)
	}

	p.logger.Debugf("%s: DRY: Stack to update: %s", p, params)
	p.logger.Debugln(aws.StringValue(params.TemplateBody))
	return nil
}

func (p *AWSProvider) createCFStack(spec *stackSpec) error {
	params := &cloudformation.CreateStackInput{
		StackName: aws.String(stackName),
		OnFailure: aws.String(cloudformation.OnFailureDelete),
		Parameters: []*cloudformation.Parameter{
			cfParam(parameterVPCIDParameter, spec.vpcID),
			cfParam(parameterInternetGatewayIDParameter, spec.internetGatewayID),
		},
		TemplateBody:                aws.String(spec.template),
		TimeoutInMinutes:            aws.Int64(int64(spec.timeoutInMinutes)),
		EnableTerminationProtection: aws.Bool(spec.stackTerminationProtection),
		Tags:                        spec.tags,
	}
	for i, az := range p.availabilityZones {
		params.Parameters = append(params.Parameters,
			cfParam(
				fmt.Sprintf("AZ%dRouteTableIDParameter", i+1),
				spec.tableID[az]))
	}
	if !p.dry {
		_, err := p.cloudformation.CreateStack(params)
		if err != nil {
			if awsErr, ok := err.(awserr.Error); ok {
				if strings.Contains(awsErr.Message(), "does not exist") {
					err = provider.NewDoesNotExistError(fmt.Sprintf("%s does not exist", stackName))
				} else if awsErr.Code() == "AlreadyExistsException" {
					err = provider.NewAlreadyExistsError(fmt.Sprintf("%s AlreadyExists", stackName))
				}
			}
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), maxStackWaitTimeout)
		defer cancel()
		return p.waitForStack(ctx, stackStatusCheckInterval, stackName)
	}
	p.logger.Debugf("%s: DRY: Stack to create: %s", p, params)
	p.logger.Debugln(aws.StringValue(params.TemplateBody))
	return nil

}

func (p *AWSProvider) getStackByName(stackName string) (*cloudformation.Stack, error) {
	params := &cloudformation.DescribeStacksInput{
		StackName: aws.String(stackName),
	}
	resp, err := p.cloudformation.DescribeStacks(params)
	if err != nil {
		return nil, err
	}
	//we expect only one stack
	if len(resp.Stacks) != 1 {
		return nil, fmt.Errorf("unexpected response, got %d, expected 1 stack", len(resp.Stacks))
	}
	return resp.Stacks[0], nil
}

func (p *AWSProvider) waitForStack(ctx context.Context, waitTime time.Duration, stackName string) error {
	for {
		stack, err := p.getStackByName(stackName)
		if err != nil {
			return err
		}
		switch aws.StringValue(stack.StackStatus) {
		case cloudformation.StackStatusUpdateComplete:
			return nil
		case cloudformation.StackStatusCreateComplete:
			return nil
		case cloudformation.StackStatusDeleteComplete:
			return nil
		case cloudformation.StackStatusCreateFailed:
			return errCreateFailed
		case cloudformation.StackStatusDeleteFailed:
			return errDeleteFailed
		case cloudformation.StackStatusRollbackComplete:
			return errRollbackComplete
		case cloudformation.StackStatusRollbackFailed:
			return errRollbackFailed
		case cloudformation.StackStatusUpdateRollbackComplete:
			return errUpdateRollbackComplete
		case cloudformation.StackStatusUpdateRollbackFailed:
			return errUpdateRollbackFailed
		}
		p.logger.Debugf("Stack '%s' - [%s]", stackName, aws.StringValue(stack.StackStatus))

		select {
		case <-ctx.Done():
			return errTimeoutExceeded
		case <-time.After(waitTime):
		}
	}
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

func (p *AWSProvider) getInternetGatewayId(vpcID string) ([]*ec2.InternetGateway, error) {
	params := &ec2.DescribeInternetGatewaysInput{
		Filters: []*ec2.Filter{
			{
				Name: aws.String("attachment.vpc-id"),
				Values: []*string{
					aws.String(vpcID),
				},
			},
		},
	}
	resp, err := p.ec2.DescribeInternetGateways(params)
	if err != nil {
		return nil, err
	}
	return resp.InternetGateways, nil
}

func (p *AWSProvider) getVpcID() ([]*ec2.Vpc, error) {
	params := &ec2.DescribeVpcsInput{}
	resp, err := p.ec2.DescribeVpcs(params)
	if err != nil {
		return nil, err
	}
	return resp.Vpcs, nil
}

func (p *AWSProvider) getRouteTables(vpcID string) ([]*ec2.RouteTable, error) {
	params := &ec2.DescribeRouteTablesInput{
		Filters: []*ec2.Filter{
			{
				Name: aws.String("vpc-id"),
				Values: []*string{
					aws.String(vpcID),
				},
			},
			{
				Name: aws.String("tag:Type"),
				Values: []*string{
					aws.String(tagDefaultTypeValueRouteTableID),
				},
			},
		},
	}
	resp, err := p.ec2.DescribeRouteTables(params)
	if err != nil {
		return nil, err
	}
	return resp.RouteTables, nil
}
