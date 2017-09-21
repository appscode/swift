# swift
[swift by AppsCode](https://github.com/appscode/swift) - Ajax friendly Helm Tiller Proxy
## TL;DR;

```console
$ helm install chart/swift
```

## Introduction

This chart bootstraps a [Helm Tiller Proxy](https://github.com/appscode/swift) deployment on a [Kubernetes](http://kubernetes.io) cluster using the [Helm](https://helm.sh) package manager.

## Prerequisites

- Kubernetes 1.5+

## Installing the Chart
To install the chart with the release name `my-release`:
```console
$ helm install chart/swift --name my-release
```
The command deploys Swift proxy on the Kubernetes cluster in the default configuration. The [configuration](#configuration) section lists the parameters that can be configured during installation.

> **Tip**: List all releases using `helm list`

## Uninstalling the Chart

To uninstall/delete the `my-release`:

```console
$ helm delete my-release
```

The command removes all the Kubernetes components associated with the chart and deletes the release.

## Configuration

The following tables lists the configurable parameters of the swift chart and their default values.


| Parameter                 | Description                                                       | Default          |
| --------------------------| ------------------------------------------------------------------| -----------------|
| `replicaCount`            | Number of swift operator replicas to create (only 1 is supported) | `1`              |
| `operator.image`          | operator container image                                          | `appscode/swift` |
| `operator.tag`            | operator container image tag                                      | `0.3.1`          |
| `operator.pullPolicy`     | operator container image pull policy                              | `IfNotPresent`   |
| `rbac.create`             | install required rbac service account, roles and rolebindings     | `false`          |
| `rbac.serviceAccountName` | ServiceAccount swift will use (ignored if rbac.create=true)       | `default`        |


Specify each parameter using the `--set key=value[,key=value]` argument to `helm install`. For example:

```console
$ helm install --name my-release --set image.tag=v0.2.1 chart/swift
```

Alternatively, a YAML file that specifies the values for the parameters can be provided while
installing the chart. For example:

```console
$ helm install --name my-release --values values.yaml chart/swift
```

## RBAC
By default the chart will not install the recommended RBAC roles and rolebindings.

You need to have the flag `--authorization-mode=RBAC` on the api server. See the following document for how to enable [RBAC](https://kubernetes.io/docs/admin/authorization/rbac/).

To determine if your cluster supports RBAC, run the the following command:

```console
$ kubectl api-versions | grep rbac
```

If the output contains "beta", you may install the chart with RBAC enabled (see below).

### Enable RBAC role/rolebinding creation

To enable the creation of RBAC resources (On clusters with RBAC). Do the following:

```console
$ helm install --name my-release chart/swift --set rbac.create=true
```