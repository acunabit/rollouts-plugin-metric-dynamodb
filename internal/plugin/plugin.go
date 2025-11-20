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
	dynamodbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	log "github.com/sirupsen/logrus"
)

// RpcPlugin implements the ArgoRollouts MetricsPlugin interface.
// This plugin uses DynamoDB as a distributed communication point for multi-cluster coordination.
// It writes analysis run metadata to DynamoDB and polls for a Result attribute that is set by
// external systems (running in different clusters or AWS accounts) to coordinate cross-cluster
// analysis validation and testing.
type RpcPlugin struct {
	LogCtx log.Entry
}

type Config struct {
	TableName        string `json:"table_name,omitempty" protobuf:"bytes,1,opt,name=table_name"`
	Region           string `json:"region,omitempty" protobuf:"bytes,2,opt,name=region"`
	ClusterID        string `json:"cluster_id,omitempty" protobuf:"bytes,3,opt,name=cluster_id"`
	AnalysisTemplate string `json:"analysis_template,omitempty" protobuf:"bytes,4,opt,name=analysis_template"`
	Namespace        string `json:"namespace,omitempty" protobuf:"bytes,5,opt,name=namespace"`
	PollInterval     int    `json:"poll_interval,omitempty" protobuf:"varint,6,opt,name=poll_interval"` // Poll interval in seconds, default 5
	PollTimeout      int    `json:"poll_timeout,omitempty" protobuf:"varint,7,opt,name=poll_timeout"`   // Poll timeout in seconds, default 300
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
	err := json.Unmarshal(metric.Provider.Plugin["block/rollouts-plugin-distributed-analysis-runs"], &cfg)
	if err != nil {
		return metricutil.MarkMeasurementError(newMeasurement, err)
	}

	// Default table name if not provided
	if cfg.TableName == "" {
		cfg.TableName = "KargoArgoRolloutsIntegration"
	}

	// Default region if not provided
	if cfg.Region == "" {
		cfg.Region = "ap-southeast-2"
	}

	if cfg.AnalysisTemplate == "" {
		return metricutil.MarkMeasurementError(newMeasurement, errors.New("analysis_template is required"))
	}

	if cfg.Namespace == "" {
		return metricutil.MarkMeasurementError(newMeasurement, errors.New("namespace is required"))
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

	// Write entry to DynamoDB with analysis template, cluster ID, namespace, and analysis run UID
	err = writeToDynamoDB(ctx, client, cfg.TableName, cfg.AnalysisTemplate, cfg.ClusterID, cfg.Namespace, analysisRunUID)
	if err != nil {
		return metricutil.MarkMeasurementError(newMeasurement, err)
	}

	// Poll DynamoDB for Result attribute
	pollInterval := time.Duration(cfg.PollInterval) * time.Second
	if pollInterval == 0 {
		pollInterval = 5 * time.Second // Default 5 seconds
	}
	pollTimeout := time.Duration(cfg.PollTimeout) * time.Second
	if pollTimeout == 0 {
		pollTimeout = 300 * time.Second // Default 5 minutes
	}

	pollCtx, pollCancel := context.WithTimeout(context.Background(), pollTimeout)
	defer pollCancel()

	result, err := pollForResult(pollCtx, client, cfg.TableName, analysisRunUID, cfg.ClusterID, pollInterval)
	if err != nil {
		return metricutil.MarkMeasurementError(newMeasurement, err)
	}

	if result == "Passed" {
		newMeasurement.Value = fmt.Sprintf("analysis_template=%s,cluster_id=%s,namespace=%s,analysis_run_uid=%s,result=Passed", cfg.AnalysisTemplate, cfg.ClusterID, cfg.Namespace, analysisRunUID)
		newMeasurement.Phase = v1alpha1.AnalysisPhaseSuccessful
	} else {
		newMeasurement.Value = fmt.Sprintf("analysis_template=%s,cluster_id=%s,namespace=%s,analysis_run_uid=%s,result=%s", cfg.AnalysisTemplate, cfg.ClusterID, cfg.Namespace, analysisRunUID, result)
		newMeasurement.Phase = v1alpha1.AnalysisPhaseError
		newMeasurement.Message = fmt.Sprintf("Result is not Passed: %s", result)
	}

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

	// Load AWS config - will automatically use IRSA credentials if available
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

func writeToDynamoDB(ctx context.Context, client *dynamodb.Client, tableName string, analysisTemplate string, clusterID string, namespace string, analysisRunUID string) error {
	// Write analysis run metadata to DynamoDB as a distributed communication point.
	// External systems (in other clusters/accounts) can read this entry, perform validation,
	// and write the Result attribute back to DynamoDB for cross-cluster coordination.
	// Create an item with AnalysisRunUid as the unique key (PascalCase to match DynamoDB schema)
	// Also include AnalysisTemplate, ClusterID, Namespace, and Timestamp for reference
	item := map[string]interface{}{
		"AnalysisRunUid":   analysisRunUID,
		"AnalysisTemplate": analysisTemplate,
		"ClusterID":        clusterID,
		"Namespace":        namespace,
		"Timestamp":        time.Now().Format(time.RFC3339),
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

	log.Debugf("Successfully wrote entry to DynamoDB table %s: AnalysisRunUid=%s, AnalysisTemplate=%s, ClusterID=%s, Namespace=%s", tableName, analysisRunUID, analysisTemplate, clusterID, namespace)
	return nil
}

func pollForResult(ctx context.Context, client *dynamodb.Client, tableName string, analysisRunUID string, clusterID string, pollInterval time.Duration) (string, error) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("polling timeout: Result attribute not found before timeout")
		case <-ticker.C:
			result, found, err := readResultFromDynamoDB(ctx, client, tableName, analysisRunUID, clusterID)
			if err != nil {
				return "", fmt.Errorf("failed to read from DynamoDB: %w", err)
			}
			if found {
				return result, nil
			}
			log.Debugf("Result not yet available, polling again in %v...", pollInterval)
		}
	}
}

func readResultFromDynamoDB(ctx context.Context, client *dynamodb.Client, tableName string, analysisRunUID string, clusterID string) (string, bool, error) {
	// Build key for GetItem
	key := map[string]dynamodbtypes.AttributeValue{
		"AnalysisRunUid": &dynamodbtypes.AttributeValueMemberS{Value: analysisRunUID},
	}
	if clusterID != "" {
		key["ClusterID"] = &dynamodbtypes.AttributeValueMemberS{Value: clusterID}
	}

	input := &dynamodb.GetItemInput{
		TableName: aws.String(tableName),
		Key:       key,
	}

	result, err := client.GetItem(ctx, input)
	if err != nil {
		return "", false, err
	}

	if result.Item == nil {
		return "", false, nil // Item not found yet
	}

	// Check if Result attribute exists and is not null
	if resultAttr, ok := result.Item["Result"]; ok {
		// Check if it's a NULL attribute
		if _, isNull := resultAttr.(*dynamodbtypes.AttributeValueMemberNULL); isNull {
			// Result exists but is null, keep polling
			return "", false, nil
		}
		// Check if it's a string attribute
		if resultAttrMember, ok := resultAttr.(*dynamodbtypes.AttributeValueMemberS); ok {
			return resultAttrMember.Value, true, nil
		}
	}

	return "", false, nil // Result attribute not found or is null
}
