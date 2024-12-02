package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"

	"github.com/spf13/pflag"
)

type RequestData struct {
	SSID     string `json:"ssid"`
	Security int    `json:"security"`
	Channel  int    `json:"channel"`
	SSID2    string `json:"ssid2"`
	Key2     string `json:"key2"`
	SSID3    string `json:"ssid3"`
	Key3     string `json:"key3"`
	Key      string `json:"key"`
	IPType   int    `json:"iptype"`
	IP       string `json:"ip"`
	IPGW     string `json:"ipgw"`
	IPMASK   string `json:"ipmask"`
	IPDNS1   string `json:"ipdns1"`
	IPDNS2   string `json:"ipdns2"`
}

func main() {
	// 定义命令行参数
	var birdIp string
	var ssid string
	var passwd string
	var ip string
	var ipgw string
	var ipmask string
	var ipdns1 string
	var ipdns2 string

	pflag.StringVarP(&birdIp, "bird_ip", "", "192.168.10.1", "memobird ip")
	pflag.StringVarP(&ssid, "ssid", "", "", "wifi ssid")
	pflag.StringVarP(&passwd, "passwd", "", "", "wifi password")
	pflag.StringVarP(&ip, "ip", "", "", "ip")
	pflag.StringVarP(&ipgw, "ipgw", "", "", "gateway")
	pflag.StringVarP(&ipmask, "ipmask", "", "", "mask")
	pflag.StringVarP(&ipdns1, "ipdns1", "", "", "dns1")
	pflag.StringVarP(&ipdns2, "ipdns2", "", "0.0.0.0", "dns2")
	pflag.Parse()

	if birdIp == "" || ssid == "" || passwd == "" || ip == "" || ipgw == "" || ipmask == "" || ipdns1 == "" || ipdns2 == "" {
		pflag.Usage()
		os.Exit(1)
	}

	data := RequestData{
		SSID:     ssid,
		Security: 7,
		Channel:  0,
		// 未来应该实现传递UTF8格式?
		SSID2: base64.StdEncoding.EncodeToString([]byte(ssid)),
		Key2:  base64.StdEncoding.EncodeToString([]byte(passwd)),
		// 未来应该实现传递GBK格式?
		SSID3:  base64.StdEncoding.EncodeToString([]byte(ssid)),
		Key3:   base64.StdEncoding.EncodeToString([]byte(passwd)),
		Key:    passwd,
		IPType: 0,
		IP:     ip,
		IPGW:   ipgw,
		IPMASK: ipmask,
		IPDNS1: ipdns1,
		IPDNS2: ipdns2,
	}
	jsonData, err := json.Marshal(data)
	if err != nil {
		log.Fatalf("Error marshalling JSON: %v\n", err)
	}
	log.Printf("Hijack data: %s\n", string(jsonData))

	resp, err := http.Post(fmt.Sprintf("http://%s/sys/network"), "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Fatalf("Error sending hijack request: %v\n", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("Error reading hijack response body: %v\n", err)
	}

	if resp.StatusCode == 200 || resp.StatusCode == 204 {
		log.Printf("Hijack Success! Status code: %d\nResponse body: %s\n", resp.StatusCode, string(body))
	} else {
		log.Printf("Hijack Failed! Status code: %d\nResponse body: %s\n", resp.StatusCode, string(body))
	}
}
