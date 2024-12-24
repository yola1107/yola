package main

import (
	"fmt"

	"yola/test/study/myconfig2/internal/conf"
)

func main() {
	configPath := "/Users/apple/workplace/file-store/config"
	// 配置初始化
	err := conf.InitConfig(configPath, "config", "yaml")
	if err != nil {
		fmt.Printf("Failed to init config, err is %s\n", err)
	}
	// 获取全局配置
	c := conf.GetConfig()
	fmt.Println(c.File.Path)
	//// 数据库操作
	//models.MysqlInit(conf.Mysql.Drive, conf.Mysql.Address)
	//// 监听端口
	//err = http.ListenAndServe("127.0.0.1:8000", nil)
	//if err != nil {
	//    fmt.Printf("Failed to start server, err %s", err.Error())
	//}
}
