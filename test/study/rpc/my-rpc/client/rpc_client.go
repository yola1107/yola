package main

import (
    "fmt"
    "log"
    "net/rpc"

    pb "yola/test/study/rpc/my-rpc/api"
)

func main() {
    conn, err := rpc.DialHTTP("tcp", ":9990")
    if err != nil {
        log.Fatalln("DialHTTP error: ", err)
    }

    req := pb.HelloRequest{A: 10, B: 5}
    var res pb.HelloResponse

    err = conn.Call("Hello.Calc", req, &res)
    if err != nil {
        log.Fatalln("Hello.Calc error: ", err)
    }
    fmt.Printf("req:%+v,rep:%+v", req, res)
}
