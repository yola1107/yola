package main

import (
	"context"
	"fmt"
	"time"
)

func dosomething(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			fmt.Println("playing")
			return
		default:
			fmt.Println("I am working!")
			time.Sleep(time.Second)
		}
	}
}

func main() {
	ctx, cancelFunc := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Second)
		cancelFunc()
	}()
	dosomething(ctx)
}
