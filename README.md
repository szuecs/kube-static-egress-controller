# Kube Static Egress Controller

Kube-static-egress-controller provides static IPs for cluster egress
calls to defined target networks.

Kube-static-egress-controller watches Kubernetes Configmaps with Label
selector `egress=static` in all namespaces to get target networks to
route to with static IPs. It changes the infrastructure this to
provide to the cluster.

## Deployment

- we use the default VPC (found via API call ec2.DescribeVpcs)
- we use internetGW attached to the found VPC
- we get routeTables via filter vpcid and --tag-key=AvailabilityZone. The Tag value will be the routeTableID of your routeTable. This tag has to be specified by the user for each dmz routing table
- --aws-nat-cidr-block=172.31.64.0/28 is used as Subnet, you have to
  have the same number of Subnets as you use AZs to apply to your NAT GWs
- --aws-az=eu-central-1a is used to create NAT GW in

- [ ] find out who sets Name=dmz-eu-central-1a in routeTable Tag

## Provider

### AWS

Creates, updates and deletes infrastructure using CloudFormation.
The infrastructure it manages is AWS::EC2::RouteTable AWS::EC2::Route AWS::EC2::NatGateway  	AWS::EC2::SubnetRouteTableAssociation AWS::EC2::EIP AWS::EC2::Subnet

### Inmemory

Used for testing only
