package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	cft "github.com/crewjam/go-cloudformation"
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
	kubernetesApplicationTagKey         = "kubernetes:application"
	resourceLifecycleOwned              = "owned"
	maxStackWaitTimeout                 = 15 * time.Minute
	stackStatusCheckInterval            = 15 * time.Second
)

var (
	errCreateFailed           = fmt.Errorf("wait for stack failed with %s", cftypes.StackStatusCreateFailed)
	errRollbackComplete       = fmt.Errorf("wait for stack failed with %s", cftypes.StackStatusRollbackComplete)
	errUpdateRollbackComplete = fmt.Errorf("wait for stack failed with %s", cftypes.StackStatusUpdateRollbackComplete)
	errRollbackFailed         = fmt.Errorf("wait for stack failed with %s", cftypes.StackStatusRollbackFailed)
	errUpdateRollbackFailed   = fmt.Errorf("wait for stack failed with %s", cftypes.StackStatusUpdateRollbackFailed)
	errDeleteFailed           = fmt.Errorf("wait for stack failed with %s", cftypes.StackStatusDeleteFailed)
	errTimeoutExceeded        = fmt.Errorf("wait for stack timeout exceeded")
)

type AWSProvider struct {
	clusterID                  string
	clusterIDTagPrefix         string
	controllerID               string
	dry                        bool
	vpcID                      string
	natCidrBlocks              []string
	availabilityZones          []string
	cloudformation             cloudformationAPI
	ec2                        ec2API
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
	tags                       []cftypes.Tag
}

func NewAWSProvider(cfg aws.Config, clusterID, controllerID string, dry bool, vpcID string, clusterIDTagPrefix string, natCidrBlocks, availabilityZones []string, stackTerminationProtection bool, additionalStackTags map[string]string) (*AWSProvider, error) {
	// TODO: find vpcID at startup
	return &AWSProvider{
		clusterID:                  clusterID,
		clusterIDTagPrefix:         clusterIDTagPrefix,
		controllerID:               controllerID,
		dry:                        dry,
		vpcID:                      vpcID,
		natCidrBlocks:              natCidrBlocks,
		availabilityZones:          availabilityZones,
		cloudformation:             cloudformation.NewFromConfig(cfg),
		ec2:                        ec2.NewFromConfig(cfg),
		stackTerminationProtection: stackTerminationProtection,
		additionalStackTags:        additionalStackTags,
		logger:                     log.WithFields(log.Fields{"provider": ProviderName}),
	}, nil
}

func (p AWSProvider) String() string {
	return ProviderName
}

func (p *AWSProvider) Ensure(ctx context.Context, configs map[provider.Resource]map[string]*net.IPNet) error {
	stack, err := p.getEgressStack(ctx)
	if err != nil {
		return err
	}

	// don't do anything if the stack doesn't exist and the config is empty
	if len(configs) == 0 && stack.StackName == nil {
		return nil
	}

	spec, err := p.generateStackSpec(ctx, configs)
	if err != nil {
		return errors.Wrap(err, "failed to generate stack spec")
	}

	// create new stack if it doesn't already exists
	if stack.StackName == nil {
		p.logger.Infof("Creating CF stack with config: %v", configs)
		err := p.createCFStack(ctx, spec)
		if err != nil {
			return errors.Wrap(err, "failed to create CF stack")
		}
		p.logger.Infof("Created CF stack with config: %v", configs)
		return nil
	}

	spec.name = aws.ToString(stack.StackName)
	if len(configs) == 0 {
		p.logger.Info("Deleting CF stack. No egress configs")
		err := p.deleteCFStack(ctx, spec.name)
		if err != nil {
			return err
		}
		p.logger.Info("Deleted CF stack.")
		return nil
	}

	// get stack template body
	templateBody, err := p.getStackTemplateBody(ctx, stack)
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
	err = p.updateCFStack(ctx, spec)
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

func findTagByKey(tags []ec2types.Tag, key string) string {
	for _, t := range tags {
		if aws.ToString(t.Key) == key {
			return aws.ToString(t.Value)
		}
	}

	return ""
}

func (p *AWSProvider) generateStackSpec(ctx context.Context, configs map[provider.Resource]map[string]*net.IPNet) (*stackSpec, error) {
	spec := &stackSpec{
		name:                       normalizeStackName(p.clusterID),
		timeoutInMinutes:           10,
		stackTerminationProtection: p.stackTerminationProtection,
	}

	tags := map[string]string{
		p.clusterIDTagPrefix + p.clusterID: resourceLifecycleOwned,
		kubernetesApplicationTagKey:        p.controllerID,
	}
	spec.tags = tagMapToCloudformationTags(mergeTags(p.additionalStackTags, tags))

	vpcID, err := p.findVPC(ctx)
	if err != nil {
		return nil, err
	}
	spec.vpcID = vpcID

	// get assigned internet gateway
	igw, err := p.getInternetGatewayId(ctx, spec.vpcID)
	p.logger.Debugf("%s: igw(%d)", p, len(igw))
	if err != nil {
		return nil, err
	}

	if len(igw) == 0 {
		return nil, fmt.Errorf("no Internet Gateways found")
	}

	// get first internet gateway ID
	igwID := aws.ToString(igw[0].InternetGatewayId)
	spec.internetGatewayID = igwID

	// get route tables
	rt, err := p.getRouteTables(ctx, spec.vpcID)
	p.logger.Debugf("%s: rt(%d)", p, len(rt))
	if err != nil {
		return nil, err
	}

	// [supporting multiple routing tables]
	// as a migration step, in order to preserve the current indexes CloudFormation template names of the
	// route-to-nat resources, we need to order them first, they are only identifiable by their standard
	// name: dmz-eu-central-1a.
	sort.SliceStable(rt, func(i, j int) bool {
		rti, rtj := rt[i], rt[j]

		zonei, ok := routeTableZone(rti)
		if !ok {
			return false
		}

		zonej, ok := routeTableZone(rtj)
		if !ok {
			return true
		}

		namei := findTagByKey(rti.Tags, "Name")
		namej := findTagByKey(rtj.Tags, "Name")
		standardi := namei == fmt.Sprintf("%s-%s", tagDefaultTypeValueRouteTableID, zonei)
		standardj := namej == fmt.Sprintf("%s-%s", tagDefaultTypeValueRouteTableID, zonej)

		if !standardi {
			return false
		}

		if standardi && !standardj {
			return true
		}

		zoneIndexi, ok := zoneIndex(p.availabilityZones, zonei)
		if !ok {
			return false
		}

		zoneIndexj, ok := zoneIndex(p.availabilityZones, zonej)
		if !ok {
			return true
		}

		return zoneIndexi < zoneIndexj
	})

	var paramOrder []string
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
		paramOrder = append(paramOrder, paramName)
		tableZoneIndexes[paramName] = zindex
		tableID[paramName] = aws.ToString(table.RouteTableId)
	}

	spec.template = p.generateTemplate(configs, paramOrder, tableZoneIndexes)
	spec.tableID = tableID
	return spec, nil
}

func routeTableZone(rt ec2types.RouteTable) (string, bool) {
	for _, tag := range rt.Tags {
		if tagDefaultAZKeyRouteTableID == aws.ToString(tag.Key) {
			return aws.ToString(tag.Value), true
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

func (p *AWSProvider) findVPC(ctx context.Context) (string, error) {
	// provided by the user
	if p.vpcID != "" {
		return p.vpcID, nil
	}

	vpcs, err := p.getVpcID(ctx)
	p.logger.Debugf("%s: vpcs(%d)", p, len(vpcs))
	if err != nil {
		return "", err
	}

	if len(vpcs) == 1 {
		return aws.ToString(vpcs[0].VpcId), nil
	}

	for _, vpc := range vpcs {
		if aws.ToBool(vpc.IsDefault) {
			return aws.ToString(vpc.VpcId), nil
		}
	}

	return "", fmt.Errorf("VPC not found")
}

func (p *AWSProvider) generateTemplate(
	configs map[provider.Resource]map[string]*net.IPNet,
	routeTableParamOrder []string,
	routeTableZoneIndexes map[string]int,
) string {
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
			SubnetID: cft.Ref(
				fmt.Sprintf("NATSubnet%d", i)).String(),
			AllocationID: cft.GetAtt(
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
			VPCID:            cft.Ref("VPCIDParameter").String(),
			Tags: &cft.TagList{
				{
					Key: cft.String("Name"),
					Value: cft.String(
						fmt.Sprintf("nat-%s", p.availabilityZones[i-1])),
				},
			},
		})
		template.AddResource(fmt.Sprintf("NATSubnetRoute%d", i), &cft.EC2Route{
			RouteTableID: cft.Ref(
				fmt.Sprintf("NATSubnetRouteTable%d", i)).String(),
			DestinationCidrBlock: cft.String("0.0.0.0/0"),
			GatewayID:            cft.Ref("InternetGatewayIDParameter").String(),
		})
		template.AddResource(fmt.Sprintf("NATSubnetRouteTableAssociation%d", i), &cft.EC2SubnetRouteTableAssociation{
			RouteTableID: cft.Ref(
				fmt.Sprintf("NATSubnetRouteTable%d", i)).String(),
			SubnetID: cft.Ref(
				fmt.Sprintf("NATSubnet%d", i)).String(),
		})
		template.AddResource(fmt.Sprintf("NATSubnetRouteTable%d", i), &cft.EC2RouteTable{
			VPCID: cft.Ref("VPCIDParameter").String(),
			Tags: &cft.TagList{
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
		for i, routeTableParam := range routeTableParamOrder {
			template.Parameters[routeTableParam] = &cft.Parameter{
				Description: fmt.Sprintf("Route Table ID No %d", i+1),
				Type:        "String",
			}

			template.AddResource(fmt.Sprintf("RouteToNAT%dz%s", i+1, cleanCidrEntry), &cft.EC2Route{
				RouteTableID:         cft.Ref(routeTableParam).String(),
				DestinationCidrBlock: cft.String(cidrEntry),
				NatGatewayID: cft.Ref(fmt.Sprintf(
					"NATGateway%d",
					routeTableZoneIndexes[routeTableParam]+1,
				)).String(),
			})
		}
	}

	stack, _ := json.Marshal(template)
	return string(stack)
}

func isDoesNotExistsErr(err error) bool {
	if smithyErr, ok := err.(*smithy.OperationError); ok {
		if respErr, ok := smithyErr.Err.(*http.ResponseError); ok {
			if apiErr, ok := respErr.Err.(*smithy.GenericAPIError); ok {
				if apiErr.Code == "ValidationError" && strings.Contains(apiErr.Message, "does not exist") {
					return true
				}
			}
		}
	}
	return false
}

func (p *AWSProvider) deleteCFStack(ctx context.Context, stackName string) error {
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

		_, err := p.cloudformation.UpdateTerminationProtection(ctx, termParams)
		if err != nil {
			return err
		}
	}

	params := &cloudformation.DeleteStackInput{StackName: aws.String(stackName)}
	_, err := p.cloudformation.DeleteStack(ctx, params)
	if err != nil {
		if isDoesNotExistsErr(err) {
			return nil
		}
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, maxStackWaitTimeout)
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

func (p *AWSProvider) updateCFStack(ctx context.Context, spec *stackSpec) error {
	params := &cloudformation.UpdateStackInput{
		StackName: aws.String(spec.name),
		Parameters: append(
			[]cftypes.Parameter{
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

			_, err := p.cloudformation.UpdateTerminationProtection(ctx, termParams)
			if err != nil {
				return err
			}
		}

		_, err := p.cloudformation.UpdateStack(ctx, params)
		if err != nil {
			if isDoesNotExistsErr(err) {
				return provider.NewDoesNotExistError(fmt.Sprintf("Stack '%s' does not exist", spec.name))
			}
			return err
		}

		ctx, cancel := context.WithTimeout(ctx, maxStackWaitTimeout)
		defer cancel()
		return p.waitForStack(ctx, stackStatusCheckInterval, spec.name)
	}

	p.logger.Debugf("%s: DRY: Stack to update: %v", p, params)
	p.logger.Debugln(aws.ToString(params.TemplateBody))
	return nil
}

func (p *AWSProvider) createCFStack(ctx context.Context, spec *stackSpec) error {
	params := &cloudformation.CreateStackInput{
		StackName: aws.String(spec.name),
		OnFailure: cftypes.OnFailureDelete,
		Parameters: append(
			[]cftypes.Parameter{
				cfParam(parameterVPCIDParameter, spec.vpcID),
				cfParam(parameterInternetGatewayIDParameter, spec.internetGatewayID),
			},
			routeTableParams(spec)...,
		),
		TemplateBody:                aws.String(spec.template),
		TimeoutInMinutes:            aws.Int32(int32(spec.timeoutInMinutes)),
		EnableTerminationProtection: aws.Bool(spec.stackTerminationProtection),
		Tags:                        spec.tags,
	}

	if !p.dry {
		_, err := p.cloudformation.CreateStack(ctx, params)
		if err != nil {
			var aer *cftypes.AlreadyExistsException
			if errors.As(err, &aer) {
				err = provider.NewAlreadyExistsError(fmt.Sprintf("%s AlreadyExists", spec.name))
			}
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), maxStackWaitTimeout)
		defer cancel()
		return p.waitForStack(ctx, stackStatusCheckInterval, spec.name)
	}
	p.logger.Debugf("%s: DRY: Stack to create: %v", p, params)
	p.logger.Debugln(aws.ToString(params.TemplateBody))
	return nil

}

func routeTableParams(s *stackSpec) []cftypes.Parameter {
	var params []cftypes.Parameter
	for paramName, routeTableID := range s.tableID {
		params = append(params, cfParam(paramName, routeTableID))
	}

	return params
}

func (p *AWSProvider) getStackByName(ctx context.Context, stackName string) (cftypes.Stack, error) {
	params := &cloudformation.DescribeStacksInput{
		StackName: aws.String(stackName),
	}
	resp, err := p.cloudformation.DescribeStacks(ctx, params)
	if err != nil {
		return cftypes.Stack{}, err
	}
	// we expect only one stack
	if len(resp.Stacks) != 1 {
		return cftypes.Stack{}, fmt.Errorf("unexpected response, got %d, expected 1 stack", len(resp.Stacks))
	}
	return resp.Stacks[0], nil
}

// getEgressStack gets the Egress stack by ClusterID tag or by static stack
// name.
func (p *AWSProvider) getEgressStack(ctx context.Context) (cftypes.Stack, error) {
	tags := map[string]string{
		p.clusterIDTagPrefix + p.clusterID: resourceLifecycleOwned,
		kubernetesApplicationTagKey:        p.controllerID,
	}

	params := &cloudformation.DescribeStacksInput{}
	paginator := cloudformation.NewDescribeStacksPaginator(p.cloudformation, params)

	var egressStack cftypes.Stack
	for paginator.HasMorePages() {
		resp, err := paginator.NextPage(ctx)
		if err != nil {
			return cftypes.Stack{}, err
		}

		for _, stack := range resp.Stacks {
			if cloudformationHasTags(tags, stack.Tags) || aws.ToString(stack.StackName) == staticLagacyStackName {
				egressStack = stack
				break
			}
		}
	}

	return egressStack, nil
}

func (p *AWSProvider) getStackTemplateBody(ctx context.Context, stack cftypes.Stack) (string, error) {
	tParams := &cloudformation.GetTemplateInput{
		StackName:     stack.StackName,
		TemplateStage: cftypes.TemplateStageOriginal,
	}

	resp, err := p.cloudformation.GetTemplate(ctx, tParams)
	if err != nil {
		return "", err
	}

	return aws.ToString(resp.TemplateBody), nil
}

// cloudformationHasTags returns true if the expected tags are found in the
// tags list.
func cloudformationHasTags(expected map[string]string, tags []cftypes.Tag) bool {
	if len(expected) > len(tags) {
		return false
	}

	tagsMap := make(map[string]string, len(tags))
	for _, tag := range tags {
		tagsMap[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
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
		stack, err := p.getStackByName(ctx, stackName)
		if err != nil {
			return err
		}
		switch stack.StackStatus {
		case cftypes.StackStatusUpdateComplete:
			return nil
		case cftypes.StackStatusCreateComplete:
			return nil
		case cftypes.StackStatusDeleteComplete:
			return nil
		case cftypes.StackStatusCreateFailed:
			return errCreateFailed
		case cftypes.StackStatusDeleteFailed:
			return errDeleteFailed
		case cftypes.StackStatusRollbackComplete:
			return errRollbackComplete
		case cftypes.StackStatusRollbackFailed:
			return errRollbackFailed
		case cftypes.StackStatusUpdateRollbackComplete:
			return errUpdateRollbackComplete
		case cftypes.StackStatusUpdateRollbackFailed:
			return errUpdateRollbackFailed
		}
		p.logger.Debugf("Stack '%s' - [%s]", stackName, stack.StackStatus)

		select {
		case <-ctx.Done():
			return errTimeoutExceeded
		case <-time.After(waitTime):
		}
	}
}

func cfParam(key, value string) cftypes.Parameter {
	return cftypes.Parameter{
		ParameterKey:   aws.String(key),
		ParameterValue: aws.String(value),
	}
}

func (p *AWSProvider) getInternetGatewayId(ctx context.Context, vpcID string) ([]ec2types.InternetGateway, error) {
	params := &ec2.DescribeInternetGatewaysInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("attachment.vpc-id"),
				Values: []string{vpcID},
			},
		},
	}
	resp, err := p.ec2.DescribeInternetGateways(ctx, params)
	if err != nil {
		return nil, err
	}
	return resp.InternetGateways, nil
}

func (p *AWSProvider) getVpcID(ctx context.Context) ([]ec2types.Vpc, error) {
	params := &ec2.DescribeVpcsInput{}
	resp, err := p.ec2.DescribeVpcs(ctx, params)
	if err != nil {
		return nil, err
	}
	return resp.Vpcs, nil
}

func (p *AWSProvider) getRouteTables(ctx context.Context, vpcID string) ([]ec2types.RouteTable, error) {
	params := &ec2.DescribeRouteTablesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{vpcID},
			},
			{
				Name:   aws.String("tag:Type"),
				Values: []string{tagDefaultTypeValueRouteTableID},
			},
		},
	}
	resp, err := p.ec2.DescribeRouteTables(ctx, params)
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

func tagMapToCloudformationTags(tags map[string]string) []cftypes.Tag {
	cfTags := make([]cftypes.Tag, 0, len(tags))
	for k, v := range tags {
		tag := cftypes.Tag{
			Key:   aws.String(k),
			Value: aws.String(v),
		}
		cfTags = append(cfTags, tag)
	}
	return cfTags
}
