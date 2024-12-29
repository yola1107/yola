// client.go
package main

import (
    "crypto/tls"
    "fmt"
    "log"
    "os"
    "os/signal"

    "github.com/gorilla/websocket"
)

func main() {
    // 连接到 WebSocket 服务器
    url := "wss://test.yola.com/ws"
    fmt.Println("Connecting to WebSocket server at", url)

    // 忽略证书验证（仅限开发使用）
    dialer := websocket.DefaultDialer
    dialer.TLSClientConfig = &tls.Config{
        InsecureSkipVerify: true, // 跳过证书验证
    }

    // 创建 WebSocket 连接
    conn, _, err := dialer.Dial(url, nil)
    if err != nil {
        log.Fatal("Failed to connect to WebSocket:", err)
    }
    defer conn.Close()

    // 设置接收消息的 goroutine
    go func() {
        for {
            // 读取消息
            _, message, err := conn.ReadMessage()
            if err != nil {
                log.Println("Error reading message:", err)
                break
            }
            // 打印收到的消息
            fmt.Printf("Received: %s\n", message)
        }
    }()

    // 设置消息发送功能
    go func() {
        for {
            var message = "hello , this is from wss client."
            if err != nil {
                log.Println("Error reading input:", err)
                return
            }

            // 发送消息
            if err := conn.WriteMessage(websocket.TextMessage, []byte(message)); err != nil {
                log.Println("Error sending message:", err)
                return
            }
            fmt.Println("Message sent:", message)
        }
    }()

    // 等待中断信号以便优雅关闭客户端
    sig := make(chan os.Signal, 1)
    signal.Notify(sig, os.Interrupt)
    <-sig

    fmt.Println("Shutting down WebSocket client...")
}
