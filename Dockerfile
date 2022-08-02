ARG BASE_IMAGE=registry.opensource.zalan.do/library/alpine-3:latest
FROM ${BASE_IMAGE}
MAINTAINER Team Teapot @ Zalando SE <team-teapot@zalando.de>

# add binary
ARG TARGETARCH
ADD build/linux/${TARGETARCH}/kube-static-egress-controller /

ENTRYPOINT ["/kube-static-egress-controller"]
