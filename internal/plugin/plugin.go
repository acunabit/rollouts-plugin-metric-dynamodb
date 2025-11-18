package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/argoproj/argo-rollouts/metricproviders/plugin"
	"github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	metricutil "github.com/argoproj/argo-rollouts/utils/metric"
	"github.com/argoproj/argo-rollouts/utils/plugin/types"
	timeutil "github.com/argoproj/argo-rollouts/utils/time"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	log "github.com/sirupsen/logrus"
)

// Here is a real implementation of MetricsPlugin
type RpcPlugin struct {
	LogCtx log.Entry
}

type Config struct {
	TableName        string `json:"table_name,omitempty" protobuf:"bytes,1,opt,name=table_name"`
	Region           string `json:"region,omitempty" protobuf:"bytes,2,opt,name=region"`
	ClusterID        string `json:"cluster_id,omitempty" protobuf:"bytes,3,opt,name=cluster_id"`
	AnalysisTemplate string `json:"analysis_template,omitempty" protobuf:"bytes,4,opt,name=analysis_template"`
}

func (g *RpcPlugin) InitPlugin() types.RpcError {
	return types.RpcError{}
}

func (g *RpcPlugin) Run(analysisRun *v1alpha1.AnalysisRun, metric v1alpha1.Metric) v1alpha1.Measurement {
	startTime := timeutil.MetaNow()
	newMeasurement := v1alpha1.Measurement{
		StartedAt: &startTime,
	}

	cfg := Config{}
	err := json.Unmarshal(metric.Provider.Plugin["argoproj-labs/dynamodb-metric-plugin"], &cfg)
	if err != nil {
		return metricutil.MarkMeasurementError(newMeasurement, err)
	}

	if cfg.TableName == "" {
		return metricutil.MarkMeasurementError(newMeasurement, errors.New("table_name is required"))
	}

	if cfg.AnalysisTemplate == "" {
		return metricutil.MarkMeasurementError(newMeasurement, errors.New("analysis_template is required"))
	}

	if cfg.ClusterID == "" {
		return metricutil.MarkMeasurementError(newMeasurement, errors.New("cluster_id is required"))
	}

	client, err := newDynamoDBClient(cfg)
	if err != nil {
		return metricutil.MarkMeasurementError(newMeasurement, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Extract AnalysisRun UID as unique identifier
	analysisRunUID := ""
	if analysisRun != nil && analysisRun.UID != "" {
		analysisRunUID = string(analysisRun.UID)
	}

	if analysisRunUID == "" {
		return metricutil.MarkMeasurementError(newMeasurement, errors.New("analysis run UID is required"))
	}

	// Write entry to DynamoDB with analysis template, cluster ID, and analysis run UID
	err = writeToDynamoDB(ctx, client, cfg.TableName, cfg.AnalysisTemplate, cfg.ClusterID, analysisRunUID)
	if err != nil {
		return metricutil.MarkMeasurementError(newMeasurement, err)
	}

	newMeasurement.Value = fmt.Sprintf("analysis_template=%s,cluster_id=%s,analysis_run_uid=%s", cfg.AnalysisTemplate, cfg.ClusterID, analysisRunUID)
	newMeasurement.Phase = v1alpha1.AnalysisPhaseSuccessful
	finishedTime := timeutil.MetaNow()
	newMeasurement.FinishedAt = &finishedTime
	return newMeasurement
}

func (g *RpcPlugin) Resume(analysisRun *v1alpha1.AnalysisRun, metric v1alpha1.Metric, measurement v1alpha1.Measurement) v1alpha1.Measurement {
	return measurement
}

func (g *RpcPlugin) Terminate(analysisRun *v1alpha1.AnalysisRun, metric v1alpha1.Metric, measurement v1alpha1.Measurement) v1alpha1.Measurement {
	return measurement
}

func (g *RpcPlugin) GarbageCollect(*v1alpha1.AnalysisRun, v1alpha1.Metric, int) types.RpcError {
	return types.RpcError{}
}

func (g *RpcPlugin) Type() string {
	return plugin.ProviderType
}

func (g *RpcPlugin) GetMetadata(metric v1alpha1.Metric) map[string]string {
	metricsMetadata := make(map[string]string)

	cfg := Config{}
	json.Unmarshal(metric.Provider.Plugin["argoproj-labs/dynamodb-metric-plugin"], &cfg)
	if cfg.TableName != "" {
		metricsMetadata["DynamoDBTable"] = cfg.TableName
	}
	if cfg.Region != "" {
		metricsMetadata["AWSRegion"] = cfg.Region
	}
	return metricsMetadata
}

func newDynamoDBClient(cfg Config) (*dynamodb.Client, error) {
	ctx := context.Background()
	var awsCfg aws.Config
	var err error

	if cfg.Region != "" {
		awsCfg, err = config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Region))
	} else {
		awsCfg, err = config.LoadDefaultConfig(ctx)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return dynamodb.NewFromConfig(awsCfg), nil
}

func writeToDynamoDB(ctx context.Context, client *dynamodb.Client, tableName string, analysisTemplate string, clusterID string, analysisRunUID string) error {
	// Create an item with analysis_run_uid as the unique key
	// Also include analysis_template, cluster_id, and timestamp for reference
	item := map[string]interface{}{
		"analysis_run_uid":  analysisRunUID,
		"analysis_template": analysisTemplate,
		"cluster_id":        clusterID,
		"timestamp":         time.Now().Format(time.RFC3339),
	}

	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("failed to marshal item: %w", err)
	}

	input := &dynamodb.PutItemInput{
		TableName: aws.String(tableName),
		Item:      av,
	}

	_, err = client.PutItem(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to put item to DynamoDB: %w", err)
	}

	log.Debugf("Successfully wrote entry to DynamoDB table %s: analysis_run_uid=%s, analysis_template=%s, cluster_id=%s", tableName, analysisRunUID, analysisTemplate, clusterID)
	return nil
}
