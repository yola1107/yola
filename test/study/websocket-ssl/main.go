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
        return true // 允许所有来源
    },
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
    // 升级 HTTP 连接为 WebSocket 连接
    conn, err := upgrader.Upgrade(w, r, nil)
    if err != nil {
        log.Println("Error upgrading connection:", err)
        return
    }
    defer conn.Close()

    // 持续监听来自客户端的消息
    for {
        messageType, p, err := conn.ReadMessage()
        if err != nil {
            log.Println("Error reading message:", err)
            return
        }
        // 回传消息给客户端
        err = conn.WriteMessage(messageType, p)
        if err != nil {
            log.Println("Error writing message:", err)
            return
        }
    }
}

func main() {

    var port = flag.Int64("p", 8000, "specify the client/server host address.\n\tUsage: -p 8000")

    flag.Parse()

    http.HandleFunc("/ws", handleConnections)

    // 启动 WebSocket 服务
    server := &http.Server{
        Addr: fmt.Sprintf("localhost:%d", *port),
    }

    log.Printf("WebSocket server started on ws://localhost:%d\n", *port)
    if err := server.ListenAndServe(); err != nil {
        log.Fatal("Error starting server:", err)
    }
}
