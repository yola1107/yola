package main

import (
    "flag"
    "fmt"
    "log"
    "net/http"

    "github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
    CheckOrigin: func(r *http.Request) bool {
        return true // 放宽 Origin 检查，生产环境中需要更严格检查
    },
}

var clients = make(map[*websocket.Conn]bool) // 连接池
var broadcast = make(chan string)            // 用于广播消息的 channel

// 处理 WebSocket 连接
func handleConnections(w http.ResponseWriter, r *http.Request) {
    // 升级 HTTP 请求为 WebSocket 协议
    conn, err := upgrader.Upgrade(w, r, nil)
    if err != nil {
        log.Println(err)
        return
    }
    defer conn.Close()

    // 将新连接添加到连接池
    clients[conn] = true
    fmt.Println("New client connected")

    // 处理来自客户端的消息
    for {
        messageType, p, err := conn.ReadMessage()
        if err != nil {
            log.Println(err)
            delete(clients, conn)
            break
        }

        // 打印接收到的消息
        if messageType == websocket.TextMessage {
            fmt.Printf("Received message: %s\n", string(p))
        }

        // 向所有客户端广播消息
        for client := range clients {
            err := client.WriteMessage(messageType, p)
            if err != nil {
                log.Println(err)
                client.Close()
                delete(clients, client)
            }
        }
    }
}

func main() {

    var port = flag.Int64("p", 8000, "specify the client/server host address.\n\tUsage: -p 8000")

    flag.Parse()

    // 启动一个新的 goroutine 来监听广播消息
    go func() {
        for {
            msg := <-broadcast
            for client := range clients {
                err := client.WriteMessage(websocket.TextMessage, []byte(msg))
                if err != nil {
                    log.Println(err)
                    client.Close()
                    delete(clients, client)
                }
            }
        }
    }()

    // 设置路由
    http.HandleFunc("/ws", handleConnections)

    // 启动 WebSocket 服务器

    fmt.Printf("WebSocket server started on port %+v\n", *port)
    log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), nil))
}
