package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"time"
)

//https://fcapi.tikurl.com/check_report/bet.php
// req :     uid:{1,2,3,4,5}
// rsp :     {code,msg,rtp}

//func main() {
//
//	url := "https://fcapi.tikurl.com/check_report/bet.php"
//
//	// 创建一个 HTTP 请求
//	resp, err := http.Get(url)
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer resp.Body.Close()
//
//	// 读取响应数据
//	body, err := ioutil.ReadAll(resp.Body)
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	// 输出响应内容
//	fmt.Println(string(body))
//}

type Result struct {
	Code int32   `json:"code"`
	Msg  string  `json:"msg"`
	RTP  float64 `json:"rtp"`
}

func main2() {
	// 定义POST请求的URL和请求体
	url := "https://fcapi.tikurl.com/check_report/bet.php"

	uid := []int64{1001, 1002, 1003}
	b, err := json.Marshal(uid)
	if err != nil {
		log.Fatalln(err)
	}

	// 创建HTTP POST请求
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(b))
	if err != nil {
		log.Fatal(err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")

	// 发送请求并获取响应
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	// 读取响应内容
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}

	ret := Result{}
	err = json.Unmarshal(body, &ret)
	if err != nil {
		log.Fatalln(err)
	}
	// 输出响应状态和内容
	fmt.Println("Response Status:", resp.Status)
	fmt.Println("Response Body:", string(body))

	fmt.Println("ret=", ret)
}

func main() {
	uid := []int64{1001, 1002, 1003}
	rtp, err := GetHttpRequestRTP(uid)
	if err != nil {
		log.Fatalln(err)
	}
	fmt.Println(rtp)
}

func GetHttpRequestRTP(ids []int64) (float64, error) {

	url := "https://fcapi.tikurl.com/check_report/bet.php"

	b, err := json.Marshal(ids)
	if err != nil {
		return 0, err
	}

	// 创建HTTP POST请求
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(b))
	if err != nil {
		return 0, err
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")

	// 发送请求并获取响应
	client := &http.Client{
		Timeout: 1 * time.Second, //10m超时
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	// 读取响应内容
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	ret := Result{}
	if err = json.Unmarshal(body, &ret); err != nil {
		return 0, err
	}
	// 输出响应状态和内容
	fmt.Println("Response Status:", resp.Status)
	fmt.Println("Response Body:", string(body))

	fmt.Printf("code=%d msg=%s RTP:%.2f \n", ret.Code, ret.Msg, ret.RTP)
	return ret.RTP, nil
}
