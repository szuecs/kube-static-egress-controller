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

## How it works

1. watch configmap by label selector and send an event with the Egress
   configuration to the controller loop.
2. Store a cache of all Egress configurations observed in the cluster.
3. Pass the stored cache to the provider to ensure the configuration is
   applied.

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

It uses your default VPC (call ec2.DescribeVpcs) and derives from it
the InternetGateway.  It gets routeTables via filter vpcid and
--tag-key=AvailabilityZone. The Tag value will be the routeTableID of
your routeTable. This tag has to be specified by the user for each
routing table in order to change the routes for the worker nodes.

- --aws-nat-cidr-block=172.15.64.0/28 is used as Subnet, you have to
  have the same number of Subnets as you use AZs to apply to your NAT GWs
- --aws-az=eu-west-1a is used to create NAT GW and EIP in the specified AZ

#### IAM role / Policy

The IAM role attached to your POD has to have the following policy:

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

#### Deployment

Replace `iam.amazonaws.com/role` with your role created to assign the
policy from above. You should also run
[kube2iam](https://github.com/jtblin/kube2iam) on that node to make
the annotation work.

    apiVersion: apps/v1beta1
    kind: Deployment
    metadata:
      name: kube-static-egress-controller
      namespace: kube-system
    spec:
      replicas: 1
      template:
        metadata:
          annotations:
            iam.amazonaws.com/role: "static-egress-controller-role"
        spec:
          containers:
          - name: controller
            image: registry.opensource.zalan.do/teapot/kube-static-egress-controller:latest
            args:
            - "--log-level=debug"
            - "--provider=aws"
            - "--aws-nat-cidr-block=172.15.64.0/28"
            - "--aws-nat-cidr-block=172.15.64.16/28"
            - "--aws-nat-cidr-block=172.15.64.32/28"
            - "--aws-az=eu-west-1a"
            - "--aws-az=eu-west-1b"
            - "--aws-az=eu-west-1c"
            env:
            - name: AWS_REGION
              value: eu-west-1
            resources:
              limits:
                cpu: 100m
                memory: 200Mi
              requests:
                cpu: 5m
                memory: 25Mi


### Inmemory

Used for testing only
