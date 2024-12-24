package conf

import (
	"fmt"

	"github.com/spf13/viper"
)

// 全局配置
var config = new(Config)

type Config struct {
	File  *File  `yaml:"file"`
	Mysql *Mysql `yaml:"mysql"`
	Token *Token `yaml:"token"`
}

type File struct {
	Path string `yaml:"path"`
}

type Mysql struct {
	Drive   string `yaml:"drive"`
	Address string `yaml:"address"`
}

type Token struct {
	Salt  string `yaml:"salt"`
	Issue string `yaml:"issue"`
}

// 获取全局配置
func GetConfig() *Config {
	return config
}

// InitConfig 读取yaml配置文件
func InitConfig(configPath, configName, configType string) error {
	viper.SetConfigName(configName) // 配置文件名
	viper.SetConfigType(configType) // 配置文件类型，例如:toml、yaml等
	viper.AddConfigPath(configPath) // 查找配置文件所在的路径，多次调用可以添加多个配置文件搜索的目录
	// 读取配置文件配置，并处理错误
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			return err
		}
	}
	// 监控配置文件变化
	viper.WatchConfig()
	_ = viper.Unmarshal(config)
	if err := validateConfig(config); err != nil {
		return err
	}
	return nil
}

// validateConfig：校验配置信息
func validateConfig(conf *Config) error {
	var (
		file    = conf.File.Path
		drive   = conf.Mysql.Drive
		address = conf.Mysql.Address
		salt    = conf.Token.Salt
		issue   = conf.Token.Issue
	)
	if file == "" {
		return fmt.Errorf("invalid file path: %s\n", file)
	}
	if drive == "" {
		return fmt.Errorf("invalid drive: %s\n", drive)
	}
	if address == "" {
		return fmt.Errorf("invalid address: %s\n", address)
	}
	if salt == "" {
		return fmt.Errorf("invalid salt: %s\n", salt)
	}
	if issue == "" {
		return fmt.Errorf("invalid issue: %s\n", issue)
	}
	return nil
}
