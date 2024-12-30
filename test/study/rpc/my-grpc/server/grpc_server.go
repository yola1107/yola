package main

import (
    "context"
    "fmt"
    "log"
    "net"

    pb "yola/test/study/rpc/my-grpc/api"

    "google.golang.org/grpc"
)

type service int

func (c *service) SayHello(ctx context.Context, req *pb.HelloReq) (*pb.HelloRsp, error) {
    rsp := &pb.HelloRsp{
        Id:  req.Id,
        Msg: "say hello",
    }

    return rsp, nil
}

func main() {
    l, err := net.Listen("tcp", ":9999")
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println("rpc服务器开始监听：", ":9999")
    s := grpc.NewServer()
    pb.RegisterHelloServer(s, new(service))
    log.Fatal(s.Serve(l))
}
