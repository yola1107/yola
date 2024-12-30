package main

import (
    "fmt"
    "log"
    "net"
    "net/http"
    "net/rpc"

    pb "yola/test/study/rpc/my-rpc/api"
)

type Service int

func (s *Service) Calc(req pb.HelloRequest, res *pb.HelloResponse) error {
    res.Mul = req.A * req.B
    res.Add = req.A + req.B
    res.Sub = req.A - req.B
    return nil
}

func main() {
    rpc.Register(new(Service)) // 注册rpc服务
    rpc.HandleHTTP()           // 采用http协议作为rpc载体

    lis, err := net.Listen("tcp", ":9990")
    if err != nil {
        log.Fatalln("fatal error: ", err)
    }
    fmt.Printf("start Listen at: %s\n", lis.Addr())
    http.Serve(lis, nil)
}
