package press

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"yola/test/ludo/internal/pressure"

	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/file"
)

const (
	defaultConcurrency       = 1000
	defaultActionConcurrency = 1000

	scenarioConnect        = "connect"
	scenarioPlay           = "play"
	scenarioReconnectChurn = "reconnect-churn"
)

type Press struct {
	URL               string
	Open              bool
	Scenario          string
	Num               int32
	Batch             []int32
	Interval          int32
	StartID           int64
	UIDCount          int64
	Concurrency       int
	ActionConcurrency int
	GatewayLimit      int32
	GatewayPerIPLimit int32
	NodeSeatCapacity  int32
	MinMoney          float64
	MaxMoney          float64
	LogoutRate        float64
	OfflineRate       float64
}

type configFile struct {
	LoadTest *loadTest
}

type loadTest struct {
	Press Press
}

func LoadConfig(path string) (config.Config, Press, error) {
	c := config.New(config.WithSource(file.NewSource(path)))
	if err := c.Load(); err != nil {
		return nil, Press{}, fmt.Errorf("load config: %w", err)
	}
	if err := validatePressKeys(c); err != nil {
		_ = c.Close()
		return nil, Press{}, err
	}

	var file configFile
	if err := c.Scan(&file); err != nil {
		_ = c.Close()
		return nil, Press{}, fmt.Errorf("scan config: %w", err)
	}
	press, err := validatePress(&file)
	if err != nil {
		_ = c.Close()
		return nil, Press{}, err
	}
	return c, press, nil
}

func validatePressKeys(c config.Config) error {
	values, err := c.Value("loadTest.press").Map()
	if err != nil {
		return fmt.Errorf("validate config: loadTest.press is required: %w", err)
	}
	var unknown []string
	for key := range values {
		switch key {
		case "url", "open", "scenario", "num", "batch", "interval", "startID", "uidCount",
			"concurrency", "actionConcurrency", "gatewayLimit", "gatewayPerIPLimit", "nodeSeatCapacity",
			"minMoney", "maxMoney", "logoutRate", "offlineRate":
		default:
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("validate config: unknown loadTest.press fields: %s", strings.Join(unknown, ", "))
}

func validatePress(file *configFile) (Press, error) {
	if file.LoadTest == nil {
		return Press{}, fmt.Errorf("validate config: loadTest is required")
	}
	press := withPressDefaults(file.LoadTest.Press)
	if press.URL == "" {
		return Press{}, fmt.Errorf("validate config: loadTest.press.url is required")
	}
	if press.Num <= 0 || len(press.Batch) != 2 || press.Batch[0] <= 0 || press.Batch[1] < press.Batch[0] {
		return Press{}, fmt.Errorf("validate config: loadTest.press num and batch are invalid")
	}
	if press.Interval <= 0 || press.StartID <= 0 {
		return Press{}, fmt.Errorf("validate config: loadTest.press interval and startID must be positive")
	}
	if press.UIDCount < int64(press.Num) || press.UIDCount > math.MaxInt64-press.StartID+1 {
		return Press{}, fmt.Errorf("validate config: loadTest.press uidCount must reserve a valid non-overlapping UID range")
	}
	if press.Concurrency <= 0 || press.ActionConcurrency <= 0 {
		return Press{}, fmt.Errorf("validate config: loadTest.press concurrency must be positive")
	}
	money := pressure.MoneyRange{Min: press.MinMoney, Max: press.MaxMoney}
	if _, err := pressure.Token(money); err != nil {
		return Press{}, fmt.Errorf("validate config: loadTest.press: %w", err)
	}
	if press.LogoutRate < 0 || press.LogoutRate > 1 || press.OfflineRate < 0 || press.OfflineRate > 1 {
		return Press{}, fmt.Errorf("validate config: loadTest.press rates must be within [0,1]")
	}
	switch press.Scenario {
	case scenarioConnect, scenarioPlay:
		if press.LogoutRate != 0 || press.OfflineRate != 0 {
			return Press{}, fmt.Errorf("validate config: %s scenario cannot enable churn rates", press.Scenario)
		}
	case scenarioReconnectChurn:
		if press.LogoutRate == 0 && press.OfflineRate == 0 {
			return Press{}, fmt.Errorf("validate config: reconnect-churn scenario requires a churn rate")
		}
		if press.UIDCount <= int64(press.Num) {
			return Press{}, fmt.Errorf("validate config: reconnect-churn scenario requires replacement UIDs")
		}
	default:
		return Press{}, fmt.Errorf("validate config: loadTest.press scenario %q is invalid", press.Scenario)
	}
	if press.Open {
		if err := validateCapacity(press); err != nil {
			return Press{}, err
		}
	}
	return press, nil
}

func withPressDefaults(press Press) Press {
	if press.Concurrency == 0 {
		press.Concurrency = defaultConcurrency
	}
	if press.ActionConcurrency == 0 {
		press.ActionConcurrency = defaultActionConcurrency
	}
	return press
}

func validateCapacity(press Press) error {
	gateways := gatewayURLs(press.URL)
	if len(gateways) == 0 || press.GatewayLimit <= 0 || press.GatewayPerIPLimit <= 0 {
		return fmt.Errorf("validate config: Gateway capacity must be positive")
	}
	target := int64(press.Num)
	instances := int64(len(gateways))
	if target > instances*int64(press.GatewayLimit) {
		return fmt.Errorf("validate config: target %d exceeds Gateway capacity %d", target, instances*int64(press.GatewayLimit))
	}
	if target > instances*int64(press.GatewayPerIPLimit) {
		return fmt.Errorf("validate config: target %d exceeds source IP capacity %d", target, instances*int64(press.GatewayPerIPLimit))
	}
	if press.Scenario != scenarioConnect && (press.NodeSeatCapacity <= 0 || target > int64(press.NodeSeatCapacity)) {
		return fmt.Errorf("validate config: target %d exceeds Node seat capacity %d", target, press.NodeSeatCapacity)
	}
	return nil
}

func gatewayURLs(raw string) []string {
	seen := make(map[string]struct{})
	urls := make([]string, 0, strings.Count(raw, ",")+1)
	for _, value := range strings.Split(raw, ",") {
		endpoint := strings.TrimSpace(value)
		if endpoint == "" {
			continue
		}
		if _, exists := seen[endpoint]; !exists {
			seen[endpoint] = struct{}{}
			urls = append(urls, endpoint)
		}
	}
	return urls
}
