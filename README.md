# Kube Static Egress Controller

Kube-static-egress-controller provides static IPs for cluster egress
calls to defined target networks.

Kube-static-egress-controller watches Kubernetes Configmaps with Label
selector `egress=static` in all namespaces to get target networks to
route to with static IPs. It changes the infrastructure this to
provide to the cluster.

This project enables teams to have static IPs for egress traffic to
user defined target network. Deployers can enable this by creating a
configmap with label `egress=static` in any namespace and as many
configmaps they want. They can choose if this should be part of their
deployment or a global configuration. You can also choose to select a
namespace to limit the usage to a given namespace.


## Deployment

TODO

## How it works

TODO

- we use the default VPC (found via API call ec2.DescribeVpcs)
- we use internetGW attached to the found VPC
- we get routeTables via filter vpcid and --tag-key=AvailabilityZone. The Tag value will be the routeTableID of your routeTable. This tag has to be specified by the user for each dmz routing table
- --aws-nat-cidr-block=172.31.64.0/28 is used as Subnet, you have to
  have the same number of Subnets as you use AZs to apply to your NAT GWs
- --aws-az=eu-central-1a is used to create NAT GW in

- [ ] find out who sets Name=dmz-eu-central-1a in routeTable Tag

## Example

The following example configmap shows how you can specify 2 target
networks that will get routed with static egress IPs. Namespace, name
and data key value pairs can be chosen by the user. Required is that
it is a configmap with labels `egress: static`.

    apiVersion: v1
    kind: ConfigMap
    metadata:
      name: egress-t1
      namespace: default
      labels:
        egress: static
    data:
      service-provider1: 192.112.1.0/21
      google-dns: 8.8.8.8/32


## Provider

### AWS

Creates, updates and deletes infrastructure using CloudFormation. The
infrastructure it manages is:

- AWS::EC2::RouteTable
- AWS::EC2::Route
- AWS::EC2::NatGateway
- AWS::EC2::SubnetRouteTableAssociation
- AWS::EC2::EIP
- AWS::EC2::Subnet

#### Policy

    {
        "Version": "2012-10-17",
        "Statement": [
              {
                "Action": "ec2:DisassociateRouteTable",
                "Effect": "Allow",
                "Resource": "*"
              },
              {
                "Action": "ec2:DescribeNatGateways",
                "Effect": "Allow",
                "Resource": "*"
              },
              {
                "Action": "ec2:CreateTags",
                "Effect": "Allow",
                "Resource": "*"
              },
              {
                "Action": "ec2:ReleaseAddress",
                "Effect": "Allow",
                "Resource": "*"
              },
              {
                "Action": "ec2:DescribeAddresses",
                "Effect": "Allow",
                "Resource": "*"
              },
              {
                "Action": "ec2:DescribeSubnets",
                "Effect": "Allow",
                "Resource": "*"
              },
              {
                "Action": "ec2:AllocateAddress",
                "Effect": "Allow",
                "Resource": "*"
              },
              {
                "Action": "ec2:DescribeRouteTables",
                "Effect": "Allow",
                "Resource": "*"
              },
              {
                "Action": "ec2:DescribeInternetGateways",
                "Effect": "Allow",
                "Resource": "*"
              },
              {
                "Action": "ec2:DescribeVpcs",
                "Effect": "Allow",
                "Resource": "*"
              },
              {
                "Action": "cloudformation:*",
                "Effect": "Allow",
                "Resource": "*"
              },
              {
                "Action": "ec2:AssociateRouteTable",
                "Effect": "Allow",
                "Resource": "*"
              },
              {
                "Action": "ec2:CreateRouteTable",
                "Effect": "Allow",
                "Resource": "*"
              },
              {
                "Action": "ec2:CreateRoute",
                "Effect": "Allow",
                "Resource": "*"
              },
              {
                "Action": "ec2:CreateNatGateway",
                "Effect": "Allow",
                "Resource": "*"
              },
              {
                "Action": "ec2:CreateSubnet",
                "Effect": "Allow",
                "Resource": "*"
              },

              {
                "Action": "ec2:DeleteRouteTable",
                "Effect": "Allow",
                "Resource": "*"
              },
              {
                "Action": "ec2:DeleteRoute",
                "Effect": "Allow",
                "Resource": "*"
              },
              {
                "Action": "ec2:DeleteNatGateway",
                "Effect": "Allow",
                "Resource": "*"
              },
              {
                "Action": "ec2:DeleteSubnet",
                "Effect": "Allow",
                "Resource": "*"
              }

      ]
    }

### Inmemory

Used for testing only
