package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

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
	staticLagacyStackName               = "egress-static-nat"
	parameterVPCIDParameter             = "VPCIDParameter"
	parameterInternetGatewayIDParameter = "InternetGatewayIDParameter"
	tagDefaultAZKeyRouteTableID         = "AvailabilityZone"
	tagDefaultTypeValueRouteTableID     = "dmz" // find route table by "Type" tag = "dmz"
	egressConfigTagPrefix               = "egress-config/"
	clusterIDTagPrefix                  = "kubernetes.io/cluster/"
	kubernetesApplicationTagKey         = "kubernetes:application"
	resourceLifecycleOwned              = "owned"
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
	clusterID                  string
	controllerID               string
	dry                        bool
	vpcID                      string
	natCidrBlocks              []string
	availabilityZones          []string
	cloudformation             cloudformationiface.CloudFormationAPI
	ec2                        ec2iface.EC2API
	stackTerminationProtection bool
	additionalStackTags        map[string]string
	logger                     *log.Entry
}

type stackSpec struct {
	name                       string
	vpcID                      string
	internetGatewayID          string
	tableID                    map[string]string
	timeoutInMinutes           uint
	template                   string
	stackTerminationProtection bool
	tags                       []*cloudformation.Tag
}

func NewAWSProvider(clusterID, controllerID string, dry bool, vpcID string, natCidrBlocks, availabilityZones []string, stackTerminationProtection bool, additionalStackTags map[string]string) *AWSProvider {
	// TODO: find vpcID at startup
	p := defaultConfigProvider()
	return &AWSProvider{
		clusterID:                  clusterID,
		controllerID:               controllerID,
		dry:                        dry,
		vpcID:                      vpcID,
		natCidrBlocks:              natCidrBlocks,
		availabilityZones:          availabilityZones,
		cloudformation:             cloudformation.New(p),
		ec2:                        ec2.New(p),
		stackTerminationProtection: stackTerminationProtection,
		additionalStackTags:        additionalStackTags,
		logger:                     log.WithFields(log.Fields{"provider": ProviderName}),
	}
}

func (p AWSProvider) String() string {
	return ProviderName
}

func (p *AWSProvider) Ensure(configs map[provider.Resource]map[string]*net.IPNet) error {
	stack, err := p.getEgressStack()
	if err != nil {
		return err
	}

	// don't do anything if the stack doesn't exist and the config is empty
	if len(configs) == 0 && stack == nil {
		return nil
	}

	spec, err := p.generateStackSpec(configs)
	if err != nil {
		return errors.Wrap(err, "failed to generate stack spec")
	}

	// create new stack if it doesn't already exists
	if stack == nil {
		p.logger.Infof("Creating CF stack with config: %v", configs)
		err := p.createCFStack(spec)
		if err != nil {
			return errors.Wrap(err, "failed to create CF stack")
		}
		p.logger.Infof("Created CF stack with config: %v", configs)
		return nil
	}

	spec.name = aws.StringValue(stack.StackName)
	if len(configs) == 0 {
		p.logger.Info("Deleting CF stack. No egress configs")
		err := p.deleteCFStack(spec.name)
		if err != nil {
			return err
		}
		p.logger.Info("Deleted CF stack.")
		return nil
	}

	// get stack template body
	templateBody, err := p.getStackTemplateBody(stack)
	if err != nil {
		return err
	}

	storedCIDRs := getCIDRsFromTemplate(templateBody)

	newCIDRs := provider.GenerateRoutes(configs)

	if stringSetEqual(storedCIDRs, newCIDRs) {
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

func stringSetEqual(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}

	for ak := range a {
		if _, ok := b[ak]; !ok {
			return false
		}
	}
	return true
}

// parses CIDRs from the Cloudformation template.
func getCIDRsFromTemplate(template string) map[string]struct{} {
	var cfTemplate struct {
		Resources map[string]struct {
			Type       string
			Properties struct {
				DestinationCidrBlock string
			}
		}
	}
	err := json.Unmarshal([]byte(template), &cfTemplate)
	if err != nil {
		// if we can't parse the template we assume no IP addresses
		// found. This will triggere a recreation/update of the stack.
		return nil
	}

	cidrs := make(map[string]struct{})
	for resourceName, r := range cfTemplate.Resources {
		if strings.HasPrefix(resourceName, "RouteToNAT") {
			// get CIDR from CF resource definition
			if r.Type == "AWS::EC2::Route" {
				cidrs[r.Properties.DestinationCidrBlock] = struct{}{}
			}
		}
	}

	return cidrs
}

func (p *AWSProvider) generateStackSpec(configs map[provider.Resource]map[string]*net.IPNet) (*stackSpec, error) {
	spec := &stackSpec{
		name:                       normalizeStackName(p.clusterID),
		timeoutInMinutes:           10,
		stackTerminationProtection: p.stackTerminationProtection,
	}

	tags := map[string]string{
		clusterIDTagPrefix + p.clusterID: resourceLifecycleOwned,
		kubernetesApplicationTagKey:      p.controllerID,
	}
	spec.tags = tagMapToCloudformationTags(mergeTags(p.additionalStackTags, tags))

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

	tableZoneIndexes := make(map[string]int)
	tableID := make(map[string]string)
	for i, table := range rt {
		zone, ok := routeTableZone(table)
		if !ok {
			continue
		}

		zindex, ok := zoneIndex(p.availabilityZones, zone)
		if !ok {
			return nil, fmt.Errorf(
				"unrecognized availability zone in routing table tags: %s",
				zone,
			)
		}

		paramName := fmt.Sprintf("AZ%dRouteTableIDParameter", i+1)
		tableZoneIndexes[paramName] = zindex
		tableID[paramName] = aws.StringValue(table.RouteTableId)
	}

	spec.template = p.generateTemplate(configs, tableZoneIndexes)
	spec.tableID = tableID
	return spec, nil
}

func routeTableZone(rt *ec2.RouteTable) (string, bool) {
	for _, tag := range rt.Tags {
		if tagDefaultAZKeyRouteTableID == aws.StringValue(tag.Key) {
			return aws.StringValue(tag.Value), true
		}
	}

	return "", false
}

func zoneIndex(zones []string, zone string) (int, bool) {
	for i, z := range zones {
		if z == zone {
			return i, true
		}
	}

	return 0, false
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

func (p *AWSProvider) generateTemplate(configs map[provider.Resource]map[string]*net.IPNet, rts map[string]int) string {
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
		template.Outputs[fmt.Sprintf("EIP%d", i)] = &cft.Output{
			Description: fmt.Sprintf("external IP of the NATGateway%d", i),
			Value:       cft.Ref(fmt.Sprintf("EIP%d", i)),
		}

		template.AddResource(fmt.Sprintf("NATSubnet%d", i), &cft.EC2Subnet{
			CidrBlock:        cft.String(p.natCidrBlocks[i-1]),
			AvailabilityZone: cft.String(p.availabilityZones[i-1]),
			VpcId:            cft.Ref("VPCIDParameter").String(),
			Tags: []cft.ResourceTag{
				{
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
				{
					Key: cft.String("Name"),
					Value: cft.String(
						fmt.Sprintf("nat-%s", p.availabilityZones[i-1])),
				},
			},
		})
	}

	nets := provider.GenerateRoutes(configs)
	for cidrEntry := range nets {
		cleanCidrEntry := strings.Replace(cidrEntry, "/", "y", -1)
		cleanCidrEntry = strings.Replace(cleanCidrEntry, ".", "x", -1)
		counter := 1
		for routeTableParam, zoneIndex := range rts {
			template.Parameters[routeTableParam] = &cft.Parameter{
				Description: fmt.Sprintf("Route Table ID No %d", counter),
				Type:        "String",
			}

			template.AddResource(fmt.Sprintf("RouteToNAT%dz%s", counter, cleanCidrEntry), &cft.EC2Route{
				RouteTableId:         cft.Ref(routeTableParam).String(),
				DestinationCidrBlock: cft.String(cidrEntry),
				NatGatewayId:         cft.Ref(fmt.Sprintf("NATGateway%d", zoneIndex+1)).String(),
			})

			counter++
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

func (p *AWSProvider) deleteCFStack(stackName string) error {
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
		StackName: aws.String(spec.name),
		Parameters: append(
			[]*cloudformation.Parameter{
				cfParam(parameterVPCIDParameter, spec.vpcID),
				cfParam(parameterInternetGatewayIDParameter, spec.internetGatewayID),
			},
			routeTableParams(spec)...,
		),
		TemplateBody: aws.String(spec.template),
		Tags:         spec.tags,
	}

	if !p.dry {
		// ensure the stack termination protection is set
		if spec.stackTerminationProtection {
			termParams := &cloudformation.UpdateTerminationProtectionInput{
				StackName:                   aws.String(spec.name),
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
					err = provider.NewAlreadyExistsError(fmt.Sprintf("%s AlreadyExists", spec.name))
				}
			}
			return err
		}

		ctx, cancel := context.WithTimeout(context.Background(), maxStackWaitTimeout)
		defer cancel()
		return p.waitForStack(ctx, stackStatusCheckInterval, spec.name)
	}

	p.logger.Debugf("%s: DRY: Stack to update: %s", p, params)
	p.logger.Debugln(aws.StringValue(params.TemplateBody))
	return nil
}

func (p *AWSProvider) createCFStack(spec *stackSpec) error {
	params := &cloudformation.CreateStackInput{
		StackName: aws.String(spec.name),
		OnFailure: aws.String(cloudformation.OnFailureDelete),
		Parameters: append(
			[]*cloudformation.Parameter{
				cfParam(parameterVPCIDParameter, spec.vpcID),
				cfParam(parameterInternetGatewayIDParameter, spec.internetGatewayID),
			},
			routeTableParams(spec)...,
		),
		TemplateBody:                aws.String(spec.template),
		TimeoutInMinutes:            aws.Int64(int64(spec.timeoutInMinutes)),
		EnableTerminationProtection: aws.Bool(spec.stackTerminationProtection),
		Tags:                        spec.tags,
	}

	if !p.dry {
		_, err := p.cloudformation.CreateStack(params)
		if err != nil {
			if awsErr, ok := err.(awserr.Error); ok {
				if strings.Contains(awsErr.Message(), "does not exist") {
					err = provider.NewDoesNotExistError(fmt.Sprintf("%s does not exist", spec.name))
				} else if awsErr.Code() == "AlreadyExistsException" {
					err = provider.NewAlreadyExistsError(fmt.Sprintf("%s AlreadyExists", spec.name))
				}
			}
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), maxStackWaitTimeout)
		defer cancel()
		return p.waitForStack(ctx, stackStatusCheckInterval, spec.name)
	}
	p.logger.Debugf("%s: DRY: Stack to create: %s", p, params)
	p.logger.Debugln(aws.StringValue(params.TemplateBody))
	return nil

}

func routeTableParams(s *stackSpec) []*cloudformation.Parameter {
	var params []*cloudformation.Parameter
	for paramName, routeTableID := range s.tableID {
		params = append(params, cfParam(paramName, routeTableID))
	}

	return params
}

func (p *AWSProvider) getStackByName(stackName string) (*cloudformation.Stack, error) {
	params := &cloudformation.DescribeStacksInput{
		StackName: aws.String(stackName),
	}
	resp, err := p.cloudformation.DescribeStacks(params)
	if err != nil {
		return nil, err
	}
	// we expect only one stack
	if len(resp.Stacks) != 1 {
		return nil, fmt.Errorf("unexpected response, got %d, expected 1 stack", len(resp.Stacks))
	}
	return resp.Stacks[0], nil
}

// getEgressStack gets the Egress stack by ClusterID tag or by static stack
// name.
func (p *AWSProvider) getEgressStack() (*cloudformation.Stack, error) {
	tags := map[string]string{
		clusterIDTagPrefix + p.clusterID: resourceLifecycleOwned,
		kubernetesApplicationTagKey:      p.controllerID,
	}

	params := &cloudformation.DescribeStacksInput{}

	var egressStack *cloudformation.Stack
	err := p.cloudformation.DescribeStacksPages(params, func(resp *cloudformation.DescribeStacksOutput, lastPage bool) bool {
		for _, stack := range resp.Stacks {
			if cloudformationHasTags(tags, stack.Tags) || aws.StringValue(stack.StackName) == staticLagacyStackName {
				egressStack = stack
				return false
			}
		}
		return true
	})
	if err != nil {
		return nil, err
	}

	return egressStack, nil
}

func (p *AWSProvider) getStackTemplateBody(stack *cloudformation.Stack) (string, error) {
	tParams := &cloudformation.GetTemplateInput{
		StackName:     stack.StackName,
		TemplateStage: aws.String(cloudformation.TemplateStageOriginal),
	}

	resp, err := p.cloudformation.GetTemplate(tParams)
	if err != nil {
		return "", err
	}

	return aws.StringValue(resp.TemplateBody), nil
}

// cloudformationHasTags returns true if the expected tags are found in the
// tags list.
func cloudformationHasTags(expected map[string]string, tags []*cloudformation.Tag) bool {
	if len(expected) > len(tags) {
		return false
	}

	tagsMap := make(map[string]string, len(tags))
	for _, tag := range tags {
		tagsMap[aws.StringValue(tag.Key)] = aws.StringValue(tag.Value)
	}

	for key, val := range expected {
		if v, ok := tagsMap[key]; !ok || v != val {
			return false
		}
	}
	return true
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

func mergeTags(tags ...map[string]string) map[string]string {
	mergedTags := make(map[string]string)
	for _, tagMap := range tags {
		for k, v := range tagMap {
			mergedTags[k] = v
		}
	}
	return mergedTags
}

func tagMapToCloudformationTags(tags map[string]string) []*cloudformation.Tag {
	cfTags := make([]*cloudformation.Tag, 0, len(tags))
	for k, v := range tags {
		tag := &cloudformation.Tag{
			Key:   aws.String(k),
			Value: aws.String(v),
		}
		cfTags = append(cfTags, tag)
	}
	return cfTags
}
