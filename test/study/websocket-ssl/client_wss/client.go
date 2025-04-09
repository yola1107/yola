package main

import (
    "fmt"
    "log"
    "os"
    "os/signal"
    "time"

    "github.com/gorilla/websocket"
)

//const serverURL = "ws://192.168.1.101:8000/ws" // 这里是 Nginx 负载均衡的地址
const serverURL = "wss://test.yola.com/ws" // 这里是 Nginx 负载均衡的地址

// 连接 WebSocket 服务器并处理消息
func connectToWebSocket() (*websocket.Conn, error) {

    //// 读取 CA 证书
    //caCert, err := ioutil.ReadFile("server.crt") // 服务器证书
    //if err != nil {
    //    log.Fatalf("Failed to read CA cert: %v", err)
    //}
    //
    //// 创建证书池并将 CA 证书添加到池中
    //certPool := x509.NewCertPool()
    //if ok := certPool.AppendCertsFromPEM(caCert); !ok {
    //    log.Fatalf("Failed to append cert to cert pool")
    //}
    //
    //// 创建自定义的 http.Transport 和 tls.Config
    //tlsConfig := &tls.Config{
    //    RootCAs: certPool,
    //}
    //tlsConfig.BuildNameToCertificate()
    //
    //// 创建自定义的 Dialer
    //dialer := websocket.Dialer{
    //    TLSClientConfig: tlsConfig,
    //}
    //
    //// 创建 WebSocket 连接
    //conn, _, err := dialer.Dial(serverURL, nil)

    conn, _, err := websocket.DefaultDialer.Dial(serverURL, nil)
    if err != nil {
        return nil, fmt.Errorf("error connecting to WebSocket server: %v", err)
    }

    // 打印连接成功
    fmt.Println("Connected to WebSocket server at", serverURL)
    return conn, nil
}

// 发送消息
func sendMessage(conn *websocket.Conn, message string) error {
    err := conn.WriteMessage(websocket.TextMessage, []byte(message))
    if err != nil {
        return fmt.Errorf("error sending message: %v", err)
    }
    fmt.Println("Sent message:", message)
    return nil
}

// 接收消息
func receiveMessage(conn *websocket.Conn) {
    for {
        // 读取消息
        messageType, msg, err := conn.ReadMessage()
        if err != nil {
            log.Printf("Error reading message: %v", err)
            return
        }

        // 打印接收到的消息
        if messageType == websocket.TextMessage {
            fmt.Printf("Received message: %s\n", msg)
        }
    }
}

func main() {
    // 连接 WebSocket 服务器
    conn, err := connectToWebSocket()
    if err != nil {
        log.Fatalf("Failed to connect to WebSocket server: %v", err)
    }
    defer conn.Close()

    // 启动一个 goroutine 来接收消息
    go receiveMessage(conn)

    go func() {
        // 在主 goroutine 中发送消息
        for i := 0; i < 50; i++ {
            message := fmt.Sprintf("Hello from client %d", i)
            if err := sendMessage(conn, message); err != nil {
                log.Println("Error sending message:", err)
            }

            // 每秒发送一条消息
            time.Sleep(1 * time.Second)
        }
    }()

    // 创建一个捕获信号的通道，以便优雅地退出
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt)
    // 等待程序结束的信号
    <-sigChan
    fmt.Println("Received interrupt signal, closing connection...")
}
