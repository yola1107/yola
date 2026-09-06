package press

import (
	"fmt"

	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/file"
)

type Bootstrap struct {
	LoadTest *LoadTest
}

type LoadTest struct {
	Press Press
}

type Press struct {
	URL         string
	Open        bool
	Num         int32
	Batch       []int32
	Interval    int32
	StartID     int64
	MinMoney    float64
	MaxMoney    float64
	LogoutRate  float64
	OfflineRate float64
}

func LoadConfig(path string) (config.Config, *Bootstrap, error) {
	c := config.New(config.WithSource(file.NewSource(path)))
	if err := c.Load(); err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}

	var bootstrap Bootstrap
	if err := c.Scan(&bootstrap); err != nil {
		_ = c.Close()
		return nil, nil, fmt.Errorf("scan config: %w", err)
	}
	if err := validatePress(&bootstrap); err != nil {
		_ = c.Close()
		return nil, nil, err
	}
	return c, &bootstrap, nil
}

func validatePress(bootstrap *Bootstrap) error {
	if bootstrap.LoadTest == nil {
		return fmt.Errorf("validate config: loadTest is required")
	}
	press := bootstrap.LoadTest.Press
	if press.URL == "" {
		return fmt.Errorf("validate config: loadTest.press.url is required")
	}
	if press.Num <= 0 || len(press.Batch) != 2 || press.Batch[0] <= 0 || press.Batch[1] < press.Batch[0] {
		return fmt.Errorf("validate config: loadTest.press num and batch are invalid")
	}
	if press.Interval <= 0 || press.StartID <= 0 {
		return fmt.Errorf("validate config: loadTest.press interval and startID must be positive")
	}
	if press.LogoutRate < 0 || press.LogoutRate > 1 || press.OfflineRate < 0 || press.OfflineRate > 1 {
		return fmt.Errorf("validate config: loadTest.press rates must be within [0,1]")
	}
	return nil
}
