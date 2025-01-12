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
    // 配置 TLS 以跳过证书验证（仅用于开发和测试环境）
    tlsConfig := &tls.Config{
        InsecureSkipVerify: true, // 跳过证书验证
    }

    // 创建一个自定义的 WebSocket Dialer，使用上面的 TLS 配置
    dialer := websocket.Dialer{
        TLSClientConfig: tlsConfig,
    }

    // 连接到 WebSocket 服务器
    url := "wss://test.yola.com/ws" // 使用 HTTPS 协议 (wss://)
    fmt.Println("Connecting to WebSocket server at", url)

    // 使用自定义 Dialer 连接到 WebSocket 服务器
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
            var message string = "hello , this is from wss client."
            fmt.Print("Enter message to send: ")
            _, err := fmt.Scanln(&message)
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
