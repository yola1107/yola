package main

import (
	"fmt"

	conf "yola/test/study/myconfig/internal/conf"
)

func main() {
	configPath := "D:\\asrc\\gitee.com\\yola\\test\\myconfig\\configs\\config.yaml"
	configDir := "D:\\asrc\\gitee.com\\yola\\test\\myconfig\\configs"

	// 加载配置文件
	_ = conf.LoadConfig(configPath, configDir, "config")
	conf.Watch(configPath, configDir, "config")
	fmt.Printf("配置为：%+v\n", conf.Conf.File.Path)

	select {}
}
