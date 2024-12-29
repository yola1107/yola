#!/bin/bash

# 查找并杀掉以 "app" 开头的进程
kill_app() {
    pids=$(pgrep -f "app")

    if [ -n "$pids" ]; then
        echo "$(date +'%Y-%m-%d %H:%M:%S') - Found app process(es): $pids. Killing them..."
        # 强制杀死所有匹配的进程
        echo "$pids" | xargs kill -9

        # 再次确认进程是否已被杀死
        sleep 2
        pids_after_kill=$(pgrep -f "app")
        if [ -z "$pids_after_kill" ]; then
            echo "$(date +'%Y-%m-%d %H:%M:%S') - All app processes killed successfully."
        else
            echo "$(date +'%Y-%m-%d %H:%M:%S') - Some app processes could not be killed."
        fi
    else
        echo "$(date +'%Y-%m-%d %H:%M:%S') - No app processes found to kill."
    fi
}

# 启动 app 进程
start_app() {
    cd /home/bin || { echo "$(date +'%Y-%m-%d %H:%M:%S') - Error: Cannot change to /home/bin directory!"; exit 1; }

    local name="app"
    if [ ! -r ./$name ]; then
        echo "$(date +'%Y-%m-%d %H:%M:%S') - Error: $name file is missing or not readable!"
        exit 1
    fi

    # 删除旧的 "App" 文件或目录
    if [ -e "App" ]; then
        rm -rf "App"
        echo "$(date +'%Y-%m-%d %H:%M:%S') - Deleted existing 'App'."
    fi

    # 将 "app" 重命名为 "App"
    mv $name "App" || { echo "$(date +'%Y-%m-%d %H:%M:%S') - Error renaming $name to App."; exit 1; }

    # 更新变量值，确保变量 name 现在指向 "App"
    name="App"

    # 赋予执行权限
    chmod +x /home/bin/$name || { echo "$(date +'%Y-%m-%d %H:%M:%S') - Error setting execute permission on $name."; exit 1; }

    # 启动两个端口
    echo "$(date +'%Y-%m-%d %H:%M:%S') - Starting app on port 8000..."
    ./$name -p 8000 &
    pid1=$!
    echo "$(date +'%Y-%m-%d %H:%M:%S') - Starting app on port 8001..."
    ./$name -p 8001 &
    pid2=$!

    # 等待进程启动
    sleep 2

    # 检查是否启动成功
    check_port 8000
    check_port 8001

    # 打印当前正在运行的 app 进程
    echo "$(date +'%Y-%m-%d %H:%M:%S') - Current running app processes:"
    ps -ef | grep "app" | grep -v "grep"
}

# 检查端口是否成功绑定
check_port() {
    port=$1
    if ss -tuln | grep -q ":$port "; then
        echo "$(date +'%Y-%m-%d %H:%M:%S') - app on port $port started successfully."
    else
        echo "$(date +'%Y-%m-%d %H:%M:%S') - Failed to start app on port $port."
    fi
}

# 主函数
main() {
    kill_app
    start_app
}

# 执行主函数
main
