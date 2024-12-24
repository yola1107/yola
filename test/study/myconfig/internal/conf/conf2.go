package utils

import (
    "io/ioutil"
    "sync"

    "gopkg.in/yaml.v3"
)

type File struct {
    Path string `yaml:"path"`
}

// LoadFileConfig 读取yaml配置文件
func LoadFileConfig(configPath, configDir, configName string) error {
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

    go Watch(configPath, configDir, configName)
    return nil
}
