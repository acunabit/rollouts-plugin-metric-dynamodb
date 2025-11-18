package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/argoproj/argo-rollouts/metricproviders/plugin/rpc"
	"k8s.io/utils/env"

	"github.com/argoproj/argo-rollouts/utils/plugin/types"
	log "github.com/sirupsen/logrus"

	"github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	goPlugin "github.com/hashicorp/go-plugin"
	"github.com/tj/assert"
)

var testHandshake = goPlugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "ARGO_ROLLOUTS_RPC_PLUGIN",
	MagicCookieValue: "metrics",
}

func pluginClient(t *testing.T) (rpc.MetricProviderPlugin, goPlugin.ClientProtocol, func(), chan struct{}) {
	logCtx := *log.WithFields(log.Fields{"plugin-test": "dynamodb"})
	ctx, cancel := context.WithCancel(context.Background())

	rpcPluginImp := &RpcPlugin{
		LogCtx: logCtx,
	}

	// pluginMap is the map of plugins we can dispense.
	var pluginMap = map[string]goPlugin.Plugin{
		"RpcMetricProviderPlugin": &rpc.RpcMetricProviderPlugin{Impl: rpcPluginImp},
	}

	ch := make(chan *goPlugin.ReattachConfig, 1)
	closeCh := make(chan struct{})
	go goPlugin.Serve(&goPlugin.ServeConfig{
		HandshakeConfig: testHandshake,
		Plugins:         pluginMap,
		Test: &goPlugin.ServeTestConfig{
			Context:          ctx,
			ReattachConfigCh: ch,
			CloseCh:          closeCh,
		},
	})

	// We should get a config
	var config *goPlugin.ReattachConfig
	select {
	case config = <-ch:
	case <-time.After(2000 * time.Millisecond):
		t.Fatal("should've received reattach")
	}
	if config == nil {
		t.Fatal("config should not be nil")
	}

	// Connect!
	c := goPlugin.NewClient(&goPlugin.ClientConfig{
		Cmd:             nil,
		HandshakeConfig: testHandshake,
		Plugins:         pluginMap,
		Reattach:        config,
	})
	client, err := c.Client()
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	// Request the plugin
	raw, err := client.Dispense("RpcMetricProviderPlugin")
	if err != nil {
		t.Fail()
	}

	plugin, ok := raw.(rpc.MetricProviderPlugin)
	if !ok {
		t.Fail()
	}

	return plugin, client, cancel, closeCh
}

func TestRunIteration(t *testing.T) {
	plugin, _, cancel, closeCh := pluginClient(t)
	defer cancel()

	err := plugin.InitPlugin()
	if err.Error() != "" {
		t.Fail()
	}

	insecureSkipVerify, getBoolErr := env.GetBool("dynamodb_INSECURE_SKIP_VERIFY", true)
	if getBoolErr != nil {
		insecureSkipVerify = true
	}

	msg := map[string]interface{}{
		"address":            env.GetString("dynamodb_ADDRESS", "http://localhost:9200"),
		"username":           env.GetString("dynamodb_USERNAME", ""),
		"password":           env.GetString("dynamodb_PASSWORD", ""),
		"insecureSkipVerify": insecureSkipVerify,
		"index":              env.GetString("dynamodb_INDEX", "sample-index"),
		"query": env.GetString("dynamodb_QUERY", `
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
		}`),
	}

	jsonBytes, e := json.Marshal(msg)
	if e != nil {
		t.Fail()
	}

	jsonStr := string(jsonBytes)

	runMeasurement := plugin.Run(&v1alpha1.AnalysisRun{}, v1alpha1.Metric{
		Provider: v1alpha1.MetricProvider{
			Plugin: map[string]json.RawMessage{"argoproj-labs/dynamodb-metric-plugin": json.RawMessage(jsonStr)},
		},
		SuccessCondition: "result[len(result)-1] <= result[len(result)-2]",
	})
	fmt.Println(runMeasurement)
	assert.Equal(t, "Successful", string(runMeasurement.Phase))

	cancel()
	<-closeCh
}

func TestPluginClosedConnection(t *testing.T) {
	plugin, client, cancel, closeCh := pluginClient(t)
	defer cancel()

	client.Close()
	time.Sleep(100 * time.Millisecond)

	const expectedError = "connection is shut down"

	newMetrics := plugin.InitPlugin()
	assert.Contains(t, newMetrics.Error(), expectedError)

	measurement := plugin.Terminate(&v1alpha1.AnalysisRun{}, v1alpha1.Metric{}, v1alpha1.Measurement{})
	assert.Contains(t, measurement.Message, expectedError)

	measurement = plugin.Run(&v1alpha1.AnalysisRun{}, v1alpha1.Metric{})
	assert.Contains(t, measurement.Message, expectedError)

	measurement = plugin.Resume(&v1alpha1.AnalysisRun{}, v1alpha1.Metric{}, v1alpha1.Measurement{})
	assert.Contains(t, measurement.Message, expectedError)

	measurement = plugin.Terminate(&v1alpha1.AnalysisRun{}, v1alpha1.Metric{}, v1alpha1.Measurement{})
	assert.Contains(t, measurement.Message, expectedError)

	typeStr := plugin.Type()
	assert.Contains(t, typeStr, expectedError)

	metadata := plugin.GetMetadata(v1alpha1.Metric{})
	assert.Contains(t, metadata["error"], expectedError)

	gcError := plugin.GarbageCollect(&v1alpha1.AnalysisRun{}, v1alpha1.Metric{}, 0)
	assert.Contains(t, gcError.Error(), expectedError)

	cancel()
	<-closeCh
}

func TestInvalidArgs(t *testing.T) {
	server := rpc.MetricsRPCServer{}
	badtype := struct {
		Args string
	}{}
	err := server.Run(badtype, &v1alpha1.Measurement{})
	assert.Error(t, err)

	err = server.Resume(badtype, &v1alpha1.Measurement{})
	assert.Error(t, err)

	err = server.Terminate(badtype, &v1alpha1.Measurement{})
	assert.Error(t, err)

	err = server.GarbageCollect(badtype, &types.RpcError{})
	assert.Error(t, err)

	resp := make(map[string]string)
	err = server.GetMetadata(badtype, &resp)
	assert.Error(t, err)
}
