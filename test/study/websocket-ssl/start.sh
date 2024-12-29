#!/bin/bash

kill_app() {
    pids=$(pgrep -f "app_server")

    if [ -n "$pids" ]; then
        echo "$(date +'%Y-%m-%d %H:%M:%S') - Found app process(es): $pids. Killing them..."
        # 强制杀死所有匹配的进程
        echo "$pids" | xargs kill -9
    else
        echo "$(date +'%Y-%m-%d %H:%M:%S') - No app processes found to kill."
    fi
}

# 启动 app 进程
start_app() {
    cd /home/bin

    local name="app"
    if [ ! -r ./$name ]; then
       echo "缺少 $name 文件"
        exit 1
    fi

    # 删除旧的 "app_server" 文件或目录
    if [ -e "app_server" ]; then
        rm -rf "app_server"
    fi

    # 将 "app" 重命名为 "app_server"
    mv $name "app_server"

    # 更新变量值，确保变量 name 现在指向 "app_server"
    name="app_server"

    # 赋予执行权限
    chmod +x /home/bin/$name

    # 启动多个端口
    declare -a ports=(8000 8001)
    declare -a pids

    for port in "${ports[@]}"; do
        echo "$(date +'%Y-%m-%d %H:%M:%S') - Starting app on port $port..."
        ./$name -p $port &
        pids[$port]=$!
    done

    # 打印当前正在运行的 app 进程
    ps -ef | grep "app_server"
}

# 主函数
main() {
    kill_app
    start_app
}

# 执行主函数
main
