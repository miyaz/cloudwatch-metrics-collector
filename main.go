package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudwatch"
	"github.com/aws/aws-sdk-go/service/ec2"
	yaml "gopkg.in/yaml.v2"
)

// CloudWatchの制限参照
// see) https://docs.aws.amazon.com/ja_jp/AmazonCloudWatch/latest/monitoring/cloudwatch_limits.html
// MetricDataQuery 構造を含められる数の上限は100固定
const MaxMetricDataQuery = 100

// API制限（秒間最大実行数）
const MaxRateLimitListMetrics = 25
const MaxRateLimitGetMetricData = 50

type SdkParam struct {
	profile string
	region  string
}

type Config struct {
	DefConf *Default   `yaml:"default"`
	SvcConf []*Service `yaml:"services"`
}

type Service struct {
	StartTime  int      `yaml:"start_time"`
	EndTime    int      `yaml:"end_time"`
	Period     int      `yaml:"period"`
	Namespace  string   `yaml:"namespace"`
	Dimensions []string `yaml:"dimensions"`
	Metrics    []Metric `yaml:"metrics"`
}

type Default struct {
	StartTime int      `yaml:"start_time"`
	EndTime   int      `yaml:"end_time"`
	Period    int      `yaml:"period"`
	Metrics   []Metric `yaml:"metrics"`
}

type Metric struct {
	Name       string `yaml:"name"`
	Statistics string `yaml:"statistics"`
}

var (
	argProfile   = flag.String("profile", "", "AWS Shared Credential の Profile 名を指定する")
	argRegion    = flag.String("region", "ap-northeast-1", "AWS Region 名を指定する")
	argConfig    = flag.String("config", "config.yml", "取得メトリクスを指定した設定ファイルを指定する")
	argOutput    = flag.Bool("output", false, "デフォルト設定情報(yaml)を標準出力する")
	argLabelOnly = flag.Bool("labelonly", false, "ラベル(1列目)のみ重複排除して出力する")
)
var cwInstance *cloudwatch.CloudWatch
var ec2Instance *ec2.EC2
var once sync.Once

//getSdkInstances returns singleton instance
func getSdkInstances(param SdkParam) (*cloudwatch.CloudWatch, *ec2.EC2) {
	once.Do(func() {
		config := aws.Config{Region: aws.String(param.region), MaxRetries: aws.Int(10)}
		if param.profile != "" {
			creds := credentials.NewSharedCredentials("", param.profile)
			config.Credentials = creds
		}
		sess := session.New(&config)
		cwInstance = cloudwatch.New(sess)
		ec2Instance = ec2.New(sess)
	})
	return cwInstance, ec2Instance
}

// ファイルの存在チェック
func fileExists(filename string) bool {
	_, err := os.Stat(filename)
	return err == nil
}

// Yamlファイルを読み込む. ファイルが存在しない場合はデフォルト設定で動く
func loadYamlConfig(filePath string) Config {
	var bytes []byte
	var err error
	if fileExists(filePath) {
		// load yaml file
		bytes, err = ioutil.ReadFile(filePath)
	} else {
		// use default settings
		bytes, err = Asset("data/config.yml")
	}
	if err != nil {
		panic(err)
	}

	// structにUnmasrshal
	var config Config
	err = yaml.Unmarshal(bytes, &config)
	if err != nil {
		panic(err)
	}

	prepareConfig(&config)
	return config
}

// 設定情報の無指定項目をデフォルト値で上書き
func prepareConfig(config *Config) {
	for _, service := range config.SvcConf {
		if service.StartTime == 0 {
			service.StartTime = config.DefConf.StartTime
		}
		if service.EndTime == 0 {
			service.EndTime = config.DefConf.EndTime
		}
		if service.Period == 0 {
			service.Period = config.DefConf.Period
		}
		if len(service.Metrics) == 0 {
			tmp := make([]Metric, 0)
			for _, metric := range config.DefConf.Metrics {
				tmp = append(tmp, metric)
			}
			service.Metrics = tmp
		}
	}
}

// Dimensionの値を結合する（出力時のラベルに使うため）
func joinValueFromDimensions(dimensions []*cloudwatch.Dimension) string {
	dimTarget := *dimensions[0].Value
	for i := 1; i < len(dimensions); i++ {
		dimTarget += "-" + *dimensions[i].Value
	}
	return dimTarget
}

// 配列に指定された文字列が含まれるかチェックする
func arrayContains(arr []string, str string) bool {
	for _, v := range arr {
		if v == str {
			return true
		}
	}
	return false
}

// ListMetricsの結果をNextTokenがなくなるまで全て取得してMetricオブジェクトの配列にして返す
func listMetrics(service *Service, metricName string) (metricList []*cloudwatch.Metric) {
	cwClient, _ := getSdkInstances(SdkParam{})

	isFirst := ""
	nextToken := aws.String(isFirst)
	listMetricsInput := &cloudwatch.ListMetricsInput{
		Namespace:  aws.String(service.Namespace),
		MetricName: aws.String(metricName),
	}

	for nextToken != nil {
		if *nextToken != isFirst {
			listMetricsInput.NextToken = nextToken
		}

		resp, err := cwClient.ListMetrics(listMetricsInput)
		if err != nil {
			panic(err)
		}
		nextToken = resp.NextToken
		for _, metric := range resp.Metrics {
			// Dimensionsが含まれない要素は除外
			if len(metric.Dimensions) != 0 {
				metricList = append(metricList, metric)
			}
		}
	}
	return
}

// 指定したMetricNameのメトリクス一覧を並列に取得する
func parallelListMetrics(service *Service, metricNames []string) (metricList []*cloudwatch.Metric) {
	var wg sync.WaitGroup
	var mu sync.Mutex

	// rate-limit. see https://gobyexample.com/rate-limiting
	requests := make(chan int, len(metricNames))
	for i := 0; i < len(metricNames); i++ {
		requests <- i
	}
	close(requests)
	limiter := time.Tick(1000 / MaxRateLimitListMetrics * time.Millisecond)
	for req := range requests {
		metricName := metricNames[req]
		wg.Add(1)
		go func(service *Service, metricName string) {
			defer wg.Done()
			<-limiter
			resp := listMetrics(service, metricName)
			mu.Lock()
			defer mu.Unlock()
			for _, metric := range resp {
				metricList = append(metricList, metric)
			}
		}(service, metricName)
	}
	wg.Wait()

	return
}

// dimensionsで指定されたリソースの、serviceに指定されたメトリクスを取得して２次元配列に格納して返す
func getMetricData(service *Service, dimList [][]*cloudwatch.Dimension) (metricTable [][]string) {
	cwClient, _ := getSdkInstances(SdkParam{})

	endTime := aws.Time(time.Now().Add(time.Duration(int64(service.EndTime)) * time.Second * -1))
	startTime := aws.Time(time.Now().Add(time.Duration(int64(service.StartTime)) * time.Second * -1))
	// クエリIDをAPIレスポンスに含まれるIdで紐づけるためのマップ
	queryId2InstanceLabel := map[string]string{}
	queryId2MetricName := map[string]string{}

	var metricDataQueries []*cloudwatch.MetricDataQuery
	for _, dimensions := range dimList {
		instanceLabel := joinValueFromDimensions(dimensions)
		for idx, metric := range service.Metrics {
			metricName := metric.Name
			metricDataId := fmt.Sprintf("%s_%d", strings.Replace(instanceLabel, "-", "", -1), idx)
			queryId2InstanceLabel[metricDataId] = instanceLabel
			queryId2MetricName[metricDataId] = metricName
			period := int64(service.Period)
			stat := metric.Statistics
			metricDataQueries = append(metricDataQueries, &cloudwatch.MetricDataQuery{
				Id: &metricDataId,
				MetricStat: &cloudwatch.MetricStat{
					Metric: &cloudwatch.Metric{
						Namespace:  &service.Namespace,
						Dimensions: dimensions,
						MetricName: &metricName,
					},
					Period: &period,
					Stat:   &stat,
				},
			})
		}
	}

	resp, err := cwClient.GetMetricData(&cloudwatch.GetMetricDataInput{
		EndTime:           endTime,
		StartTime:         startTime,
		MetricDataQueries: metricDataQueries,
	})
	if err != nil {
		panic(err)
	}

	for _, metricdata := range resp.MetricDataResults {
		for index, _ := range metricdata.Timestamps {
			instanceLabel := queryId2InstanceLabel[*metricdata.Id]
			metricName := queryId2MetricName[*metricdata.Id]
			metricTime := fmt.Sprintf("%v", (*metricdata.Timestamps[index]).Unix())
			metricValue := fmt.Sprintf("%v", *metricdata.Values[index])

			metricTable = append(metricTable, []string{instanceLabel, metricName, metricTime, metricValue})
		}
	}
	return
}

// 指定したdimensions配列のメトリクスデータを並列に取得する
func parallelGetMetricData(service *Service, dimList [][]*cloudwatch.Dimension) (metricRecords [][]string) {
	var wg sync.WaitGroup
	var mu sync.Mutex

	// 1回のGetMetricDataで取得するMetricDataQuery用のデータを準備
	bulkNum := MaxMetricDataQuery / len(service.Metrics)
	dimParams := [][][]*cloudwatch.Dimension{}
	dimParam := [][]*cloudwatch.Dimension{}
	for idx, dimensions := range dimList {
		dimParam = append(dimParam, dimensions)
		if idx%bulkNum == bulkNum-1 || idx == len(dimList)-1 {
			dimParams = append(dimParams, dimParam)
			dimParam = [][]*cloudwatch.Dimension{}
		}
	}

	// rate-limit. see https://gobyexample.com/rate-limiting
	requests := make(chan int, len(dimParams))
	for i := 0; i < len(dimParams); i++ {
		requests <- i
	}
	close(requests)
	limiter := time.Tick(1000 / MaxRateLimitGetMetricData * time.Millisecond)
	for req := range requests {
		bulkDims := dimParams[req]
		wg.Add(1)
		go func(service *Service, bulkDims [][]*cloudwatch.Dimension) {
			defer wg.Done()
			<-limiter
			resp := getMetricData(service, bulkDims)
			mu.Lock()
			defer mu.Unlock()
			for _, metricRecord := range resp {
				metricRecords = append(metricRecords, metricRecord)
			}
		}(service, bulkDims)
	}
	wg.Wait()
	return
}

// InstanceIdに紐づくInstanceStateをmap型に格納して返す
func getEc2InstanceStatuses() map[string]string {
	_, ec2Client := getSdkInstances(SdkParam{})
	resp, err := ec2Client.DescribeInstances(nil)
	if err != nil {
		panic(err)
	}
	instanceStates := map[string]string{}
	for _, r := range resp.Reservations {
		for _, i := range r.Instances {
			instanceStates[*i.InstanceId] = *i.State.Name
		}
	}
	return instanceStates
}

// InstanceIdに紐づくInstanceNameをmap型に格納して返す
func getEc2InstanceNames() map[string]string {
	_, ec2Client := getSdkInstances(SdkParam{})
	resp, err := ec2Client.DescribeInstances(nil)
	if err != nil {
		panic(err)
	}
	instanceNames := map[string]string{}
	for _, r := range resp.Reservations {
		for _, i := range r.Instances {
			var tag_name string
			for _, t := range i.Tags {
				if *t.Key == "Name" {
					tag_name = *t.Value
				}
			}
			if tag_name != "" {
				instanceNames[*i.InstanceId] = tag_name
			}
		}
	}
	return instanceNames
}

// 指定されたインスタンスの状態を返すクロージャ
func makeFuncGetInstanceState() func(string) string {
	statusSet := getEc2InstanceStatuses()
	return func(instanceId string) string {
		if val, ok := statusSet[instanceId]; ok {
			return val
		}
		return "unknown"
	}
}

// 指定されたインスタンスのTag[Name]を返す関数を返すクロージャ
func makeFuncGetInstanceName() func(string) string {
	idNameSet := getEc2InstanceNames()
	return func(instanceId string) string {
		if val, ok := idNameSet[instanceId]; ok {
			return val
		}
		return instanceId
	}
}

func main() {
	// オプション判定
	flag.Parse()

	// エラー発生(panic)したら、Usage出してエラーメッセージだしてExit1する
	defer func() {
		if r := recover(); r != nil {
			flag.Usage()
			fmt.Println(r)
			os.Exit(1)
		}
	}()

	// デフォルトコンフィグ出力モード
	if *argOutput {
		config, _ := Asset("data/config.yml")
		fmt.Println(string(config))
		os.Exit(0)
	}

	// 設定ファイルをロード
	config := loadYamlConfig(*argConfig)

	// aws-sdk client生成
	getSdkInstances(SdkParam{profile: *argProfile, region: *argRegion})

	var getInstanceState func(string) string
	var getInstanceName func(string) string
	for _, service := range config.SvcConf {

		// DimensionがInstanceIdの場合はInstanceNameにラベルに使うための関数生成
		if service.Dimensions[0] == "InstanceId" {
			getInstanceState = makeFuncGetInstanceState()
			getInstanceName = makeFuncGetInstanceName()
		}

		// metricNameリスト作成
		metricNames := []string{}
		for _, metric := range service.Metrics {
			metricNames = append(metricNames, metric.Name)
		}
		respList := parallelListMetrics(service, metricNames)
		var doneList []string
		var dimList [][]*cloudwatch.Dimension
		for _, respMetric := range respList {

			dimNames := []string{}
			dimValues := []string{}
			for _, dimension := range respMetric.Dimensions {
				dimNames = append(dimNames, *dimension.Name)
				dimValues = append(dimValues, *dimension.Value)
			}

			// EC2Instanceの場合はrunning状態以外はスキップ
			if dimNames[0] == "InstanceId" {
				if getInstanceState(dimValues[0]) != "running" {
					continue
				}
			}

			// Dimension構成要素が一致する場合のみメトリクス取得する
			sort.Sort(sort.StringSlice(dimNames))
			sort.Sort(sort.StringSlice(service.Dimensions))
			if reflect.DeepEqual(dimNames, service.Dimensions) {
				instanceLabel := joinValueFromDimensions(respMetric.Dimensions)
				if dimNames[0] == "InstanceId" {
					instanceLabel = getInstanceName(dimValues[0])
				}
				if arrayContains(doneList, instanceLabel) {
					continue
				}

				if *argLabelOnly {
					fmt.Println(instanceLabel)
				} else {
					dimList = append(dimList, respMetric.Dimensions)
				}
				doneList = append(doneList, instanceLabel)
			}
		}

		// 並列にMetricsDataを取得する
		metricRecords := parallelGetMetricData(service, dimList)
		// Print results
		for _, metricRecord := range metricRecords {
			if strings.Index(metricRecord[0], "i-") == 0 {
				metricRecord[0] = getInstanceName(metricRecord[0])
			}
			fmt.Println(strings.Join(metricRecord, " "))
		}
	}
}
