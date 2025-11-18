# rollouts-plugin-metric-dynamodb

[![Go Report Card](https://goreportcard.com/badge/github.com/argoproj-labs/rollouts-plugin-metric-dynamodb)](https://goreportcard.com/report/github.com/argoproj-labs/rollouts-plugin-metric-dynamodb)
[![GitHub license](https://img.shields.io/badge/license-MIT-blue.svg)](https://github.com/argoproj-labs/rollouts-plugin-metric-dynamodb/blob/master/LICENSE)

The `rollouts-plugin-metric-dynamodb` is an dynamodb plugin designed for use with the Argo Rollouts plugin system. This plugin enables the integration of dynamodb metrics into Argo Rollouts, allowing for advanced metric analysis and monitoring during application rollouts.

> [!IMPORTANT]
> The dynamodb Metric Plugin relies on aggregation query results to function correctly. Aggregation queries in dynamodb allow for the computation of metrics, such as averages, sums, and counts, over a set of documents. This plugin specifically requires the presence of an aggregation block in the query results to operate. If the query results do not contain an aggregation block, the plugin will be unable to process the data and will not function as intended. Therefore, it is essential to ensure that all queries used with this plugin include the necessary aggregation blocks to enable accurate metric analysis and monitoring.

## Features

- **Metric Integration:** Seamlessly integrates dynamodb metrics with Argo Rollouts.

- **Custom Queries:** Supports custom dynamodb queries for flexible metric retrieval.

- **Error Handling:** Robust error handling to ensure reliable metric collection.

- **Debugging Support:** Provides options for building debug versions and attaching debuggers.

## Build & Debug

To build the plugin, use the following commands:

### Release Build

```bash
make build-rollouts-plugin-metric-dynamodb
```

### Debug Build

```bash
make build-rollouts-plugin-metric-dynamodb-debug
```

### Attaching a debugger to debug build

If using goland you can attach a debugger to the debug build by following the directions https://www.jetbrains.com/help/go/attach-to-running-go-processes-with-debugger.html

You can also do this with many other debuggers as well. Including cli debuggers like delve.

## Using a Metric Plugin

There are two methods of installing and using an argo rollouts plugin. The first method is to mount up the plugin executable
into the rollouts controller container. The second method is to use a HTTP(S) server to host the plugin executable.

### Mounting the plugin executable into the rollouts controller container

There are a few ways to mount the plugin executable into the rollouts controller container. Some of these will depend on your
particular infrastructure. Here are a few methods:

- Using an init container to download the plugin executable
- Using a Kubernetes volume mount with a shared volume such as NFS, EBS, etc.
- Building the plugin into the rollouts controller container

Then you can use setup the configmap to point to the plugin executable. Example:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: argo-rollouts-config
data:
  plugins: |-
    metrics:
    - name: "argoproj-labs/dynamodb-metric-plugin" # name of the plugin uses the name to find this configuration, it must match the name required by the plugin
      location: "file://./my-custom-plugin" # supports http(s):// urls and file://
```

### Using a HTTP(S) server to host the plugin executable

Argo Rollouts supports downloading the plugin executable from a HTTP(S) server. To use this method, you will need to
configure the controller via the `argo-rollouts-config` configmaps `pluginLocation` to an http(s) url. Example:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: argo-rollouts-config
data:
  plugins: |-
    metrics:
    - name: "argoproj-labs/dynamodb-metric-plugin" # name of the plugin uses the name to find this configuration, it must match the name required by the plugin
      location: "https://github.com/argoproj-labs/rollouts-plugin-metric-dynamodb/releases/download/v0.0.1/rollouts-plugin-metric-dynamodb-linux-amd64" # supports http(s):// urls and file://
      sha256: "08f588b1c799a37bbe8d0fc74cc1b1492dd70b2c" #optional sha256 checksum of the plugin executable
```

### Sample Analysis Template

The `successCondition` checks that the error count in the last 5 minutes should be lower than or equal to the error count in the previous 5 minutes.

> [!TIP]
> Note: The fields `successCondition`, `"gte": "now-10m/m"`, `"fixed_interval": "5m"`, and `"Level": "Error"` can be configured by your check actions. For example, if you want to check the **"Warning"** log count over the last 15 minutes, you can adjust these fields accordingly: set `"gte": "now-30m/m"`, `"fixed_interval": "15m"`, and `"Level": "Warning"`.

An example for this sample plugin below.

```
apiVersion: argoproj.io/v1alpha1
kind: AnalysisTemplate
metadata:
  name: success-rate
spec:
  args:
    - name: service-name
  metrics:
    - name: success-rate
      interval: 10s
      successCondition: result[len(result)-1] <= result[len(result)-2]
      failureLimit: 2
      count: 3
      provider:
        plugin:
          argoproj-labs/dynamodb-metric-plugin:
            address: http://localhost:9200/
            username: foo
            password: bar
            insecureSkipVerify: false
            index: sample-index
            query: |
              {
                "size": 0,
                "query": {
                  "range": {
                    "@timestamp": {
                      "gte": "now-10m/m",
                      "lt": "now/m"
                    }
                  }
                },
                "aggs": {
                  "logs_per_5min": {
                    "date_histogram": {
                      "field": "@timestamp",
                      "fixed_interval": "5m"
                    },
                    "aggs": {
                      "error_logs": {
                        "filter": {
                          "term": {
                            "Level": "Error"
                          }
                        }
                      }
                    }
                  }
                }
              }
```

### Sample Analysis Result

dynamodb query should response like below:

```
{
  "took": 2057,
  "timed_out": false,
  "_shards": {
    "total": 1,
    "successful": 1,
    "skipped": 0,
    "failed": 0
  },
  "hits": {
    "total": {
      "value": 10000,
      "relation": "gte"
    },
    "max_score": null,
    "hits": []
  },
  "aggregations": {
    "logs_per_5min": {
      "buckets": [
        {
          "key_as_string": "2024-10-25T14:00:00.000Z",
          "key": 1729864800000,
          "doc_count": 523779,
          "error_logs": {
            "doc_count": 0
          }
        },
        {
          "key_as_string": "2024-10-25T14:05:00.000Z",
          "key": 1729865100000,
          "doc_count": 343716,
          "error_logs": {
            "doc_count": 0
          }
        }
      ]
    }
  }
}
```

## Credit

The development of this plugin was inspired by the [Argo Rollouts Prometheus Metric Plugin](https://github.com/argoproj-labs/rollouts-plugin-metric-sample-prometheus). Leveraging the knowledge and design principles from the Prometheus plugin, this dynamodb Metric Plugin was created to provide similar functionality for dynamodb metrics. The foundational concepts and architecture were adapted to suit the specific requirements and capabilities of dynamodb, ensuring seamless integration and reliable performance within the Argo Rollouts ecosystem.
