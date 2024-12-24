package utils

import (
    "fmt"
    "io/ioutil"
    "log"
    "sync"

    "github.com/fsnotify/fsnotify"
    "gopkg.in/yaml.v3"
)

var Conf *Config

type Config struct {
    File    *File    `yaml:"file"`
    GameCfg *GameCfg `json:"game_cfg"`
}

// LoadConfig 读取yaml配置文件
func LoadConfig(configPath, configDir, configName string) error {
    var locker = new(sync.RWMutex)
    yamlFile, err := ioutil.ReadFile(configPath)
    if err != nil {
        panic(err)
    }
    locker.Lock()
    if err1 := yaml.Unmarshal(yamlFile, &Conf); err1 != nil {
        panic(err)
    }
    locker.Unlock()
    //fmt.Println(Conf)

    //go Watch(configPath, configDir, configName)
    return nil
}

func Watch(configPath, configDir, configName string) {
    go func() {
        watcher, err := fsnotify.NewWatcher()
        if err != nil {
            log.Fatal(err)
        }
        defer watcher.Close()

        go func() {
            for {
                select {
                case event := <-watcher.Events:
                    if event.Op&fsnotify.Write == fsnotify.Write {
                        _ = LoadFileConfig(configPath, configDir, "config")
                        fmt.Printf("更新配置为：%+v\n", Conf.File)
                    }
                case err := <-watcher.Errors:
                    log.Println("error:", err)
                }
            }
        }()
        // 监控文件
        if err = watcher.Add(configPath); err != nil {
            log.Fatal(err)
        }
        //// 监控文件夹
        //if err = watcher.Add(configDir); err != nil {
        //    log.Fatal(err)
        //}

        <-make(chan struct{})
    }()
}
