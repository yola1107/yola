package main

import (
	"net/http"

	"github.com/go-kratos/kratos/pkg/log"
	"github.com/gorilla/websocket"
)

func main() {
	http.HandleFunc("/ws", wsHandler)
	_ = http.ListenAndServe(":8000", nil)
}

func wsHandler(w http.ResponseWriter, r *http.Request) {
	var (
		err  error
		data []byte
		conn *websocket.Conn
	)

	conn, err = websocket.Upgrade(w, r, nil, 1024, 1024)
	if err != nil {
		log.Error("upgrade err, %+v", err)
		return
	}

	for {
		if _, data, err = conn.ReadMessage(); err != nil {
			_ = conn.Close()
			log.Error("conn.ReadMessage() err, %+v", err)
			return
		}
		if err = conn.WriteMessage(websocket.TextMessage, data); err != nil {
			_ = conn.Close()
			log.Error("conn.WriteMessage() err, %+v", err)
			return
		}
	}
}
