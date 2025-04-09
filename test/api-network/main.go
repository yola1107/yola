package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
)

var req = `{
    "channel_name": "810001",
    "gaid": "bcd88274-e2c2-45c8-af4b-ffeb6af5a280",
    "player_id": "3202196",
    "event_type": 0,
    "extra_params": {
        "order_no": "R202101011234",
        "order_value":"200",
        "is_first_purchase": 0,
        "client_ip_address": "113.90.237.236",
        "client_user_agent": "Mozilla/5.0 (Linux;Android 10; CPH2185 Build/QP1A.190711.020",
        "email": "vrzajamshed81@dasgupta.in",
        "phone": "7929250034"
    }
}`

func main() {

	url := "https://api.33tp.in/api/v6/event/upload"
	method := "POST"

	payload := strings.NewReader(req)

	client := &http.Client{}
	req, err := http.NewRequest(method, url, payload)

	if err != nil {
		fmt.Println(err)
		return
	}
	req.Header.Add("Content-Type", "application/json")

	res, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer res.Body.Close()

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(string(body))
}
